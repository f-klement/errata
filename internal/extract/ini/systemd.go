package ini

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// The systemd dialect reads a unit file as systemd 257's config_parse reads
// it (src/shared/conf-parser.c), and a Podman Quadlet file, which Quadlet
// reads with the same rules:
//
//   - a line ends at \n, \r or NUL, and at a run of them in which none
//     repeats, so \r\n and \n\r are each one line end (read_line);
//   - a line whose first character other than a space or a tab is # or ;
//     is a comment, also between the lines of a value a \ continues;
//   - a \ at the end of a line that no \ escapes continues the value on
//     the next line, and stands as a space; an empty line ends it;
//   - [name] is a section header, refused when it does not end in ] or
//     its name holds a quote, a \ or a control character;
//   - key=value at the first =, both sides stripped of spaces and tabs; a
//     line with no =, or with nothing before it, and a key before the first
//     header are ignored, as systemd ignores them with a warning;
//   - a key set to nothing is a reset (core.EntryReset): it drops the
//     values the key collected before it in its section, as systemd
//     empties a list such as ExecStart= or Environment=, and sets any other
//     key back to its default;
//   - a line that is not UTF-8, and a line of 1 MiB or more, are refused,
//     as systemd refuses to load the unit.
//
// A key set more than once gives every value in order; for a key that
// holds one value, systemd uses the last. A value is returned as written,
// and one with a specifier such as %i or %h, or a $ in a key that starts
// with Exec, which systemd expands from the environment, is marked not
// literal. Sections are read whatever their name: systemd ignores the keys
// of a section the unit type does not know, errata returns them.
var systemdDialect = &iniDialect{
	name:       "systemd",
	files:      func(file string) bool { return unitClassOf(file) != notUnit },
	query:      systemdQuery,
	notLiteral: computedEntry,
	read:       readSystemd,
	readFor:    systemdReaderFor,
}

// systemdReaderFor is the reader of file: Podman's grammar for a Quadlet file
// and its drop-ins (readQuadlet), systemd's for any other.
func systemdReaderFor(file string) core.ReadFunc {
	if unitClassOf(file).quadlet() {
		return readQuadlet
	}
	return readSystemd
}

// systemdQuery checks a systemd spec: a section and a key, since systemd
// ignores a key before the first header.
func systemdQuery(i Spec) (core.Query, error) {
	if err := onlyDialect(i, false, false); err != nil {
		return core.Query{}, err
	}
	switch {
	case i.Section == "":
		return core.Query{}, errors.New("section is required: systemd ignores a key before the first section header")
	case i.Key == "":
		return core.Query{}, errors.New("key is required")
	}
	return core.Query{Section: []byte(i.Section), Key: []byte(i.Key), HasKey: true}, nil
}

// unitClass is what a file's name says it is to systemd or Quadlet.
type unitClass uint8

const (
	notUnit unitClass = iota
	systemdUnit
	quadletUnit
	systemdDropIn // a .conf file in a systemd unit's drop-in directory
	quadletDropIn // a .conf file in a Quadlet file's drop-in directory
)

// dropIn reports whether c is a drop-in's class.
func (c unitClass) dropIn() bool { return c == systemdDropIn || c == quadletDropIn }

// quadlet reports whether c is read with Podman's grammar.
func (c unitClass) quadlet() bool { return c == quadletUnit || c == quadletDropIn }

// systemdTypes are the unit types systemd 257 reads from unit files, and
// quadletTypes the files Podman's Quadlet turns into units. A .network
// file is Quadlet's, but for one networkd reads (networkd), read with
// systemd's parser.
var (
	systemdTypes = []string{"service", "socket", "device", "mount", "automount", "swap", "target", "path", "timer", "slice", "scope"}
	quadletTypes = []string{"container", "volume", "network", "kube", "image", "pod", "build"}
)

// unitType is the type of a unit's name, the text after its last dot, and
// its class; notUnit for a name of no type systemd or Quadlet reads.
func unitType(base string) (string, unitClass) {
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 {
		return "", notUnit
	}
	return typeClass(base[dot+1:])
}

// typeClass is t and its class when t is a type systemd or Quadlet reads.
func typeClass(t string) (string, unitClass) {
	switch {
	case slices.Contains(systemdTypes, t):
		return t, systemdUnit
	case slices.Contains(quadletTypes, t):
		return t, quadletUnit
	}
	return "", notUnit
}

