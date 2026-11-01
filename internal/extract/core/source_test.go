package core

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The read cannot overflow whatever its limit: a source with the largest
// int64 as its limit, which Build refuses, still reads the whole file. The
// planted break is reading limit+1 bytes, which wraps to a negative count
// and reads nothing.
func TestReadDoesNotOverflowAtTheLargestLimit(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "a: 1\n"})
	data, err := Source{File: "f", Limit: math.MaxInt64}.Read(t.Context(), root)
	if err != nil || string(data) != "a: 1\n" {
		t.Errorf("read %q, %v", data, err)
	}
}

// Head reads at most n bytes and gives the whole file's size, and refuses
// as Read does: a file over the limit, one that is not there, and a link
// that leads out. Planted breaks: Head reading the whole file; the limit
// check dropped from open.
func TestHead(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "#!/bin/sh\nX=1\n"})
	extracttest.Symlink(t, "/etc/hostname", root+"/out")
	head, size, err := Source{File: "f", Limit: 100}.Head(t.Context(), root, 4)
	if err != nil || string(head) != "#!/b" || size != 14 {
		t.Errorf("head %q, size %d, %v; want #!/b and 14", head, size, err)
	}
	if head, _, err := (Source{File: "f", Limit: 100}).Head(t.Context(), root, 100); err != nil || len(head) != 14 {
		t.Errorf("head of a short file %q, %v", head, err)
	}
	_, _, err = Source{File: "f", Limit: 13}.Head(t.Context(), root, 4)
	extracttest.WantError(t, err, "over the limit")
	_, _, err = Source{File: "g", Limit: 100}.Head(t.Context(), root, 4)
	extracttest.WantError(t, err, "read g:")
	_, _, err = Source{File: "out", Limit: 100}.Head(t.Context(), root, 4)
	extracttest.WantError(t, err, "a symlink leads outside the repository")
}

// Resolved names the file a link leads to, relative to the root, and refuses
// what Read refuses, with Read's words: a link out of the repository and a
// root that is not there. The planted breaks are each of its two error
// checks negated, which return a path for the refusal and refuse the link
// that stays inside.
func TestResolvedFollowsLinksAndRefusesAsReadDoes(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"real/f.yaml": "a: 1\n"})
	extracttest.Symlink(t, "real/f.yaml", filepath.Join(root, "link.yaml"))
	extracttest.Symlink(t, "/", filepath.Join(root, "slash"))
	if got, err := (Source{File: "link.yaml"}).Resolved(t.Context(), root); err != nil || got != "real/f.yaml" {
		t.Errorf("link.yaml resolves to %q, %v; want real/f.yaml", got, err)
	}
	for _, c := range []struct{ root, file, want string }{
		{root, "slash/etc/passwd", "read slash/etc/passwd: a symlink leads outside the repository"},
		{filepath.Join(t.TempDir(), "gone"), "a.txt", "open the repository root: no such file or directory"},
	} {
		got, err := (Source{File: c.file}).Resolved(t.Context(), c.root)
		if err == nil || err.Error() != c.want || got != "" {
			t.Errorf("%s resolves to %q, %v; want the error %q", c.file, got, err, c.want)
		}
	}
}

