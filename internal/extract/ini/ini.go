// Package ini is the ini extractor: every value of a key in a section of an
// INI file, read in the dialect of the tool that reads the file, through
// core's entry extractor.
package ini

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns every value of Key in Section of an INI file. Dialect names
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
type Spec struct {
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
	query func(Spec) (core.Query, error)
	read  core.ReadFunc
	// notLiteral, for the file a spec names, is the test of an entry whose
	// value the file's tool computes rather than reads as written, such as
	// php's E_ALL & ~E_DEPRECATED; nil when the tool reads every value of
	// the file as written. Such a value is returned as written and marked
	// not literal (check.Value.NotLiteral).
	notLiteral func(file string) func(*core.Entry) bool
}

// The fixed work of the dialects' own steps, in units of about 1.5 ns, as
// core.LineWork is of a line: measured on the densest shapes, a php token
// and a tox key's map entry each cost tens of nanoseconds whatever their
// length.
const (
	tokenWork = 96  // a php token
	mapWork   = 256 // a tox section's or key's map entry
)

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

// Describe names the dialect i reads in and the file's name, as a
// message names an ini extractor's kind: "plain dialect on pytest.ini". It
// is false when i names no dialect errata reads.
func Describe(i Spec) (string, bool) {
	d, err := chooseDialect(i.Dialect, i.File)
	if err != nil {
		return "", false
	}
	_, base := core.FileParts(i.File)
	return d.name + " dialect on " + base, true
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
		core.Quote(name), dialectNames())
}

// hostMatches reports whether a spec's host selects an entry's host: the
// host as written, or one of the hosts ansible reads from it (hostIn).
func hostMatches(host, want []byte, m *core.Meter) (bool, error) {
	if err := m.Charge(len(want)); err != nil {
		return false, err
	}
	if bytes.Equal(host, want) {
		return true, nil
	}
	ok, err := hostIn(host, want, m)
	if err != nil && !errors.Is(err, core.ErrWork) {
		return false, fmt.Errorf("the host %s %w", core.Quote(string(host)), err)
	}
	return ok, err
}

// Build checks i and returns its extractor, in the dialect it names or the
// file's name chooses.
func Build(i Spec) (check.Extractor, error) {
	src, err := core.CheckSource("ini", i.File, i.MaxBytes)
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
	x := core.EntryExtractor{Source: src, Query: q, Read: d.read}
	if d.notLiteral != nil {
		x.NotLiteral = d.notLiteral(i.File)
	}
	x.NotLiteral = core.TemplateNotLiteral(i.File, x.NotLiteral)
	return x, nil
}

// eachTextLine calls f with every line of data and its number, until f
// returns an error, as Python reads a file in text mode, where \r\n,
// a lone \r and \n each end a line. The number is still the line counted at
// \n, as every extractor counts lines, so the lines a lone \r ends share one.
func eachTextLine(data []byte, m *core.Meter, f func(n int, line []byte) error) error {
	n := 1
	for {
		i := bytes.IndexAny(data, "\r\n")
		if i < 0 {
			if err := m.Charge(core.LineWork + len(data)); err != nil {
				return err
			}
			return f(n, data)
		}
		if err := m.Charge(core.LineWork + i + 1); err != nil {
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
func eachPyLine(data []byte, m *core.Meter, f func(n int, line []byte) error) error {
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
			if err := m.Charge(core.LineWork + i + w - start); err != nil {
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
	if err := m.Charge(core.LineWork + len(data) - start); err != nil {
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
	return core.LineError(bytes.Count(data[:i], newline)+1, "the file is not UTF-8, which %s refuses", tool)
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
func onlyDialect(i Spec, subsection, host bool) error {
	switch {
	case i.Subsection != nil && !subsection:
		return fmt.Errorf("subsection is read only by the git dialect; set dialect: git to read %s as a git config file", core.Quote(i.File))
	case i.Host != "" && !host:
		return fmt.Errorf("host is read only by the ansible dialect; set dialect: ansible to read %s as an inventory", core.Quote(i.File))
	}
	return nil
}

// Resolve is i with the dialect it reads in written out: the one it names,
// or the one Build chooses by the file's name. It is i unchanged when i names
// no dialect errata reads, which Build refuses.
func Resolve(i Spec) Spec {
	if d, err := chooseDialect(i.Dialect, i.File); err == nil {
		i.Dialect = d.name
	}
	return i
}

// Place is i with only what decides where it reads, for Spec.Place: the
// file cleaned, no max_bytes, the dialect the file reads in, and the names
// in the form the dialect compares them in.
func Place(i Spec) Spec {
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
		i.Section, i.Subsection, i.Key = string(q.Section), nil, string(q.Key)
	case ansibleDialect, toxDialect:
		// ansible's web and web:hosts are one section, and tox lowers keys.
		i.Section, i.Key = string(q.Section), string(q.Key)
	}
	return i
}

// bom is the UTF-8 byte order mark, which a git config file, a php.ini and a
// tox.ini may start with.
var bom = []byte("\xef\xbb\xbf")

// hasDir reports whether one of dirs is one of names.
func hasDir(dirs []string, names ...string) bool {
	return slices.ContainsFunc(dirs, func(d string) bool { return slices.Contains(names, d) })
}

// newline is a line break, as bytes.Count takes it.
var newline = []byte("\n")

// isDigit reports whether c is an ASCII digit.
func isDigit(c byte) bool { return '0' <= c && c <= '9' }
