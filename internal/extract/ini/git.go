package ini

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// The git dialect reads a git config file as git's own parser does
// (config.c in git 2.47), so a value is what git config --get-all prints:
//
//   - [section] or [section "subsection"]; a section name is letters,
//     digits, hyphens and dots, and is compared without regard to case; a subsection
//     is compared as written, and takes \ before any character;
//   - the deprecated [section.subsection] is the subsection in lower case;
//   - a key is a letter, then letters, digits and -, compared without
//     regard to case; key = value, and a key with no = is true;
//   - # or ; outside double quotes starts a comment, to the end of the
//     line; white space at both ends of a value goes, and inside double
//     quotes stays; \" \\ \n \t \b and \ at the end of a line, which
//     continues the value, are the escapes, and any other is an error.
//
// A section, a subsection and a key name an entry together, as git names it
// remote.origin.url: section remote with subsection origin and key url, and
// section remote.origin with key url, are one entry, as they are to git.
// include and includeIf are not followed, as git config --file does not
// follow them. A file with a NUL byte is refused, since git reads a name or
// a value only up to one.
var gitDialect = &iniDialect{
	name: "git",
	files: func(file string) bool {
		dirs, base := core.FileParts(file)
		switch {
		case base == ".gitconfig" || base == ".gitmodules" || base == "gitconfig" || strings.HasSuffix(base, ".gitconfig"):
			return true
		default:
			return base == "config" && len(dirs) > 0 && (dirs[len(dirs)-1] == ".git" || dirs[len(dirs)-1] == "git")
		}
	},
	query: gitQuery,
	read:  readGit,
}

// gitQuery joins the spec's names as git config writes them on its command
// line and checks them as git does (git_config_parse_key): the section part,
// up to the first dot, and the key, after the last, are letters, digits and
// -, and the key starts with a letter; both are lower-cased, and what lies
// between is the subsection, as written.
func gitQuery(i Spec) (core.Query, error) {
	if err := onlyDialect(i, true, false); err != nil {
		return core.Query{}, err
	}
	switch {
	case i.Section == "":
		return core.Query{}, errors.New("section is required")
	case i.Key == "":
		return core.Query{}, errors.New("key is required")
	}
	name := i.Section
	if i.Subsection != nil {
		name += "." + *i.Subsection
	}
	out, last, err := gitConfigKey(name + "." + i.Key)
	if err != nil {
		return core.Query{}, err
	}
	return core.Query{Section: out[:last], Key: out[last+1:], HasKey: true}, nil
}

// gitConfigKey checks name, a key as git config takes it on its command
// line, and returns it with its section and key lower-cased, and where its
// last dot is.
func gitConfigKey(name string) ([]byte, int, error) {
	first, last := strings.IndexByte(name, '.'), strings.LastIndexByte(name, '.')
	out := []byte(name)
	for j := range out {
		c := out[j]
		if j < first || j > last {
			if !isGitNameByte(c, j == last+1) {
				return nil, 0, fmt.Errorf("%s is no name git config reads: a section and a key are letters, digits and -, and a key starts with a letter", core.Quote(name))
			}
			out[j] = lower(c)
		} else if c == '\n' || c == 0 {
			return nil, 0, fmt.Errorf("%s is no name git config reads: a subsection holds no newline and no NUL", core.Quote(name))
		}
	}
	return out, last, nil
}

// isGitNameByte reports whether c can be in a section or a key: a letter,
// a digit or -, and a letter where it starts the key (keyStart).
func isGitNameByte(c byte, keyStart bool) bool {
	return isKeyChar(c) && (!keyStart || isAlpha(c))
}

