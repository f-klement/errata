package htmlparse

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
)

// stackAfter runs ops on a nest, each "<x" a start tag that opens x, "<x/"
// one that does not (void, or self closing in svg), "</x" an end tag, and
// returns the elements open, or the refusal.
func stackAfter(ops string, headings bool) string {
	return stackIn(ops, headings, false)
}

// stackIn is stackAfter for a nest that may have elements open before its
// block, outside, as each HTML block of a Markdown file has.
func stackIn(ops string, headings, outside bool) string {
	n := nest{headings: headings, outside: outside}
	for _, op := range strings.Fields(ops) {
		var err string
		switch {
		case strings.HasPrefix(op, "</"):
			err = n.end(op[2:])
		case strings.HasSuffix(op, "/"):
			err = n.start(op[1:len(op)-1], false)
		default:
			err = n.start(op[1:], true)
		}
		if err != "" {
			return "refused"
		}
	}
	return strings.Join(n.stack, " ")
}

// The nest holds what a browser's tree builder holds: each row is the
// elements open after the tags, by the HTML standard's rules for those
// tags. Each row names the rule it holds.
func TestNestStack(t *testing.T) {
	for _, row := range [][2]string{
		{"<div", "div"},            // an element opens
		{"<pre <br/ <img/", "pre"}, // a void one does not
		{"<html <head </head <body <div </body </html", "div"},                   // html, head and body hold nothing and close nothing
		{"<p <span <div", "div"},                                                 // a p closer closes the p in scope, and what is above it
		{"<pre <p <button <div", "pre p button div"},                             // a button ends the p's scope
		{"<pre <h2 <h3", "pre h3"},                                               // a heading's start ends a heading on top
		{"<pre <h6 <h5", "pre h5"},                                               // h6 is a heading
		{"<pre <h2 <b <h3", "pre h2 b h3"},                                       // and nests below anything else
		{"<pre <ul <li <li", "pre ul li"},                                        // a list item ends the one open
		{"<pre <ul <li <div <li", "pre ul li"},                                   // past a div
		{"<pre <ul <li <span <li", "pre ul li"},                                  // past an element of no special kind
		{"<pre <ul <li <ol <li", "pre ul li ol li"},                              // not past a list
		{"<li <li", "li"},                                                        // with nothing below it
		{"<pre <dl <dt <dd", "pre dl dd"},                                        // dd ends dt
		{"<pre <dl <dd <dt", "pre dl dt"},                                        // and dt ends dd
		{"<pre <dl <dt <li", "pre dl dt li"},                                     // li ends no dt
		{"<pre <table <tr <table", "pre table"},                                  // a table in a table outside a cell ends it
		{"<pre <table <td <table", "pre table tbody tr td table"},                // inside a cell it nests
		{"<table <tr <td <td", "table tbody tr td"},                              // a cell ends the cell open
		{"<table <tr <td <tr", "table tbody tr"},                                 // a row ends the row open, and its cell
		{"<table <tr <td <table <tr", "table tbody tr td table tbody tr"},        // not a row of the table around
		{"<table <tbody <tr <thead", "table thead"},                              // a table section ends the one open
		{"<table <tr <td </tbody", "table"},                                      // the tbody a browser implies, which </tbody> closes
		{"<table <tbody <tr <td <table <thead", "table tbody tr td table thead"}, // not one of the table around
		{"<table <caption <td", "table tbody tr td"},                             // a cell ends a caption
		{"<select <option <option", "select option"},                             // an option ends an option
		{"<select <optgroup <option <optgroup", "select optgroup"},               // and an optgroup an option and an optgroup
		{"<div <pre </div", ""},                                                  // </div> ends the pre inside the div
		{"<details <pre </details", ""},                                          // and </details>
		{"<pre <div <span </div", "pre"},                                         // an end tag closes its element in scope
		{"<pre <div <table </div", "pre div table"},                              // not past a table
		{"<pre <div <td </td </div", "pre"},                                      // and the table's end lifts the scope
		{"<pre <span <div </span", "pre span div"},                               // no special one closes for an element of no special kind
		{"<pre <span <b </span", "pre"},                                          // other ones do
		{"<pre <p <button </p", "pre p button"},                                  // a button ends a p's scope
		{"<pre <p <span </p", "pre"},                                             // p
		{"<pre <td </td", "pre"},                                                 // a scope's end closes itself
		{"<pre <h1 <h2 </h1", "pre"},                                             // any heading's end ends the nearest heading
		{"<pre <h2 <table </h3", "pre h2 table"},                                 // in scope
		{"<pre </span </p </br </textarea </img </script", "pre"},                // no element of the name: ignored
		{"<pre </div </li </h2", "pre"},                                          // in one file, whatever the name
		{"<pre </pre", ""},                                                       // the pre ends
		{"<pre <pre </pre", "pre"},                                               // the inner one
		{"<pre <listing </pre", ""},                                              // the pre, past the listing
		{"<table <tr <td </table", ""},                                           // </table> closes its cell and row (table scope)
		{"<table <tr <th <th </table", ""},
		{"<table <tbody <tr <td </tbody </table", ""}, // and so do a section's and a row's end
		{"<table <tr <td </tr", "table tbody"},
		{"<table <tr <td <table </td", "table tbody tr td table"}, // not past a table inside the cell
		{"<a <a", "a"},                               // a link ends the link open
		{"<a <div <a", "div a"},                      // the first ends around the div, as the adoption agency does
		{"<table <td <col/", "table"},                // a col closes the cell (in cell, FuzzBrowser: <tABle><td><Col >0)
		{"<table <td <colgroup", "table colgroup"},   // so does a colgroup
		{"<table <tr <td <caption", "table caption"}, // and a caption, which opens in the table
		{"<td <col/ <caption", ""},                   // outside a table each is ignored
	} {
		if got := stackAfter(row[0], false); got != row[1] {
			t.Errorf("%q: got %q, want %q", row[0], got, row[1])
		}
	}
	// In a block of a Markdown file an element may have opened before the
	// block: an end tag the block does not place, which may end a pre or a
	// heading open, is refused; one the block places is followed.
	for _, row := range [][2]string{
		{"<pre </div", "refused"},
		{"<pre </li", "refused"},
		{"<pre </h2", "refused"},
		{"<pre </span </p </textarea", "pre"},
		{"<pre <table </div", "pre table"},
		{"<div </div </div", ""},
		{"<details <pre </details", ""},
		{"<div <pre </div </div", ""},
		{"<div <pre </div <pre </div", "refused"},
	} {
		if got := stackIn(row[0], false, true); got != row[1] {
			t.Errorf("outside %q: got %q, want %q", row[0], got, row[1])
		}
	}
	for _, row := range [][2]string{
		{"<h1", "h1"},
		{"<h1 <h2", "h2"},
		{"<h1 <b <h2", "refused"}, // a heading inside a heading
		{"<h1 <pre </h1", ""},
		{"<h1 </div", "h1"},
	} {
		if got := stackAfter(row[0], true); got != row[1] {
			t.Errorf("headings %q: got %q, want %q", row[0], got, row[1])
		}
	}
	if got := stackIn("<h1 </div", true, true); got != "refused" {
		t.Errorf("outside, headings: got %q", got)
	}
	// Elements nested past core.MaxDepth are refused, from the block's
	// start.
	if got := stackAfter(strings.Repeat("<b ", 1000), false); got == "refused" {
		t.Errorf("at the depth limit: refused")
	}
	if got := stackAfter(strings.Repeat("<b ", 1001), false); got != "refused" {
		t.Errorf("past the depth limit: read")
	}
}

