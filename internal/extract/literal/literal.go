// Package literal is the literal extractor: it reads a path in the literal
// data of a file written in a programming language, such as a Django
// settings.py, a Laravel config/app.php or a vite.config.ts, without running
// it. One grammar of literal data serves every language, and the lexer
// table's row for the file's language (core.Language, its Syntax) says how
// the language spells it; whatever is not literal data comes back marked as
// computed (check.Value.NotLiteral), with the text the file wrote. ADR 0002
// is the decision.
package literal

import (
	"context"
	"fmt"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads Path in the literal data of File (Parse). Language names the
// row of the lexer table the file is read with; left out, the file's name
// chooses it (core.LanguageFor). Only a row with a Syntax is read.
type Spec struct {
	core.OneFile
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
}

// Kind is the literal kind, which a rule writes under the key literal.
var Kind = core.Kind[Spec]{Key: "literal", Place: Place, Resolve: Resolve, Describe: Describe}

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

// chosen is the language s reads its file with: the one it names, else the
// one the file's name chooses, else none.
func chosen(s Spec) string {
	if s.Language != "" {
		return s.Language
	}
	if l, ok := core.LanguageFor(s.File); ok && l.Syntax != nil {
		return l.Name
	}
	return ""
}

// Describe is what the kind's extractor returns: flat values, any of which
// may be computed.
func Describe(Spec) check.Description {
	return check.Description{Label: "literal", NotLiteral: true}
}

// names are the rows the reader reads, comma separated, for a message.
func names() string {
	var out []string
	for _, l := range core.Languages() {
		if l.Syntax != nil {
			out = append(out, l.Name)
		}
	}
	return strings.Join(out, ", ")
}

// extractor reads its path in the literal data of its file.
type extractor struct {
	core.Source
	path core.Path
	lang core.Language
}

// Build refuses a spec with no file or no path, a path that does not parse,
// and a language the reader has no row of, or a file whose name chooses
// none.
func Build(s Spec) (check.Extractor, error) {
	src, err := s.Check(Kind.Key)
	if err != nil {
		return nil, err
	}
	var lang core.Language
	var ok bool
	if s.Language != "" {
		if lang, ok = core.LanguageNamed(s.Language); !ok || lang.Syntax == nil {
			return nil, fmt.Errorf("literal: language %s is not one errata reads literal data of; write one of %s", core.Quote(s.Language), names())
		}
	} else if lang, ok = core.LanguageFor(s.File); !ok || lang.Syntax == nil {
		return nil, fmt.Errorf("literal: %s: its name chooses no language errata reads literal data of; set language: to one of %s", s.File, names())
	}
	path, err := core.CheckPath(Kind.Key, s.Path)
	if err != nil {
		return nil, err
	}
	return extractor{Source: src, path: path, lang: lang}, nil
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(ctx, root)
}

// Extract reads the file under root and returns every scalar its path
// selects.
func (e extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	return core.ExtractOne(ctx, root, e.Source, e)
}

// FromShared is FromData: a literal read spends none of a set's budget.
func (e extractor) FromShared(data []byte, _ *core.Spent) ([]check.Value, error) {
	return e.FromData(data)
}

// FromData returns the values in data, the bytes of the file, as Extract
// does once it has read them: it refuses a file denser than the node limit
// allows, then parses it and evaluates the path on its tree.
func (e extractor) FromData(data []byte) ([]check.Value, error) {
	if err := e.CheckNodes(data, core.LiteralFormat); err != nil {
		return nil, err
	}
	root, err := Parse(e.File, e.lang, data)
	if err != nil {
		return nil, err
	}
	return e.Values(e.path, root)
}
