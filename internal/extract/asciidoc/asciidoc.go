// Package asciidoc is the asciidoc extractor: the comments, sections, code
// blocks and table cells of an AsciiDoc file, read by its structure. What a
// rule writes and the values it gets are core/markup's, which the readers of
// every markup share; this package holds AsciiDoc's parse, written here
// since no maintained AsciiDoc parser in pure Go was found (#86). The parse
// reads the common forms of the language line by line, in one pass, and
// leaves out what it does not understand: preprocessor directives,
// attribute references and inline macros are read as the text they are.
package asciidoc

import (
	"context"
	"errors"
	"fmt"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/markup"
)

// Spec reads one target of File, an AsciiDoc file, as markup.Selector says:
// each comment, the prose of each section, each listing, source or literal
// block, or each cell of a table. A code block's language is the second
// positional attribute of its attribute line, [source,sh], or the word
// after a ``` fence.
type Spec struct {
	core.OneFile
	markup.Selector
}

// Kind is the asciidoc kind, which a rule writes under the key asciidoc.
var Kind = core.Kind[Spec]{Key: "asciidoc", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is t with only what decides where it reads: the file cleaned, and
// no max_bytes, since a limit does not change what is read.
func Place(t Spec) Spec {
	t.OneFile = t.Place()
	return t
}

// Resolve is t with its max_bytes written out, for errata list.
func Resolve(t Spec) Spec {
	t.OneFile = t.Resolved()
	return t
}

// Describe is what the kind's extractor returns: flat values, each literal.
func Describe(Spec) check.Description {
	return check.Description{Label: "asciidoc"}
}

// extractor reads its target of its file.
type extractor struct {
	src core.Source
	sel *markup.Select
}

// Build refuses what markup.Selector.Build refuses, and the file as every
// reader of one file refuses it, under the name asciidoc.
func Build(t Spec) (check.Extractor, error) {
	src, err := t.Check(Kind.Key)
	if err != nil {
		return nil, err
	}
	sel, err := t.Build(Kind.Key, src)
	if err != nil {
		return nil, err
	}
	return extractor{src: src, sel: sel}, nil
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(ctx, root)
}

// Extract returns the values of the file under root.
func (e extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	return core.ExtractOne(ctx, root, e.src, e)
}

// FromShared is FromData: an asciidoc read spends none of a set's budget.
func (e extractor) FromShared(data []byte, _ *core.Spent) ([]check.Value, error) {
	return e.FromData(data)
}

// FromData returns the values in data, the bytes of the file, as Extract
// does once it has read them.
func (e extractor) FromData(data []byte) ([]check.Value, error) {
	d, err := parse(data, e.sel, e.src.Limit)
	if errors.Is(err, errTooManyValues) {
		return nil, core.TooManyValues(e.src.File)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.src.File, err)
	}
	return e.sel.Values(e.src.File, d.text, d.items, d.lineAt)
}
