package ini

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// SystemdSpec returns every value of Key in Section of a systemd unit or a
// Podman Quadlet file, read in the systemd dialect with the drop-ins the
// repository holds for it applied in systemd's order. With Variable, Key
// is Environment, and the values are those its assignments give the
// variable. A file whose name is no unit's is an environment file, as
// EnvironmentFile= names one, read for Variable alone.
type SystemdSpec struct {
	core.OneFile
	Section  string `json:"section,omitempty"`
	Key      string `json:"key,omitempty"`
	Variable string `json:"variable,omitempty"`
}

// systemdKey is the kind's key, which SystemdKind and BuildSystemd both
// use: an env reader's Read calls the Build, and Build reading the Kind
// would make an initialization cycle.
const systemdKey = "systemd"

// SystemdKind is the systemd kind, which a rule writes under the key
// systemd. It is an env reader, after jenkins.
var SystemdKind = core.Kind[SystemdSpec]{Key: systemdKey, Place: PlaceSystemd, Resolve: ResolveSystemd, Describe: DescribeSystemd,
	Env: &check.EnvChoice{Order: 60, Chooses: systemdChooses}, EnvRead: systemdEnvRead}

// envSections are the sections whose Environment= sets a variable of the
// process a unit starts, by the unit's type: the sections of systemd.exec,
// and Quadlet's [Container], whose Environment= is the container's.
var envSections = map[string][]string{
	"service": {"Service"}, "socket": {"Socket"}, "mount": {"Mount"}, "swap": {"Swap"},
	"container": {"Container"},
}

// systemdChooses takes, for the env kind, a unit whose type starts a
// process with an environment: .service, .socket, .mount and .swap, and
// Quadlet's .container. An environment file has a name of any shape, so
// env reads one with this reader only when readers: gives it.
func systemdChooses(file string) bool {
	t, _ := unitType(path.Base(core.Untemplated(file)))
	return envSections[t] != nil
}

// systemdEnvRead is the env kind's reader of the variable name in file:
// the Environment= of every section envSections gives the unit's type, each
// value naming its section, or an environment file's assignments.
func systemdEnvRead(file, name string, maxBytes int64, _ core.Parsers) (check.Extractor, error) {
	t, _ := unitType(path.Base(core.Untemplated(file)))
	sections := envSections[t]
	if sections == nil {
		return BuildSystemd(SystemdSpec{OneFile: core.OneFile{File: file, MaxBytes: maxBytes}, Variable: name})
	}
	x, err := BuildSystemd(SystemdSpec{OneFile: core.OneFile{File: file, MaxBytes: maxBytes}, Section: sections[0], Key: "Environment", Variable: name})
	if err != nil {
		return nil, err
	}
	u := x.(unitExtractor)
	u.sections, u.origin = sections, true
	return u, nil
}

// PlaceSystemd is s with only what decides where it reads: the file
// cleaned, and no max_bytes.
func PlaceSystemd(s SystemdSpec) SystemdSpec {
	s.OneFile = s.Place()
	return s
}

// ResolveSystemd is s with its max_bytes written out, for errata list.
func ResolveSystemd(s SystemdSpec) SystemdSpec {
	s.OneFile = s.Resolved()
	return s
}

// DescribeSystemd is what the kind's extractor returns: flat values, some
// not literal for a unit, since systemd expands a specifier such as %i,
// and for an environment file only when it is a template.
func DescribeSystemd(s SystemdSpec) check.Description {
	notLiteral := unitClassOf(s.File) != notUnit || core.TemplateNotLiteral(s.File, nil) != nil
	return check.Description{Label: "systemd", NotLiteral: notLiteral}
}

