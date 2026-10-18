package heapcap

import (
	"errors"
	"fmt"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ticks returns a channel that delivers n ticks and then closes.
func ticks(n int) <-chan time.Time {
	c := make(chan time.Time, n)
	for range n {
		c <- time.Time{}
	}
	close(c)
	return c
}

// readings returns a reader that returns each of heaps in turn, and counts
// the reads in *n.
func readings(n *int, heaps ...uint64) func() uint64 {
	return func() uint64 {
		h := heaps[*n]
		*n++
		return h
	}
}

// A reading at the limit is under it; the first one above it ends the watch,
// and no later reading is taken.
func TestWatchReturnsTheFirstReadingPastTheLimit(t *testing.T) {
	var n int
	got := watch(100, ticks(5), readings(&n, 10, 100, 101, 500, 1))
	if got != 101 || n != 3 {
		t.Errorf("watch returned %d after %d readings, want 101 after 3", got, n)
	}
}

func TestWatchReturnsZeroWhenTheTicksEnd(t *testing.T) {
	var n int
	if got := watch(100, ticks(3), readings(&n, 10, 100, 50)); got != 0 || n != 3 {
		t.Errorf("watch returned %d after %d readings, want 0 after 3", got, n)
	}
}

// The reader reads the process's own heap: a retained allocation shows in it.
// The collection first sweeps what earlier tests left, which a collection
// between the two readings would otherwise subtract; the margin of 1 MiB is
// for the stacks, which a collection may shrink.
func TestReaderSeesAnAllocation(t *testing.T) {
	read := reader()
	runtime.GC()
	before := read()
	hold = append(hold, make([]byte, 8<<20))
	defer func() { hold = nil }()
	if after := read(); after < before+7<<20 {
		t.Errorf("the heap read %d bytes before an 8 MiB allocation and %d after", before, after)
	}
}

// hold keeps the helper's allocations reachable, so a collection cannot
// free them before the watchdog reads the heap.
var hold [][]byte

// TestHeapcapHelper runs only as the child of the tests below. It arms the
// watchdog and then grows what its environment names: heap:N fills the heap
// with N MiB, goroutines blocks 2000 goroutines on 32 KiB stacks each, and
// stack recurses without end. Then it waits for 50 readings.
func TestHeapcapHelper(t *testing.T) {
	what := os.Getenv("ERRATA_HEAPCAP_HELPER")
	switch mib, isHeap := strings.CutPrefix(what, "heap:"); {
	case isHeap:
		n, err := strconv.Atoi(mib)
		if err != nil {
			t.Fatal(err)
		}
		arm(32 << 20)
		for range n {
			hold = append(hold, make([]byte, 1<<20))
		}
	case what == "goroutines":
		arm(32 << 20)
		block := make(chan struct{})
		defer close(block)
		for range 2000 {
			go func() { _ = deepWait(block) }()
		}
	case what == "stack":
		arm(Cap)
		recurse(0)
	default:
		t.Skip("runs as the child of TestAHeapPastTheCapExits and the tests after it")
	}
	if armed != 32<<20 {
		t.Fatalf("arm(32 MiB) recorded a limit of %d bytes", armed)
	}
	time.Sleep(50 * poll)
	t.Logf("%s ran for 50 readings", what)
}

// deepWait blocks on block with 16 KiB of its own on the stack, so each
// goroutine holds a stack of 32 KiB that the heap does not count.
//
//go:noinline
func deepWait(block chan struct{}) byte {
	var pad [16 << 10]byte
	pad[len(pad)-1] = 1
	<-block
	return pad[len(pad)-1]
}

// recurse calls itself without end, 512 bytes of stack a call.
//
//go:noinline
func recurse(n int) int {
	var pad [512]byte
	pad[n%len(pad)] = byte(n)
	return recurse(n+1) + int(pad[(n+1)%len(pad)]) //nolint:staticcheck // SA5007: the stack test needs a recursion without end
}

// helper runs TestHeapcapHelper in a child process growing what.
func helper(t *testing.T, what string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHeapcapHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "ERRATA_HEAPCAP_HELPER="+what)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// exitedWith reports whether err is an exit with code.
func exitedWith(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}

// The watchdog itself, in a child process: 64 MiB against a 32 MiB cap exits
// with the watchdog's code and its line, and 16 MiB runs to the end.
func TestAHeapPastTheCapExits(t *testing.T) {
	out, err := helper(t, "heap:64")
	if !exitedWith(err, exitCode) || !strings.Contains(out, "heapcap: the Go heap and stacks hold ") ||
		!strings.Contains(out, "past the mutation run's cap of 32 MiB") {
		t.Errorf("64 MiB against a 32 MiB cap: %v, want exit code %d and the watchdog's line\n%s", err, exitCode, out)
	}
	out, err = helper(t, "heap:16")
	if err != nil || strings.Contains(out, "heapcap:") || !strings.Contains(out, "--- PASS: TestHeapcapHelper") {
		t.Errorf("16 MiB against a 32 MiB cap: %v, want a pass and no watchdog line\n%s", err, out)
	}
}

// Blocked goroutines grow stacks and hardly any heap, and the cap counts the
// stacks: 2000 goroutines of 32 KiB, about 64 MiB, pass a 32 MiB cap. Without
// the stacks the leak passed at 1050 MiB.
func TestBlockedGoroutinesPastTheCapExit(t *testing.T) {
	out, err := helper(t, "goroutines")
	if !exitedWith(err, exitCode) || !strings.Contains(out, "past the mutation run's cap of 32 MiB") {
		t.Errorf("2000 goroutines of 32 KiB against a 32 MiB cap: %v, want exit code %d and the watchdog's line\n%s", err, exitCode, out)
	}
}

// Recursion without end overflows the stack at maxStack, 64 MiB, where the
// runtime's own limit of 1 GB let one binary grow to 1031 MiB. The overflow is
// the runtime's fatal error, exit 2, which go test reports as a failure.
func TestRecursionStopsAtMaxStack(t *testing.T) {
	out, err := helper(t, "stack")
	if !exitedWith(err, 2) || !strings.Contains(out, "goroutine stack exceeds 67108864-byte limit") {
		t.Errorf("recursion without end: %v, want exit code 2 and a stack overflow at 64 MiB\n%s", err, out)
	}
}

// The comment at Cap gives the measurements the two limits rest on, so a
// change to either goes with new figures there.
func TestTheLimitsAreTheMeasuredOnes(t *testing.T) {
	if Cap != 256<<20 || maxStack != 64<<20 {
		t.Errorf("Cap is %d MiB and maxStack %d MiB; the figures at Cap and maxStack are for 256 and 64: measure again and change them with the limits", Cap>>20, maxStack>>20)
	}
}

func TestMessageNamesTheHeapAndTheCap(t *testing.T) {
	want := "heapcap: the Go heap and stacks hold 1030 MiB, past the mutation run's cap of 1024 MiB: " +
		"a test allocates without limit, so the binary exits (internal/heapcap)"
	if got := message(1030<<20+5, 1<<30); got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

const importPath = "github.com/f-klement/errata/internal/heapcap"

// Every package with tests links the watchdog through a test file built only
// with the mutation tag, and nothing else imports it: a package left out has
// no cap when a mutant makes it allocate without limit, and an import
// without the tag would carry the watchdog into make test or the release.
func TestEveryTestedPackageLinksTheWatchdog(t *testing.T) {
	linked, problems, err := links("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	if len(linked) < 9 {
		t.Errorf("found the watchdog linked in %v, want each of errata's nine tested packages", linked)
	}
}

// The planted breaks: a package with tests and no link; a link from a file
// built without the tag; a constraint that also holds without it (h); and
// links a linux build with the tag does not compile, for another GOOS (i, j)
// or in a file Go ignores (k). A directory of test data and another module
// are not packages of this one, and heapcap needs no link to itself.
func TestLinksRefuses(t *testing.T) {
	root := t.TempDir()
	imp := "\n\npackage p\n\nimport _ \"" + importPath + "\"\n"
	tagged := "//go:build mutation" + imp
	untagged := "package p\n\nimport _ \"" + importPath + "\"\n"
	plain := "package p\n"
	for name, src := range map[string]string{
		"go.mod":                           "module github.com/f-klement/errata\n",
		"a/a_test.go":                      plain,
		"a/mutation_test.go":               tagged,
		"b/b_test.go":                      plain,
		"c/c_test.go":                      untagged,
		"d/d.go":                           plain,
		"e/testdata/e_test.go":             plain,
		"f/go.mod":                         "module f\n",
		"f/f_test.go":                      plain,
		"g/g.go":                           untagged,
		"g/g_test.go":                      "//go:build !mutation" + imp,
		"g/mutation_test.go":               tagged,
		"h/h_test.go":                      plain,
		"h/mutation_test.go":               "//go:build mutation || race" + imp,
		"i/i_test.go":                      plain,
		"i/mutation_test.go":               "//go:build mutation && !linux" + imp,
		"j/j_test.go":                      plain,
		"j/mutation_windows_test.go":       tagged,
		"k/k_test.go":                      plain,
		"k/_mutation_test.go":              tagged,
		"internal/heapcap/heapcap_test.go": plain,
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	linked, problems, err := links(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "c", "g", "h"}; !slices.Equal(linked, want) {
		t.Errorf("linked %v, want %v", linked, want)
	}
	unlinked := func(dir string) string {
		return dir + " has tests, and no test file that a linux build with the mutation tag compiles imports internal/heapcap: " +
			"add " + dir + "/mutation_test.go holding `//go:build mutation` and `import _ \"" + importPath + "\"`"
	}
	untaggedImport := func(file, dir string) string {
		return file + " imports internal/heapcap and is not a test file that only a build with the mutation tag compiles, " +
			"so the watchdog would run outside the mutation run: import it from " + dir + "/mutation_test.go"
	}
	want := []string{
		unlinked("b"),
		untaggedImport("c/c_test.go", "c"),
		untaggedImport("g/g.go", "g"),
		untaggedImport("g/g_test.go", "g"),
		untaggedImport("h/mutation_test.go", "h"),
		unlinked("i"),
		unlinked("j"),
		unlinked("k"),
	}
	if !slices.Equal(problems, want) {
		t.Errorf("problems:\n%s\nwant:\n%s", strings.Join(problems, "\n"), strings.Join(want, "\n"))
	}
}

// links walks the module at root and returns the directories, relative to
// root, whose tests link the watchdog, and a line for each package with tests
// that does not and for each file that imports the watchdog without needing
// the mutation tag. A link counts when go/build, for linux with the tag,
// compiles it into the package's tests, as Gremlins does on CI. It skips
// directories Go skips (testdata, and names starting with . or _) and
// directories holding another module.
func links(root string) (linked, problems []string, err error) {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.BuildTags = "linux", "amd64", []string{"mutation"}
	// go/build enables cgo by default, but go test turns it off when no C
	// compiler is on PATH, so a link file that needs cgo can be compiled here
	// and left out of a run. Without cgo, such a file never counts as a link.
	ctx.CgoEnabled = false
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if path != root {
			if name := d.Name(); name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(rel)
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		tested := false
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
				continue
			}
			test := strings.HasSuffix(name, "_test.go")
			tested = tested || test
			imports, needsTag, err := importsWatchdog(filepath.Join(path, name))
			if err != nil {
				return err
			}
			if imports && (!test || !needsTag) {
				problems = append(problems, fmt.Sprintf("%s/%s imports internal/heapcap and is not a test file that only a build with the mutation tag compiles, "+
					"so the watchdog would run outside the mutation run: import it from %s/mutation_test.go", dir, name, dir))
			}
		}
		if !tested || dir == "internal/heapcap" {
			return nil
		}
		pkg, err := ctx.ImportDir(path, 0)
		if err != nil && !errors.As(err, new(*build.NoGoError)) {
			return err
		}
		if pkg != nil && (slices.Contains(pkg.TestImports, importPath) || slices.Contains(pkg.XTestImports, importPath)) {
			linked = append(linked, dir)
			return nil
		}
		problems = append(problems, fmt.Sprintf("%s has tests, and no test file that a linux build with the mutation tag compiles imports internal/heapcap: "+
			"add %s/mutation_test.go holding `//go:build mutation` and `import _ \"%s\"`", dir, dir, importPath))
		return nil
	})
	slices.Sort(linked)
	slices.Sort(problems)
	return linked, problems, err
}

// importsWatchdog reports whether the Go file at path imports heapcap, and
// whether its build constraint needs the mutation tag: it fails with every
// tag but mutation set.
func importsWatchdog(path string) (imports, needsTag bool, err error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return false, false, err
	}
	for _, imp := range f.Imports {
		imports = imports || imp.Path.Value == strconv.Quote(importPath)
	}
	for _, g := range f.Comments {
		if g.Pos() > f.Package {
			break
		}
		for _, c := range g.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				return false, false, fmt.Errorf("%s: %w", path, err)
			}
			needsTag = !expr.Eval(func(tag string) bool { return tag != "mutation" })
		}
	}
	return imports, needsTag, nil
}
