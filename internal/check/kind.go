package check

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Kind is a check kind: a program check, a rule or a generated check
// (GLOSSARY.md). A check holds one kind, so it is never two. Each kind is a
// pointer to its type, as Check.Kind holds it. Adding a kind takes:
//
//   - a type with these methods;
//   - a row in config's kindRows, with the keys that mark the kind and its
//     builder, and those keys as fields of config's checkEntry, so the
//     decoder accepts them;
//   - a row in engine's dispatch table, which TestTheTableRunsEveryKindAConfigLoads
//     asks for;
//   - its fields in report's listedFields, which TestListPlacesEveryField
//     asks for.
type Kind interface {
	// Name is the kind as the JSON of errata list writes its type, such as
	// rule.
	Name() string
	// Nouns name the kind in the text of errata list, for one check and for
	// more, such as rule and rules.
	Nouns() (one, many string)
	// ScopeRefusal says why the kind does not take s, a known scope, or is
	// "" when it does.
	ScopeRefusal(s Scope) string
	// Validate returns every problem with the kind. c is the check it is
	// the kind of, for the keys a check holds whatever its kind, such as
	// its timeout.
	Validate(c Check) []error
	// ListFields are the keys the JSON of errata list writes for c after
	// its type, in order.
	ListFields(c Check) Fields
	// ListLines are the lines the text of errata list writes for c after
	// its reason, in order, with each value from the config written by q.
	ListLines(c Check, q Quoter) []Line
}

// errNoKind is Validate's refusal of a check that holds no kind, and a
// program check's of one with no run, which is what an entry that writes
// neither run nor a default run loads as.
var errNoKind = errors.New("run or extract is required: a program check has run, a rule has extract, and a generated check has generated")

// Rule returns the rule c is, or nil when c is another kind.
func (c Check) Rule() *Rule { r, _ := c.Kind.(*Rule); return r }

// Generated returns the generated check c is, or nil when c is another kind.
func (c Check) Generated() *Generated { g, _ := c.Kind.(*Generated); return g }

// Program returns the program check c is, or nil when c is another kind.
func (c Check) Program() *Program { p, _ := c.Kind.(*Program); return p }

// IsRule reports whether c is a rule.
func (c Check) IsRule() bool { return c.Rule() != nil }

// IsGenerated reports whether c is a generated check.
func (c Check) IsGenerated() bool { return c.Generated() != nil }

// Field is one key of a JSON object and its value.
type Field struct {
	Key   string
	Value any
}

// Fields is a JSON object whose keys keep their order.
type Fields []Field

// MarshalJSON writes f as one object, its keys in order. Strings are
// written without HTML escapes, as errata list writes them.
func (f Fields) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	b.WriteByte('{')
	for i, field := range f {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := enc.Encode(field.Key); err != nil {
			return nil, err
		}
		b.WriteByte(':')
		if err := enc.Encode(field.Value); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Line is one labelled line of the text of errata list, such as run with
// the program's arguments.
type Line struct {
	Label string
	Text  string
}

// Quoter writes a value from the config for the text of errata list, so no
// value reads as two. The report package implements it.
type Quoter interface {
	// Value writes a name, an argument or a path.
	Value(s string) string
	// Spec writes an extractor's entry, NamedExtractor.Spec.
	Spec(spec any) string
}

// quoted is each of s written by q, joined with sep.
func quoted(q Quoter, s []string, sep string) string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = q.Value(v)
	}
	return strings.Join(out, sep)
}

// OrEmpty is s, or an empty list for nil, so the JSON holds [] and not null.
func OrEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