func isAlpha(c byte) bool   { return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') }
func isKeyChar(c byte) bool { return isAlpha(c) || ('0' <= c && c <= '9') || c == '-' }
func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// gitSpace is white space as git's ctype reads it: no \v and no \f.
func gitSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// gitScanner reads bytes as git's get_next_char does: CRLF is one \n, and
// the end of the file is a \n, again at each call, with eof set.
type gitScanner struct {
	data []byte
	pos  int
	line int // the line of the byte at pos
	eof  bool
}

func (s *gitScanner) next() byte {
	if s.pos >= len(s.data) {
		s.eof = true
		return '\n'
	}
	c := s.data[s.pos]
	s.pos++
	if c == '\r' && s.pos < len(s.data) && s.data[s.pos] == '\n' {
		c = '\n'
		s.pos++
	}
	if c == '\n' || c == '\r' { // a lone \r is a byte of the line to git, and ends one to errata's numbers
		s.line++
	}
	return c
}

// errorLine is the line of the last byte read, where git stops.
func (s *gitScanner) errorLine() int {
	if s.pos == 0 {
		return 1
	}
	return core.LineOf(s.data, s.pos-1)
}

func readGit(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	s := &gitScanner{data: data, line: 1}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return core.LineError(core.LineOf(data, i), "a NUL byte, which git reads as the end of a name or a value")
	}
	if err := s.skipBOM(); err != nil {
		return err
	}
	r := &gitReader{s: s, m: m, visit: visit}
	for {
		if done, err := r.read(s.next()); done || err != nil {
			return err
		}
	}
}

// skipBOM skips a byte order mark, as git does, and refuses a part of one.
func (s *gitScanner) skipBOM() error {
	if !bytes.HasPrefix(s.data, []byte(core.BOM[:1])) {
		return nil
	}
	if !bytes.HasPrefix(s.data, []byte(core.BOM)) {
		return core.LineError(1, "a part of a UTF-8 byte order mark")
	}
	s.pos = len(core.BOM)
	return nil
}

// gitReader is readGit's state between the bytes of the file.
type gitReader struct {
	s       *gitScanner
	m       *core.Meter
	section []byte // the section and subsection, as git names them
	seq     int    // the headers read
	e       core.Entry
	value   []byte
	last    int // where the bytes not yet charged start
	comment bool
	visit   func(*core.Entry) error
}

// read reads the file at c, the next byte outside an entry, and reports
// true at the end of the file.
func (r *gitReader) read(c byte) (bool, error) {
	switch {
	case c == '\n':
		return r.endLine()
	case r.comment || gitSpace(c):
		return false, nil
	case c == '#' || c == ';':
		r.comment = true
		return false, nil
	case c == '[':
		return false, r.header()
	case !isAlpha(c):
		return false, r.bad()
	}
	return false, r.entry(c)
}

// bad is the error of a line git config does not read.
func (r *gitReader) bad() error {
	return core.LineError(r.s.errorLine(), "this is no line git config reads")
}

// charge charges the bytes read since the last charge, and their line.
func (r *gitReader) charge() error {
	if err := r.m.Charge(core.LineWork + r.s.pos - r.last); err != nil {
		return err
	}
	r.last = r.s.pos
	return nil
}

// endLine ends a line of no entry, blank, a comment or a header, which is
// charged at its end, and reports true at the end of the file.
func (r *gitReader) endLine() (bool, error) {
	if err := r.charge(); err != nil {
		return false, err
	}
	if r.s.eof {
		return true, nil
	}
	r.comment = false
	return false, nil
}

// header reads a section header after its [.
func (r *gitReader) header() error {
	var ok bool
	if r.section, ok = r.s.header(r.section[:0]); !ok || len(r.section) == 0 {
		return core.LineError(r.s.errorLine(), "this is no section header git config reads")
	}
	r.seq++
	return nil
}

// entry reads an entry whose key starts with c, charges its line, and
// visits it.
func (r *gitReader) entry(c byte) error {
	s := r.s
	r.e = core.Entry{Section: r.section, SectionSeq: r.seq, Line: s.line, Key: append(r.e.Key[:0], lower(c))}
	switch c = r.key(); {
	case c == '\n':
		r.e.Value, r.e.NoValue = gitTrue, true
	case c != '=':
		return r.bad()
	default:
		var ok bool
		if r.value, ok = s.value(r.value[:0]); !ok {
			return core.LineError(s.errorLine(), "the value has an unknown escape or an unclosed double quote")
		}
		r.e.Value = r.value
	}
	if err := r.charge(); err != nil {
		return err
	}
	return r.visit(&r.e)
}

