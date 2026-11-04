package rst

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/markup"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// read is the values s reads from a README.rst that holds body, each as
// "line:text", read from the file under a repository as Extract reads it.
func read(t *testing.T, s Spec, body string) []string {
	t.Helper()
	s.File = "README.rst"
	root := extracttest.Repo(t, map[string]string{"README.rst": body})
	return show(extracttest.MustRun(t, Build, s, root))
}

// show writes each value as "line:text".
func show(values []check.Value) []string {
	out := []string{}
	for _, v := range values {
		out = append(out, fmt.Sprintf("%d:%s", v.At.Line, v.Text))
	}
	return out
}

// want fails t unless got is want.
func want(t *testing.T, name string, got []string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
		t.Errorf("%s: values %q, want %q", name, got, want)
	}
}

// sel is a spec of target with the fields of f.
func sel(target string, f ...string) Spec {
	s := Spec{Selector: markup.Selector{Target: target}}
	for i := 0; i+1 < len(f); i += 2 {
		switch v := f[i+1]; f[i] {
		case "section":
			s.Section = v
		case "pattern":
			s.Pattern = v
		case "language":
			s.Language = v
		case "column":
			s.Column = v
		case "key":
			s.Key = v
		case "row":
			s.Row = v
		}
	}
	return s
}

const comments = "Ports\n=====\n\n" +
	".. port: 8080\n\n" + // 4
	"Text .. port: 9 is prose.\n\n" + // 6
	"..\n   port: 7070\n   more\n\n" + // 8, a comment whose text starts on the next line
	".. note:: An admonition.\n\n   .. port: 6060\n\n" + // 14, its content is rST
	".. code-block:: sh\n\n   .. port: 1\n\n" + // a code block holds none
	".. _target: http://port: 2\n.. |sub| replace:: port: 3\n.. [1] port: 4\n.. [#] port: 4\n.. [cit] port: 4\n" + // a target, a substitution, footnotes and a citation
	".. image:: port: 5\n\n   .. port: 5\n\n" + // nor does a directive errata does not know
	"..\n\n   port: 6 in a block quote\n\n" + // 29, an empty comment holds nothing after it
	"Para::\n\n   .. port: 7\n\n" + // a literal block
	"- item\n\n  .. port: 5050\n\n" + // 39, in a list item
	".. [no label] port: 4040\n.. |no end port: 3030\n" + // 41 and 42: no label, no substitution
	".. a::b port: 2020\nx\n\n\t.. port: 1010\n" // 43: no directive; 46: a tab indents

// A comment is explicit markup that is no directive, target, substitution
// definition, footnote or citation, wherever rST is read; its value is its
// text without the blanks at either end, its lines after the first dedented,
// and its line where its .. is. The planted breaks: a directive's content
// read as rST whatever the directive (5 found), and the empty comment read
// as taking the block after it (6 found).
func TestCommentsAreExplicitMarkupOfNoOtherKind(t *testing.T) {
	got := read(t, sel("comment", "pattern", `port:\s*(\d+)`), comments)
	want(t, "pattern", got, "4:8080", "8:7070", "14:6060", "39:5050", "41:4040", "42:3030", "43:2020", "46:1010")
	got = read(t, sel("comment"), "..  a  b\n    c\n\n      d\n\n..\n\n.. x\n..\n")
	want(t, "whole", got, "1:a  b\nc\n\n  d", "6:", "8:x", "9:")
}

const sections = "======\nerrata\n======\n\nIntro.\n\n" +
	"Install\n=======\n\nRun make.\n\n" +
	"Requirements\n------------\n\nGo *1.27* and\n``git``.\n\n" +
	"::\n\n   Requirements\n   ------------\n\nText.\n\n" +
	"  Requirements\n  ------------\n\n" + // indented: a block quote, no title
	"Usage\n=====\n\nerrata check.\n\n" +
	"Requirements\n------------\n\nNothing.\n\n" +
	"Install\n=======\n\nAgain.\n"

