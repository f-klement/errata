// Package comment is the comment extractor: core's regex search over the
// comments of a source file, which the lexer table finds by the file's
// language (core.Language).
package comment

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns capture group 1 of every match of Pattern in the comments of
// File. Language names the lexer's row the file is read with; left out, the
// file's name chooses it (core.LanguageFor). The comments are their bodies, without
// their delimiters, one after another with a line feed between two, so a
// match may run from one comment into the next.
type Spec struct {
	core.OneFile
	Pattern  string `json:"pattern"`
	Language string `json:"language,omitempty"`
}

// Kind is the comment kind, which a rule writes under the key comment.
var Kind = core.Kind[Spec]{Key: "comment", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is c with only what decides where it reads: the file cleaned, no
// max_bytes, and the language written out when the file's name chooses it,
// since naming the language the name chooses reads the same.
func Place(c Spec) Spec {
	c.OneFile = c.Place()
	c.Language = chosen(c)
	return c
}

// Resolve is c with its max_bytes and its language written out, for errata
// list.
func Resolve(c Spec) Spec {
	c.OneFile = c.Resolved()
	c.Language = chosen(c)
	return c
}

// chosen is the language c reads its file with: the one it names, else the
// one the file's name chooses, else none.
func chosen(c Spec) string {
	if c.Language != "" {
		return c.Language
	}
	if l, ok := core.LanguageFor(c.File); ok {
		return l.Name
	}
	return ""
}

// Describe is what the kind's extractor returns: flat values, each literal.
func Describe(Spec) check.Description {
	return check.Description{Label: "comment"}
}

// commentExtractor matches its pattern against the comments of its file, with
// the regex extractor's search: the same pattern, the same time limit, the
// same cap on values.
type commentExtractor struct {
	search core.Search
	lang   core.Language
}

// Build refuses what the regex extractor refuses, under the name comment, and
// a language the table has no row of, or a file whose name chooses none.
func Build(c Spec) (check.Extractor, error) {
	src, err := c.Check(Kind.Key)
	if err != nil {
		return nil, err
	}
	var lang core.Language
	var ok bool
	if c.Language != "" {
		if lang, ok = core.LanguageNamed(c.Language); !ok {
			return nil, fmt.Errorf("comment: language %s is not one errata lexes; write one of %s", core.Quote(c.Language), core.LanguageNames())
		}
	} else if lang, ok = core.LanguageFor(c.File); !ok {
		return nil, fmt.Errorf("comment: %s: its name chooses no language errata lexes; set language: to one of %s", c.File, core.LanguageNames())
	}
	search, err := core.CompileSearchAt(Kind.Key, src, c.Pattern)
	if err != nil {
		return nil, err
	}
	return commentExtractor{search: search, lang: lang}, nil
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e commentExtractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(ctx, root)
}

// Extract returns group 1 of every match in the comments of the file under
// root. Its line is the line of the file where the match starts.
func (e commentExtractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	return core.ExtractOne(ctx, root, e.search.Source, e)
}

// FromShared is FromData: a comment read spends none of a set's budget.
func (e commentExtractor) FromShared(data []byte, _ *core.Spent) ([]check.Value, error) {
	return e.FromData(data)
}

// FromData returns the values in data, the bytes of the file, as Extract
// does once it has read them. CRLF line endings read as LF.
func (e commentExtractor) FromData(data []byte) ([]check.Value, error) {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	t, err := comments(e.search.File, e.lang, data)
	if err != nil {
		return nil, err
	}
	if len(t.pieces) == 0 {
		return []check.Value{}, nil // a pattern that matches nothing finds nothing in no comment
	}
	matches, err := e.search.Matches(t.text)
	if err != nil {
		return nil, err
	}
	lines := core.NewLineCursor(data)
	out := []check.Value{}
	for _, m := range matches {
		if m[2] < 0 {
			continue
		}
		out = append(out, check.Value{
			Text: string(t.text[m[2]:m[3]]),
			At:   check.Location{File: e.search.File, Line: lines.At(t.offset(m[0]))},
		})
	}
	return out, nil
}

// text is the comments of a file one after another, and where each body
// starts in the text and in the file. It is never longer than the file: each
// comment's opener is at least one byte, and the line feed after it one.
type text struct {
	text   []byte
	pieces []piece
}

// piece is one comment's body: where it starts in the text and in the file.
type piece struct{ at, from int }

// comments is the text of the comments l finds in data, the bytes of file.
// A span of any kind that runs off the end of the file is refused
// (core.CheckSpan): past it the lexer cannot tell comment from code.
func comments(file string, l core.Language, data []byte) (text, error) {
	var t text
	for s := range l.Spans(data) {
		if err := core.CheckSpan(file, data, s); err != nil {
			return text{}, err
		}
		if s.Kind != core.SpanComment {
			continue
		}
		if len(t.pieces) > 0 {
			t.text = append(t.text, '\n')
		}
		t.pieces = append(t.pieces, piece{at: len(t.text), from: s.BodyStart})
		t.text = append(t.text, data[s.BodyStart:s.BodyEnd]...)
	}
	return t, nil
}

// offset is the offset in the file of off, an offset in the text: the byte
// the text copied, or for the line feed after a body, or the end of the text,
// the end of the body, which is on the comment's last line. The next body
// starts one past that line feed, so off is never past the end of its body.
func (t text) offset(off int) int {
	i := sort.Search(len(t.pieces), func(i int) bool { return t.pieces[i].at > off }) - 1
	p := t.pieces[i]
	return p.from + off - p.at
}
