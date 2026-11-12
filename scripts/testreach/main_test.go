package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseMutantTakesGremlinsShapeOnly(t *testing.T) {
	m, ok := parseMutant([]string{"test", "-tags", "mutation", "-timeout", "17.5s", "-failfast", "example.com/m/p"})
	if !ok || m.tags != "mutation" || m.timeout != 17500*time.Millisecond || m.pkg != "example.com/m/p" {
		t.Fatalf("gremlins' mutant run: got %+v %v", m, ok)
	}
	if m, ok := parseMutant([]string{"test", "-timeout", "12s", "-failfast", "example.com/m/p"}); !ok || m.tags != "" {
		t.Fatalf("without tags: got %+v %v", m, ok)
	}
	for _, args := range [][]string{
		{"test", "-tags", "mutation", "-cover", "-coverprofile", "c.out", "./internal/p/..."},
		{"test", "-tags", "mutation", "-timeout", "12s", "-failfast", "-run", "X", "example.com/m/p"},
		{"test", "-timeout", "12s", "example.com/m/p"},
		{"test", "-timeout", "1s", "-failfast", "example.com/m/p"},
		{"build", "./..."},
		{"test", "-timeout", "12s", "-failfast", "-v"},
	} {
		if _, ok := parseMutant(args); ok {
			t.Errorf("%q read as a mutant's run", args)
		}
	}
}

const orig = `package p

// Add adds.
func Add(a, b int) int {
	return a + b
}

func Less(a, b int) bool {
	if a < b { return true }
	return false
}
`

// mutate does what gremlins v0.6.0 does to a file: one token swapped, the
// file written back with go/printer.
func mutate(t *testing.T, src string, from, to token.Token, nth int) []byte {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	seen, found := 0, false
	ast.Inspect(f, func(n ast.Node) bool {
		if be, ok := n.(*ast.BinaryExpr); ok && be.Op == from {
			if seen == nth {
				be.Op = to
				found = true
			}
			seen++
		}
		return true
	})
	if !found {
		t.Fatalf("no %s number %d", from, nth)
	}
	var b bytes.Buffer
	if err := printer.Fprint(&b, fset, f); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestChangedTokenFindsTheMutatedLineThroughPrinterSpacing(t *testing.T) {
	b := mutate(t, orig, token.LSS, token.LEQ, 0)
	if bytes.Equal(b, []byte(orig)) {
		t.Fatal("the mutant equals the original")
	}
	line, err := changedToken([]byte(orig), b)
	if err != nil || line != 9 {
		t.Fatalf("got line %d, %v; want 9, the line of a < b in the original", line, err)
	}
	// go/printer splits the one-line if, so the lines after it move.
	if !bytes.Contains(b, []byte("if a <= b {\n")) {
		t.Fatalf("printer kept the if on one line; the test no longer covers moved lines:\n%s", b)
	}
	twice := bytes.Replace(b, []byte("a + b"), []byte("a - b"), 1)
	if _, err := changedToken([]byte(orig), twice); err == nil {
		t.Fatal("two tokens changed, and changedToken named a line")
	}
	if _, err := changedToken([]byte(orig), []byte(orig)); err == nil {
		t.Fatal("no token changed, and changedToken named a line")
	}
}

// world is a package directory and gremlins' working copy of it, with the
// mutant at Less's < (line 9), an index, the limits, and a fake go. The fake
// go logs its arguments; given test -c, it writes a fake test binary to -o,
// exiting $BUILD_RC; given another go test, gremlins' own, it exits $PLAIN_RC.
// The fake binary logs "bin" and its arguments and, given -test.run, exits
// $NARROW_RC, else sleeps $WHOLE_SLEEP and exits $WHOLE_RC; with $PID_FILE it
// writes its pid there and that of a sleep it starts to $PID_FILE.child, and
// waits for the sleep. With $LINGER it leaves a sleep of that many seconds
// behind, holding its output, and with $NARROW_LINGER the same on a narrowed
// run only. The index's own binary, pkg.test, logs "check" and exits $CHECK_RC.
//
// The commands run, but the time each step takes is the one in took, not the
// machine's: see charge.
type world struct {
	root, work, index, goBin, log string
	// took is the time charged to each step: build, check, narrow, whole,
	// and go for any other go command. A step it leaves out runs under
	// bounded's own clock and limit.
	took   map[string]time.Duration
	calls  *[]call    // each command run, in order
	waited *time.Time // the deadline testreach waited for after a timeout
}

// call is a command testreach ran: its step, and the limit and wait for its
// output testreach gave it.
type call struct {
	step          string
	limit, ioWait time.Duration
}

// charge replaces bounded for the test. A step that w.took names runs to its
// end without the limit, and is charged the time w.took gives it: as long as
// the limit or longer, it is a timeout, as bounded reports one. Its exit, its
// signal and the wait for its output are the command's own. A step w.took
// leaves out runs under bounded as it is. The deadline testreach waits for
// after a timeout is kept in w.waited, not waited for.
func charge(t *testing.T, w world) {
	t.Helper()
	oldRun, oldWait := runBounded, waitUntil
	t.Cleanup(func() { runBounded, waitUntil = oldRun, oldWait })
	runBounded = func(name string, args []string, dir string, limit, ioWait time.Duration) outcome {
		step := stepOf(name, args)
		*w.calls = append(*w.calls, call{step, limit, ioWait})
		took, ok := w.took[step]
		if !ok || limit <= 0 {
			return bounded(name, args, dir, limit, ioWait)
		}
		o := bounded(name, args, dir, time.Hour, ioWait)
		if took >= limit {
			return outcome{timedOut: true, took: limit}
		}
		o.took = took
		return o
	}
	waitUntil = func(deadline time.Time) { *w.waited = deadline }
}

// stepOf names the step a command is: the build and gremlins' own go test
// run go, the check runs the index's pkg.test, and the narrowed and whole
// runs run the mutant's binary, with -test.run or without.
func stepOf(name string, args []string) string {
	switch filepath.Base(name) {
	case "pkg.test":
		return "check"
	case "mutant.test":
		if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "-test.run=") }) {
			return "narrow"
		}
		return "whole"
	}
	if len(args) > 1 && args[1] == "-c" {
		return "build"
	}
	return "go"
}