// A section is the prose under a title, to the next title of the same or a
// higher level, a level being the order in which an adornment style first
// appears, and an overline making a style of its own. A path names the
// titles a title sits under, the nearest first, from any level. A title in
// a literal block is none, and an indented one is prose. The planted
// breaks: an overline read as the underline's style (errata a level with
// Install, so Install > Requirements also names errata's), the literal
// block read as prose, and an indented title read as one.
func TestSectionsByHeadingPath(t *testing.T) {
	got := read(t, sel("section", "section", "Install > Requirements"), sections)
	want(t, "path", got, "12:Go 1.27 and git.\nText.\nRequirements ------------")
	got = read(t, sel("section", "section", "Install"), sections)
	want(t, "twice", got, "7:Run make.\nRequirements\nGo 1.27 and git.\nText.\nRequirements ------------", "38:Again.")
	got = read(t, sel("section", "section", "errata"), sections)
	want(t, "top", got, "2:Intro.\nInstall\nRun make.\nRequirements\nGo 1.27 and git.\nText.\nRequirements ------------\nUsage\nerrata check.\nRequirements\nNothing.\nInstall\nAgain.")
	got = read(t, sel("section", "section", "Usage > Install"), sections)
	want(t, "not a parent", got)
	got = read(t, sel("section", "section", "Requirements", "pattern", `(\w+)\.$`), sections)
	want(t, "match lines", got, "16:git", "23:Text", "36:Nothing")
}

// A heading path keeps every other target to its sections, once each where
// sections of the path nest, and a path names nested titles too. The
// planted break: the nested scope read again, which gives b twice.
func TestAPathScopesEveryTarget(t *testing.T) {
	body := "A\n=\n\n.. a\n\nA\n-\n\n.. b\n\nB\n=\n\n.. c\n"
	got := read(t, sel("comment", "section", "A"), body)
	want(t, "nested", got, "4:a", "9:b")
	got = read(t, sel("section", "section", "A"), body)
	want(t, "sections", got, "1:A", "6:")
}

const titles = "Short\n===\n\n" + // too short an underline: prose
	"Ab\n==\n\n" + // as long as the title: a title
	"----\n\n" + // a transition
	"--\n\n" + // too short to be one: prose
	"====\nOver\n====\n\n" + // an overline
	"====\nOdd\n=====\n\n" + // a mismatched overline: neither title nor prose
	"  Inset\n=======\n\n" + // indented: prose, then a transition
	"Ab\n==\nnext"

// What a title is: one line, at the first column, under an adornment at
// least four characters long or as long as it, with an overline the same as
// the underline when it has one. A lone adornment is a transition, which is
// no prose.
func TestTitles(t *testing.T) {
	got := read(t, sel("section", "section", "Ab"), titles)
	want(t, "ab", got, "4:--\nOver\nInset", "22:next")
	got = read(t, sel("section", "section", "Over"), titles)
	want(t, "over", got, "12:Inset")
}

const code = "Build\n=====\n\n.. code-block:: sh\n   :caption: x\n   :linenos:\n\n   make build\n\n     indented\n\n" + // 8
	"Test\n----\n\nRun::\n\n    go test \\\n      ./...\n\n" + // 17
	".. code:: yaml\n\n   port: 8080\n\n" + // 22
	".. sourcecode:: Shell\n\n   echo\n\n" + // 26, a language compared in its case
	".. code-block::\n\n" + // 28, empty
	"- item ::\n\n    in an item\n\n" + // 32
	"::\n\n\tA tab\n\n" + // 36
	"Para::\n\nno block\n\nEnd::\n"

// A code target reads literal blocks, which have no language, and the code
// directives, whose language is their argument, each line dedented, at the
// line of its first, or the directive's for an empty one. The planted
// breaks: an option read as code, and a literal block read where no
// indented block follows its paragraph.
func TestCodeBlocks(t *testing.T) {
	got := read(t, sel("code", "language", "sh"), code)
	want(t, "sh", got, "8:make build\n\n  indented")
	got = read(t, sel("code", "section", "Test", "pattern", `port: (\d+)`), code)
	want(t, "in a section", got, "22:8080")
	got = read(t, sel("code"), code)
	want(t, "every block", got, "8:make build\n\n  indented", "17:go test \\\n  ./...", "22:port: 8080", "26:echo", "28:", "32:in an item", "36:A tab")
	got = read(t, sel("code", "pattern", `(\./\.\.\.)`), code)
	want(t, "match line", got, "18:./...")
}

