package extract

import (
	"bytes"
	"errors"
	"fmt"
	slashpath "path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
)

// INI returns every value of Key in Section of an INI file. Dialect names
// the INI the file is written in, as the tool that reads it reads it:
// plain (Python's configparser), tox, php, git or ansible. Left out, it is
// chosen by the file's name (dialectFor). Subsection is read only by git and
// Host only by ansible.
//
// Section and Key are matched as text, never parsed as a path: an INI file
// is two levels deep, a section name holds the characters the path grammar
// gives a meaning to (testenv:py311, remote "origin", url "https://x/"),
// and a key repeated in a section has every one of its values, where a key
// step of a path selects one node.
type INI struct {
	File       string  `json:"file"`
	Dialect    string  `json:"dialect,omitempty"`
	Section    string  `json:"section,omitempty"`
	Subsection *string `json:"subsection,omitempty"`
	Host       string  `json:"host,omitempty"`
	Key        string  `json:"key,omitempty"`
	MaxBytes   int64   `json:"max_bytes,omitempty"`
}

// The dialect seam. A dialect is a reader that visits a file's entries in
// file order, the rules a spec's names are checked and compared by, and the
// file names it is chosen for; a new one is one more value in iniDialects.
// The ini extractor keeps the entries its query wants, and nothing else, so
// a file of many keys costs no memory for each key. Two dialects come later
// through this seam: systemd units (v0.4.0), whose reader adds \
// continuation and an entry kind for an empty assignment that clears the
// values a list key has collected so far, and EditorConfig, whose extractor
// selects the sections whose glob matches a path rather than a section by
// name, from the entries the same reader returns.
type iniDialect struct {
	name string
	// files reports whether the dialect reads file when the spec names no
	// dialect. The plain dialect has none: it reads every other file.
	files func(file string) bool
	// query checks the spec's names against the dialect and returns them in
	// the form its entries are compared in. Its errors start without a kind.
	query func(INI) (iniQuery, error)
	read  readFunc
	// notLiteral, for the file a spec names, is the test of an entry whose
	// value the file's tool computes rather than reads as written, such as
	// php's E_ALL & ~E_DEPRECATED; nil when the tool reads every value of
	// the file as written. Such a value is returned as written and marked
	// not literal (check.Value.NotLiteral).
	notLiteral func(file string) func(*iniEntry) bool
}

// readFunc reads a file into entries in file order, charging m for its work.
type readFunc func(data []byte, m *meter, visit func(*iniEntry) error) error

// The work meter. Each read of an ini or properties extractor has one
// budget, workPerByte for each byte of the file and of the spec's names,
// and every loop that touches a byte charges it: each line or token a
// reader reads, each entry it visits and the names compared for it, and
// for an ansible host the range walk, the match of a spec's host against
// a pattern and the address patterns. A read past the budget is refused,
// so no shape of file or spec costs more than its size times workPerByte,
// whatever loop a later change adds, as long as the loop charges.
//
// workPerByte was chosen from measurement. The fixed costs below and in
// the range walk make a unit 0.3 ns to 2.6 ns on the dense shapes of every
// reader measured at 32 MiB on a cluster node (TestReplayINIShapesAtFullSize's
// and the reviews'). Real files charge 2 to 8 units a byte: pytest's tox.ini
// 7.19 selecting minversion, git's own config 5.78, php.ini-production
// 4.16, flake8's setup.cfg 4.04, qemu's .gitmodules 3.96, an ansible
// example inventory 3.83, spring-petclinic's application.properties 3.61,
// chromium's .gitmodules 3.04, the JDK's java.security 2.52. The budget is
// 128 a byte, 18 times the densest real file, and at 32 MiB about 4.3
// billion units, 1.5 s to 11 s (the reviews' worst shape is refused at it in
// 5.9 s): a file errata refuses is one no tool writes.
// TestWorkMeterLeavesRoomForOrdinaryFiles holds the tests' files of each
// dialect to a quarter of it.
const workPerByte = 128

// workFloor is the budget every read has besides its bytes', about 1.5 ms:
// room for the address patterns of one host, 400 ns a byte, in a file of a
// few lines.
const workFloor = 1 << 20

