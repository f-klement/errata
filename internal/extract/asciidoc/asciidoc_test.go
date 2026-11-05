package asciidoc

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/markup"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// read is the values s reads from a README.adoc that holds body, each as
// "line:text", read from the file under a repository as Extract reads it.
func read(t *testing.T, s Spec, body string) []string {
	t.Helper()
	s.File = "README.adoc"
	root := extracttest.Repo(t, map[string]string{"README.adoc": body})
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

// sel is a spec of the selector s.
func sel(s markup.Selector) Spec { return Spec{Selector: s} }

const comments = "= Ports\n\n" +
	"// port: 8080\n" + // a line comment
	"Text and\n// port: 9090\nmore.\n\n" + // in a paragraph
	"////\nport: 7070\n////\n\n" + // a comment block, at its opening line
	"----\n// port: 1\n----\n\n" + // a listing block holds none
	"====\n// port: 6060\n====\n\n" + // an example block does
	"/// port: 2\n\n" + // three slashes are text
	"|===\n| a\n// port: 5050\n|===\n\n" + // a table does
	"////\nport: 4040\n////\n\n" +
	"++++\n// port: 3\n++++\n\n" + // a passthrough block holds none
	"--\n// port: 7\n--\n" // an open block does

// A comment is a // line outside a verbatim block, or a //// block, and
// its value is its text without the blanks at either end, at the line it
// starts. The planted breaks: a listing block's lines read as AsciiDoc
// (port 1 found), /// read as a comment (port 2 found), and a comment
// block's line taken from its first line of text (7070 at line 9).
func TestComments(t *testing.T) {
	got := read(t, sel(markup.Selector{Target: "comment", Pattern: `port:\s*(\d+)`}), comments)
	want(t, "pattern", got, "3:8080", "5:9090", "8:7070", "17:6060", "24:5050", "27:4040", "36:7")
	got = read(t, sel(markup.Selector{Target: "comment"}), "//  a  b \n\n////\n////\n")
	want(t, "whole", got, "1:a  b", "3:")
}

const sections = "= errata\n\nIntro.\n\n" +
	"== Install\n\nRun *make*.\n\n" +
	"=== Requirements\n\nGo *1.27* and\n`git`.\n\n" +
	"[source,sh]\n----\n=== Requirements\nnot a heading\n----\n\n" +
	"====\n=== Requirements\nin a block\n====\n\n" +
	"== Usage\n\nerrata check.\n\n" +
	"=== Requirements\n\nNothing.\n\n" +
	"== Install\n\nAgain.\n"

// A section is the prose under a title, to the next title of the same or
// a higher level, its formatting marks left out and verbatim blocks left
// out. A path names the titles a title sits under, the nearest first. A
// title in a listing block is none, and one in an example block is prose.
// The planted breaks: a section run to the next title of any level
// (Install stops at Requirements), a title in the example block read as a
// section's, and the marks kept (*make*).
func TestSectionsByHeadingPath(t *testing.T) {
	got := read(t, sel(markup.Selector{Target: "section", Section: "Install > Requirements"}), sections)
	want(t, "path", got, "9:Go 1.27 and\ngit.\nRequirements\nin a block")
	got = read(t, sel(markup.Selector{Target: "section", Section: "Install"}), sections)
	want(t, "twice", got, "5:Run make.\nRequirements\nGo 1.27 and\ngit.\nRequirements\nin a block", "33:Again.")
	got = read(t, sel(markup.Selector{Target: "section", Section: "errata > Requirements"}), sections)
	want(t, "not a parent", got)
	got = read(t, sel(markup.Selector{Target: "section", Section: "Nowhere"}), sections)
	want(t, "none", got)
	got = read(t, sel(markup.Selector{Target: "section", Section: "Requirements", Pattern: `(\w+)\.$`}), sections)
	want(t, "match lines", got, "12:git", "31:Nothing")
}

// Formatting marks pair over the lines of one paragraph, as Asciidoctor
// pairs them, and a comment line in the paragraph is no part of it; they
// do not pair from one paragraph into the next, or into a list item. A
// match's line is still its own. The planted breaks: the paragraph's lines
// read one by one (*bold kept), and a list item read as the paragraph's
// (_x and y_ paired).
func TestMarksPairOverAParagraph(t *testing.T) {
	body := "= A\n\n*bold\ntext* and _it\n// c\nalic_\n\n*not\n\npaired*\n_x\n* y_\n"
	got := read(t, sel(markup.Selector{Target: "section", Section: "A"}), body)
	want(t, "whole", got, "1:bold\ntext and it\nalic\n*not\npaired*\n_x\ny_")
	got = read(t, sel(markup.Selector{Target: "section", Section: "A", Pattern: `(text|alic|paired)`}), body)
	want(t, "lines", got, "4:text", "6:alic", "10:paired")
}

// Titles take = or #, one to six, with the same marks after them left out.
// A title under [discrete] or [float], or in a sidebar, is prose and names
// no section, and a title in a paragraph is text. The planted breaks: the
// trailing marks kept (A == named nothing), [discrete] ignored, and a title
// read inside a paragraph.
func TestTitles(t *testing.T) {
	body := "= Doc\n\n== A ==\n\none\n\n## B\n\ntwo\n\n[discrete]\n=== C\n\nthree\ntext\n== D\n\nfour\n" +
		"\n[float]\n=== E\n****\n=== F\nfive\n****\n"
	for _, c := range []struct{ section, want string }{
		{"A", "3:one"},
		{"Doc > B", "7:two\nC\nthree\ntext\n== D\nfour\nE\nF\nfive"},
		{"C", ""}, {"D", ""}, {"E", ""}, {"F", ""},
	} {
		got := read(t, sel(markup.Selector{Target: "section", Section: c.section}), body)
		var w []string
		if c.want != "" {
			w = []string{c.want}
		}
		want(t, c.section, got, w...)
	}
}

// A two line title, a line over an underline of = - ~ ^ or + as long as
// it, give or take one character, is refused at its underline, as
// Asciidoctor reads it as a title the reader does not read. Only the first
// line of a paragraph where a section can start, outside every delimited
// block, can be one, and only a line with a letter or a digit that starts
// with no dot. The planted breaks: the length counted in bytes (Größe
// passes), the check on every line of a paragraph (more over ---- refused),
// and the check inside a block (Run: in an example refused).
func TestTwoLineTitlesAreRefused(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"Install\n-------\n\ntext\n", "line 2: the line before over this line of - is a two line title, which the asciidoc extractor does not read"},
		{"= A\n\nGo\n~~~\n", "line 4: the line before over this line of ~"},
		{"x\n\nNAME\n+++\n", "line 4: the line before over this line of +"},
		{"Größe\n=====\n", "line 2: the line before over this line of ="},
		{"1.2\n^^^\n", "line 2: the line before over this line of ^"},
	} {
		_, err := fromDataErr(sel(markup.Selector{Target: "code"}), c.body)
		extracttest.WantError(t, err, "README.adoc: "+c.want)
	}
	got := read(t, sel(markup.Selector{Target: "code"}), "Run the tests:\n----\nmake\n----\n\ntext\nmore\n----\nx\n----\n")
	want(t, "no title", got, "3:make", "9:x")
	for _, open := range []string{"====", "****", "____", "--", "[NOTE]\n===="} {
		close := open[strings.LastIndexByte(open, '\n')+1:]
		got = read(t, sel(markup.Selector{Target: "code"}), open+"\nRun:\n----\nmake\n----\n"+close+"\n")
		w := "4:make"
		if strings.HasPrefix(open, "[") {
			w = "5:make"
		}
		want(t, "in "+open, got, w)
	}
	got = read(t, sel(markup.Selector{Target: "code"}), "::::\n----\nmake\n----\n")
	want(t, "no letter", got, "3:make")
	got = read(t, sel(markup.Selector{Target: "code"}), ". item\n------\nx\n------\n")
	want(t, "a dot first", got, "3:x")
}

