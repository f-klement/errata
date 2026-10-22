package ini

import (
	"bytes"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
)

// The ansible dialect reads an INI inventory as ansible-core's ini inventory
// plugin does (ansible-core 2.21):
//
//   - a line ends where Python's str.splitlines ends one: \n, \r\n, \r,
//     \v, \f, \x1c, \x1d, \x1e, NEL, LS and PS; lines are still counted at \n;
//   - a line whose first character other than white space is # or ; is a
//     comment;
//   - [group], [group:hosts], [group:vars] and [group:children] are section
//     headers, with white space and a # comment after them; a group name
//     holds no :, ] or white space;
//   - in a group's hosts, each line is a host and its variables, split as a
//     POSIX shell splits words (Python's shlex), # outside quotes starting a
//     comment: web1 ansible_host=10.0.0.1 http_port=80;
//   - in group:vars, key=value at the first =, both sides stripped;
//   - in group:children, each line names a group.
//
// The lines before the first header are hosts of the group ungrouped, as
// ansible reads them. A spec selects group:vars and group:children by those
// names, and a group's hosts by the group's name. A host is the first word
// of its line as written: a port (host:2222) and a range (web[1:3]) are
// part of it, and a spec's host selects the line when it is that word or
// one of the hosts ansible reads from it, without its port and with its
// ranges expanded (hostIn), which errata decides without building them. A host ansible refuses is refused: one that ends in :, ---, and
// one whose range expand_hostname_range refuses (checkHost); a range of no
// host reads no host and no variable from its line, as ansible reads none.
// A value is the text after shell quoting, as pyValue reads it: a string
// literal decoded as Python decodes it, a comment after any other literal
// dropped, and anything else the text written. A literal whose value
// Python writes otherwise (0x10, 1_000, [80, 443]) and a Jinja2 template
// are marked not literal, since ansible computes them, and so is a host
// with a range or a port (web[01:03], db1:2222); a group is literal. A byte that is not UTF-8 is
// kept, as ansible's surrogateescape keeps it. errata does not check that a
// group named in children or vars is declared, nor that a group is not its
// own child.
var ansibleDialect = &iniDialect{
	name: "ansible",
	files: func(file string) bool {
		dirs, base := core.FileParts(file)
		switch base {
		case "hosts", "hosts.ini", "inventory", "inventory.ini":
			return true
		default:
			return hasDir(dirs, "inventory", "inventories")
		}
	},
	query:      ansibleQuery,
	read:       readAnsible,
	notLiteral: ansibleNotLiteral,
}

// ansibleQuery takes a section of a group's hosts, its vars or its
// children, and a key or host only where one is set: a key in vars always,
// a key or a host or both in hosts, neither in children. A key left out in
// hosts selects the hosts, and in children the groups.
func ansibleQuery(i Spec) (core.Query, error) {
	if err := onlyDialect(i, false, true); err != nil {
		return core.Query{}, err
	}
	if i.Section == "" {
		return core.Query{}, errors.New("section is required: a group's name, group:vars or group:children, and ungrouped for the hosts before the first header")
	}
	name, tag, ok := ansibleSection([]byte(i.Section))
	if !ok || (tag != "" && tag != "hosts" && tag != "vars" && tag != "children") {
		return core.Query{}, fmt.Errorf("section %s is no group's name, group:vars or group:children; a group's name holds no :, ] or white space", core.Quote(i.Section))
	}
	q := core.Query{Section: ansibleSectionName(nil, name, tag), Key: []byte(i.Key), HasKey: i.Key != ""}
	switch {
	case tag == "vars" && i.Key == "":
		return core.Query{}, errors.New("key is required in a vars section")
	case (tag == "vars" || tag == "children") && i.Host != "":
		return core.Query{}, fmt.Errorf("host selects a host line, and section %s holds none", core.Quote(i.Section))
	case tag == "children" && i.Key != "":
		return core.Query{}, fmt.Errorf("section %s lists groups and sets no key; leave key out to select the groups", core.Quote(i.Section))
	}
	if i.Host != "" {
		q.Host, q.HasHost, q.MatchHost = []byte(i.Host), true, hostMatches
	}
	return q, nil
}

