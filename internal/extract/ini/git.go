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
	name += "." + i.Key
	first, last := strings.IndexByte(name, '.'), strings.LastIndexByte(name, '.')
	out := []byte(name)
	for j := range out {
		c := out[j]
		if j < first || j > last {
			if !isKeyChar(c) || (j == last+1 && !isAlpha(c)) {
				return core.Query{}, fmt.Errorf("%s is no name git config reads: a section and a key are letters, digits and -, and a key starts with a letter", core.Quote(name))
			}
			out[j] = lower(c)
		} else if c == '\n' || c == 0 {
			return core.Query{}, fmt.Errorf("%s is no name git config reads: a subsection holds no newline and no NUL", core.Quote(name))
		}
	}
	return core.Query{Section: out[:last], Key: out[last+1:], HasKey: true}, nil
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
	if c == '\n' {
		s.line++
	}
	return c
}

// errorLine is the line of the last byte read, where git stops.
func (s *gitScanner) errorLine() int {
	if s.pos == 0 {
		return 1
	}
	return bytes.Count(s.data[:s.pos-1], newline) + 1
}

func readGit(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	s := &gitScanner{data: data, line: 1}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return core.LineError(bytes.Count(data[:i], newline)+1, "a NUL byte, which git reads as the end of a name or a value")
	}
	// git skips a byte order mark, and refuses a part of one.
	if bytes.HasPrefix(data, []byte(core.BOM[:1])) {
		if !bytes.HasPrefix(data, []byte(core.BOM)) {
			return core.LineError(1, "a part of a UTF-8 byte order mark")
		}
		s.pos = len(core.BOM)
	}
	var (
		section []byte // the section and subsection, as git names them
		seq     int    // the headers read
		e       core.Entry
		value   []byte
	)
	bad := func() error { return core.LineError(s.errorLine(), "this is no line git config reads") }
	last := 0 // where the bytes not yet charged start
	comment := false
	for {
		c := s.next()
		switch {
		case c == '\n':
			// A line of no entry, blank, a comment or a header, is charged
			// at its end; an entry's line at its entry.
			if err := m.Charge(core.LineWork + s.pos - last); err != nil {
				return err
			}
			last = s.pos
			if s.eof {
				return nil
			}
			comment = false
			continue
		case comment || gitSpace(c):
			continue
		case c == '#' || c == ';':
			comment = true
			continue
		case c == '[':
			var ok bool
			if section, ok = s.header(section[:0]); !ok || len(section) == 0 {
				return core.LineError(s.errorLine(), "this is no section header git config reads")
			}
			seq++
			continue
		case !isAlpha(c):
			return bad()
		}
		e = core.Entry{Section: section, SectionSeq: seq, Line: s.line, Key: append(e.Key[:0], lower(c))}
		for {
			c = s.next()
			if s.eof || !isKeyChar(c) {
				break
			}
			e.Key = append(e.Key, lower(c))
		}
		for c == ' ' || c == '\t' {
			c = s.next()
		}
		switch {
		case c == '\n':
			e.Value, e.NoValue = gitTrue, true
		case c != '=':
			return bad()
		default:
			var ok bool
			if value, ok = s.value(value[:0]); !ok {
				return core.LineError(s.errorLine(), "the value has an unknown escape or an unclosed double quote")
			}
			e.Value = value
		}
		if err := m.Charge(core.LineWork + s.pos - last); err != nil {
			return err
		}
		last = s.pos
		if err := visit(&e); err != nil {
			return err
		}
	}
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
	name = append(name, '.')
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

// value reads a value after its = into v, as git's parse_value does. trim
// is where white space at the end of the value starts, 0 while there is
// none; white space at the start is never added.
func (s *gitScanner) value(v []byte) ([]byte, bool) {
	quote, comment := false, false
	trim := 0
	for {
		c := s.next()
		switch {
		case c == '\n':
			if quote {
				return v, false
			}
			if trim > 0 {
				v = v[:trim]
			}
			return v, true
		case comment:
			continue
		case gitSpace(c) && !quote:
			if trim == 0 {
				trim = len(v)
			}
			if len(v) > 0 {
				v = append(v, c)
			}
			continue
		case !quote && (c == ';' || c == '#'):
			comment = true
			continue
		}
		trim = 0
		switch c {
		case '\\':
			switch c = s.next(); c {
			case '\n':
				continue
			case 't':
				c = '\t'
			case 'b':
				c = '\b'
			case 'n':
				c = '\n'
			case '\\', '"':
			default:
				return v, false
			}
		case '"':
			quote = !quote
			continue
		}
		v = append(v, c)
	}
}
