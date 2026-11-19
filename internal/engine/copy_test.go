package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/treecopy"
)

// committed is a git repository whose one commit holds files, with the
// copies and the record of durations going to a cache directory of the
// test's, outside it.
func committed(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "init", "-q", "-b", "main")
	track(t, root)
	git(t, root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "files")
	return root
}

// gitOut is git's stdout for args in dir.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// program is a program check with id and argv, on the repository.
func program(id string, argv ...string) check.Check {
	return check.Check{ID: id, Scope: check.ScopeRepo, Timeout: 10 * time.Second, Kind: &check.Program{Run: argv}}
}

// leftClean fails t unless the repository at root holds what it held
// before (git status prints before), its branches and stash are as
// committed made them, and the run left no copy behind: none in the cache
// and no worktree but the repository's own.
func leftClean(t *testing.T, root, before string) {
	t.Helper()
	if after := gitOut(t, root, "status", "--porcelain=v1", "--untracked-files=all"); after != before {
		t.Errorf("git status of the repository after the run:\n%s\nbefore:\n%s", after, before)
	}
	if got := gitOut(t, root, "for-each-ref", "--format=%(refname)"); got != "refs/heads/main\n" {
		t.Errorf("the repository's refs after the run:\n%s", got)
	}
	if left := copiesOf(t, root); len(left) != 0 {
		t.Errorf("the run left %v in the cache", left)
	}
	if n := strings.Count(gitOut(t, root, "worktree", "list", "--porcelain"), "worktree "); n != 1 {
		t.Errorf("the run left %d worktrees, want the repository's own", n)
	}
}

