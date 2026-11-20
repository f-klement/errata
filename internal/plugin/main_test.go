package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
)

// fakeBin is the fake plugin, testdata/fake, built once for the package's
// tests.
var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "errata-fake-plugin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeBin = filepath.Join(dir, "fake")
	if out, err := exec.CommandContext(context.Background(), "go", "build", "-o", fakeBin, "./testdata/fake").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the fake plugin: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// FakeBinary is the fake plugin, for the package's external tests.
func FakeBinary() string { return fakeBin }

// sumOf is the sha256 of the file at path, in hex.
func sumOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// placeFake copies the fake plugin to the build path of name at 0.1.0 for
// this platform below base, and returns its sum.
func placeFake(t *testing.T, base, name string) string {
	t.Helper()
	sum := sumOf(t, fakeBin)
	path := BuildPath(base, name, "0.1.0", Platform(), sum)
	data, err := os.ReadFile(fakeBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return sum
}

// pinLine is a lock line that pins name at 0.1.0 for this platform at sum.
func pinLine(name, sum string) string {
	return name + " 0.1.0 " + Platform() + " sha256:" + sum
}

// newRepo is a repository holding files, with the fake vendored under
// each of names and a lock that pins it.
func newRepo(t *testing.T, files map[string]string, names ...string) string {
	t.Helper()
	root := t.TempDir()
	var lock []string
	for _, n := range names {
		lock = append(lock, pinLine(n, placeFake(t, filepath.Join(root, VendorDir), n)))
	}
	writeFile(t, root, LockFile, strings.Join(lock, "\n")+"\n")
	for name, text := range files {
		writeFile(t, root, name, text)
	}
	gitAdd(t, root)
	return root
}

// gitAdd tracks every file under root in a new repository, so a glob
// lists them.
func gitAdd(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func writeFile(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// openSet is the Set of root's lock.
func openSet(t *testing.T, root string) *Set {
	t.Helper()
	s, err := Open(root, []string{"yaml"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// entries decodes each of raws, an entry of key's kind in rule r, and
// sends them in one build request.
func entries(t *testing.T, set *Set, key string, raws ...string) []*Spec {
	t.Helper()
	k := set.Kind(context.Background(), key)
	var specs []any
	for _, raw := range raws {
		s, err := k.Decode([]byte(raw))
		if err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		specs = append(specs, k.ForRule(s, "r"))
	}
	Build(context.Background(), specs)
	out := make([]*Spec, len(specs))
	for i, s := range specs {
		out[i] = s.(*Spec)
	}
	return out
}

// extractOne builds the one entry raw of key's kind in root and runs it.
func extractOne(t *testing.T, root, key, raw string) ([]check.Value, error) {
	t.Helper()
	set := openSet(t, root)
	s := entries(t, set, key, raw)[0]
	x, err := set.Kind(context.Background(), key).Build(s)
	if err != nil {
		t.Fatalf("build %s: %v", raw, err)
	}
	return x.Extract(context.Background(), root)
}