// calledFor is the one command run for the step.
func calledFor(t *testing.T, w world, step string) call {
	t.Helper()
	var found []call
	for _, c := range *w.calls {
		if c.step == step {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d commands for the %s step, want 1: %+v", len(found), step, *w.calls)
	}
	return found[0]
}

const fakeBin = `#!/bin/sh
echo "bin $*" >>"$GO_LOG"
[ -z "${FAIL_OUT:-}" ] || printf '%s\n' "$FAIL_OUT"
if [ -n "${PID_FILE:-}" ]; then
    echo $$ >"$PID_FILE.tmp" && mv "$PID_FILE.tmp" "$PID_FILE"
    sleep 120 &
    echo $! >"$PID_FILE.child.tmp" && mv "$PID_FILE.child.tmp" "$PID_FILE.child"
    wait
    exit 0
fi
[ -z "${LINGER:-}" ] || sleep "$LINGER" &
case " $* " in *" -test.run="*)
    [ -z "${NARROW_LINGER:-}" ] || sleep "$NARROW_LINGER" &
    exit "${NARROW_RC:-0}" ;;
esac
sleep "${WHOLE_SLEEP:-0}"
exit "${WHOLE_RC:-0}"
`

func newWorld(t *testing.T) world {
	t.Helper()
	d := t.TempDir()
	w := world{root: filepath.Join(d, "root"), work: filepath.Join(d, "wd", "internal", "p"), index: filepath.Join(d, "reach"), log: filepath.Join(d, "go.log"),
		took: map[string]time.Duration{"build": 0, "check": 0, "narrow": 0, "whole": 0}, calls: new([]call), waited: new(time.Time)}
	for _, dir := range []string{filepath.Join(w.root, "internal", "p"), w.work, w.index} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(w.root, "internal", "p", "p.go"), orig)
	write(t, filepath.Join(w.root, "internal", "p", "other.go"), "package p\n")
	write(t, filepath.Join(w.work, "p.go"), string(mutate(t, orig, token.LSS, token.LEQ, 0)))
	write(t, filepath.Join(w.work, "other.go"), "package p\n")
	write(t, filepath.Join(w.index, "index"), "package example.com/m/internal/p\ndir internal/p\n"+
		"test TestLess\np.go 8 11\n"+
		"test TestAdd\np.go 4 6\n"+
		"test ExampleLess\np.go 8 9\n"+
		"test TestOther\n")
	write(t, filepath.Join(w.index, "limits"), "2 3\n")
	write(t, filepath.Join(w.index, "pkg.test"), "#!/bin/sh\necho \"check $*\" >>\"$GO_LOG\"\nexit \"${CHECK_RC:-0}\"\n")
	w.goBin = filepath.Join(d, "go")
	write(t, w.goBin, `#!/bin/sh
echo "go $*" >>"$GO_LOG"
if [ "$1 $2" = "test -c" ]; then
    out=""; prev=""
    for a; do [ "$prev" != -o ] || out=$a; prev=$a; done
    [ "${BUILD_RC:-0}" = 0 ] || exit "$BUILD_RC"
    cat >"$out" <<'BIN'
`+fakeBin+`BIN
    chmod +x "$out"
    exit 0
fi
[ "$1" = test ] || exit "${GO_RC:-0}"
exit "${PLAIN_RC:-0}"
`)
	for _, f := range []string{w.goBin, filepath.Join(w.index, "pkg.test")} {
		if err := os.Chmod(f, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GO_LOG", w.log)
	t.Setenv("TMPDIR", d)
	t.Chdir(w.work)
	charge(t, w)
	return w
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// logLines is the go log with the build's temporary path replaced by BIN.
func logLines(t *testing.T, w world) string {
	t.Helper()
	return regexp.MustCompile(`-o \S*/mutant\.test`).ReplaceAllString(read(t, w.log), "-o BIN")
}

var mutantRun = []string{"test", "-tags", "mutation", "-timeout", "8s", "-failfast", "example.com/m/internal/p"}

// With limits 2 3 and gremlins' -timeout of 8 s, gremlins gives 6 s: build and
// whole run get 4 s, the planned limit, and the narrowed run 1.5 s. Every
// binary gets -test.timeout=6s, the planned limit and 2 s, as gremlins' go
// test set it.
const (
	build      = "go test -c -tags mutation -o BIN example.com/m/internal/p\n"
	check      = "check -test.paniconexit0 -test.timeout=6s -test.failfast -test.count=1 -test.run=^(TestLess|ExampleLess)$\n"
	narrowRun  = "bin -test.paniconexit0 -test.timeout=6s -test.failfast -test.count=1 -test.run=^(TestLess|ExampleLess)$\n"
	wholeRun   = "bin -test.paniconexit0 -test.timeout=6s -test.failfast -test.count=1\n"
	reachingOf = "p.go:9 2/4 "
)

func TestRunNarrowsAMutantToTheTestsThatReachItsLine(t *testing.T) {
	w := newWorld(t)
	t.Setenv("NARROW_RC", "1")
	if rc := run(w.goBin, w.root, w.index, mutantRun); rc != 1 {
		t.Fatalf("a narrowed run that fails: exit %d, want 1, a kill", rc)
	}
	// The narrowed binary gets the whole run's -test.timeout, so t.Deadline is
	// the same narrowed or whole.
	if got, want := logLines(t, w), build+check+narrowRun; got != want {
		t.Fatalf("ran\n%s\nwant\n%s", got, want)
	}
	if got := read(t, filepath.Join(w.index, "log")); !strings.HasPrefix(got, reachingOf+"killed ") || !strings.Contains(got, " - - narrowed (set checked in ") {
		t.Fatalf("log: %q", got)
	}
}

func TestRunRerunsASurvivorOfTheNarrowedRunWhole(t *testing.T) {
	for _, tc := range []struct{ narrow, whole string }{{"0", "1"}, {"0", "0"}, {"2", "1"}, {"0", "2"}} {
		w := newWorld(t)
		t.Setenv("NARROW_RC", tc.narrow)
		t.Setenv("WHOLE_RC", tc.whole)
		want := 0
		if tc.whole != "0" {
			want = 1 // go test reports any failure of the binary as exit 1
		}
		if tc.narrow != "0" {
			want = 1
		}
		rc := run(w.goBin, w.root, w.index, mutantRun)
		if rc != want {
			t.Fatalf("narrowed exit %s, whole exit %s: exit %d, want %d", tc.narrow, tc.whole, rc, want)
		}
		if tc.narrow != "0" {
			continue
		}
		if got, wantLog := logLines(t, w), build+check+narrowRun+wholeRun; got != wantLog {
			t.Fatalf("ran\n%s\nwant\n%s", got, wantLog)
		}
		if got := read(t, filepath.Join(w.index, "log")); !strings.Contains(got, " narrowed, then whole (set checked in ") {
			t.Fatalf("log: %q", got)
		}
	}
}

// F1 of the review of #264, and E1 of its second delta review: build and whole
// tests are charged against the planned limit as one cold go test is. A 3 s
// build and 1.5 s of whole tests pass the planned 4 s, so the mutant is timed
// out, and that is final; after a narrowed run that overruns its share, a 2 s
// build and 1.5 s of whole tests fit, so it lives (the review's probe). The
// planted break: the whole run given the planned limit, its build not charged,
// and the slow build lives.
func TestRunJudgesBuildAndWholeTestsAgainstThePlannedLimit(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		build, narrow, whole time.Duration
		rc                   int
		log                  string
	}{
		{"a slow build", 3 * time.Second, 0, 1500 * time.Millisecond, 1, ` lived 0\.00 timeout 1\.00 narrowed, then whole \(set checked in 0\.00\) \(build 3\.00\); held 2\.00 past the planned limit$`},
		{"a narrowed run that overruns", 2 * time.Second, 5 * time.Second, 1500 * time.Millisecond, 0, ` timeout 1\.50 lived 1\.50 narrowed, then whole \(set checked in 0\.00\) \(build 2\.00\)$`},
	} {
		w := newWorld(t)
		w.took["build"], w.took["narrow"], w.took["whole"] = tc.build, tc.narrow, tc.whole
		if rc := run(w.goBin, w.root, w.index, mutantRun); rc != tc.rc {
			t.Fatalf("%s: exit %d, want %d", tc.name, rc, tc.rc)
		}
		if got, want := logLines(t, w), build+check+narrowRun+wholeRun; got != want {
			t.Fatalf("%s: ran\n%s\nwant\n%s", tc.name, got, want)
		}
		if l := read(t, filepath.Join(w.index, "log")); !regexp.MustCompile(tc.log).MatchString(strings.TrimSuffix(l, "\n")) {
			t.Fatalf("%s: log %q", tc.name, l)
		}
	}
}

// The narrowed run's own time is not charged to the whole run: a build of 1 s,
// narrowed tests of 1 s and whole tests of 2.5 s live, since build and whole
// tests take 3.5 s of the 4. The planted break: the narrowed run's time taken
// from the whole run's limit too, which leaves it 2 s.
func TestRunDoesNotChargeTheNarrowedTestsToTheWholeRun(t *testing.T) {
	w := newWorld(t)
	w.took["build"], w.took["narrow"], w.took["whole"] = time.Second, time.Second, 2500*time.Millisecond
	if rc := run(w.goBin, w.root, w.index, mutantRun); rc != 0 {
		t.Fatalf("exit %d, want 0: build and whole tests fit the planned limit", rc)
	}
	if got := calledFor(t, w, "whole").limit; got != 3*time.Second {
		t.Fatalf("the whole run's limit: %s, want the 3 s the build left", got)
	}
}

// The other time verdicts of a run, each pinned by its whole log line: the
// check's time taken from the narrowed share, which leaves 1 s; a build that
// takes the planned limit, which is a timeout held until gremlins' limit; a
// check that takes the whole share, which leaves the mutant to the whole run.
// The planted breaks: the share left as it was before the check; the build's
// timeout not read.
func TestRunPinsTheTimeVerdictsOfTheBuildAndTheCheck(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		build, check, narrow time.Duration
		rc                   int
		ran, log             string
		waited               bool
	}{
		{"a check that takes part of the share", 0, 500 * time.Millisecond, 5 * time.Second, 0, build + check + narrowRun + wholeRun,
			reachingOf + "timeout 1.00 lived 0.00 narrowed, then whole (set checked in 0.50) (build 0.00)\n", false},
		{"a build that takes the planned limit", 4 * time.Second, 0, 0, 1, build,
			reachingOf + "- - timeout 4.00 the build took the whole limit; held 2.00 past the planned limit\n", true},
		{"a check that takes the whole share", 0, 1500 * time.Millisecond, 0, 0, build + check + wholeRun,
			reachingOf + "- - lived 0.00 no time to run the reaching tests together on the original code (build 0.00)\n", false},
	} {
		w := newWorld(t)
		w.took["build"], w.took["check"], w.took["narrow"] = tc.build, tc.check, tc.narrow
		if rc := run(w.goBin, w.root, w.index, mutantRun); rc != tc.rc {
			t.Fatalf("%s: exit %d, want %d", tc.name, rc, tc.rc)
		}
		if got := logLines(t, w); got != tc.ran {
			t.Fatalf("%s: ran\n%s\nwant\n%s", tc.name, got, tc.ran)
		}
		if got := read(t, filepath.Join(w.index, "log")); got != tc.log {
			t.Fatalf("%s: log\n%q\nwant\n%q", tc.name, got, tc.log)
		}
		if got := !w.waited.IsZero(); got != tc.waited {
			t.Fatalf("%s: waited for gremlins' limit: %v, want %v", tc.name, got, tc.waited)
		}
	}
}

// Delta review D3: go test waits for a test binary's output for the larger of
// 5 s and a tenth of -timeout after it exits, then fails the test, "Test I/O
// incomplete". A binary that passes and leaves a process holding its output,
// under a planned limit of 20 s, is then a kill after 5 s. The holder sleeps
// 60 s, so it still holds the output when the wait ends however late the
// binary exits on a busy machine; the kill of the group ends it then.
func TestRunFailsOutputStillHeldAsGoTestDoes(t *testing.T) {
	w := newWorld(t)
	t.Setenv("CHECK_RC", "1") // no narrowed run: the whole run alone
	t.Setenv("LINGER", "60")
	// 30 s from gremlins: 20 s planned, the binaries' -test.timeout 22 s.
	args := []string{"test", "-tags", "mutation", "-timeout", "32s", "-failfast", "example.com/m/internal/p"}
	start := time.Now()
	rc := run(w.goBin, w.root, w.index, args)
	took := time.Since(start)
	if rc != 1 {
		t.Fatalf("exit %d, want the 1 of a failed test", rc)
	}
	if got := calledFor(t, w, "whole").ioWait; got != 5*time.Second {
		t.Fatalf("the wait for output: %s, want go test's 5 s", got)
	}
	// Only a lower bound: a busy machine makes the run longer, never shorter.
	if took < 4900*time.Millisecond {
		t.Fatalf("took %s, want at least go test's wait of 5 s", took)
	}
	if got := read(t, filepath.Join(w.index, "log")); !strings.Contains(got, " - - iowait ") {
		t.Fatalf("log %q", got)
	}
}

// Delta review 3 of #264, G1, the reviewer's probe: a narrowed run whose
// binary passes and leaves a process holding its output ends on go test's
// wait of 5 s. That is no test failure: the whole run, whose other tests run
// 8 s, passes, so the mutant lives. The holder sleeps 60 s, past the wait
// however busy the machine; the planted break: a narrowed run ended by any
// failure, outcome.failed in place of testFailed, and the mutant is killed.
func TestRunSendsANarrowedIOWaitToTheWholeRun(t *testing.T) {
	w := newWorld(t)
	t.Setenv("NARROW_LINGER", "60")
	w.took["narrow"], w.took["whole"] = 5*time.Second, 8*time.Second
	args := []string{"test", "-tags", "mutation", "-timeout", "32s", "-failfast", "example.com/m/internal/p"}
	if rc := run(w.goBin, w.root, w.index, args); rc != 0 {
		t.Fatalf("exit %d, want the 0 of the whole run", rc)
	}
	if got := read(t, filepath.Join(w.index, "log")); !regexp.MustCompile(` iowait \S+ lived \S+ narrowed, then whole`).MatchString(got) {
		t.Fatalf("log %q", got)
	}
}

func TestRunGivesABuildFailureGoTestsStatus(t *testing.T) {
	w := newWorld(t)
	t.Setenv("BUILD_RC", "1")
	if rc := run(w.goBin, w.root, w.index, mutantRun); rc != 1 {
		t.Fatalf("exit %d, want go test -c's 1", rc)
	}
	if got := logLines(t, w); got != build {
		t.Fatalf("ran %q after a failed build", got)
	}
}

func TestRunRunsTheWholePackageWhenTheReachingTestsFailTogether(t *testing.T) {
	w := newWorld(t)
	t.Setenv("CHECK_RC", "1")
	t.Setenv("NARROW_RC", "1")
	for range 2 {
		if rc := run(w.goBin, w.root, w.index, mutantRun); rc != 0 {
			t.Fatalf("exit %d, want the whole run's 0", rc)
		}
	}
	// The set is checked once, and never narrowed.
	if got, want := logLines(t, w), build+check+wholeRun+build+wholeRun; got != want {
		t.Fatalf("ran\n%s\nwant\n%s", got, want)
	}
	if got := read(t, filepath.Join(w.index, "log")); strings.Count(got, " the reaching tests fail together on the original code (build ") != 2 {
		t.Fatalf("log %q", got)
	}
}

func TestRunRunsTheWholePackageWhenTheIndexCannotNarrow(t *testing.T) {
	cases := []struct {
		name, index, why string
	}{
		{"no test reaches the line", "package example.com/m/internal/p\ndir internal/p\ntest TestAdd\np.go 4 6\ntest TestOther\n", "no indexed test reaches the line"},
		{"every test reaches it", "package example.com/m/internal/p\ndir internal/p\ntest TestLess\np.go 8 11\ntest ExampleLess\np.go 9 9\n", "every indexed test reaches the line"},
		{"an index of another package", "package example.com/m/internal/q\ndir internal/q\ntest TestLess\np.go 8 11\n", "the index is of another package"},
	}
	for _, tc := range cases {
		w := newWorld(t)
		write(t, filepath.Join(w.index, "index"), tc.index)
		if rc := run(w.goBin, w.root, w.index, mutantRun); rc != 0 {
			t.Fatalf("%s: exit %d", tc.name, rc)
		}
		if got, want := logLines(t, w), build+wholeRun; got != want {
			t.Fatalf("%s: ran\n%s\nwant\n%s", tc.name, got, want)
		}
		if got := read(t, filepath.Join(w.index, "log")); !strings.Contains(got, " "+tc.why+" (build ") {
			t.Fatalf("%s: log %q", tc.name, got)
		}
	}
	// Two files changed: no single site, so the whole package.
	w := newWorld(t)
	write(t, filepath.Join(w.work, "other.go"), "package p\n\nvar X = 1\n")
	if rc := run(w.goBin, w.root, w.index, mutantRun); rc != 0 {
		t.Fatalf("two files changed: exit %d", rc)
	}
	if got := read(t, filepath.Join(w.index, "log")); !strings.HasPrefix(got, "- ") || !strings.Contains(got, "2 files differ") {
		t.Fatalf("two files changed: log %q", got)
	}
}

// The whole run runs under bounded's own clock: its 30 s of tests are stopped
// at the planned 4 s, and testreach then waits until 30 s past gremlins' 6 s.
func TestRunStopsAWholeRunAtThePlannedLimitAndWaitsForGremlins(t *testing.T) {
	w := newWorld(t)
	delete(w.took, "whole")
	t.Setenv("WHOLE_SLEEP", "30")
	before := time.Now()
	rc := run(w.goBin, w.root, w.index, mutantRun)
	after := time.Now()
	if rc != 1 {
		t.Fatalf("exit %d after a stopped whole run", rc)
	}
	if got := calledFor(t, w, "whole").limit; got != 4*time.Second {
		t.Fatalf("the whole run's limit: %s, want the planned 4 s", got)
	}
	// run reads its start between before and after.
	if wait := 6*time.Second + afterLimit; w.waited.Before(before.Add(wait)) || w.waited.After(after.Add(wait)) {
		t.Fatalf("waited until %s, want %s past the start, in [%s, %s]", w.waited, wait, before, after)
	}
	if got := read(t, filepath.Join(w.index, "log")); !strings.Contains(got, " lived ") || !strings.Contains(got, " timeout ") || !strings.Contains(got, "; held 2.00 past the planned limit") {
		t.Fatalf("log %q", got)
	}
}

// waitUntil returns at the deadline, not before it. The planted break: a
// waitUntil that returns at once.
func TestWaitUntilSleepsToTheDeadline(t *testing.T) {
	deadline := time.Now().Add(200 * time.Millisecond)
	waitUntil(deadline)
	if now := time.Now(); now.Before(deadline) {
		t.Fatalf("returned at %s, before the deadline %s", now, deadline)
	}
}

func TestRunWithoutLimitsLogsItForMutationToFail(t *testing.T) {
	if os.Getenv("TESTREACH_EXEC") == "nolimits" {
		os.Exit(run(os.Getenv("FAKE_GO"), os.Getenv("ROOT"), os.Getenv("INDEX"), mutantRun))
	}
	w := newWorld(t)
	if err := os.Remove(filepath.Join(w.index, "limits")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^TestRunWithoutLimitsLogsItForMutationToFail$")
	cmd.Env = append(os.Environ(), "TESTREACH_EXEC=nolimits", "FAKE_GO="+w.goBin, "ROOT="+w.root, "INDEX="+w.index)
	_ = cmd.Run()
	if got := read(t, filepath.Join(w.index, "log")); !strings.HasPrefix(got, "- 0/0 - - - - limits unreadable: ") {
		t.Fatalf("log %q", got)
	}
	if got := read(t, w.log); got != "go "+strings.Join(mutantRun, " ")+"\n" {
		t.Fatalf("go ran %q", got)
	}
}

func TestRunHandsAnyOtherCommandToGo(t *testing.T) {
	if os.Getenv("TESTREACH_EXEC") == "other" {
		os.Exit(run(os.Getenv("FAKE_GO"), "/nowhere", "/nowhere", []string{"mod", "download"}))
	}
	w := newWorld(t)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^TestRunHandsAnyOtherCommandToGo$")
	cmd.Env = append(os.Environ(), "TESTREACH_EXEC=other", "FAKE_GO="+w.goBin, "GO_RC=4")
	err := cmd.Run()
	var code int
	if ee, ok := err.(*exec.ExitError); ok { //nolint:errorlint // Run returns the *ExitError itself
		code = ee.ExitCode()
	}
	if code != 4 || read(t, w.log) != "go mod download\n" {
		t.Fatalf("exit %d, go ran %q; want go's 4 and go mod download", code, read(t, w.log))
	}
}

func TestReachingReadsTheIndexByLine(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "index"), "package example.com/m/p\ndir p\ntest TestA\na.go 3 5\nb.go 1 1\ntest TestB\na.go 5 9\ntest TestC\n")
	r, err := readIndex(filepath.Join(d, "index"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file string
		line int
		want []string
	}{{"a.go", 3, []string{"TestA"}}, {"a.go", 5, []string{"TestA", "TestB"}}, {"a.go", 10, []string{}}, {"b.go", 1, []string{"TestA"}}} {
		if got := r.reaching(tc.file, tc.line); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:%d: got %q, want %q", tc.file, tc.line, got, tc.want)
		}
	}
	write(t, filepath.Join(d, "bad"), "package x\ndir p\na.go 1 2\n")
	if _, err := readIndex(filepath.Join(d, "bad")); err == nil {
		t.Error("an index with a range before any test read")
	}
}