// A block with no closing line is refused, at the line that opened the
// innermost such block, since it would run to the end of the file. The
// planted break: the check left out.
func TestUnclosedBlocksAreRefused(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"////\nport: 1\n", `line 1: the block opened here by "////" has no closing line, so it would run to the end of the file`},
		{"= A\n\n====\n----\nx\n====\n", `line 4: the block opened here by "----" has no closing line`},
		{"|===\n|a\n", `line 1: the block opened here by "|===" has no closing line`},
	} {
		_, err := fromDataErr(sel(markup.Selector{Target: "comment"}), c.body)
		extracttest.WantError(t, err, "README.adoc: "+c.want)
	}
}

// A heading path keeps every other target to its sections, once each where
// sections of the path nest. The planted break: a named section left open
// at the next title of its level, which gives c.
func TestAPathScopesEveryTarget(t *testing.T) {
	body := "= A\n\n// a\n\n== A\n\n// b\n\n= B\n\n// c\n"
	got := read(t, sel(markup.Selector{Target: "comment", Section: "A"}), body)
	want(t, "nested", got, "3:a", "7:b")
}

const code = "= Build\n\n[source,sh]\n----\nmake build\n----\n\n" +
	"== Test\n\n[,sh]\n----\n  go test \\\n  ./...\n----\n\n" +
	"```yaml\nport: 8080\n```\n\n----\n----\n\n....\nliteral\n....\n\n  indented\n\n" +
	"[source%linenums,python]\n----\nprint(1)\n----\n"

