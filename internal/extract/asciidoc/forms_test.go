package asciidoc

import (
	"fmt"
	"testing"

	"github.com/f-klement/errata/internal/extract/core/markup"
)

// The line forms the parse tells apart, each on both sides of its edge:
// titles, block titles, attribute entries, list items, delimiters and two
// line titles' underlines.
func TestLineForms(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		{"== A", "2 A"}, {"====== A", "6 A"}, {"======= A", "none"}, {"==", "none"}, {"==A", "none"},
		{"== ==", "2 =="}, {"== A ==", "2 A"}, {"== A==", "2 A=="}, {"# A #", "1 A"}, {"x", "none"},
	} {
		level, title, ok := heading([]byte(c.line))
		if got := fmt.Sprintf("%d %s", level, title); !ok && c.want != "none" || ok && got != c.want {
			t.Errorf("heading %q: %d %q %v, want %s", c.line, level, title, ok, c.want)
		}
	}
	for _, c := range []struct{ line, want string }{
		{".a", "a"}, {".Title", "Title"}, {".", ""}, {"..a", ""}, {". a", ""}, {".\ta", ""}, {"a", ""},
	} {
		got, ok := blockTitle([]byte(c.line))
		if string(got) != c.want || ok != (c.want != "") {
			t.Errorf("blockTitle %q: %q %v", c.line, got, ok)
		}
	}
	for line, want := range map[string]bool{
		":a:": true, ":": false, ":a: b": true, ":!a:": true, ":a!:": true, ":a-b_c:": true, ":a:\tb": true,
		"::": false, ":!:": false, ":a": false, ":a:b": false, "a:": false, ":a b:": false, ":!": false, ":a!": false,
	} {
		if got := attributeEntry([]byte(line)); got != want {
			t.Errorf("attributeEntry %q: %v", line, got)
		}
	}
	for _, c := range []struct{ line, want string }{
		{"* x", "x"}, {"** x", "x"}, {". x", "x"}, {"... x", "x"}, {"- x", "x"}, {"1. x", "x"}, {"12.\tx", "x"},
		{"  * x", "x"}, {"*x", ""}, {"-x", ""}, {"--", ""}, {"1.x", ""}, {"1", ""}, {"1 x", ""}, {"*", ""}, {"x", ""}, {"9. x", "x"}, {"0. x", "x"}, {"10. x", "x"}, {"/. x", ""},
	} {
		got, ok := listItem([]byte(c.line))
		if string(got) != c.want || ok != (c.want != "") {
			t.Errorf("listItem %q: %q %v", c.line, got, ok)
		}
	}
	for _, c := range []struct {
		line string
		want string // kind and close, or none
	}{
		{"--", "3 --"}, {"---", "none"}, {"----", "1 ----"}, {"....", "1 ...."}, {"////", "0 ////"}, {"++++", "2 ++++"},
		{"====", "3 ===="}, {"****", "3 ****"}, {"____", "3 ____"}, {"~~~~", "none"}, {"```", "1 ```"}, {"```sh,x", "1 ```"},
		{"````", "none"}, {"|===", "4 |==="}, {"|==", "none"}, {"!====", "4 !===="}, {",===", "4 ,==="}, {":===", "4 :==="}, {"|=-=", "none"},
		{"-- ", "none"},
	} {
		b := delimiter(c.line)
		got := "none"
		if b != nil {
			got = fmt.Sprintf("%d %s", b.kind, b.close)
		}
		if got != c.want {
			t.Errorf("delimiter %q: %s, want %s", c.line, got, c.want)
		}
	}
	if b := delimiter("```sh,x"); b.lang != "sh" {
		t.Errorf("fence language %q", b.lang)
	}
	for _, c := range []struct {
		line string
		n    int
		want bool
	}{
		{"~~", 2, true}, {"~~", 1, true}, {"~~", 3, true}, {"~~", 4, false}, {"~~~~", 2, false}, {"~", 1, false},
		{"^^^", 3, true}, {"+++", 3, true}, {"-~-", 3, false}, {"***", 3, false}, {"===", 3, true}, {"---", 3, true},
	} {
		if got := underline([]byte(c.line), c.n); got != c.want {
			t.Errorf("underline %q under %d: %v", c.line, c.n, got)
		}
	}
}