// unitClassOf is the class of file by its name, or by the name of the file
// a template renders: a unit or a Quadlet file by its type, and a .conf in
// a directory whose name is a unit's or a type's with .d after it, such as
// api.service.d or service.d.
func unitClassOf(file string) unitClass {
	dirs, base := core.FileParts(core.Untemplated(file))
	if t, c := unitType(base); c != notUnit {
		return networkd(dirs, t, c)
	}
	if !strings.HasSuffix(base, ".conf") || len(dirs) == 0 {
		return notUnit
	}
	return dropInClass(dirs, dirs[len(dirs)-1])
}

// dropInClass is the class of a .conf file in the directory dir, below
// dirs: a drop-in of the unit or the type dir names with .d after it.
func dropInClass(dirs []string, dir string) unitClass {
	name, ok := strings.CutSuffix(dir, ".d")
	if !ok {
		return notUnit
	}
	t, c := unitType(name)
	if c == notUnit {
		t, c = typeClass(name)
	}
	return dropInOf[networkd(dirs[:len(dirs)-1], t, c)]
}

// dropInOf is the class of a drop-in of a unit of each class.
var dropInOf = map[unitClass]unitClass{systemdUnit: systemdDropIn, quadletUnit: quadletDropIn}

// networkd is c, but systemd's for a .network file directly in a
// directory named network outside containers/systemd, as systemd-networkd
// reads /etc/systemd/network and not its subdirectories, and Quadlet
// reads containers/systemd and every directory below it.
func networkd(dirs []string, t string, c unitClass) unitClass {
	if t == "network" && len(dirs) > 0 && dirs[len(dirs)-1] == "network" && !quadletTree(dirs) {
		return systemdUnit
	}
	return c
}

// quadletTree reports whether dirs hold containers, then systemd.
func quadletTree(dirs []string) bool {
	for i := 1; i < len(dirs); i++ {
		if dirs[i-1] == "containers" && dirs[i] == "systemd" {
			return true
		}
	}
	return false
}

// longLine is LONG_LINE_MAX, systemd's limit on a line: a line of this many
// bytes or more is refused, and so is one a \ continues to more.
const longLine = 1 << 20

// The line ends systemd reads (read_line_full): each kind once in a run.
const (
	eolNone = 0
	eolNUL  = 1 << iota
	eolLF
	eolCR
)

func eolOf(c byte) int {
	switch c {
	case 0:
		return eolNUL
	case '\n':
		return eolLF
	case '\r':
		return eolCR
	}
	return eolNone
}

// eachSystemdLine calls f with every line of data as read_line_full reads
// it, and the offset where it starts: a line ends at \n, \r or NUL, and so
// at a run of them in which no kind repeats and no NUL comes before
// another, so \r\n, \n\r and \n\0 each end one line. A line of longLine
// bytes or more is refused before f sees it.
func eachSystemdLine(data []byte, m *core.Meter, f func(off int, line []byte) error) error {
	for start := 0; start < len(data); {
		end := start
		for end < len(data) && eolOf(data[end]) == eolNone {
			end++
		}
		if end-start >= longLine {
			return core.LineError(core.LineOf(data, start), "a line of %d bytes or more, which systemd refuses", longLine)
		}
		next := lineEndPast(data, end)
		if err := m.Charge(core.LineWork + next - start); err != nil {
			return err
		}
		if err := f(start, data[start:end]); err != nil {
			return err
		}
		start = next
	}
	return nil
}

// lineEndPast is the offset past the line end at end: a run of \n, \r and
// NUL in which no kind repeats, ending after a NUL.
func lineEndPast(data []byte, end int) int {
	seen := eolNone
	for ; end < len(data); end++ {
		e := eolOf(data[end])
		if seen&eolNUL != 0 || e == eolNone || seen&e != 0 {
			break
		}
		seen |= e
	}
	return end
}

// systemdSpace is WHITESPACE, which systemd strips: no \v and no \f.
func systemdSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func trimSystemd(b []byte) (from, to int) {
	from, to = 0, len(b)
	for from < to && systemdSpace(b[from]) {
		from++
	}
	for to > from && systemdSpace(b[to-1]) {
		to--
	}
	return from, to
}

// endsEscaped reports whether a \ that no \ escapes ends line.
func endsEscaped(line []byte) bool {
	escaped := false
	for _, c := range line {
		escaped = !escaped && c == '\\'
	}
	return escaped
}

