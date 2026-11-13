package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/config"
)

// rules are checks of the rule kind with ids, which the engine runs on its
// pool; the runner of a test decides what each returns.
func rules(ids ...string) []check.Check {
	out := make([]check.Check, len(ids))
	for i, id := range ids {
		out[i] = check.Check{ID: id, Scope: check.ScopeRepo, Kind: &check.Rule{}}
	}
	return out
}

// The report keeps the order of the config, each check followed by its
// witness, whatever order the jobs finish in: here every job waits for the
// last check to run, so the last finishes first. Planted breaks: each
// result appended as it finished, which put last first; a pool of one
// worker, where the waits ran out.
func TestThePoolReportsInConfigOrder(t *testing.T) {
	root := fixtures(t, "w")
	checks := rules("first", "second", "last")
	checks[0].Witness = []string{"w"}
	ran := make(chan struct{})
	e := &Engine{Root: root, workers: 4, Run: func(_ context.Context, at string, c check.Check) check.Result {
		said := c.ID + " on " + filepath.Base(at)
		if c.ID == "last" {
			close(ran)
			return check.Result{Check: c, Verdict: check.VerdictTrue, Message: said}
		}
		select {
		case <-ran:
		case <-time.After(10 * time.Second):
			said = c.ID + " waited out"
		}
		v := check.VerdictTrue
		if at != root {
			v = check.VerdictFalse
		}
		return check.Result{Check: c, Verdict: v, Message: said}
	}}
	rep := e.Execute(t.Context(), checks)
	var got []string
	for _, r := range rep.Results {
		got = append(got, r.Name()+": "+r.Message)
	}
	want := []string{
		"first: first on " + filepath.Base(root),
		"first [witness w]: first is false on its witness w, as it must be",
		"second: second on " + filepath.Base(root),
		"last: last on " + filepath.Base(root),
	}
	if !slices.Equal(got, want) {
		t.Errorf("the report:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// flight counts the jobs a runner is in, and the most at once.
type flight struct {
	now, most, started atomic.Int32
}

// enter counts one more job in flight.
func (f *flight) enter() {
	n := f.now.Add(1)
	f.started.Add(1)
	for m := f.most.Load(); n > m && !f.most.CompareAndSwap(m, n); m = f.most.Load() {
	}
}

// No more jobs run at once than the pool has workers, and that many do:
// the first three wait for each other, then hold, while nine more wait
// their turn. Planted breaks: a goroutine for each job, which ran twelve
// at once; width ignoring workers, where GOMAXPROCS set the bound.
func TestThePoolRunsAtMostItsWidth(t *testing.T) {
	var f flight
	e := &Engine{workers: 3, Run: func(_ context.Context, _ string, c check.Check) check.Result {
		f.enter()
		defer f.now.Add(-1)
		if c.ID < "d" {
			for deadline := time.Now().Add(5 * time.Second); f.started.Load() < 3 && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			time.Sleep(20 * time.Millisecond)
		}
		return check.Result{Check: c, Verdict: check.VerdictTrue}
	}}
	rep := e.Execute(t.Context(), rules("a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"))
	if len(rep.Results) != 12 {
		t.Fatalf("%d results for 12 checks", len(rep.Results))
	}
	if most := f.most.Load(); most != 3 {
		t.Errorf("%d jobs ran at once, want 3, the pool's width", most)
	}
}

// width is GOMAXPROCS unless a test sets workers. Planted break: width
// always 1, which kept the run on one core.
func TestThePoolIsGOMAXPROCSWide(t *testing.T) {
	if got, want := (&Engine{}).width(), runtime.GOMAXPROCS(0); got != want {
		t.Errorf("width %d, want GOMAXPROCS %d", got, want)
	}
	if got := (&Engine{workers: 5}).width(); got != 5 {
		t.Errorf("width %d with 5 workers, want 5", got)
	}
}

// events is what a runner saw, in order, under a lock.
type events struct {
	mu  sync.Mutex
	log []string
}

func (v *events) add(s string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.log = append(v.log, s)
}

// A program check, and a check of a kind the engine has no row for, runs
// alone: every job before it has ended when it starts, and none after it
// starts before it ends, since a program may write what a rule reads. The
// rules on either side still run at once. Planted break: alone always
// false, which started the program beside the rules before it.
func TestAProgramRunsAlone(t *testing.T) {
	checks := rules("a", "b", "c", "d", "e", "f")
	checks = slices.Insert(checks, 2, check.Check{ID: "program", Scope: check.ScopeRepo, Kind: &check.Program{}})
	checks = slices.Insert(checks, 5, check.Check{ID: "kindless", Scope: check.ScopeRepo})
	var log events
	var f flight
	e := &Engine{workers: 4, Run: func(_ context.Context, _ string, c check.Check) check.Result {
		f.enter()
		defer f.now.Add(-1)
		log.add("start " + c.ID)
		time.Sleep(20 * time.Millisecond)
		log.add("end " + c.ID)
		return check.Result{Check: c, Verdict: check.VerdictTrue}
	}}
	e.Execute(t.Context(), checks)
	for _, alone := range []string{"program", "kindless"} {
		at := slices.Index(log.log, "start "+alone)
		if at < 0 || at+1 >= len(log.log) || log.log[at+1] != "end "+alone {
			t.Errorf("%s did not run alone:\n%s", alone, strings.Join(log.log, "\n"))
		}
	}
	if f.most.Load() < 2 {
		t.Errorf("the rules ran one at a time:\n%s", strings.Join(log.log, "\n"))
	}
}

// A pool of one worker runs the jobs one at a time in the order of the
// config, as the engine did before the pool, so a run on one core reads
// in the same order. Planted break: the jobs fed to the pool last first.
func TestAPoolOfOneRunsInOrder(t *testing.T) {
	var log events
	e := &Engine{workers: 1, Run: func(_ context.Context, _ string, c check.Check) check.Result {
		log.add(c.ID)
		return check.Result{Check: c, Verdict: check.VerdictTrue}
	}}
	e.Execute(t.Context(), rules("a", "b", "c", "d"))
	if want := []string{"a", "b", "c", "d"}; !slices.Equal(log.log, want) {
		t.Errorf("ran %v, want %v", log.log, want)
	}
}

// sharing is a config whose rules read the same files: regexes over **,
// a marker walk two checks share, a YAML read, a CEL assert over one of
// the regexes, and a witness, each on files the others read.
const sharing = `checks:
  - id: ports-agree
    reason: r
    scope: repo
    extract:
      doc: {marker: {files: ['docs/**']}}
      chart: {yaml: {file: values.yaml, path: port}}
    agree: [doc, chart]
  - id: hosts-agree
    reason: r
    scope: repo
    extract:
      doc: {marker: {files: ['docs/**']}}
      chart: {yaml: {file: values.yaml, path: host}}
    agree: [doc, chart]
  - id: no-file-says-todo
    reason: r
    scope: repo
    extract:
      todo: {regex: {files: ['**'], pattern: 'TODO (\w+)'}}
    allow_empty: [todo]
    assert: size(todo) == 0
    witness: [a-todo]
  - id: every-file-names-a-port
    reason: r
    scope: repo
    extract:
      ports: {regex: {files: ['**'], pattern: 'port:? *(\d+)'}}
    assert: ports.all(p, p == '8080')
  - id: hosts-are-lowercase
    reason: r
    scope: repo
    extract:
      hosts: {regex: {files: ['**'], pattern: 'host:? *(\S+)'}}
    assert: hosts.all(h, h.lowerAscii() == h)
`

// The checks of a config that share files, a marker walk and the run's
// shared read give, on a pool of eight, the same results as one at a time,
// field by field but the time; under -race the test also shows that
// nothing they share is written by two at once. Planted break: core's
// Tracked reading the index under a plain flag, which -race reported.
func TestChecksThatShareFilesGiveTheSameResultsOnThePool(t *testing.T) {
	files := map[string]string{
		"errata.yaml":                     sharing,
		"values.yaml":                     "port: 8080\nhost: api.example\n",
		".errata/witness/a-todo/notes.md": "TODO later\n",
		"docs/ops.md":                     "port <!-- errata: ports-agree -->8080<!-- /errata --> on <!-- errata: hosts-agree -->api.example<!-- /errata -->\n",
		"docs/more.md":                    "port: 8080 and host: api.example\n",
		"src/main.go":                     "package main // port 8080\n",
		"src/later.go":                    "package main // TODO soon\n",
	}
	for i := range 20 {
		files[fmt.Sprintf("src/f%02d.txt", i)] = fmt.Sprintf("host: api.example\nport: 8080\nline %d\n", i)
	}
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
	git(t, root, "init", "-q")
	track(t, root)
	cfg, err := config.Load(root, filepath.Join(root, "errata.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(workers int) []check.Result {
		e := New(root, nil)
		e.workers = workers
		results := e.Execute(t.Context(), cfg.Checks).Results
		for i := range results {
			results[i].Duration = 0
		}
		return results
	}
	alone, pooled := run(1), run(8)
	if len(alone) < len(cfg.Checks) {
		t.Fatalf("%d results for %d checks", len(alone), len(cfg.Checks))
	}
	if !reflect.DeepEqual(alone, pooled) {
		for i := range alone {
			if !reflect.DeepEqual(alone[i], pooled[i]) {
				t.Errorf("%s: one at a time %+v, on the pool %+v", alone[i].Name(), alone[i], pooled[i])
			}
		}
	}
	if verdicts := fmt.Sprint(verdictsOf(alone)); verdicts != "[true true false true true true true]" {
		t.Errorf("verdicts %s, want the todo rule false and the rest true", verdicts)
	}
}

// verdictsOf is each result's verdict, in order.
func verdictsOf(results []check.Result) []check.Verdict {
	out := make([]check.Verdict, len(results))
	for i, r := range results {
		out[i] = r.Verdict
	}
	return out
}

// writer is a config whose program check writes two files between rules,
// as a program may (ADR 0011): the first rule reads the tree before it,
// and the last two read what it wrote, one of them the file the first
// rule read and the run kept.
const writer = `checks:
  - id: before
    reason: r
    scope: repo
    extract:
      port: {yaml: {file: values.yaml, path: port}}
    assert: port.all(p, p == '8080')
  - id: writes
    reason: r
    scope: repo
    run: [sh, -c, 'sleep 0.2; printf "port: 9090\n" > values.yaml; printf "port: 9090\n" > written.yaml']
    timeout: 10s
  - id: after
    reason: r
    scope: repo
    extract:
      port: {yaml: {file: written.yaml, path: port}}
    assert: port.all(p, p == '9090')
  - id: rereads
    reason: r
    scope: repo
    extract:
      port: {yaml: {file: values.yaml, path: port}}
    assert: port.all(p, p == '9090')
`

// A program check that writes the tree is a barrier on the pool: the rule
// before it reads values.yaml as committed, and the rules after it read
// what the program wrote, as when every check ran in turn (the probe of
// the readonce review). The program sleeps first, so a rule run beside it
// reads before it writes. after reads a file no rule read before, so it
// fails only on a missing barrier; rereads reads values.yaml again, which
// the run kept from before, so it fails also when the run keeps it past
// the program. Planted breaks: alone always false, where after read the
// port 1 written.yaml holds as committed; runAll without check.Wrote,
// where rereads got the kept 8080.
func TestARuleAfterAProgramReadsWhatItWrote(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"errata.yaml": writer, "values.yaml": "port: 8080\n", "written.yaml": "port: 1\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "init", "-q")
	track(t, root)
	cfg, err := config.Load(root, filepath.Join(root, "errata.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	e := New(root, nil)
	e.workers = 4
	for _, r := range e.Execute(t.Context(), cfg.Checks).Results {
		if r.Verdict != check.VerdictTrue {
			t.Errorf("%s is %s: %s", r.Name(), r.Verdict, r.Message)
		}
	}
}

// A check that panics on the pool panics on the goroutine that called
// Execute, once the pool's workers are done, so the caller's defers run as
// they did when every check ran on it: cli's removes the --output temp
// files. The panic carries the check's value and the worker's stack.
// Planted break: the worker without its recover, where the panic ended
// the test binary in the worker's goroutine before the recover here ran.
func TestAPanicOnThePoolReachesTheCaller(t *testing.T) {
	var ran atomic.Int32
	e := &Engine{workers: 2, Run: func(_ context.Context, _ string, c check.Check) check.Result {
		ran.Add(1)
		if c.ID == "b" {
			panic(errBoom)
		}
		return check.Result{Check: c, Verdict: check.VerdictTrue}
	}}
	defer func() {
		v := recover()
		err, ok := v.(error)
		if !ok || !errors.Is(err, errBoom) {
			t.Fatalf("the caller recovered %v, want the check's panic", v)
		}
		if !strings.Contains(err.Error(), "boom\n\nin a worker of the engine's pool:\ngoroutine ") {
			t.Errorf("the panic says %q, want the value and the worker's stack", err)
		}
		if n := ran.Load(); n != 3 {
			t.Errorf("%d checks ran, want 3: the pool ends its stretch before it panics again", n)
		}
	}()
	e.Execute(t.Context(), rules("a", "b", "c"))
	t.Fatal("Execute returned")
}

// errBoom is what a check panics with in a test.
var errBoom = errors.New("boom")

// markersAround is a config whose program check rewrites a marked file and
// the value its marker must agree with, between two marker rules.
const markersAround = `checks:
  - id: before
    reason: r
    scope: repo
    extract:
      doc: {marker: {files: ['docs/**']}}
      chart: {yaml: {file: values.yaml, path: port}}
    agree: [doc, chart]
  - id: writes
    reason: r
    scope: repo
    run: [sh, -c, 'printf "<!-- errata: before -->9090<!-- /errata --> <!-- errata: after -->9090<!-- /errata -->\n" > docs/a.md; printf "port: 9090\n" > values.yaml']
    timeout: 10s
  - id: after
    reason: r
    scope: repo
    extract:
      doc: {marker: {files: ['docs/**']}}
      chart: {yaml: {file: values.yaml, path: port}}
    agree: [doc, chart]
`

// A marker rule after a program that rewrote its marked file reads the new
// marker, at one worker and at four: check.Wrote drops what the run kept
// through check.Shared, the marker walk's read of git's index and of each
// file's markers, besides the files the run shares (pool review, P2).
// Planted break: Wrote keeping the run's results, where after read the
// new values.yaml against the old marker, "the values differ".
func TestAMarkerRuleAfterAProgramReadsWhatItWrote(t *testing.T) {
	for _, workers := range []int{1, 4} {
		root := t.TempDir()
		files := map[string]string{
			"errata.yaml": markersAround,
			"values.yaml": "port: 8080\n",
			"docs/a.md":   "<!-- errata: before -->8080<!-- /errata --> <!-- errata: after -->8080<!-- /errata -->\n",
		}
		for name, body := range files {
			p := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		git(t, root, "init", "-q")
		track(t, root)
		cfg, err := config.Load(root, filepath.Join(root, "errata.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		e := New(root, nil)
		e.workers = workers
		for _, r := range e.Execute(t.Context(), cfg.Checks).Results {
			if r.Verdict != check.VerdictTrue {
				t.Errorf("workers %d: %s is %s: %s", workers, r.Name(), r.Verdict, r.Message)
			}
		}
	}
}
