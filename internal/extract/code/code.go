// Package code is the code extractor: it reads the value of one name a
// source file declares, such as a version constant in version.go or
// __version__ in __init__.py, without compiling or running it (ADR 0002).
// A Go file is read with the standard library's go/parser, and its
// constants evaluated with go/constant as the compiler does, iota and typed
// constants included. A file of any other language is read through the
// literal reader's parse (core/litparse), with the members of its classes.
// What the program computes comes back marked not literal.
package code

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/litparse"
)

// Spec reads the value File gives Name, or gives Const, which must be
// declared a constant. Language names the row of the lexer table the file
// is read with; left out, the file's name chooses it.
type Spec struct {
	core.OneFile
	Name     string `json:"name,omitempty"`
	Const    string `json:"const,omitempty"`
	Language string `json:"language,omitempty"`
}

// Kind is the code kind, which a rule writes under the key code.
var Kind = core.Kind[Spec]{Key: "code", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is s with only what decides where it reads: the file cleaned, no
// max_bytes, and the language written out when the file's name chooses it.
func Place(s Spec) Spec {
	s.OneFile = s.Place()
	s.Language = chosen(s)
	return s
}

// Resolve is s with its max_bytes and its language written out, for errata
// list.
func Resolve(s Spec) Spec {
	s.OneFile = s.Resolved()
	s.Language = chosen(s)
	return s
}

// Describe is what the kind's extractor returns: one flat value, which may
// be computed.
func Describe(Spec) check.Description {
	return check.Description{Label: "code", NotLiteral: true}
}

// golang is the name of the row the Go reader reads.
const golang = "go"

// reads reports whether the reader reads l: Go, and every row with a Syntax.
func reads(l core.Language) bool { return l.Name == golang || l.Syntax != nil }

// chosen is the language s reads its file with: the one it names, else the
// one the file's name chooses, else none.
func chosen(s Spec) string {
	if s.Language != "" {
		return s.Language
	}
	if l, ok := core.LanguageFor(s.File); ok && reads(l) {
		return l.Name
	}
	return ""
}

// names are the rows the reader reads, comma separated, for a message.
func names() string {
	var out []string
	for _, l := range core.Languages() {
		if reads(l) {
			out = append(out, l.Name)
		}
	}
	return strings.Join(out, ", ")
}

// extractor reads one name of its file.
type extractor struct {
	core.Source
	parts    []string
	constant bool
	lang     core.Language
}

// Build refuses a spec with no file, none or both of name and const, a name
// that is no dotted list of names, a language the reader has no row of or a
// file whose name chooses none, a dot in a Go name, and const for a
// language that declares no constant.
func Build(s Spec) (check.Extractor, error) {
	src, err := s.Check(Kind.Key)
	if err != nil {
		return nil, err
	}
	name, field := s.Name, "name"
	switch {
	case s.Name == "" && s.Const == "":
		return nil, errors.New("code: name or const is required: the name whose value to read, such as name: __version__ or const: DefaultPort")
	case s.Name != "" && s.Const != "":
		return nil, errors.New("code: write name or const, not both; const reads only a name declared a constant")
	case s.Const != "":
		name, field = s.Const, "const"
	}
	parts := strings.Split(name, ".")
	for _, p := range parts {
		if !identifier(p) {
			return nil, fmt.Errorf("code: %s %s is not a name, or names joined by dots, such as Config.PORT", field, core.Quote(name))
		}
	}
	var lang core.Language
	var ok bool
	if s.Language != "" {
		if lang, ok = core.LanguageNamed(s.Language); !ok || !reads(lang) {
			return nil, fmt.Errorf("code: language %s is not one errata reads code of; write one of %s", core.Quote(s.Language), names())
		}
	} else if lang, ok = core.LanguageFor(s.File); !ok || !reads(lang) {
		return nil, fmt.Errorf("code: %s: its name chooses no language errata reads code of; set language: to one of %s", s.File, names())
	}
	switch {
	case lang.Name == golang && len(parts) > 1:
		return nil, fmt.Errorf("code: %s %s has a dot, and a Go file's top level names have none; name the const or var itself", field, core.Quote(name))
	case s.Const != "" && lang.Name != golang && len(lang.Syntax.Constants) == 0:
		return nil, fmt.Errorf("code: %s declares no constant, so const: names nothing; write name: %s", lang.Name, s.Const)
	}
	return extractor{Source: src, parts: parts, constant: s.Const != "", lang: lang}, nil
}

// identifier reports whether s is one name: a letter, _ or $, then those
// and digits. A byte past ASCII stands in a name too.
func identifier(s string) bool {
	for i := range len(s) {
		c := s[i]
		letter := c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return s != ""
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(ctx, root)
}

// Extract reads the file under root and returns the value of its name.
func (e extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	return core.ExtractOne(ctx, root, e.Source, e)
}

// FromShared is FromData: a code read spends none of a set's budget.
func (e extractor) FromShared(data []byte, _ *core.Spent) ([]check.Value, error) {
	return e.FromData(data)
}

// FromData returns the value of the name in data, the bytes of the file, as
// Extract does once it has read them: it refuses a file denser than the node
// limit allows, then reads the name. A file that declares no such name
// gives no value. A name that holds a list, a mapping or a class, and a
// const: whose name is declared no constant, are errors.
func (e extractor) FromData(data []byte) ([]check.Value, error) {
	if err := e.CheckNodes(data, core.LiteralFormat); err != nil {
		return nil, err
	}
	n, isConst, found, err := e.read(data)
	if err != nil || !found {
		return []check.Value{}, err
	}
	name := strings.Join(e.parts, ".")
	if n.Shape != core.Scalar {
		return nil, fmt.Errorf("%s: line %d: %s holds %s, not a value; name one of its members, or read it with the literal extractor's path", e.File, n.Line, name, n.Shape)
	}
	if e.constant && !isConst {
		return nil, fmt.Errorf("%s: line %d: %s is not a constant: the file does not declare it one, or changes it after it does, so the program may change it; write name: to read it anyway", e.File, n.Line, name)
	}
	return []check.Value{{Text: n.Text, At: check.Location{File: e.File, Line: n.Line}, NotLiteral: n.NotLiteral}}, nil
}

// read is the node of the name in data, and whether it is declared a
// constant; found is false when the file declares no such name.
func (e extractor) read(data []byte) (n *core.Node, isConst, found bool, err error) {
	if e.lang.Name == golang {
		g, err := readGo(e.File, data, e.Limit)
		if err != nil {
			return nil, false, false, err
		}
		return g.value(e.parts[0])
	}
	tree, err := litparse.ParseMembers(e.File, e.lang, data)
	if err != nil {
		return nil, false, false, err
	}
	n = tree.Root
	for _, p := range e.parts {
		if n.Shape == core.Scalar && n.NotLiteral {
			// A class the file changes as a whole, as C.X = 2 does: its
			// members are not literal either, which a missing value would
			// hide, and no constant, which const: refuses.
			return n, false, true, nil
		}
		if n.Shape != core.Mapping || n.Field[p] == nil {
			return nil, false, false, nil
		}
		n, isConst = n.Field[p], tree.Const(n, p)
	}
	return n, isConst, true, nil
}
