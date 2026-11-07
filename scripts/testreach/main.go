// Command testreach runs a mutant's tests narrowed to those that reach its line,
// and the whole package's when those pass.
//
// Usage:
//
//	testreach index <go> <casecover> <tags> <module root> <package dir> <out dir> <jobs> <limit>
//	testreach run <go> <module root> <index dir> <go arguments...>
//
// scripts/mutation.sh runs `index` once per package before gremlins' pass. It
// builds the package's test binary with -cover and runs it once per test,
// Example and Fuzz target (its seeds), each alone with a profile of its own,
// <jobs> at once, each for at most <limit> seconds, the planned limit per
// mutant, and writes which lines each one ran to <out dir>/index. Each
// profile first goes through scripts/casecover, as gremlins' does, so a case
// expression counts as run by the tests that ran its clause's body. A
// package whose tests fail when one runs alone gets no index: a test that
// fails alone would fail every narrowed run it is in, and read as a kill the
// whole package's tests never made. It exits 3 with the reason on stdout when
// the package gets no index, and 1 on an error.
//
// The go shim, scripts/goshim.sh, runs `run` for each mutant's go test, as
// gremlins v0.6.0 makes it: `go test [-tags <tags>] -timeout <t> -failfast
// <package>`. Any other command goes to go unchanged. The mutant's file is the
// one in the working directory, gremlins' copy of the package, that differs
// from the module root's; the line is that of the one token that differs.
//
// testreach builds the mutant's test binary once, with go test -c and
// gremlins' flags, bounded at the planned limit, and runs the binary as go
// test would, in the package's directory:
//
//   - the tests the index says reach the line, with -test.run, once those
//     tests have passed together on the original code (each set is run once a
//     pass, with the index's binary, and the answer kept). When the binary
//     itself exits non-zero, a test failed, and testreach exits 1: gremlins
//     reads a kill, and the same test fails in the whole package's run, whose
//     verdict that is.
//   - when they pass, or end any other way (output held past go test's wait, a
//     binary that never started), the whole package's tests, and any failure
//     of the binary, held output included, is exit 1, as go test reports it.
//     So a mutant lives
//     only when the whole package's tests pass.
//   - the whole package's tests alone when no single token differs, when no
//     test in the index reaches the line, when every test does, or when the
//     reaching tests fail together on the original code.
//
// A build that fails ends testreach with go test -c's status, the one go test
// gives for it.
//
// The limit. scripts/mutation.sh gives gremlins a coefficient half again the
// one it planned, and writes both to <index dir>/limits. testreach reads
// gremlins' limit from -timeout, which gremlins sets 2 s above it, and takes
// the planned limit L as its share of that. The build and the whole run
// together get L: the build, which holds the one compile, the one link and
// go's one start, is bounded at L, and the whole run of the binary at L less
// the build's time B. So build and tests are charged B + T against L, term
// for term as one cold go test is charged them. A build past L, or a whole run
// past L less B, is timed out, and that is final. The narrowed run and the
// check of its set get the rest, less half a second, and add only their own
// time; a narrowed run that passes its share goes to the whole run, as one
// that passes does. Every test binary gets -test.timeout L plus 2 s, as
// gremlins' go test did, so a test sees the same t.Deadline narrowed or whole,
// and go test's wait for its output after it exits, the larger of 5 s and a
// tenth of that timeout, after which output still held is a failure, as go
// test's "Test I/O incomplete" is. A run past its bound is stopped, and
// testreach then waits for gremlins' own limit to end it, since gremlins reads
// a timeout only when its own limit runs out: a mutant that times out holds
// its worker for one and a half limits. When the limits file cannot be read,
// testreach logs it and hands the command to go, and scripts/mutation.sh
// fails the package.
//
// TERM, INT or HUP to testreach kill the group it runs go or the binary in,
// and on Linux the child also dies with testreach.
//
// Each mutant adds a line to <index dir>/log: the site, the tests run of the
// tests indexed, the narrowed run's result and seconds, the whole run's (or
// the build's, when the build ended it), and why. A result is killed (exit 1),
// lived (exit 0), exit<n>, signal or timeout; a dash where a run did not
// happen.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "index":
		if len(os.Args) != 10 {
			usage()
		}
		jobs, err := strconv.Atoi(os.Args[8])
		if err != nil || jobs < 1 {
			usage()
		}
		limit, err := strconv.ParseFloat(os.Args[9], 64)
		if err != nil || limit <= 0 {
			usage()
		}
		why, err := index(os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6], os.Args[7], jobs, time.Duration(limit*float64(time.Second)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "testreach: %v\n", err)
			os.Exit(1)
		}
		if why != "" {
			fmt.Fprintln(os.Stdout, why)
			os.Exit(3)
		}
	case "run":
		if len(os.Args) < 5 {
			usage()
		}
		os.Exit(run(os.Args[2], os.Args[3], os.Args[4], os.Args[5:]))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: testreach index <go> <casecover> <tags> <module root> <package dir> <out dir> <jobs> <limit>\n       testreach run <go> <module root> <index dir> <go arguments...>")
	os.Exit(2)
}