// BuildSystemd checks s and returns its extractor: of a unit, a Quadlet
// file or a drop-in by the file's name, or else of an environment file.
func BuildSystemd(s SystemdSpec) (check.Extractor, error) {
	src, err := s.Check(systemdKey)
	if err != nil {
		return nil, err
	}
	if s.Variable != "" && !envName([]byte(s.Variable)) {
		return nil, fmt.Errorf("systemd: variable %s is no name systemd sets: write a letter or _, then letters, digits and _", core.Quote(s.Variable))
	}
	class := unitClassOf(s.File)
	if class == notUnit {
		return buildEnvironmentFile(s, src)
	}
	if err := checkUnitSpec(s); err != nil {
		return nil, err
	}
	_, base := core.FileParts(core.Untemplated(s.File))
	return unitExtractor{Source: src, sections: []string{s.Section}, key: s.Key, variable: s.Variable,
		class: class, base: base}, nil
}

// buildEnvironmentFile is the extractor of a file whose name is no unit's,
// read as an environment file for the variable alone.
func buildEnvironmentFile(s SystemdSpec, src core.Source) (check.Extractor, error) {
	const notUnitFile = "systemd: %s is no unit file by its name, which ends in none of the unit types systemd and Quadlet read, so it is read as an environment file"
	switch {
	case s.Section != "" || s.Key != "":
		return nil, fmt.Errorf(notUnitFile+", which has no section or key; write variable alone", core.Quote(s.File))
	case s.Variable == "":
		return nil, fmt.Errorf(notUnitFile+": variable is required", core.Quote(s.File))
	}
	return core.EntryExtractor{Source: src, Query: core.Query{Key: []byte(s.Variable), HasKey: true},
		Read: core.TemplateRead(s.File, readEnvironmentFile), NotLiteral: core.TemplateNotLiteral(s.File, nil)}, nil
}

// checkUnitSpec refuses a spec of a unit with no section or key, or with
// a variable of a key other than Environment.
func checkUnitSpec(s SystemdSpec) error {
	switch {
	case s.Section == "":
		return errors.New("systemd: section is required: systemd ignores a key before the first section header")
	case s.Key == "":
		return errors.New("systemd: key is required")
	case s.Variable != "" && s.Key != "Environment":
		return fmt.Errorf("systemd: variable reads the assignments of Environment=, and key is %s; set key: Environment", core.Quote(s.Key))
	}
	return nil
}

