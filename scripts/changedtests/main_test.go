package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The base commit of every case: a test in a file of each kind the change
// touches, and a helper.
var base = map[string]string{
	"a_test.go":               "package m\n\nfunc TestRoot(t *testing.T) {}\n",
	"internal/p/p_test.go":    "package p\n\n// TestKept is kept.\nfunc TestKept(t *testing.T) { t.Log(1) }\n\nfunc TestEdited(t *testing.T) { t.Log(1) }\n\nfunc TestMoved(t *testing.T) {}\n\nfunc TestMain(m *testing.M) {}\n\nfunc helper() {}\n",
	"internal/p/gone_test.go": "package p\n\nfunc TestGone(t *testing.T) {}\n",
	"internal/p/p.go":         "package p\n",
}

// The change adds and edits tests, one in an external test package, moves
// one, rewrites a doc comment, edits a helper no test names and a method named
// like a test, deletes a file, and touches testdata and code: only the added
// and the edited tests are listed, each with its directory.
func TestChangedListsAddedAndEditedTests(t *testing.T) {
	repo(t, map[string]string{
		"a_test.go":                     "package m\n\nfunc TestRoot(t *testing.T) { t.Log(2) }\n",
		"internal/p/p_test.go":          "package p\n\n// TestKept is still kept.\nfunc TestKept(t *testing.T) { t.Log(1) }\n\nfunc TestEdited(t *testing.T) { t.Log(2) }\n\nfunc TestMain(m *testing.M) {}\n\nfunc helper() { println() }\n\nfunc TestAdded(t *testing.T) {}\n\nfunc FuzzAdded(f *testing.F) {}\n\nfunc ExampleAdded() {}\n\nfunc Testable(t *testing.T) {}\n\nfunc (s) TestMethod(t *testing.T) {}\n",
		"internal/p/q_test.go":          "package p\n\nfunc TestMoved(t *testing.T) {}\n",
		"internal/p/ext_test.go":        "package p_test\n\nfunc TestExternal(t *testing.T) {}\n",
		"internal/p/gone_test.go":       "",
		"internal/p/p.go":               "package p\n\nvar X = 1\n",
		"internal/p/testdata/t_test.go": "package t\n\nfunc TestHidden(t *testing.T) {}\n",
	})
	got, err := changed("base")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{". TestRoot edited", "./internal/p ExampleAdded edited", "./internal/p FuzzAdded edited", "./internal/p TestAdded edited", "./internal/p TestEdited edited", "./internal/p TestExternal edited"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A test declared in two files of different build constraints is edited when
// either text changes, and not when the two only trade places.
func TestChangedComparesEveryDeclarationOfAName(t *testing.T) {
	for name, c := range map[string]struct {
		x, y string
		want []string
	}{
		"one edited": {"{ t.Log(1) }", "{ t.Log(3) }", []string{"./internal/p TestTwice edited"}},
		"swapped":    {"{ t.Log(2) }", "{ t.Log(1) }", nil},
	} {
		t.Run(name, func(t *testing.T) {
			decl := func(tag, body string) string {
				return "//go:build " + tag + "\n\npackage p\n\nfunc TestTwice(t *testing.T) " + body + "\n"
			}
			repo(t, map[string]string{
				"internal/p/x_test.go": decl("linux", "{ t.Log(1) }"),
				"internal/p/y_test.go": decl("!linux", "{ t.Log(2) }"),
			}, map[string]string{
				"internal/p/x_test.go": decl("linux", c.x),
				"internal/p/y_test.go": decl("!linux", c.y),
			})
			got, err := changed("base")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// A base git cannot read, and a test file that does not parse, end the run
// with git's or the parser's message: an empty list would read as a change
// that adds no test.
func TestChangedFails(t *testing.T) {
	repo(t, map[string]string{"x_test.go": "package m\n\nfunc TestX(t *testing.T) {\n"})
	if _, err := changed("base"); err == nil || !strings.Contains(err.Error(), "x_test.go:3:28: expected '}'") {
		t.Errorf("got %v, want the parser's error", err)
	}
	if _, err := changed("nosuch"); err == nil || !strings.Contains(err.Error(), "git diff --name-status --no-renames -z nosuch HEAD") {
		t.Errorf("got %v, want git's error", err)
	}
}

// A test that names a changed table, var or method of a _test.go file, in its
// own body or through a helper of an unchanged file, or a changed var of the
// package's export_test.go from its external test package, is listed; a test
// of the same package that names none of them is not, and neither is the
// changed method named like a test.
func TestChangedListsTestsReachingAChangedDeclaration(t *testing.T) {
	repo(t, map[string]string{
		"internal/p/a_test.go":      "package p\n\nvar cases = []int{1}\n\ntype s struct{}\n\nfunc (s) run() {}\n\nfunc (s) TestMethod(t *testing.T) {}\n\nfunc TestTable(t *testing.T) { for range cases {} }\n\nfunc TestCalls(t *testing.T) { s{}.run() }\n\nfunc TestOther(t *testing.T) {}\n",
		"internal/p/b_test.go":      "package p\n\nfunc each(t *testing.T) { for range cases {} }\n\nfunc TestThrough(t *testing.T) { each(t) }\n",
		"internal/p/export_test.go": "package p\n\nvar Exported = 1\n",
		"internal/p/x_test.go":      "package p_test\n\nfunc TestOutside(t *testing.T) { _ = p.Exported }\n",
		"internal/q/q_test.go":      "package q\n\nvar cases = 1\n\nfunc TestElsewhere(t *testing.T) { _ = cases }\n",
	}, map[string]string{
		"internal/p/a_test.go":      "package p\n\nvar cases = []int{1, 2}\n\ntype s struct{}\n\nfunc (s) run() { println() }\n\nfunc (s) TestMethod(t *testing.T) { println() }\n\nfunc TestTable(t *testing.T) { for range cases {} }\n\nfunc TestCalls(t *testing.T) { s{}.run() }\n\nfunc TestOther(t *testing.T) {}\n\nfunc TestEditedToo(t *testing.T) { _ = cases }\n",
		"internal/p/export_test.go": "package p\n\nvar Exported = 2\n",
	})
	got, err := changed("base")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./internal/p TestCalls reaches", "./internal/p TestEditedToo edited", "./internal/p TestOutside reaches", "./internal/p TestTable reaches", "./internal/p TestThrough reaches"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A changed type reaches the tests that name it, and through its methods
// nothing else: a test that calls a method of the same name on something
// else, or writes a field key of that name, is not listed.
func TestChangedReachesAMethodThroughItsType(t *testing.T) {
	repo(t, map[string]string{
		"internal/p/f_test.go": "package p\n\ntype fake struct{}\n\nfunc (fake) Name() string { return \"f\" }\n\ntype row struct{ Name string }\n\nfunc TestUsesFake(t *testing.T) { _ = fake{} }\n\nfunc TestUnrelated(t *testing.T) { _ = t.Name() }\n\nfunc TestField(t *testing.T) { _ = row{Name: \"x\"} }\n",
	}, map[string]string{
		"internal/p/f_test.go": "package p\n\ntype fake struct{ n int }\n\nfunc (fake) Name() string { return \"f\" }\n\ntype row struct{ Name string }\n\nfunc TestUsesFake(t *testing.T) { _ = fake{} }\n\nfunc TestUnrelated(t *testing.T) { _ = t.Name() }\n\nfunc TestField(t *testing.T) { _ = row{Name: \"x\"} }\n",
	})
	got, err := changed("base")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"./internal/p TestUsesFake reaches"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A changed TestMain runs around every test of its package's binary, so each
// is listed, and none of another package.
func TestChangedListsEveryTestOfAChangedTestMain(t *testing.T) {
	repo(t, map[string]string{
		"internal/p/m_test.go": "package p\n\nfunc TestMain(m *testing.M) {}\n\nfunc TestOne(t *testing.T) {}\n",
		"internal/p/o_test.go": "package p_test\n\nfunc TestTwo(t *testing.T) {}\n",
		"internal/q/q_test.go": "package q\n\nfunc TestThree(t *testing.T) {}\n",
	}, map[string]string{
		"internal/p/m_test.go": "package p\n\nfunc TestMain(m *testing.M) { m.Run() }\n\nfunc TestOne(t *testing.T) {}\n",
	})
	got, err := changed("base")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"./internal/p TestOne reaches", "./internal/p TestTwo reaches"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRun(t *testing.T) {
	repo(t, map[string]string{"x_test.go": "package m\n\nfunc TestX(t *testing.T) {}\n"})
	for name, c := range map[string]struct {
		args         []string
		code         int
		stdout, warn string
	}{
		"list":     {[]string{"list", "base"}, 0, ". TestX edited\n", ""},
		"no base":  {[]string{"list"}, 2, "", "usage: changedtests list <base> | changedtests drop <dir>\n"},
		"no verb":  {[]string{"base"}, 2, "", "usage: "},
		"other":    {[]string{"show", "base"}, 2, "", "usage: "},
		"bad base": {[]string{"list", "nosuch"}, 1, "", "changedtests: git diff"},
		"drop":     {[]string{"drop", "."}, 3, "", "changedtests: cannot drop: no error"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(c.args, strings.NewReader(""), &stdout, &stderr)
			if code != c.code || stdout.String() != c.stdout || !strings.HasPrefix(stderr.String(), c.warn) {
				t.Errorf("got %d, %q, %q; want %d, %q, %q", code, stdout.String(), stderr.String(), c.code, c.stdout, c.warn)
			}
		})
	}
}

func TestIsTest(t *testing.T) {
	for name, want := range map[string]bool{
		"Test": true, "TestX": true, "Test_x": true, "Test1": true, "Fuzz": true, "FuzzX": true,
		"Example": true, "ExampleX_y": true, "TestMain": false, "Testable": false, "Fuzzy": false,
		"Examples": false, "Benchmark": false, "test": false, "TestÄ": true, "Testä": false,
	} {
		if got := isTest(name); got != want {
			t.Errorf("isTest(%q) = %v, want %v", name, got, want)
		}
	}
}

// repo makes a git repository in a new directory and changes into it: a
// commit tagged base of the base files, overlaid by each later map's files
// in turn (the shared base when none is given before the change), then a
// commit of the change at HEAD. An empty file's text deletes it.
func repo(t *testing.T, changes ...map[string]string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitT(t, "init", "-q")
	steps := changes
	if len(changes) == 1 {
		steps = []map[string]string{base, changes[0]}
	}
	for i, files := range steps {
		write(t, files)
		gitT(t, "add", "-A")
		gitT(t, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "step")
		if i == 0 {
			gitT(t, "tag", "base")
		}
	}
}

func write(t *testing.T, files map[string]string) {
	t.Helper()
	for name, src := range files {
		if src == "" {
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func gitT(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
