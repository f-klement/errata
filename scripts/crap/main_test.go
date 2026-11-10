package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sign and signProfile are scripts/casecover's fixture: the profile is what go
// test -cover -coverprofile printed for sign with Go 1.27.2 when TestSign
// called Sign(-1), Sign(0) and Sign(7). Four statements, three of them run.
const sign = `package p

// Sign names n's range.
func Sign(n int) string {
	switch {
	case n < 0:
		return "negative"
	case n > 100,
		n == 50:
		return "large"
	case n == 7:
	}
	return "other"
}
`

const signProfile = `mode: set
example.com/p/p.go:5.2,5.9 1 1
example.com/p/p.go:7.3,7.20 1 1
example.com/p/p.go:10.3,10.17 1 0
example.com/p/p.go:11.14,11.14 0 1
example.com/p/p.go:13.2,13.16 1 1
`

// branchy has a method of complexity 12 on a generic receiver: 1, an if with
// && and ||, a for, a range, a switch of three clauses with its default, a
// select of two with its default, and an if in a closure.
const branchy = `package q

type T[K any] struct{}

func (t *T[K]) Branchy(a, b bool, xs []int, c chan int) int {
	n := 0
	if a && b || a {
		n++
	}
	for i := 0; i < 2; i++ {
		n++
	}
	for range xs {
		n++
	}
	switch {
	case a:
		n++
	case b:
		n--
	default:
	}
	select {
	case <-c:
	default:
	}
	f := func() {
		if b {
			n++
		}
	}
	f()
	return n
}

func Small() int {
	return 1
}

func Empty() {}
`

// tree writes files under a fresh module root and returns the root.
func tree(t *testing.T, module string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module " + module + "\n\ngo 1.27\n"
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// bodyOf is the span of a function's body in branchy, from just after its { to
// its }, as a profile line writes it.
func bodyOf(t *testing.T, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", branchy, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			l, r := fset.Position(fd.Body.Lbrace), fset.Position(fd.Body.Rbrace)
			return fmt.Sprintf("%d.%d,%d.%d", l.Line, l.Column+1, r.Line, r.Column)
		}
	}
	t.Fatalf("no function %s", name)
	return ""
}

// branchyProfile covers each function of branchy whole: a block per body,
// with the statements given, and a run count of 1.
func branchyProfile(t *testing.T, extra ...string) string {
	t.Helper()
	lines := []string{
		"mode: atomic",
		"example.com/m/q/q.go:" + bodyOf(t, "Branchy") + " 20 1",
		"example.com/m/q/q.go:" + bodyOf(t, "Small") + " 1 1",
		"example.com/m/q/q.go:" + bodyOf(t, "Empty") + " 0 1",
	}
	return strings.Join(append(lines, extra...), "\n") + "\n"
}