// A code target reads listing, literal and fenced blocks, each line as the
// block holds it, at the line of its first, or the opening delimiter's for
// an empty block. Its language is the second positional attribute under
// source or no style, or the word after ```. The planted breaks: the style
// read with its %linenums (python missed), an empty block's line left
// unset, and a second attribute line in place of the first (make missed).
// Attribute lines one after another merge, as Asciidoctor merges them: a
// later style replaces the first, and [#id] keeps it, so verse is no
// source block.
func TestCodeBlocksByLanguage(t *testing.T) {
	got := read(t, sel(markup.Selector{Target: "code", Language: "sh"}), code)
	want(t, "sh", got, "5:make build", "12:  go test \\\n  ./...")
	got = read(t, sel(markup.Selector{Target: "code", Section: "Test", Pattern: `port: (\d+)`}), code)
	want(t, "in a section", got, "17:8080")
	got = read(t, sel(markup.Selector{Target: "code", Language: "python"}), code)
	want(t, "options", got, "31:print(1)")
	got = read(t, sel(markup.Selector{Target: "code"}), code)
	want(t, "every block", got, "5:make build", "12:  go test \\\n  ./...", "17:port: 8080", "20:", "24:literal", "31:print(1)")
	got = read(t, sel(markup.Selector{Target: "code", Pattern: `(x*)`}), "----\n----\n")
	want(t, "empty", got, "1:")
	merged := "[source,sh]\n[#id.role]\n[[anchor]]\n----\nmake\n----\n\n[source,sh]\n[python]\n----\nprint\n----\n\n[,go]\n[source]\n----\nx\n----\n\n[verse,sh]\n[#v]\n----\nnot code\n----\n"
	got = read(t, sel(markup.Selector{Target: "code", Language: "sh"}), merged)
	want(t, "merged", got, "5:make")
	got = read(t, sel(markup.Selector{Target: "code", Language: "go"}), merged)
	want(t, "merged", got, "17:x")
}

const tables = "= Hosts\n\n|===\n| Host | Port | Path\n\n" +
	"| web | 8080 | `/a\\|b`\n" +
	"| *db*\n| 5432 \\| 5433\n| x\n\n" +
	"| web | 9090 | multi\nline\n|===\n\n" +
	"[cols=\"2*\"]\n|===\n| Host | Other\n| web | 1\n|===\n\n" +
	"[%header,cols=\"1,1\"]\n|===\n| Host | Port\n| web | 1\n|===\n\n" +
	"|===\n| a | b\n| c | d\n|===\n"