// meter is one read's work budget, workFloor and workPerByte for each byte
// of the file and the spec, and the last address the address patterns
// decided, which a file of many lines of one host would otherwise pay for
// on each. A nil meter has no budget and keeps nothing, for tests of a
// reader alone.
type meter struct {
	budget, spent int64
	address       []byte // the host text isAddress last decided, and its answer
	isAddress     bool
}

func newMeter(bytes int) *meter {
	return &meter{budget: workFloor + workPerByte*int64(bytes)}
}

// charge spends n units, and reports errWork once the budget is spent.
func (m *meter) charge(n int) error {
	if m == nil {
		return nil
	}
	if m.spent += int64(n); m.spent > m.budget {
		return errWork
	}
	return nil
}

var errWork = errors.New("work")

// The fixed work of the readers' steps, in units of about 1.5 ns, besides
// one for each byte they read: measured on the densest shapes, a line of
// a few bytes, a key, a php token and a tox key's map entry each cost tens
// of nanoseconds whatever their length.
const (
	lineWork  = 64  // a line, or a properties logical line, or a git entry
	entryWork = 48  // an entry the matcher looks at
	tokenWork = 96  // a php token
	mapWork   = 256 // a tox section's or key's map entry
)

// tooMuchWork refuses a read that spent its budget.
func tooMuchWork(file string) error {
	return fmt.Errorf("%s: reading the file and matching the spec take more than %d units of work for each byte of the two and %d more, the limit; narrow the spec, or a regex extractor can still read the file", file, workPerByte, workFloor)
}

// iniDialects are the dialects in the order a file name is tried against
// them; plain comes last and takes every file.
var iniDialects = []*iniDialect{gitDialect, ansibleDialect, toxDialect, phpDialect, plainDialect}

// dialectNames lists the dialects in a message.
func dialectNames() string {
	var names []string
	for _, d := range iniDialects {
		names = append(names, d.name)
	}
	return strings.Join(names, ", ")
}

// dialectFor is the dialect for file when the spec names none: git for
// .gitconfig, .gitmodules, gitconfig, a name ending in .gitconfig, and
// config in a directory named .git or git; ansible for hosts, hosts.ini,
// inventory, inventory.ini and any file under a directory named inventory
// or inventories; tox for tox.ini; php for php.ini and a name ending in
// .ini under a directory named php, as /etc/php/8.3/fpm/conf.d/10-opcache.ini
// is; plain for every other file.
func dialectFor(file string) *iniDialect {
	for _, d := range iniDialects {
		if d.files == nil || d.files(file) {
			return d
		}
	}
	return plainDialect
}

// fileParts is file with slashes, cleaned, as its directories and its name.
func fileParts(file string) (dirs []string, base string) {
	p := filepath.ToSlash(filepath.Clean(file))
	return strings.Split(slashpath.Dir(p), "/"), slashpath.Base(p)
}

// chooseDialect is the dialect named, or the one for the file when none is.
func chooseDialect(name, file string) (*iniDialect, error) {
	if name == "" {
		return dialectFor(file), nil
	}
	for _, d := range iniDialects {
		if d.name == name {
			return d, nil
		}
	}
	return nil, fmt.Errorf("ini: dialect %s is not one errata reads; write one of %s, or leave it out to choose by the file's name",
		quote(name), dialectNames())
}

// entryKind is what an entry is.
type entryKind uint8

const (
	// entryKey is a key and its value.
	entryKey entryKind = iota
	// entryName is a name a line holds with no key: an ansible host line's
	// host, or a group a children section lists. Its value is the name.
	entryName
)

// iniEntry is one value a file sets, with the names it is set under, as its
// dialect compares them. The slices are valid only while the visit runs,
// since readers reuse their buffers.
type iniEntry struct {
	section []byte
	host    []byte // ansible: the host of a host line
	kind    entryKind
	key     []byte
	value   []byte
	line    int  // where the key, or the name, starts
	noValue bool // git: a key with no =, which git reads as true
	// sectionSeq counts the headers read so far, and hostSeq the ansible
	// host lines: an entry with the seq of the entry before has its
	// section, or its host, so a matcher compares each name once.
	sectionSeq int
	hostSeq    int
	// computed marks a value its reader found the tool computes: a php
	// boolean word, constant, operator or ${} reference, and an ansible
	// literal whose value Python writes otherwise, such as 0x10 or [1, 2].
	computed bool
}

