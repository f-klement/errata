package core

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A glob of a real run leaves the fixtures of witnesses out, at the root
// and in any directory .errata, and a glob on a witness matches the
// fixture's tracked files with its path cut off, leaving out an untracked
// file of the fixture, the real tree's files and a fixture nested in it.
// Planted breaks: the real run's list without the DeleteFunc; the witness
// given the whole real index; the cut prefix without its slash, which
// matched a sibling fixture named a-b beside a.
func TestTrackedFilesOfAWitness(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"conf/a.yaml":                           "",
		".errata/witness/a/conf/a.yaml":         "",
		".errata/witness/a/.errata/witness/b/x": "",
		".errata/witness/a-b/conf/c.yaml":       "",
		"sub/.errata/witness/c/conf/d.yaml":     "",
		".errata/other.yaml":                    "",
	})
	if err := os.WriteFile(filepath.Join(root, ".errata/witness/a/conf/untracked.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := trackedFiles(t.Context(), root)
	if want := ".errata/other.yaml conf/a.yaml"; err != nil || strings.Join(got, " ") != want {
		t.Errorf("a real run lists %q, %v, want %q", got, err, want)
	}
	fixture := filepath.Join(root, ".errata", "witness", "a")
	got, err = trackedFiles(check.OnWitness(t.Context(), root), fixture)
	if want := "conf/a.yaml"; err != nil || strings.Join(got, " ") != want {
		t.Errorf("a witness lists %q, %v, want %q", got, err, want)
	}
}

// A witness whose root is not inside the real root is refused, not read
// as a list of every file. The planted break: the IsLocal test dropped,
// which cut ../ paths from nothing and listed none.
func TestTrackedFilesOfAWitnessOutside(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{"a": ""})
	_, err := trackedFiles(check.OnWitness(t.Context(), root), t.TempDir())
	extracttest.WantError(t, err, "is not inside the repository root")
}

// An index entry that is not in the form git writes is refused on every
// run, before any is cut or passed over: on a witness one that leaves the
// fixture once its prefix is cut, one that reaches a fixture's file
// through another directory and one with an empty name; on a real run one
// with a . name and one with an empty name, each of which names a
// fixture's file that check.Witnessed does not see. Each is inside the
// repository, so readIndex lists it. Planted breaks: the Clean test made
// vacuous, which cut ../../x from the first; the test kept to the witness
// branch, which listed the real run's entries.
func TestTrackedFilesRefusesANonCanonicalEntry(t *testing.T) {
	for _, e := range []struct {
		entry   string
		witness bool
	}{
		{".errata/witness/a/b/../../../x", true},
		{"x/../.errata/witness/a/y", true},
		{".errata/witness/a//y", true},
		{".errata/./witness/a/y", false},
		{".errata//witness/a/y", false},
	} {
		root := repo(t, map[string]string{".errata/witness/a/y": ""})
		index := gitIndex(2, indexEntry{path: ".errata/witness/a/y"}, indexEntry{path: e.entry})
		if err := os.WriteFile(filepath.Join(root, ".git", "index"), index, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, at := t.Context(), root
		if e.witness {
			ctx, at = check.OnWitness(ctx, root), filepath.Join(root, ".errata", "witness", "a")
		}
		_, err := trackedFiles(ctx, at)
		extracttest.WantError(t, err, "git's index lists "+Quote(e.entry)+", a path with a . or .. name or an empty one, which git does not write; errata does not read such an index, since the path may name a file of a witness's fixture in a form that is not its own")
	}
}

// Only indexBelow, which trackedFiles and OnFixture call, lists git's
// index, so every glob of a real run leaves the fixtures of witnesses out
// and every glob on a witness reads the cut index: no other function or
// declaration of the package names readIndex.
// Planted breaks: Files.List calling readIndex(root) itself; Files.List
// calling it through read := readIndex; a package var holding readIndex.
func TestOnlyIndexBelowReadsTheIndex(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	callers := map[string]bool{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			// Every use of the name counts, not only a call, so a value
			// such as read := readIndex, or a package var holding it, is
			// seen; the declaration's own name is outside its body.
			var body ast.Node = d
			user := "a package-level declaration"
			if fn, ok := d.(*ast.FuncDecl); ok {
				if fn.Body == nil {
					continue
				}
				body, user = fn.Body, fn.Name.Name
			}
			ast.Inspect(body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "readIndex" {
					callers[user] = true
				}
				return true
			})
		}
	}
	if len(callers) != 1 || !callers["indexBelow"] {
		t.Errorf("readIndex is named by %v, want indexBelow alone", slices.Sorted(maps.Keys(callers)))
	}
}