// testName is a name the test binary lists that go test runs without -bench:
// a test, an Example with output, a Fuzz target, whose seeds run as tests.
var testName = regexp.MustCompile(`^(Test|Example|Fuzz)[\p{L}\p{N}_]*$`)

// index writes outDir/index for the package in pkgDir, a directory relative to
// root. It returns why there is none, or an error.
func index(goBin, casecover, tags, root, pkgDir, outDir string, jobs int, limit time.Duration) (string, error) {
	ctx := context.Background()
	var tagArgs []string
	if tags != "" {
		tagArgs = []string{"-tags", tags}
	}
	if err := os.MkdirAll(filepath.Join(outDir, "cov"), 0o750); err != nil { //nolint:gosec // the index directory scripts/mutation.sh names
		return "", err
	}
	list := exec.CommandContext(ctx, goBin, append(append([]string{"list"}, tagArgs...), "-f", "{{.ImportPath}} {{len .TestGoFiles}} {{len .XTestGoFiles}}", "./"+pkgDir)...) //nolint:gosec // the go and package scripts/mutation.sh names
	list.Dir = root
	out, err := list.Output()
	if err != nil {
		return "", fmt.Errorf("go list ./%s: %w", pkgDir, err)
	}
	var importPath string
	var nTest, nXTest int
	if _, err := fmt.Sscanf(string(out), "%s %d %d", &importPath, &nTest, &nXTest); err != nil {
		return "", fmt.Errorf("go list ./%s printed %q: %w", pkgDir, out, err)
	}
	if nTest+nXTest == 0 {
		return "it has no test files", nil
	}
	bin := filepath.Join(outDir, "pkg.test")
	build := exec.CommandContext(ctx, goBin, append(append([]string{"test", "-c", "-cover"}, tagArgs...), "-o", bin, "./"+pkgDir)...) //nolint:gosec // as above
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go test -c ./%s: %w\n%s", pkgDir, err, out)
	}
	dir := filepath.Join(root, filepath.FromSlash(pkgDir))
	lister := exec.CommandContext(ctx, bin, "-test.list", ".*") //nolint:gosec // the binary built above
	lister.Dir = dir
	out, err = lister.Output()
	if err != nil {
		return "", fmt.Errorf("listing the tests of %s: %w", pkgDir, err)
	}
	var names []string
	for _, n := range strings.Fields(string(out)) {
		if testName.MatchString(n) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "its test binary lists no test", nil
	}
	failed := make([]string, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	next := make(chan int)
	for range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				failed[i], errs[i] = runAlone(ctx, bin, casecover, root, dir, names[i], filepath.Join(outDir, "cov", strconv.Itoa(i)), limit)
			}
		}()
	}
	for i := range names {
		next <- i
	}
	close(next)
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	for i, f := range failed {
		if f != "" {
			return fmt.Sprintf("%s fails when it runs alone (%s)", names[i], f), nil
		}
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "package %s\ndir %s\n", importPath, pkgDir)
	for i, n := range names {
		fmt.Fprintf(&b, "test %s\n", n)
		if err := ranLines(&b, filepath.Join(outDir, "cov", strconv.Itoa(i)), importPath); err != nil {
			return "", err
		}
	}
	tmp := filepath.Join(outDir, "index.tmp")
	if err := os.WriteFile(tmp, b.Bytes(), 0o600); err != nil { //nolint:gosec // in the index directory scripts/mutation.sh names
		return "", err
	}
	return "", os.Rename(tmp, filepath.Join(outDir, "index")) //nolint:gosec // as above
}

