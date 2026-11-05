package html

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/htmlparse"
	"github.com/f-klement/errata/internal/extract/core/markup"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// errataReads is what the html kind reads of src, as
// extracttest.BrowserRead holds it, or its refusal.
func errataReads(src string) (extracttest.BrowserRead, error) {
	out, _, err := errataReadsFoster(src)
	return out, err
}

// errataReadsFoster is errataReads, and whether src puts text or an element
// in a table outside a cell, which a browser moves before the table and
// errata reads where it is.
func errataReadsFoster(src string) (extracttest.BrowserRead, bool, error) {
	var out extracttest.BrowserRead
	foster := false
	e, err := Build(Spec{OneFile: core.OneFile{File: "f"}, Selector: markup.Selector{Target: markup.TargetCode}})
	if err != nil {
		panic(err)
	}
	values, err := e.(extractor).FromData([]byte(src))
	if err != nil {
		return out, false, err
	}
	for _, v := range values {
		out.Pres = append(out.Pres, v.Text)
	}
	sel, err := markup.Selector{Target: markup.TargetTable, Column: "x"}.Build("html", core.Source{File: "f", Limit: core.DefaultMaxBytes})
	if err != nil {
		panic(err)
	}
	r := newReading(sel, core.LineFeeds([]byte(src)), core.DefaultMaxBytes) // folded, as FromData hands it over
	r.trace = func(kind, text string) {
		switch kind {
		case "heading":
			out.Headings = append(out.Headings, text)
		case "row":
			out.Rows = append(out.Rows, text)
		default:
			foster = true
		}
	}
	err = r.read()
	out.StandardOnly = r.s.StandardOnly()
	if err != nil {
		return out, foster, err
	}
	slices.Sort(out.Pres)
	slices.Sort(out.Headings)
	slices.Sort(out.Rows)
	return out, foster, nil
}

// refusedFor is why errata may refuse HTML whose tree the tree builder
// gave: "heading" for a heading inside a heading, only where the tree has
// one; "formatting" for the adoption agency's rebuild around a pre or a
// heading, only where the tree builder rebuilt one of the refused name
// around or inside a pre or a heading (BrowserRead.RebuiltIn), which a
// reconstruction can look like too, so the property tests cap how often
// it may happen. It is "" when the refusal is not one of these.
func refusedFor(err error, want extracttest.BrowserRead) string {
	switch msg := err.Error(); {
	case strings.Contains(msg, "a heading opens inside a heading") && want.NestedHeading:
		return "heading"
	case strings.Contains(msg, "errata does not follow") && want.RebuiltIn[refusedName(msg)]:
		return "formatting"
	}
	return ""
}

// sameRead fails t unless errata reads src as the browser does, or refuses
// it for a reason refusedFor accepts, which it returns. It compares nothing
// where the oracle is not the standard: "standard" where the reading says
// it followed the standard where x/net does not, "template" where x/net
// drops a template.
func sameRead(t interface {
	Helper()
	Fatalf(string, ...any)
}, src string) string {
	t.Helper()
	got, err := errataReads(src)
	switch {
	case got.StandardOnly:
		return "standard" // errata followed the standard where x/net does not
	case extracttest.XNetDropsTemplate(src):
		return "template" // x/net drops the template and all after it
	}
	want := extracttest.BrowserReads(src, htmlparse.LineTag, true)
	if err != nil {
		why := refusedFor(err, want)
		if why == "" {
			t.Fatalf("%q: refused: %v", src, err)
		}
		return why
	}
	for _, c := range []struct {
		what      string
		got, want []string
	}{{"pres", got.Pres, want.Pres}, {"headings", got.Headings, want.Headings}, {"rows", got.Rows, want.Rows}} {
		if !slices.Equal(c.got, c.want) {
			t.Fatalf("%q: %s\nerrata  %q\nbrowser %q", src, c.what, c.got, c.want)
		}
	}
	return ""
}