// copiesOf is the names of the copies of root on disk now.
func copiesOf(t *testing.T, root string) []string {
	t.Helper()
	home, err := treecopy.Home(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(home)
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
}

// Two programs that write the tree run beside a rule, each in a copy of its
// own: each reads back what it wrote, the rule, which runs while both are
// in flight, reads the file as committed, and the repository is as it was.
// The programs hold until the rule has read, so the three run at once.
// Planted break: inCopies that set no tree, where the programs ran alone
// in the repository, the first waited out the rule and the rule read the
// second's write.
func TestACheckWritingInItsCopyLeavesTheRepositoryAndARuleAlone(t *testing.T) {
	root := committed(t, map[string]string{"f.txt": "committed\n"})
	var wrote sync.WaitGroup
	wrote.Add(2)
	read := make(chan struct{})
	roots := map[string]string{}
	var mu sync.Mutex
	e := &Engine{Root: root, workers: 3, Run: func(_ context.Context, at string, c check.Check) check.Result {
		if _, rule := c.Kind.(*check.Rule); rule {
			waited := make(chan struct{})
			go func() { wrote.Wait(); close(waited) }()
			select {
			case <-waited:
			case <-time.After(2 * time.Second):
			}
			defer close(read)
			return check.Result{Check: c, Verdict: check.VerdictTrue, Message: readFile(t, filepath.Join(at, "f.txt"))}
		}
		mu.Lock()
		roots[c.ID] = at
		mu.Unlock()
		for _, name := range []string{"f.txt", c.ID + ".out"} {
			if err := os.WriteFile(filepath.Join(at, name), []byte("written by "+c.ID+"\n"), 0o600); err != nil {
				t.Error(err)
			}
		}
		wrote.Done()
		select {
		case <-read:
		case <-time.After(2 * time.Second):
		}
		return check.Result{Check: c, Verdict: check.VerdictTrue, Message: readFile(t, filepath.Join(at, "f.txt"))}
	}}
	checks := append([]check.Check{program("one"), program("two")}, rules("reads")...)
	rep := e.Execute(t.Context(), checks)
	for i, want := range []string{"written by one\n", "written by two\n", "committed\n"} {
		if got := rep.Results[i].Message; got != want {
			t.Errorf("%s read %q, want %q", rep.Results[i].Name(), got, want)
		}
	}
	if roots["one"] == root || roots["two"] == root || roots["one"] == roots["two"] {
		t.Errorf("the programs ran at %v, want a copy each, not %s", roots, root)
	}
	if got := readFile(t, filepath.Join(root, "f.txt")); got != "committed\n" {
		t.Errorf("f.txt in the repository: %q", got)
	}
	leftClean(t, root, "")
}

// A check in a copy sees what the disk holds, not HEAD: a file changed and
// not committed, one staged, one untracked. Its own write stays in its
// copy. Planted break: a copy without the overlay, which read HEAD's
// f.txt and found neither of the others.
func TestACopiedCheckSeesTheUncommittedTree(t *testing.T) {
	root := committed(t, map[string]string{"f.txt": "committed\n"})
	for name, body := range map[string]string{"f.txt": "changed\n", "s.txt": "staged\n", "u.txt": "untracked\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "add", "s.txt")
	before := gitOut(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	e := New(root, nil)
	e.workers = 2
	reads := []string{"sh", "-c", "cat f.txt s.txt u.txt; echo written > f.txt"}
	rep := e.Execute(t.Context(), []check.Check{program("one", reads...), program("two", reads...)})
	for _, r := range rep.Results {
		if r.Verdict != check.VerdictTrue || r.Message != "changed\nstaged\nuntracked" {
			t.Errorf("%s: %s %q, want what the disk holds", r.Name(), r.Verdict, r.Message)
		}
	}
	if got := readFile(t, filepath.Join(root, "f.txt")); got != "changed\n" {
		t.Errorf("f.txt in the repository: %q, want the uncommitted change", got)
	}
	leftClean(t, root, before)
}

// sameRun is a config's worth of programs, generators and a witness, each
// ending one way the protocol reads: true, false, a JSON message, an exit
// of 3 with stderr, a timeout, a generator that matches its file and one
// that does not, a witness that holds, and a check that writes a marker
// it must not find.
func sameRun() []check.Check {
	gen := func(id, out string) check.Check {
		return check.Check{ID: id, Scope: check.ScopeRepo, Timeout: 10 * time.Second,
			Kind: &check.Generated{File: "gen.txt", Run: []string{"printf", out}, MaxBytes: 1 << 20}}
	}
	timeout := program("slow", "sleep", "5")
	timeout.Timeout = 300 * time.Millisecond
	witnessed := program("no-bad-file", "sh", "-c", `test ! -e "$ERRATA_ROOT/bad"`)
	witnessed.Witness = []string{"w"}
	return []check.Check{
		program("holds", "sh", "-c", "echo it holds"),
		program("fails", "sh", "-c", "echo it fails; exit 1"),
		program("json", "sh", "-c", `echo '{"message": "from json"}'`),
		program("crashes", "sh", "-c", "echo out; echo why >&2; exit 3"),
		timeout,
		gen("current", "line\n"),
		gen("stale", "other\n"),
		witnessed,
		program("marks", "sh", "-c", "test ! -e marker && touch marker"),
	}
}

// The report of a run in copies is the report in place, field by field but
// the time: each verdict, message and witness, a timeout and an exit of 3
// as unknown with the same message. The run in copies goes first and
// leaves no marker in the repository, which the run in place then makes.
// Planted break: the repository's root given to the runner in place of the
// copy's, where the run in copies wrote the marker in the repository.
func TestTheReportIsTheSameInACopy(t *testing.T) {
	root := committed(t, map[string]string{"gen.txt": "line\n", ".errata/witness/w/bad": "x"})
	run := func(workers int) []check.Result {
		e := New(root, nil)
		e.workers = workers
		results := e.Execute(t.Context(), sameRun()).Results
		for i := range results {
			results[i].Duration = 0
		}
		return results
	}
	copied := run(4)
	if _, err := os.Lstat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
		t.Fatalf("the run in copies wrote the marker in the repository: %v", err)
	}
	leftClean(t, root, "")
	inPlace := run(1)
	if !reflect.DeepEqual(copied, inPlace) {
		for i := range copied {
			if !reflect.DeepEqual(copied[i], inPlace[i]) {
				t.Errorf("%s in a copy: %+v\nin place: %+v", copied[i].Name(), copied[i], inPlace[i])
			}
		}
	}
	if want := []check.Verdict{"true", "false", "true", "unknown", "unknown", "true", "false", "true", "true", "true"}; !reflect.DeepEqual(verdictsOf(copied), want) {
		t.Errorf("verdicts %v, want %v", verdictsOf(copied), want)
	}
}

// A check with in_place runs in the repository, alone, as before: the rule
// after it reads what it wrote, while the two checks without it run in
// copies, and the copy made after it holds what it wrote. Planted breaks:
// inCopies that moved a check with in_place too, where the rule read f.txt
// as committed; no Refresh after it, where the copy after it did.
func TestInPlaceKeepsACheckInTheRepository(t *testing.T) {
	root := committed(t, map[string]string{"f.txt": "committed\n"})
	keep := program("keeps")
	keep.InPlace = true
	var mu sync.Mutex
	roots := map[string]string{}
	e := &Engine{Root: root, workers: 2, Run: func(_ context.Context, at string, c check.Check) check.Result {
		mu.Lock()
		roots[c.ID] = at
		mu.Unlock()
		if c.ID == "keeps" {
			if err := os.WriteFile(filepath.Join(at, "f.txt"), []byte("written\n"), 0o600); err != nil {
				t.Error(err)
			}
		}
		return check.Result{Check: c, Verdict: check.VerdictTrue, Message: readFile(t, filepath.Join(at, "f.txt"))}
	}}
	checks := append([]check.Check{program("one"), keep}, rules("reads")...)
	checks = append(checks, program("two"))
	rep := e.Execute(t.Context(), checks)
	if got := rep.Results[2].Message; got != "written\n" {
		t.Errorf("the rule after the check in place read %q, want what it wrote", got)
	}
	if got := rep.Results[3].Message; got != "written\n" {
		t.Errorf("the copied check after the check in place read %q, want what it wrote", got)
	}
	if roots["keeps"] != root || roots["one"] == root || roots["two"] == root {
		t.Errorf("roots %v: want keeps at %s and the others in copies", roots, root)
	}
}

// A check in place does not wait for a check in a copy before it to end,
// only for its copy to be made, so a short check between two long ones
// does not keep them apart: "one" holds until "keeps" has run, and still
// reads the tree as it was before "keeps" wrote it, while "two", after
// it, reads what it wrote. f.txt is changed on disk, so the copy takes it
// from the disk after the 3000 untracked files that sort before it, and a
// write that came while the copy was made would be in it. Planted breaks:
// the check in place waiting for every job before it to end, where "one"
// waited out its time; the job before it let go of the repository before
// its copy was made, where "one" read what "keeps" wrote.
func TestACheckInPlaceRunsBesideACopyBeforeIt(t *testing.T) {
	files := map[string]string{"f.txt": "committed\n"}
	root := committed(t, files)
	for i := range 3000 {
		name := filepath.Join(root, "a", fmt.Sprintf("%04d", i))
		if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("on disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keep := program("keeps")
	keep.InPlace = true
	kept := make(chan struct{})
	e := &Engine{Root: root, workers: 2, Run: func(_ context.Context, at string, c check.Check) check.Result {
		switch c.ID {
		case "one":
			select {
			case <-kept:
			case <-time.After(10 * time.Second):
				return check.Result{Check: c, Verdict: check.VerdictUnknown, Message: "waited for keeps"}
			}
		case "keeps":
			if err := os.WriteFile(filepath.Join(at, "f.txt"), []byte("written\n"), 0o600); err != nil {
				t.Error(err)
			}
			close(kept)
		}
		return check.Result{Check: c, Verdict: check.VerdictTrue, Message: readFile(t, filepath.Join(at, "f.txt"))}
	}}
	rep := e.Execute(t.Context(), []check.Check{program("one"), keep, program("two")})
	for i, want := range []string{"on disk\n", "written\n", "written\n"} {
		if got := rep.Results[i].Message; got != want {
			t.Errorf("%s read %q, want %q", rep.Results[i].Name(), got, want)
		}
	}
}

// A check in place that grows the ignored files past the budget leaves
// the tree one errata does not copy, so each check after it runs in the
// repository, alone, as every check does on such a tree, and gives its
// own verdict rather than unknown. Planted break: refresh that kept the
// copies, where the checks after it were unknown.
func TestChecksAfterATreeOverTheBudgetRunInPlace(t *testing.T) {
	root := committed(t, map[string]string{".gitignore": "bin/\n"})
	grow := program("grows")
	grow.InPlace = true
	var mu sync.Mutex
	roots := map[string]string{}
	e := &Engine{Root: root, workers: 2, Run: func(_ context.Context, at string, c check.Check) check.Result {
		mu.Lock()
		roots[c.ID] = at
		mu.Unlock()
		if c.ID == "grows" {
			big := filepath.Join(at, "bin", "big")
			if err := os.MkdirAll(filepath.Dir(big), 0o750); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(big, nil, 0o600); err != nil {
				t.Error(err)
			}
			// A sparse file: its size counts against the budget, and
			// no disk is written.
			if err := os.Truncate(big, treecopy.Budget+1); err != nil {
				t.Error(err)
			}
		}
		return check.Result{Check: c, Verdict: check.VerdictFalse}
	}}
	rep := e.Execute(t.Context(), []check.Check{program("one"), program("two"), grow, program("three"), program("four")})
	for _, r := range rep.Results {
		if r.Verdict != check.VerdictFalse {
			t.Errorf("%s is %s: %s", r.Name(), r.Verdict, r.Message)
		}
	}
	if roots["one"] == root || roots["three"] != root || roots["four"] != root {
		t.Errorf("roots %v: want one in a copy, and three and four at %s", roots, root)
	}
}

// Copies are made only where they can pay: two jobs or more that would run
// alone, with no record of their last run or one of a second or more, on
// a pool of two workers or more, in a tree errata can copy. A check's
// witness is a job of its own, and a record that does not decode is none.
// Planted breaks: a bound of one job, which copied the lone program; the
// width left out, which copied on a pool of one; the record ignored, which
// copied the fast programs.
func TestCopiesOnlyWhereTheyCanPay(t *testing.T) {
	notRepo := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
		return dir
	}
	repo := func(t *testing.T) string {
		t.Helper()
		return committed(t, map[string]string{".errata/witness/w/f": "x"})
	}
	witnessed := program("witnessed")
	witnessed.Witness = []string{"w"}
	for _, tc := range []struct {
		name    string
		root    func(t *testing.T) string
		workers int
		checks  []check.Check
		record  string
		copied  bool
	}{
		{"one program", repo, 4, []check.Check{program("a")}, "", false},
		{"a pool of one", repo, 1, []check.Check{program("a"), program("b")}, "", false},
		{"no repository", notRepo, 4, []check.Check{program("a"), program("b")}, "", false},
		{"two programs", repo, 2, []check.Check{program("a"), program("b")}, "", true},
		{"a program and its witness", repo, 2, []check.Check{witnessed}, "", true},
		{"two slow programs", repo, 2, []check.Check{program("a"), program("b")}, `{"a":1000,"b":5000}`, true},
		{"a fast program and a slow one", repo, 2, []check.Check{program("a"), program("b")}, `{"a":999,"b":5000}`, false},
		{"two fast programs", repo, 2, []check.Check{program("a"), program("b")}, `{"a":10,"b":20}`, false},
		{"a record that does not decode", repo, 2, []check.Check{program("a"), program("b")}, `{"a":10,`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root(t)
			if tc.record != "" {
				writeRecord(t, root, tc.record)
			}
			var mu sync.Mutex
			var copied []bool
			e := &Engine{Root: root, workers: tc.workers, Run: func(_ context.Context, at string, c check.Check) check.Result {
				mu.Lock()
				defer mu.Unlock()
				copied = append(copied, !strings.HasPrefix(at, root))
				return check.Result{Check: c, Verdict: check.VerdictTrue}
			}}
			e.Execute(t.Context(), tc.checks)
			if len(copied) == 0 {
				t.Fatal("the runner ran nothing")
			}
			for _, got := range copied {
				if got != tc.copied {
					t.Errorf("ran in a copy: %v, want %v", copied, tc.copied)
					break
				}
			}
		})
	}
}

// writeRecord writes body as the record of durations of the repository at
// root.
func writeRecord(t *testing.T, root, body string) {
	t.Helper()
	home, err := treecopy.Home(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, durationsFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Each run records how long each program ran, and the next run copies only
// the ones that took a second or more: fast checks copy on the first run,
// with no record, and stay in the repository after it, until one of them
// runs slow again. Planted break: the record left unwritten, where each
// run copied.
func TestARunRecordsHowLongEachCheckRan(t *testing.T) {
	root := committed(t, map[string]string{"f.txt": "x"})
	var took time.Duration
	var mu sync.Mutex
	var copied []bool
	e := &Engine{Root: root, workers: 2, Run: func(_ context.Context, at string, c check.Check) check.Result {
		mu.Lock()
		defer mu.Unlock()
		copied = append(copied, at != root)
		return check.Result{Check: c, Verdict: check.VerdictTrue, Duration: took}
	}}
	for i, tc := range []struct {
		took   time.Duration
		copied bool
	}{
		{10 * time.Millisecond, true}, {2 * time.Second, false}, {10 * time.Millisecond, true}, {10 * time.Millisecond, false},
	} {
		took, copied = tc.took, nil
		e.Execute(t.Context(), []check.Check{program("a"), program("b")})
		if !reflect.DeepEqual(copied, []bool{tc.copied, tc.copied}) {
			t.Errorf("run %d copied %v, want %v", i+1, copied, tc.copied)
		}
	}
	home, _ := treecopy.Home(root)
	if got := readFile(t, filepath.Join(home, durationsFile)); got != `{"a":10,"b":10}` {
		t.Errorf("the record: %s", got)
	}
}

// Checks in copies stay within the pool's bound as checks in place do: six
// programs on a pool of two run two at once, and no more than two copies
// are on disk at any time, since each is made and removed in its job's
// slot. Planted break: a copy left on disk after its check, where six
// were there by the end.
func TestCopiesStayWithinThePool(t *testing.T) {
	root := committed(t, map[string]string{"f.txt": "x"})
	var f flight
	var most atomic.Int32
	e := &Engine{Root: root, workers: 2, Run: func(_ context.Context, at string, c check.Check) check.Result {
		f.enter()
		defer f.now.Add(-1)
		on := copiesOf(t, root)
		for n, m := int32(len(on)), most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(30 * time.Millisecond)
		v := check.VerdictTrue
		if at == root {
			v = check.VerdictFalse
		}
		return check.Result{Check: c, Verdict: v}
	}}
	var checks []check.Check
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		checks = append(checks, program(id))
	}
	rep := e.Execute(t.Context(), checks)
	if got := verdictsOf(rep.Results); !reflect.DeepEqual(got, []check.Verdict{"true", "true", "true", "true", "true", "true"}) {
		t.Errorf("verdicts %v, want each check run in a copy", got)
	}
	if n := f.most.Load(); n != 2 {
		t.Errorf("%d checks ran at once on a pool of two", n)
	}
	if n := most.Load(); n > 2 {
		t.Errorf("%d copies were on disk at once on a pool of two", n)
	}
	leftClean(t, root, "")
}

// A copy that cannot be made leaves its check unknown, with why, since the
// check did not run; the witness's result keeps the witness's name. Here
// a check in place before them put a file where the copies go, so the
// tree still reads and no copy can be made. Planted break: the error
// dropped, which ran the check at an empty root.
func TestACopyThatCannotBeMadeIsUnknown(t *testing.T) {
	root := committed(t, map[string]string{".errata/witness/w/f": "x"})
	home, err := treecopy.Home(root)
	if err != nil {
		t.Fatal(err)
	}
	takes := program("takes")
	takes.InPlace = true
	witnessed := program("a")
	witnessed.Witness = []string{"w"}
	e := &Engine{Root: root, workers: 2, Run: func(_ context.Context, _ string, c check.Check) check.Result {
		if c.ID == "takes" {
			if err := os.RemoveAll(home); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(home, nil, 0o600); err != nil {
				t.Error(err)
			}
		}
		return check.Result{Check: c, Verdict: check.VerdictTrue}
	}}
	rep := e.Execute(t.Context(), []check.Check{takes, witnessed})
	for i, name := range []string{"a", "a [witness w]"} {
		r := rep.Results[i+1]
		if r.Name() != name || r.Verdict != check.VerdictUnknown || !strings.HasPrefix(r.Message, "errata could not copy the repository to run the check in, so it did not run: ") {
			t.Errorf("%s: %s %q, want unknown for the copy", r.Name(), r.Verdict, r.Message)
		}
	}
}