// The scanner's own edges, each a mutant's: a text of nothing but the line
// feed after <pre> gives no token, <pre/> opens a pre outside svg, a self
// closing svg inside svg opens nothing, and a stray </svg> closes no svg.
func TestScannerEdges(t *testing.T) {
	if got := tokens("<pre>\n</pre>", 10); strings.Join(got, "|") != "<pre|</pre" {
		t.Errorf("pre of a line feed: %q", got)
	}
	if pre, _ := ends("<pre/>a"); pre != "true" {
		t.Errorf("<pre/>: in a pre %s", pre)
	}
	for src, want := range map[string]string{
		"<svg><svg/></svg><![CDATA[a]]>b":      "b",
		"</svg><svg><text><![CDATA[a]]></svg>": "a",
	} {
		var got []string
		for s := NewScanner([]byte(src), 100); s.Next(); {
			if s.Kind() == Text {
				got = append(got, string(s.Bytes()))
			}
		}
		if strings.Join(got, "") != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}

// A carriage return after <pre> is the pre's first line feed: the HTML
// standard reads a lone CR, and a CR before a LF, as one LF before it
// tokenizes, so a browser drops it and reopens no formatting element
// before it. The scanner reads the source folded, as its callers hand it
// over (core.LineFeeds). A textarea's first line feed is the tag's too,
// and a line feed alone in a pre leaves it empty, so the one after a tag a
// browser ignores is dropped as well. The planted breaks: no line feed
// dropped, which refuses the find (FuzzHTML, the round after 04c52e9); the
// line feed alone not taken as alone.
func TestPreFirstCarriageReturn(t *testing.T) {
	for src, want := range map[string]string{
		"<pre>\rx</pre>":               "<pre|pre:x|</pre",
		"<pre>\r\nx</pre>":             "<pre|pre:x|</pre",
		"<pre>\r</pre>":                "<pre|</pre",
		"<pre>\r\rx</pre>":             "<pre|pre:\nx|</pre",
		"<pre>\r</li>\rx":              "<pre|</li|pre:x",
		"<textarea>\rx</textarea>":     "<textarea|pre:x|</textarea",
		"<p ><Code ><pre>\r<p></Code>": "<p|<code|<pre|<p|</code",
	} {
		if got := strings.Join(tokens(string(core.LineFeeds([]byte(src))), 100), "|"); got != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}

// A textarea inside svg or math is a foreign element, as x/net's parser
// reads it: the tokenizer reads what follows as markup, not raw text, and
// the line feed after the tag is text. A self closing svg or math opens
// none, so a textarea after it is HTML's. The planted breaks: the textarea
// read raw in foreign content (its <b> is text), its line feed dropped, and
// a self closing svg taken to open one.
func TestTextareaInForeignContentIsMarkup(t *testing.T) {
	for src, want := range map[string]string{
		"<svg><textarea>a<b>c</b></textarea></svg>": "<svg|<textarea|<b|text:c|</b|</textarea|</svg", // a: svg text outside a text element
		"<math><textarea>\nx</textarea></math>":     "<math|<textarea|text:\nx|</textarea|</math",
		"<svg/><textarea>a<b>c</textarea>":          "<svg|<textarea|pre:a<b>c|</textarea",
		"<math/><![CDATA[a]]>b":                     "<math|text:b",
		"<textarea>\na<b></textarea>":               "<textarea|pre:a<b>|</textarea",
	} {
		if got := strings.Join(tokens(src, 100), "|"); got != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}

// The token limit holds exactly, fresh tokenizers counted: a scan reads at
// the count it makes, and is refused one under it.
func TestTheTokenLimitIsExact(t *testing.T) {
	src, ends := []byte("<div a\n<p>b\n<div\nc"), []int{7, 12, 17, 18}
	count := func(max int64) (int64, error) {
		s := NewScanner(src, max)
		s.Blocks(ends)
		for s.Next() {
		}
		return s.tokens, s.Err()
	}
	n, err := count(1 << 30)
	if err != nil || n < 2*restartTokens {
		t.Fatalf("%d tokens, %v", n, err)
	}
	if _, err := count(n); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	if _, err := count(n - 1); err == nil {
		t.Errorf("under the limit: read")
	}
}

// What a scan of blocks counts, by hand: a comment left open in the first
// block reads to the end (1 token), its 7 bytes past the block's end count
// 2, the block is read again by a tokenizer of its own (1024, and its
// comment 1), and the next block starts a fresh one (1024, and its text
// 1): 2053. A tag left open to the end gives no token; its 2 bytes past
// the block count 1, then the same two tokenizers and one token: 2050.
func TestWhatBlocksCount(t *testing.T) {
	for _, c := range []struct {
		blocks []string
		want   int64
	}{
		{[]string{"<!--a", "bcdefgh"}, 2053},
		{[]string{`<a b="x`, "yz"}, 2050},
	} {
		src := strings.Join(c.blocks, "")
		s := NewScanner([]byte(src), 1<<20)
		s.Blocks([]int{len(c.blocks[0]), len(src)})
		for s.Next() {
		}
		if s.Err() != nil || s.tokens != c.want {
			t.Errorf("%q: %d tokens, %v; want %d", c.blocks, s.tokens, s.Err(), c.want)
		}
	}
}

// The scan stops past the passes its input allows, with its refusal: no
// input reaches the bound (TestRestartsCount reads the densest shape), so
// a test lowers it.
func TestTheStepBoundRefuses(t *testing.T) {
	s := NewScanner([]byte("<p>a</p><p>b</p>"), 100)
	s.maxSteps = 3
	for s.Next() {
	}
	if s.Err() == nil || s.Err().Error() != "the HTML scan ran past the steps its input allows" {
		t.Errorf("got %v", s.Err())
	}
}

// afeAfter is the elements open and the list of active formatting
// elements after ops, "<x" and "</x" tags and any other op text; in the
// list "name" is open, "name~" closed by a block's end, "|" a marker; or
// the refusal.
func afeAfter(ops string, headings bool) string {
	n := nest{headings: headings}
	for _, op := range strings.Fields(ops) {
		var err string
		switch {
		case strings.HasPrefix(op, "</"):
			err = n.end(op[2:])
		case strings.HasPrefix(op, "<"):
			err = n.start(op[1:], true)
		default: // text, before which a browser reopens formatting elements
			err = n.reconstruct()
		}
		if err != "" {
			return "refused"
		}
	}
	var out []string
	for _, e := range n.afe {
		switch e.at {
		case -2:
			out = append(out, "|")
		case -1:
			out = append(out, e.name+"~")
		default:
			out = append(out, e.name)
		}
	}
	return strings.Join(n.stack, " ") + " / " + strings.Join(out, " ")
}

// The nest keeps the browser's list of active formatting elements: an
// element closed by a block's end stays to be reopened, its own end tag
// takes it off, a cell puts a marker that its end clears to, three alike at
// most follow the last marker, and a formatting element's end tag with a
// block open inside it, which the adoption agency rebuilds, is refused
// inside a pre or a heading. A heading that opens where the browser would
// reopen a formatting element, while one is open, is refused.
func TestFormattingList(t *testing.T) {
	for _, row := range [][2]string{
		{"<b <i </b", " / i~"},                               // </b> closes i too, which stays to be reopened
		{"<b <div </div </b", " / "},                         // its own end takes it off
		{"<div <b </div", " / b~"},                           // a block's end closes it
		{"<div <b </div </b", " / "},                         // and its end tag then takes it off
		{"<b <div </b", "div / "},                            // around a block: off the list and the stack (adopt)
		{"<pre <b <div </b", "refused"},                      // inside a pre: refused
		{"<table <tr <td <b </td", "table tbody tr / "},      // a cell's marker, cleared with it
		{"<b <td", "b / b"},                                  // a cell outside a table opens nothing
		{"<b <b <b <b", "b b b b / b b b"},                   // three alike at most
		{"<b <b <b <i <b </i", "b b b / b b b~"},             // the list shifts under the open ones
		{"<table <td <b", "table tbody tr td b / | b"},       //
		{"<a <a", "a / a"},                                   // a link ends a link
		{"<a <div <a", "div a / a"},                          // around a block, the first leaves the list and the stack
		{"<h1 <a <table <a", "h1 table a / a"},               // past a table, the first leaves the list and the stack
		{"<h1 <a <table <a </table <h1", "h1 / a~"},          // so the next heading ends the first
		{"<a <table <b <a </b", "table / a~"},                // the entries above move down with their elements
		{"<a <table <p <a <div", "table div / a~"},           // and so do the kinds: the div closes the p
		{"<a <table <a", "table a / a"},                      // the link at the bottom too
		{"<a <table <div <div <a </div </div", "table / a~"}, // each of two alike moves down
		{"<b <b <b <b </b </b </b </b", " / "},               // the one the clause took off has no entry
		// The adoption agency, outside a pre or a heading (adopt): the
		// fourth formatting element and on below the block leave the list
		// and the stack, an element not on the list leaves the stack, the
		// clone's entry goes after the nearest one kept, and after eight
		// passes the clone stays open (foreign review, round 2).
		{"<b <i <u <s <em <div </b", "u s em div / u s em"},
		{"<b <span <div </b", "div / "},
		{"<b <i" + strings.Repeat(" <div", 8) + " </b", "i" + strings.Repeat(" div", 8) + " b / i b"},
		{"<b" + strings.Repeat(" <div", 7) + " </b", strings.TrimSpace(strings.Repeat(" div", 7)) + " / "},
		{"<b <i <u <s <div </b", "i u s div / i u s"},                                                // three below the block stay
		{"<u <b <i <div </b", "u i div / u i"},                                                       // an entry before the element's
		{"<b" + strings.Repeat(" <div", 8) + " <i </b", "div div div div div div div div b i / b i"}, // no bookmark: the clone's entry stays in place
	} {
		if got := afeAfter(row[0], false); got != row[1] {
			t.Errorf("%q: got %q, want %q", row[0], got, row[1])
		}
	}
	if got := afeAfter("<h1 <div <a </div <h2", true); got != "h2 / a~" {
		t.Errorf("a heading before the link reopens: %q", got) // no text came: the h2 ends the h1
	}
	if got := afeAfter("<h1 <div <a </div x <h2", true); got != "refused" {
		t.Errorf("a heading after text reopened the link: %q", got) // the h2 opens inside the h1
	}
	for _, row := range [][2]string{
		{"<div <b </div x", "b / b"},                  // text reopens it
		{"<div <b <i </div x", "b i / b i"},           // in order
		{"<div <b <i </i </div x", "b / b"},           // not one its end took off
		{"<div <b </div <span", "b span / b"},         // an inline start tag reopens it
		{"<div <b </div <div", "div / b~"},            // a block does not
		{"<table <td <b </td x", "table tbody tr / "}, // a cell's end clears it
	} {
		if got := afeAfter(row[0], false); got != row[1] {
			t.Errorf("%q: got %q, want %q", row[0], got, row[1])
		}
	}
	if got := afeAfter("<h1 <div <a </div </a <h2", true); got != "h2 / " {
		t.Errorf("the link ended first: %q", got)
	}
}

// Closing a formatting element whose entry the Noah's Ark clause took off
// the list looks no further back than the last cell: 230 nested cells,
// each holding 39 formatting elements a block's end closed, cost a page of
// blocks of twelve <b>s no more than twice its time without them, where a
// search of the whole list took several times as long (delta review 3 of
// a13dfeb, finding 3).
// The planted break: the search past the markers.
func TestTheFormattingSearchStopsAtACell(t *testing.T) {
	var cells strings.Builder
	for range 230 {
		cells.WriteString("<table><tr><td><div>")
		for _, f := range []string{"a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt"} {
			cells.WriteString(strings.Repeat("<"+f+">", 3))
		}
		cells.WriteString("</div>")
	}
	// The page runs in one more cell, with nothing of its own to reopen, so
	// that what it costs is the search, not the reopening a browser makes
	// of the 39 closed elements of a cell before text in it.
	cells.WriteString("<table><tr><td>")
	body := strings.Repeat("<div>"+strings.Repeat("<b>x", 12)+"</div>", 10_000) // 9 entries dropped a block
	scan := func(src string) time.Duration {
		fastest := time.Duration(math.MaxInt64)
		for range 3 {
			start := time.Now()
			s := NewScanner([]byte(src), 1<<30)
			s.Headings()
			for s.Next() {
			}
			if s.Err() != nil {
				t.Fatal(s.Err())
			}
			fastest = min(fastest, time.Since(start))
		}
		return fastest
	}
	alone, after := scan(body), scan(cells.String()+body)
	t.Logf("the page took %v, after the cells %v", alone, after)
	if after > 2*alone+50*time.Millisecond {
		t.Errorf("after 230 cells of closed formatting elements the page took %v, over twice its %v", after, alone)
	}
}

// TestDoctypeMode holds the page's mode to the HTML standard's rules: the
// first doctype, before anything but blanks and comments, sets it, a later
// one changes nothing, and a Markdown file's blocks stay in standards mode
// whatever doctype they hold.
func TestDoctypeMode(t *testing.T) {
	quirks := func(src string, blocks bool) bool {
		s := NewScanner([]byte(src), MaxTokens(int64(len(src))))
		if blocks {
			s.Blocks([]int{len(src)})
		}
		for s.Next() {
		}
		if s.Err() != nil {
			t.Fatalf("%q: %v", src, s.Err())
		}
		return s.nest.quirks
	}
	const legacy = `<!DOCTYPE html PUBLIC "-//W3O//DTD W3 HTML 3.0//EN">`
	for src, want := range map[string]bool{
		"<p>a":                          true,
		"<!DOCTYPE html>":               false,
		"<!DOCTYPE HTML>":               false, // the standard lowercases the name; x/net does not
		" \n<!-- c --><!doctype html >": false,
		"a<!DOCTYPE html>":              true,
		"<p><!DOCTYPE html>":            true,
		"<!DOCTYPE html>" + legacy:      false,
		legacy + "<!DOCTYPE html>":      true,
		"<!DOCTYPE svg>":                true,
		`<!DOCTYPE html SYSTEM "about:legacy-compat">`:                                                           false,
		`<!DOCTYPE html PUBLIC "-//W3C//DTD HTML 4.01 Transitional//EN">`:                                        true,
		`<!DOCTYPE html PUBLIC "-//W3C//DTD HTML 4.01 Transitional//EN" "http://www.w3.org/TR/html4/loose.dtd">`: false,
		// x/net's readings where the standard reads standards mode (doctypeQuirks).
		`<!DOCTYPE html PUBLIC "-//W3C//DTD HTML 4.01//EN">`:          true,
		`<!DOCTYPE html SYSTEM "about:legacy-compat" x>`:              true,
		`<!DOCTYPE html PUBLIC "-/W3D/DTD HTML 4.0 Transitional/EN">`: true,
	} {
		if got := quirks(src, false); got != want {
			t.Errorf("%q: quirks %v, want %v", src, got, want)
		}
	}
	if quirks(legacy, true) {
		t.Errorf("%q in a Markdown block: quirks, want standards mode", legacy)
	}
}

// broken is the first way the nest's indexes disagree with its stack, or
// "": open, afeAt and stack are as long as each other; each element's
// entry in pos is its name's and holds its index; each name's indexes rise
// and name elements of that name; each afeAt entry and each open entry of
// afe point at each other; and the kind lists rise, stay in range, and
// specials, bounds and heading hold exactly the elements of their kind.
func (n *nest) broken() string {
	if len(n.open) != len(n.stack) || len(n.afeAt) != len(n.stack) {
		return fmt.Sprintf("stack %d, open %d, afeAt %d", len(n.stack), len(n.open), len(n.afeAt))
	}
	for i, name := range n.stack {
		if n.open[i] != n.pos[name] || !slices.Contains(n.open[i].at, i) {
			return fmt.Sprintf("open[%d] is not the entry of %s that holds %d", i, name, i)
		}
	}
	for name, e := range n.pos {
		for m, at := range e.at {
			if at < 0 || at >= len(n.stack) || n.stack[at] != name || m > 0 && e.at[m-1] >= at {
				return fmt.Sprintf("pos[%s] is %v", name, e.at)
			}
		}
	}
	for i, j := range n.afeAt {
		if j >= 0 && (j >= len(n.afe) || n.afe[j].at != i || n.afe[j].name != n.stack[i]) {
			return fmt.Sprintf("afeAt[%d]=%d does not point back", i, j)
		}
	}
	for j, e := range n.afe {
		if e.at >= 0 && (e.at >= len(n.stack) || n.afeAt[e.at] != j) {
			return fmt.Sprintf("afe[%d] at %d does not point back", j, e.at)
		}
	}
	lists := map[string][]int{"bounds": n.bounds, "buttons": n.buttons, "specials": n.specials,
		"notADP": n.notADP, "heading": n.heading, "tabular": n.tabular, "sections": n.sections, "htmls": n.htmls}
	for list, idx := range lists {
		for m, i := range idx {
			if i < 0 || i >= len(n.stack) || m > 0 && idx[m-1] >= i {
				return fmt.Sprintf("%s is %v over %d elements", list, idx, len(n.stack))
			}
		}
	}
	var specials, bounds, heading, htmls []int
	for i, name := range n.stack {
		if namespace(name) == "" {
			htmls = append(htmls, i)
		}
		if special[name] || isHeading(name) {
			specials = append(specials, i)
		}
		if scopeEnds[name] {
			bounds = append(bounds, i)
		}
		if isHeading(name) {
			heading = append(heading, i)
		}
	}
	if !slices.Equal(specials, n.specials) || !slices.Equal(bounds, n.bounds) || !slices.Equal(heading, n.heading) || !slices.Equal(htmls, n.htmls) {
		return fmt.Sprintf("specials %v, bounds %v, heading %v, htmls %v over %v", n.specials, n.bounds, n.heading, n.htmls, n.stack)
	}
	return ""
}

// The nest's indexes stay in step with its stack after every token, over
// random tag soup read once as a page with headings followed and once as
// two Markdown blocks (html delta review 6, finding 1). The fixed rows above
// and the differential properties see a slip only where it changes a pre's,
// heading's or row's text. The planted break: afeRemove's loop that
// re-indexes the entries after the one removed starting one entry late.
func TestTheNestsIndexesKeepInStep(t *testing.T) {
	names := strings.Fields(`a b i code em nobr font table tbody tr td th caption p div pre h1 h2
		li ul button xmp span img br select option object marquee template svg math g title foreignObject mi style`)
	rapid.Check(t, func(t *rapid.T) {
		var b strings.Builder
		var cuts []int
		for range rapid.IntRange(0, 60).Draw(t, "tokens") {
			name := rapid.SampledFrom(names).Draw(t, "name")
			switch rapid.IntRange(0, 2).Draw(t, "kind") {
			case 0:
				b.WriteString("<" + name + ">")
			case 1:
				b.WriteString("</" + name + ">")
			default:
				b.WriteString("x")
			}
			cuts = append(cuts, b.Len())
		}
		src := b.String()
		whole := NewScanner([]byte(src), math.MaxInt64)
		whole.Headings()
		read := []*Scanner{whole}
		if len(cuts) > 1 {
			mid := cuts[rapid.IntRange(0, len(cuts)-2).Draw(t, "cut")]
			blocks := NewScanner([]byte(src), math.MaxInt64)
			blocks.Blocks([]int{mid, len(src)})
			read = append(read, blocks)
		}
		for _, s := range read {
			for s.Next() {
				if msg := s.nest.broken(); msg != "" {
					t.Fatalf("%q at %d: %s", src, s.start, msg)
				}
			}
		}
	})
}
