// Package etc is the etc extractor: every value of a keyword in a Unix
// configuration file of the keyword and value family, read in the dialect of
// the tool that reads the file (sshd, ssh, sysctl, resolv, limits,
// logrotate, nsswitch, modprobe, chrony and ntp), with the files it includes
// inside the repository read in the tool's order.
package etc

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns every value of Key that a configuration file sets, in the
// order the file's tool reads them, each at its file and line. File names
// the file; Files, for the dialects that read a directory of drop-ins
// (sysctl, limits and modprobe), names the files of one, which errata reads
// in the tool's order whatever the order written. Dialect names the tool;
// left out, the file's name chooses it (dialectFor).
//
// Block, for a dialect with blocks, keeps only the values written under the
// block it names: an sshd or ssh Match or Host line, a logrotate block's
// paths, a limits domain or a modprobe module. A Block of "" keeps the
// values outside any block. Match, for sshd and ssh, names one connection,
// as criterion and value (user: git, host: web1, address: 10.0.0.5), and
// keeps only the values OpenSSH applies to it.
type Spec struct {
	core.Where
	Dialect string            `json:"dialect,omitempty"`
	Key     string            `json:"key"`
	Block   *string           `json:"block,omitempty"`
	Match   map[string]string `json:"match,omitempty"`
}

// Kind is the etc kind, which a rule writes under the key etc.
var Kind = core.Kind[Spec]{Key: "etc", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is s with only what decides where it reads: the file or files as
// core.PlaceFiles writes them, no max_bytes, the dialect written out, and
// the key, the block and the match criteria in the form the dialect compares
// them, so two spellings of one keyword are one place.
func Place(s Spec) Spec {
	s.Where = s.Place()
	d, err := chooseDialect(s)
	if err != nil {
		return s
	}
	s.Dialect = d.name
	s.Key = d.foldKey(s.Key)
	if s.Block != nil {
		b := d.foldBlock(*s.Block)
		s.Block = &b
	}
	if s.Match != nil {
		m := make(map[string]string, len(s.Match))
		for k, v := range s.Match {
			m[strings.ToLower(k)] = v
		}
		s.Match = m
	}
	return s
}

// Resolve is s with its max_bytes and the dialect Build chooses written out,
// for errata list, sharing nothing with s.
func Resolve(s Spec) Spec {
	s.Where = s.Resolved()
	if d, err := chooseDialect(s); err == nil {
		s.Dialect = d.name
	}
	if s.Block != nil {
		b := *s.Block
		s.Block = &b
	}
	s.Match = maps.Clone(s.Match)
	return s
}

// Describe is what s's extractor returns: flat values under a label that
// names the dialect and the file, not literal in a template file and, for
// sshd and ssh, where OpenSSH expands a token or a variable in the value.
// The label is "" when no dialect is chosen.
func Describe(s Spec) check.Description {
	d, err := chooseDialect(s)
	if err != nil {
		return check.Description{}
	}
	_, base := core.FileParts(firstFile(s.Where))
	return check.Description{Label: "etc, " + d.name + " dialect on " + base, NotLiteral: d.expands || templated(s.Where)}
}

// firstFile is the file w names, or the first entry of its files.
func firstFile(w core.Where) string {
	if w.File == "" && len(w.Files) > 0 {
		return w.Files[0]
	}
	return w.File
}

// templated reports whether a file w names is a template.
func templated(w core.Where) bool {
	for _, f := range append([]string{w.File}, w.Files...) {
		if _, base := core.FileParts(f); f != "" && core.IsTemplate(base) {
			return true
		}
	}
	return false
}

// Build checks s and returns its extractor.
func Build(s Spec) (check.Extractor, error) {
	d, err := chooseDialect(s)
	if err != nil {
		return nil, err
	}
	x := extractor{d: d, key: d.foldKey(s.Key)}
	if err := x.where(s); err != nil {
		return nil, err
	}
	if err := x.checkKey(s.Key); err != nil {
		return nil, err
	}
	if err := x.checkBlock(s); err != nil {
		return nil, err
	}
	if s.Match != nil {
		if x.match, err = checkMatch(d, s); err != nil {
			return nil, err
		}
	}
	return x, nil
}

// where sets what x reads from s: one file, or for a dialect of drop-ins
// the files of files:.
func (x *extractor) where(s Spec) error {
	var err error
	switch {
	case s.File != "" && s.Files != nil:
		return errors.New("etc: file and files are both set; write file for one file, or files for the drop-ins of a directory")
	case s.Files != nil && !x.d.set:
		return fmt.Errorf("etc: the %s dialect reads one file and the files it includes, so files is refused; write file", x.d.name)
	case s.Files != nil && len(s.Files) == 0:
		return errors.New("etc: files lists no entry; name a file or a glob, or write file: for one file")
	case s.Files != nil:
		x.files, err = core.CheckFiles(Kind.Key, s.Files, s.MaxBytes)
		x.limit = core.MaxBytes(s.MaxBytes)
		return err
	}
	x.Source, err = core.CheckSource(Kind.Key, s.File, s.MaxBytes)
	x.limit = x.Limit
	return err
}

// checkKey refuses an empty key, and one the dialect's tool never reads.
func (x *extractor) checkKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("etc: key is required")
	}
	if err := x.d.checkKey(x.key); err != nil {
		return fmt.Errorf("etc: %s: %w", x.d.name, err)
	}
	return nil
}

