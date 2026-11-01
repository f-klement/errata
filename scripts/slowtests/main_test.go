package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every slow test of the tree reaches make slow: the Makefile reads its
// SLOW_TESTS from this command, and the tree holds no call of slowOnly or
// SlowOnly that the list would miss, so a slow test the list leaves out fails
// here (#214). make slow fails on a name that runs no test.
func TestEverySlowTestIsInMakeSlow(t *testing.T) {
	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(makefile), "\nSLOW_TESTS = $(shell $(GO) run ./scripts/slowtests .)\n") {
		t.Error("the Makefile's SLOW_TESTS does not come from ./scripts/slowtests, so a slow test it lists could run nowhere")
	}
	found, err := slowTests("../..")
	if err != nil {
		t.Fatal(err)
	}
	var report, extract bool
	for _, f := range found {
		report = report || strings.HasPrefix(f, "./internal/report:")
		extract = extract || strings.HasPrefix(f, "./internal/extract/")
	}
	if !report || !extract {
		t.Errorf("found %v, want the slow tests of report and extract", found)
	}
}

// The list, sorted, from a tree of a test calling slowOnly in its body and in
// a subtest's, a test that does not, the slowOnly that calls SlowOnly, and a
// slow test in testdata, which go test does not build.
func TestSlowTestsLists(t *testing.T) {
	root := tree(t, map[string]string{
		"internal/b/b_test.go":          "package b\n\nfunc TestY(t *testing.T) { slowOnly(t) }\n\nfunc TestFast(t *testing.T) {}\n\nfunc slowOnly(t *testing.T) { extracttest.SlowOnly(t) }\n",
		"internal/a/x/x_test.go":        "package x\n\nfunc TestX(t *testing.T) {\n\tt.Run(\"big\", func(t *testing.T) { slowOnly(t) })\n}\n",
		"internal/a/testdata/t_test.go": "package t\n\nfunc TestHidden(t *testing.T) { slowOnly(t) }\n",
		"internal/a/a.go":               "package a\n\nfunc helper() { slowOnly(nil) }\n",
	})
	found, err := slowTests(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"./internal/a/x:TestX", "./internal/b:TestY"}; !slices.Equal(found, want) {
		t.Errorf("found %v, want %v", found, want)
	}
}

// The planted breaks: a helper that calls slowOnly for a test, a test that
// calls extracttest.SlowOnly itself, and a method named like a test. Each
// slow test would be missed, so the list is refused, naming the call.
func TestSlowTestsRefuses(t *testing.T) {
	for name, c := range map[string]struct{ src, want string }{
		"helper": {
			"package p\n\nfunc TestP(t *testing.T) { big(t) }\n\nfunc big(t *testing.T) { slowOnly(t) }\n",
			"p_test.go:5:26: big calls slowOnly, but only a test that calls it in its own body is listed",
		},
		"SlowOnly": {
			"package p\n\nfunc TestP(t *testing.T) { extracttest.SlowOnly(t) }\n",
			"p_test.go:3:28: TestP calls extracttest.SlowOnly, but only a call of the package's own slowOnly is listed",
		},
		"method": {
			"package p\n\nfunc (s suite) TestP(t *testing.T) { slowOnly(t) }\n",
			"p_test.go:3:38: TestP calls slowOnly, but only a test that calls it in its own body is listed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := slowTests(tree(t, map[string]string{"internal/p/p_test.go": c.src}))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error with %q", err, c.want)
			}
		})
	}
}

// tree writes the files under a new directory and returns it.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, src := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