// isWord is a letter, a digit, an underscore or a byte past ASCII, at each
// edge of each range.
func TestIsWord(t *testing.T) {
	for _, c := range []byte("azAZ09_\x80\xff") {
		if !isWord(c) {
			t.Errorf("%q is a word character", c)
		}
	}
	for _, c := range []byte("`{@[/:^- \x7f") {
		if isWord(c) {
			t.Errorf("%q is no word character", c)
		}
	}
}

// Cell specifiers, as Asciidoctor's CellSpecStartRx and CellSpecEndRx read
// them, and whether they span or repeat.
func TestCellSpecs(t *testing.T) {
	for _, c := range []struct {
		spec     string
		ok, span bool
	}{
		{"", true, false}, {"a", true, false}, {"z", true, false}, {"<", true, false}, {"^", true, false}, {">", true, false},
		{".<", true, false}, {".^", true, false}, {".>", true, false}, {"<.>a", true, false}, {"2+", true, true}, {"9*", true, true},
		{".2+", true, true}, {"2.3+^.^s", true, true}, {"2", false, false}, {".", false, false}, {"+", false, false},
		{"A", false, false}, {"ab", false, false}, {"..", false, false}, {".x", false, false}, {"0+", true, true},
	} {
		ok, span := isSpec([]byte(c.spec))
		if ok != c.ok || span != c.span {
			t.Errorf("isSpec %q: %v %v, want %v %v", c.spec, ok, span, c.ok, c.span)
		}
	}
	for _, c := range []struct {
		seg   string
		first bool
		want  string
	}{
		{"", true, ""}, {" a", true, ""}, {"x a", true, "x"}, {"x", true, ""}, {"X", true, "X"}, {" a", false, ""}, {"x", false, "x"},
		{"x  <", false, "x"}, {"x .", false, "x ."}, {"x 2", false, "x 2"}, {"x\ta", false, "x"},
	} {
		got, _ := cellSpec([]byte(c.seg), c.first)
		if string(got) != c.want {
			t.Errorf("cellSpec %q %v: %q, want %q", c.seg, c.first, got, c.want)
		}
	}
}

// cols, as Asciidoctor's parse_colspecs reads it, at each edge.
func TestColumns(t *testing.T) {
	for _, c := range []struct {
		cols string
		n    int
		ok   bool
	}{
		{"", 0, true}, {" ", 0, true}, {"3", 3, true}, {" 3 ", 3, true}, {"03", 1, true}, {"0", 0, false}, {"-1", 0, false},
		{"1,1", 2, true}, {"1;1;1", 3, true}, {"1,", 2, true}, {"2*", 2, true}, {"0*,1", 1, true}, {"0*", 0, false},
		{"2*,<.^1a", 3, true}, {"1,~,20%", 3, true}, {".<", 1, true}, {"<.", 1, true}, {"<.,>", 2, true}, {"<.^2a", 1, true}, {"<..", 0, false}, {"..^", 0, false}, {"z", 1, true}, {"1z", 1, true},
		{"X", 0, false}, {"%", 0, false}, {".", 0, false}, {"1,foo", 0, false}, {"x*", 0, false}, {"-1*", 0, false},
		{"*", 0, false}, {"99999999999999999999*", 2147483647, true}, {"99999999999999999999", 2147483647, true},
		{"1;2,3", 0, false}, {"<,^,>", 3, true}, {"1%a", 1, true}, {"~a", 1, true}, {"a~", 0, false},
	} {
		n, ok := columns(c.cols)
		if ok != c.ok || ok && n != c.n {
			t.Errorf("columns %q: %d %v, want %d %v", c.cols, n, ok, c.n, c.ok)
		}
	}
}