// A table target reads the cell under the header column of each row, or of
// the rows whose key cell holds row, read as prose, at its row's line. A
// header row is the first line when a blank line follows it, or the first
// row under %header. Rows run over lines, as many cells to a row as the
// first line or cols gives. The planted breaks: the key cell compared
// before its marks are left out (db missed), the backslash of \| kept, and
// a table with no blank line after its first read with a header.
func TestTableCellsByColumnAndKey(t *testing.T) {
	got := read(t, sel(markup.Selector{Target: "table", Column: "Port", Key: "Host", Row: "web"}), tables)
	want(t, "web", got, "6:8080", "11:9090", "24:1")
	got = read(t, sel(markup.Selector{Target: "table", Column: "Path"}), tables)
	want(t, "path", got, "6:/a|b", "7:x", "11:multi\nline")
	got = read(t, sel(markup.Selector{Target: "table", Column: "Port", Key: "Host", Row: "db", Pattern: `(\d+)`}), tables)
	want(t, "pattern", got, "7:5432", "7:5433")
	got = read(t, sel(markup.Selector{Target: "table", Column: "b"}), tables)
	want(t, "no header", got)
	got = read(t, sel(markup.Selector{Target: "table", Column: "Other"}), tables)
	want(t, "no header under cols", got)
	three := "|===\n|Host |Port |Proto\n\n|web |80 |tcp\n|db |5432 |tcp\n|===\n"
	for _, cols := range []string{"3", "'1,1,1'", `"1;2;3"`, `"2*,<.^1a"`, `"1, ~ ,20%"`, `"<,^,>"`} {
		got = read(t, sel(markup.Selector{Target: "table", Column: "Host"}), "[cols="+cols+"]\n"+three)
		want(t, "cols="+cols, got, "5:web", "6:db")
	}
	for _, cols := range []string{"X", `"1,foo"`, "0", `"1,2*ab"`, `"-1"`} {
		_, err := fromDataErr(sel(markup.Selector{Target: "table", Column: "Host"}), "[cols="+cols+"]\n"+three)
		extracttest.WantError(t, err, "README.adoc: line 2: the table has cols ")
	}
}

// A cell specifier before a separator is no part of a cell: alone before a
// line's first separator, or after a blank, so "x a|" ends the cell before
// with x and makes the next an AsciiDoc cell. One that spans or repeats a
// cell, or a table of another format, is refused. The planted break: a
// specifier alone before a line's first separator read as text (e then m).
func TestTableForms(t *testing.T) {
	body := "|===\n|A |B\n\n|a |b\nx a|c d|e\nm|f |g\n|===\n"
	got := read(t, sel(markup.Selector{Target: "table", Column: "B"}), body)
	want(t, "specs", got, "4:b\nx", "5:e", "6:g")
	got = read(t, sel(markup.Selector{Target: "table", Column: "A"}), body)
	want(t, "specs", got, "4:a", "5:c", "6:f")
	for _, c := range []struct{ body, want string }{
		{"|===\n|A\n\n2+|a\n|===\n", "line 1: the table has a cell that spans columns or rows or is repeated, at line 4, which the asciidoc extractor does not read; " + core.OtherWay},
		{"|===\n|A\n\n.2+^|a\n|===\n", "line 1: the table has a cell that spans"},
		{"|===\n|A\n\n|x 3*|a\n|===\n", "line 1: the table has a cell that spans"},
		{",===\nA\n\na\n,===\n", "line 1: the table is in CSV or DSV form, which the asciidoc extractor does not read; " + core.OtherWay},
		{":===\nA\n\na\n:===\n", "line 1: the table is in CSV or DSV form"},
		{"\n[format=csv]\n|===\n|===\n", `line 3: the table has the format "csv"`},
		{"[separator=;]\n|===\n|===\n", "line 2: the table has a separator of its own"},
	} {
		_, err := fromDataErr(sel(markup.Selector{Target: "table", Column: "A"}), c.body)
		extracttest.WantError(t, err, "README.adoc: "+c.want)
	}
	got = read(t, sel(markup.Selector{Target: "table", Column: "A"}), "[format=psv,options=\"header\"]\n|===\n|A\n|a\n|===\n")
	want(t, "psv", got, "4:a")
	got = read(t, sel(markup.Selector{Target: "table", Column: "A"}), "!===\n!A !B\n\n!8 !9\n!===\n")
	want(t, "!", got, "4:8")
	got = read(t, sel(markup.Selector{Target: "table", Column: "A"}), "[%noheader]\n|===\n|A\n\n|a\n|===\n")
	want(t, "noheader", got)
}

