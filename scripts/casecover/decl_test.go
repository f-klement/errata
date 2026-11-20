package main

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// decl is p/p.go of the module declFS holds. TestTwice called Twice, which
// ran the add func literal of Ops and never the sub one, nor Hook.
const decl = `package p

import "time"

// Timeout is how long to wait.
const Timeout = 30 * time.Second

// The kinds.
const (
	A = iota * 2
	B
)

// Ops maps a name to its op; only add ever runs.
var Ops = map[string]func(int) int{
	"add": func(n int) int { return n + 1 },
	"sub": func(n int) int { return n - 1 },
}

// Twice doubles n's successor.
func Twice(n int) int { return Ops["add"](n) * 2 }

// Hook is a func literal alone, which no test ran.
var Hook = func() int { return A }
`

// declFS is a module of two packages. p holds decl, a file of a constant
// alone, which the profile does not name, one under a build constraint, one
// for another system and a test; q holds a constant and a function no test
// ran.
var declFS = fstest.MapFS{
	"p/p.go":              {Data: []byte(decl)},
	"p/limits.go":         {Data: []byte("package p\n\n// Max is the most.\nconst Max = 1 << 4\n")},
	"p/other.go":          {Data: []byte("//go:build !mutation\n\npackage p\n\n// Lim is a limit.\nconst Lim = 2 + 2\n")},
	"p/limits_windows.go": {Data: []byte("package p\n\n// Win is a limit.\nconst Win = 3 + 3\n")},
	"p/p_test.go":         {Data: []byte("package p\n\nimport \"testing\"\n\nvar fixture = 1 + 1\n\nfunc TestTwice(t *testing.T) {\n\tif Twice(fixture) != 6 {\n\t\tt.Fatal()\n\t}\n}\n")},
	"q/q.go":              {Data: []byte("package q\n\n// Q is a count.\nconst Q = 1 + 1\n\n// Half halves n.\nfunc Half(n int) int { return n / 2 }\n")},
}

// declProfile is what go test -tags mutation -cover -coverprofile printed for
// p with Go 1.27.2, with q's block, which go test leaves out of a profile of
// p's tests, added as a package no test ran. No block holds a const or a var.
const declProfile = `mode: set
example.com/p/p/p.go:16.27,16.41 1 1
example.com/p/p/p.go:17.27,17.41 1 0
example.com/p/p/p.go:21.25,21.51 1 1
example.com/p/p/p.go:24.25,24.35 1 0
example.com/p/q/q.go:7.24,7.38 1 0
`

// The mutants gremlins finds in declFS's values, by file and the position of
// their operator token.
var (
	timeoutMul = pos{6, 20}  // 30 * time.Second
	iotaMul    = pos{10, 11} // iota * 2
	addBody    = pos{16, 36} // n + 1, in the func literal that ran
	subBody    = pos{17, 36} // n - 1, in the func literal that never ran
	maxShift   = pos{4, 15}  // 1 << 4, in limits.go
)

func withDeclBlocks(t *testing.T) string {
	t.Helper()
	lines, err := addedBlocks([]byte(declProfile), "example.com/p", declFS)
	if err != nil {
		t.Fatal(err)
	}
	return declProfile + strings.Join(lines, "\n") + "\n"
}

// Before the helper, no mutant in a package level value reads as covered:
// the blind spot that held landing F's wire.go:51.
func TestTheProfileAloneLeavesPackageLevelValuesOut(t *testing.T) {
	for _, p := range []pos{timeoutMul, iotaMul, subBody} {
		if gremlinsCoversIn(t, declProfile, "example.com/p/p/p.go", p) {
			t.Errorf("the profile alone covers %v; the fixture no longer shows the blind spot", p)
		}
	}
	if gremlinsCoversIn(t, declProfile, "example.com/p/p/limits.go", maxShift) {
		t.Error("the profile alone covers limits.go")
	}
}

// In a package a test ran, the values of its consts and vars read as covered,
// in a file the profile names and in one it does not, which holds no
// function. The planted breaks, declBlocks returning nothing and
// unlistedBlocks returning nothing, each fail here.
func TestARunPackageCoversItsPackageLevelValues(t *testing.T) {
	got := withDeclBlocks(t)
	for _, p := range []pos{timeoutMul, iotaMul, addBody} {
		if !gremlinsCoversIn(t, got, "example.com/p/p/p.go", p) {
			t.Errorf("the mutant at p.go %v is not covered:\n%s", p, got)
		}
	}
	if !gremlinsCoversIn(t, got, "example.com/p/p/limits.go", maxShift) {
		t.Errorf("the mutant at limits.go %v is not covered:\n%s", maxShift, got)
	}
}

