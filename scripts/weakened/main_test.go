package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// findings runs check on a change and returns its lines.
func findings(t *testing.T, in input) []string {
	t.Helper()
	found := check(in)
	lines := make([]string, len(found))
	for i, f := range found {
		lines[i] = f.String()
	}
	return lines
}

func files(kv ...string) map[string][]byte {
	m := map[string][]byte{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = []byte(kv[i+1])
	}
	return m
}

func same(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("found\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

const twoTests = `package a

import "testing"

func TestA(t *testing.T) {
	if f() != 1 {
		t.Error("f")
	}
}

func FuzzB(f *testing.F) {
	f.Fuzz(func(t *testing.T, s string) { t.Fatal(s) })
}
`

// A test that is gone is a finding at its base line; one renamed, or moved to
// another file, with the same body is not. The planted break: TestA dropped.
func TestGoneTests(t *testing.T) {
	head := `package a

import "testing"

func FuzzB(f *testing.F) {
	f.Fuzz(func(t *testing.T, s string) { t.Fatal(s) })
}
`
	same(t, findings(t, input{base: files("a/a_test.go", twoTests), head: files("a/a_test.go", head)}),
		"a/a_test.go:5 (base): TestA is gone, and no test at the head has its body")
	renamed := strings.Replace(twoTests, "TestA", "TestRenamed", 1)
	same(t, findings(t, input{base: files("a/a_test.go", twoTests), head: files("a/a_test.go", renamed)}))
	same(t, findings(t, input{base: files("a/a_test.go", twoTests), head: files("a/a_test.go", head, "a/b_test.go", "package a\n\nfunc TestA(t *testing.T) {\n\t// moved\n\tif f() != 1 {\n\t\tt.Error(\"f\")\n\t}\n}\n")}))
	// A deleted file's tests are gone too.
	same(t, findings(t, input{base: files("a/a_test.go", twoTests), head: files()}),
		"a/a_test.go:5 (base): TestA is gone, and no test at the head has its body",
		"a/a_test.go:11 (base): FuzzB is gone, and no test at the head has its body")
	// A method named like a test is no test.
	same(t, findings(t, input{base: files("a/a_test.go", "package a\n\nfunc (s *suite) TestX() {}\n"), head: files("a/a_test.go", "package a\n")}))
}

const tableSrc = `package a

import "testing"

var shared = []struct{ in, want string }{{"x", "y"}}

func TestT(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"one": {"1", "a"},
		"two": {in: "2", want: "b"},
	} {
		_ = name
		_ = c
	}
	for _, c := range []*struct{ name, in string }{
		{name: "first", in: "1"},
		&struct{ name, in string }{name: "second", in: "2"},
	} {
		_ = c
	}
	for _, c := range []struct{ in string }{{"p"}, {"q"}, {"r"}} {
		_ = c
	}
	_ = []int{1, 2}
}
`

// A named row gone, by map key or name field, and fewer unnamed rows, are
// findings at the base; an edited row keeps its name, and a row whose text
// moved to another table is not gone. The planted breaks: the row "two", the
// row "second" and the row {"q"} dropped.
func TestGoneRows(t *testing.T) {
	head := strings.NewReplacer(`		"two": {in: "2", want: "b"},`+"\n", "",
		`		&struct{ name, in string }{name: "second", in: "2"},`+"\n", "",
		`{"p"}, {"q"}, {"r"}`, `{"p"}, {"r"}`).Replace(tableSrc)
	same(t, findings(t, input{base: files("a/a_test.go", tableSrc), head: files("a/a_test.go", head)}),
		`a/a_test.go:10 (base): row "two" of a table in TestT is gone`,
		`a/a_test.go:17 (base): row "second" of a table in TestT is gone`,
		`a/a_test.go:21 (base): a table in TestT has 2 rows with no name, against 3 at the base`)
	edited := strings.NewReplacer(`"one": {"1", "a"}`, `"one": {"1", "z"}`, `{"q"}`, `{"Q"}`).Replace(tableSrc)
	same(t, findings(t, input{base: files("a/a_test.go", tableSrc), head: files("a/a_test.go", edited)}))
	moved := strings.Replace(tableSrc, `{"p"}, {"q"}, {"r"}`, `{"p"}, {"r"}`, 1) +
		"\nfunc TestU(t *testing.T) { _ = []struct{ in string }{{\"q\"}} }\n"
	same(t, findings(t, input{base: files("a/a_test.go", tableSrc), head: files("a/a_test.go", moved)}))
	// A table whose rows took a field is found by its place among the
	// tables of its test, so its rows are compared with the base's.
	retyped := strings.Replace(tableSrc, `[]struct{ in string }{{"p"}, {"q"}, {"r"}}`, `[]struct{ in, out string }{{"p", ""}, {"q", ""}, {"r", ""}}`, 1)
	same(t, findings(t, input{base: files("a/a_test.go", tableSrc), head: files("a/a_test.go", retyped)}))
	retyped = strings.Replace(retyped, `{"q", ""}, `, "", 1)
	same(t, findings(t, input{base: files("a/a_test.go", tableSrc), head: files("a/a_test.go", retyped)}),
		`a/a_test.go:21 (base): a table in TestT has 2 rows with no name, against 3 at the base`)
	// A table whose declaration is gone is left to the test that is gone.
	same(t, findings(t, input{base: files("a/a_test.go", tableSrc), head: files("a/a_test.go", "package a\n\nfunc f() {}\n")}),
		"a/a_test.go:7 (base): TestT is gone, and no test at the head has its body")
}

// A skip on an added line is a finding; one the base had is not, nor a skip
// whose line was rewritten in a test that skips no more often than at the
// base (TestB). The planted break: the added lines left out.
func TestAddedSkips(t *testing.T) {
	head := `package a

import "testing"

func TestA(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	t.SkipNow()
	defer t.Cleanup(func() {})
	skip()
}

func TestB(t *testing.T) {
	t.Skip("reworded")
}
`
	base := strings.NewReplacer("\tif testing.Short() {\n\t\tt.Skip(\"short\")\n\t}\n\tt.SkipNow()\n", "", "reworded", "old").Replace(head)
	added := map[string]map[int]bool{"a/a_test.go": {6: true, 7: true, 9: true, 15: true}}
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", head), added: added}),
		"a/a_test.go:6: testing.Short in TestA, on an added line",
		"a/a_test.go:7: t.Skip in TestA, on an added line",
		"a/a_test.go:9: t.SkipNow in TestA, on an added line")
}

// A line that called a helper that skips at the base and was rewritten, as
// in 8e78bec4 where goBin's callers gained an argument, skips no more often:
// no finding. A second call added beside it is one. The planted break: the
// counts at the base and the head not compared.
func TestRewrittenHelperCall(t *testing.T) {
	base := "package a\n\nimport \"testing\"\n\nfunc goBin(t *testing.T) string {\n\tt.Skip(\"no go\")\n\treturn \"\"\n}\n\nfunc TestG(t *testing.T) {\n\tindex(goBin(t), \"a\")\n}\n"
	head := strings.Replace(base, `index(goBin(t), "a")`, `index(goBin(t), "a", "b")`, 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", head), added: map[string]map[int]bool{"a/a_test.go": {11: true}}}))
	head = strings.Replace(head, `"a", "b")`, `"a", "b")`+"\n\tindex(goBin(t), \"c\")", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", head), added: map[string]map[int]bool{"a/a_test.go": {11: true, 12: true}}}),
		"a/a_test.go:11: goBin in TestG, on an added line, calls a helper that skips",
		"a/a_test.go:12: goBin in TestG, on an added line, calls a helper that skips")
}

// A skip whose if was dropped, or whose condition now always holds, turns a
// test off though the count of skips stays level: each is a finding. The
// planted breaks: the conditions left out of a skip's key, and a guard's
// line not read as the skip's.
func TestChangedGuards(t *testing.T) {
	base := `package a

import (
	"os"
	"testing"
)

func TestS(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if os.Getenv("SLOW") == "" {
		t.Skip("set SLOW")
	}
}
`
	dropped := strings.Replace(base, "\tif testing.Short() {\n\t\tt.Skip(\"short\")\n\t}\n", "\tt.Skip(\"short\")\n", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", dropped), added: map[string]map[int]bool{"a/a_test.go": {9: true}}}),
		"a/a_test.go:9: t.Skip in TestS, on an added line")
	always := strings.Replace(base, `if os.Getenv("SLOW") == "" {`, "if true {", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", always), added: map[string]map[int]bool{"a/a_test.go": {12: true}}}),
		"a/a_test.go:13: t.Skip in TestS, under a condition the change rewrote: if true")
	inElse := strings.Replace(base, "\t\tt.Skip(\"set SLOW\")\n\t}\n", "\t\tprintln()\n\t} else {\n\t\tt.Skip(\"set SLOW\")\n\t}\n", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", inElse), added: map[string]map[int]bool{"a/a_test.go": {13: true, 14: true, 15: true}}}),
		"a/a_test.go:15: t.Skip in TestS, on an added line")
}

// A guard is an if's init and condition, over every line they span, printed
// in one form whatever the source's line breaks: a rewritten init or a
// rewritten second line of a condition is a finding, a condition or a call's
// arguments only split over lines is none. The planted breaks: the init left
// out of the guard, only a condition's first line read as its line, and the
// guard printed with the source's positions.
func TestGuardShapes(t *testing.T) {
	base := `package a

import (
	"os"
	"testing"
)

func TestI(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("no null")
	}
	if os.Getenv("A") == "" ||
		os.Getenv("B") == "" {
		t.Skip("set A and B")
	}
}
`
	at := func(lines ...int) map[string]map[int]bool {
		m := map[int]bool{}
		for _, l := range lines {
			m[l] = true
		}
		return map[string]map[int]bool{"a/a_test.go": m}
	}
	init := strings.Replace(base, `os.Stat("/dev/null")`, `os.Stat("/nonexistent")`, 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", init), added: at(9)}),
		`a/a_test.go:10: t.Skip in TestI, under a condition the change rewrote: if _, err := os.Stat("/nonexistent"); err != nil`)
	second := strings.Replace(base, "\t\tos.Getenv(\"B\") == \"\" {", "\t\ttrue {", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", second), added: at(13)}),
		`a/a_test.go:14: t.Skip in TestI, under a condition the change rewrote: if os.Getenv("A") == "" || true`)
	joined := strings.Replace(base, "||\n\t\tos.Getenv", "|| os.Getenv", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", joined), added: at(12)}))
	// A call's arguments one a line, with the trailing comma that needs,
	// joined onto one line: the guard is the same.
	split := strings.Replace(base, `os.Stat("/dev/null")`, "os.Stat(\n\t\t\"/dev/null\",\n\t)", 1)
	same(t, findings(t, input{base: files("a/a_test.go", split), head: files("a/a_test.go", base), added: at(9)}))
}

// A skip in an else runs when the if's condition fails, and its guard says
// so: `if !(<cond>)`, and, down an else-if chain, each condition that must
// fail before the one that holds. The planted break: an else's condition
// printed as it stands.
func TestElseGuards(t *testing.T) {
	base := `package a

import (
	"os"
	"testing"
)

func TestE(t *testing.T) {
	if os.Getenv("SLOW") != "" {
		println()
	} else {
		t.Skip("set SLOW")
	}
	if os.Getenv("A") != "" {
		println()
	} else if os.Getenv("B") == "" {
		t.Skip("set A or B")
	}
}
`
	head := strings.Replace(base, `if os.Getenv("SLOW") != "" {`, "if false {", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", head), added: map[string]map[int]bool{"a/a_test.go": {9: true}}}),
		"a/a_test.go:12: t.Skip in TestE, under a condition the change rewrote: if !(false)")
	head = strings.Replace(base, `if os.Getenv("A") != "" {`, "if false {", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", head), added: map[string]map[int]bool{"a/a_test.go": {14: true}}}),
		`a/a_test.go:17: t.Skip in TestE, under a condition the change rewrote: if !(false); if os.Getenv("B") == ""`)
}

// Two long values that differ past the clip print where they differ, cut on
// a rune's start; short ones print whole. The planted break: each value
// clipped alone.
func TestClipPair(t *testing.T) {
	for _, c := range [][4]string{
		{strings.Repeat("x", 70) + "AAAAAAAAAA", strings.Repeat("x", 70) + "BBBBBBBBBB", "...xxxxxxxxxxAAAAAAAAAA", "...xxxxxxxxxxBBBBBBBBBB"},
		{"x" + strings.Repeat("é", 40) + "ya", "x" + strings.Repeat("é", 40) + "yb", "..." + strings.Repeat("é", 5) + "ya", "..." + strings.Repeat("é", 5) + "yb"},
		{"1", "2", "1", "2"},
		{"prefix 15 byte " + strings.Repeat("a", 60), "prefix 15 byte " + strings.Repeat("b", 60), "prefix 15 byte " + strings.Repeat("a", 42) + "...", "prefix 15 byte " + strings.Repeat("b", 42) + "..."},
		{"\"same start\" + x", "\"same start\" + y", "\"same start\" + x", "\"same start\" + y"},
	} {
		if a, b := clipPair(c[0], c[1]); a != c[2] || b != c[3] {
			t.Errorf("clipPair(%q, %q) = %q, %q; want %q, %q", c[0], c[1], a, b, c[2], c[3])
		}
	}
}

// A test with fewer assertions is a finding at its head line; err.Error() is
// no assertion. The planted break: an if with its t.Errorf dropped.
func TestFewerAsserts(t *testing.T) {
	base := `package a

import "testing"

func TestA(t *testing.T) {
	err := f()
	if err != nil {
		t.Fatalf("%v", err.Error())
	}
	if g() {
		t.Errorf("g")
	}
	t.Run("sub", func(t *testing.T) { t.Fail() })
	t.FailNow()
	t.Error()
}
`
	head := strings.Replace(base, "\tif g() {\n\t\tt.Errorf(\"g\")\n\t}\n", "", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", head)}),
		"a/a_test.go:5: TestA makes 3 assertions, against 4 at the base")
	more := strings.Replace(base, "t.FailNow()", "t.FailNow()\n\tt.Fatal(1)", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", more)}))
}

const wants = `package a

import "testing"

var wantAll = "all"

func TestW(t *testing.T) {
	want := 1
	want = 2
	var expected, other = "e", "o"
	for name, c := range map[string]struct{ in, want string }{
		"one": {in: "1", want: "a"},
	} {
		_, _ = name, c
	}
	for _, c := range []struct{ in, wantErr string }{{in: "1", wantErr: "x"}} {
		_ = c
	}
	_, _, _, _ = want, expected, other, wantAll
	f(want)
}
`

// A want value that changed is a finding when the change also edits code; a
// testdata file it edited or deleted is one too. With no code edited, or a
// value left alone, neither is. The planted breaks: each want edited.
func TestChangedWants(t *testing.T) {
	head := strings.NewReplacer(`wantAll = "all"`, `wantAll = "every"`, "want = 2", "want = 3",
		`"e", "o"`, `"E", "O"`, `want: "a"`, `want: "b"`, `wantErr: "x"`, `wantErr: "y"`).Replace(wants)
	in := input{base: files("a/a_test.go", wants), head: files("a/a_test.go", head), golden: []string{"a/testdata/x.golden"}, code: "a/a.go"}
	same(t, findings(t, in),
		"a/a_test.go:5: wantAll in wantAll changed from \"all\" to \"every\", in a change that also edits a/a.go",
		"a/a_test.go:9: want in TestW changed from 2 to 3, in a change that also edits a/a.go",
		"a/a_test.go:10: expected in TestW changed from \"e\" to \"E\", in a change that also edits a/a.go",
		"a/a_test.go:12: want in TestW changed from \"a\" to \"b\", in a change that also edits a/a.go",
		"a/a_test.go:16: wantErr in TestW changed from \"x\" to \"y\", in a change that also edits a/a.go",
		"a/testdata/x.golden: a file under testdata changed, in a change that also edits a/a.go")
	in.code = ""
	same(t, findings(t, in))
	same(t, findings(t, input{base: files("a/a_test.go", wants), head: files("a/a_test.go", wants), code: "a/a.go"}))
}

func TestClip(t *testing.T) {
	long := strings.Repeat("x", 70)
	if got := clip(long); got != strings.Repeat("x", 57)+"..." {
		t.Errorf("clip of 70 bytes is %q", got)
	}
	if got := clip("a\n\tb"); got != "a b" {
		t.Errorf("clip of two lines is %q", got)
	}
}

// A file that does not parse is a finding at its side, and the other files
// are still read. The planted break: collect stopping at the first error.
func TestBrokenFile(t *testing.T) {
	fuzzOnly := strings.Replace(twoTests, "func TestA(t *testing.T) {\n\tif f() != 1 {\n\t\tt.Error(\"f\")\n\t}\n}\n", "", 1)
	same(t, findings(t, input{base: files("a/a_test.go", twoTests, "a/b_test.go", "package"), head: files("a/0_test.go", "package", "a/a_test.go", fuzzOnly)}),
		"a/0_test.go: does not parse at the head, so the check read nothing in it: a/0_test.go:1:8: expected 'IDENT', found 'EOF'",
		"a/a_test.go:5 (base): TestA is gone, and no test at the head has its body",
		"a/b_test.go (base): does not parse at the base, so the check read nothing in it: a/b_test.go:1:8: expected 'IDENT', found 'EOF'")
}

func TestAddedLines(t *testing.T) {
	diff := `diff --git a/a/a_test.go b/a/a_test.go
index 1..2 100644
--- a/a/a_test.go
+++ b/a/a_test.go
@@ -3,0 +4,2 @@ func x
+++ an added line that looks like a header
+b
@@ -10 +12 @@
-old
+new
diff --git a/a/gone_test.go b/a/gone_test.go
deleted file mode 100644
--- a/a/gone_test.go
+++ /dev/null
@@ -1 +0,0 @@
-package a
`
	got := addedLines([]byte(diff))
	want := map[string]map[int]bool{"a/a_test.go": {4: true, 5: true, 12: true}}
	if len(got) != 1 || len(got["a/a_test.go"]) != 3 || !got["a/a_test.go"][4] || !got["a/a_test.go"][5] || !got["a/a_test.go"][12] {
		t.Errorf("added lines %v, want %v", got, want)
	}
	if n := hunkStart("@@ broken"); n != 0 {
		t.Errorf("a header with no head range starts at %d", n)
	}
}

// load reads a real change through git: a test file edited, one added, one
// deleted, a testdata file edited and one added, a test file under testdata,
// and code. The first code file is the one named. Every test file of a
// touched package is read at both sides, and none of a package below it or of
// a package whose tests the change left alone.
func TestLoad(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(kv ...string) {
		for i := 0; i+1 < len(kv); i += 2 {
			p := filepath.Join(dir, kv[i])
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(kv[i+1]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	git("init", "-q")
	write("a/a_test.go", "package a\n", "a/gone_test.go", "package a\n", "a/keep_test.go", "package a\n", "a/sub/s_test.go", "package s\n",
		"a/testdata/x.golden", "x\n", "b/b.go", "package b\n", "b/b_test.go", "package b\n", "z/z.go", "package z\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	write("a/a_test.go", "package a\n\nfunc TestA() {}\n", "a/new_test.go", "package a\n", "a/testdata/x.golden", "y\n",
		"a/testdata/new.golden", "n\n", "a/testdata/c_test.go", "package c\n", "b/b.go", "package b\n\n", "z/z.go", "package z\n\n", "doc.md", "d\n")
	if err := os.Remove(filepath.Join(dir, "a/gone_test.go")); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "head")
	in, err := load(func(args ...string) ([]byte, error) {
		return execGit(append([]string{"-C", dir}, args...)...)
	}, base, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sortedKeys(in.base), []string{"a/a_test.go", "a/gone_test.go", "a/keep_test.go"}) || !slices.Equal(sortedKeys(in.head), []string{"a/a_test.go", "a/keep_test.go", "a/new_test.go"}) {
		t.Errorf("base files %v, head files %v", sortedKeys(in.base), sortedKeys(in.head))
	}
	if string(in.head["a/a_test.go"]) != "package a\n\nfunc TestA() {}\n" || string(in.base["a/a_test.go"]) != "package a\n" {
		t.Errorf("a_test.go read as %q at the base and %q at the head", in.base["a/a_test.go"], in.head["a/a_test.go"])
	}
	if !slices.Equal(in.golden, []string{"a/testdata/x.golden"}) || in.code != "b/b.go" {
		t.Errorf("golden %v, code %q", in.golden, in.code)
	}
	if !in.added["a/a_test.go"][2] || !in.added["a/a_test.go"][3] || in.added["a/a_test.go"][1] {
		t.Errorf("added lines %v", in.added)
	}
	for _, fail := range []string{"ls-tree", "show"} {
		if _, err := load(func(args ...string) ([]byte, error) {
			if args[0] == fail {
				return nil, errors.New("git broke")
			}
			return execGit(append([]string{"-C", dir}, args...)...)
		}, base, "HEAD"); err == nil {
			t.Errorf("a failed git %s passed", fail)
		}
	}
	if _, err := load(func(args ...string) ([]byte, error) {
		return execGit(append([]string{"-C", dir}, args...)...)
	}, "nosuchrev", "HEAD"); err == nil || !strings.Contains(err.Error(), "nosuchrev") {
		t.Errorf("a base that is no revision gave %v", err)
	}
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// The command prints one finding a line and exits 0 whatever it finds, a
// file that does not parse included, 1 when git fails, 2 on a wrong call.
func TestRun(t *testing.T) {
	fake := func(show string, fail string) gitRunner {
		return func(args ...string) ([]byte, error) {
			if args[0] == fail {
				return nil, errors.New("git broke")
			}
			switch {
			case args[0] == "diff":
				return []byte("D\x00a/a_test.go\x00"), nil
			case args[0] == "ls-tree" && args[3] == "b":
				return []byte("a/a_test.go\x00"), nil
			case args[0] == "show":
				return []byte(show), nil
			}
			return nil, nil
		}
	}
	for _, c := range []struct {
		args       []string
		git        gitRunner
		code       int
		out, error string
	}{
		{[]string{"b", "h"}, fake(twoTests, ""), 0,
			"a/a_test.go:5 (base): TestA is gone, and no test at the head has its body\na/a_test.go:11 (base): FuzzB is gone, and no test at the head has its body\n", ""},
		{[]string{"b", "h"}, fake("package a\n", ""), 0, "", ""},
		{[]string{"b", "h"}, fake("package", ""), 0, "a/a_test.go (base): does not parse at the base, so the check read nothing in it: a/a_test.go:1:8: expected 'IDENT', found 'EOF'\n", ""},
		{[]string{"b", "h"}, fake(twoTests, "diff"), 1, "", "weakened: git broke\n"},
		{[]string{"b", "h"}, fake(twoTests, "show"), 1, "", "weakened: git broke\n"},
		{[]string{"b"}, fake(twoTests, ""), 2, "", "usage: weakened <base> <head>\n"},
	} {
		var out, errOut bytes.Buffer
		if code := run(c.args, &out, &errOut, c.git); code != c.code || out.String() != c.out || errOut.String() != c.error {
			t.Errorf("run %v: %d, %q, %q; want %d, %q, %q", c.args, code, out.String(), errOut.String(), c.code, c.out, c.error)
		}
	}
}

const positional = `package a

import "testing"

type tc struct {
	name string
	in   string
	want int
}

func TestP(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     int
	}{
		{"one", "a", 1},
		{"two", "b", 2},
	} {
		_ = c
	}
	for _, c := range []tc{{"three", "c", 3}} {
		_ = c
	}
	for _, c := range []*tc{{"four", "d", 4}} {
		_ = c
	}
}
`

// A positional row's elements are its struct's fields in order, for a
// struct type literal and for one the file declares, through a pointer: its
// want changed is a finding, and its name field names it. The planted break:
// positional elements given no field.
func TestPositionalRows(t *testing.T) {
	head := strings.NewReplacer(`"a", 1}`, `"a", 7}`, `"b", 2}`, `"b", 9}`, `"c", 3}`, `"c", 8}`, `"d", 4}`, `"d", 6}`).Replace(positional)
	same(t, findings(t, input{base: files("a/a_test.go", positional), head: files("a/a_test.go", head), code: "a/a.go"}),
		"a/a_test.go:16: want in TestP changed from 1 to 7, in a change that also edits a/a.go",
		"a/a_test.go:17: want in TestP changed from 2 to 9, in a change that also edits a/a.go",
		"a/a_test.go:21: want in TestP changed from 3 to 8, in a change that also edits a/a.go",
		"a/a_test.go:24: want in TestP changed from 4 to 6, in a change that also edits a/a.go")
	// Embedded fields take their places too, named by their types.
	embedded := "package a\n\ntype base struct{}\n\ntype row struct {\n\t*base\n\tfmt.Stringer\n\tlist[int]\n\tname string\n\twant int\n}\n\nfunc TestE(t *testing.T) { _ = []row{{nil, nil, nil, \"x\", 1}} }\n"
	same(t, findings(t, input{base: files("a/a_test.go", embedded), head: files("a/a_test.go", strings.Replace(embedded, `"x", 1}`, `"x", 2}`, 1)), code: "a/a.go"}),
		"a/a_test.go:13: want in TestE changed from 1 to 2, in a change that also edits a/a.go")
	head = strings.Replace(positional, "\t\t{\"two\", \"b\", 2},\n", "", 1)
	same(t, findings(t, input{base: files("a/a_test.go", positional), head: files("a/a_test.go", head)}),
		`a/a_test.go:17 (base): row "two" of a table in TestP is gone`)
}

// An unnamed row's wants are keyed by its inputs, so a row inserted before
// it moves none of them; rows with the same inputs fall back to their place
// among those. The planted break: every unnamed row keyed by its place.
func TestUnnamedRowsKeyedByInputs(t *testing.T) {
	base := "package a\n\nfunc TestU(t *testing.T) {\n\t_ = []struct {\n\t\tin   string\n\t\twant int\n\t}{{in: \"a\", want: 1}, {in: \"b\", want: 2}, {in: \"c\", want: 3}, {in: \"c\", want: 4}}\n}\n"
	inserted := strings.Replace(base, "}{{in: \"a\"", "}{{in: \"\", want: 0}, {in: \"d\", want: 9}, {in: \"a\"", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", inserted), code: "a/a.go"}))
	repeated := strings.Replace(base, "want: 4}", "want: 5}", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", repeated), code: "a/a.go"}),
		"a/a_test.go:7: want in TestU changed from 4 to 5, in a change that also edits a/a.go")
}

// A literal an if compares with before it fails the test is a want value;
// one compared where the block fails nothing is not. The planted break: the
// check that the block fails the test dropped.
func TestInlineWants(t *testing.T) {
	base := `package a

func TestI(t *testing.T) {
	if got := f("abc"); got != 3 {
		t.Errorf("got %d", got)
	}
	if n := g(); -1 == n || h() != "x" {
		t.Fatal(n)
	}
	if v := k(); v != 5 {
		println(v)
	}
	if ok := func() bool { return m() == 7 }(); !ok {
		t.Fail()
	}
}
`
	head := strings.NewReplacer("got != 3", "got != 99", "-1 == n", "-2 == n", `!= "x"`, `!= "y"`, "v != 5", "v != 6", "m() == 7", "m() == 8").Replace(base)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", head), code: "a/a.go"}),
		"a/a_test.go:4: the value compared with got in TestI changed from 3 to 99, in a change that also edits a/a.go",
		"a/a_test.go:7: the value compared with h() in TestI changed from \"x\" to \"y\", in a change that also edits a/a.go",
		"a/a_test.go:7: the value compared with n in TestI changed from -1 to -2, in a change that also edits a/a.go")
}

// A //go:build line added to or changed in a test file the base had is a
// finding; one in a new file, one removed, or a comment after the package
// clause is not. The planted break: the line compared nowhere.
func TestBuildConstraints(t *testing.T) {
	plain := "package a\n\n//go:build later\n"
	never := "//go:build never\n\npackage a\n"
	linux := "//go:build linux\n\npackage a\n"
	same(t, findings(t, input{base: files("a/a_test.go", plain, "a/b_test.go", linux, "a/c_test.go", linux),
		head: files("a/a_test.go", never, "a/b_test.go", never, "a/c_test.go", plain, "a/d_test.go", never)}),
		"a/a_test.go:1: //go:build never, against none at the base: the file's tests run only where it holds",
		"a/b_test.go:1: //go:build never, against //go:build linux at the base: the file's tests run only where it holds")
}

// A call through a package, such as fmt.Errorf, is no assertion, and a
// package's Skip is no skip. The planted break: calls through a package
// counted.
func TestPackageCallsAreNoAssertions(t *testing.T) {
	base := "package a\n\nimport (\n\t\"fmt\"\n\tlg \"log\"\n)\n\nfunc TestE(t *testing.T) {\n\t_ = fmt.Errorf(\"x %d\", 1)\n\tlg.Fatal(\"y\")\n\tt.Error(\"z\")\n}\n"
	head := strings.NewReplacer("\t_ = fmt.Errorf(\"x %d\", 1)\n", "\tfmt.Skip()\n", "\tlg.Fatal(\"y\")\n", "").Replace(base)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", head), added: map[string]map[int]bool{"a/a_test.go": {9: true}}}))
	head = strings.Replace(base, "\tt.Error(\"z\")\n", "", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", head)}),
		"a/a_test.go:8: TestE makes 0 assertions, against 1 at the base")
}

const helpers = `package a

import "testing"

func skipSlow(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
}

func check(tb testing.TB, got int) {
	if got != 1 {
		tb.Errorf("got %d", got)
	}
}

func TestH(t *testing.T) {
	check(t, f())
	if f() != 2 {
		t.Error("f")
	}
}
`

// One level of calls into the package's helpers: a call on an added line of
// a helper that skips is a finding, a helper that lost assertions is named
// once, and a test's assertions moved into a new helper keep its count. The
// planted breaks: helper calls on added lines left out, and helpers counted
// as the head has them.
func TestHelpers(t *testing.T) {
	head := strings.Replace(helpers, "\tcheck(t, f())\n", "\tskipSlow(t)\n\tcheck(t, f())\n", 1)
	same(t, findings(t, input{base: files("a/a_test.go", helpers), head: files("a/a_test.go", head), added: map[string]map[int]bool{"a/a_test.go": {18: true}}}),
		"a/a_test.go:18: skipSlow in TestH, on an added line, calls a helper that skips")
	head = strings.Replace(helpers, "\t\ttb.Errorf(\"got %d\", got)\n", "\t\tprintln(got)\n", 1)
	same(t, findings(t, input{base: files("a/a_test.go", helpers), head: files("a/a_test.go", head)}),
		"a/a_test.go:11: helper check makes 0 assertions, against 1 at the base")
	head = strings.Replace(helpers, "\tif f() != 2 {\n\t\tt.Error(\"f\")\n\t}\n", "\tcheckTwo(t)\n", 1) +
		"\nfunc checkTwo(t *testing.T) {\n\tif f() != 2 {\n\t\tt.Error(\"f\")\n\t}\n}\n"
	same(t, findings(t, input{base: files("a/a_test.go", helpers), head: files("a/a_test.go", head)}))
	// A helper of another package does not count.
	same(t, findings(t, input{base: files("a/a_test.go", helpers), head: files("a/a_test.go", strings.Replace(head, "func checkTwo", "func checkOther", 1), "b/b_test.go", "package b\n\nfunc checkTwo(t *testing.T) { t.Error(1) }\n")}),
		"a/a_test.go:17: TestH makes 1 assertions, against 2 at the base")
}

// go test's rule for a test's name and signature. The planted break: any
// name with the prefix taken as a test.
func TestTestNames(t *testing.T) {
	base := "package a\n\nfunc TestLower(t *testing.T) { t.Error(1) }\n\nfunc Test(t *testing.T) { t.Error(2) }\n\nfunc Test_x(t *testing.T) { t.Error(3) }\n\nfunc TestNoParam() {}\n\nfunc FuzzWrong(t *testing.T) {}\n"
	head := strings.NewReplacer("TestLower", "Testlower", "func Test(", "func Testing(", "Test_x", "Test_y").Replace(base)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", head)}),
		"a/a_test.go:3 (base): TestLower is gone, and no test at the head has its body",
		"a/a_test.go:5 (base): Test is gone, and no test at the head has its body")
}

// A row's name is on one line, however its literal spans lines. The planted
// break: the raw literal printed.
func TestRowNamesOnOneLine(t *testing.T) {
	base := "package a\n\nfunc TestN(t *testing.T) {\n\t_ = map[string]struct{ in string }{\n\t\t`a\nb`: {in: \"1\"},\n\t\t\"c\":  {in: \"2\"},\n\t}\n}\n"
	head := strings.Replace(base, "\t\t`a\nb`: {in: \"1\"},\n", "", 1)
	same(t, findings(t, input{base: files("a/a_test.go", base), head: files("a/a_test.go", head)}),
		"a/a_test.go:5 (base): row `a b` of a table in TestN is gone")
}