// Build refuses a spec that names no target or one errata does not read, a
// field its target does not read, and one its target needs left out, by
// what is wrong, under the name asciidoc.
func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{}, "asciidoc: target is required; write comment, section, code or table"},
		{sel(markup.Selector{Target: "heading"}), `asciidoc: target "heading" is not one of comment, section, code and table`},
		{sel(markup.Selector{Target: "comment", Language: "sh"}), "asciidoc: language is read only by target code, and this one is comment"},
		{sel(markup.Selector{Target: "section"}), "asciidoc: target section needs section, the heading path"},
		{sel(markup.Selector{Target: "table"}), "asciidoc: target table needs column"},
		{sel(markup.Selector{Target: "code", Pattern: "x"}), "asciidoc: pattern \"x\" has no capture group"},
		{Spec{OneFile: core.OneFile{MaxBytes: -1}, Selector: markup.Selector{Target: "code"}}, "asciidoc: max_bytes is -1"},
	} {
		if c.spec.File == "" {
			c.spec.File = "README.adoc"
		}
		_, err := Build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
	_, err := Build(sel(markup.Selector{Target: "code"}))
	extracttest.WantError(t, err, "asciidoc: file is required")
}

// specs are a spec of each target, with and without a pattern and a path.
var specs = []Spec{
	sel(markup.Selector{Target: "comment"}), sel(markup.Selector{Target: "comment", Pattern: `(\S+)`, Section: "A"}),
	sel(markup.Selector{Target: "section", Section: "A"}), sel(markup.Selector{Target: "section", Section: "Install", Pattern: `(\S+)`}),
	sel(markup.Selector{Target: "code"}), sel(markup.Selector{Target: "code", Pattern: `(?s)(.+)`}),
	sel(markup.Selector{Target: "code", Pattern: `(\S*)`, Section: "B"}),
	sel(markup.Selector{Target: "table", Column: "Port"}), sel(markup.Selector{Target: "table", Column: "Port", Key: "Host", Row: "web", Pattern: `(.)`}),
}

// fromDataErr is the values s reads from body, or its refusal.
func fromDataErr(s Spec, body string) ([]check.Value, error) {
	s.File = "README.adoc"
	e, err := Build(s)
	if err != nil {
		return nil, err
	}
	return e.(extractor).FromData([]byte(body))
}

// A lone CR ends a line as a LF does, so a section's match after one is at
// the line an editor shows; a \v is a character of its line, as \S reads
// it, and ends none, as Asciidoctor splits lines at LF alone. The case of
// gate 59da540's FuzzAsciidoc failure, whose property split at LF alone.
// The planted break: the file read without its line ends folded, which
// reads no heading after a lone CR. 99eabea, which neither folded nor
// counted a lone CR, numbered t\vx at line 2.
func TestALoneCREndsAnAsciiDocLine(t *testing.T) {
	for body, want := range map[string]string{
		"== Install\n\rt\vx\nin a block\n": "t\vx@3 in@4 a@4 block@4",
		"= T\r== Install\rt\n":             "t@3", // the heading is a line of its own
	} {
		values, err := fromDataErr(sel(markup.Selector{Target: "section", Section: "Install", Pattern: `(\S+)`}), body)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if got := extracttest.Texts(values); got != want {
			t.Errorf("%q: got %q, want %q", body, got, want)
		}
	}
}

// Every value of every target is at a line inside its file, on samples of
// each target's construct.
func TestEveryValueLineIsInsideItsFile(t *testing.T) {
	for _, body := range []string{comments, sections, code, tables, lines, "", "= A", "----\n----", "|===\n|Port\n\n|\n|==="} {
		n := core.LastLine([]byte(body))
		for _, s := range specs {
			values, err := fromDataErr(s, body)
			if err != nil {
				t.Fatalf("%+v over %q: %v", s, body, err)
			}
			for _, v := range values {
				if v.At.Line < 1 || v.At.Line > n {
					t.Errorf("%+v over %q: %q at line %d of %d", s, body, v.Text, v.At.Line, n)
				}
			}
		}
	}
}