// key reads the rest of an entry's key into it, and the blanks after it,
// and returns the byte after them.
func (r *gitReader) key() byte {
	s := r.s
	var c byte
	for {
		c = s.next()
		if s.eof || !isKeyChar(c) {
			break
		}
		r.e.Key = append(r.e.Key, lower(c))
	}
	for c == ' ' || c == '\t' {
		c = s.next()
	}
	return c
}

var gitTrue = []byte("true")

// header reads a section header after its [ into name, as git's
// get_base_var and get_extended_base_var do.
func (s *gitScanner) header(name []byte) ([]byte, bool) {
	for {
		c := s.next()
		switch {
		case s.eof:
			return name, false
		case c == ']':
			return name, true
		case gitSpace(c):
			return s.subsection(name, c)
		case !isKeyChar(c) && c != '.':
			return name, false
		}
		name = append(name, lower(c))
	}
}

// subsection reads the blanks after a section's name, from c, and the
// quoted subsection after them, into name after a dot.
func (s *gitScanner) subsection(name []byte, c byte) ([]byte, bool) {
	for {
		if c == '\n' {
			return name, false
		}
		if c = s.next(); !gitSpace(c) {
			break
		}
	}
	if c != '"' {
		return name, false
	}
	return s.quotedSubsection(append(name, '.'))
}

// quotedSubsection reads a subsection after its opening " into name, and
// the ] after its closing ". A \ takes the byte after it.
func (s *gitScanner) quotedSubsection(name []byte) ([]byte, bool) {
	for {
		c := s.next()
		switch c {
		case '\n':
			return name, false
		case '"':
			return name, s.next() == ']'
		case '\\':
			if c = s.next(); c == '\n' {
				return name, false
			}
		}
		name = append(name, c)
	}
}

// value reads a value after its = into v, as git's parse_value does.
func (s *gitScanner) value(v []byte) ([]byte, bool) {
	g := gitValue{v: v}
	for {
		switch c := s.next(); {
		case c == '\n':
			return g.end()
		case g.skip(c):
		case !g.char(s, c):
			return g.v, false
		}
	}
}

// gitValue is a value as gitScanner.value reads it. trim is where white
// space at the end of the value starts, 0 while there is none; white space
// at the start is never added.
type gitValue struct {
	v              []byte
	quote, comment bool
	trim           int
}

// end is the value at the end of its line, without the white space at its
// end, and false inside double quotes.
func (g *gitValue) end() ([]byte, bool) {
	if g.quote {
		return g.v, false
	}
	if g.trim > 0 {
		g.v = g.v[:g.trim]
	}
	return g.v, true
}

// skip reads c where it is a comment's byte, white space outside double
// quotes, or the # or ; that starts a comment, and reports whether it was.
func (g *gitValue) skip(c byte) bool {
	switch {
	case g.comment:
	case gitSpace(c) && !g.quote:
		g.space(c)
	case !g.quote && (c == ';' || c == '#'):
		g.comment = true
	default:
		return false
	}
	return true
}

// space adds white space c, which may end the value.
func (g *gitValue) space(c byte) {
	if g.trim == 0 {
		g.trim = len(g.v)
	}
	if len(g.v) > 0 {
		g.v = append(g.v, c)
	}
}

// char adds c, a byte of the value other than white space outside double
// quotes: a ", which opens or closes them, an escape, read from s, or the
// byte itself. It reports false for an unknown escape.
func (g *gitValue) char(s *gitScanner, c byte) bool {
	g.trim = 0
	switch c {
	case '\\':
		var cont, ok bool
		if c, cont, ok = s.escape(); !ok || cont {
			return ok
		}
	case '"':
		g.quote = !g.quote
		return true
	}
	g.v = append(g.v, c)
	return true
}

// escape reads the byte after a \ and returns the byte the escape stands
// for, or cont true for a \ at the end of a line, which continues the
// value. It reports false for an unknown escape.
func (s *gitScanner) escape() (c byte, cont, ok bool) {
	switch c = s.next(); c {
	case '\n':
		return 0, true, true
	case '\\', '"':
		return c, false, true
	case 't':
		return '\t', false, true
	case 'b':
		return '\b', false, true
	case 'n':
		return '\n', false, true
	}
	return 0, false, false
}
