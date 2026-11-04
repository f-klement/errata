package lexer

import (
	"slices"
	"strings"
	"testing"
)

// pinned are cases of spanList: a row, a file, and the spans it holds.
type pinned struct {
	lang, src string
	want      []string
}

// check fails t on each case whose spans differ from its want.
func check(t *testing.T, cases []pinned) {
	t.Helper()
	for _, c := range cases {
		if got := spanList(t, c.lang, c.src); !slices.Equal(got, c.want) {
			t.Errorf("%s %q:\n got %q\nwant %q", c.lang, c.src, got, c.want)
		}
	}
}

// Every form stops at the end of the file without reading past it, whatever
// it was scanning there: a fence, a fence at its bound, a greedy closer, a
// character literal's escape, a block scalar's header or its last line, an
// empty line comment, a form after a line start at the file's first byte.
// It kills the boundary mutants of each of those scans (< for <=), which
// read one byte past the end and panic.
func TestFormsStopAtTheEndOfTheFile(t *testing.T) {
	check(t, []pinned{
		{"rust", "x = r##", nil},
		{"rust", "r" + strings.Repeat("#", maxFence), nil},
		{"kotlin", `val s = """a"""`, []string{"string a"}},
		{"rust", `'\u`, nil},
		{"yaml", "a: |-", []string{"string "}},
		{"yaml", "a: | ", []string{"string "}},
		{"yaml", "a: |\n  x\n   ", []string{"string   x\n   "}},
		{"yaml", "--- |\nx\n---", []string{"string x\n"}},
		{"go", "//\nx // c", []string{"comment ", "comment  c"}},
		{"yaml", "'a # b' # c", []string{"string a # b", "comment  c"}},
	})
}

// A character literal's escape runs to its closer within maxShortEscape
// bytes and no further, and a greedy closer takes at most maxRun quotes. It
// kills the mutants of the escape's limit, of its scan for the closer and
// the line feed, and of the greedy run's count.
func TestShortAndGreedyBounds(t *testing.T) {
	check(t, []pinned{
		{"rust", `let a = '\n'; let b = '\u{10FFFF}'; let c = '\''; let d = '\u{12345678}';`,
			[]string{`string \n`, `string \u{10FFFF}`, `string \'`}},
		{"kotlin", `"""a"""` + strings.Repeat(`"`, 40) + " // c",
			[]string{"string a" + strings.Repeat(`"`, maxRun), `string ""`, "comment  c"}},
	})
}

// A YAML block scalar's header takes at most two indicators, an explicit
// indentation of 1 to 9, and blanks or a comment after a blank; its body
// ends at a line no further in than its node or than its content, or at a
// document marker; its node is the key past any "- " entries, the entry's
// dash for a scalar that is the entry, the column before a scalar alone on
// its line, and nothing for one after ---. It kills the mutants of
// indented, documentMarker and parentColumn that the forms test leaves.
func TestYAMLBlockScalarEdges(t *testing.T) {
	check(t, []pinned{
		{"yaml", "key: |-2-\n  # in\n", []string{"comment  in"}},
		{"yaml", "key: |1\n  a\n # no\nnext: 1 # yes\n", []string{"string   a\n # no\n", "comment  yes"}},
		{"yaml", "key: |9\n          a\n         # no\nb: 1 # yes\n", []string{"string           a\n         # no\n", "comment  yes"}},
		{"yaml", "a: | #c\n  # in\nb: 1\n", []string{"string   # in\n"}},
		{"yaml", "a: |\t#c\n  # in\nb: 1\n", []string{"string   # in\n"}},
		{"yaml", "--- |\nx\n # in\ny # still\n", []string{"string x\n # in\ny # still\n"}},
		{"yaml", "a: |\nb: 1 # c\n", []string{"string ", "comment  c"}},
		{"yaml", "---\t|\n# in\n", []string{"string # in\n"}},
		{"yaml", "--- |\nx\n---\n# c\n", []string{"string x\n", "comment  c"}},
		{"yaml", "--- |\nx\n...\n# c\n", []string{"string x\n", "comment  c"}},
		{"yaml", "--- |\nx\n---\r\n# c\n", []string{"string x\n", "comment  c"}},
		{"yaml", "--- |\nx\n---x # c\n", []string{"string x\n---x # c\n"}},
		{"yaml", "a:\n  |\n # c\n", []string{"string ", "comment  c"}},
		{"yaml", "a:\n  |\n  # in\n", []string{"string   # in\n"}},
		{"yaml", "- key: |\n  # c\n", []string{"string ", "comment  c"}},
		{"yaml", "-\tkey: |\n  # c\n", []string{"string ", "comment  c"}},
		{"yaml", "z: 1\nkey: |\n  # in\n", []string{"string   # in\n"}},
		{"yaml", "- |\n # c\n", []string{"string  # c\n"}},
		{"yaml", "  a: |\n  b: 1 # c\n", []string{"string ", "comment  c"}},
	})
}

// A row a caller builds, outside the table, is indexed when it is walked;
// a span that ends at a line's start leaves the next byte at one, so a form
// that opens only there opens; a doubled closer of two bytes is skipped
// whole. It kills the mutants of the index for a row outside the table, of
// the line start after a span, and of the doubled closer's step.
func TestAHandBuiltRow(t *testing.T) {
	l := Language{Name: "x", Forms: []Form{
		{Kind: String, Open: "|", After: " ", Indented: true},
		{Kind: Comment, Open: "#", LineStart: true},
		{Kind: String, Open: "[[", Close: "]]", Doubled: true},
	}}
	src := "a: |\n  x\n# c\n[[a]]]]b]] # d"
	var got []string
	for s := range l.Spans([]byte(src)) {
		got = append(got, src[s.BodyStart:s.BodyEnd])
	}
	if want := []string{"  x\n", " c", "a]]]]b"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