// safeSection is string_is_safe: no control character, quote or \.
func safeSection(name []byte) bool {
	for _, c := range name {
		if c < ' ' || c == 0x7f || c == '"' || c == '\'' || c == '\\' {
			return false
		}
	}
	return true
}

// part is where one line of a value a \ continues starts in the joined
// text, and the line of the file it is.
type part struct{ at, line int }

// lineAt is the line of the file that holds byte at of a joined text.
func lineAt(parts []part, at int) int {
	i := sort.Search(len(parts), func(i int) bool { return parts[i].at > at }) - 1
	return parts[max(i, 0)].line
}

func readSystemd(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	r := systemdReader{m: m, visit: visit, lines: core.NewLineCursor(data)}
	if err := eachSystemdLine(data, m, r.line); err != nil || !r.open {
		return err
	}
	return r.parse(r.joined, r.parts)
}

// systemdReader is readSystemd's state between lines: the section and
// the value a \ continues.
type systemdReader struct {
	m         *core.Meter
	visit     func(*core.Entry) error
	lines     *core.LineCursor
	section   []byte
	inSection bool
	seq       int
	bomSeen   bool
	joined    []byte // the text of a value a \ continues
	parts     []part
	open      bool // joined holds a line a \ ended
	e         core.Entry
}

// line reads the line at off as config_parse does: a comment goes, even
// between continued lines; the first byte order mark goes; a line a \
// ends is joined to the next; any other line is parsed.
func (r *systemdReader) line(off int, line []byte) error {
	n := r.lines.At(off)
	if lead := bytes.TrimLeft(line, " \t"); len(lead) > 0 && (lead[0] == '#' || lead[0] == ';') {
		return nil
	}
	if rest, ok := bytes.CutPrefix(line, []byte(core.BOM)); ok && !r.bomSeen {
		line, r.bomSeen = rest, true
	}
	escaped := endsEscaped(line)
	if !r.open && !escaped {
		return r.parse(line, []part{{0, n}})
	}
	return r.join(n, line, escaped)
}

// join adds line n to the value a \ continues, and parses the value once a
// line does not end in \.
func (r *systemdReader) join(n int, line []byte, escaped bool) error {
	if r.open && len(r.joined)+len(line) > longLine {
		return core.LineError(n, "a line a \\ continues grows past %d bytes, which systemd refuses", longLine)
	}
	if err := r.m.Charge(len(line)); err != nil {
		return err
	}
	if !r.open {
		r.joined, r.parts = r.joined[:0], r.parts[:0]
	}
	r.parts = append(r.parts, part{len(r.joined), n})
	r.joined = append(r.joined, line...)
	r.open = escaped
	if escaped {
		r.joined[len(r.joined)-1] = ' '
		return nil
	}
	return r.parse(r.joined, r.parts)
}

// parse is parse_line: a section header, or an assignment.
func (r *systemdReader) parse(text []byte, parts []part) error {
	from, to := trimSystemd(text)
	s := text[from:to]
	if len(s) == 0 {
		return nil
	}
	line := lineAt(parts, from)
	if !systemdUTF8(s) {
		return core.LineError(line, "the line is not UTF-8, which systemd refuses")
	}
	if s[0] == '[' {
		return r.header(line, s)
	}
	return r.assignment(line, s)
}

// header starts the section a header names, or refuses it.
func (r *systemdReader) header(line int, s []byte) error {
	if s[len(s)-1] != ']' {
		return core.LineError(line, "the section header %s does not end in ], which systemd refuses", core.Quote(string(s)))
	}
	name := s[1 : len(s)-1]
	if !safeSection(name) {
		return core.LineError(line, "the section header %s holds a quote, a \\ or a control character, which systemd refuses", core.Quote(string(s)))
	}
	r.section, r.inSection = append(r.section[:0], name...), true
	r.seq++
	return nil
}

// assignment visits key=value; a key set to nothing is a reset. systemd
// ignores a line with no = or nothing before it, and one before the first
// header, with a warning.
func (r *systemdReader) assignment(line int, s []byte) error {
	eq := bytes.IndexByte(s, '=')
	if !r.inSection || eq <= 0 {
		return nil
	}
	kf, kt := trimSystemd(s[:eq])
	vf, vt := trimSystemd(s[eq+1:])
	r.e = core.Entry{Section: r.section, SectionSeq: r.seq, Key: s[kf:kt], Value: s[eq+1+vf : eq+1+vt], Line: line}
	if len(r.e.Value) == 0 {
		r.e.Kind = core.EntryReset
	}
	r.e.Computed = systemdComputes(r.e.Key, r.e.Value)
	return r.visit(&r.e)
}

