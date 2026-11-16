package mdparse

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/yuin/goldmark/v2/ast"

	"github.com/f-klement/errata/internal/extract/core/htmlparse"
)

// htmlBlocks is what the HTML parse found in the HTML blocks of a document
// that the prose reads through it (shownAsHTML): for each block, in file
// order, the runs of text, the line breaks and the comments, at offsets of
// the file. One scanner reads every block of the document, so the
// tokenizer and its buffer are made once (htmlparse.Scanner), the tokens
// of all the blocks together count against the document's limit, and a
// section that reads a block again reads what was found the first time.
type htmlBlocks struct {
	index  map[*ast.HTMLBlock]int
	events [][]htmlEvent
	err    error
}

// htmlEvent is one thing the HTML parse found in a block: a run of the
// file from from to to, or, for a break and a comment's start, the offset
// it stands for in from.
type htmlEvent struct {
	kind     htmlKind
	from, to int
}

type htmlKind uint8

const (
	eventText        htmlKind = iota // text, its blanks folded
	eventPre                         // text of a pre or a textarea, its lines kept
	eventBreak                       // a tag of a line element
	eventComment                     // a comment's <!--
	eventCommentText                 // a run of the comment's text
)

// htmlBlocks runs the HTML parse over the document's HTML blocks the first
// time it is asked, and returns what it found.
func (d *Doc) htmlBlocks() *htmlBlocks {
	if d.html != nil {
		return d.html
	}
	hb := &htmlBlocks{index: map[*ast.HTMLBlock]int{}}
	d.html = hb
	st := d.joinHTML(hb)
	hb.events = make([][]htmlEvent, len(st.ends))
	if len(st.ends) == 0 {
		return hb
	}
	scan := htmlparse.NewScanner(st.stream, d.maxTokens)
	scan.Blocks(st.ends)
	for scan.Next() {
		st.event(hb, scan)
	}
	if err := scan.Err(); err != nil {
		hb.err = st.lineError(d.Src, err)
	}
	return hb
}

// htmlStream is the text of the document's HTML blocks, joined into one
// stream for the HTML parse.
type htmlStream struct {
	// The blocks' lines are joined without the markers of the blocks around
	// them, as a block quote's; starts and froms map each line back: line i
	// starts at starts[i] in stream and at froms[i] in the file. ends holds
	// where each block ends in stream.
	stream              []byte
	starts, froms, ends []int
}

// joinHTML joins the lines of the HTML blocks shown as HTML, and indexes
// each block in hb by its place among them.
func (d *Doc) joinHTML(hb *htmlBlocks) *htmlStream {
	st := &htmlStream{}
	_ = ast.Walk(d.Root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.HTMLBlock)
		if !entering || !ok || !shownAsHTML(h, d.Src) {
			return ast.WalkContinue, nil
		}
		hb.index[h] = len(st.ends)
		for _, seg := range h.Value.Segments() {
			st.starts, st.froms = append(st.starts, len(st.stream)), append(st.froms, seg.Start)
			st.stream = append(st.stream, d.Src[seg.Start:seg.Stop]...)
		}
		st.ends = append(st.ends, len(st.stream))
		return ast.WalkSkipChildren, nil
	})
	return st
}

// line is the line of the stream the offset at is on.
func (st *htmlStream) line(at int) int { return sort.SearchInts(st.starts, at+1) - 1 }

// fileAt is the offset in the file of the offset at of the stream.
func (st *htmlStream) fileAt(at int) int {
	i := st.line(at)
	return st.froms[i] + at - st.starts[i]
}

// add records the run from to to of the stream in block b, split where its
// lines split, each part at its own offset of the file.
func (st *htmlStream) add(hb *htmlBlocks, b int, kind htmlKind, from, to int) {
	for i := st.line(from); from < to; i++ {
		end := to
		if i+1 < len(st.starts) {
			end = min(end, st.starts[i+1])
		}
		hb.events[b] = append(hb.events[b], htmlEvent{kind: kind, from: st.froms[i] + from - st.starts[i], to: st.froms[i] + end - st.starts[i]})
		from = end
	}
}

