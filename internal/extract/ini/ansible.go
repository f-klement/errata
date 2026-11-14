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
	if !ok || !knownAnsibleTag(tag) {
		return core.Query{}, fmt.Errorf("section %s is no group's name, group:vars or group:children; a group's name holds no :, ] or white space", core.Quote(i.Section))
	}
	if err := ansibleSpecFits(i, tag); err != nil {
		return core.Query{}, err
	}
	q := core.Query{Section: ansibleSectionName(nil, name, tag), Key: []byte(i.Key), HasKey: i.Key != ""}
	if i.Host != "" {
		q.Host, q.HasHost, q.MatchHost = []byte(i.Host), true, hostMatches
	}
	return q, nil
}

// knownAnsibleTag reports whether tag is a section type ansible knows: none,
// hosts, vars or children.
func knownAnsibleTag(tag string) bool {
	switch tag {
	case "", "hosts", "vars", "children":
		return true
	}
	return false
}

// ansibleSpecFits refuses a key or a host that a section of type tag does
// not hold: vars need a key and hold no host, and children hold neither.
func ansibleSpecFits(i Spec, tag string) error {
	switch {
	case tag == "vars" && i.Key == "":
		return errors.New("key is required in a vars section")
	case (tag == "vars" || tag == "children") && i.Host != "":
		return fmt.Errorf("host selects a host line, and section %s holds none", core.Quote(i.Section))
	case tag == "children" && i.Key != "":
		return fmt.Errorf("section %s lists groups and sets no key; leave key out to select the groups", core.Quote(i.Section))
	}
	return nil
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
	r := &ansibleReader{section: []byte("ungrouped"), m: m, visit: visit}
	return eachPyLine(data, m, r.line)
}

// ansibleReader is readAnsible's state between lines.
type ansibleReader struct {
	section []byte
	tag     string
	words   shellWords
	e       core.Entry // one entry, visited again for each value, so a value costs no allocation
	seq     struct{ section, host int }
	m       *core.Meter
	visit   func(*core.Entry) error
}

// line reads line n of the inventory.
func (r *ansibleReader) line(n int, raw []byte) error {
	line := pyStrip(raw)
	if ansibleBlank(line) {
		return nil
	}
	if name, t, ok := ansibleHeader(line); ok {
		return r.header(n, name, t)
	}
	if line[0] == '[' && line[len(line)-1] == ']' {
		return core.LineError(n, "%s is no section header: a group's name holds no :, ] or white space, and a type after : is a word", core.Quote(string(line)))
	}
	switch r.tag {
	case "vars":
		return r.vars(n, line)
	case "children":
		return r.child(n, line)
	default:
		r.seq.host++
		return hostLine(n, line, r.section, &r.words, &r.e, r.m, r.visitHost)
	}
}

// ansibleBlank reports whether line, stripped, is blank or a comment.
func ansibleBlank(line []byte) bool {
	return len(line) == 0 || line[0] == '#' || line[0] == ';'
}

// header starts the section of a header's name and type t.
func (r *ansibleReader) header(n int, name []byte, t string) error {
	if !knownAnsibleTag(t) {
		return core.LineError(n, "section [%s] has the type %s, which ansible does not know; write hosts, vars or children", core.Clip(string(name)), core.Quote(t))
	}
	r.section, r.tag = ansibleSectionName(r.section, name, t), t
	r.seq.section++
	return nil
}

// vars visits a key=value line of a vars section.
func (r *ansibleReader) vars(n int, line []byte) error {
	at := bytes.IndexByte(line, '=')
	if at < 0 {
		return core.LineError(n, "expected key=value in a vars section, got %s", core.Quote(string(line)))
	}
	if err := r.m.Charge(len(line)); err != nil { // pyValue reads the value once
		return err
	}
	value, computed, err := pyValue(pyStrip(line[at+1:]))
	if err != nil {
		return core.LineError(n, "%v", err)
	}
	r.e = core.Entry{Section: r.section, SectionSeq: r.seq.section, Key: pyStrip(line[:at]), Value: value, Line: n, Computed: computed}
	return r.visit(&r.e)
}