// unitExtractor reads a unit file and its drop-ins.
type unitExtractor struct {
	core.Source
	sections []string // the sections read, the spec's one but for env's
	key      string
	variable string
	// class is what the file is: a drop-in named itself is read alone, and
	// a Quadlet file and its drop-ins with Podman's grammar.
	class  unitClass
	base   string // the unit's name, which its drop-in directories are named by
	origin bool   // each value names its section, for env
	// valueLimit is the most values the extractor returns, 0 for
	// core.MaxValues: a test sets it to witness the limit with a small file.
	valueLimit int
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (x unitExtractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return x.Extract(ctx, root)
}

// Extract returns the values the unit and its drop-ins give the key, in
// the order systemd reads them, with a reset in a later file dropping the
// values before it.
func (x unitExtractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	vs, _, err := x.read(ctx, root, core.NewTracked(ctx, root), x.Limit)
	return vs, err
}

// held is a value and the section it was set in, which a reset in that
// section drops.
type held struct {
	check.Value
	section int
}

// read is Extract with the bytes the files may still hold, which it
// returns less what it read.
func (x unitExtractor) read(ctx context.Context, root string, index *core.Tracked, left int64) ([]check.Value, int64, error) {
	out, left, err := x.readValues(ctx, root, index, left)
	if err == nil && x.class == quadletUnit && x.key == "Image" && x.variable == "" {
		return x.quadletImage(ctx, root, index, out, left)
	}
	return out, left, err
}

// readValues is read without Quadlet's reading of Image=.
func (x unitExtractor) readValues(ctx context.Context, root string, index *core.Tracked, left int64) ([]check.Value, int64, error) {
	files, err := x.files(index)
	if err != nil {
		return nil, 0, err
	}
	r := unitReading{x: x, envFiles: make([]int, len(x.sections)), unset: make([]unsetWords, len(x.sections))}
	for _, f := range files {
		if left, err = r.read(ctx, root, f, left); err != nil {
			return nil, 0, err
		}
	}
	return r.values(), left, nil
}

// files are the unit's file and the drop-ins it reads, in order.
func (x unitExtractor) files(index *core.Tracked) ([]string, error) {
	if x.class.dropIn() {
		return []string{x.File}, nil
	}
	more, err := x.dropInFiles(index)
	return append([]string{x.File}, more...), err
}

// unitReading is the values one read collects across the unit's files.
type unitReading struct {
	x        unitExtractor
	out      []held
	envFiles []int // the EnvironmentFile= entries of each section no reset dropped
	// unset is what each section's UnsetEnvironment= no reset dropped
	// says of the variable.
	unset  []unsetWords
	words  []byte
	file   string // the file being read
	notLit func(*core.Entry) bool
	m      *core.Meter
	stop   error // the bound a value passed, which ends the read
}

// errBound ends a read whose values passed a bound, which stop names.
var errBound = errors.New("past a bound")

// read reads file, within left bytes, into r, and returns what is left.
func (r *unitReading) read(ctx context.Context, root, file string, left int64) (int64, error) {
	data, err := core.Source{File: file, Limit: left}.Read(ctx, root)
	if errors.Is(err, core.ErrTooLarge) && file != r.x.File {
		return 0, fmt.Errorf("systemd: %w; the unit and its drop-ins hold at most max_bytes, %d, in all", err, r.x.Limit)
	}
	if err != nil {
		return 0, err
	}
	return left - int64(len(data)), r.readData(file, data)
}

// values are the values held. A value of a variable is not literal when
// an EnvironmentFile= of its section is left, but in a Quadlet group
// Podman reads (podmanGroups): systemd reads that file after
// Environment=, and errata cannot read it, so it may set the variable to
// anything, where podman run takes --env over --env-file. What
// UnsetEnvironment= does to the variable is the section's verdict
// (verdict), one lookup for each section.
func (r *unitReading) values() []check.Value {
	last := make([]string, len(r.x.sections))
	for _, h := range r.out {
		last[h.section] = h.Text
	}
	dropped, unknown := r.verdicts(last)
	out := make([]check.Value, 0, len(r.out))
	for _, h := range r.out {
		if dropped[h.section] {
			continue
		}
		h.NotLiteral = h.NotLiteral || unknown[h.section] || r.x.variable != "" && r.envFiles[h.section] > 0 && !podmanGroups[r.x.sections[h.section]]
		out = append(out, h.Value)
	}
	return out
}

// verdicts are verdict's answers for each section, given its last value.
// They charge no work: each hashes one value the read already charged
// for, once for each of the spec's sections, so the pass is linear in it.
func (r *unitReading) verdicts(last []string) (dropped, unknown []bool) {
	dropped, unknown = make([]bool, len(last)), make([]bool, len(last))
	for i := range last {
		dropped[i], unknown[i] = r.verdict(i, last[i])
	}
	return dropped, unknown
}

// unsetWords is what a section's UnsetEnvironment= words say of the
// variable: whether one is its name, the values a NAME=VALUE word names
// for it, and whether one holds a specifier, which errata does not expand
// and may expand to either.
type unsetWords struct {
	name      bool
	values    map[string]bool
	specifier bool
}

// verdict is what UnsetEnvironment= does to the variable of section, whose
// last value is last, asked once for each section, so its lookup hashes
// last once, as strv_env_delete does once strv_env_merge has
// kept the last assignment of each name: its name unsets it; NAME=VALUE
// unsets it when VALUE is its last value, or makes it unknown when an
// EnvironmentFile=, which errata does not read, may set it to another;
// a specifier makes it unknown.
func (r *unitReading) verdict(section int, last string) (dropped, unknown bool) {
	u := r.unset[section]
	switch {
	case u.name:
		return true, false
	case u.values[last] && r.envFiles[section] > 0:
		return false, true
	case u.values[last]:
		return true, false
	}
	return false, u.specifier
}

// readData reads one file of the unit into r. Its meter bounds the
// reader's work as core's entry extractor's does; no file spends it, since
// a line costs less than core.WorkPerByte for each of its bytes, and a
// refusal for work would end the read as a refused line does.
func (r *unitReading) readData(file string, data []byte) error {
	r.file, r.m = file, core.NewMeter(len(data))
	r.notLit = core.TemplateNotLiteral(file, computedEntry(file))
	if r.x.variable != "" {
		// A specifier is marked in the word that holds it, not the line.
		r.notLit = core.TemplateNotLiteral(file, nil)
	}
	read := readSystemd
	if r.x.class.quadlet() {
		read = readQuadlet
	}
	err := core.TemplateRead(file, read)(data, r.m, r.entry)
	switch {
	case r.stop != nil:
		return r.stop
	case err != nil:
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}

// entry takes an entry of the key in one of the sections read: a reset
// drops the section's values, and any other adds its value or, with a
// variable, the variable's values among its assignments. An
// EnvironmentFile= is counted, with a variable.
func (r *unitReading) entry(e *core.Entry) error {
	section, ok, err := r.wanted(e)
	if !ok || err != nil {
		return err
	}
	if e.Kind == core.EntryReset {
		r.out = slices.DeleteFunc(r.out, func(h held) bool { return h.section == section })
		return nil
	}
	v := check.Value{At: check.Location{File: r.file, Line: e.Line}, NotLiteral: r.notLit != nil && r.notLit(e)}
	if r.x.origin {
		v.Origin = "section " + core.Quote(r.x.sections[section])
	}
	if r.x.variable == "" {
		v.Text = string(e.Value)
		return r.add(v, section)
	}
	return r.assignments(e, v, section)
}

// wanted is the index of e's section among those read, and whether e is
// of the key read there. With a variable, an EnvironmentFile= is counted
// and an UnsetEnvironment= kept, and neither is wanted.
func (r *unitReading) wanted(e *core.Entry) (int, bool, error) {
	section := slices.Index(r.x.sections, string(e.Section))
	switch {
	case section < 0:
		return 0, false, nil
	case r.x.variable == "":
	case string(e.Key) == "EnvironmentFile":
		r.envFile(e, section)
		return 0, false, nil
	case string(e.Key) == "UnsetEnvironment" && !podmanGroups[r.x.sections[section]]:
		// Quadlet's groups have no UnsetEnvironment=, which it refuses.
		return 0, false, r.keepUnset(e, section)
	}
	return section, string(e.Key) == r.x.key, nil
}

// keepUnset keeps what the words of an UnsetEnvironment= of section say
// of the variable, as config_parse_unset_environ reads them: a reset drops
// the words before it, a word that is neither a name nor an assignment
// systemd ignores, and a word it cannot read ends the line.
func (r *unitReading) keepUnset(e *core.Entry, section int) error {
	if e.Kind == core.EntryReset {
		r.unset[section] = unsetWords{}
		return nil
	}
	err := environWords(e.Value, nil, r.m, func(word []byte) error {
		r.unset[section].add(word, r.x.variable)
		return nil
	})
	if errors.Is(err, errEnvironSyntax) {
		return nil
	}
	return err
}

// add notes what word says of the variable. systemd checks a word once its
// specifiers are expanded, which errata does not do, so a word with % is
// kept as one that may name it.
func (u *unsetWords) add(word []byte, variable string) {
	name, value, assign := bytes.Cut(word, []byte("="))
	switch {
	case bytes.ContainsRune(name, '%'), string(name) == variable && bytes.ContainsRune(value, '%'):
		// Only a specifier in the name, or in the value of a word that
		// names the variable, can expand to a word that unsets it.
		u.specifier = true
	case string(name) != variable || !envName(name):
	case !assign:
		u.name = true
	case systemdUTF8(value):
		if u.values == nil {
			u.values = map[string]bool{}
		}
		u.values[string(value)] = true
	}
}

// envFile counts an EnvironmentFile= of a section, and a reset of it
// clears the count.
func (r *unitReading) envFile(e *core.Entry, section int) {
	if e.Kind == core.EntryReset {
		r.envFiles[section] = 0
		return
	}
	r.envFiles[section]++
}

// assignments adds the values an Environment= sets the variable to, each
// not literal when its word holds a specifier, which systemd expands once
// the word is unquoted. systemd ignores the rest of a line from a word it
// cannot read, with a warning.
func (r *unitReading) assignments(e *core.Entry, v check.Value, section int) error {
	if r.x.class.quadlet() && podmanGroups[r.x.sections[section]] {
		return r.podmanAssignments(e, v, section)
	}
	err := environWords(e.Value, r.words, r.m, func(word []byte) error {
		r.words = word
		name, value, ok := envAssignment(word)
		if !ok || string(name) != r.x.variable {
			return nil
		}
		w := v
		w.Text, w.NotLiteral = string(value), v.NotLiteral || bytes.ContainsRune(word, '%')
		return r.add(w, section)
	})
	if errors.Is(err, errEnvironSyntax) {
		return nil
	}
	return err
}

// podmanGroups are the Quadlet groups whose Environment= Podman reads
// (LookupAllKeyVal), not systemd: a .container's [Container] and a
// .build's [Build]. Their values reach podman run or podman build in the
// unit's command line, where systemd expands a specifier and a $.
var podmanGroups = map[string]bool{"Container": true, "Build": true}

// podmanAssignments adds the values a Quadlet Environment= sets the
// variable to, split as Podman splits it (podmanWords): a word is NAME=
// value at its first =, with no check of the name or the value, and one
// with % or $ is not literal.
func (r *unitReading) podmanAssignments(e *core.Entry, v check.Value, section int) error {
	return podmanWords(e.Value, r.m, func(word []byte) error {
		name, value, ok := bytes.Cut(word, []byte("="))
		if !ok || string(name) != r.x.variable {
			return nil
		}
		w := v
		w.Text, w.NotLiteral = string(value), v.NotLiteral || bytes.ContainsAny(word, "%$")
		return r.add(w, section)
	})
}

// add holds v, within the extractor's bound on values. Their bytes need
// no bound of their own: a value is a part of the file, or shorter, as an
// escape is, and max_bytes holds the files.
func (r *unitReading) add(v check.Value, section int) error {
	// A value is held, and looked at once more in values: two steps, each
	// charged here, as unsetWords holds UnsetEnvironment='s words in maps.
	if err := r.m.Charge(2 * core.LineWork); err != nil {
		return err
	}
	if len(r.out) == r.x.maxValues() {
		r.stop = core.TooManyValues(r.x.File)
		return errBound
	}
	r.out = append(r.out, held{v, section})
	return nil
}

// maxValues is the most values the extractor returns.
func (x unitExtractor) maxValues() int {
	if x.valueLimit == 0 {
		return core.MaxValues
	}
	return x.valueLimit
}

// globEscaper writes a name as a glob that matches it alone.
var globEscaper = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)

// dropInFiles are the drop-ins of the unit git's index lists beside it, in
// the order systemd applies them (dropInDirs, orderDropIns).
func (x unitExtractor) dropInFiles(index *core.Tracked) ([]string, error) {
	dir := path.Dir(path.Clean(filepath.ToSlash(x.File)))
	found := map[string][]string{}
	dirs, err := dropInDirs(x.base, core.NewMeter(len(x.base)))
	if err != nil {
		return nil, fmt.Errorf("systemd: %s: %w", core.Quote(x.File), err)
	}
	globs := make([]string, len(dirs))
	for i, d := range dirs {
		globs[i] = path.Join(globEscaper.Replace(dir), globEscaper.Replace(d), "*.conf")
	}
	for chunk := range slices.Chunk(globs, core.MaxEntries) {
		files, err := core.CheckFiles(systemdKey, chunk, x.Limit)
		if err != nil {
			return nil, err
		}
		named, err := index.List(files)
		if err != nil {
			return nil, fmt.Errorf("systemd: the drop-ins of %s: %w", core.Quote(x.File), err)
		}
		for _, n := range named {
			d := path.Base(path.Dir(n.File))
			found[d] = append(found[d], n.File)
		}
	}
	return orderDropIns(dirs, func(d string) []string { return found[d] }), nil
}