// runAlone runs one test of the binary with a profile of its own, as go test
// runs the binary for a mutant, then casecover on the profile. It says how the
// test failed, empty when it passed, and returns an error when casecover fails.
func runAlone(ctx context.Context, bin, casecover, root, dir, name, profile string, limit time.Duration) (string, error) {
	cmd := exec.CommandContext(ctx, bin, "-test.run", "^"+name+"$", "-test.count", "1", "-test.paniconexit0", //nolint:gosec // the binary built above, a name it listed
		"-test.timeout", limit.String(), "-test.coverprofile", profile)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		cc := exec.CommandContext(ctx, casecover, root, profile) //nolint:gosec // the casecover scripts/mutation.sh names
		if out, err := cc.CombinedOutput(); err != nil {
			return "", fmt.Errorf("casecover on the profile of %s: %w: %s", name, err, strings.TrimSpace(string(out)))
		}
		return "", nil
	}
	last := strings.TrimSpace(string(out))
	if i := strings.LastIndexByte(last, '\n'); i >= 0 {
		last = last[i+1:]
	}
	return fmt.Sprintf("%v: %s", err, last), nil
}

// profileLine is a profile's block: file:startLine.startCol,endLine.endCol statements count.
var profileLine = regexp.MustCompile(`^(.+):([0-9]+)\.[0-9]+,([0-9]+)\.[0-9]+ [0-9]+ ([0-9]+)$`)

// ranLines writes "<file> <first line> <last line>" for each block of the
// profile that ran, in a file of the package itself; a file in a directory
// below it is another package's.
func ranLines(w io.Writer, profile, importPath string) error {
	data, err := os.ReadFile(profile) //nolint:gosec // the profile runAlone wrote
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		m := profileLine.FindStringSubmatch(line)
		if m == nil || m[4] == "0" {
			continue
		}
		file, ok := strings.CutPrefix(m[1], importPath+"/")
		if !ok || strings.Contains(file, "/") {
			continue
		}
		fmt.Fprintf(w, "%s %s %s\n", file, m[2], m[3])
	}
	return nil
}

// reach is a package's index: its import path, directory, tests, and the
// lines each ran.
type reach struct {
	pkg, dir string
	tests    []string
	ranges   map[string][]span
}

type span struct{ first, last, test int }

