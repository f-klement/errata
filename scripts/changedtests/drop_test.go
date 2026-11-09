package main

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
)

const dropSrc = `package p

import (
	"fmt"
	"testing"
)

import "strings"

// TestNew calls what the base lacks.
func TestNew(t *testing.T) {
	_ = New()
}

func TestKept(t *testing.T) {}

var (
	table = []int{New()}
	other = 1
)

func (s) helper() { _ = Gone }
`

// Each declaration holding an error goes with its doc comment: a test, a
// var block with both its names, a method; of the imports, the one spec in a
// group and the whole single import. What no error names stays, and go's
// paths, from the root with or without ./, name the file.
func TestDropRemovesEachDeclarationHoldingAnError(t *testing.T) {
	t.Chdir(t.TempDir())
	write(t, map[string]string{"internal/p/p_test.go": dropSrc})
	errs := "# example.com/m/internal/p [example.com/m/internal/p.test]\n" +
		"internal/p/p_test.go:12:6: undefined: New\n" +
		"./internal/p/p_test.go:12:7: undefined: New\n" +
		"internal/p/p_test.go:18:16: undefined: New\n" +
		"internal/p/p_test.go:22:25: undefined: Gone\n" +
		"internal/p/p_test.go:4:2: \"fmt\" imported and not used\n" +
		"internal/p/p_test.go:8:8: \"strings\" imported and not used\n"
	names, err := drop("./internal/p", strings.NewReader(errs))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"TestNew", "table", "other", ".s.helper"}; !slices.Equal(names, want) {
		t.Errorf("dropped %q, want %q", names, want)
	}
	got, err := os.ReadFile("internal/p/p_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if want := "package p\n\nimport (\n\t\n\t\"testing\"\n)\n\n\n\n\n\nfunc TestKept(t *testing.T) {}\n\n\n\n\n"; string(got) != want {
		t.Errorf("left\n%s\nwant\n%s", got, want)
	}
}

// An error drop cannot clear by dropping a test file's declaration makes it
// drop nothing and say why: one in a file that is not a test, in another
// directory, in the package clause, a line that is no error, or no error at
// all. The files stay as they were.
func TestDropRefuses(t *testing.T) {
	for name, c := range map[string]struct{ errs, want string }{
		"code":        {"internal/p/p.go:3:1: undefined: X\n", "internal/p/p.go:3:1: undefined: X is no error in a _test.go file of ./internal/p"},
		"other dir":   {"internal/q/q_test.go:3:1: undefined: X\n", "is no error in a _test.go file"},
		"package":     {"internal/p/p_test.go:1:1: wrong package\n", "internal/p/p_test.go:1:1 is in no declaration"},
		"not a place": {"internal/p/p_test.go:9:1: x\ngo: module lookup disabled\n", "go: module lookup disabled is no error"},
		"no error":    {"# example.com/m/internal/p\n", "no error in the build's output"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			write(t, map[string]string{"internal/p/p_test.go": dropSrc})
			var stdout, stderr bytes.Buffer
			code := dropRun("./internal/p", strings.NewReader(c.errs), &stdout, &stderr)
			if code != 3 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "changedtests: cannot drop: ") || !strings.Contains(stderr.String(), c.want) {
				t.Errorf("got %d, %q, %q; want 3 and %q", code, stdout.String(), stderr.String(), c.want)
			}
			if got, _ := os.ReadFile("internal/p/p_test.go"); string(got) != dropSrc {
				t.Errorf("the file changed:\n%s", got)
			}
		})
	}
}