// iniQuery is what an extractor selects, in the form of the entries.
type iniQuery struct {
	section []byte
	host    []byte
	hasHost bool // false: the entries of every host
	key     []byte
	hasKey  bool // false: the entries of kind entryName
}

func (q *iniQuery) wants(e *iniEntry) bool {
	switch {
	case !bytes.Equal(e.section, q.section):
		return false
	case q.hasHost && !hostMatchesOrNot(e.host, q.host):
		return false
	case q.hasKey:
		return e.kind == entryKey && bytes.Equal(e.key, q.key)
	default:
		return e.kind == entryName
	}
}

// matcher is iniQuery.wants for entries in file order: it compares the
// section once for each header and the host once for each host line, so a
// long name costs its length once, not once for each entry under it.
type matcher struct {
	q                   *iniQuery
	m                   *meter
	sectionSeq, hostSeq int
	sectionOK, hostOK   bool
	err                 error // a host the matcher refused to match, which ends the read
}

func newMatcher(q *iniQuery, m *meter) *matcher {
	return &matcher{q: q, m: m, sectionSeq: -1, hostSeq: -1}
}

func (m *matcher) wants(e *iniEntry) bool {
	if m.err = m.m.charge(entryWork + len(m.q.key)); m.err != nil {
		return false
	}
	if e.sectionSeq != m.sectionSeq {
		if m.err = m.m.charge(len(m.q.section)); m.err != nil {
			return false
		}
		m.sectionSeq, m.sectionOK = e.sectionSeq, bytes.Equal(e.section, m.q.section)
	}
	if !m.sectionOK {
		return false
	}
	if m.q.hasHost {
		if e.hostSeq != m.hostSeq {
			m.hostSeq = e.hostSeq
			if m.hostOK, m.err = hostMatches(e.host, m.q.host, m.m); m.err != nil {
				return false
			}
		}
		if !m.hostOK {
			return false
		}
	}
	if m.q.hasKey {
		return e.kind == entryKey && bytes.Equal(e.key, m.q.key)
	}
	return e.kind == entryName
}

// hostMatches reports whether a spec's host selects an entry's host: the
// host as written, or one of the hosts ansible reads from it (hostIn).
func hostMatches(host, want []byte, m *meter) (bool, error) {
	if err := m.charge(len(want)); err != nil {
		return false, err
	}
	if bytes.Equal(host, want) {
		return true, nil
	}
	ok, err := hostIn(host, want, m)
	if err != nil && !errors.Is(err, errWork) {
		return false, fmt.Errorf("the host %s %w", quote(string(host)), err)
	}
	return ok, err
}

// hostMatchesOrNot is hostMatches with a refusal read as no match, for the
// reference the fuzz target holds the matcher to.
func hostMatchesOrNot(host, want []byte) bool {
	ok, _ := hostMatches(host, want, nil)
	return ok
}

// entryExtractor returns the values of the entries its query wants from the
// file read reads. The ini and properties extractors are both one.
type entryExtractor struct {
	source
	query iniQuery
	read  readFunc
	// notLiteral is the dialect's test for the file, nil when every value
	// is literal.
	notLiteral func(*iniEntry) bool
	// valueLimit is the most values the extractor returns, 0 for maxValues,
	// which every extractor errata builds has: a test sets it to witness the
	// limit with a small file. The refusal still names maxValues.
	valueLimit int
}

// valuesAllowed is the extractor's value limit.
func (e entryExtractor) valuesAllowed() int {
	if e.valueLimit == 0 {
		return maxValues
	}
	return e.valueLimit
}

func buildINI(i INI) (check.Extractor, error) {
	src, err := checkSource("ini", i.File, i.MaxBytes)
	if err != nil {
		return nil, err
	}
	d, err := chooseDialect(i.Dialect, i.File)
	if err != nil {
		return nil, err
	}
	q, err := d.query(i)
	if err != nil {
		return nil, fmt.Errorf("ini: %s: %w", d.name, err)
	}
	x := entryExtractor{source: src, query: q, read: d.read}
	if d.notLiteral != nil {
		x.notLiteral = d.notLiteral(i.File)
	}
	x.notLiteral = templateNotLiteral(i.File, x.notLiteral)
	return x, nil
}

// MayReturnNotLiteral reports whether the dialect's tool computes some
// values of the file, which skip_not_literal can then leave out.
func (e entryExtractor) MayReturnNotLiteral() bool { return e.notLiteral != nil }