func readIndex(path string) (*reach, error) {
	f, err := os.Open(path) //nolint:gosec // the index scripts/mutation.sh names
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := &reach{ranges: map[string][]span{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		switch {
		case len(fields) == 2 && fields[0] == "package":
			r.pkg = fields[1]
		case len(fields) == 2 && fields[0] == "dir":
			r.dir = fields[1]
		case len(fields) == 2 && fields[0] == "test":
			r.tests = append(r.tests, fields[1])
		case len(fields) == 3 && len(r.tests) > 0:
			first, err1 := strconv.Atoi(fields[1])
			last, err2 := strconv.Atoi(fields[2])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("%s: not an index line: %q", path, sc.Text())
			}
			r.ranges[fields[0]] = append(r.ranges[fields[0]], span{first, last, len(r.tests) - 1})
		default:
			return nil, fmt.Errorf("%s: not an index line: %q", path, sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if r.pkg == "" || r.dir == "" {
		return nil, fmt.Errorf("%s names no package or directory", path)
	}
	return r, nil
}

// reaching is the tests that ran the line, in the index's order.
func (r *reach) reaching(file string, line int) []string {
	seen := map[int]bool{}
	for _, s := range r.ranges[file] {
		if s.first <= line && line <= s.last {
			seen[s.test] = true
		}
	}
	idx := make([]int, 0, len(seen))
	for i := range seen {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	names := make([]string, len(idx))
	for k, i := range idx {
		names[k] = r.tests[i]
	}
	return names
}

// mutantArgs is gremlins' go test for a mutant, split into its parts; false
// for any other command.
type mutantArgs struct {
	tags    string
	timeout time.Duration
	pkg     string
}

func parseMutant(args []string) (mutantArgs, bool) {
	var m mutantArgs
	rest := args
	if len(rest) == 0 || rest[0] != "test" {
		return m, false
	}
	rest = rest[1:]
	if len(rest) >= 2 && rest[0] == "-tags" {
		m.tags = rest[1]
		rest = rest[2:]
	}
	if len(rest) != 4 || rest[0] != "-timeout" || rest[2] != "-failfast" || strings.HasPrefix(rest[3], "-") {
		return m, false
	}
	d, err := time.ParseDuration(rest[1])
	if err != nil || d <= 2*time.Second {
		return m, false
	}
	m.timeout, m.pkg = d, rest[3]
	return m, true
}

// buildArgs is the go test -c that builds the mutant's test binary to out,
// with the flags gremlins' go test builds with.
func (m mutantArgs) buildArgs(out string) []string {
	args := []string{"test", "-c"}
	if m.tags != "" {
		args = append(args, "-tags", m.tags)
	}
	return append(args, "-o", out, m.pkg)
}

// binArgs is what go test passes the test binary for gremlins' go test, with
// the binary's own timeout and, when not empty, a -run. GOFLAGS' -count=1 is
// passed as go test passes it.
func binArgs(timeout time.Duration, runExpr string) []string {
	args := []string{"-test.paniconexit0", "-test.timeout=" + timeout.String(), "-test.failfast", "-test.count=1"}
	if runExpr != "" {
		args = append(args, "-test.run="+runExpr)
	}
	return args
}

// outcome is how one command ended.
type outcome struct {
	code     int // the exit status, when it exited
	sig      syscall.Signal
	signaled bool
	timedOut bool // stopped at its limit
	// ioWait: it exited 0 and its output was still held after go test's wait.
	ioWait bool
	// notStarted: it never ran, as when a fork is refused, or Wait failed for
	// a reason other than how it ended, which tells nothing of the tests.
	notStarted bool
	took       time.Duration
}

func (o outcome) String() string {
	switch {
	case o.timedOut:
		return "timeout"
	case o.notStarted:
		return "notstarted"
	case o.ioWait:
		return "iowait"
	case o.signaled:
		return "signal"
	case o.code == 0:
		return "lived"
	case o.code == 1:
		return "killed"
	}
	return "exit" + strconv.Itoa(o.code)
}

// failed is a test binary that ended by itself with a failure: go test reports
// any such end as a failure, exit 1, output still held after its wait among
// them.
func (o outcome) failed() bool { return !o.timedOut && (o.signaled || o.code != 0 || o.ioWait) }

// testFailed is a binary that exited non-zero itself: a test failed. Only that
// ends a mutant on a narrowed run. Output held past go test's wait depends on
// how long the rest of the package runs, which narrowing changes, and a binary
// that never started tells nothing of the tests (delta review 3 of #264, G1).
func (o outcome) testFailed() bool {
	return !o.timedOut && !o.signaled && !o.ioWait && !o.notStarted && o.code != 0
}

// afterLimit is how long past gremlins' limit testreach waits for gremlins to
// end it, after a run it stopped at the planned limit.
var afterLimit = 30 * time.Second

func secs(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 2, 64) }

// run handles one go command gremlins makes, and returns the status to exit with.
func run(goBin, root, indexDir string, args []string) int {
	m, ok := parseMutant(args)
	if !ok {
		return execGo(goBin, args)
	}
	logPath := filepath.Join(indexDir, "log")
	c, e, err := readLimits(filepath.Join(indexDir, "limits"))
	if err != nil {
		// Gremlins' limit may be half again the planned one, and testreach
		// cannot tell. scripts/mutation.sh fails the package on this line.
		appendLog(logPath, fmt.Sprintf("- 0/0 - - - - limits unreadable: %s\n", strings.ReplaceAll(err.Error(), "\n", " ")))
		return execGo(goBin, args)
	}
	stop := catchSignals()
	defer stop()
	given := m.timeout - 2*time.Second
	whole := given * time.Duration(c) / time.Duration(e)
	// L for the build and the whole run, and the rest for the narrowed run.
	narrowed := given - whole - 500*time.Millisecond
	start := time.Now()
	site, why := "-", ""
	var tests []string
	r, err := readIndex(filepath.Join(indexDir, "index"))
	nTests := 0
	switch {
	case err != nil:
		why = "index unreadable"
	case r.pkg != m.pkg:
		why = "the index is of another package"
	default:
		nTests = len(r.tests)
		wd, err := os.Getwd()
		if err != nil {
			why = "no working directory"
		} else if file, line, err := changedLine(wd, filepath.Join(root, filepath.FromSlash(r.dir))); err != nil {
			why = err.Error()
		} else {
			site = fmt.Sprintf("%s:%d", file, line)
			tests = r.reaching(file, line)
			switch {
			case len(tests) == 0:
				why = "no indexed test reaches the line"
			case len(tests) == len(r.tests):
				why = "every indexed test reaches the line"
			case narrowed <= 0:
				why = "no time for a narrowed run"
			}
		}
	}
	logf := func(n outcome, ran bool, w outcome, wran bool, why string) {
		ns, nt, ws, wt := "-", "-", "-", "-"
		if ran {
			ns, nt = n.String(), secs(n.took)
		}
		if wran {
			ws, wt = w.String(), secs(w.took)
		}
		appendLog(logPath, fmt.Sprintf("%s %d/%d %s %s %s %s %s\n", site, len(tests), nTests, ns, nt, ws, wt, strings.ReplaceAll(why, "\n", " ")))
	}
	timedOut := func() int {
		// Gremlins reads a timeout only when its own limit ends the command.
		time.Sleep(time.Until(start.Add(given)) + afterLimit)
		return 1
	}
	// held is what a timeout costs past the planned limit: testreach holds
	// the worker until gremlins' own limit, for gremlins to read it.
	held := fmt.Sprintf("; held %s past the planned limit", secs(given-whole))
	var b outcome

	// The build, once, bounded at the planned limit as today's go test is. Its
	// time is charged to the whole run, so build and whole tests together get
	// the planned limit, as they do under gremlins today.
	tmp, err := os.MkdirTemp("", "testreach-")
	if err != nil {
		logf(outcome{}, false, outcome{code: 2}, true, "no directory for the test binary: "+err.Error())
		return 2
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	bin := filepath.Join(tmp, "mutant.test")
	b = bounded(goBin, m.buildArgs(bin), "", whole, 0)
	switch {
	case b.timedOut:
		// The build alone took the planned limit: timed out, as one go test
		// would be.
		logf(outcome{}, false, b, true, "the build took the whole limit"+held)
		return timedOut()
	case b.signaled:
		logf(outcome{}, false, b, true, "the build was signalled")
		raise(b.sig)
		return 3
	case b.code != 0:
		// go test exits with go test -c's status when the build fails.
		logf(outcome{}, false, b, true, "the build failed")
		return b.code
	}
	if _, err := os.Stat(bin); err != nil {
		logf(outcome{}, false, outcome{code: 2}, true, "the build wrote no test binary")
		return 2
	}
	binTimeout := whole + 2*time.Second
	// go test's own wait for a test binary's output after it exits: the
	// larger of 5 s and a tenth of -timeout (cmd/go/internal/test, Go 1.27).
	ioWait := max(5*time.Second, binTimeout/10)

	var n outcome
	ran := false
	if why == "" {
		expr := "^(" + strings.Join(tests, "|") + ")$"
		ok, left, checkWhy := checkSet(filepath.Join(indexDir, "pkg.test"), filepath.Join(root, filepath.FromSlash(r.dir)), indexDir, expr, binTimeout, ioWait, narrowed)
		if ok {
			ran = true
			n = bounded(bin, binArgs(binTimeout, expr), "", left, ioWait)
			if n.testFailed() {
				logf(n, true, outcome{}, false, "narrowed"+checkWhy)
				return 1
			}
			why = "narrowed, then whole" + checkWhy
		} else {
			why = checkWhy
		}
	}
	w := bounded(bin, binArgs(binTimeout, ""), "", whole-b.took, ioWait)
	if w.timedOut {
		// Build and whole tests passed the planned limit: timed out, final.
		logf(n, ran, w, true, fmt.Sprintf("%s (build %s)%s", why, secs(b.took), held))
		return timedOut()
	}
	logf(n, ran, w, true, fmt.Sprintf("%s (build %s)", why, secs(b.took)))
	if w.failed() {
		return 1
	}
	return 0
}

// checkSet says whether the reaching tests, run together on the original
// code, pass: the index proved each alone, not each set. The answer is kept in
// the index directory, so each set runs once a pass. It runs the index's own
// binary, built from the original code, in the original package directory,
// within the narrowed run's share, and returns the share left and why, as a
// suffix to the log's reason, or the reason the set is not narrowed.
func checkSet(origBin, origDir, indexDir, expr string, binTimeout, ioWait, share time.Duration) (bool, time.Duration, string) {
	sum := sha256.Sum256([]byte(expr))
	memo := filepath.Join(indexDir, "sets", hex.EncodeToString(sum[:8]))
	switch got, _ := os.ReadFile(memo); string(got) { //nolint:gosec // in the index directory
	case "pass":
		return true, share, ""
	case "fail":
		return false, 0, "the reaching tests fail together on the original code"
	}
	o := bounded(origBin, binArgs(binTimeout, expr), origDir, share, ioWait)
	left := share - o.took
	var verdict string
	switch {
	case o.timedOut:
		return false, 0, "no time to run the reaching tests together on the original code"
	case o.failed():
		verdict = "fail"
	default:
		verdict = "pass"
	}
	if err := os.MkdirAll(filepath.Dir(memo), 0o750); err == nil { //nolint:gosec // in the index directory
		tmp := fmt.Sprintf("%s.%d", memo, os.Getpid())
		if os.WriteFile(tmp, []byte(verdict), 0o600) == nil { //nolint:gosec // as above
			_ = os.Rename(tmp, memo) //nolint:gosec // as above
		}
	}
	if verdict == "fail" {
		return false, 0, "the reaching tests fail together on the original code"
	}
	if left <= 0 {
		return false, 0, "no time left for a narrowed run after checking its tests"
	}
	return true, left, fmt.Sprintf(" (set checked in %s)", secs(o.took))
}

// readLimits reads the planned coefficient and the one gremlins was given.
func readLimits(path string) (int, int, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the file scripts/mutation.sh names
	if err != nil {
		return 0, 0, err
	}
	var c, e int
	if _, err := fmt.Sscanf(string(data), "%d %d", &c, &e); err != nil {
		return 0, 0, err
	}
	if c < 1 || e <= c {
		return 0, 0, fmt.Errorf("%s: want a coefficient and a larger one, got %q", path, data)
	}
	return c, e, nil
}

func appendLog(path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the log in the index directory
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

// execGo replaces this process with go, as the shim would have run it.
func execGo(goBin string, args []string) int {
	path, err := exec.LookPath(goBin)
	if err == nil {
		err = syscall.Exec(path, append([]string{goBin}, args...), os.Environ()) //nolint:gosec // the go the shim names, with gremlins' arguments
	}
	fmt.Fprintf(os.Stderr, "testreach: running go: %v\n", err)
	return 2
}

// current is the command bounded runs, for the signal handler.
var current struct {
	sync.Mutex
	cmd      *exec.Cmd
	stopping bool // set by the signal handler: no command starts
}

// catchSignals kills the running command's process group when testreach gets
// TERM, INT or HUP, as the deadline's timeout and a cancelled job send, and
// then ends testreach with that signal. Without it the group testreach made
// would outlive it, outside the process group and the parent links
// scripts/mutation.sh stops on exit.
func catchSignals() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-ch:
			// The lock is held to the end, so no command starts after the
			// kill and outlives testreach.
			current.Lock()
			current.stopping = true
			if current.cmd != nil {
				killGroup(current.cmd)
			}
			signal.Reset()
			if s, ok := sig.(syscall.Signal); ok {
				raise(s)
			}
			os.Exit(1)
		case <-done:
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}

// bounded runs a command in a process group of its own, in dir when not
// empty, and kills the group at the limit. With an ioWait, the command is a
// test binary run as go test runs one: its output goes to a pipe that Wait
// drains, for at most ioWait after it exits, and output still held then is a
// failure, exit 1, as go test reports "Test I/O incomplete". go itself gets
// no output, as gremlins runs it.
func bounded(name string, args []string, dir string, limit, ioWait time.Duration) outcome {
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // go, or a test binary built here
	cmd.Dir = dir
	if ioWait > 0 {
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		cmd.WaitDelay = ioWait
	}
	ownGroup(cmd)
	start := time.Now()
	if limit <= 0 {
		return outcome{timedOut: true}
	}
	current.Lock()
	if current.stopping {
		current.Unlock()
		return outcome{code: 2, notStarted: true}
	}
	err := cmd.Start()
	if err == nil {
		current.cmd = cmd
	}
	current.Unlock()
	if err != nil {
		return outcome{code: 2, notStarted: true, took: time.Since(start)}
	}
	var mu sync.Mutex
	stopped := false
	timer := time.AfterFunc(limit, func() {
		mu.Lock()
		stopped = true
		mu.Unlock()
		killGroup(cmd)
	})
	err = cmd.Wait()
	timer.Stop()
	o := outcome{took: time.Since(start)}
	mu.Lock()
	o.timedOut = stopped
	mu.Unlock()
	// Whatever the group left behind goes too.
	killGroup(cmd)
	current.Lock()
	current.cmd = nil
	current.Unlock()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(err, exec.ErrWaitDelay):
		o.ioWait = true
	case errors.As(err, &exitErr):
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			o.signaled, o.sig = true, ws.Signal()
		} else {
			o.code = exitErr.ExitCode()
		}
	default:
		// Copying the output failed, which io.Discard cannot do today. It
		// says nothing of the tests, so a narrowed run sends the mutant to the
		// whole run, as for a binary that never started (delta review 4 of
		// #264, H1).
		o.code, o.notStarted = 2, true
	}
	return o
}

// changedLine finds the one Go file of the package that differs between the
// working copy and the original, and in it the one token that differs, by its
// line in the original.
func changedLine(work, orig string) (string, int, error) {
	entries, err := os.ReadDir(orig)
	if err != nil {
		return "", 0, err
	}
	var changed []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		a, err := os.ReadFile(filepath.Join(orig, e.Name())) //nolint:gosec // a file of the package
		if err != nil {
			return "", 0, err
		}
		b, err := os.ReadFile(filepath.Join(work, e.Name())) //nolint:gosec // its working copy
		if err != nil {
			return "", 0, err
		}
		if !bytes.Equal(a, b) {
			changed = append(changed, e.Name())
		}
	}
	if len(changed) != 1 {
		return "", 0, fmt.Errorf("%d files differ from the original, not 1", len(changed))
	}
	name := changed[0]
	a, _ := os.ReadFile(filepath.Join(orig, name)) //nolint:gosec // as above
	b, _ := os.ReadFile(filepath.Join(work, name)) //nolint:gosec // as above
	line, err := changedToken(a, b)
	if err != nil {
		return "", 0, fmt.Errorf("%s: %w", name, err)
	}
	return name, line, nil
}

type tok struct {
	tok  token.Token
	lit  string
	line int
}

func tokens(src []byte) ([]tok, error) {
	fset := token.NewFileSet()
	file := fset.AddFile("", -1, len(src))
	var s scanner.Scanner
	bad := 0
	s.Init(file, src, func(token.Position, string) { bad++ }, 0)
	var out []tok
	for {
		p, t, lit := s.Scan()
		if t == token.EOF {
			break
		}
		// go/printer moves line breaks, which adds and removes the
		// semicolons the scanner inserts, and turns one between two
		// statements on a line into a break.
		if t == token.SEMICOLON {
			continue
		}
		out = append(out, tok{t, lit, fset.PositionFor(p, false).Line})
	}
	if bad > 0 {
		return nil, errors.New("does not scan")
	}
	return out, nil
}

// changedToken is the line, in a, of the one token that differs between a and
// b; an error when the two differ in more than one token or in their number.
// Gremlins writes a mutant's file with go/printer, which may change its spacing
// and so its lines, but not its tokens other than the mutated one.
func changedToken(a, b []byte) (int, error) {
	ta, err := tokens(a)
	if err != nil {
		return 0, err
	}
	tb, err := tokens(b)
	if err != nil {
		return 0, err
	}
	if len(ta) != len(tb) {
		return 0, errors.New("a different number of tokens")
	}
	line := 0
	for i := range ta {
		if ta[i].tok != tb[i].tok || ta[i].lit != tb[i].lit {
			if line != 0 {
				return 0, errors.New("more than one token differs")
			}
			line = ta[i].line
		}
	}
	if line == 0 {
		return 0, errors.New("no token differs")
	}
	return line, nil
}
