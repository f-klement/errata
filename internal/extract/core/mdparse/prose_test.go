package mdparse

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/core"
)

// texts is what a text extractor of pattern reads from the file src: group
// 1 of each match in its prose, or with code its code too, as "text@line",
// the line where the match starts in src, joined by spaces. The text
// extractor runs core's search over the same prose; these tests hold the
// prose, where the text extractor's own tests hold the reader.
func texts(t *testing.T, src, pattern string, code bool) string {
	t.Helper()
	doc, err := Parse("README.md", []byte(src), core.DefaultMaxBytes, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p, err := doc.Prose(code)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range regexp.MustCompile("(?m)"+pattern).FindAllSubmatchIndex(p.Text, -1) {
		if m[2] < 0 {
			continue
		}
		out = append(out, string(p.Text[m[2]:m[3]])+"@"+strconv.Itoa(LineOf(doc.Src, p.Offset(m[0]))))
	}
	return strings.Join(out, " ")
}

const claim = `CI runs on node (\w+)`

// An autolink keeps its address, and the file's own text keeps a backslash
// escape and an entity as written.
func TestProseKeepsAutolinksAndEscapes(t *testing.T) {
	root := "# Build\n\n" + "See <https://ci.example/a> and \\*not emphasis\\* &amp; more.\n"
	for pattern, want := range map[string]string{
		`See (\S+)`:     "https://ci.example/a@3",
		`and (.*) more`: `\*not emphasis\* &amp;@3`,
	} {
		if got := texts(t, root, pattern, false); got != want {
			t.Errorf("%s: got %q, want %q", pattern, got, want)
		}
	}
}

// A code block's line keeps the spaces left of a tab that its indent
// splits, and a match in which group 1 took no part gives no value.
func TestProseCodeLinesAndEmptyGroups(t *testing.T) {
	root := ">\t\tx\n\nCI runs on node builder\n"
	if got := texts(t, root, `^( *x)$`, true); got != "  x@1" {
		t.Errorf("code line: got %q", got)
	}
	if got := texts(t, root, `(x)?CI`, false); got != "" {
		t.Errorf("empty group: got %s", got)
	}
}

// Front matter is a block of --- or +++ lines at the very start of the file,
// closed by the same line, or by ... after ---. Anything else is Markdown.
func TestProseFrontMatter(t *testing.T) {
	for body, want := range map[string]string{
		"---\nCI runs on node a\n---\n":            "",
		"---\nCI runs on node a\n...\n":            "",
		"+++\nCI runs on node a\n+++\n":            "",
		"+++\nCI runs on node a\n...\n":            "a@2",
		"---\nCI runs on node a\n":                 "a@2",
		"\n---\nCI runs on node a\n---\n":          "a@3",
		"---\n---\nCI runs on node a\n":            "a@3",
		"---\nx\n---\n---\nCI runs on node a\n---": "a@5",
	} {
		root := body
		if got := texts(t, root, claim, false); got != want {
			t.Errorf("%q: got %q, want %q", body, got, want)
		}
	}
	// Every byte of the front matter is out, its closing fence too, and a
	// UTF-8 byte order mark before the file starts nothing. The planted
	// breaks are the closing fence left in, which read "... b", and the
	// mark read as text, which hid the front matter.
	for body, want := range map[string]string{
		"---\na\n...\nb\n":                "b@4",
		"---\na\n---\nb\n":                "b@4",
		"\xef\xbb\xbf---\nx: y\n---\nb\n": "b@4",
		"\xef\xbb\xbfb\n":                 "b@1",
		"\xef\xbb\xbf\xef\xbb\xbfb\n":     "\ufeffb@1",
	} {
		root := body
		if got := texts(t, root, `^(\S+)`, false); got != want {
			t.Errorf("%q: got %q, want %q", body, got, want)
		}
	}
}

// The prose is spaced as a browser shows it: an element of inline HTML,
// <br> among them, stands for one space, a run of spaces and tabs is one
// space, and a line of prose neither starts nor ends with one. The reported
// line is still the line where the match starts. The planted break for each
// case is the input itself, which joined two words or doubled a space.
func TestProseSpacesAsABrowserDoes(t *testing.T) {
	for name, c := range map[string]struct {
		body, pattern, want string
	}{
		"comment beside a claim": {"See: <!-- errata: ci --> CI runs on node builder.\n", `See: (CI runs on node \w+)`, "CI runs on node builder@3"},
		"comment across lines":   {"CI runs on node <!--\nx\n--> builder\n", claim, "builder@3"},
		"tag first":              {"<img src=x> CI runs on node builder.\n", `^(CI) runs`, "CI@3"},
		"br":                     {"CI runs on node<br>builder\n", claim, "builder@3"},
		"br closed":              {"CI runs on node<br/>builder\n", claim, "builder@3"},
		"spaces and tabs":        {"CI runs on  node \t builder\n", claim, "builder@3"},
		"value collapsed":        {"CI runs on  *node*   \t builder.\n", `on (.*)\.`, "node builder@3"},
		"trailing tag":           {"CI runs on node builder <b></b>\n", `(builder)$`, "builder@3"},
		"code span spaces":       {"CI runs on `node   builder`\n", claim, "builder@3"},
		"match after a comment":  {"x <!-- a\nb --> y\nCI runs on node builder\n", claim, "builder@5"},
		"wrap after a tag":       {"CI runs on <i>\nnode</i> builder\n", claim, "builder@3"},
		"inline code tag":        {"node <code>builder</code>.\n", `node (\w+)\.`, "builder@3"},
		"tag in a word":          {"CI runs on node b<i>uild</i>er\n", claim, "builder@3"},
		"kbd":                    {"Press <kbd>Ctrl</kbd>+<kbd>C</kbd>\n", `(Ctrl\+C)`, "Ctrl+C@3"},
		"sub":                    {"H<sub>2</sub>O\n", `(H2O)`, "H2O@3"},
		"link tag":               {"<a href=x>builder</a>, today\n", `(builder,)`, "builder,@3"},
		"comment in a word":      {"CI runs on node build<!-- x -->er\n", claim, "builder@3"},
		"cell tags":              {"CI runs on node</td><td>builder\n", claim, "builder@3"},
		"paragraph tag":          {"CI runs on node<P>builder\n", claim, "builder@3"},
		"menu":                   {"CI runs on node<menu>builder\n", claim, "builder@3"},
		"noscript":               {"CI runs on node</noscript>builder\n", claim, "builder@3"},
		"option":                 {"CI runs on node<option value=x>builder\n", claim, "builder@3"},
		"custom element":         {"CI runs on node build<h1-x>er</h1-x>\n", claim, "builder@3"},
		"br closed by a slash":   {"CI runs on node<br/>builder\n", claim, "builder@3"},
		"carriage return":        {"CI runs on\rnode builder\n", claim, "builder@3"},
		"form feed":              {"CI runs on\fnode builder\n", claim, "builder@3"},
	} {
		got := texts(t, "# Build\n\n"+c.body, c.pattern, false)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// A line that starts an HTML block hides nothing a browser shows after the
// block's end on that line: text after a comment, a processing instruction,
// a declaration, CDATA, or a script, style, pre or textarea element. The
// text inside a pre, a <div> and the other blocks that end at a blank line
// is prose too, read through the HTML parse (#86); with code: true only the
// lines themselves are read. The planted
// break is the remainder left out, which hid every claim below; and the end
// looked for only after the opening, which hid the claim after <?>.
func TestProseReadsTextAfterAnHTMLBlockEnds(t *testing.T) {
	for name, c := range map[string]struct {
		body, want string
	}{
		"comment":            {"<!-- errata: ci --> CI runs on node builder.\n", "builder@3"},
		"comment over lines": {"<!--\nmarker\n--> CI runs on node builder\n", "builder@5"},
		"two comments":       {"<!-- a --> CI runs <!-- b --> on node builder\n", "builder@3"},
		"tags after":         {"<!-- a --> CI runs on <b>node</b> builder\n", "builder@3"},
		"processing":         {"<?x y?> CI runs on node builder\n", "builder@3"},
		"declaration":        {"<!X y> CI runs on node builder\n", "builder@3"},
		"cdata":              {"<![CDATA[x]]> CI runs on node builder\n", "builder@3"},
		"script":             {"<script>x</script> CI runs on node builder\n", "builder@3"},
		"pre over lines":     {"<pre>\nCI runs on node shown\n</pre> CI runs on node builder\n", "shown@4 builder@5"},
		"unclosed comment":   {"<!-- a\nCI runs on node hidden\n", ""},
		"comment in a quote": {"> <!-- a --> CI runs on node builder\n", "builder@3"},
		"div":                {"<div>\nCI runs on node shown\n</div>\n", "shown@4"},
		"tag alone":          {"<span>\nCI runs on node shown\n", "shown@4"},
		"empty comment":      {"<!--> CI runs on node builder\n", "builder@3"},
		"empty comment dash": {"<!---> CI runs on node builder\n", "builder@3"},
		"empty processing":   {"<?> CI runs on node builder\n", "builder@3"},
		"inline tag in tail": {"<!-- a --> CI runs on node b<i>uild</i>er\n", "builder@3"},
		"br in tail":         {"<!-- a --> CI runs on node<br>builder\n", "builder@3"},
		"folds longer":       {"<!-- ȺȺȺ -->x CI runs on node builder\n", "builder@3"},
		"folds shorter":      {"<!-- \u212a\u212a\u212a -->x CI runs on node builder\n", "builder@3"},
		"invalid UTF-8":      {"<!-- \xff\xfe -->x CI runs on node builder\n", "builder@3"},
		"script folds":       {"<script>Ⱥ\u212a</SCRIPT> CI runs on node builder\n", "builder@3"},
	} {
		got := texts(t, "# Build\n\n"+c.body, claim, false)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
	root := "# Build\n\n" + "<!-- a --> CI runs on node builder\n"
	if got := texts(t, root, `^(.*)$`, true); got != "Build@1 <!-- a --> CI runs on node builder@3" {
		t.Errorf("code: got %q", got)
	}
	// A rune whose lower case is longer or shorter in UTF-8 moved the cut,
	// past the line's end in the first two: a panic, and comment bytes in
	// the prose of the others.
	for _, body := range []string{"# B\n\n<!-- ȺȺȺȺ -->\n", "<!-- ȺȺ -->", "<!-- ȺȺȺ -->x y\n", "<!-- \u212a\u212a\u212a -->x y\n"} {
		root := body
		if got := texts(t, root, `(-->|\x{212a}|Ⱥ)`, false); got != "" {
			t.Errorf("%q: comment bytes in the prose: %q", body, got)
		}
	}
}

// The text of an HTML block that a blank line ends, and of a pre or a
// textarea, is prose, read by the HTML parse as a browser shows it: each
// tag of a line element ends a line, other tags join the text around them,
// and a pre keeps its lines. A comment, a script, a style or a template in
// the block shows nothing, an entity stays as written, and a block in a
// block quote reads without the quote's markers, each piece at its own
// line. The planted breaks: the blocks left out as before (every claim
// lost), a line element read as a space (the two lines of the div one),
// the segments of the quote mapped from the offset of the first (b2 at
// line 8). A <details> or a <div> that ends without its pre's </pre> ends
// the pre, as in a browser, from the block alone (delta review 1 of
// 9f3cd3b, finding 2); the planted break, the nest followed only from the
// pre, refused both.
func TestProseReadsHTMLBlocksThroughTheHTMLParse(t *testing.T) {
	for name, c := range map[string]struct {
		body, pattern, want string
	}{
		"lines":     {"<div align=center>\n<p>CI runs on\nnode <b>bui</b>lder</p><p>next</p>\n</div>\n", `^(.*)$`, "Build@1 CI runs on node builder@4 next@5"},
		"pre lines": {"<pre>\n  first   line\nsecond\n</pre>\n", `^(.*)$`, "Build@1 first line@4 second@5"},
		"textarea":  {"<textarea>\nCI runs on node builder</textarea>\n", claim, "builder@4"},
		"hidden":    {"<div><!-- CI runs on node c --><script>CI runs on node s</script>\n<style>CI runs on node y</style><template>CI runs on node t</template></div>\n", claim, ""},
		"entity":    {"<div>CI runs on node a&amp;b</div>\n", `node (\S+)`, "a&amp;b@3"},
		"quote":     {"> <div>\n> CI runs on\n> node builder\n> a\n> a\n> a\n> a\n> CI runs on node b2\n> </div>\n", claim, "builder@4 b2@10"},
		"type 7":    {"<my-box>\nCI runs on node builder\n</my-box>\n", claim, "builder@4"},
		"tail kept": {"<pre>a</pre> CI runs on node builder\n", claim, "builder@3"},
		"details":   {"<details><summary>Log</summary><pre>\nout\n</details>\n\nCI runs on node builder\n", `^(.*)$`, "Build@1 Log@3 out@4 CI runs on node builder@7"},
		"div":       {"<div align=center><pre>\nx  y\n</div>\nCI runs on node builder\n", `^(.*)$`, "Build@1 x y@4 CI runs on node builder@6"},
		"lone CR":   {"<p><code><pre>\r<p></code>\n\nCI runs on node builder\n", claim, "builder@6"}, // the pre's first line feed reopens nothing (FuzzHTML)
	} {
		if got := texts(t, "# Build\n\n"+c.body, c.pattern, false); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// Without the table extension a table is a paragraph, its rows joined by a
// space, delimiter row and pipes included.
func TestProseReadsATableAsAParagraph(t *testing.T) {
	root := "# Build\n\n" + "| CI | node |\n|---|---|\n| runs on | builder |\n"
	if got := texts(t, root, `^(.*)$`, false); got != "Build@1 | CI | node | |---|---| | runs on | builder |@3" {
		t.Errorf("got %q", got)
	}
	if got := texts(t, root, `runs on \| (\w+)`, false); got != "builder@5" {
		t.Errorf("cell: got %q", got)
	}
}

// A CRLF line ending reads as LF, as in the regex extractor, so the lines
// are the file's.
func TestProseReadsCRLFAsLF(t *testing.T) {
	root := "# Build\r\n\r\nCI runs on\r\nnode builder\r\n\r\n```\r\nCI runs on node code\r\n```\r\n"
	if got := texts(t, root, claim+`$`, true); got != "builder@3 code@7" {
		t.Errorf("got %q", got)
	}
}

// The prose of a claim written in every way Markdown allows is one line of
// text, at the line where the match starts: wraps, hard breaks, code spans,
// emphasis, links, images, inline HTML, headings, list items and block
// quotes. The text extractor's TestTextFindsTheClaimHoweverItIsWritten holds
// the same cases against a regex over the raw file.
func TestProseOfAClaimHoweverItIsWritten(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want string
	}{
		"plain":                   {"CI runs on node builder.\n", "builder@3"},
		"wrapped":                 {"CI runs on\nnode builder.\n", "builder@3"},
		"wrapped with indent":     {"CI runs on   \n   node builder.\n", "builder@3"},
		"wrapped twice":           {"CI\nruns\non\nnode\nbuilder.\n", "builder@3"},
		"starting on a wrap":      {"Our pipeline says so.\nCI runs on node\nbuilder.\n", "builder@4"},
		"hard break":              {"CI runs on node\\\nbuilder.\n", "builder@3"},
		"hard break of spaces":    {"CI runs on node  \nbuilder.\n", "builder@3"},
		"backticked":              {"CI runs on node `builder`.\n", "builder@3"},
		"backticked twice":        {"CI runs on ``node`` ```builder```.\n", "builder@3"},
		"backticks across a wrap": {"CI runs on node\n`builder`.\n", "builder@3"},
		"code span wrapped":       {"CI runs on `node\nbuilder`.\n", "builder@3"},
		"emphasised":              {"CI runs on *node* _builder_.\n", "builder@3"},
		"strong":                  {"CI runs on **node** __builder__.\n", "builder@3"},
		"strong emphasis":         {"CI runs on ***node*** builder.\n", "builder@3"},
		"emphasis in a word":      {"CI runs on node b*uild*er.\n", "builder@3"},
		"emphasis across a wrap":  {"CI runs *on\nnode* builder.\n", "builder@3"},
		"linked":                  {"CI runs on [node builder](https://ci.example/node).\n", "builder@3"},
		"reference link":          {"CI runs on [node][n] builder.\n\n[n]: https://ci.example\n", "builder@3"},
		"image":                   {"![CI runs on node builder](ci.png)\n", "builder@3"},
		"everything at once":      {"*CI* runs\n[on `node`](x)\n**`builder`**.\n", "builder@3"},
		"inline HTML":             {"CI runs on <b>node</b> builder.\n", "builder@3"},
		"heading":                 {"## CI runs on node builder\n", "builder@3"},
		"setext heading":          {"CI runs\non node builder\n---\n", "builder@3"},
		"list item":               {"- CI runs on\n  node builder\n", "builder@3"},
		"loose list item":         {"- a\n\n- CI runs on\n  node builder\n", "builder@5"},
		"block quote":             {"> CI runs on\n> node builder\n", "builder@3"},
		"lazy line":               {"> CI runs on\nnode builder\n", "builder@3"},
		"nested":                  {"> 1. - CI runs on\n>      node `builder`\n", "builder@3"},
	} {
		if got := texts(t, "# Build\n\n"+c.body, claim, false); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// The prose rows delta review 4 of 180db1a found no witness for in mdparse,
// each named by the mutant it kills: front matter in the code prose (the
// d.Front != nil test negated); two end tags on a type 1 block's
// last line, of which the earlier ends it (ARITHMETIC_BASE and
// CONDITIONALS_BOUNDARY on the later one taken); a match that starts on the
// newline written after a one-letter paragraph, at that paragraph's line
// (ARITHMETIC_BASE on the newline's offset); and one that starts on the
// space written for a <br> in an HTML block's tail, at the tail's line
// (ARITHMETIC_BASE on the space's offset).
func TestProseOffsetsOfWrittenBytes(t *testing.T) {
	for _, c := range []struct {
		src, pattern string
		code         bool
		want         string
	}{
		{"---\nCI runs on node fm\n---\n", claim, true, "fm@2"},
		{"# Build\n\n<script>\na\n</script> CI </style> runs\n", `(CI runs)`, false, "CI runs@5"}, // a script: a pre's text is read by the HTML parse (#86)
		{"# Build\n\nx\n\nb\n", `\n(b)`, false, "b@3"},
		{"# Build\n\n<!-- a -->" + strings.Repeat("x", 20) + "<br>CI\n", ` (CI)`, false, "CI@3"},
	} {
		if got := texts(t, c.src, c.pattern, c.code); got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

// The prose holds exactly as many pieces as its limit allows, and takes no
// more (CONDITIONALS_BOUNDARY on the limit: a > there takes one piece over
// it). Through a file the piece over the limit is always followed by one
// more, the newline that ends its block, so the edge is held here.
func TestProsePiecesAtTheEdge(t *testing.T) {
	p := &Prose{maxPieces: 2}
	p.CopyFrom([]byte("a"), 0)
	p.write(' ', 1)
	if p.over || string(p.Text) != "a " {
		t.Fatalf("two pieces: over %v, text %q", p.over, p.Text)
	}
	p.CopyFrom([]byte("b"), 2)
	if !p.over || p.overAt != 2 || string(p.Text) != "a " {
		t.Errorf("a third piece: over %v at %d, text %q", p.over, p.overAt, p.Text)
	}
}
