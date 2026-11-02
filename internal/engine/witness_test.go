package engine

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/provider"
)

// fixtures makes the directory of each witness of names below a new
// root, a git repository whose index lists each fixture's file .keep.
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
		if err := os.WriteFile(filepath.Join(root, ".errata", "witness", n, ".keep"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "init", "-q")
	track(t, root)
	return root
}

// track adds every file below root to git's index.
func track(t *testing.T, root string) {
	t.Helper()
	git(t, root, "add", "-A")
}

// git runs git with args in dir, away from the machine's config.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
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

// The engine hands the runner the fixture it checked, held open: a
// symlink put at .errata once the check is done, to a tree whose witness
// holds another x.pin, moves no read of the rule's. Planted breaks: the
// run given no held fixture, whose read was then refused; core's openTree
// opening the fixture by its path, which read the symlink's target.
func TestExecuteReadsTheFixtureItChecked(t *testing.T) {
	root := fixtures(t, "w")
	write := func(p, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, ".errata", "witness", "w", "x.pin"), "checked")
	track(t, root)
	other := t.TempDir()
	write(filepath.Join(other, "witness", "w", "x.pin"), "outside")
	var read string
	e := &Engine{Root: root, Run: func(ctx context.Context, at string, c check.Check) check.Result {
		if _, witness := check.RealRoot(ctx); !witness {
			return check.Result{Check: c, Verdict: check.VerdictTrue}
		}
		if err := os.Rename(filepath.Join(root, ".errata"), filepath.Join(root, "moved")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, filepath.Join(root, ".errata")); err != nil {
			t.Fatal(err)
		}
		data, err := core.Source{File: "x.pin", Limit: 100}.Read(ctx, at)
		read = string(data)
		if err != nil {
			read = err.Error()
		}
		return check.Result{Check: c, Verdict: check.VerdictFalse}
	}}
	e.Execute(context.Background(), []check.Check{{ID: "c", Scope: check.ScopeRepo, Witness: []string{"w"}}})
	if read != "checked" {
		t.Errorf("the rule read %q, want the checked fixture's x.pin", read)
	}
}

// enter opens a part of a fixture's path only when the directory it
// opened is the one it looked at: here the look is handed b's, as when a
// symlink to b was swapped in at a between the look and the open. Planted
// break: the os.SameFile test dropped.
func TestEnterRefusesADirectoryOtherThanTheOneLookedAt(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	cur, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close()
	if _, err := enter(cur, "a", func(string) (fs.FileInfo, error) { return cur.Lstat("b") }); !errors.Is(err, errMoved) {
		t.Errorf("a opened while b was looked at: %v, want errMoved", err)
	}
	next, err := enter(cur, "a", cur.Lstat)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	_ = next.Close()
}

