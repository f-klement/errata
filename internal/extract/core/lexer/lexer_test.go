package lexer

import (
	"bytes"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// spans are the bodies of the spans of kind k that l finds in src.
func spans(t *testing.T, name, src string, k Kind) []string {
	t.Helper()
	l, ok := Named(name)
	if !ok {
		t.Fatalf("no language %q", name)
	}
	var out []string
	for s := range l.Spans([]byte(src)) {
		if s.Kind == k {
			out = append(out, src[s.BodyStart:s.BodyEnd])
		}
	}
	return out
}

// Each form of the table reads as its language writes it: the comments come
// out with their delimiters gone, and a comment opener inside a literal, or a
// literal's quote inside a comment, opens nothing. Planted breaks: Nests
// ignored; Doubled ignored; Short read as a plain quote; a fence's length not
// kept for the closer; After ignored; LineStart ignored; the language's
// Escape ignored.
func TestFormsReadAsTheLanguageWritesThem(t *testing.T) {
	for _, c := range []struct {
		lang, src string
		comments  []string
	}{
		{"go", "x := \"// no\" // yes\n", []string{" yes"}},
		{"go", "x := `/* no\n*/` /* yes */", []string{" yes "}},
		{"go", "r := '\"' // a \"quote\"\n", []string{" a \"quote\""}},
		{"go", "s := \"unclosed // still string\n// yes", []string{" yes"}},
		{"c", "s = \"a\\\" // no\"; // yes", []string{" yes"}},
		{"cpp", "s = R\"x(a )\" // no)x\"; // yes", []string{" yes"}},
		{"rust", "/* a /* b */ c */ x", []string{" a /* b */ c "}},
		{"rust", "let s = r##\"a \"# // no\"##; // yes", []string{" yes"}},
		{"rust", "fn f<'a>(x: &'a str) {} // it's here\nlet c = '\\''; // yes", []string{" it's here", " yes"}},
		{"rust", "fn f<'a>(x: &'a str) -> &'a str { x } // yes", []string{" yes"}},
		{"swift", "let s = ##\"a \"# // no\"##; // yes", []string{" yes"}},
		{"kotlin", "val s = \"\"\"a \" // no\"\"\" // yes", []string{" yes"}},
		{"csharp", "var s = @\"C:\\\"\" // no\"; // yes", []string{" yes"}},
		{"csharp", "var s = \"\"\"\"a \"\"\" // no\"\"\"\"; // yes", []string{" yes"}},
		{"javascript", "const s = `a\n// no ${x}` // yes", []string{" yes"}},
		{"css", "a { content: \"/* no */\"; } /* yes */", []string{" yes "}},
		{"python", "s = '''a\n# no\n''' # yes", []string{" yes"}},
		{"shell", "echo \\\"# no\necho ${#x} # yes\necho a#b '#no' \"#no\"", []string{" yes"}},
		{"ruby", "x = 1\n=begin\nyes\n=end\ny = '#no'", []string{"\nyes\n"}},
		{"perl", "my $n = $#a; # yes\n=pod\npod\n=cut\n", []string{" yes", "\npod\n"}},
		{"yaml", "a: don't # yes\nb: 'it''s # no' # yes2\n", []string{" yes", " yes2"}},
		{"toml", "a = '''\n# no\n''' # yes", []string{" yes"}},
		{"dockerfile", "  # yes\nRUN echo # no\n", []string{" yes"}},
		{"makefile", "a = \\# no # yes\n", []string{" yes"}},
		{"hcl", "a = \"# no\" # yes\n// yes2", []string{" yes", " yes2"}},
		{"sql", "select 'it''s -- no' -- yes\n/* yes2 */", []string{" yes", " yes2 "}},
		{"lua", "s = [==[a ]] -- no]==] --[[ yes\n]] -- yes2", []string{" yes\n", " yes2"}},
		{"html", "<a title=\"<!-- no -->\">don't</a><!-- yes -->", []string{" yes "}},
		{"xml", "<a><![CDATA[<!-- no -->]]></a><!-- yes -->", []string{" yes "}},
		{"yaml", "run: |\n  # no\n\n  echo hi # no\n# yes\n", []string{" yes"}},
		{"yaml", "- run: >-\n    # no\n  name: x # yes\n- |\n  # no\n- b # yes2\n", []string{" yes", " yes2"}},
		{"yaml", "a: x | y # yes\n", []string{" yes"}},
		{"kotlin", "val s = \"\"\"a\"\"\"\" // yes", []string{" yes"}},
		{"sql", "select $$ -- no $$, $fn$ -- no $fn$, E'a\\' -- no', [x -- no], `y -- no` -- yes", []string{" yes"}},
		{"sql", "create function f() as $migration_function$\n  -- no\n$migration_function$; -- yes", []string{" yes"}},
		{"yaml", "key: |\n    text\n  # yes\nnext: 1\n", []string{" yes"}},
		{"yaml", "key: |2\n    a\n   # no\nnext: 1 # yes\n", []string{" yes"}},
		{"yaml", "--- |\n# no\ntext\n--- # yes\n", []string{" yes"}},
		{"swift", "let s = #\"\"\"\n// no\n\"\"\"# // yes", []string{" yes"}},
	} {
		if got := spans(t, c.lang, c.src, Comment); !slices.Equal(got, c.comments) {
			t.Errorf("%s %q: comments %q, want %q", c.lang, c.src, got, c.comments)
		}
	}
}

// spanList is every span l finds in src, as kind, body, and "runs off" for
// one that runs off the end.
func spanList(t *testing.T, name, src string) []string {
	t.Helper()
	l, ok := Named(name)
	if !ok {
		t.Fatalf("no language %q", name)
	}
	var out []string
	for s := range l.Spans([]byte(src)) {
		if s.Kind == Text && s.BodyStart == s.BodyEnd {
			continue // the <? a PHP file starts with
		}
		item := [...]string{Comment: "comment", String: "string", Name: "name", Text: "text"}[s.Kind] + " " + src[s.BodyStart:s.BodyEnd]
		if s.RunsOff(len(src)) {
			item += " (runs off)"
		}
		out = append(out, item)
	}
	return out
}

// What docs/reference.md#comment says the lexer does not understand, as it
// reads it. Each case pins a stated limit, so a change to one shows here and
// the reference is read again.
func TestKnownLimits(t *testing.T) {
	for _, c := range []struct {
		lang, src string
		want      []string
	}{
		// A quote of the string's own kind inside interpolated code ends the
		// string there, and what follows can read as a comment.
		{"csharp", "var s = $\"{d[\"// k\"]}\";", []string{"string {d[", "comment  k\"]}\";"}},
		{"hcl", "s = \"${x[\"#\"]}\" # c", []string{"string ${x[", "comment \"]}\" # c"}},
		// A heredoc's text is code: a # in it is a comment, and a quote in it
		// opens a string that runs on, here off the end of the file.
		{"shell", "cat <<EOF\n# in heredoc\nEOF\n", []string{"comment  in heredoc"}},
		{"shell", "cat <<EOF\nUsage: don't run\nEOF\n# real\n", []string{"string t run\nEOF\n# real\n (runs off)"}},
		// A regular expression after a value's end, such as a closing
		// parenthesis, reads as a division, so its quote opens a string.
		{"javascript", "if (x) /'/.test(s) // c\nx = 1 // d\n", []string{"string /.test(s) // c", "comment  d"}},
		// Perl's $" and $' are variables, read as a quote.
		{"perl", "local $\" = ', ';\n# real\n", []string{"string  = ', ';\n# real\n (runs off)"}},
		// JSX text is code.
		{"typescript", "const a = <p>See https://example.com</p>;\n", []string{"comment example.com</p>;"}},
		{"javascript", "const a = <p>Don't</p>; // c\n", []string{"string t</p>; // c"}},
		// SQL dialects: MySQL's backslash escape and # comment are not read,
		// and a block comment does not nest.
		{"sql", "select 'it\\'s -- no' from t; -- c\n", []string{"string it\\", "comment  no' from t; -- c"}},
		{"sql", "select 1 # c\n", nil},
		{"sql", "/* a /* b */ c */ x\n", []string{"comment  a /* b "}},
		// Elixir's ?" and PHP's #[ are not read.
		{"elixir", "c = ?\" # one\n", []string{"string  # one\n (runs off)"}},
		{"php", "<?php #[Attr('x')] $a = 1;\n", []string{"comment [Attr('x')] $a = 1;"}},
		// A line comment continued with a backslash ends at its line.
		{"c", "// a \\\nb // c\n", []string{"comment  a \\", "comment  c"}},
		// A quote after a blank inside a YAML plain scalar opens a string.
		{"yaml", "a: x 'y # z\nb: 1 # c\n", []string{"string y # z\nb: 1 # c\n (runs off)"}},
		// make ends a value at # inside quotes; the lexer reads a string.
		{"makefile", "X = \"a # b\"\n", []string{"string a # b"}},
		// The content of a script element is read as HTML.
		{"html", "<script>var s = \"<!-- x -->\"; // y\n</script>\n", []string{"comment  x "}},
		// More code in strings and constructs the lexer reads as code.
		{"shell", "echo \"$(date # x)\" # c\n", []string{"string $(date # x)", "comment  c"}},
		{"ruby", "c = ?\" # one\n# two\n", []string{"string  # one\n# two\n (runs off)"}},
		{"perl", "my @a = qw(a #b);\n", []string{"comment b);"}},
		{"perl", "print $'; # c\n", []string{"string ; # c\n (runs off)"}},
		{"hcl", "a = <<EOF\n# x\nEOF\n", []string{"comment  x"}},
		{"javascript", "const a = <p>{/* c */}</p>;\n", []string{"comment  c "}},
		// A fence past its bound runs off the end, so the rule is unknown,
		// in every fenced form; one at its bound reads right.
		{"rust", "r" + strings.Repeat("#", 17) + "\"a \"# // x\"" + strings.Repeat("#", 17), []string{"string  (runs off)"}},
		{"rust", "r" + strings.Repeat("#", 16) + "\"a \"# // x\"" + strings.Repeat("#", 16), []string{"string a \"# // x"}},
		{"swift", strings.Repeat("#", 18) + "\"// x\"" + strings.Repeat("#", 18), []string{"string  (runs off)"}},
		{"csharp", strings.Repeat(`"`, 20) + "// x" + strings.Repeat(`"`, 20) + " // yes", []string{"string  (runs off)"}},
		{"lua", "[" + strings.Repeat("=", 17) + "[ -- x ]" + strings.Repeat("=", 17) + "]", []string{"string  (runs off)"}},
		{"cpp", "R\"" + strings.Repeat("d", 17) + "(// x)" + strings.Repeat("d", 17) + "\"", []string{"string  (runs off)"}},
		{"sql", "$" + strings.Repeat("t", 64) + "$ -- x $" + strings.Repeat("t", 64) + "$", []string{"string  (runs off)"}},
		{"sql", "$" + strings.Repeat("t", 63) + "$ -- x $" + strings.Repeat("t", 63) + "$ -- c", []string{"string  -- x ", "comment  c"}},
		// A comment on a YAML block scalar's header line is part of the scalar.
		{"yaml", "key: >- # head\n  # in\nnext: 1 # c\n", []string{"string   # in\n", "comment  c"}},
	} {
		if got := spanList(t, c.lang, c.src); !slices.Equal(got, c.want) {
			t.Errorf("%s %q:\n got %q\nwant %q", c.lang, c.src, got, c.want)
		}
	}
}

// A literal's span is its body, which the literal reader reads: a quoted
// name is no literal, and a Rust lifetime opens none.
func TestLiteralsAreTheirBodies(t *testing.T) {
	for _, c := range []struct {
		lang, src string
		strings   []string
	}{
		{"go", "a := \"x\" + `y` + 'z'", []string{"x", "y", "z"}},
		{"rust", "fn f<'a>(s: &'a str) { r#\"q\"# }", []string{"q"}},
		{"sql", "select \"col\" from t where a = 'v'", []string{"v"}},
		{"python", "x = \"\"\"doc\"\"\" # c", []string{"doc"}},
		{"swift", "let s = #\"\"\"\nabc\n\"\"\"#", []string{"\nabc\n"}},
		{"swift", "let s = ##\"a\"#b\"##", []string{"a\"#b"}},
		{"kotlin", "val s = \"\"\"a\"\"\"\" // c", []string{"a\""}},
		{"sql", "select $$a$$, $fn$b$$c$fn$, E'd\\'e', `n`, [m]", []string{"a", "b$$c", "d\\'e"}},
		{"yaml", "run: |\n  echo # x\n  y\nb: 1\n", []string{"  echo # x\n  y\n"}},
	} {
		if got := spans(t, c.lang, c.src, String); !slices.Equal(got, c.strings) {
			t.Errorf("%s %q: strings %q, want %q", c.lang, c.src, got, c.strings)
		}
	}
}

// A line comment ends before a CRLF's carriage return, and one at the end of
// the file without a line feed is closed; a block comment the file ends in is
// not.
func TestEnds(t *testing.T) {
	l, _ := Named("go")
	src := "// a\r\n// b"
	var got []Span
	for s := range l.Spans([]byte(src)) {
		got = append(got, s)
	}
	if len(got) != 2 || src[got[0].BodyStart:got[0].BodyEnd] != " a" || got[0].End != 5 || !got[1].Closed || got[1].End != len(src) {
		t.Errorf("%q: %+v", src, got)
	}
	for s := range l.Spans([]byte("x /* open")) {
		if s.Closed || s.End != 9 || s.BodyEnd != 9 {
			t.Errorf("an unclosed block comment: %+v", s)
		}
	}
}

// A file is chosen its language by its base name, then by the longest
// extension, in any case; one no row lists has none.
func TestFor(t *testing.T) {
	for file, want := range map[string]string{
		"a/b/main.go": "go", "Dockerfile": "dockerfile", "web/Makefile": "makefile", "x.TF": "hcl",
		"lib.HPP": "cpp", "a.d.ts": "typescript", "ci.yml": "yaml", "a\\b.py": "python",
	} {
		if l, ok := For(file); !ok || l.Name != want {
			t.Errorf("%s: %v, want %s", file, l, want)
		}
	}
	for _, file := range []string{"README.md", "go", ".go", "Dockerfile.prod", "noext"} {
		if l, ok := For(file); ok {
			t.Errorf("%s: chose %s", file, l.Name)
		}
	}
}

// problems are what is wrong with rows as a table: a row with no name, a
// name two rows share, a row with no family, no file name or no forms, an
// extension not lower case or with no dot, an extension two rows list, a
// form with no opener or no kind, an opener that ends in a carriage return,
// and a fenced form with no FenceMax or one past its bound (fenceBound),
// whose walk would be quadratic on a run of its fence byte, a string form
// with no closer that is no heredoc or block scalar, which would end at its
// line as a comment does, and a tag with no TagMax. Spans takes the
// CR before a line comment's line feed off its body without checking that
// the body is not empty, so an opener ending in a CR would give a body that
// ends before it starts; this check is the guard (comment delta review 4).
func problems(rows []Language) []string {
	var out []string
	names := map[string]bool{}
	ext := map[string]string{}
	for _, l := range rows {
		if l.Name == "" || names[l.Name] || l.Family == "" || len(l.Extensions)+len(l.Files) == 0 || len(l.Forms) == 0 {
			out = append(out, "row "+l.Name+": a name of its own, a family, a file name and forms")
		}
		names[l.Name] = true
		for _, e := range l.Extensions {
			if !strings.HasPrefix(e, ".") || strings.ToLower(e) != e {
				out = append(out, l.Name+": extension "+e)
			}
			if other, dup := ext[e]; dup {
				out = append(out, other+" and "+l.Name+" both list "+e)
			}
			ext[e] = l.Name
		}
		for _, f := range l.Forms {
			if f.Open == "" || f.Kind == 0 {
				out = append(out, l.Name+": a form with no opener or no kind")
			}
			if strings.HasSuffix(f.Open, "\r") {
				out = append(out, l.Name+": opener "+strconv.Quote(f.Open)+" ends in a carriage return")
			}
			if f.Fence != "" && (f.FenceMax <= 0 || f.FenceMax > fenceBound(f)) {
				out = append(out, l.Name+": fence "+f.Open+f.Fence+" with no FenceMax up to its bound")
			}
			if f.Kind == String && f.Close == "" && !f.Heredoc && !f.Indented {
				out = append(out, l.Name+": string "+f.Open+f.Inner+" with no closer")
			}
			if f.Tag != "" && f.TagMax <= 0 {
				out = append(out, l.Name+": tag after "+f.Open+" with no TagMax")
			}
		}
	}
	return out
}

// fenceBound is the longest FenceMax f may have: maxFence where a fence byte
// is its opener's or its closer's first byte, so a run of that byte starts a
// fence at each of its bytes, and maxTag otherwise.
func fenceBound(f Form) int {
	if strings.IndexByte(f.Fence, f.Open[0]) >= 0 || f.Close != "" && strings.IndexByte(f.Fence, f.Close[0]) >= 0 {
		return maxFence
	}
	return maxTag
}

// The table has none of the problems.
func TestTheTableIsWellFormed(t *testing.T) {
	if p := problems(Languages()); len(p) > 0 {
		t.Errorf("%q", p)
	}
	if !strings.Contains(Names(), "go, c, cpp") {
		t.Errorf("Names: %s", Names())
	}
}

// Each problem is found in a row that has it. Planted breaks, each in
// problems: the duplicate extension not checked; the lower case not
// checked; the form's kind not checked; the fence's FenceMax not checked;
// the opener's carriage return not checked; the string's closer not
// checked; the TagMax not checked.
func TestProblemsAreFound(t *testing.T) {
	ok := Form{Kind: Comment, Open: "#"}
	rows := []Language{
		{Name: "a", Family: "f", Extensions: []string{".A"}, Forms: []Form{ok}},
		{Name: "b", Family: "f", Extensions: []string{".x"}, Forms: []Form{{Open: "#"}}},
		{Name: "c", Family: "f", Extensions: []string{".x"}, Forms: []Form{ok}},
		{Name: "d", Family: "f", Files: []string{"D"}, Forms: []Form{{Kind: String, Open: "r", Fence: "#", Inner: `"`, Close: `"`}}},
		{Name: "e", Family: "f", Files: []string{"E2"}, Forms: []Form{{Kind: String, Open: "$", Fence: "a", FenceMax: maxTag + 1, Inner: "$", Close: "$", Outer: "$"}}},
		{Name: "f", Family: "f", Files: []string{"F"}, Forms: []Form{{Kind: String, Open: "#", Fence: "#", FenceMax: maxTag, Inner: `"`, Close: `"`, Outer: "#"}}},
		{Name: "g", Family: "f", Files: []string{"G"}, Forms: []Form{{Kind: Comment, Open: "--\r"}}},
		{Name: "h", Family: "f", Files: []string{"H"}, Forms: []Form{{Kind: String, Open: "~", Inner: ", "}, {Kind: String, Open: "~", Tag: "s", Inner: "(", Close: ")"}}},
		{Name: "", Family: "f", Files: []string{"E"}, Forms: []Form{ok}},
	}
	want := []string{
		"a: extension .A",
		"b: a form with no opener or no kind",
		"b and c both list .x",
		`d: fence r# with no FenceMax up to its bound`,
		`e: fence $a with no FenceMax up to its bound`,
		`f: fence ## with no FenceMax up to its bound`,
		`g: opener "--\r" ends in a carriage return`,
		"h: string ~,  with no closer",
		"h: tag after ~ with no TagMax",
		"row : a name of its own, a family, a file name and forms",
	}
	if got := problems(rows); !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// walkTime is the fastest of runs walks of data in the row named name.
func walkTime(t *testing.T, name string, data []byte, runs int) time.Duration {
	t.Helper()
	l, _ := Named(name)
	fastest := time.Duration(math.MaxInt64)
	for range runs {
		start := time.Now()
		for range l.Spans(data) {
			// Drain the spans: the walk itself is what is timed.
		}
		fastest = min(fastest, time.Since(start))
	}
	return fastest
}

// A walk is linear in the file, on the two shapes that made it quadratic
// before every fence had a bound (maxFence): a run of Swift's #, each of which
// opened a fence that ran to the end of the run, and a C# raw string whose
// body holds runs of quotes one short of its closer, which a closer of
// unbounded length compared at each of their bytes; and SQL's dollar tags,
// bounded at maxTag, on runs of tags one short of it. Four times the input
// takes at most eight times as long, and a tenth of a second. Planted
// breaks: Swift's FenceMax left out; C#'s left out. Each made the larger
// input take sixteen times as long (8 KiB of # took 0.19 s, 32 KiB 3 s).
func TestFencesAreLinear(t *testing.T) {
	for _, c := range []struct {
		name  string
		shape func(n int) []byte
		n     int
	}{
		{"swift", func(n int) []byte { return bytes.Repeat([]byte("#"), n) }, 8 << 10},
		{"sql", func(n int) []byte {
			return bytes.Repeat([]byte("$"+strings.Repeat("t", maxTag-1)+" "), n/(maxTag+1))
		}, 128 << 10},
		{"csharp", func(n int) []byte {
			q := strings.Repeat(`"`, n/2)
			return []byte(`"""` + q + "x" + q + `""` + strings.Repeat("y", n/2))
		}, 128 << 10},
	} {
		quarter := walkTime(t, c.name, c.shape(c.n), 3)
		whole := walkTime(t, c.name, c.shape(4*c.n), 3)
		t.Logf("%s: %d bytes took %v, a quarter of them %v", c.name, 4*c.n, whole, quarter)
		if limit := 8*quarter + 100*time.Millisecond; whole > limit {
			t.Errorf("%s: %d bytes took %v, over %v, eight times the %v of a quarter of them and a tenth of a second", c.name, 4*c.n, whole, limit, quarter)
		}
	}
}

// A row a caller has is a copy: changing its forms changes neither the
// table nor what the row's next copy reads. Planted break: Named returning
// the row with its forms shared.
func TestRowsAreCopies(t *testing.T) {
	l, _ := Named("go")
	l.Forms[0].Open = "@@"
	l.Extensions[0] = ".changed"
	if again, _ := Named("go"); again.Forms[0].Open != "//" || again.Extensions[0] != ".go" {
		t.Errorf("a change to a copy reached the table: %+v", again.Forms[0])
	}
	if got := spans(t, "go", "x // c", Comment); !slices.Equal(got, []string{" c"}) {
		t.Errorf("after a change to a copy, go reads %q", got)
	}
}