// Extract reads the file under root and returns every value the query
// selects, in file order.
func (e entryExtractor) Extract(root string) ([]check.Value, error) {
	data, err := e.source.read(root)
	if err != nil {
		return nil, err
	}
	return e.fromData(data)
}

// fromData returns the values of the entries the query wants, at most
// maxValues of them and at most the extractor's max_bytes in all, as every
// extractor's values. A value is mostly a part of the file, but git reads a
// key with no = as true, four bytes for a key of one, and a byte that is
// not UTF-8 is three in the U+FFFD Java reads for it.
func (e entryExtractor) fromData(data []byte) ([]check.Value, error) {
	out := []check.Value{}
	var total int64
	var stop error
	work := newMeter(len(data) + len(e.query.section) + len(e.query.host) + len(e.query.key))
	m := newMatcher(&e.query, work)
	err := e.read(data, work, func(en *iniEntry) error {
		if !m.wants(en) {
			switch {
			case errors.Is(m.err, errWork):
				return errWork
			case m.err != nil:
				return lineError(en.line, "%v", m.err)
			}
			return nil
		}
		if total += int64(len(en.value)); total > e.limit {
			stop = tooManyBytes(e.file, e.limit)
			return errStop
		}
		if len(out) == e.valuesAllowed() {
			stop = tooManyValues(e.file)
			return errStop
		}
		out = append(out, check.Value{Text: string(en.value), At: check.Location{File: e.file, Line: en.line}, NotLiteral: e.notLiteral != nil && e.notLiteral(en)})
		return nil
	})
	switch {
	case stop != nil:
		return nil, stop
	case errors.Is(err, errWork):
		return nil, tooMuchWork(e.file)
	case err != nil:
		return nil, fmt.Errorf("%s: %w", e.file, err)
	}
	return out, nil
}

// tooManyBytes refuses values that hold more bytes than limit in all.
func tooManyBytes(file string, limit int64) error {
	return fmt.Errorf("%s: the values the extractor selects hold more than %d bytes in all, the extractor's max_bytes; a key git reads as true and a byte Java reads as U+FFFD hold more than the file, so narrow the section or the key, or set max_bytes: higher", file, limit)
}

// errStop ends a read from inside a visit.
var errStop = errors.New("stopped")

// lineError names the line a reader stopped at.
func lineError(line int, format string, args ...any) error {
	return fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, args...))
}

// eachTextLine calls f with every line of data and its number, until f
// returns an error, as Python reads a file in text mode, where \r\n,
// a lone \r and \n each end a line. The number is still the line counted at
// \n, as every extractor counts lines, so the lines a lone \r ends share one.
func eachTextLine(data []byte, m *meter, f func(n int, line []byte) error) error {
	n := 1
	for {
		i := bytes.IndexAny(data, "\r\n")
		if i < 0 {
			if err := m.charge(lineWork + len(data)); err != nil {
				return err
			}
			return f(n, data)
		}
		if err := m.charge(lineWork + i + 1); err != nil {
			return err
		}
		if err := f(n, data[:i]); err != nil {
			return err
		}
		switch {
		case data[i] == '\n':
			n++
		case i+1 < len(data) && data[i+1] == '\n':
			n++
			i++
		}
		data = data[i+1:]
	}
}

// eachPyLine is eachTextLine as Python's str.splitlines splits the file's text,
// as ansible splits an inventory: at \n, \r\n, \r, \v, \f, \x1c, \x1d,
// \x1e, NEL, LS and PS. A byte that is not UTF-8 is a character of its own
// and ends no line, as Python's surrogateescape reads it. The number is the
// line counted at \n.
func eachPyLine(data []byte, m *meter, f func(n int, line []byte) error) error {
	n, start := 1, 0
	for i := 0; i < len(data); {
		c := data[i]
		w := 1
		brk := false
		switch {
		case c == '\n' || c == '\v' || c == '\f' || c == 0x1c || c == 0x1d || c == 0x1e:
			brk = true
		case c == '\r':
			brk = true
			if i+1 < len(data) && data[i+1] == '\n' {
				w = 2
			}
		case c >= utf8.RuneSelf:
			r, size := utf8.DecodeRune(data[i:])
			w = size
			brk = r == 0x85 || r == 0x2028 || r == 0x2029
		}
		if brk {
			if err := m.charge(lineWork + i + w - start); err != nil {
				return err
			}
			if err := f(n, data[start:i]); err != nil {
				return err
			}
			if data[i+w-1] == '\n' {
				n++
			}
			start = i + w
		}
		i += w
	}
	if err := m.charge(lineWork + len(data) - start); err != nil {
		return err
	}
	return f(n, data[start:])
}