// module writes a module with one package, p, of the files given.
func module(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	if err := os.MkdirAll(filepath.Join(root, "p", "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, src := range files {
		write(t, filepath.Join(root, "p", name), src)
	}
	return root
}

func goBin(t *testing.T) string {
	t.Helper()
	g, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go on PATH")
	}
	return g
}

// casecoverBin builds scripts/casecover, which index runs on each profile.
func casecoverBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "casecover")
	if out, err := exec.CommandContext(t.Context(), goBin(t), "build", "-o", bin, "../casecover").CombinedOutput(); err != nil {
		t.Fatalf("building casecover: %v\n%s", err, out)
	}
	return bin
}

func TestIndexRecordsTheLinesEachTestExampleAndFuzzSeedRuns(t *testing.T) {
	root := module(t, map[string]string{
		"p.go": "package p\n\nvar x int\n\nfunc init() { x = 1 }\n\n// Add adds.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Sub subtracts.\nfunc Sub(a, b int) int {\n\treturn a - b\n}\n\n// Neg negates.\nfunc Neg(a int) int {\n\treturn -a\n}\n\n// Kind names n.\nfunc Kind(n int) string {\n\tswitch {\n\tcase n < 0:\n\t\treturn \"neg\"\n\t}\n\treturn \"other\"\n}\n",
		"p_test.go": "package p\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n)\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 || Kind(-1) != \"neg\" {\n\t\tt.Fatal()\n\t}\n}\n\nfunc BenchmarkSub(b *testing.B) { Sub(1, 1) }\n\n" +
			"func FuzzSub(f *testing.F) {\n\tf.Add(1)\n\tf.Fuzz(func(t *testing.T, a int) { Sub(a, a) })\n}\n\nfunc ExampleNeg() {\n\tfmt.Println(Neg(1))\n\t// Output: -1\n}\n",
		"sub/sub.go": "package sub\n",
	})
	out := filepath.Join(t.TempDir(), "reach")
	why, err := index(goBin(t), casecoverBin(t), "", root, "p", out, 2, time.Minute)
	if err != nil || why != "" {
		t.Fatalf("index: %q, %v", why, err)
	}
	r, err := readIndex(filepath.Join(out, "index"))
	if err != nil {
		t.Fatal(err)
	}
	if r.pkg != "example.com/m/p" || r.dir != "p" || !reflect.DeepEqual(r.tests, []string{"TestAdd", "FuzzSub", "ExampleNeg"}) {
		t.Fatalf("index of %s in %s: tests %q", r.pkg, r.dir, r.tests)
	}
	for _, tc := range []struct {
		line int
		want []string
	}{
		{9, []string{"TestAdd"}},
		{14, []string{"FuzzSub"}},
		{19, []string{"ExampleNeg"}},
		// init runs before every test, so every test reaches it.
		{5, []string{"TestAdd", "FuzzSub", "ExampleNeg"}},
		// A case expression: no block holds it, and casecover credits it to
		// the test that ran its clause's body.
		{25, []string{"TestAdd"}},
	} {
		if got := r.reaching("p.go", tc.line); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("p.go:%d: got %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestIndexRefusesAPackageWhoseTestFailsAlone(t *testing.T) {
	root := module(t, map[string]string{
		"p.go":      "package p\n\n// State is set by one test and read by the next.\nvar State int\n",
		"p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestSet(t *testing.T) { State = 1 }\n\nfunc TestRead(t *testing.T) {\n\tif State != 1 {\n\t\tt.Fatal(\"needs TestSet first\")\n\t}\n}\n",
	})
	out := filepath.Join(t.TempDir(), "reach")
	why, err := index(goBin(t), casecoverBin(t), "", root, "p", out, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(why, "TestRead fails when it runs alone") {
		t.Fatalf("why: %q", why)
	}
	if _, err := os.Stat(filepath.Join(out, "index")); err == nil {
		t.Fatal("an index was written")
	}
}

// A test that runs its own binary again, as internal/cli's TestScripts runs
// errata, passes alone as it does under go test, and the line only the child
// ran is the test's. Without GOCOVERDIR the child's cover runtime warns on its
// stderr, which TestScripts' `! stderr .` refused, so internal/cli got no
// index. The planted break: runAlone without GOCOVERDIR.
func TestIndexGivesAChildOfTheTestBinaryItsCoverDir(t *testing.T) {
	root := module(t, map[string]string{
		"p.go": "package p\n\n// Twice doubles.\nfunc Twice(a int) int {\n\treturn 2 * a\n}\n",
		"p_test.go": "package p\n\nimport (\n\t\"os\"\n\t\"os/exec\"\n\t\"testing\"\n)\n\n" +
			"func TestMain(m *testing.M) {\n\tif os.Getenv(\"P_CHILD\") == \"1\" {\n\t\tos.Exit(Twice(0))\n\t}\n\tos.Exit(m.Run())\n}\n\n" +
			"func TestChild(t *testing.T) {\n\tcmd := exec.Command(os.Args[0])\n\tcmd.Env = append(os.Environ(), \"P_CHILD=1\")\n" +
			"\tif out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {\n\t\tt.Fatalf(\"child: %v: %q\", err, out)\n\t}\n}\n",
	})
	out := filepath.Join(t.TempDir(), "reach")
	why, err := index(goBin(t), casecoverBin(t), "", root, "p", out, 1, time.Minute)
	if err != nil || why != "" {
		t.Fatalf("index: %q, %v", why, err)
	}
	r, err := readIndex(filepath.Join(out, "index"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.reaching("p.go", 5); !reflect.DeepEqual(got, []string{"TestChild"}) {
		t.Errorf("p.go:5, run by the child alone: got %q, want TestChild", got)
	}
}

func TestIndexOfAPackageWithoutTests(t *testing.T) {
	root := module(t, map[string]string{"p.go": "package p\n"})
	why, err := index(goBin(t), casecoverBin(t), "", root, "p", filepath.Join(t.TempDir(), "reach"), 1, time.Minute)
	if err != nil || why != "it has no test files" {
		t.Fatalf("got %q, %v", why, err)
	}
}