const grids = "Hosts\n=====\n\n" +
	"+------+------+---------+\n" +
	"| Host | Port | Path    |\n" +
	"+======+======+=========+\n" +
	"| web  | 8080 | ``/a``  |\n" + // 7
	"|      |      | more    |\n" +
	"+------+------+---------+\n" +
	"| *db* | 5432 |         |\n" + // 10
	"+------+------+---------+\n" +
	"| web  | 9090 | x       |\n" + // 12
	"+------+------+---------+\n\n" +
	"+------+-------+\n| Host | Other |\n+======+=======+\n| web  | 1     |\n+------+-------+\n\n" +
	"+------+------+\n| Host | Port |\n+------+------+\n| web  | 2    |\n+------+------+\n" // no header

// A grid table's header is the rows above its border of =, and a cell is
// the text between the | of its row's lines, read as prose, at its row's
// first line. A table without the column, or without a header, gives
// nothing. The planted breaks: the key cell compared before its emphasis
// is read (db missed), and a headless table read with its first row as the
// header.
func TestGridTables(t *testing.T) {
	got := read(t, sel("table", "column", "Port", "key", "Host", "row", "web"), grids)
	want(t, "web", got, "7:8080", "12:9090")
	got = read(t, sel("table", "column", "Path"), grids)
	want(t, "path", got, "7:/a more", "10:", "12:x")
	got = read(t, sel("table", "column", "Port", "key", "Host", "row", "db", "pattern", `(\d)`), grids)
	want(t, "pattern", got, "10:5", "10:4", "10:3", "10:2")
	got = read(t, sel("table", "column", "Nowhere"), grids)
	want(t, "no column", got)
}

const simpleTable = "Hosts\n=====\n\n" +
	"=====  ====  =====\n" +
	"Host   Port  Path\n" +
	"=====  ====  =====\n" +
	"web    8080  /a and\n" + // 7
	"             more\n\n" +
	"*db*   5432\n" + // 10
	"web    9090  x\n" + // 11
	"=====  ====  =====\n\n" +
	"=====  ====\nweb    1\n=====  ====\n\n" + // no header
	"  ===  ===\n  Host Port\n  ===  ===\n  web  2\n  ===  ===\nText\n" // 21: indented, inside a block quote

// A simple table's columns start at the runs of = of its top border, the
// last running to the end of the line, and a line whose first column is
// blank goes on the row before. Its header is the rows above the border
// that more of the table follows; a table with only a top and a bottom has
// none. The planted breaks: the continuation line read as a row of its
// own, and the bottom border followed by a blank read as a header border.
func TestSimpleTables(t *testing.T) {
	got := read(t, sel("table", "column", "Port", "key", "Host", "row", "web"), simpleTable)
	want(t, "web", got, "7:8080", "11:9090", "21:2")
	got = read(t, sel("table", "column", "Path"), simpleTable)
	want(t, "path", got, "7:/a and more", "10:", "11:x")
	got = read(t, sel("table", "column", "Host", "section", "Hosts", "key", "Port", "row", "5432"), simpleTable)
	want(t, "key", got, "10:db")
}

