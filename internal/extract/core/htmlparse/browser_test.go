package htmlparse

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// scanReads is what the scanner's tokens put in each pre and heading of
// src: the text of each outermost pre or listing, less a newline at its
// end, and the text of each heading, a line element's tag a space and
// blanks folded, both sorted as extracttest.BrowserRead holds them; or the
// scan's refusal.
func scanReads(src string) (extracttest.BrowserRead, error) {
	var out extracttest.BrowserRead
	s := NewScanner([]byte(src), 1<<30)
	s.Headings()
	var pre, head strings.Builder
	inPre, inHead := false, false
	for s.Next() {
		isText := s.Kind() == Text || s.Kind() == Pre
		if !isText && inHead && (!s.HeadingOpen() || s.Kind() == StartTag && len(s.Name()) == 2 && s.Name()[0] == 'h' && s.Name()[1] >= '1' && s.Name()[1] <= '6') {
			out.Headings = append(out.Headings, extracttest.Fold(head.String())) // ended, or ended by the next heading
			head.Reset()
		}
		if !isText && inPre && !s.InPre() {
			out.Pres = append(out.Pres, strings.TrimSuffix(pre.String(), "\n"))
			pre.Reset()
		}
		inPre, inHead = s.InPre(), s.HeadingOpen()
		switch {
		case isText:
			if inPre {
				pre.Write(s.Bytes())
			}
			if inHead {
				head.Write(s.Bytes())
			}
		case s.BreaksLine() && inHead:
			head.WriteByte(' ')
		}
	}
	if err := s.Err(); err != nil {
		return out, err
	}
	if inPre {
		out.Pres = append(out.Pres, strings.TrimSuffix(pre.String(), "\n"))
	}
	if inHead {
		out.Headings = append(out.Headings, extracttest.Fold(head.String()))
	}
	slices.Sort(out.Pres)
	slices.Sort(out.Headings)
	return out, nil
}

// sameScan fails t unless the scanner places src's text in its pres and
// headings as the HTML standard's tree builder does, or refuses src for a
// heading inside a heading where the tree has one, for the adoption agency,
// which it returns as "formatting", or past a bound.
func sameScan(t interface {
	Helper()
	Fatalf(string, ...any)
}, src string) string {
	t.Helper()
	got, err := scanReads(src)
	want := extracttest.BrowserReads(src, LineTag, false)
	if err != nil {
		switch msg := err.Error(); {
		case strings.Contains(msg, "a heading opens inside a heading") && want.NestedHeading:
			return ""
		case strings.Contains(msg, "errata does not follow") && want.RebuiltIn[refusedName(msg)]:
			return "formatting"
		case strings.Contains(msg, "tokens, the limit") || strings.Contains(msg, "levels deep, the limit"):
			return ""
		}
		t.Fatalf("%q: refused: %v", src, err)
	}
	if !slices.Equal(got.Pres, want.Pres) {
		t.Fatalf("%q: pres\nscanner %q\nbrowser %q", src, got.Pres, want.Pres)
	}
	if !slices.Equal(got.Headings, want.Headings) {
		t.Fatalf("%q: headings\nscanner %q\nbrowser %q", src, got.Headings, want.Headings)
	}
	return ""
}

// sameScanOf is sameScan for a fuzz target.
func sameScanOf(t *testing.T, src string) {
	t.Helper()
	sameScan(t, src)
}

// The scanner places each shape of #86's reviews as the tree builder does,
// or refuses it with the words extracttest.BrowserRefusals gives: no shape
// may end either way.
func TestScanShapes(t *testing.T) {
	for _, src := range extracttest.BrowserShapes {
		got, err := scanReads(src)
		want := extracttest.BrowserReads(src, LineTag, false)
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
		if !slices.Equal(got.Pres, want.Pres) || !slices.Equal(got.Headings, want.Headings) {
			t.Errorf("%q: scanner %q %q, browser %q %q", src, got.Pres, got.Headings, want.Pres, want.Headings)
		}
	}
}

// Wherever the scanner says a token is in a pre or a heading, the HTML
// standard's tree builder (golang.org/x/net/html's Parse) puts its text in
// that element, on generated HTML: the nest's rules held to the standard's
// in htmlparse's own tests, 20 documents a case, 2000 a default run, and
// at most one in fifty refused for the adoption agency. The planted
// breaks: </table> blocked by an open cell (delta review 2 of 9195a5e,
// finding 1); the list item scope taken out of </li>.
func TestTheScanIsTheBrowsers(t *testing.T) {
	cases, formatting := 0, 0
	rapid.Check(t, func(t *rapid.T) {
		for range 20 {
			cases++
			if sameScan(t, extracttest.GenHTML(t, 4)) == "formatting" {
				formatting++
			}
		}
	})
	t.Logf("%d of %d generated documents refused for the adoption agency", formatting, cases)
	if formatting*50 > cases {
		t.Errorf("%d of %d generated documents were refused for the adoption agency, over one in fifty", formatting, cases)
	}
}

// Text of NULs alone reopens no formatting element (TestNULsReopenNothing
// in the html kind).
func TestNULsReopenNothing(t *testing.T) {
	if _, err := scanReads("<h1><div><b></div>\x00<h2>"); err != nil {
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
