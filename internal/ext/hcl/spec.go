package hcl

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/plugin/wire"
)

// spec is one entry of the hcl kind: the path of steps that selects its
// values.
type spec struct {
	steps []step
}

// rawSpec is a spec as a request carries it. A member left out is nil.
type rawSpec struct {
	Path *string `json:"path"`
}

// parseSpec reads a spec as a build or an extract request carries it, and
// refuses one errata should not have sent or whose path does not parse.
// wire's strict decode refuses a member the kind does not declare, one in
// another case, and one written twice.
func parseSpec(raw json.RawMessage) (spec, error) {
	var r rawSpec
	if err := wire.DecodeSpec(raw, &r); err != nil {
		return spec{}, err
	}
	if r.Path == nil {
		return spec{}, errors.New("path is required")
	}
	steps, err := parsePath(*r.Path)
	return spec{steps: steps}, err
}

// written is the spec as a build reply carries it: its path written one
// way, so two entries that select one place are one place.
func (s spec) written() json.RawMessage {
	out, _ := json.Marshal(struct {
		Path string `json:"path"`
	}{pathText(s.steps)})
	return out
}

// fans reports whether the path holds [*], so it may select many values,
// each of which then names the steps it took.
func (s spec) fans() bool {
	for _, st := range s.steps {
		if st.kind == stepAll {
			return true
		}
	}
	return false
}

// nothing is why the spec found no value in files: nothing matches its
// path.
func (s spec) nothing(files []wire.File) string {
	in := "the " + strconv.Itoa(len(files)) + " files"
	if len(files) == 1 {
		in = files[0].Path
	}
	return "nothing in " + in + " matches the path " + pathText(s.steps)
}

// stepKind is what a step of a path selects.
type stepKind int

const (
	stepName  stepKind = iota // a block type, a label, an attribute or a key, by name
	stepIndex                 // an element of a list, by its index from 0
	stepAll                   // [*]: every label, every attribute, every key or every element
)

// step is one step of a path.
type step struct {
	kind  stepKind
	name  string
	index int
}

// named reports whether st is a name step that names name.
func (st step) named(name string) bool { return st.kind == stepName && st.name == name }

// parsePath reads a path: steps after one another, each a name after a
// dot (the first without one), a quoted name, an index or [*] in brackets:
// resource.aws_instance["web"].tags.Name, ingress[*].from_port. A path
// that does not parse is refused, naming the column where it stops.
func parsePath(text string) ([]step, error) {
	if text == "" {
		return nil, errors.New(`path is ""; name an attribute, such as variable.region.default`)
	}
	if i := strings.IndexFunc(text, unicode.IsControl); i >= 0 {
		return nil, pathError(text, i, "a path holds no control character")
	}
	var steps []step
	for i := 0; i < len(text); {
		st, next, err := nextStep(text, i, len(steps) == 0)
		if err != nil {
			return nil, pathError(text, next, err.Error())
		}
		steps = append(steps, st)
		i = next
	}
	return steps, nil
}

// pathError is a path refused at byte at, named by its column from 1.
func pathError(text string, at int, why string) error {
	return fmt.Errorf("path %s: column %d: %s", strconv.Quote(text), utf8.RuneCountInString(text[:at])+1, why)
}

// nextStep reads the step at byte i of text, and returns it with the byte
// after it, or the error with the byte it stopped at.
func nextStep(text string, i int, first bool) (step, int, error) {
	switch {
	case text[i] == '[':
		return bracket(text, i+1)
	case text[i] == '.' && !first:
		return name(text, i+1)
	case first:
		return name(text, i)
	}
	return step{}, i, errors.New("a step starts with . or [")
}

// name reads the name that starts at byte i of text: a letter or _, then
// letters, digits, _ and -, as HCL writes an identifier.
func name(text string, i int) (step, int, error) {
	rest := text[i:]
	n := strings.IndexFunc(rest, func(r rune) bool { return !identRune(r, false) })
	if n == -1 {
		n = len(rest)
	}
	if first, _ := utf8.DecodeRuneInString(rest); !identRune(first, true) {
		return step{}, i, errors.New(`a name starts with a letter or _; write ["..."] for a label or a key that does not`)
	}
	return step{kind: stepName, name: rest[:n]}, i + n, nil
}

// identRune reports whether r may stand in an identifier, first or later.
func identRune(r rune, first bool) bool {
	if unicode.IsLetter(r) || r == '_' {
		return true
	}
	return !first && (unicode.IsDigit(r) || r == '-')
}

// bracket reads the step in brackets whose inside starts at byte i of
// text: *, an index, or a quoted name.
func bracket(text string, i int) (step, int, error) {
	if i == len(text) {
		return step{}, i, errors.New(`[ is not closed; write [*], an index such as [0], or a quoted name such as ["web"]`)
	}
	switch c := text[i]; {
	case c == '*':
		return closing(text, i+1, step{kind: stepAll})
	case c == '"' || c == '\'':
		return quoted(text, i)
	case c >= '0' && c <= '9':
		return index(text, i)
	}
	return step{}, i, errors.New(`write [*], an index such as [0], or a quoted name such as ["web"] in brackets`)
}

// closing returns st when byte i of text is the ] that closes it.
func closing(text string, i int, st step) (step, int, error) {
	if i == len(text) || text[i] != ']' {
		return step{}, i, errors.New("] is missing")
	}
	return st, i + 1, nil
}

// index reads the digits of an index at byte i of text: 0, or up to nine
// digits that do not start with 0.
func index(text string, i int) (step, int, error) {
	end := i
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	if end-i > 9 || text[i] == '0' && end-i > 1 {
		return step{}, i, errors.New("an index is 0, or up to nine digits that do not start with 0")
	}
	n, _ := strconv.Atoi(text[i:end])
	return closing(text, end, step{kind: stepIndex, index: n})
}

// quoted reads the name quoted at byte i of text, in the quote found
// there, in which \\ is a backslash and a backslash before the quote is the
// quote.
func quoted(text string, i int) (step, int, error) {
	q := text[i]
	var b strings.Builder
	for j := i + 1; j < len(text); j++ {
		switch c := text[j]; {
		case c == q:
			return closing(text, j+1, step{kind: stepName, name: b.String()})
		case c == '\\' && escapes(text, j+1, q):
			j++
			b.WriteByte(text[j])
		case c == '\\':
			return step{}, j, fmt.Errorf(`in a quoted name a backslash is written \\ and the quote \%c`, q)
		default:
			b.WriteByte(c)
		}
	}
	return step{}, len(text), errors.New("the quoted name is not closed")
}

// escapes reports whether byte i of text is one a backslash before it
// escapes in a name quoted with q: q or a backslash.
func escapes(text string, i int, q byte) bool {
	return i < len(text) && (text[i] == q || text[i] == '\\')
}

// pathText writes steps as a path, one way for each: a name that is an
// identifier after a dot, any other name in double quotes.
func pathText(steps []step) string {
	var b strings.Builder
	for i, st := range steps {
		switch {
		case st.kind == stepAll:
			b.WriteString("[*]")
		case st.kind == stepIndex:
			b.WriteString("[" + strconv.Itoa(st.index) + "]")
		case isIdent(st.name) && i > 0:
			b.WriteString("." + st.name)
		case isIdent(st.name):
			b.WriteString(st.name)
		default:
			b.WriteString(`["` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(st.name) + `"]`)
		}
	}
	return b.String()
}

// isIdent reports whether s is a name a path may write after a dot.
func isIdent(s string) bool {
	_, end, err := name(s, 0)
	return err == nil && end == len(s)
}