// lines is a file of each construct whose line a value could miss: a
// listing block attached to a list item, a table in a quote block whose
// rows run over lines, and a section's matches across a comment and a
// quote block, which break its prose into runs.
const lines = "= A\r\n\r\nx\r\n\r\n// c1\r\n\r\n* item\r\n+\r\n[source,sh]\r\n----\r\necho hi\r\n----\r\n\r\n" +
	"____\r\n|===\r\n| Host | Port\r\n\r\n| web | 80\r\n| lazy\r\n| 81\r\n|===\r\n____\r\n\r\n== B\r\n\r\n" +
	"para one\r\n// skipped\r\nlazy two 2.0\r\n____\r\nquote 3.0\r\n____\r\nlast 4.0\r\n"

// Every target gives the exact line of the unnormalised file: with CRLF,
// after a byte order mark, and after attribute entries, which move every
// line by three. The planted break: the line of a match counted in the text
// rather than mapped back to the file.
func TestExactLines(t *testing.T) {
	cases := []struct {
		spec Spec
		want []string
	}{
		{sel(markup.Selector{Target: "comment"}), []string{"5:c1", "27:skipped"}},
		{sel(markup.Selector{Target: "code"}), []string{"11:echo hi"}},
		{sel(markup.Selector{Target: "table", Column: "Port"}), []string{"18:80", "19:81"}},
		{sel(markup.Selector{Target: "table", Column: "Port", Key: "Host", Row: "lazy"}), []string{"19:81"}},
		{sel(markup.Selector{Target: "section", Section: "A > B", Pattern: `(one)`}), []string{"26:one"}},
		{sel(markup.Selector{Target: "section", Section: "B", Pattern: `(\d\.\d)`}), []string{"28:2.0", "30:3.0", "32:4.0"}},
		{sel(markup.Selector{Target: "code", Pattern: `(hi)`}), []string{"11:hi"}},
	}
	for _, prefix := range []struct {
		text  string
		lines int
	}{{"", 0}, {"\xef\xbb\xbf", 0}, {":toc:\r\n:a-b!: x\r\n\r\n", 3}} {
		for _, c := range cases {
			got := read(t, c.spec, prefix.text+lines)
			var expect []string
			for _, w := range c.want {
				line, text, _ := strings.Cut(w, ":")
				var n int
				_, _ = fmt.Sscan(line, &n)
				expect = append(expect, fmt.Sprintf("%d:%s", n+prefix.lines, text))
			}
			want(t, fmt.Sprintf("%q %+v", prefix.text, c.spec.Selector), got, expect...)
		}
	}
}

// The values over every target together are held to the cap on values,
// with a pattern and without. Without one, each item is a value, so the
// read stops at the cap, before the blocks nested too deep after it. The
// planted break: no stop in add, which refuses the nesting instead.
func TestValuesAreCapped(t *testing.T) {
	body := strings.Repeat("// a\n", core.MaxValues+1)
	for _, s := range []Spec{sel(markup.Selector{Target: "comment"}), sel(markup.Selector{Target: "comment", Pattern: "(a)"})} {
		_, err := fromDataErr(s, body)
		extracttest.WantError(t, err, "README.adoc: the extractor selects more than 100000 values, the limit")
	}
	_, err := fromDataErr(sel(markup.Selector{Target: "comment"}), body+strings.Repeat("====\n****\n", core.MaxDepth))
	extracttest.WantError(t, err, "README.adoc: the extractor selects more than 100000 values, the limit")
}