// Attribute lines: which lines are one, their entries, quotes where a
// value starts, and how a later line merges into an earlier one.
func TestAttributeLines(t *testing.T) {
	for _, c := range []struct {
		line string
		want string // style, positional, named and options, or none, or anchor
	}{
		{"[]", " [] map[] map[]"}, {"[[a]]", "anchor"}, {"[x]", "x [x] map[] map[]"}, {"[.role]", " [.role] map[] map[]"},
		{"[#id]", " [#id] map[] map[]"}, {"[%header]", " [%header] map[] map[header:true]"}, {"[,sh]", " [ sh] map[] map[]"},
		{"[\"q\"]", "q [q] map[] map[]"}, {"['q']", "q [q] map[] map[]"}, {"[{a}]", "{a} [{a}] map[] map[]"},
		{"[!x]", "none"}, {"[x", "none"}, {"x]", "none"}, {"[", "none"}, {"] [", "none"},
		{"[cols= \"1,2\"]", " [] map[cols:1,2] map[]"}, {"[cols=\t'1,2']", " [] map[cols:1,2] map[]"},
		{"[cols=\"\"]", " [] map[cols:] map[]"}, {"[a=it's,b]", "b [b] map[a:it's] map[]"},
		{"[opts=\"header,footer\"]", " [] map[opts:header,footer] map[footer:true header:true]"},
		{"[options=header]", " [] map[options:header] map[header:true]"},
		{"[source%linenums%x#id.r,py]", "source [source%linenums%x#id.r py] map[] map[linenums:true x:true]"},
		{"[a-b=c]", " [] map[a-b:c] map[]"}, {"[a b=c]", "a b=c [a b=c] map[] map[]"}, {"[=c]", "none"},
	} {
		a, ok := attributeLine([]byte(c.line))
		got := "none"
		switch {
		case ok && a == nil:
			got = "anchor"
		case ok:
			got = fmt.Sprintf("%s %v %v %v", a.style, a.pos, a.named, a.opts)
		}
		if got != c.want {
			t.Errorf("attributeLine %q: %s, want %s", c.line, got, c.want)
		}
	}
	merge := func(lines ...string) string {
		a, _ := attributeLine([]byte(lines[0]))
		for _, l := range lines[1:] {
			b, _ := attributeLine([]byte(l))
			a.merge(b)
		}
		return fmt.Sprintf("%s %v %v %v", a.style, a.pos, a.named, a.opts)
	}
	for _, c := range []struct {
		lines []string
		want  string
	}{
		{[]string{"[source]", "[,sh]"}, "source [source sh] map[] map[]"},
		{[]string{"[source,sh]", "[#id]"}, "source [source sh] map[] map[]"},
		{[]string{"[source,sh]", "[python]"}, "python [python sh] map[] map[]"},
		{[]string{"[]", "[a=1]", "[%x]"}, " [] map[a:1] map[x:true]"},
		{[]string{"[a=1]", "[a=2]"}, " [] map[a:2] map[]"},
	} {
		if got := merge(c.lines...); got != c.want {
			t.Errorf("merge %q: %s, want %s", c.lines, got, c.want)
		}
	}
	for s, want := range map[string]string{`""`: "", `''`: "", `"a"`: "a", `'a'`: "a", `"a'`: `"a'`, `"`: `"`, "a": "a", `a"`: `a"`} {
		if got := unquote(s); got != want {
			t.Errorf("unquote %q: %q, want %q", s, got, want)
		}
	}
	for s, want := range map[string]bool{"a": true, "a-b": true, "a_1": true, "": false, "a b": false, "a.b": false} {
		if got := isName(s); got != want {
			t.Errorf("isName %q: %v", s, got)
		}
	}
}

// What a table line does at its edges: a backslash at its end, a lone
// specifier after a blank, an alignment specifier, and a section's prose,
// which holds a table's lines as written and no blank ones.
func TestTableLineEdges(t *testing.T) {
	body := "= A\n\n|===\n|A |B |C\n\n|x| a|y\n|p\\\n|q <|r\n|s .|t 2|u\n|===\n"
	got := read(t, sel(markup.Selector{Target: "table", Column: "B"}), body)
	want(t, "B", got, "6:", "7:q", "9:t 2")
	got = read(t, sel(markup.Selector{Target: "table", Column: "A"}), body)
	want(t, "A", got, "6:x", "7:p\\", "9:s .")
	got = read(t, sel(markup.Selector{Target: "section", Section: "A"}), body)
	want(t, "section", got, "1:|A |B |C\n|x| a|y\n|p\\\n|q <|r\n|s .|t 2|u")
	got = read(t, sel(markup.Selector{Target: "code", Language: ""}), "[source]\n----\nplain\n----\n")
	want(t, "source alone", got, "3:plain")
}
