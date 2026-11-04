package markup

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

var src = core.Source{File: "f", Limit: core.DefaultMaxBytes}

// Build refuses by what is wrong, each refusal under the reader's kind.
// Each case was seen passing Build before its check.
func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		sel  Selector
		want string
	}{
		{Selector{}, "html: target is required; write comment, section, code or table"},
		{Selector{Target: "heading"}, `html: target "heading" is not one of comment, section, code and table`},
		{Selector{Target: "comment", Language: "sh"}, "html: language is read only by target code, and this one is comment"},
		{Selector{Target: "code", Column: "a"}, "html: column is read only by target table, and this one is code"},
		{Selector{Target: "section", Section: "a", Key: "a"}, "html: key is read only by target table, and this one is section"},
		{Selector{Target: "code", Row: "a"}, "html: row is read only by target table, and this one is code"},
		{Selector{Target: "section"}, "html: target section needs section, the heading path"},
		{Selector{Target: "table"}, "html: target table needs column"},
		{Selector{Target: "table", Column: "a", Key: "b"}, "html: key and row go together"},
		{Selector{Target: "table", Column: "a", Row: "b"}, "html: key and row go together"},
		{Selector{Target: "code", Section: "A >  > B"}, `html: section "A >  > B" names an empty heading`},
		{Selector{Target: "code", Pattern: "x"}, `html: pattern "x" has no capture group`},
	} {
		_, err := c.sel.Build("html", src)
		extracttest.WantError(t, err, c.want)
	}
	s, err := Selector{Target: "table", Section: " A>B ", Column: "c", Key: "k", Row: "r", Pattern: "(x)"}.Build("html", src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Path, "|") != "A|B" || s.Search == nil || s.Column != "c" || s.Key != "k" || s.Row != "r" || s.Target != "table" {
		t.Errorf("select %+v", s)
	}
}

