package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/provider"
)

// fixtures makes the directory of each witness of names below a new root.
func fixtures(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".errata", "witness"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, ".errata", "witness", n), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// call is one run the engine asked of the runner: the root it gave and the
// real root its context held.
type call struct{ root, real string }

// byRoot returns, for each root, the verdict of verdicts under the root's
// last segment, true on the repository, and records each call.
func byRoot(calls *[]call, verdicts map[string]check.Verdict) Runner {
	return func(ctx context.Context, root string, c check.Check) check.Result {
		real, _ := check.RealRoot(ctx)
		*calls = append(*calls, call{root, real})
		v, ok := verdicts[filepath.Base(root)]
		if !ok {
			v = check.VerdictTrue
		}
		return check.Result{Check: c, Verdict: v, Message: "said " + filepath.Base(root)}
	}
}

// Each witness runs after its check, with the fixture's absolute path as
// the root and the repository's in the context, and turns the check's
// verdict there into its own: false holds, true is an erratum naming both,
// unknown stays unknown with the check's message. Planted breaks: the
// witness given the real root; the check's verdict kept; an unknown held;
// no real root in the context.
func TestExecuteRunsEachWitness(t *testing.T) {
	root := fixtures(t, "held", "erratum", "unknown")
	var calls []call
	e := &Engine{Root: root, Run: byRoot(&calls, map[string]check.Verdict{
		"held": check.VerdictFalse, "erratum": check.VerdictTrue, "unknown": check.VerdictUnknown,
	})}
	c := check.Check{ID: "c", Scope: check.ScopeRepo, Witness: []string{"held", "erratum", "unknown"}}
	rep := e.Execute(context.Background(), []check.Check{c})

	fixture := func(n string) string { return filepath.Join(root, ".errata", "witness", n) }
	want := []call{{root, ""}, {fixture("held"), root}, {fixture("erratum"), root}, {fixture("unknown"), root}}
	if len(calls) != len(want) {
		t.Fatalf("runner saw %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("run %d: %+v, want %+v", i, calls[i], want[i])
		}
	}
	got := rep.Results
	if len(got) != 4 || got[0].Witness != "" {
		t.Fatalf("results %+v", got)
	}
	for i, w := range []struct {
		witness string
		verdict check.Verdict
		message string
	}{
		{"held", check.VerdictTrue, "c is false on its witness held, as it must be"},
		{"erratum", check.VerdictFalse, "c held on its witness erratum, so it could not be shown to fail"},
		{"unknown", check.VerdictUnknown, "said unknown"},
	} {
		r := got[i+1]
		if r.Witness != w.witness || r.Verdict != w.verdict || r.Message != w.message || r.Check.ID != "c" {
			t.Errorf("witness %s: %+v, want verdict %s and message %q", w.witness, r, w.verdict, w.message)
		}
	}
	if held, ran := rep.WitnessesHeld(); held != 1 || ran != 3 {
		t.Errorf("held %d of %d, want 1 of 3", held, ran)
	}
}

// A witness whose fixture is not there, is a file, or is a symlink, here
// one that leads out of the repository, is unknown without a run. Planted
// breaks: fixtureDir returning nil; the IsDir test dropped; the symlink
// test dropped.
func TestExecuteRefusesAFixtureThatIsNoDirectory(t *testing.T) {
	root := fixtures(t)
	if err := os.WriteFile(filepath.Join(root, ".errata", "witness", "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".errata", "witness", "out")); err != nil {
		t.Fatal(err)
	}
	var calls []call
	e := &Engine{Root: root, Run: byRoot(&calls, nil)}
	c := check.Check{ID: "c", Scope: check.ScopeRepo, Witness: []string{"gone", "file", "out"}}
	rep := e.Execute(context.Background(), []check.Check{c})
	if len(calls) != 1 {
		t.Errorf("runner saw %v, want the check's own run only", calls)
	}
	for i, want := range []string{
		"its fixture .errata/witness/gone is not in the repository: ",
		"its fixture .errata/witness/file is no directory",
		"its fixture .errata/witness/out is reached through the symlink .errata/witness/out",
	} {
		r := rep.Results[i+1]
		if r.Verdict != check.VerdictUnknown || !strings.HasPrefix(r.Message, want) {
			t.Errorf("%s: %s %q, want unknown %q", r.Witness, r.Verdict, r.Message, want)
		}
	}
}

// A relative root is made absolute for the fixture, which a program gets
// as ERRATA_ROOT. The planted break: the root used as given.
func TestExecuteGivesAWitnessAnAbsoluteRoot(t *testing.T) {
	root := fixtures(t, "w")
	t.Chdir(root)
	var calls []call
	e := &Engine{Root: ".", Run: byRoot(&calls, map[string]check.Verdict{"w": check.VerdictFalse})}
	e.Execute(context.Background(), []check.Check{{ID: "c", Scope: check.ScopeRepo, Witness: []string{"w"}}})
	if len(calls) != 2 || !filepath.IsAbs(calls[1].root) || !filepath.IsAbs(calls[1].real) {
		t.Errorf("runner saw %v, want absolute roots for the witness", calls)
	}
}

// A skipped check runs no witness, and a witness takes a duration. The
// planted breaks: witnesses run before the skip's continue; the duration's
// defer dropped.
func TestExecuteWitnessesOfASkipAndTheirDuration(t *testing.T) {
	root := fixtures(t, "w")
	var calls []call
	slow := func(ctx context.Context, r string, c check.Check) check.Result {
		time.Sleep(time.Millisecond)
		return byRoot(&calls, nil)(ctx, r, c)
	}
	e := &Engine{Root: root, Run: slow, Providers: []provider.Provider{}}
	rep := e.Execute(context.Background(), []check.Check{
		{ID: "live", Scope: check.ScopeLive, Witness: []string{"w"}},
		{ID: "repo", Scope: check.ScopeRepo, Witness: []string{"w"}},
	})
	if len(rep.Results) != 3 || len(calls) != 2 {
		t.Fatalf("results %+v, calls %v", rep.Results, calls)
	}
	if d := rep.Results[2].Duration; d < time.Millisecond {
		t.Errorf("the witness took %s, want at least 1ms", d)
	}
}

// A fixture reached through a symlinked .errata or .errata/witness is
// refused, even one that leads to a directory inside the repository, where
// the index lists the fixtures under a path a real run does not pass over.
// The planted break: only the fixture itself looked at, as before review
// finding 1.
func TestExecuteRefusesAFixtureBehindASymlink(t *testing.T) {
	for _, l := range []struct{ link, inside string }{
		{".errata", "witness/w"},
		{".errata/witness", "w"},
	} {
		link, inside := l.link, l.inside
		root := t.TempDir()
		target := filepath.Join(root, "elsewhere")
		if err := os.MkdirAll(filepath.Join(target, inside), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, link)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
		var calls []call
		e := &Engine{Root: root, Run: byRoot(&calls, map[string]check.Verdict{"w": check.VerdictFalse})}
		rep := e.Execute(context.Background(), []check.Check{{ID: "c", Scope: check.ScopeRepo, Witness: []string{"w"}}})
		want := "its fixture .errata/witness/w is reached through the symlink " + link
		if r := rep.Results[1]; r.Verdict != check.VerdictUnknown || r.Message != want || len(calls) != 1 {
			t.Errorf("%s a symlink: %s %q after %d runs, want unknown %q after the check's own", link, r.Verdict, r.Message, len(calls), want)
		}
	}
}
