package sudoers

import (
	"bytes"
	"sort"

	"github.com/f-klement/errata/internal/extract/core"
)

// stmt is one statement of a file, as sudo's lexer reads it: the text of
// its lines with a \ at the end of a line joining the next and a comment
// taken off, and the line each part of the text is on. An include line is
// a statement of its own, with include set and no text.
type stmt struct {
	text []byte
	// marks are where the text moves to another line: the text from off
	// on is on line, up to the next mark.
	marks   []mark
	include *include
}

// mark is the offset in a statement's text where a line starts.
type mark struct{ off, line int }

// include is an include line: whether it reads a directory, the text
// after its keyword, and its line.
type include struct {
	dir  bool
	rest []byte
	line int
}

// lineAt is the line the byte at off of s's text is on.
func (s *stmt) lineAt(off int) int {
	i := sort.Search(len(s.marks), func(i int) bool { return s.marks[i].off > off })
	if i == 0 {
		return 0
	}
	return s.marks[i-1].line
}

// put adds c, on line n, to s's text.
func (s *stmt) put(c byte, n int) {
	if len(s.marks) == 0 || s.marks[len(s.marks)-1].line != n {
		s.marks = append(s.marks, mark{off: len(s.text), line: n})
	}
	s.text = append(s.text, c)
}

// statements calls visit with each statement of data in file order,
// charging m for each line. In a template, a line that holds one tag of
// the engine's and nothing else is passed over.
func statements(data []byte, template bool, m *core.Meter, visit func(*stmt) error) error {
	var s *stmt
	for off, n := 0, 1; off < len(data); n++ {
		next := core.NextLine(data, off)
		if err := m.Charge(core.LineWork + next - off); err != nil {
			return err
		}
		line := bytes.TrimSuffix(bytes.TrimSuffix(data[off:next], []byte("\n")), []byte("\r"))
		off = next
		var err error
		if s, err = step(s, line, n, template, visit); err != nil {
			return err
		}
	}
	if s != nil {
		return visitText(s, visit)
	}
	return nil
}

// step adds line n to s, the statement still open, or starts one, visits a
// statement the line ends, and returns the one still open after it.
func step(s *stmt, line []byte, n int, template bool, visit func(*stmt) error) (*stmt, error) {
	if s == nil {
		var err error
		if s, err = startLine(line, n, template, visit); s == nil || err != nil {
			return nil, err
		}
	}
	if s.add(line, n) {
		return s, nil
	}
	return nil, visitText(s, visit)
}

// startLine is the statement line n starts, or nil for a blank line, a
// template's tag line or an include line, which it visits whole.
func startLine(line []byte, n int, template bool, visit func(*stmt) error) (*stmt, error) {
	t := trim(line)
	switch {
	case len(t) == 0, template && tagLine(t):
		return nil, nil
	}
	if inc := includeLine(line, t, n); inc != nil {
		return nil, visit(&stmt{include: inc})
	}
	return &stmt{}, nil
}

// visitText visits s when it holds any text but white space.
func visitText(s *stmt, visit func(*stmt) error) error {
	if len(trim(s.text)) == 0 {
		return nil
	}
	return visit(s)
}

// add adds line n to s, without its comment, and reports whether a \ at
// its end joins the next line to it. A \ keeps the byte after it with it,
// for the parse to read; a # starts a comment outside double quotes,
// unless a user or group ID follows it (#1000, %#1000); a template's {{
// expression is kept whole.
func (s *stmt) add(line []byte, n int) bool {
	quoted := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && len(trim(line[i+1:])) == 0 {
			s.put(' ', n)
			return true
		}
		if c == '#' && !quoted && !idAt(line, i) {
			return false
		}
		if c == '"' {
			quoted = !quoted
		}
		i = s.addAt(line, i, n)
	}
	return false
}

// addAt adds the byte at i of line n, or the escape or the template
// expression it starts, and returns the offset of the last byte added.
func (s *stmt) addAt(line []byte, i, n int) int {
	switch {
	case line[i] == '\\':
		s.put(line[i], n)
		s.put(line[i+1], n)
		return i + 1
	case bytes.HasPrefix(line[i:], []byte("{{")):
		return s.expression(line, i, n)
	}
	s.put(line[i], n)
	return i
}

// expression adds the template expression that starts at i of line n, to
// its }} or to the end of the line, and returns the offset of its last
// byte.
func (s *stmt) expression(line []byte, i, n int) int {
	end := len(line)
	if j := bytes.Index(line[i+2:], []byte("}}")); j >= 0 {
		end = i + 2 + j + 2
	}
	for _, c := range line[i:end] {
		s.put(c, n)
	}
	return end - 1
}

// idAt reports whether the # at i of line starts a user or group ID: a
// digit after it, and before it the start of a word.
func idAt(line []byte, i int) bool {
	if i+1 >= len(line) || line[i+1] < '0' || line[i+1] > '9' {
		return false
	}
	return i == 0 || bytes.IndexByte([]byte(" \t,!(:=%"), line[i-1]) >= 0
}

// includeKeywords are the keywords of an include line, longest first.
var includeKeywords = []struct {
	word string
	dir  bool
}{{"#includedir", true}, {"@includedir", true}, {"#include", false}, {"@include", false}}

// includeLine is the include line n writes, or nil: one of
// includeKeywords, then white space. t is the line without white space
// around it. A #include or #includedir counts only at the start of the
// line, as sudo's lexer anchors them; indented, it is a comment.
func includeLine(line, t []byte, n int) *include {
	for _, k := range includeKeywords {
		from := t
		if k.word[0] == '#' {
			from = line
		}
		rest, ok := bytes.CutPrefix(from, []byte(k.word))
		if ok && len(rest) > 0 && isBlank(rest[0]) {
			return &include{dir: k.dir, rest: trim(rest), line: n}
		}
	}
	return nil
}

// tagLine reports a line of a template that is one tag of its engine's
// and nothing else: a Jinja statement or comment, or an ERB tag.
func tagLine(t []byte) bool {
	for _, p := range [][2]string{{"{%", "%}"}, {"{#", "#}"}, {"<%", "%>"}} {
		if bytes.HasPrefix(t, []byte(p[0])) && bytes.HasSuffix(t, []byte(p[1])) {
			return true
		}
	}
	return false
}

// trim is b without the spaces and tabs around it.
func trim(b []byte) []byte { return bytes.Trim(b, " \t") }

// isBlank reports a space or a tab.
func isBlank(c byte) bool { return c == ' ' || c == '\t' }