// systemdComputes reports whether systemd changes a value before it uses
// it: a % starts a specifier, such as %i or %h, or %% for %, and in a
// command line, the value of a key that starts with Exec, $ starts a
// variable systemd expands, or $$ for $.
func systemdComputes(key, value []byte) bool {
	return bytes.ContainsRune(value, '%') || bytes.HasPrefix(key, []byte("Exec")) && bytes.ContainsRune(value, '$')
}

// dropInDirs are the directories, below the unit's own, whose .conf files
// systemd applies to the unit file named base, in the order
// unit_file_find_dropin_paths lists them: base.d; for an instance such as
// a@x.service, the template's, a@.service.d, and its own dirs in turn; then
// for each prefix of the name that ends in a dash, from the longest,
// a-b-.service.d and a-.service.d, each with its instance; then the type's,
// service.d. A directory two of these reach is listed once, at the first.
// A file of one name in two of them is read from the first.
//
// A name of maxUnitName bytes or more is refused, as unit_name_is_valid
// refuses it. A name the walk meets again is skipped with all it reaches,
// which it listed the first time, so the walk is linear in the names it
// lists, each charged to m by its length.
func dropInDirs(base string, m *core.Meter) ([]string, error) {
	if len(base) >= maxUnitName {
		return nil, fmt.Errorf("the unit name %s is %d bytes, and systemd reads a name of at most %d", core.Quote(base), len(base), maxUnitName-1)
	}
	t, _ := unitType(base)
	w := dirWalk{seen: map[string]bool{}, m: m}
	if err := w.find(base); err != nil {
		return nil, err
	}
	return append(w.out, t+".d"), nil
}

// maxUnitName is UNIT_NAME_MAX, 256: systemd refuses a unit name of this
// many bytes or more.
const maxUnitName = 256

// dirWalk is unit_file_find_dirs's walk of the names a unit's drop-in
// directories are named by.
type dirWalk struct {
	seen map[string]bool
	out  []string
	m    *core.Meter
}

// find lists name's directory, then its template's and its prefix's.
func (w *dirWalk) find(name string) error {
	if w.seen[name] {
		return nil
	}
	if err := w.m.Charge(core.LineWork + len(name)); err != nil {
		return err
	}
	w.seen[name] = true
	w.out = append(w.out, name+".d")
	at := strings.IndexByte(name, '@')
	dot := strings.LastIndexByte(name, '.')
	instance := ""
	if at >= 0 && at+1 < dot {
		instance = name[at+1 : dot]
		if err := w.find(name[:at+1] + name[dot:]); err != nil {
			return err
		}
	}
	if prefix, ok := prefixUnit(name, at, dot, instance); ok {
		return w.find(prefix)
	}
	return nil
}

// prefixUnit is the unit of name's prefix up to its last dash, with
// name's instance and type, whose directories unit_file_find_dirs lists
// next; ok is false for a name with no such prefix.
func prefixUnit(name string, at, dot int, instance string) (string, bool) {
	prefix := name[:dot]
	if at >= 0 {
		prefix = name[:at]
	}
	prefix, ok := dashPrefix(prefix)
	if instance != "" {
		prefix += "@" + instance
	}
	return prefix + name[dot:], ok
}

// dashPrefix is the prefix of a unit name's prefix up to and with its last
// dash, as unit_file_find_dirs cuts it: a dash at the end is cut once and
// the dash before it taken, and a name with no dash, or only one at its
// start, has none.
func dashPrefix(prefix string) (string, bool) {
	chopped := false
	for {
		dash := strings.LastIndexByte(prefix, '-')
		if dash <= 0 {
			return "", false
		}
		if dash+1 < len(prefix) || chopped {
			return prefix[:dash+1], true
		}
		prefix, chopped = prefix[:dash], true
	}
}

// orderDropIns is the .conf files of dirs, found by list, in the order
// systemd applies them (conf_files_list_strv): by name, byte by byte, a
// name in two directories read from the first that lists it, and a name
// that starts with a dot left out, as systemd skips a hidden file.
func orderDropIns(dirs []string, list func(dir string) []string) []string {
	var files []string
	for _, d := range dirs {
		for _, f := range list(d) {
			if name := path.Base(f); !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".conf") {
				files = append(files, f)
			}
		}
	}
	return core.FirstOfEachName(files)
}
