// Package text is the text extractor: core's regex search over the prose of
// a Markdown file, as a browser shows it.
package text

import (
	"fmt"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns capture group 1 of every match of Pattern in the prose of
// File, a Markdown file. The prose is the text of its paragraphs, headings
// and list items, each on one line and spaced as a browser shows it: a line
// wrap, and a tag a browser starts a line or a cell at (lineTags), is a
// space, any other tag joins the text around it, and a run of blanks is one
// space.
// Code span backticks, emphasis markers and link destinations are gone, and
// a link keeps its text. Code blocks, HTML blocks and front matter are left
// out, but for the text after an HTML block's end on its line, unless Code is
// set, which reads their lines instead.
type Spec struct {
	File     string `json:"file"`
	Pattern  string `json:"pattern"`
	Code     bool   `json:"code,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// maxProseRatio is how many times the size of its file the prose may be.
// The prose holds text copied from the file, and a space or a newline for
// each line break it drops, where the file has a byte the prose does not
// copy. With code:, a line of a code block whose indent splits a tab holds up
// to three spaces for that tab's one byte, after an indent the prose does not
// copy. So the prose is not much larger than its file, and the regex time
// limit, which scales with the file's limit, holds for it too. FuzzText
// checks that no prose reaches the ratio.
const maxProseRatio = 2

// textExtractor matches its pattern against the prose of its file, with the
// regex extractor's search: the same pattern, the same time limit, the same
// cap on values.
type textExtractor struct {
	search    core.Search
	code      bool
	parseTime time.Duration // the parse's time limit
}

// Build refuses what the regex extractor refuses, under the name text.
func Build(t Spec) (check.Extractor, error) {
	search, err := core.CompileSearch("text", t.File, t.Pattern, t.MaxBytes)
	if err != nil {
		return nil, err
	}
	return textExtractor{search: search, code: t.Code, parseTime: markdownTime(search.Limit)}, nil
}

// Extract returns group 1 of every match in the prose of the file under
// root. Its line is the line of the file where the match starts, and its
// text is the prose's.
func (e textExtractor) Extract(root string) ([]check.Value, error) {
	data, err := e.search.Read(root)
	if err != nil {
		return nil, err
	}
	return e.FromData(data)
}

// FromData returns the values in data, the bytes of the file, as Extract
// does once it has read them.
func (e textExtractor) FromData(data []byte) ([]check.Value, error) {
	size := len(data)
	doc, err := parseMarkdown(e.search.File, data, e.search.Limit, e.parseTime)
	if err != nil {
		return nil, err
	}
	p, err := doc.prose(e.code)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.search.File, err)
	}
	if err := checkProse(e.search.File, len(p.text), size, maxProseRatio); err != nil {
		return nil, err
	}
	matches, err := e.search.Matches(p.text)
	if err != nil {
		return nil, err
	}
	lines := core.NewLineCursor(doc.src) // it counts back for an offset before the one asked before
	out := []check.Value{}
	for _, m := range matches {
		if m[2] < 0 {
			continue
		}
		out = append(out, check.Value{
			Text: string(p.text[m[2]:m[3]]),
			At:   check.Location{File: e.search.File, Line: lines.At(p.offset(m[0]))},
		})
	}
	return out, nil
}

// checkProse refuses prose of more than ratio times the size of its file.
func checkProse(file string, prose, size, ratio int) error {
	if prose > ratio*size {
		return fmt.Errorf("%s: the prose is %d bytes, over the limit of %d, %d times the file's size; %s",
			file, prose, ratio*size, ratio, core.OtherWay)
	}
	return nil
}