// checkBlock sets the block s names, in the form the dialect compares it,
// and refuses one for a dialect with no blocks.
func (x *extractor) checkBlock(s Spec) error {
	if s.Block == nil {
		return nil
	}
	if x.d.foldBlock == nil {
		return fmt.Errorf("etc: the %s dialect has no blocks, so block is refused", x.d.name)
	}
	b := x.d.foldBlock(*s.Block)
	x.block = &b
	return nil
}

// checkMatch checks the criteria of s.Match against what d's tool matches.
func checkMatch(d *dialect, s Spec) (connection, error) {
	if d.criteria == nil {
		return nil, fmt.Errorf("etc: the %s dialect has no Match blocks, so match is refused", d.name)
	}
	if s.Block != nil {
		return nil, errors.New("etc: block and match are both set; block keeps the values of one block as written, match the values OpenSSH applies to one connection, so write one")
	}
	c := connection{}
	for _, k := range slices.Sorted(maps.Keys(s.Match)) {
		name := strings.ToLower(k)
		if err := checkCriterion(d, c, k, s.Match[k]); err != nil {
			return nil, err
		}
		c[name] = s.Match[k]
	}
	return c, nil
}

// checkCriterion refuses the criterion k of a match: with value v that d
// does not match, that c holds already, or whose value is no address or
// port where the criterion takes one.
func checkCriterion(d *dialect, c connection, k, v string) error {
	name := strings.ToLower(k)
	if !slices.Contains(d.criteria, name) {
		return fmt.Errorf("etc: match names %s, which is no criterion of %s errata matches; write one of %s", core.Quote(k), d.name, strings.Join(d.criteria, ", "))
	}
	if _, dup := c[name]; dup {
		return fmt.Errorf("etc: match names %s twice", core.Quote(name))
	}
	return checkCriterionValue(name, v)
}

// checkCriterionValue refuses a value v of the criterion name that is no
// address, or no port, where the criterion takes one.
func checkCriterionValue(name, v string) error {
	switch name {
	case "address", "localaddress":
		if _, err := netip.ParseAddr(v); err != nil {
			return fmt.Errorf("etc: match %s is %s, which is no IP address", name, core.Quote(v))
		}
	case "localport":
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("etc: match localport is %s, which is no port from 1 to 65535", core.Quote(v))
		}
	}
	return nil
}