// event records the events of the token scan is at: text, a comment's
// opening and text, and a tag that breaks a line.
func (st *htmlStream) event(hb *htmlBlocks, scan *htmlparse.Scanner) {
	b, from, to := scan.Block(), scan.Start(), scan.End()
	switch scan.Kind() {
	case htmlparse.Text:
		st.add(hb, b, eventText, from, to)
	case htmlparse.Pre:
		st.add(hb, b, eventPre, from, to)
	case htmlparse.Comment:
		if start, end, ok := scan.CommentText(); ok {
			hb.events[b] = append(hb.events[b], htmlEvent{kind: eventComment, from: st.fileAt(from)})
			st.add(hb, b, eventCommentText, start, end)
		}
	case htmlparse.StartTag, htmlparse.EndTag:
		if scan.BreaksLine() {
			hb.events[b] = append(hb.events[b], htmlEvent{kind: eventBreak, from: st.fileAt(from)})
		}
	}
}

// lineError is err, the HTML parse's error, with the line of the file it
// stopped at when it names one.
func (st *htmlStream) lineError(src []byte, err error) error {
	var many *htmlparse.TooManyTokensError
	var nest *htmlparse.NestError
	switch {
	case errors.As(err, &many):
		return fmt.Errorf("line %d: %w", LineOf(src, st.fileAt(many.At)), err)
	case errors.As(err, &nest):
		return fmt.Errorf("line %d: %w", LineOf(src, st.fileAt(nest.At)), err)
	}
	return err
}

// htmlText appends the text a browser shows of HTML block h, as the HTML
// parse read it (htmlBlocks): its text spaced as copyText spaces it, each
// line of a pre or a textarea a line of prose, and a tag of
// htmlparse.LineTag the end of a line. Comments, scripts, styles and
// templates show nothing. An entity stays as written, as everywhere in the
// prose. Each piece of text maps back to its own line of the file.
func (p *Prose) htmlText(src []byte, h *ast.HTMLBlock) error {
	hb := p.doc.htmlBlocks()
	if hb.err != nil {
		return hb.err
	}
	for _, e := range hb.events[hb.index[h]] {
		switch e.kind {
		case eventText:
			p.copyText(src[e.from:e.to], e.from)
		case eventPre:
			for from := e.from; from < e.to; {
				line := bytes.IndexByte(src[from:e.to], '\n')
				if line < 0 {
					p.copyText(src[from:e.to], from)
					break
				}
				p.copyText(src[from:from+line], from)
				p.EndBlock()
				from += line + 1
			}
		case eventBreak:
			p.EndBlock()
		case eventComment, eventCommentText:
		}
	}
	p.EndBlock()
	return nil
}

// HTMLComments reads the comments of HTML block h as the HTML parse found
// them, when the prose reads h through it: a block a blank line ends, or a
// pre or a textarea element (shownAsHTML). For each comment, in order, it
// appends the comment's text to the prose and calls found with the offset
// of its <!-- in the file and where its text starts and ends in the prose.
// A comment in a script, a style, a textarea or a template is none, as in
// a browser and the html kind. ok is false for any other block, whose
// comments the caller reads itself.
func (p *Prose) HTMLComments(src []byte, h *ast.HTMLBlock, found func(open, start, end int)) (ok bool, err error) {
	if !shownAsHTML(h, src) {
		return false, nil
	}
	hb := p.doc.htmlBlocks()
	if hb.err != nil {
		return true, hb.err
	}
	events := hb.events[hb.index[h]]
	for i := 0; i < len(events); i++ {
		if events[i].kind != eventComment {
			continue
		}
		open, start := events[i].from, len(p.Text)
		for i+1 < len(events) && events[i+1].kind == eventCommentText {
			i++
			p.CopyFrom(src[events[i].from:events[i].to], events[i].from)
		}
		found(open, start, len(p.Text))
	}
	return true, nil
}
