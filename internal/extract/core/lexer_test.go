package core

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// CheckSpan refuses only a span that runs off the end of the file, and names
// the line it opens on and what it is; a closed span, and one that ends at a
// line feed unclosed, pass. Planted break: the line counted from 0.
func TestCheckSpan(t *testing.T) {
	data := []byte("a\nb\n/* c")
	if err := CheckSpan("f.go", data, Span{Kind: SpanComment, Start: 4, BodyStart: 6, BodyEnd: 8, End: 8}); err == nil ||
		!strings.HasPrefix(err.Error(), "f.go: line 3: a comment opens here and runs to the end of the file") {
		t.Errorf("an unclosed comment at the end: %v", err)
	}
	if err := CheckSpan("f.go", data, Span{Kind: SpanString, Start: 0, End: 1}); err != nil {
		t.Errorf("an unclosed string that ends before the end: %v", err)
	}
	if err := CheckSpan("f.go", data, Span{Kind: SpanName, Start: 4, End: 8, Closed: true}); err != nil {
		t.Errorf("a closed span: %v", err)
	}
	l, _ := LanguageNamed("sql")
	for s := range l.Spans([]byte("select\n\"name")) {
		if err := CheckSpan("q.sql", []byte("select\n\"name"), s); err == nil ||
			!strings.HasPrefix(err.Error(), "q.sql: line 2: a quoted name opens here") {
			t.Errorf("a quoted name to the end: %v", err)
		}
	}
	if _, ok := LanguageFor("a.go"); !ok {
		t.Error("LanguageFor chose nothing for a.go")
	}
	if !strings.Contains(LanguageNames(), "go") {
		t.Errorf("LanguageNames: %s", LanguageNames())
	}
}

// CheckSpan passes a span that ends, and refuses one that runs off the end
// of the file, naming the file and the line it opens on, counted from 1.
// Planted break: the line counted from 0 (a mutation that went uncovered,
// judged for #32).
func TestCheckSpanNamesTheLine(t *testing.T) {
	lang, ok := LanguageFor("a.go")
	if !ok {
		t.Fatal("no row for a.go")
	}
	for _, c := range []struct{ src, want string }{
		{"x := 1\n// a\n", ""},
		{"x := 1\ns := `\n", "a.go: line 2: a string opens here and runs to the end of the file"},
	} {
		var err error
		data := []byte(c.src)
		for s := range lang.Spans(data) {
			if err = CheckSpan("a.go", data, s); err != nil {
				break
			}
		}
		if c.want == "" {
			if err != nil {
				t.Errorf("%q: %v", c.src, err)
			}
			continue
		}
		extracttest.WantError(t, err, c.want)
	}
}