// errata reads each shape of the reviews as the HTML standard's tree
// builder does (golang.org/x/net/html's Parse), or refuses it with the
// words extracttest.BrowserRefusals gives for it: no shape may end either
// way. The planted break: </a> refused in an open pre, which the shape
// <pre><a href=x>y</a></pre> fails on every run (delta review 4 of
// 8c0fb83).
func TestBrowserShapes(t *testing.T) {
	for _, src := range extracttest.BrowserShapes {
		got, err := errataReads(src)
		want := extracttest.BrowserReads(src, htmlparse.LineTag, true)
		if std, ok := extracttest.StandardReads[src]; ok {
			want.Pres, want.Headings, want.Rows = std.Pres, std.Headings, std.Rows // x/net is not the standard here
		} else if got.StandardOnly || extracttest.XNetDropsTemplate(src) {
			t.Errorf("%q: the oracle is not the standard here, and StandardReads holds no read", src)
			continue
		}
		if refusal, ok := extracttest.BrowserRefusals[src]; ok {
			if err == nil || !strings.Contains(err.Error(), refusal) {
				t.Errorf("%q: got %v, want the refusal %q", src, err, refusal)
			}
			if strings.Contains(refusal, "heading") && !want.NestedHeading {
				t.Errorf("%q: the tree nests no heading, so the refusal is errata's own", src)
			}
			if err != nil && strings.Contains(refusal, "around a block") && !want.RebuiltIn[refusedName(err.Error())] {
				t.Errorf("%q: the tree rebuilds no formatting element there, so the refusal is errata's own", src)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: refused: %v", src, err)
			continue
		}
		for _, c := range []struct {
			what      string
			got, want []string
		}{{"pres", got.Pres, want.Pres}, {"headings", got.Headings, want.Headings}, {"rows", got.Rows, want.Rows}} {
			if !slices.Equal(c.got, c.want) {
				t.Errorf("%q: %s\nerrata  %q\nbrowser %q", src, c.what, c.got, c.want)
			}
		}
	}
}

// Wherever errata reads a value of generated HTML, a pre's text, a
// heading's text or a row's cells, the HTML standard's tree builder
// (golang.org/x/net/html's Parse) puts the same text in the same element;
// errata refuses only for a reason refusedFor accepts, and for the adoption
// agency in at most one document in fifty: the generator leaves a
// formatting element open in one draw in 60, and correct code refused 1 to
// 9 of 2000 over 20 runs, 3.6 on average (delta review 4 of 8c0fb83). Each of rapid's cases draws
// documentsPerCase documents, so a run of rapid's default 100 cases checks
// 2000. The planted breaks: the table scope of round 3 taken back, which
// the generated cases alone found after 57; the list item scope taken out
// of </li>, and a heading's start no longer ending a heading on top, each
// failing a default run (delta review 3 of a13dfeb).
func TestTheReadingIsTheBrowsers(t *testing.T) {
	cases, why := 0, map[string]int{}
	rapid.Check(t, func(t *rapid.T) {
		for range documentsPerCase {
			cases++
			why[sameRead(t, extracttest.GenHTML(t, 4))]++
		}
	})
	t.Logf("of %d generated documents %d were refused for the adoption agency, %d not compared where errata followed the standard and x/net does not, %d where x/net drops a template",
		cases, why["formatting"], why["standard"], why["template"])
	if why["template"]*10 > cases {
		t.Errorf("%d of %d generated documents were not compared, as x/net drops a template there, over one in ten", why["template"], cases)
	}
	for _, w := range []string{"formatting", "standard"} {
		if why[w]*50 > cases {
			t.Errorf("%d of %d generated documents were %s, over one in fifty", why[w], cases, map[string]string{"formatting": "refused for the adoption agency", "standard": "not compared, as errata followed the standard where x/net does not"}[w])
		}
	}
}

// documentsPerCase is how many documents each of rapid's cases draws.
const documentsPerCase = 20

// FuzzBrowser holds the same over any bytes the fuzzer finds, past
// extracttest.BrowserComparable.
func FuzzBrowser(f *testing.F) {
	for _, s := range extracttest.BrowserShapes {
		f.Add(s)
	}
	f.Add("<tABle><td><Col >0") // the find of the foreign delta review 3: a col closes the cell
	f.Fuzz(func(t *testing.T, src string) {
		if !extracttest.BrowserComparable(src) {
			t.Skip()
		}
		if _, foster, err := errataReadsFoster(src); foster || err != nil && bounded(err) {
			return // foster parenting moves text errata reads where it is (docs/reference.md#html)
		}
		// The inputs this process read, and those it did not compare as
		// errata followed the standard where x/net does not: past 1000, at
		// most one in fifty of those. The shapes of StandardReads are such
		// by design, compared by TestBrowserShapes, and count as neither.
		if _, known := extracttest.StandardReads[src]; known {
			return
		}
		n, skipped := fuzzCompared.Add(1), fuzzStandard.Load()
		if sameRead(t, src) == "standard" {
			skipped = fuzzStandard.Add(1)
		}
		if n >= 1000 && skipped*50 > n {
			t.Fatalf("%d of %d inputs were not compared, as errata followed the standard where x/net does not, over one in fifty", skipped, n)
		}
	})
}

// fuzzCompared and fuzzStandard count FuzzBrowser's inputs in a process,
// and those it did not compare as errata followed the standard there.
var fuzzCompared, fuzzStandard atomic.Int64

// Text of NULs alone, which a browser drops, reopens no formatting
// element, so the heading after it ends the one open: read, not refused.
// FuzzHTML found it refused (the round after 8c0fb83); the NUL keeps it out
// of the shapes, whose text a browser renders without it.
func TestNULsReopenNothing(t *testing.T) {
	if _, err := errataReads("<h1><div><b></div>\x00<h2>"); err != nil {
		t.Errorf("refused: %v", err)
	}
}

// refusedName is the name of the formatting element an adoption agency
// refusal names: the x of "</x> ends a formatting element".
func refusedName(msg string) string {
	_, rest, _ := strings.Cut(msg, "</")
	name, _, _ := strings.Cut(rest, ">")
	return name
}

// In a cell the HTML standard's "in cell" mode closes the cell at the
// start tag of a caption, a col, a colgroup, a tbody, a td, a tfoot, a th,
// a thead or a tr, and reads it again: the text after it is no part of
// the cell, a new cell's for td and th, or moved before the table
// (foster parenting) for the others but caption, whose text is the
// caption's. errata reads each as x/net's tree builder does. FuzzBrowser
// found the col (foreign delta review 3, <tABle><td><Col >0). The planted
// break: col and colgroup left out of the nest's table parts.
func TestInCellTagsCloseTheCell(t *testing.T) {
	for _, row := range []struct {
		tag    string
		rows   []string
		foster bool
	}{
		{"caption", []string{"a", "c"}, false},
		{"col", []string{"a", "c"}, true},
		{"colgroup", []string{"a", "c"}, true},
		{"tbody", []string{"a", "c"}, true},
		{"td", []string{"a|b|c"}, false},
		{"tfoot", []string{"a", "c"}, true},
		{"th", []string{"a|b|c"}, false},
		{"thead", []string{"a", "c"}, true},
		{"tr", []string{"a", "c"}, true},
	} {
		src := "<table><tr><td>a<" + row.tag + ">b<td>c</table>"
		got, foster, err := errataReadsFoster(src)
		want := extracttest.BrowserReads(src, htmlparse.LineTag, true)
		if err != nil || !slices.Equal(got.Rows, row.rows) || foster != row.foster || !slices.Equal(want.Rows, row.rows) {
			t.Errorf("%q: errata %q, foster %t, %v; x/net %q; want %q, foster %t", src, got.Rows, foster, err, want.Rows, row.rows, row.foster)
		}
	}
	got, _, err := errataReadsFoster("<tABle><td><Col >0")
	if err != nil || !slices.Equal(got.Rows, []string{""}) {
		t.Errorf("<tABle><td><Col >0: %q, %v; want the empty cell alone, the 0 moved before the table", got.Rows, err)
	}
}