// What a read costs on a real run, which opens the root by its path, and
// on a witness, which reads through the fixture the engine holds open: a
// file three directories deep, and Resolved, which a file set calls before
// each read.
func BenchmarkSourceRead(b *testing.B) {
	real := b.TempDir()
	fixture := filepath.Join(real, ".errata", "witness", "w")
	for _, dir := range []string{real, fixture} {
		if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o750); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "a", "b", "c.yaml"), []byte("a: 1\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.CommandContext(b.Context(), "git", args...)
		cmd.Dir = real
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	held, err := os.OpenRoot(fixture)
	if err != nil {
		b.Fatal(err)
	}
	defer held.Close()
	onWitness, err := OnFixture(check.OnWitness(b.Context(), real), fixture, held)
	if err != nil {
		b.Fatal(err)
	}
	src := Source{File: "a/b/c.yaml", Limit: 100}
	for _, run := range []struct {
		name string
		ctx  context.Context
		root string
	}{
		{"real", b.Context(), real},
		{"witness", onWitness, fixture},
	} {
		b.Run(run.name+"/read", func(b *testing.B) {
			for b.Loop() {
				if _, err := src.Read(run.ctx, run.root); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(run.name+"/resolved", func(b *testing.B) {
			for b.Loop() {
				if _, err := src.Resolved(run.ctx, run.root); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Reads in one tree share the directories they pass through, and each
// reads its own file: a sibling directory of the same name's file, a file
// at the root after them, and a file 70 directories deep, below which the
// chain holds no more than maxChain directories open. Planted breaks: the
// chain kept whatever the next path's directories, which read a/x.txt for
// b/x.txt; the cap dropped, which held 70.
func TestATreeSharesItsDirectories(t *testing.T) {
	deep := strings.Repeat("d/", 70) + "z.txt"
	root := extracttest.Repo(t, map[string]string{"a/x.txt": "a\n", "b/x.txt": "b\n", "a/c/x.txt": "c\n", "r.txt": "r\n", deep: "z\n"})
	tr, err := openTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	for _, c := range []struct{ file, want string }{
		{"a/x.txt", "a\n"}, {"a/c/x.txt", "c\n"}, {"b/x.txt", "b\n"}, {"a/x.txt", "a\n"}, {"r.txt", "r\n"}, {deep, "z\n"},
	} {
		if data, err := (Source{File: c.file, Limit: 100}).readIn(tr); err != nil || string(data) != c.want {
			t.Errorf("%s: %q, %v; want %q", c.file, data, err, c.want)
		}
	}
	if n := len(tr.dirs.roots); n != maxChain {
		t.Errorf("the chain holds %d directories after the deep read, want %d", n, maxChain)
	}
}

// The chain keeps the directories the next path shares, each opened once:
// a second read of a/c/x.txt goes through the a/ and a/c/ already open, as
// does a read of a/x.txt after it and one at the root; a read of b/x.txt
// closes both. Planted breaks, among them the mutants mutation left alive:
// the shared prefix never kept (keep < len(dirs) negated), which opened a/
// again; the prefix test running past the next path's directories (<=
// len(dirs)), which read past them; the chain closed wherever it runs on
// below a path, which opened a/c/ again at each read.
func TestATreeKeepsTheDirectoriesPathsShare(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a/x.txt": "a\n", "a/c/x.txt": "c\n", "b/x.txt": "b\n", "r.txt": "r\n"})
	tr, err := openTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	read := func(file, want string) {
		t.Helper()
		if data, err := (Source{File: file, Limit: 100}).readIn(tr); err != nil || string(data) != want {
			t.Fatalf("%s: %q, %v; want %q", file, data, err, want)
		}
	}
	read("a/c/x.txt", "c\n")
	a, c := tr.dirs.roots[0], tr.dirs.roots[1]
	read("a/c/x.txt", "c\n")
	if len(tr.dirs.roots) != 2 || tr.dirs.roots[0] != a || tr.dirs.roots[1] != c {
		t.Errorf("a second read of a/c/x.txt opened its directories again: %v", tr.dirs.names)
	}
	for _, f := range []struct{ file, want string }{{"a/x.txt", "a\n"}, {"r.txt", "r\n"}} {
		read(f.file, f.want)
		if len(tr.dirs.roots) != 2 || tr.dirs.roots[0] != a || tr.dirs.roots[1] != c {
			t.Errorf("%s after a/c/x.txt: the chain holds %v, want a and c as opened", f.file, tr.dirs.names)
		}
	}
	read("b/x.txt", "b\n")
	if !slices.Equal(tr.dirs.names, []string{"b"}) {
		t.Errorf("b/x.txt: the chain holds %v, want b", tr.dirs.names)
	}
}

// A read on a witness of a file git's index lacks says what the index lacks
// and what to commit. The one-name form is for the file itself; the
// two-name form is for a link or directory on the way, and names the file,
// the entry and the entry again as the one to commit. Planted breaks: the
// ", which is" piece dropped; the file and the entry swapped.
func TestNotIndexedNamesTheFileAndTheEntryOnTheWay(t *testing.T) {
	const lacks = " not in git's index below the fixture, so a clone of the repository does not hold it; commit "
	for _, c := range []struct{ file, entry, want string }{
		{"x.yaml", "x.yaml", "x.yaml is" + lacks + "it"},
		{"./x.yaml", "x.yaml", "./x.yaml is" + lacks + "it"},
		{"sub/x.yaml", "sub", "sub/x.yaml needs sub, which is" + lacks + "sub"},
		{"chain.yaml", "b", "chain.yaml needs b, which is" + lacks + "b"},
	} {
		if got := (notIndexed{file: c.file, entry: c.entry}).Error(); got != c.want {
			t.Errorf("notIndexed{%q, %q}: %q, want %q", c.file, c.entry, got, c.want)
		}
	}
}