// ansibleSectionName is the name an entry's section is compared by: the
// group's name for its hosts, and name:vars or name:children.
func ansibleSectionName(buf, name []byte, tag string) []byte {
	buf = append(buf[:0], name...)
	if tag == "vars" || tag == "children" {
		buf = append(append(buf, ':'), tag...)
	}
	return buf
}

// ansibleSection reads name and the optional :tag of s, a header between its
// brackets or a spec's section, as ansible's section pattern
// [^:\]\s]+(?::(\w+))? reads them.
func ansibleSection(s []byte) (name []byte, tag string, ok bool) {
	n := groupNameLen(s)
	if n == 0 {
		return nil, "", false
	}
	name, rest := s[:n], s[n:]
	if len(rest) == 0 {
		return name, "", true
	}
	if rest[0] != ':' || len(rest) == 1 {
		return nil, "", false
	}
	for _, r := range string(rest[1:]) {
		if !isWordRune(r) {
			return nil, "", false
		}
	}
	return name, string(rest[1:]), true
}

// groupNameLen is how many bytes of s are a group name: no :, ], or white
// space.
func groupNameLen(s []byte) int {
	i := 0
	for i < len(s) {
		r, w := utf8.DecodeRune(s[i:])
		if r == ':' || r == ']' || pySpace(r) {
			break
		}
		i += w
	}
	return i
}

// isWordRune is \w as Python reads it in a str pattern.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// ansibleHeader reports whether line, stripped, is a section header, and its
// name and tag: [name] or [name:tag], then white space and a # comment.
func ansibleHeader(line []byte) (name []byte, tag string, ok bool) {
	if line[0] != '[' {
		return nil, "", false
	}
	end := bytes.IndexByte(line, ']')
	if end < 0 {
		return nil, "", false
	}
	if rest := pyStrip(line[end+1:]); len(rest) > 0 && rest[0] != '#' {
		return nil, "", false
	}
	return ansibleSection(line[1:end])
}

// ansibleGroupLine reads a line of a children section: a group's name, then
// white space and a # comment.
func ansibleGroupLine(line []byte) ([]byte, bool) {
	n := groupNameLen(line)
	if rest := pyStrip(line[n:]); n == 0 || (len(rest) > 0 && rest[0] != '#') {
		return nil, false
	}
	return line[:n], true
}

func readAnsible(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	var (
		section = []byte("ungrouped")
		tag     string
		words   shellWords
		e       core.Entry // one entry, visited again for each value, so a value costs no allocation
		seq     struct{ section, host int }
	)
	visitHost := func(e *core.Entry) error {
		e.SectionSeq, e.HostSeq = seq.section, seq.host
		return visit(e)
	}
	return eachPyLine(data, m, func(n int, raw []byte) error {
		line := pyStrip(raw)
		if len(line) == 0 || line[0] == '#' || line[0] == ';' {
			return nil
		}
		if name, t, ok := ansibleHeader(line); ok {
			if t != "" && t != "hosts" && t != "vars" && t != "children" {
				return core.LineError(n, "section [%s] has the type %s, which ansible does not know; write hosts, vars or children", core.Clip(string(name)), core.Quote(t))
			}
			section, tag = ansibleSectionName(section, name, t), t
			seq.section++
			return nil
		}
		if line[0] == '[' && line[len(line)-1] == ']' {
			return core.LineError(n, "%s is no section header: a group's name holds no :, ] or white space, and a type after : is a word", core.Quote(string(line)))
		}
		switch tag {
		case "vars":
			at := bytes.IndexByte(line, '=')
			if at < 0 {
				return core.LineError(n, "expected key=value in a vars section, got %s", core.Quote(string(line)))
			}
			if err := m.Charge(len(line)); err != nil { // pyValue reads the value once
				return err
			}
			value, computed, err := pyValue(pyStrip(line[at+1:]))
			if err != nil {
				return core.LineError(n, "%v", err)
			}
			e = core.Entry{Section: section, SectionSeq: seq.section, Key: pyStrip(line[:at]), Value: value, Line: n, Computed: computed}
			return visit(&e)
		case "children":
			group, ok := ansibleGroupLine(line)
			if !ok {
				return core.LineError(n, "expected a group's name in a children section, got %s", core.Quote(string(line)))
			}
			e = core.Entry{Section: section, SectionSeq: seq.section, Kind: core.EntryName, Value: group, Line: n}
			return visit(&e)
		default:
			seq.host++
			return hostLine(n, line, section, &words, &e, m, visitHost)
		}
	})
}