// crap runs the command in root with the profile written beside it, and
// returns its status, stdout and stderr.
func crap(t *testing.T, root, prof string, args ...string) (int, string, string) {
	t.Helper()
	pf := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(pf, []byte(prof), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	rc := run(append(append([]string{"-root", root}, args...), pf), &out, &errb)
	return rc, out.String(), errb.String()
}

// scores runs -list and returns each function's line, by key.
func scores(t *testing.T, root, prof string, args ...string) map[string]string {
	t.Helper()
	rc, out, errb := crap(t, root, prof, append(args, "-list")...)
	if rc != 0 {
		t.Fatalf("crap -list: exit %d, %s", rc, errb)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		f := strings.Split(line, "\t")
		got[f[4]] = line
	}
	return got
}

func TestScoresARealProfile(t *testing.T) {
	root := tree(t, "example.com/p", map[string]string{"p.go": sign})
	got := scores(t, root, signProfile)
	// complexity 4 (three clauses), coverage 3/4: 16 * (1/4)^3 + 4.
	want := "4.25\t4\t0.750\t4\texample.com/p Sign\tp.go:4"
	if got["example.com/p Sign"] != want || len(got) != 1 {
		t.Errorf("scores = %q, want only %q", got, want)
	}
}

func TestComplexityIsCrappersCount(t *testing.T) {
	root := tree(t, "example.com/m", map[string]string{"q/q.go": branchy})
	got := scores(t, root, branchyProfile(t))
	want := map[string]string{
		"example.com/m/q.T Branchy": "12.00\t12\t1.000\t20\texample.com/m/q.T Branchy\tq/q.go:5",
		"example.com/m/q Small":     "1.00\t1\t1.000\t1\texample.com/m/q Small\tq/q.go:36",
		"example.com/m/q Empty":     "1.00\t1\t1.000\t0\texample.com/m/q Empty\tq/q.go:40",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("scores =\n%q\nwant\n%q", got, want)
	}
}

// A -coverpkg run lists a block once per test binary: the copies count once,
// covered when any copy ran.
func TestABlockListedTwiceCountsOnce(t *testing.T) {
	root := tree(t, "example.com/p", map[string]string{"p.go": sign})
	prof := signProfile + "example.com/p/p.go:10.3,10.17 1 1\nexample.com/p/p.go:5.2,5.9 1 0\n"
	got := scores(t, root, prof)["example.com/p Sign"]
	if want := "4.00\t4\t1.000\t4\texample.com/p Sign\tp.go:4"; got != want {
		t.Errorf("Sign = %q, want %q", got, want)
	}
}

// The partly covered score: complexity 12, half covered.
func TestCoverageCountsTheBlocksInTheBody(t *testing.T) {
	root := tree(t, "example.com/m", map[string]string{"q/q.go": branchy})
	prof := branchyProfile(t, "example.com/m/q/q.go:7.3,7.6 20 0")
	got := scores(t, root, prof)["example.com/m/q.T Branchy"]
	// 144 * (1/2)^3 + 12 = 30.
	if want := "30.00\t12\t0.500\t40\texample.com/m/q.T Branchy\tq/q.go:5"; got != want {
		t.Errorf("Branchy = %q, want %q", got, want)
	}
}

// What make cover leaves out of its total is left out here: tests, the
// packages named like httptest, generated files and files a build constraint
// leaves out. None of them is scored, and none is refused for having no block.
func TestTheLeftOutFilesAreNotScored(t *testing.T) {
	root := tree(t, "example.com/m", map[string]string{
		"q/q.go":          branchy,
		"q/q_test.go":     "package q\n\nfunc helper() int {\n\treturn 1\n}\n",
		"q/gen.go":        "// Code generated by hand. DO NOT EDIT.\n\npackage q\n\nfunc generated() int {\n\treturn 1\n}\n",
		"q/off.go":        "//go:build never\n\npackage q\n\nfunc off() int {\n\treturn 1\n}\n",
		"qtest/helper.go": "package qtest\n\nfunc Helper() int {\n\treturn 1\n}\n",
	})
	prof := branchyProfile(t, "example.com/m/qtest/helper.go:3.20,5.2 1 0")
	got := scores(t, root, prof, "-require", ".")
	if len(got) != 3 {
		t.Errorf("scored %d functions, want the 3 of q.go: %q", len(got), got)
	}
}

// Each refusal names its reason and exits 2, so a broken profile is never a
// table of 0% scores.
func TestRefusals(t *testing.T) {
	root := tree(t, "example.com/m", map[string]string{"q/q.go": branchy, "r/r.go": "package r\n"})
	for _, c := range []struct{ name, prof, want string }{
		{"no mode line", strings.SplitN(branchyProfile(t), "\n", 2)[1], "its first line is not a mode line"},
		{"a line that is not a block", branchyProfile(t, "example.com/m/q/q.go:7.3 1 1"), "does not parse at line 5"},
		{"no block", "mode: set\n", "holds no block"},
		{"a file outside the module", branchyProfile(t, "example.com/other/q.go:1.1,2.2 1 1"), "is not a file of module example.com/m"},
		{"a file the tree lacks", branchyProfile(t, "example.com/m/q/gone.go:1.1,2.2 1 1"), "which the tree at " + root + " lacks"},
		{"a path out of the root", branchyProfile(t, "example.com/m/../q.go:1.1,2.2 1 1"), "outside the module root"},
		{"one block, two statement counts", branchyProfile(t, "example.com/m/q/q.go:"+bodyOf(t, "Small")+" 2 1"), "is listed with 1 statements and with 2"},
		{"a function with statements and no block", "mode: set\nexample.com/m/q/q.go:" + bodyOf(t, "Branchy") + " 20 1\n", "example.com/m/q Small at q/q.go:36 has statements and no block"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rc, out, errb := crap(t, root, c.prof)
			if rc != 2 || out != "" || !strings.HasPrefix(errb, "crap: REFUSED: ") || !strings.Contains(errb, c.want) {
				t.Errorf("exit %d, stdout %q, stderr %q; want 2 and a refusal naming %q", rc, out, errb, c.want)
			}
		})
	}
}