// In one round drop also drops what names a dropped name, across the files
// of the directory: a chain of helpers to its test, every name of a var block
// one of whose names holds the error and their users, and a method's receiver
// type with the type's other methods and the tests that name it. What names
// none of them stays, and so does a file with nothing to drop.
func TestDropSpreadsToWhatNamesADroppedName(t *testing.T) {
	t.Chdir(t.TempDir())
	files := map[string]string{
		"a_test.go": "package p\n\nfunc h2() int { return New() }\n\nfunc h1() int { return h2() }\n\nvar (\n\tx = New()\n\ty = 1\n)\n\ntype fake struct{}\n\nfunc (fake) Run() int { return Gone() }\n\nfunc (fake) Name() string { return \"f\" }\n",
		"b_test.go": "package p\n\nfunc TestChain(t *testing.T) { _ = h1() }\n\nfunc TestY(t *testing.T) { _ = y }\n\nfunc TestFake(t *testing.T) { _ = fake{} }\n\nfunc TestKept(t *testing.T) { _ = t.Name() }\n",
		"c_test.go": "package p\n\nfunc TestAlone(t *testing.T) {}\n",
	}
	write(t, files)
	errs := "./a_test.go:3:24: undefined: New\n./a_test.go:8:6: undefined: New\n./a_test.go:14:31: undefined: Gone\n"
	names, err := drop(".", strings.NewReader(errs))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"h2", "h1", "x", "y", "fake", ".fake.Run", ".fake.Name", "TestChain", "TestY", "TestFake"}; !slices.Equal(names, want) {
		t.Errorf("dropped %q, want %q", names, want)
	}
	if got, _ := os.ReadFile("b_test.go"); string(got) != "package p\n\n\n\n\n\n\n\nfunc TestKept(t *testing.T) { _ = t.Name() }\n" {
		t.Errorf("b_test.go left\n%s", got)
	}
	if got, _ := os.ReadFile("c_test.go"); string(got) != files["c_test.go"] {
		t.Errorf("c_test.go changed:\n%s", got)
	}
}

// Dropping TestMain or init, directly or because it names a dropped name,
// would run the tests at the base without their setup: drop refuses, and the
// package is left out whole.
func TestDropRefusesTheSetup(t *testing.T) {
	for name, c := range map[string]struct{ src, errs, want string }{
		"TestMain":    {"package p\n\nfunc TestMain(m *testing.M) { New(); m.Run() }\n", "p_test.go:3:31: undefined: New\n", "it would drop TestMain"},
		"init":        {"package p\n\nfunc init() { New() }\n", "p_test.go:3:15: undefined: New\n", "it would drop init"},
		"through one": {"package p\n\nvar v = New()\n\nfunc init() { _ = v }\n", "p_test.go:3:9: undefined: New\n", "it would drop init"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			write(t, map[string]string{"p_test.go": c.src})
			var stdout, stderr bytes.Buffer
			if code := dropRun(".", strings.NewReader(c.errs), &stdout, &stderr); code != 3 || !strings.Contains(stderr.String(), c.want) {
				t.Errorf("got %d, %q, %q; want 3 and %q", code, stdout.String(), stderr.String(), c.want)
			}
			if got, _ := os.ReadFile("p_test.go"); string(got) != c.src {
				t.Errorf("the file changed:\n%s", got)
			}
		})
	}
}

func TestDropRun(t *testing.T) {
	t.Chdir(t.TempDir())
	write(t, map[string]string{"p_test.go": "package p\n\nfunc TestNew(t *testing.T) { New() }\n"})
	var stdout, stderr bytes.Buffer
	if code := dropRun(".", strings.NewReader("./p_test.go:3:31: undefined: New\n"), &stdout, &stderr); code != 0 || stdout.String() != "TestNew\n" {
		t.Errorf("got %d, %q, %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := dropRun(".", strings.NewReader("./x_test.go:3:31: undefined: New\n"), &stdout, &stderr); code != 3 || !strings.Contains(stderr.String(), "cannot drop: x_test.go is no _test.go file of .") {
		t.Errorf("a file go names that is not there: got %d, %q, %q", code, stdout.String(), stderr.String())
	}
}

// An error with no column, or a column past its line, lies in the line's
// declaration; one past the file's end lies in none.
func TestDropReadsAnyPlaceOnALine(t *testing.T) {
	t.Chdir(t.TempDir())
	src := "package p\n\nfunc TestNew(t *testing.T) { New() }\n"
	for _, errs := range []string{"p_test.go:3: x\n", "p_test.go:3:999: x\n"} {
		write(t, map[string]string{"p_test.go": src})
		if names, err := drop(".", strings.NewReader(errs)); err != nil || !slices.Equal(names, []string{"TestNew"}) {
			t.Errorf("%q: got %q, %v", errs, names, err)
		}
	}
	write(t, map[string]string{"p_test.go": src})
	if _, err := drop(".", strings.NewReader("p_test.go:99:1: x\n")); err == nil || !strings.Contains(err.Error(), "p_test.go:99:1 is in no declaration") {
		t.Errorf("a line past the end: got %v", err)
	}
}