// A heading names the path when the headings it sits under, nearest first,
// end with the path's, at any level, and its section ends at the next
// heading of the same or a higher level: E6 sits under C5, under E4, so
// C > E names it, in E4's section. The planted breaks: the path
// matched as a subsequence (C > E names E under C > D), a section ended by
// a lower heading, and nesting left unseen.
func TestOutlineNamesHeadings(t *testing.T) {
	o := NewOutline([]string{"C", "E"})
	var got []string
	for _, h := range []struct {
		level int
		text  string
	}{{1, "A"}, {2, "C"}, {3, "D"}, {4, "E"}, {3, "E"}, {4, "x"}, {2, "C"}, {4, "E"}, {5, "C"}, {6, "E"}, {1, "Z"}} {
		ends, named, nested := o.Heading(h.level, h.text)
		got = append(got, fmt.Sprintf("%s%d:%v %v %v", h.text, h.level, ends, named, nested))
	}
	want := []string{
		"A1:[] false false", "C2:[] false false", "D3:[] false false", "E4:[] false false",
		"E3:[] true false", "x4:[] false true", "C2:[0] false false", "E4:[] true false",
		"C5:[] false true", "E6:[] true true", "Z1:[2 1] false false",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Columns finds the first header alike, and a table without a header the
// select names gives nothing. Planted break: the last of two headers alike.
func TestColumns(t *testing.T) {
	s := &Select{Column: "Port", Key: "Host"}
	if col, key, ok := s.Columns([]string{"Host", "Port", "Port", "Host"}); col != 1 || key != 0 || !ok {
		t.Errorf("col %d key %d ok %v", col, key, ok)
	}
	if _, _, ok := s.Columns([]string{"Port"}); ok {
		t.Errorf("a table without the key reads")
	}
	if col, key, ok := (&Select{Column: "Port"}).Columns([]string{"Port"}); col != 0 || key != -1 || !ok {
		t.Errorf("no key: col %d key %d ok %v", col, key, ok)
	}
}

// HTMLComments finds each comment outside a tag, trims its text, reads
// <!--> and <!---> as empty, and stops at one with no end. A <!-- in an
// attribute's value, quoted either way, is none; a > in quotes does not end
// the tag. The planted breaks: tags not skipped (the attribute's comment
// found), and quotes not read (the > in the value ending the tag).
func TestHTMLComments(t *testing.T) {
	text := []byte(`a <!-- x --> <div title="<!-- no -->" data-x='a > <!-- no -->'>` +
		"<!--> <!---> </p><!--\n y\n--> <b>< !-- no --> <!-- open")
	var got []string
	for _, c := range HTMLComments(text, 0) {
		got = append(got, fmt.Sprintf("%d:%q", c.Open, text[c.Start:c.End]))
	}
	want := `2:"x"|63:""|69:""|80:"y"`
	if strings.Join(got, "|") != want {
		t.Errorf("comments %s, want %s", strings.Join(got, "|"), want)
	}
	if got := HTMLComments([]byte("<!-- a --><!-- b -->"), 10); len(got) != 1 || got[0].Open != 10 {
		t.Errorf("from 10: %+v", got)
	}
	// A quote opens a value only right after =, blanks allowed: the
	// apostrophe of alt=don't is a byte of an unquoted value, and the tag
	// ends at its >. Planted break: a quote opening anywhere, which finds
	// no comment here.
	for _, c := range []struct{ text, want string }{
		{"<img alt=don't src=x><!-- 3 --> and <!-- 4 -->", "3|4"},
		{"<a title = 'x > <!-- no -->'><!-- 5 -->", "5"},
		{`<a b"c><!-- 6 -->`, "6"},
	} {
		var got []string
		for _, m := range HTMLComments([]byte(c.text), 0) {
			got = append(got, c.text[m.Start:m.End])
		}
		if strings.Join(got, "|") != c.want {
			t.Errorf("%q: comments %q, want %s", c.text, got, c.want)
		}
	}
}

// HTMLComments reads its text once: twice the comments allocate at most
// three times as much, where copying the rest of the text for each comment
// allocates four times. Planted break: that copy (review of 55c0780,
// finding 1).
func TestHTMLCommentsIsLinear(t *testing.T) {
	allocs := func(n int) uint64 {
		text := []byte("<div>\n" + strings.Repeat("<!---->", n))
		least := uint64(math.MaxUint64)
		for range 2 {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			if got := HTMLComments(text, 0); len(got) != n {
				t.Fatalf("%d comments of %d", len(got), n)
			}
			runtime.ReadMemStats(&after)
			least = min(least, after.TotalAlloc-before.TotalAlloc)
		}
		return least
	}
	small, large := allocs(2000), allocs(4000)
	t.Logf("allocated %d bytes for 2000 comments, %d for 4000", small, large)
	if large > 3*small {
		t.Errorf("twice the comments allocated %d bytes, over three times the %d", large, small)
	}
}

// Values gives each item whole, or group 1 of each match inside each item,
// at the item's line when it is fixed and at the match's otherwise, under
// one cap on values. Planted break: a fixed item's match put at its own
// line.
func TestValues(t *testing.T) {
	text := []byte("a1\nb2 c3")
	items := []Item{{Start: 0, End: 2, Line: 7}, {Start: 3, End: 8, Line: 9, Fixed: true}, {Start: 0, End: 8, Line: 1}}
	lineAt := func(off int) int { return 100 + off }
	show := func(s *Select) string {
		values, err := s.Values("f", text, items, lineAt)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, v := range values {
			out = append(out, fmt.Sprintf("%d:%s", v.At.Line, v.Text))
		}
		return strings.Join(out, "|")
	}
	if got := show(&Select{}); got != "7:a1|9:b2 c3|1:a1\nb2 c3" {
		t.Errorf("whole: %s", got)
	}
	s, err := Selector{Target: "comment", Pattern: `(\d)`}.Build("f", src)
	if err != nil {
		t.Fatal(err)
	}
	if got := show(s); got != "101:1|9:2|9:3|101:1|104:2|107:3" {
		t.Errorf("matches: %s", got)
	}
	many := make([]Item, core.MaxValues+1)
	_, err = (&Select{}).Values("f", text, many, lineAt)
	extracttest.WantError(t, err, "f: the extractor selects more than 100000 values, the limit")
}

// TrimBlanks moves past spaces, tabs and line breaks at both ends, and
// meets in the middle for a run of blanks.
func TestTrimBlanks(t *testing.T) {
	text := []byte(" \t\na b\r\n\f")
	if s, e := TrimBlanks(text, 0, len(text)); string(text[s:e]) != "a b" {
		t.Errorf("trimmed %q", text[s:e])
	}
	if s, e := TrimBlanks(text, 0, 3); s != e {
		t.Errorf("blanks: %d %d", s, e)
	}
}

// The edges the mutation of c35446e found unguarded, each named by the
// mutant class it kills. A path as long as the headings above it names the
// last of them (a < read as <=); the first header alike counts though it is
// the first column (a < 0 read as <= 0); exactly MaxValues items read, one
// more is refused (> read as >=); a match at the very start of its item is
// kept (m[2] < 0 read as <= 0).
func TestEdgesOfTheSelect(t *testing.T) {
	o := NewOutline([]string{"A", "B"})
	o.Heading(1, "A")
	if _, named, _ := o.Heading(2, "B"); !named {
		t.Errorf("A > B does not name B under A alone")
	}
	if col, _, _ := (&Select{Column: "Port"}).Columns([]string{"Port", "x", "Port"}); col != 0 {
		t.Errorf("column %d, want the first, 0", col)
	}
	text := []byte("a1")
	if _, err := (&Select{}).Values("f", text, make([]Item, core.MaxValues), func(int) int { return 1 }); err != nil {
		t.Errorf("MaxValues items: %v", err)
	}
	s, err := Selector{Target: "comment", Pattern: `(a)`}.Build("f", src)
	if err != nil {
		t.Fatal(err)
	}
	values, err := s.Values("f", text, []Item{{Start: 0, End: 2, Line: 3}}, func(off int) int { return 10 + off })
	if err != nil || len(values) != 1 || values[0].Text != "a" || values[0].At.Line != 10 {
		t.Errorf("a match at the start: %v, %v", values, err)
	}
}

// The edges of the comment scan: a < at the very end, and a tag with no
// end at the end of the text (bounds read past the text); a closing tag's
// attribute holds no comment (/ not read as a tag); every letter a to z and
// A to Z starts a tag and the bytes beside the ranges do not (isLetter's
// bounds); a run of blanks with nothing in it trims to itself (start < end
// read as <=).
func TestEdgesOfTheCommentScan(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"a <!-- x --> <", "x"},
		{"<!-- x --> <b", "x"},
		{`</p x="<!-- no -->"><!-- y -->`, "y"},
		{`<a x="<!-- no -->"><z x="<!-- no -->"><A x="<!-- no -->"><Z x="<!-- no -->">`, ""},
		{`<@ x="<!-- 1 -->"><[ x="<!-- 2 -->"><` + "`" + ` x="<!-- 3 -->"><{ x="<!-- 4 -->">`, "1|2|3|4"},
	} {
		var got []string
		for _, m := range HTMLComments([]byte(c.text), 0) {
			got = append(got, c.text[m.Start:m.End])
		}
		if strings.Join(got, "|") != c.want {
			t.Errorf("%q: comments %q, want %q", c.text, got, c.want)
		}
	}
	if s, e := TrimBlanks([]byte(" "), 0, 0); s != 0 || e != 0 {
		t.Errorf("an empty range trims to %d %d", s, e)
	}
	if s, e := TrimBlanks([]byte(" a "), 1, 2); s != 1 || e != 2 {
		t.Errorf("a range with no blank trims to %d %d", s, e)
	}
}