// hostLine visits the host of a line in a group's hosts, and then each of its
// variables.
func hostLine(n int, line, section []byte, words *shellWords, e *core.Entry, m *core.Meter, visit func(*core.Entry) error) error {
	// The split, the range walk and pyValue each read the line once.
	if err := m.Charge(core.LineWork + 3*len(line)); err != nil {
		return err
	}
	if err := words.split(line); err != nil {
		return core.LineError(n, "the host line does not split as shell words: %v", err)
	}
	host := words.word(0)
	empty, err := checkHost(host, m)
	switch {
	case errors.Is(err, core.ErrWork):
		return err
	case err != nil:
		return core.LineError(n, "the host %s %v", core.Quote(string(host)), err)
	}
	if empty {
		// ansible reads no host from the line, but still checks its words.
		for i := 1; i < words.count(); i++ {
			if w := words.word(i); bytes.IndexByte(w, '=') < 0 {
				return core.LineError(n, "expected key=value after the host, got %s", core.Quote(string(w)))
			}
		}
		return nil
	}
	*e = core.Entry{Section: section, Host: host, Kind: core.EntryName, Value: host, Line: n}
	if err := visit(e); err != nil {
		return err
	}
	for i := 1; i < words.count(); i++ {
		w := words.word(i)
		at := bytes.IndexByte(w, '=')
		if at < 0 {
			return core.LineError(n, "expected key=value after the host, got %s", core.Quote(string(w)))
		}
		value, computed, err := pyValue(w[at+1:])
		if err != nil {
			return core.LineError(n, "%v", err)
		}
		*e = core.Entry{Section: section, Host: host, Key: w[:at], Value: value, Line: n, Computed: computed}
		if err := visit(e); err != nil {
			return err
		}
	}
	return nil
}

// shellWords splits a line as Python's shlex.split(line, comments=True)
// does, in POSIX mode: words between blanks (space, tab, CR, LF), '...'
// literal, "..." with \ escaping only " and \, \ outside quotes escaping any
// character, and # outside quotes ending the line. A quote left open, or a
// \ at the end, is an error. The words share one buffer, reused for each
// line.
type shellWords struct {
	buf  []byte
	ends []int // where each word ends in buf
}

var (
	errOpenQuote  = errors.New("a quote is not closed")
	errEndEscape  = errors.New("a \\ ends the line")
	errNoHostWord = errors.New("the line holds no word")
)

func (w *shellWords) count() int { return len(w.ends) }

func (w *shellWords) word(i int) []byte {
	start := 0
	if i > 0 {
		start = w.ends[i-1]
	}
	return w.buf[start:w.ends[i]]
}

func (w *shellWords) split(line []byte) error {
	w.buf, w.ends = w.buf[:0], w.ends[:0]
	in := false // inside a word, quoted or not
	for i := 0; i < len(line); i++ {
		switch c := line[i]; c {
		case ' ', '\t', '\r', '\n':
			if in {
				w.ends, in = append(w.ends, len(w.buf)), false
			}
		case '#':
			i = len(line)
		case '\\':
			if i+1 == len(line) {
				return errEndEscape
			}
			i++
			w.buf, in = append(w.buf, line[i]), true
		case '\'', '"':
			end, err := w.quoted(line, i)
			if err != nil {
				return err
			}
			i, in = end, true
		default:
			w.buf, in = append(w.buf, c), true
		}
	}
	if in {
		w.ends = append(w.ends, len(w.buf))
	}
	if len(w.ends) == 0 {
		return errNoHostWord
	}
	return nil
}

// quoted adds the text of the quote that opens at line[i] and returns where
// it closes.
func (w *shellWords) quoted(line []byte, i int) (int, error) {
	q := line[i]
	for i++; i < len(line); i++ {
		c := line[i]
		switch {
		case c == q:
			return i, nil
		case c == '\\' && q == '"':
			if i+1 == len(line) {
				return 0, errEndEscape
			}
			if next := line[i+1]; next == '"' || next == '\\' {
				c = next
				i++
			}
		}
		w.buf = append(w.buf, c)
	}
	return 0, errOpenQuote
}
