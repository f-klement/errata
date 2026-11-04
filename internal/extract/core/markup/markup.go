// Package markup is what the readers of a document markup share, so that
// one rule reads a claim the same way whichever markup a page uses: the
// fields a rule writes to pick a comment, a section, a code block or a
// table cell (Selector) and their checks, the heading path and how it names
// a heading (Outline), the pieces a target reads and the values they give
// (Item, Values), and the comments of HTML. The markdown reader builds on
// it, and the html, asciidoc and rst readers do too; each keeps only the
// walk of its own markup. It lives below core, as mdparse does, since no
// reader imports another.
package markup

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// The targets a Selector reads.
const (
	TargetComment = "comment"
	TargetSection = "section"
	TargetCode    = "code"
	TargetTable   = "table"
)

// Selector is the fields a rule writes to read one Target of a document:
// each comment, the prose of each section, each code block, or each cell of
// a table. Section, a heading path such as "Install > Requirements", keeps
// only what lies under the headings it names, and is the section a section
// target reads. Without Pattern a value is the whole of what the target
// reads; with it, capture group 1 of every match of Pattern in it. Language
// keeps the code blocks of that language. Column names the header of the
// cells a table target reads, and Key with Row keeps the rows whose cell
// under the header Key is Row. A reader's Spec embeds it, so its keys are
// the Spec's own.
type Selector struct {
	Target   string `json:"target"`
	Section  string `json:"section,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Language string `json:"language,omitempty"`
	Column   string `json:"column,omitempty"`
	Key      string `json:"key,omitempty"`
	Row      string `json:"row,omitempty"`
}

// Select is a Selector checked and compiled for its file.
type Select struct {
	Target string
	// Path is the headings of the heading path, nil for the whole file.
	Path []string
	// Search is the pattern's search in the file, nil without a pattern.
	Search                     *core.Search
	Language, Column, Key, Row string
}

// Build refuses a selector whose target is missing or unknown, that sets a
// field its target does not read, that leaves out one its target needs, or
// whose heading path names an empty heading, and refuses its pattern as the
// regex extractor does; each refusal starts with kind. It returns the
// selector compiled for src.
func (s Selector) Build(kind string, src core.Source) (*Select, error) {
	if err := s.check(kind); err != nil {
		return nil, err
	}
	out := &Select{Target: s.Target, Language: s.Language, Column: s.Column, Key: s.Key, Row: s.Row}
	if s.Section != "" {
		path, err := HeadingPath(kind, s.Section)
		if err != nil {
			return nil, err
		}
		out.Path = path
	}
	if s.Pattern != "" {
		search, err := core.CompileSearchAt(kind, src, s.Pattern)
		if err != nil {
			return nil, err
		}
		out.Search = &search
	}
	return out, nil
}

// check refuses a target errata does not read, a field the target does not
// read, and a field it needs left out.
func (s Selector) check(kind string) error {
	switch s.Target {
	case "":
		return errors.New(kind + ": target is required; write comment, section, code or table")
	case TargetComment, TargetSection, TargetCode, TargetTable:
	default:
		return fmt.Errorf("%s: target %s is not one of comment, section, code and table", kind, core.Quote(s.Target))
	}
	for _, f := range []struct {
		name, value, target string
	}{
		{"language", s.Language, TargetCode},
		{"column", s.Column, TargetTable},
		{"key", s.Key, TargetTable},
		{"row", s.Row, TargetTable},
	} {
		if f.value != "" && s.Target != f.target {
			return fmt.Errorf("%s: %s is read only by target %s, and this one is %s", kind, f.name, f.target, s.Target)
		}
	}
	switch {
	case s.Target == TargetSection && s.Section == "":
		return errors.New(kind + ": target section needs section, the heading path, such as Install > Requirements")
	case s.Target == TargetTable && s.Column == "":
		return errors.New(kind + ": target table needs column, the header of the cells it reads")
	case (s.Key == "") != (s.Row == ""):
		return errors.New(kind + ": key and row go together: key names the header of the column that picks a row, and row the cell it must hold")
	}
	return nil
}

// HeadingPath is the headings a section names, split at > with the blanks
// around each trimmed. An empty heading is refused, with kind first.
func HeadingPath(kind, section string) ([]string, error) {
	path := strings.Split(section, ">")
	for i, h := range path {
		if path[i] = strings.TrimSpace(h); path[i] == "" {
			return nil, fmt.Errorf("%s: section %s names an empty heading; write headings between the >, such as Install > Requirements", kind, core.Quote(section))
		}
	}
	return path, nil
}

// Outline is a walk of a document's headings, in order, that tells which
// headings a heading path names and where their sections end. A heading
// names the path when the headings it sits under end with the path's: the
// nearest heading of a lower level before it, then the nearest of a lower
// level before that one, and so on, each heading's text equal to the
// path's, whole and in its case. Its section runs to the next heading of the
// same or a lower level, and holds the sections below it.
type Outline struct {
	path  []string
	stack []heading
	named int
}

// heading is a heading whose section the walk is in.
type heading struct {
	level int
	text  string
	named int // its number among the named headings, -1 when the path does not name it
}

// NewOutline is a walk for path.
func NewOutline(path []string) *Outline { return &Outline{path: path} }

// Heading enters a heading of level, a smaller level being a higher
// heading, with its text. It returns the numbers of the named headings
// whose sections it ends, whether the path names it, which gives it the
// next number from 0, and whether it is in the section of a named heading
// before it, as "## A" under "# A" is for the path A.
func (o *Outline) Heading(level int, text string) (ends []int, named, nested bool) {
	for len(o.stack) > 0 && o.stack[len(o.stack)-1].level >= level {
		if n := o.stack[len(o.stack)-1].named; n >= 0 {
			ends = append(ends, n)
		}
		o.stack = o.stack[:len(o.stack)-1]
	}
	for _, h := range o.stack {
		nested = nested || h.named >= 0
	}
	o.stack = append(o.stack, heading{level: level, text: text, named: -1})
	if named = o.names(); named {
		o.stack[len(o.stack)-1].named = o.named
		o.named++
	}
	return ends, named, nested
}

// names reports whether the texts of the headings of the stack end with
// the path.
func (o *Outline) names() bool {
	if len(o.stack) < len(o.path) {
		return false
	}
	tail := o.stack[len(o.stack)-len(o.path):]
	for i, h := range o.path {
		if tail[i].text != h {
			return false
		}
	}
	return true
}

// Columns is the index of the header Column among headers, and of the header
// Key when the select names one, -1 for none; the first of two headers alike
// counts. ok is whether the table holds every header the select names; a
// table that does not gives no value.
func (s *Select) Columns(headers []string) (column, key int, ok bool) {
	column, key = -1, -1
	for i, h := range headers {
		if column < 0 && h == s.Column {
			column = i
		}
		if key < 0 && s.Key != "" && h == s.Key {
			key = i
		}
	}
	return column, key, column >= 0 && (s.Key == "" || key >= 0)
}

// Item is one piece of the text a target reads, from Start to End. Line is
// the line its whole value is at, and the line of every match in it when
// Fixed is set: a comment's first line, a table cell's row. Otherwise a
// match is at the line of the file where it starts.
type Item struct {
	Start, End int
	Line       int
	Fixed      bool
}

// Values is the items, pieces of text, as values of file: each whole, or
// capture group 1 of every match of the pattern in each, searched on its
// own so that no match runs into the next, and all under one time limit and
// one cap on values. lineAt is the line of the file that holds an offset of
// text.
func (s *Select) Values(file string, text []byte, items []Item, lineAt func(int) int) ([]check.Value, error) {
	out := []check.Value{}
	at := func(line int) check.Location { return check.Location{File: file, Line: line} }
	if s.Search == nil {
		if len(items) > core.MaxValues {
			return nil, core.TooManyValues(file)
		}
		for _, it := range items {
			out = append(out, check.Value{Text: string(text[it.Start:it.End]), At: at(it.Line)})
		}
		return out, nil
	}
	texts := make([][]byte, len(items))
	for i, it := range items {
		texts[i] = text[it.Start:it.End]
	}
	matches, err := s.Search.MatchesIn(texts)
	if err != nil {
		return nil, err
	}
	for i, it := range items {
		for _, m := range matches[i] {
			if m[2] < 0 {
				continue
			}
			line := it.Line
			if !it.Fixed {
				line = lineAt(it.Start + m[0])
			}
			out = append(out, check.Value{Text: string(texts[i][m[2]:m[3]]), At: at(line)})
		}
	}
	return out, nil
}

// Comment is an HTML comment in a text: Open is where its <!-- starts, and
// Start to End its text, without the blanks at either end.
type Comment struct {
	Open, Start, End int
}

// HTMLComments is the comments, <!-- to -->, of the HTML in text from from.
// A <!-- inside a tag, such as in an attribute's value, is none, as in a
// browser: a < before a letter or a / starts a tag, which ends at the first
// > outside quotes. <!--> and <!---> are empty comments, as in CommonMark
// and a browser, and a <!-- with no --> after it is none. One pass over the
// text.
func HTMLComments(text []byte, from int) []Comment {
	var out []Comment
	for i := from; i < len(text); {
		switch rest := text[i:]; {
		case bytes.HasPrefix(rest, []byte("<!--")):
			c, ok := comment(text, i)
			if !ok {
				return out
			}
			out = append(out, c)
			i = c.End // what follows is blanks and the end, which hold no <
		case len(rest) > 1 && rest[0] == '<' && (rest[1] == '/' || isLetter(rest[1])):
			i = tagEnd(text, i)
		default:
			i++
		}
	}
	return out
}

// comment is the comment whose <!-- starts at open; ok is false when it has
// no end.
func comment(text []byte, open int) (c Comment, ok bool) {
	body := open + len("<!--")
	rest := text[body:] // searched in place: a copy of the rest for each comment is quadratic
	end := body         // <!--> and <!---> end at once
	if !bytes.HasPrefix(rest, []byte(">")) && !bytes.HasPrefix(rest, []byte("->")) {
		close := bytes.Index(rest, []byte("-->"))
		if close < 0 {
			return Comment{}, false
		}
		end = body + close
	}
	start, end := TrimBlanks(text, body, end)
	return Comment{Open: open, Start: start, End: end}, true
}

// tagEnd is the offset of the > that ends the tag whose < is at i, or the
// end of text for a tag with no end. A > inside a quoted value is the
// value's. A quote opens a value only right after =, blanks between them
// allowed, as a browser reads it; elsewhere, as in alt=don't, it is a byte
// of a name or of an unquoted value.
func tagEnd(text []byte, i int) int {
	var quote, last byte // last is the last byte before j that is no blank
	for j := i + 1; j < len(text); j++ {
		switch c := text[j]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case (c == '"' || c == '\'') && last == '=':
			quote = c
		case c == '>':
			return j
		}
		if !isBlank(text[j]) {
			last = text[j]
		}
	}
	return len(text)
}

// isLetter reports whether c is an ASCII letter.
func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// TrimBlanks is start and end moved past the blanks, spaces, tabs and line
// breaks, at either end of text[start:end].
func TrimBlanks(text []byte, start, end int) (int, int) {
	for start < end && isBlank(text[start]) {
		start++
	}
	for end > start && isBlank(text[end-1]) {
		end--
	}
	return start, end
}

// isBlank reports whether b is a space, a tab or a line break.
func isBlank(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' }