// swapped is a repository whose witness w holds x.pin "checked", opened as
// the engine opens it, after which .errata was moved to moved and a
// symlink put in its place, to a tree whose witness w holds y.pin
// "outside" and x.pin, a symlink to y.pin. It returns the repository's root, the fixture's path
// and the context of a run on the witness.
func swapped(t *testing.T) (real, fixture string, ctx context.Context) {
	t.Helper()
	real = t.TempDir()
	fixture = filepath.Join(real, ".errata", "witness", "w")
	other := filepath.Join(t.TempDir(), "witness", "w")
	for dir, files := range map[string][]string{fixture: {"x.pin"}, other: {"y.pin"}} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			text := "outside\n"
			if dir == fixture {
				text = "checked\n"
			}
			if err := os.WriteFile(filepath.Join(dir, f), []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink("y.pin", filepath.Join(other, "x.pin")); err != nil {
		t.Fatal(err)
	}
	git(t, real, "init", "-q")
	git(t, real, "add", "-A")
	held, err := os.OpenRoot(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	ctx, err = OnFixture(check.OnWitness(t.Context(), real), fixture, held)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(real, ".errata"), filepath.Join(real, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(filepath.Dir(other)), filepath.Join(real, ".errata")); err != nil {
		t.Fatal(err)
	}
	return real, fixture, ctx
}

// On a witness every read of the fixture goes through the directory the
// engine opened when it checked the fixture, so a symlink swapped in at
// .errata after that check moves no read: Read, Head, Stat and Resolved
// see the checked tree, and a walk and a listing of the root list it.
// Once that directory is gone the read is refused. A run on a witness
// whose fixture is not held is refused, never read by its path. Planted
// breaks: openTree opening the fixture by its path, which read "outside"
// and found y.pin; resolve's Lstat by path, which followed the target's
// x.pin and failed Resolved.
func TestAWitnessReadsTheFixtureItChecked(t *testing.T) {
	real, fixture, ctx := swapped(t)
	x := Source{File: "x.pin", Limit: 100}
	if data, err := x.Read(ctx, fixture); err != nil || string(data) != "checked\n" {
		t.Errorf("Read: %q, %v; want the checked fixture's x.pin", data, err)
	}
	if head, _, err := x.Head(ctx, fixture, 3); err != nil || string(head) != "che" {
		t.Errorf("Head: %q, %v; want the checked fixture's x.pin", head, err)
	}
	if at, err := x.Resolved(ctx, fixture); err != nil || at != "x.pin" {
		t.Errorf("Resolved: %q, %v", at, err)
	}
	if _, err := (Source{File: "y.pin"}).Stat(ctx, fixture); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat of y.pin, which only the symlink's target holds: %v, want not there", err)
	}
	var walked []string
	err := Walk(ctx, fixture, DefaultWalk, func(string, string) bool { return true }, func(_ string, names []string) error {
		walked = append(walked, names...)
		return nil
	})
	if err != nil || !slices.Equal(walked, []string{"x.pin"}) {
		t.Errorf("Walk: %v, %v; want the checked fixture's x.pin alone", walked, err)
	}
	d, err := OpenDir(ctx, fixture)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := d.Readdirnames(-1)
	_ = d.Close()
	if err != nil || !slices.Equal(listed, []string{"x.pin"}) {
		t.Errorf("OpenDir: %v, %v; want the checked fixture's x.pin alone", listed, err)
	}

	if err := os.RemoveAll(filepath.Join(real, "moved")); err != nil {
		t.Fatal(err)
	}
	_, err = x.Read(ctx, fixture)
	extracttest.WantError(t, err, "read x.pin: no such file or directory")

	_, err = x.Read(check.OnWitness(t.Context(), real), fixture)
	extracttest.WantError(t, err, "the witness at "+Quote(fixture)+" is not a fixture opened inside the repository root")
}

// On a witness a read under any root but the fixture held is refused,
// though the root is inside the repository: a sibling fixture, .errata
// and the repository root itself would each read the held directory with
// the wrong path for resolve to follow links against. Planted break: the
// comparison with the held fixture's path dropped, which read the held
// x.pin under each.
func TestAWitnessReadsNoRootButItsFixture(t *testing.T) {
	real, fixture, ctx := swapped(t)
	for _, root := range []string{filepath.Join(real, ".errata", "witness", "v"), filepath.Join(real, ".errata"), real} {
		_, err := (Source{File: "x.pin", Limit: 100}).Read(ctx, root)
		extracttest.WantError(t, err, "the witness at "+Quote(root)+" is not a fixture opened inside the repository root")
	}
	if data, err := (Source{File: "x.pin", Limit: 100}).Read(ctx, fixture+string(filepath.Separator)); err != nil || string(data) != "checked\n" {
		t.Errorf("the fixture with a trailing separator: %q, %v", data, err)
	}
}

// On a witness a glob is refused when git's index lists no file below the
// fixture, as a clone in CI holds none: an untracked fixture, and one the
// index lists as .Errata while the disk holds .errata, which macOS or
// Windows would open. Without the refusal the glob matched nothing, and a
// rule under allow_empty held its witness on no file read. A fixture whose
// only entries are another fixture's below it still lists, as none.
// Planted break: the count of entries below the fixture dropped, which
// listed nothing for both.
func TestTrackedFilesOfAFixtureTheIndexDoesNotList(t *testing.T) {
	for name, tracked := range map[string]map[string]string{
		"untracked":    {"conf/a.yaml": ""},
		".Errata only": {".Errata/witness/w/x.yaml": ""},
	} {
		root := extracttest.Tracked(t, tracked)
		fixture := filepath.Join(root, ".errata", "witness", "w")
		if err := os.MkdirAll(fixture, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture, "x.yaml"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		files, err := CheckFiles("yaml", []string{"**/*.yaml"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = files.List(check.OnWitness(t.Context(), root), fixture)
		if err == nil || !strings.Contains(err.Error(), `git's index lists no file below the fixture ".errata/witness/w"; a clone holds only the files git tracks, so commit the fixture's files`) {
			t.Errorf("%s: %v, want the fixture refused", name, err)
		}
	}
	root := extracttest.Tracked(t, map[string]string{".errata/witness/a/.errata/witness/b/x": ""})
	got, err := trackedFiles(check.OnWitness(t.Context(), root), filepath.Join(root, ".errata", "witness", "a"))
	if err != nil || len(got) != 0 {
		t.Errorf("a fixture holding only a nested one: %q, %v, want none", got, err)
	}
}

// A run on a held fixture lists a glob's files from the list OnFixture
// read, so git's index is read once per witness: an index written after
// OnFixture changes nothing. That list leaves out a nested fixture, which
// a read still finds, and a file the index does not list is refused with
// an error that errors.Is reads as not there. Planted breaks: the held
// list dropped from trackedFiles, which read the index again and lost
// c.yaml; the held set built from the glob list, which refused the nested
// fixture's x.
func TestAHeldFixtureListsItsFilesOnce(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		".errata/witness/w/a.yaml":              "a",
		".errata/witness/w/c.yaml":              "c",
		".errata/witness/w/.errata/witness/b/x": "x",
	})
	fixture := filepath.Join(root, ".errata", "witness", "w")
	if err := os.WriteFile(filepath.Join(fixture, "u.yaml"), []byte("u"), 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenRoot(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, err := OnFixture(check.OnWitness(t.Context(), root), fixture, held)
	if err != nil {
		t.Fatal(err)
	}
	git(t, root, "rm", "-q", "--cached", ".errata/witness/w/c.yaml")
	if got, err := trackedFiles(ctx, fixture); err != nil || strings.Join(got, " ") != "a.yaml c.yaml" {
		t.Errorf("a glob's files on the held fixture: %q, %v, want a.yaml c.yaml", got, err)
	}
	if data, err := (Source{File: ".errata/witness/b/x", Limit: 10}).Read(ctx, fixture); err != nil || string(data) != "x" {
		t.Errorf("the nested fixture's x: %q, %v", data, err)
	}
	_, err = Source{File: "u.yaml", Limit: 10}.Read(ctx, fixture)
	if want := "read u.yaml: u.yaml is not in git's index below the fixture, so a clone of the repository does not hold it; commit it"; err == nil || err.Error() != want || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the untracked u.yaml: %v, want %q, read as not there", err, want)
	}
}