// -require puts in scope each package under a directory, so a package the
// run did not instrument at all is refused rather than left unscored.
func TestRequireRefusesAPackageThatIsNotInTheProfile(t *testing.T) {
	root := tree(t, "example.com/m", map[string]string{
		"q/q.go":          branchy,
		"x/y/y.go":        "package y\n\nfunc Y() int {\n\treturn 1\n}\n",
		"x/testdata/t.go": "package t\n\nfunc T() int {\n\treturn 1\n}\n",
		"empty/README":    "",
	})
	if got := scores(t, root, branchyProfile(t)); len(got) != 3 {
		t.Fatalf("without -require, scored %q", got)
	}
	for _, c := range []struct{ dir, want string }{
		{"x", "example.com/m/x/y Y at x/y/y.go:3 has statements and no block"},
		{"empty", "-require empty holds no Go package"},
	} {
		rc, _, errb := crap(t, root, branchyProfile(t), "-require", c.dir)
		if rc != 2 || !strings.HasPrefix(errb, "crap: REFUSED: ") || !strings.Contains(errb, c.want) {
			t.Errorf("-require %s: exit %d, stderr %q; want a refusal naming %q", c.dir, rc, errb, c.want)
		}
	}
}

// writeFile writes a file in a fresh directory and returns its path.
func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The baseline's rules, each against branchy's Branchy at 12.00 and Small at 1.00.
func TestBaselineRules(t *testing.T) {
	root := tree(t, "example.com/m", map[string]string{"q/q.go": branchy})
	for _, c := range []struct {
		name, base string
		rc         int
		want       string
	}{
		{"a function at 10 or more with no row fails", "", 1,
			"example.com/m/q.T Branchy scores 12.00 (q/q.go:5, complexity 12, coverage 100.0%), and a function with no row in the baseline scores below 10"},
		{"a row at the score passes", "# a comment\n\nexample.com/m/q.T Branchy 12.00\n", 0,
			"crap: 3 functions scored; 1 at 10 or more, each in the baseline at its score or lower"},
		{"a row above the score passes", "example.com/m/q.T Branchy 12.50\n", 0, "each in the baseline"},
		{"a score above its row fails", "example.com/m/q.T Branchy 11.99\n", 1,
			"example.com/m/q.T Branchy rose from 11.99 to 12.00"},
		{"a row whose function is gone fails", "example.com/m/q.T Branchy 12.00\nexample.com/m/q Gone 15.00\n", 1,
			"stale row: example.com/m/q Gone (15.00) is gone from the scored tree; remove its row"},
		{"a row whose function scores below 10 fails", "example.com/m/q.T Branchy 12.00\nexample.com/m/q Small 10.00\n", 1,
			"stale row: example.com/m/q Small now scores 1.00 (q/q.go:36, complexity 1, coverage 100.0%), below 10; remove its row"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rc, out, errb := crap(t, root, branchyProfile(t), "-baseline", writeFile(t, c.base))
			if rc != c.rc || !strings.Contains(out, c.want) || errb != "" {
				t.Errorf("exit %d, stdout %q, stderr %q; want %d and %q", rc, out, errb, c.rc, c.want)
			}
		})
	}
}

