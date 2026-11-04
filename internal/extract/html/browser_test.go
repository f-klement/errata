package html

import (
	"slices"
	"strings"
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
	r := newReading(sel, []byte(src), core.DefaultMaxBytes)
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
	if err := r.read(); err != nil {
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
// it for a reason refusedFor accepts, which it returns.
func sameRead(t interface {
	Helper()
	Fatalf(string, ...any)
}, src string) string {
	t.Helper()
	got, err := errataReads(src)
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
	cases, formatting := 0, 0
	rapid.Check(t, func(t *rapid.T) {
		for range documentsPerCase {
			cases++
			if sameRead(t, extracttest.GenHTML(t, 4)) == "formatting" {
				formatting++
			}
		}
	})
	t.Logf("%d of %d generated documents refused for the adoption agency", formatting, cases)
	if formatting*50 > cases {
		t.Errorf("%d of %d generated documents were refused for the adoption agency, over one in fifty", formatting, cases)
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
	f.Fuzz(func(t *testing.T, src string) {
		if !extracttest.BrowserComparable(src) {
			t.Skip()
		}
		if _, foster, err := errataReadsFoster(src); foster || err != nil && bounded(err) {
			return // foster parenting moves text errata reads where it is (README)
		}
		sameRead(t, src)
	})
}

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
