package protocol

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/f-klement/errata/internal/check"
)

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
	res := RunGenerated(check.OnWitness(context.Background(), real), fixture, c)
	if res.Verdict != check.VerdictFalse || res.At.File != "out.md" {
		t.Fatalf("got %s %q, want the fixture's out.md to differ from the real one", res.Verdict, res.Message)
	}
}
