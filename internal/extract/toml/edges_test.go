package toml

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The tests in this file each pin a boundary that no other test reached:
// Gremlins changed the comparison or the arithmetic there and every test
// still passed. Each row names the edge it holds.

// A header through an array of tables counts the array's element as a level,
// so a key in a table there reaches maxDepth and one more part is refused.
func TestTOMLDepthThroughAnArrayOfTables(t *testing.T) {
	at := "[[a]]\n[a." + keys(core.MaxDepth-3) + "]\nb = 1\n"
	got, err := run(t, tomlSpec("a[0]."+keys(core.MaxDepth-3)+".b"), extracttest.Repo(t, map[string]string{"config.toml": at}))
	if err != nil || extracttest.Texts(got) != "1@3" {
		t.Fatalf("at maxDepth: got %s, %v", extracttest.Texts(got), err)
	}
	_, err = run(t, tomlSpec("a"), extracttest.Repo(t, map[string]string{"config.toml": "[[a]]\n[a." + keys(core.MaxDepth-2) + "]\nb = 1\n"}))
	extracttest.WantError(t, err, "config.toml: line 3: nested more than 1000 levels deep, the limit")
}

// An offset's two digits run from 0 to 9 at either place, and a syntax error
// at the first byte of a file, or at its end, still names its line.
func TestTOMLEdges(t *testing.T) {
	for _, body := range []string{"a = 2023-01-01T00:00:00+09:59\n", "a = 2023-01-01T00:00:00-10:00\n", "a = 2023-01-01T00:00:00+00:00\n"} {
		if _, err := run(t, tomlSpec("a"), extracttest.Repo(t, map[string]string{"config.toml": body})); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
	_, err := run(t, tomlSpec("a"), extracttest.Repo(t, map[string]string{"config.toml": "=1\n"}))
	if err == nil || !strings.HasPrefix(err.Error(), "config.toml: toml: line 1: ") {
		t.Errorf("error at the first byte: %v", err)
	}
	_, err = Parse([]byte("a = 1\nb = "))
	if err == nil || err.Error() != "toml: line 2: expected value, not end of input" {
		t.Errorf("error at the end of the input: %v", err)
	}
}