// notUTF8 refuses data that is not UTF-8 for a dialect whose tool decodes it
// strictly, at the line of the first byte that is not.
func notUTF8(data []byte, tool string) error {
	if utf8.Valid(data) {
		return nil
	}
	i := 0
	for i < len(data) {
		r, w := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && w == 1 {
			break
		}
		i += w
	}
	return lineError(bytes.Count(data[:i], newline)+1, "the file is not UTF-8, which %s refuses", tool)
}

// pySpace is white space as Python's str.strip and \s read it, which the
// plain and ansible dialects strip as configparser and ansible do: Go's
// white space and the four separators U+001C to U+001F.
func pySpace(r rune) bool {
	return unicode.IsSpace(r) || (0x1c <= r && r <= 0x1f)
}

// pyStrip is b without white space at either end, as Python strips it.
func pyStrip(b []byte) []byte { return pyTrimRight(pyTrimLeft(b)) }

// pyTrimLeft and pyTrimRight look at an ASCII byte without decoding it,
// which a file of short lines does for most of its bytes.
func pyTrimLeft(b []byte) []byte {
	for len(b) > 0 {
		if c := b[0]; c < utf8.RuneSelf {
			if !asciiSpace(c) {
				break
			}
			b = b[1:]
			continue
		}
		r, w := utf8.DecodeRune(b)
		if !pySpace(r) {
			break
		}
		b = b[w:]
	}
	return b
}

func pyTrimRight(b []byte) []byte {
	for len(b) > 0 {
		if c := b[len(b)-1]; c < utf8.RuneSelf {
			if !asciiSpace(c) {
				break
			}
			b = b[:len(b)-1]
			continue
		}
		r, w := utf8.DecodeLastRune(b)
		if !pySpace(r) {
			break
		}
		b = b[:len(b)-w]
	}
	return b
}

// asciiSpace is pySpace for an ASCII byte.
func asciiSpace(c byte) bool { return c == ' ' || ('\t' <= c && c <= '\r') || (0x1c <= c && c <= 0x1f) }

// pyIndent is how many characters of white space line starts with.
func pyIndent(line []byte) int {
	n := 0
	for len(line) > 0 {
		r, w := utf8.DecodeRune(line)
		if !pySpace(r) || (r == utf8.RuneError && w == 1) {
			break
		}
		n++
		line = line[w:]
	}
	return n
}

// onlyDialect refuses subsection outside git and host outside ansible.
func onlyDialect(i INI, subsection, host bool) error {
	switch {
	case i.Subsection != nil && !subsection:
		return fmt.Errorf("subsection is read only by the git dialect; set dialect: git to read %s as a git config file", quote(i.File))
	case i.Host != "" && !host:
		return fmt.Errorf("host is read only by the ansible dialect; set dialect: ansible to read %s as an inventory", quote(i.File))
	}
	return nil
}

// iniPlace is i with only what decides where it reads, for Spec.Place: the
// file cleaned, no max_bytes, the dialect the file reads in, and the names
// in the form the dialect compares them in.
func iniPlace(i INI) INI {
	i.File, i.MaxBytes = filepath.Clean(i.File), 0
	d, err := chooseDialect(i.Dialect, i.File)
	if err != nil {
		return i
	}
	i.Dialect = d.name
	q, err := d.query(i)
	if err != nil {
		return i
	}
	switch d {
	case gitDialect:
		i.Section, i.Subsection, i.Key = string(q.section), nil, string(q.key)
	case ansibleDialect, toxDialect:
		// ansible's web and web:hosts are one section, and tox lowers keys.
		i.Section, i.Key = string(q.section), string(q.key)
	}
	return i
}

// hasDir reports whether one of dirs is one of names.
func hasDir(dirs []string, names ...string) bool {
	return slices.ContainsFunc(dirs, func(d string) bool { return slices.Contains(names, d) })
}