func TestABaselineThatDoesNotParseIsRefused(t *testing.T) {
	root := tree(t, "example.com/m", map[string]string{"q/q.go": branchy})
	for _, c := range []struct{ base, want string }{
		{"example.com/m/q.T Branchy\n", "does not parse at line 1"},
		{"example.com/m/q.T Branchy twelve\n", "does not parse at line 1"},
		{"a b 12\na b 11\n", "a b has two rows"},
	} {
		rc, _, errb := crap(t, root, branchyProfile(t), "-baseline", writeFile(t, c.base))
		if rc != 2 || !strings.HasPrefix(errb, "crap: REFUSED: ") || !strings.Contains(errb, c.want) {
			t.Errorf("baseline %q: exit %d, stderr %q; want a refusal naming %q", c.base, rc, errb, c.want)
		}
	}
}

// -write-baseline lists each function at 10 or more, and the file it prints
// passes the check.
func TestWriteBaselinePassesItsOwnCheck(t *testing.T) {
	root := tree(t, "example.com/m", map[string]string{"q/q.go": branchy})
	rc, out, _ := crap(t, root, branchyProfile(t), "-write-baseline")
	rows := strings.Split(strings.TrimSpace(strings.TrimPrefix(out, baselineHeader)), "\n")
	if rc != 0 || !strings.HasPrefix(out, baselineHeader) || len(rows) != 1 || rows[0] != "example.com/m/q.T Branchy 12.00" {
		t.Fatalf("exit %d, baseline %q", rc, out)
	}
	if rc, out, _ := crap(t, root, branchyProfile(t), "-baseline", writeFile(t, out)); rc != 0 {
		t.Errorf("the written baseline fails its check: %s", out)
	}
}

// -shrinks holds a baseline to an older one: no new row, no higher score.
func TestShrinks(t *testing.T) {
	old := writeFile(t, "a x 12.00\nb y 15.00\nc z 20.00\n")
	for _, c := range []struct {
		cur  string
		rc   int
		want string
	}{
		{"a x 12.00\nb y 14.00\n", 0, "crap: " + "%s holds 2 rows, none new or higher than in %s"},
		{"a x 12.00\nd w 10.00\n", 1, "new row: d w (10.00) is not in the older baseline, and the baseline only shrinks"},
		{"a x 12.01\n", 1, "raised row: a x is 12.01, above the older baseline's 12.00"},
	} {
		cur := writeFile(t, c.cur)
		var out, errb bytes.Buffer
		rc := run([]string{"-baseline", cur, "-shrinks", old}, &out, &errb)
		want := c.want
		if c.rc == 0 {
			want = fmt.Sprintf(c.want, cur, old)
		}
		if rc != c.rc || !strings.Contains(out.String(), want) {
			t.Errorf("%q: exit %d, stdout %q, stderr %q; want %d and %q", c.cur, rc, out.String(), errb.String(), c.rc, want)
		}
	}
}

func TestTheCommandLineIsChecked(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"a", "b"},
		{"-list", "-write-baseline", "p"},
		{"-shrinks", "old"},
		{"-shrinks", "old", "-baseline", "new", "p"},
		{"-nope"},
	} {
		var out, errb bytes.Buffer
		if rc := run(args, &out, &errb); rc != 2 || !strings.Contains(errb.String(), "crap: ") {
			t.Errorf("%q: exit %d, stderr %q; want 2", args, rc, errb.String())
		}
	}
}

// A method is keyed by its receiver's type name, whatever wraps it, as
// crapper takes the receiver's first type identifier.
func TestMethodsAreKeyedByTheReceiversTypeName(t *testing.T) {
	src := `package p

type T struct{}
type P[K, V any] struct{}

func (T) A()            {}
func (t (T)) B()        {}
func (p *P[K, V]) C()   {}
func (p P[K, V]) D()    {}
func (p *(P[K, V])) E() {}
func F()                {}
`
	f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, fd := range funcs(f) {
		got = append(got, key("m/p", fd))
	}
	want := []string{"m/p.T A", "m/p.T B", "m/p.P C", "m/p.P D", "m/p.P E", "m/p F"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("keys = %q, want %q", got, want)
	}
}

