package protocol

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// onWitness is the context of a run on the witness at fixture, below the
// repository's root real, with the fixture held open as the engine holds
// it. It commits every file under real to git's index first, as a fixture
// a witness reads is in the index.
func onWitness(t *testing.T, real, fixture string) context.Context {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = real
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	dir, err := os.OpenRoot(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	ctx, err := core.OnFixture(check.OnWitness(t.Context(), real), fixture, dir)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

// On a witness a program starts in the repository's root, which the
// context holds, with ERRATA_ROOT at the fixture it is given as its root.
// The planted break: Dir at root, the fixture.
func TestRunOnAWitness(t *testing.T) {
	real := t.TempDir()
	fixture := filepath.Join(real, ".errata", "witness", "w")
	if err := os.MkdirAll(fixture, 0o750); err != nil {
		t.Fatal(err)
	}
	c := sh(fmt.Sprintf(`[ "$PWD" = '%s' ] && [ "$ERRATA_ROOT" = '%s' ]`, real, fixture))
	if res := Run(check.OnWitness(context.Background(), real), fixture, c); res.Verdict != check.VerdictTrue {
		t.Fatalf("a witness's program did not start in the real root with ERRATA_ROOT at the fixture: %+v", res)
	}
}

// On a witness a generated check reads its file from the fixture, and its
// generator starts in the repository's root: a generator that prints the
// real root's file differs from the fixture's. The planted breaks: the
// file read from the real root, and the generator started in the fixture,
// each of which held.
func TestRunGeneratedOnAWitness(t *testing.T) {
	real, c := generated(t, []byte("current\n"), "cat out.md")
	fixture := filepath.Join(real, ".errata", "witness", "w")
	if err := os.MkdirAll(fixture, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "out.md"), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := RunGenerated(onWitness(t, real, fixture), fixture, c)
	if res.Verdict != check.VerdictFalse || res.At.File != "out.md" {
		t.Fatalf("got %s %q, want the fixture's out.md to differ from the real one", res.Verdict, res.Message)
	}
}

// A generated check's witness whose fixture holds nothing at generated.file
// is unknown, and its generator does not start: the false a missing file
// gives on the repository would hold the witness, though no break was
// planted. Here the generator fails, so a run would say so instead. A
// fixture whose file is a link out of it is no missing file: the generator
// runs and its failure is the verdict's message, as on the repository.
// Planted breaks: the witness test dropped, which made the missing file
// false; the test placed after the run, which gave the generator's
// failure; every read error taken for a missing file, which gave the
// fixture's message for the link.
func TestRunGeneratedOnAWitnessWithoutItsFile(t *testing.T) {
	real, c := generated(t, []byte("current\n"), "exit 3")
	fixture := filepath.Join(real, ".errata", "witness", "w")
	if err := os.MkdirAll(fixture, 0o750); err != nil {
		t.Fatal(err)
	}
	// The fixture commits another file, so the index lists it.
	if err := os.WriteFile(filepath.Join(fixture, "other.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	res := RunGenerated(onWitness(t, real, fixture), fixture, c)
	want := "its fixture .errata/witness/w holds nothing at out.md, the file the generated check compares, so the check could not be shown to fail; commit the file with a break planted in it"
	if res.Verdict != check.VerdictUnknown || res.Message != want {
		t.Errorf("no out.md in the fixture: %s %q, want unknown %q", res.Verdict, res.Message, want)
	}
	if err := os.Symlink(filepath.Join(real, "out.md"), filepath.Join(fixture, "out.md")); err != nil {
		t.Fatal(err)
	}
	res = RunGenerated(onWitness(t, real, fixture), fixture, c)
	if res.Verdict != check.VerdictUnknown || !strings.HasPrefix(res.Message, "the generator failed: exit status 3") {
		t.Errorf("out.md a link out of the fixture: %s %q, want unknown with the generator's failure", res.Verdict, res.Message)
	}
}