// Inline markup reads as its text: the delimiters of literals, strong,
// emphasis, interpreted text with its role, and references go, and a
// backslash escape leaves its character. What is no markup, by docutils'
// rules for where markup starts and ends, stays as written.
func TestInlineMarkup(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"``a *b*``", "a *b*"},
		{"**strong** and *em*", "strong and em"},
		{"`text`, :role:`x`, `y`:role:, :py:func:`f`", "text, x, y, f"},
		{"`Link <http://a>`_ and `<http://b>`__ and `ref`_", "Link and http://b and ref"},
		{`\*not\* a\ b \\`, `*not* ab \`},
		{"a*b* 2 * 3 `` ``", "a*b* 2 * 3 `` ``"},
		{"*x * x*", "x * x"},
		{"*a\\*b* **c* *d** (*e*) x*", "a*b c* *d (e) x*"},
		{"``x\\`` ``y`` `z` :r: :r:`` :a::`x` :a- `w`:", "x\\ y z :r: :r:`` :a::x :a- w:"},
		{"`a`b` `<x>`_", "a`b x"},
		{"name_ |sub| [1]_", "name_ |sub| [1]_"},
		{"*a*. *b*x", "a. *b*x"},
		{"\\", ""},
	} {
		var b []byte
		inline([]byte(c.in), func(from, to int) { b = append(b, c.in[from:to]...) })
		if string(b) != c.want {
			t.Errorf("%q: %q, want %q", c.in, b, c.want)
		}
	}
}

// Build refuses what markup.Selector refuses, under the name rst.
func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{}, "rst: target is required"},
		{sel("comment", "language", "sh"), "rst: language is read only by target code, and this one is comment"},
		{sel("table"), "rst: target table needs column"},
		{sel("code", "pattern", "x"), "rst: pattern \"x\" has no capture group"},
		{Spec{OneFile: core.OneFile{File: "a", MaxBytes: -1}, Selector: markup.Selector{Target: "code"}}, "rst: max_bytes is -1"},
	} {
		if c.spec.File == "" {
			c.spec.File = "README.rst"
		}
		_, err := Build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
	_, err := Build(sel("code"))
	extracttest.WantError(t, err, "rst: file is required")
}

// The pieces read may be at most maxTextRatio times their file; the
// planted break is one byte more.
func TestCheckRatio(t *testing.T) {
	if err := checkRatio("a.rst", 1200, 100); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	err := checkRatio("a.rst", 1201, 100)
	extracttest.WantError(t, err, "a.rst: the text read is 1201 bytes, over the limit of 1200, 12 times the file's size; "+core.OtherWay)
}

// Sections of one path that nest share the text they are cut from, so the
// text is built once, and the pieces are held to the ratio before a value
// is copied. 64 titles named A, each a level below the one before, read
// 64 times the prose of the deepest. The planted break: the sum of the
// pieces left unchecked.
func TestNestedSectionsAreBounded(t *testing.T) {
	var b strings.Builder
	chars := "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
	for i := range 64 {
		c := strings.Repeat(string(chars[i%len(chars)]), 4)
		if i < len(chars) {
			fmt.Fprintf(&b, "A\n%s\n\n", c)
		} else {
			fmt.Fprintf(&b, "%s\nA\n%s\n\n", c, c)
		}
	}
	b.WriteString(strings.Repeat("word ", 1000) + "\n")
	s := sel("section", "section", "A")
	s.File = "a.rst"
	e, err := Build(s)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.(extractor).FromData([]byte(b.String()))
	extracttest.WantError(t, err, "times the file's size")
}

// The values over every target together are held to the cap on values,
// with a pattern and without.
func TestValuesAreCapped(t *testing.T) {
	body := strings.Repeat(".. a\n", core.MaxValues+1)
	for _, s := range []Spec{sel("comment"), sel("comment", "pattern", "(a)")} {
		s.File = "a.rst"
		e, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.(extractor).FromData([]byte(body))
		extracttest.WantError(t, err, "a.rst: the extractor selects more than 100000 values, the limit")
	}
}

// specs are a spec of each target, with and without a pattern and a path.
var specs = []Spec{
	sel("comment"), sel("comment", "pattern", `(\S+)`, "section", "A"),
	sel("section", "section", "A"), sel("section", "section", "Install", "pattern", `(\S+)`),
	sel("code"), sel("code", "pattern", `(?s)(.+)`),
	sel("table", "column", "Port"), sel("table", "column", "Port", "key", "Host", "row", "web", "pattern", `(.)`),
}

// lines is a file of each construct whose line a value could miss, with
// CRLF endings: a comment, a code block in a list item, a table in a
// directive, an overline title, and matches on the second line of a
// paragraph and of a table cell.
const lines = "A\r\n=\r\n\r\nx\r\n\r\n.. c1\r\n\r\n- item\r\n\r\n  .. code:: sh\r\n\r\n     echo hi\r\n\r\n" +
	".. table:: T\r\n\r\n   ====  ====\r\n   Host  Port\r\n   ====  ====\r\n   web   80\r\n         81\r\n   ====  ====\r\n\r\n" +
	"---\r\nB\r\n---\r\n\r\npara one\r\ntwo 2.0\r\n*three*\r\n4.0"

// Every target gives the exact line of the unnormalised file: with CRLF,
// and after a byte order mark, which no line counts. The planted break: the
// line of a match counted in the text rather than mapped back to the file.
func TestExactLines(t *testing.T) {
	cases := []struct {
		spec Spec
		want []string
	}{
		{sel("comment"), []string{"6:c1"}},
		{sel("code"), []string{"12:echo hi"}},
		{sel("table", "column", "Port"), []string{"19:80 81"}},
		{sel("table", "column", "Port", "pattern", `(81)`), []string{"19:81"}},
		{sel("section", "section", "A > B", "pattern", `(one)`), []string{"27:one"}},
		{sel("section", "section", "B", "pattern", `(\d\.\d)|(three)`), []string{"28:2.0", "30:4.0"}},
		{sel("section", "section", "B", "pattern", `(three)`), []string{"29:three"}},
	}
	for _, prefix := range []string{"", core.BOM} {
		for _, c := range cases {
			got := read(t, c.spec, prefix+lines)
			want(t, fmt.Sprintf("%q %+v", prefix, c.spec.Selector), got, c.want...)
		}
	}
}

// An empty match is at the line of what it ends: an empty section's at
// its title, the end of a code block at its last line, though the next
// title or block starts where it ends. The planted breaks: an empty
// section's match at the line of the next title's text, and a code
// block's at the next block's first line.
func TestEmptyMatchesAreAtTheirLines(t *testing.T) {
	body := "Intro\n=====\n\nA\n-\n\nB\n-\n\nx\n\n.. code:: a\n\n   one\n\n.. code:: b\n\n   two\n   three\n"
	got := read(t, sel("section", "section", "A", "pattern", `(z*)\z`), body)
	want(t, "empty section", got, "4:")
	got = read(t, sel("code", "pattern", `(z*)\z`), body)
	want(t, "code", got, "14:", "19:")
	got = read(t, sel("section", "section", "B", "pattern", `(z*)\z`), "B\n=\n\nx\n\ny\n")
	want(t, "section", got, "6:")
}

// What starts a list item, whose marker is no part of the prose and
// whose text the lines after it are indented to: a bullet, a number or #
// followed by . or ), or either in parentheses, each followed by a space.
// What is none stays in the prose, and so does a marker on a line that
// goes on a paragraph.
func TestListItems(t *testing.T) {
	body := "A\n=\n\n" +
		"- a\n  b\n" +
		"12. c\n    d\n" +
		"#) e\n" +
		"(3) f\n" +
		"(#) g\n" +
		"(x) h\n" +
		"(3 i\n" +
		"3: j\n" +
		"-k\n" +
		"* \n" +
		"(3)l\n" +
		"x. m\n"
	got := read(t, sel("section", "section", "A"), body)
	want(t, "items", got, "1:a b\nc d\ne\nf\ng\n(x) h (3 i 3: j -k * (3)l x. m")
}

// A line of a grid table that starts with + and is no border of the
// table's columns, as a cell that spans rows has, is refused at its line
// where the table target reads the table: its + elsewhere, a fill that is
// neither - nor =, or two fills.
func TestGridSeparators(t *testing.T) {
	for _, sep := range []string{"+----+---+---+", "+~~~~+~~~~~~~+", "+-==-+-------+", "+----+----+--+", "+----+-------+-"} {
		body := "+----+-------+\n| H  | P     |\n+====+=======+\n| a  | 1     |\n" + sep + "\n| b  | 2     |\n+----+-------+\n"
		_, err := extracted(sel("table", "column", "P"), body)
		extracttest.WantError(t, err, "a.rst: line 5: a cell of the grid table here spans rows or columns, which the rst extractor does not read; "+core.OtherWay)
	}
	got := read(t, sel("table", "column", "P"), "+----+-------+\n| H  | P     |\n+====+=======+\n| a  | 1     |\n+----+-------+\n")
	want(t, "border", got, "4:1")
}

// Options are the lines right after a directive, indented under it, that
// start with a colon; a line that is not indented, or a blank line, ends
// them.
func TestDirectiveOptions(t *testing.T) {
	got := read(t, sel("code"), ".. code:: sh\n   :a: 1\n   x\n\n.. code:: sh\n:b:\n")
	want(t, "options", got, "3:x", "5:")
	got = read(t, sel("comment"), ".. note::\n   :class: x\n   .. c\n\n.. note::\n\n   :y:\n\n   .. d\n")
	want(t, "container", got, "3:c", "9:d")
}

// A directive's name is letters and digits joined singly by - _ + : and .,
// and then :: and a blank or the end of the line; anything else after .. is
// a comment.
func TestDirectiveNames(t *testing.T) {
	got := read(t, sel("comment"), ".. a--b:: 1\n.. -a:: 2\n.. a-:: 3\n.. a::b 4\n.. ::\n.. a.b-c_d+e:f:: 5\n.. A1:: 6\n")
	want(t, "names", got, "1:a--b:: 1", "2:-a:: 2", "3:a-:: 3", "4:a::b 4", "5:::")
}