// A -require directory that is not there, and a scored file that does not
// parse, are refused.
func TestAnUnreadableTreeIsRefused(t *testing.T) {
	root := tree(t, "example.com/m", map[string]string{"q/q.go": branchy, "q/broken.go": "package q\n\nfunc {\n"})
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-require", "nowhere"}, "nowhere: no such file or directory"},
		{nil, "q/broken.go:3:6: expected 'IDENT'"},
	} {
		rc, _, errb := crap(t, root, branchyProfile(t), c.args...)
		if rc != 2 || !strings.HasPrefix(errb, "crap: REFUSED: ") || !strings.Contains(errb, c.want) {
			t.Errorf("%q: exit %d, stderr %q; want a refusal naming %q", c.args, rc, errb, c.want)
		}
	}
}

// -only scores the packages named and holds only their rows: a gate that ran
// the tests reaching a change has exact coverage for those packages alone
// (ci/mutation-scope.sh, "The gate's scope"). A package left out is neither
// refused for having no block nor scored, and its rows stand, though one is
// stale and one names a package whose import path starts like q's.
func TestOnlyScoresThePackagesNamedAndHoldsTheirRows(t *testing.T) {
	root := tree(t, "example.com/m", map[string]string{
		"q/q.go":       branchy,
		"x/y/y.go":     "package y\n\nfunc Y() int {\n\treturn 1\n}\n",
		"empty/README": "",
	})
	got := scores(t, root, branchyProfile(t), "-only", "q/")
	if len(got) != 3 || got["example.com/m/q.T Branchy"] == "" {
		t.Fatalf("-only q scored %q, want q's three functions", got)
	}
	others := "example.com/m/x/y Gone 15.00\nexample.com/m/qq Z 15.00\nexample.com/m/qq.T Z 15.00\n"
	for _, c := range []struct {
		name, base string
		rc         int
		want       string
	}{
		{"the rows of other packages stand", "example.com/m/q.T Branchy 12.00\n" + others, 0,
			"crap: 3 functions scored; 1 at 10 or more, each in the baseline at its score or lower"},
		{"a function of q at 10 or more still needs its row", others, 1,
			"example.com/m/q.T Branchy scores 12.00"},
		{"a stale row of q still fails", "example.com/m/q.T Branchy 12.00\nexample.com/m/q Gone 15.00\n" + others, 1,
			"stale row: example.com/m/q Gone (15.00) is gone from the scored tree"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rc, out, errb := crap(t, root, branchyProfile(t), "-only", "q", "-baseline", writeFile(t, c.base))
			if rc != c.rc || !strings.Contains(out, c.want) || errb != "" {
				t.Errorf("exit %d, stdout %q, stderr %q; want %d and %q", rc, out, errb, c.rc, c.want)
			}
			if c.rc == 1 && strings.Contains(out, "example.com/m/x/y") {
				t.Errorf("a row of a package outside -only was held: %q", out)
			}
		})
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-only", "x/y"}, "example.com/m/x/y Y at x/y/y.go:3 has statements and no block"},
		{[]string{"-only", "empty"}, "-only empty holds no Go package"},
		{[]string{"-only", "nowhere"}, "no such file or directory"},
		{[]string{"-only", "q", "-require", "q"}, "takes neither -require nor -write-baseline"},
		{[]string{"-only", "q", "-write-baseline"}, "takes neither -require nor -write-baseline"},
	} {
		rc, _, errb := crap(t, root, branchyProfile(t), c.args...)
		if rc != 2 || !strings.HasPrefix(errb, "crap: ") || !strings.Contains(errb, c.want) {
			t.Errorf("%q: exit %d, stderr %q; want a refusal naming %q", c.args, rc, errb, c.want)
		}
	}
}