// child visits a group's line of a children section.
func (r *ansibleReader) child(n int, line []byte) error {
	group, ok := ansibleGroupLine(line)
	if !ok {
		return core.LineError(n, "expected a group's name in a children section, got %s", core.Quote(string(line)))
	}
	r.e = core.Entry{Section: r.section, SectionSeq: r.seq.section, Kind: core.EntryName, Value: group, Line: n}
	return r.visit(&r.e)
}

// visitHost visits an entry of a host line, numbered by its section and
// host.
func (r *ansibleReader) visitHost(e *core.Entry) error {
	e.SectionSeq, e.HostSeq = r.seq.section, r.seq.host
	return r.visit(e)
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
	empty, err := lineHost(n, host, m)
	if err != nil {
		return err
	}
	if empty {
		// ansible reads no host from the line, but still checks its words.
		return words.keyValues(n)
	}
	// ansible reads a host with a range as the hosts it expands to, and one
	// with a port it takes off as the host without it.
	computed, err := hostComputed(host, m)
	if err != nil {
		return err
	}
	*e = core.Entry{Section: section, Host: host, Kind: core.EntryName, Value: host, Line: n, Computed: computed}
	if err := visit(e); err != nil {
		return err
	}
	return hostVars(n, section, host, words, e, visit)
}

// lineHost checks the host of line n (checkHost), and reports whether its
// range expands to no host.
func lineHost(n int, host []byte, m *core.Meter) (bool, error) {
	empty, err := checkHost(host, m)
	switch {
	case errors.Is(err, core.ErrWork):
		return false, err
	case err != nil:
		return false, core.LineError(n, "the host %s %v", core.Quote(string(host)), err)
	}
	return empty, nil
}

// hostComputed reports whether ansible reads host as a name other than
// its text: one with a range, or one it takes a port off.
func hostComputed(host []byte, m *core.Meter) (bool, error) {
	if bytes.IndexByte(host, '[') >= 0 {
		return true, nil
	}
	return portTaken(host, m)
}

// hostVars visits each key=value word after the host of line n.
func hostVars(n int, section, host []byte, words *shellWords, e *core.Entry, visit func(*core.Entry) error) error {
	for i := 1; i < words.count(); i++ {
		w := words.word(i)
		at := bytes.IndexByte(w, '=')
		if at < 0 {
			return keyValueError(n, w)
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

// keyValueError refuses w, a word after the host of line n that is no
// key=value.
func keyValueError(n int, w []byte) error {
	return core.LineError(n, "expected key=value after the host, got %s", core.Quote(string(w)))
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
	in   bool  // inside a word, quoted or not
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
	w.buf, w.ends, w.in = w.buf[:0], w.ends[:0], false
	for i := 0; i < len(line); i++ {
		var err error
		if i, err = w.step(line, i); err != nil {
			return err
		}
	}
	w.endWord()
	if len(w.ends) == 0 {
		return errNoHostWord
	}
	return nil
}

// step reads the line at i, and returns where it stopped reading: the end
// of a quote, the byte after a \, or past the line at a comment.
func (w *shellWords) step(line []byte, i int) (int, error) {
	switch c := line[i]; c {
	case ' ', '\t', '\r', '\n':
		w.endWord()
	case '#':
		return len(line), nil
	case '\\':
		if i+1 == len(line) {
			return 0, errEndEscape
		}
		w.buf, w.in = append(w.buf, line[i+1]), true
		return i + 1, nil
	case '\'', '"':
		w.in = true
		return w.quoted(line, i)
	default:
		w.buf, w.in = append(w.buf, c), true
	}
	return i, nil
}

// endWord ends the word being read, if any.
func (w *shellWords) endWord() {
	if w.in {
		w.ends, w.in = append(w.ends, len(w.buf)), false
	}
}

// keyValues refuses the first word after the host of line n that is no
// key=value.
func (w *shellWords) keyValues(n int) error {
	for i := 1; i < w.count(); i++ {
		if word := w.word(i); bytes.IndexByte(word, '=') < 0 {
			return keyValueError(n, word)
		}
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