// A func literal in a var's value keeps the coverage go test gave its body:
// one no test ran stays not covered. The planted break, valueBlocks not
// cutting func literals out, fails here.
func TestAFuncLiteralInAValueKeepsItsOwnCoverage(t *testing.T) {
	got := withDeclBlocks(t)
	if gremlinsCoversIn(t, got, "example.com/p/p/p.go", subBody) {
		t.Errorf("the mutant at %v, in a func literal no test ran, is covered:\n%s", subBody, got)
	}
}

// Each value's block runs from its start to where go test would end it, cut
// around each func literal's body, braces and all, with no empty block after
// a value that ends on a func literal's brace; a file with a build
// constraint, one for another system, a test and a package no test ran get
// none. The planted breaks, appendBlock taking an empty span, constrained
// returning false, goSource taking a test file or a file for another system,
// and ranDirs taking a package whose blocks never ran, each fail here.
func TestTheDeclarationBlocksSpanTheValuesOfRunPackages(t *testing.T) {
	lines, err := addedBlocks([]byte(declProfile), "example.com/p", declFS)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"example.com/p/p/p.go:6.17,6.33 0 1",
		"example.com/p/p/p.go:10.6,10.14 0 1",
		"example.com/p/p/p.go:15.11,16.25 0 1",
		"example.com/p/p/p.go:16.41,17.25 0 1",
		"example.com/p/p/p.go:17.41,18.2 0 1",
		"example.com/p/p/p.go:24.12,24.23 0 1",
		"example.com/p/p/limits.go:4.13,4.19 0 1",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("addedBlocks gave\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// A file of a run package that the profile names and no block of which ran
// still gets its values' blocks: its package's initializers ran. The planted
// break, file skipping a name whose own blocks never ran, fails here.
func TestAFileOfARunPackageWhoseBlocksNeverRanCoversItsValues(t *testing.T) {
	profile := declProfile + "example.com/p/p/limits.go:9.1,9.9 1 0\n"
	lines, err := addedBlocks([]byte(profile), "example.com/p", declFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 || lines[0] != "example.com/p/p/limits.go:4.13,4.19 0 1" {
		t.Errorf("addedBlocks gave %q, want limits.go's block first, as a named file", lines)
	}
}

// withFiles is declFS with extra files added.
func withFiles(extra fstest.MapFS) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, f := range declFS {
		fsys[name] = f
	}
	for name, f := range extra {
		fsys[name] = f
	}
	return fsys
}

// A file in a run package's directory that the tests' build may not hold, or
// that is no Go source, gets no block and fails nothing: assembly, C and an
// object file, a Go file under an old // +build line, a cgo file, which
// go/build matches even with cgo off, and a file under a constraint whose body
// does not parse, which a build with other tags leaves out. The planted breaks, goSource
// taking any name its system matches, constrained ignoring // +build,
// unconstrainedBlocks reading a cgo file, and unconstrainedBlocks parsing the
// whole file before it reads the constraint, each fail here.
func TestFilesTheBuildMayLeaveOutGetNoBlock(t *testing.T) {
	fsys := withFiles(fstest.MapFS{
		"p/asm.s":     {Data: []byte("TEXT ·f(SB),0,$0\n\tRET\n")},
		"p/glue.c":    {Data: []byte("int f(void) { return 1 + 1; }\n")},
		"p/glue.h":    {Data: []byte("int f(void);\n")},
		"p/blob.syso": {Data: []byte("\x7fELF")},
		"p/old.go":    {Data: []byte("// +build !mutation\n\npackage p\n\n// Old is a limit.\nconst Old = 5 + 5\n")},
		"p/cgo.go":    {Data: []byte("package p\n\nimport \"C\"\n\n// Cg is a limit.\nconst Cg = 4 + 4\n")},
		"p/wip.go":    {Data: []byte("//go:build !never\n\npackage p\n\nconst = 1\n")},
	})
	got, err := addedBlocks([]byte(declProfile), "example.com/p", fsys)
	want, _ := addedBlocks([]byte(declProfile), "example.com/p", declFS)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("addedBlocks gave %q, %v; want declFS's blocks alone and no error", got, err)
	}
}

// A profile name in a directory the root does not have, as a //line
// directive naming a file elsewhere gives, still lets its package count as
// run, and the directory read then finds nothing rather than failing the run.
// The planted break, unlistedBlocks returning the missing directory's error,
// fails here.
func TestARunNameInAMissingDirectoryIsSkipped(t *testing.T) {
	profile := declProfile + "example.com/p/gone/x.go:3.2,3.9 1 1\n"
	got, err := addedBlocks([]byte(profile), "example.com/p", declFS)
	want, _ := addedBlocks([]byte(declProfile), "example.com/p", declFS)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("addedBlocks gave %q, %v; want declFS's blocks alone and no error", got, err)
	}
}