// A read holds at most one piece for every four bytes of max_bytes: items,
// runs of lines and table cells. The planted break: the cap one more.
func TestPiecesAreCapped(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		body string
	}{
		{sel(markup.Selector{Target: "comment"}), strings.Repeat("//\n", 11)},
		{sel(markup.Selector{Target: "section", Section: "A"}), "= A\n" + strings.Repeat("a\n\n", 9)},
		{sel(markup.Selector{Target: "table", Column: "A"}), "|===\n" + strings.Repeat("|", 11) + "\n|===\n"},
	} {
		c.spec.MaxBytes = 40
		_, err := fromDataErr(c.spec, c.body)
		extracttest.WantError(t, err, "the read is made of more than 10 comments, blocks, cells, sections and runs of lines, the limit, one for every 4 bytes of max_bytes")
		c.spec.MaxBytes = 44
		if _, err := fromDataErr(c.spec, c.body); err != nil {
			t.Errorf("%+v at 11 pieces: %v", c.spec.Selector, err)
		}
	}
}

// A paragraph's lines one after another hold one piece, however many: the
// title's run, the section and the paragraph are three. The planted break:
// a stretch of lines never grown, which holds a piece for each line.
func TestAParagraphIsOnePiece(t *testing.T) {
	s := sel(markup.Selector{Target: "section", Section: "A"})
	s.MaxBytes = 12
	got, err := fromDataErr(s, "= A\n\na\na\n// c\na\n")
	if err == nil {
		t.Errorf("a paragraph broken by a comment is two pieces, over the limit of 3: %q", show(got))
	}
	got, err = fromDataErr(s, "= A\n\na\na\na\na\n")
	if err != nil {
		t.Fatal(err)
	}
	want(t, "one piece", show(got), "1:a\na\na\na")
}

// A code block's lines one after another are one run, however many: the
// run and the block are two pieces. The planted break: a run started at
// every line.
func TestACodeBlockIsOneRun(t *testing.T) {
	s := sel(markup.Selector{Target: "code"})
	s.MaxBytes = 8
	got, err := fromDataErr(s, "----\na\nb\nc\nd\n----\n")
	if err != nil {
		t.Fatal(err)
	}
	want(t, "one run", show(got), "2:a\nb\nc\nd")
}

// Delimited blocks nest at most core.MaxDepth deep. The planted break: the
// depth checked after the push.
func TestNestingIsBounded(t *testing.T) {
	opens := strings.Repeat("====\n****\n", core.MaxDepth/2)
	_, err := fromDataErr(sel(markup.Selector{Target: "code"}), opens+"--\n")
	extracttest.WantError(t, err, fmt.Sprintf("README.adoc: line %d: nested more than %d levels deep", core.MaxDepth+1, core.MaxDepth))
	if _, err := fromDataErr(sel(markup.Selector{Target: "code"}), opens+strings.Repeat("****\n====\n", core.MaxDepth/2)); err != nil {
		t.Errorf("at the limit: %v", err)
	}
}

// Formatting marks are left out where they pair as Asciidoctor pairs them,
// and kept where they do not. The planted breaks: a mark after a word char
// read as opening (x_y and z_), and a backslash kept.
func TestProseMarks(t *testing.T) {
	var m marks
	for _, c := range []struct{ in, want string }{
		{"*bold* and _it_ and `mono` and #mark#", "bold and it and mono and mark"},
		{"**b**old and __i__t", "bold and it"},
		{"snake_case_name and 5 * 3 * 2", "snake_case_name and 5 * 3 * 2"},
		{"x_y and z_", "x_y and z_"},
		{"C# and F# and issue #12", "C# and F# and issue #12"},
		{`\*not bold* and \_x_`, "*not bold* and _x_"},
		{"*a _b_ c*", "a b c"},
		{"**", "**"}, {"* x*", "* x*"}, {"**x", "**x"}, {"****", "****"},
		{"*a **b** c*", "a b c"}, {"**a** x **b**", "a x b"}, {"*a* b*", "a b*"},
		{`x\`, `x\`}, {"x *", "x *"}, {`\\*a*`, `\*a*`}, {"_a_b_", "a_b"},
	} {
		if got := string(m.appendProse([]byte(">"), []byte(c.in))); got != ">"+c.want {
			t.Errorf("%q: prose %q, want %q", c.in, got, ">"+c.want)
		}
	}
}