// A witness whose fixture's path git's index also lists in another case
// is unknown before its check runs, on Linux too: macOS and Windows open
// .Errata/witness/w as .errata/witness/w, so a run there would read the
// files of both as the fixture, which no clone on Linux holds. The rule is
// core's (Index.Below), and the engine opens the fixture as spelt. Planted
// break: Below's test of another case dropped, which ran the check.
func TestExecuteRefusesAFixtureTheIndexListsInAnotherCase(t *testing.T) {
	root := fixtures(t, "w")
	if err := os.MkdirAll(filepath.Join(root, ".Errata", "witness", "w"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".Errata", "witness", "w", "x.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".errata", "witness", "w", "x.yaml")); err == nil {
		t.Skip("this file system ignores case")
	}
	track(t, root)
	var calls []call
	e := &Engine{Root: root, Run: byRoot(&calls, map[string]check.Verdict{"w": check.VerdictFalse})}
	rep := e.Execute(context.Background(), []check.Check{{ID: "c", Scope: check.ScopeRepo, Witness: []string{"w"}}})
	want := `git's index lists ".Errata/witness/w/x.yaml", below the fixture ".errata/witness/w" in another case, and macOS and Windows open the two as one directory, so the fixture would not read there as a clone holds it; rename it to ".errata/witness/w/x.yaml"`
	if r := rep.Results[1]; r.Verdict != check.VerdictUnknown || r.Message != want || len(calls) != 1 {
		t.Errorf("a fixture listed in two cases: %s %q after %d runs, want unknown %q after the check's own", r.Verdict, r.Message, len(calls), want)
	}
}

// A witness whose fixture git's index lists no file below is unknown for
// every kind of check, before its check runs: a clone in CI holds none of
// its files. Here the fixture is on disk, untracked, and a rule reading its
// file: would hold. Planted break: the refusal of core.OnFixture ignored,
// which ran the check on the untracked fixture.
func TestExecuteRefusesAFixtureTheIndexDoesNotList(t *testing.T) {
	root := fixtures(t)
	if err := os.MkdirAll(filepath.Join(root, ".errata", "witness", "u"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".errata", "witness", "u", "x.yaml"), []byte("v: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x.yaml"), []byte("v: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	track(t, root)
	git(t, root, "rm", "-q", "--cached", ".errata/witness/u/x.yaml")
	var calls []call
	e := &Engine{Root: root, Run: byRoot(&calls, map[string]check.Verdict{"u": check.VerdictFalse})}
	rep := e.Execute(context.Background(), []check.Check{{ID: "c", Scope: check.ScopeRepo, Witness: []string{"u"}}})
	want := `git's index lists no file below the fixture ".errata/witness/u"; a clone holds only the files git tracks, so commit the fixture's files`
	if r := rep.Results[1]; r.Verdict != check.VerdictUnknown || r.Message != want || len(calls) != 1 {
		t.Errorf("an untracked fixture: %s %q after %d runs, want unknown %q after the check's own", r.Verdict, r.Message, len(calls), want)
	}
}

// On a fixture that commits some of its files, a read of one the index
// does not list finds nothing, as the read in a clone would: Read refuses
// it, Resolved refuses it, and Stat, which exists reads, calls it not
// there, while a committed file reads. A symlink the fixture does not
// commit is refused before it is followed, though its target is
// committed: link.yaml to a.yaml, and dir to e, which commits y.yaml. A
// committed link, kept.yaml to a.yaml, is followed. Where the entry the
// index lacks is not the file the spec names, the refusal names it as the
// one to commit: the link dir, the untracked link b that the committed
// chain.yaml leads to, and the untracked x.yaml that the committed to.yaml
// leads to. Planted breaks:
// tree.indexed passing every file, which read the untracked x.yaml;
// resolve's check of a link dropped, which read both untracked links;
// resolvedIn's check dropped, which resolved x.yaml; the refusal built
// from the spec's file alone, which said to commit the committed
// chain.yaml.
func TestExecuteReadsOnlyTheFilesTheFixtureCommits(t *testing.T) {
	root := fixtures(t, "w")
	w := filepath.Join(root, ".errata", "witness", "w")
	if err := os.Mkdir(filepath.Join(w, "e"), 0o750); err != nil {
		t.Fatal(err)
	}
	for f, text := range map[string]string{"a.yaml": "a\n", "x.yaml": "x\n", "e/y.yaml": "y\n"} {
		if err := os.WriteFile(filepath.Join(w, f), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"kept.yaml": "a.yaml", "link.yaml": "a.yaml", "dir": "e", "chain.yaml": "b", "b": "a.yaml", "to.yaml": "x.yaml"} {
		if err := os.Symlink(target, filepath.Join(w, link)); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "add", ".errata/witness/w/a.yaml", ".errata/witness/w/e/y.yaml", ".errata/witness/w/kept.yaml", ".errata/witness/w/chain.yaml", ".errata/witness/w/to.yaml")
	read := map[string]string{}
	var stat, resolved string
	e := &Engine{Root: root, Run: func(ctx context.Context, at string, c check.Check) check.Result {
		if _, witness := check.RealRoot(ctx); !witness {
			return check.Result{Check: c, Verdict: check.VerdictTrue}
		}
		for _, f := range []string{"a.yaml", "x.yaml", "kept.yaml", "link.yaml", "dir/y.yaml", "chain.yaml", "to.yaml"} {
			data, err := core.Source{File: f, Limit: 100}.Read(ctx, at)
			read[f] = string(data) + said(err)
		}
		at2, err := core.Source{File: "x.yaml"}.Resolved(ctx, at)
		resolved = at2 + said(err)
		var gone []string
		for _, f := range []string{"x.yaml", "link.yaml", "dir/y.yaml"} {
			_, err := core.Source{File: f}.Stat(ctx, at)
			gone = append(gone, strconv.FormatBool(errors.Is(err, fs.ErrNotExist)))
		}
		stat = strings.Join(gone, " ")
		return check.Result{Check: c, Verdict: check.VerdictFalse}
	}}
	e.Execute(context.Background(), []check.Check{{ID: "c", Scope: check.ScopeRepo, Witness: []string{"w"}}})
	refused := func(f string) string {
		return "read " + f + ": " + f + " is not in git's index below the fixture, so a clone of the repository does not hold it; commit it"
	}
	needs := func(f, entry string) string {
		return "read " + f + ": " + f + " needs " + entry + ", which is not in git's index below the fixture, so a clone of the repository does not hold it; commit " + entry
	}
	for f, want := range map[string]string{
		"a.yaml":     "a\n<nil>",
		"kept.yaml":  "a\n<nil>",
		"x.yaml":     refused("x.yaml"),
		"link.yaml":  refused("link.yaml"),
		"dir/y.yaml": needs("dir/y.yaml", "dir"),
		"chain.yaml": needs("chain.yaml", "b"),
		"to.yaml":    needs("to.yaml", "x.yaml"),
	} {
		if read[f] != want {
			t.Errorf("Read %s: %q, want %q", f, read[f], want)
		}
	}
	if want := refused("x.yaml"); resolved != want {
		t.Errorf("Resolved of the untracked x.yaml: %q, want %q", resolved, want)
	}
	if stat != "true true true" {
		t.Errorf("Stat of x.yaml, link.yaml and dir/y.yaml is not there: %s, want true for each", stat)
	}
}

// A fixture stands for a repository, so a file: into a fixture nested in
// it reads the nested fixture's committed file, as a real run's file:
// into .errata/witness reads it; only a glob passes over the nested
// fixture. Planted break: the held set built from the glob list, which
// refused the committed x.yaml as not in the index.
func TestExecuteReadsAFileOfANestedFixture(t *testing.T) {
	root := fixtures(t, "w")
	nested := filepath.Join(root, ".errata", "witness", "w", ".errata", "witness", "b")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "x.yaml"), []byte("v: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	track(t, root)
	var read string
	e := &Engine{Root: root, Run: func(ctx context.Context, at string, c check.Check) check.Result {
		if _, witness := check.RealRoot(ctx); !witness {
			return check.Result{Check: c, Verdict: check.VerdictTrue}
		}
		data, err := core.Source{File: ".errata/witness/b/x.yaml", Limit: 100}.Read(ctx, at)
		read = string(data) + said(err)
		return check.Result{Check: c, Verdict: check.VerdictFalse}
	}}
	e.Execute(context.Background(), []check.Check{{ID: "c", Scope: check.ScopeRepo, Witness: []string{"w"}}})
	if read != "v: 2\n<nil>" {
		t.Errorf("the nested fixture's committed x.yaml: %q", read)
	}
}

// said is err's text, or <nil>.
func said(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