// dialect is one tool's reading of its files.
type dialect struct {
	name string
	// names reports whether the dialect reads a file of these directories
	// and base name when the spec names no dialect.
	names func(dirs []string, base string) bool
	// read visits the entries of a file in file order.
	read func(data []byte, m *core.Meter, visit func(*entry) error) error
	// foldKey is a keyword in the form the dialect compares it; checkKey
	// refuses a key the tool never reads.
	foldKey  func(string) string
	checkKey func(string) error
	// wants reports whether an entry's key is the spec's key.
	wants func(e *entry, key string) bool
	// foldBlock is a block: in the form the dialect compares it, nil for a
	// dialect with no blocks.
	foldBlock func(string) string
	// includes are the keywords whose value names files to read in place,
	// and how.
	includes map[string]includeKind
	// homes are, for the file the spec names, the directories of the host
	// an include may name, longest first, each with the directory of the
	// repository that stands for it (homeOf).
	homes func(file string) []home
	// shadows are the keywords that, set before a Match line, change what
	// the Match criterion they name matches: ssh's User, Hostname and Tag.
	shadows map[string]string
	// mustInclude marks a tool that refuses an include of one file that is
	// not there, where the others pass over it.
	mustInclude bool
	// set marks a dialect that reads a directory of drop-ins (files:), and
	// order sorts their files in the tool's order.
	set   bool
	order func([]string) []string
	// criteria are the Match criteria a spec's match: may give; nil for a
	// dialect with no Match blocks.
	criteria []string
	// many reports whether a keyword takes every value a connection
	// obtains, rather than the first, when the spec gives match:.
	many func(key string) bool
	// matchFirst marks sshd, which applies the values of the Match blocks
	// a connection matches over those outside any block.
	matchFirst bool
	// expands marks a dialect whose tool expands tokens in some values,
	// which notLiteral finds.
	expands    bool
	notLiteral func(e *entry) bool
}

// dialects are the dialects in the order a file name is tried against them.
var dialects = []*dialect{sshdDialect, sshDialect, sysctlDialect, resolvDialect, limitsDialect, logrotateDialect, nsswitchDialect, modprobeDialect, chronyDialect, ntpDialect}

// dialectNames lists the dialects in a message.
func dialectNames() string {
	var names []string
	for _, d := range dialects {
		names = append(names, d.name)
	}
	return strings.Join(names, ", ")
}

// dialectFor is the dialect whose tool reads file, by its name once a
// template suffix is taken off, or nil.
func dialectFor(file string) *dialect {
	dirs, base := core.FileParts(core.Untemplated(file))
	for _, d := range dialects {
		if d.names(dirs, base) {
			return d
		}
	}
	return nil
}

// chooseDialect is the dialect s names, or the one the name of its file, or
// of the first entry of its files, chooses.
func chooseDialect(s Spec) (*dialect, error) {
	if s.Dialect != "" {
		for _, d := range dialects {
			if d.name == s.Dialect {
				return d, nil
			}
		}
		return nil, fmt.Errorf("etc: dialect %s is not one errata reads; write one of %s, or leave it out to choose by the file's name", core.Quote(s.Dialect), dialectNames())
	}
	file := firstFile(s.Where)
	if d := dialectFor(file); d != nil {
		return d, nil
	}
	return nil, fmt.Errorf("etc: no dialect reads a file named %s; write dialect: one of %s; %s", core.Quote(path.Base(file)), dialectNames(), otherKinds)
}

// otherKinds names the kinds that read the Unix files no dialect of etc
// reads, for the refusal of a file whose name chooses no dialect.
const otherKinds = "the table kind reads a field table such as fstab or passwd, the sudoers kind a sudoers file, and the udev kind udev rules"

// inDir reports whether dirs ends in a directory named name.
func inDir(dirs []string, name string) bool { return len(dirs) > 0 && dirs[len(dirs)-1] == name }

// exact is a key compared as written.
func exact(s string) string { return strings.TrimSpace(s) }

// oneWord refuses a key that holds white space, which no keyword does.
func oneWord(key string) error {
	if strings.ContainsAny(key, " \t") {
		return fmt.Errorf("key %s holds white space, which no keyword does", core.Quote(key))
	}
	return nil
}

// sameKey is the test of an entry's key against a key in the form the
// dialect compares it.
func sameKey(e *entry, key string) bool { return string(e.key) == key }

// fields is s with its runs of spaces and tabs written as one space, and
// none at either end: a block's header in the form it is compared.
func fields(s string) string { return strings.Join(strings.Fields(s), " ") }
