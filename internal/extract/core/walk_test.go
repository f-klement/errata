package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	slashpath "path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// pins accepts the name .pin.
func pins(_, name string) bool { return name == ".pin" }

// paths is Walk of root with l and want, as the path of each file visited.
func paths(root string, l WalkLimits, want func(dir, name string) bool) ([]string, error) {
	var out []string
	err := Walk(root, l, want, func(dir string, names []string) error {
		for _, n := range names {
			out = append(out, slashpath.Join(dir, n))
		}
		return nil
	})
	return out, err
}

// walk is Walk of root with l and pins, its paths joined by spaces.
func walk(t *testing.T, root string, l WalkLimits) (string, error) {
	t.Helper()
	got, err := paths(root, l, pins)
	return strings.Join(got, " "), err
}

// A walk returns each file want accepts, a directory's before its
// subdirectories', each by name, and enters none of SkippedDirs. The
// planted break: read without the SkippedDirs test, which returns a pin
// from each.
func TestWalkOrderAndSkippedDirs(t *testing.T) {
	files := map[string]string{".pin": "", "b/.pin": "", "a/.pin": "", "a/x": "", "a/c/.pin": "", "a/c/d/.other": ""}
	for name := range SkippedDirs {
		files[name+"/.pin"] = ""
		if name != ".git" { // a .git below the root passes over its directory
			files["a/"+name+"/.pin"] = ""
		}
	}
	var visited []string
	err := Walk(extracttest.Repo(t, files), DefaultWalk, pins, func(dir string, names []string) error {
		for _, n := range names {
			visited = append(visited, slashpath.Join(dir, n))
		}
		if len(names) == 0 {
			t.Errorf("%s visited with no files", dir)
		}
		return nil
	})
	if got := strings.Join(visited, " "); err != nil || got != ".pin a/.pin a/c/.pin b/.pin" {
		t.Errorf("got %q, %v", got, err)
	}
}

// A symlink to a directory is not entered; a symlink whose name want
// accepts is returned, for the Source to follow or refuse, wherever it
// points.
func TestWalkSymlinks(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"real/.pin": "", "outside/.pin": ""})
	repo := filepath.Join(root, "repo")
	extracttest.Symlink(t, "../real", filepath.Join(repo, "dir"))
	extracttest.Symlink(t, "../../outside/.pin", filepath.Join(repo, "out", ".pin"))
	got, err := walk(t, repo, DefaultWalk)
	if err != nil || got != "out/.pin" {
		t.Errorf("got %q, %v", got, err)
	}
}

// A directory of more entries than one batch is read whole.
func TestWalkReadsAWholeDirectory(t *testing.T) {
	files := map[string]string{}
	for i := range walkBatch*2 + 1 {
		files["d"+strconv.Itoa(i)+"/.pin"] = ""
	}
	got, err := paths(extracttest.Repo(t, files), DefaultWalk, pins)
	if err != nil || len(got) != len(files) {
		t.Errorf("%d of %d, %v", len(got), len(files), err)
	}
}

// Each limit holds at its value and refuses one past it: entries counts
// every file and directory read, the skipped directories too but nothing
// in them, and depth the directories below the root. The planted breaks:
// the count of entries and the depth test each removed.
func TestWalkLimits(t *testing.T) {
	// a, a/b, a/b/.pin, c, node_modules: five entries; a/b is two deep.
	root := extracttest.Repo(t, map[string]string{"a/b/.pin": "", "c": "", "node_modules/x/y": ""})
	for _, c := range []struct {
		l    WalkLimits
		want string
	}{
		{WalkLimits{Entries: 5, Dirs: 2, Depth: 2, Names: 100}, ""},
		// Each limit exactly: five entries, two directories, two deep, and
		// one byte of names waiting at a time.
		{WalkLimits{Entries: 5, Dirs: 2, Depth: 2, Names: 1}, ""},
		{WalkLimits{Entries: 4, Dirs: 2, Depth: 2, Names: 100}, `walk: the repository holds more than 4 files and directories outside the directories errata passes over, the limit, and stopped in "a/b"; name the files to read in files`},
		// The directories entered: a and a/b.
		{WalkLimits{Entries: 5, Dirs: 1, Depth: 2, Names: 100}, `walk: the repository holds more than 1 directories outside the directories errata passes over, the limit, and stopped at "a/b"; name the files to read in files`},
		// The names waiting: a, 1 byte, at the root, then b below it.
		{WalkLimits{Entries: 5, Dirs: 2, Depth: 2, Names: 0}, `walk: the directories waiting to be walked have names of more than 0 bytes in all, the limit, in "."; name the files to read in files`},
		{WalkLimits{Entries: 5, Dirs: 2, Depth: 1, Names: 100}, `walk: "a/b" is nested more than 1 directories deep, the limit; name the files to read in files`},
	} {
		got, err := walk(t, root, c.l)
		switch {
		case c.want == "" && (err != nil || got != "a/b/.pin"):
			t.Errorf("%+v: %q, %v", c.l, got, err)
		case c.want != "" && (err == nil || err.Error() != c.want):
			t.Errorf("%+v: %v, want %q", c.l, err, c.want)
		}
	}
}

// deepTree makes 20 directories of 200 bytes, one inside the other, a path
// of 4019 bytes, and in the last one the file or directory last names. The
// directories are made through os.Root, since the whole path is past what
// one call may name.
func deepTree(t *testing.T, last string, dir bool) string {
	t.Helper()
	root := t.TempDir()
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		name := strings.Repeat("d", 200)
		if err := r.Mkdir(name, 0o750); err != nil {
			t.Fatal(err)
		}
		next, err := r.OpenRoot(name)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		r = next
	}
	if dir {
		err = r.Mkdir(last, 0o750)
	} else {
		err = r.WriteFile(last, nil, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return root
}

// A path of 4096 bytes is walked, and a longer one is refused, not passed
// over. Linux refuses a name over 255 bytes, so the paths are many
// directories. The planted breaks: the length test removed, and > as >=.
func TestWalkLongPaths(t *testing.T) {
	l := WalkLimits{Entries: 100, Dirs: 100, Depth: 100, Names: 1 << 20}
	pin := strings.Repeat("p", 76)
	got, err := paths(deepTree(t, pin, false), l, func(_, name string) bool { return name == pin })
	if err != nil || len(got) != 1 || len(got[0]) != maxFileName {
		t.Errorf("a path of %d bytes: %d found, %v", maxFileName, len(got), err)
	}
	_, err = walk(t, deepTree(t, strings.Repeat("e", 80), true), l)
	extracttest.WantError(t, err, "is 4100 bytes long, over the limit of 4096")
}

// A directory the walk cannot read is refused. Root reads every directory,
// so the test needs another user.
func TestWalkRefusesAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory of mode 000")
	}
	root := extracttest.Repo(t, map[string]string{"a/.pin": ""})
	if err := os.Chmod(filepath.Join(root, "a"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "a"), 0o750) })
	_, err := walk(t, root, DefaultWalk)
	extracttest.WantError(t, err, `walk: read "a": permission denied`)
}

// A root that does not exist is refused.
func TestWalkRefusesAMissingRoot(t *testing.T) {
	_, err := walk(t, filepath.Join(t.TempDir(), "gone"), DefaultWalk)
	extracttest.WantError(t, err, "open the repository root: no such file or directory")
}

// The README's table of the directories a walk passes over is SkippedDirs,
// row for row, so the list has one home and the README cannot drift from
// it. The planted break: a directory added to SkippedDirs alone.
func TestSkippedDirsAreTheREADMEs(t *testing.T) {
	data, err := os.ReadFile("../../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(data), "| Directory | Why |\n|---|---|\n")
	if !ok {
		t.Fatal("the README has no table of skipped directories")
	}
	table, _, _ = strings.Cut(table, "\n\n")
	rows := map[string]string{}
	for line := range strings.SplitSeq(table, "\n") {
		name, why, _ := strings.Cut(strings.TrimPrefix(line, "| `"), "` | ")
		rows[name] = strings.TrimSuffix(why, " |")
	}
	if len(rows) != len(SkippedDirs) {
		t.Errorf("the README lists %d directories, SkippedDirs %d", len(rows), len(SkippedDirs))
	}
	for name, why := range SkippedDirs {
		if rows[name] != why {
			t.Errorf("%s: the README says %q, SkippedDirs %q", name, rows[name], why)
		}
	}
}

// A directory below the root that holds .git, a file for a submodule or a
// worktree and a directory for a repository of its own, is not entered: its
// pins are another project's. The root's own .git is the repository's. The
// planted break: read without the .git test.
func TestWalkPassesOverNestedRepositories(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		".pin": "", ".git/HEAD": "", "sub/.git": "gitdir: ../.git/modules/sub\n", "sub/.pin": "",
		"nested/.git/HEAD": "", "nested/.pin": "", "nested/a/.pin": "", "own/.pin": "",
	})
	got, err := walk(t, root, DefaultWalk)
	if err != nil || got != ".pin own/.pin" {
		t.Errorf("got %q, %v", got, err)
	}
}

// A directory that is no longer one when the walk opens it is refused: the
// test swaps a for a symlink out of the repository once the root is listed,
// which os.Root refuses to open, as root too. The planted break: read
// passing over a directory it cannot open.
func TestWalkRefusesADirectoryItCannotOpen(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a/.pin": "", "x": ""})
	elsewhere := t.TempDir()
	swap := func(dir, name string) bool {
		if dir == "." && name == "x" {
			if err := os.RemoveAll(filepath.Join(root, "a")); err != nil {
				t.Fatal(err)
			}
			extracttest.Symlink(t, elsewhere, filepath.Join(root, "a"))
		}
		return name == ".pin"
	}
	_, err := paths(root, DefaultWalk, swap)
	extracttest.WantError(t, err, `walk: read "a": `)
}

// failingDir lists one batch of a directory, then fails.
type failingDir struct{ calls int }

func (f *failingDir) ReadDir(int) ([]fs.DirEntry, error) {
	if f.calls++; f.calls == 1 {
		return []fs.DirEntry{fs.FileInfoToDirEntry(fakeInfo(".pin"))}, nil
	}
	return nil, errors.New("input/output error")
}

func (*failingDir) Close() error { return nil }

// fakeInfo is a file's FileInfo, by name.
type fakeInfo string

func (n fakeInfo) Name() string     { return string(n) }
func (fakeInfo) Size() int64        { return 0 }
func (fakeInfo) Mode() fs.FileMode  { return 0 }
func (fakeInfo) ModTime() time.Time { return time.Time{} }
func (fakeInfo) IsDir() bool        { return false }
func (fakeInfo) Sys() any           { return nil }

// A directory whose listing fails partway is refused, not read as the
// entries before the failure. The planted break: read ending the listing at
// any error.
func TestWalkRefusesAListingThatFails(t *testing.T) {
	r, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	w := walker{limits: DefaultWalk, want: pins, visit: func(string, []string) error { return nil },
		list: func(*os.Root) (lister, error) { return &failingDir{}, nil }}
	err = w.dir(r, ".", 0)
	extracttest.WantError(t, err, `walk: read ".": input/output error`)
}

// The walk holds the names of the directories it has still to walk, not
// their paths: 10000 directories below a path of 3000 bytes keep the heap
// in use under 8 MB, sampled inside the walk, where their paths alone are
// 30 MB. The planted break: read keeping each subdirectory's whole path.
func TestWalkHoldsNamesNotPaths(t *testing.T) {
	root := t.TempDir()
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	for range 15 {
		name := strings.Repeat("d", 200)
		if err := r.Mkdir(name, 0o750); err != nil {
			t.Fatal(err)
		}
		next, err := r.OpenRoot(name)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		r = next
	}
	for i := range 10000 {
		d := fmt.Sprintf("%05d", i)
		if err := r.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := r.WriteFile(d+"/f", nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.Close()
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	base, peak, calls := m.HeapAlloc, uint64(0), 0
	sample := func(string, string) bool {
		if calls++; calls%1000 == 0 {
			runtime.GC()
			runtime.ReadMemStats(&m)
			peak = max(peak, m.HeapAlloc)
		}
		return false
	}
	if err := Walk(root, DefaultWalk, sample, func(string, []string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 10000 {
		t.Fatalf("want called %d times", calls)
	}
	if peak > base+8<<20 {
		t.Errorf("the heap in use rose %d bytes during the walk", peak-base)
	}
}

// DefaultWalk holds the values the README's Limits rows give, so a limit
// raised or removed in one place and not the other fails here. The planted
// break: DefaultWalk unbounded, every limit 1 << 40.
func TestDefaultWalkIsTheREADMEs(t *testing.T) {
	data, err := os.ReadFile("../../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	row := func(name string) int {
		_, rest, ok := strings.Cut(string(data), "\n| "+name+" | ")
		if !ok {
			t.Fatalf("the README has no Limits row %q", name)
		}
		cell, _, _ := strings.Cut(rest, " |")
		number, unit, _ := strings.Cut(cell, " ")
		n, err := strconv.Atoi(number)
		if err != nil {
			t.Fatalf("row %q: %v", name, err)
		}
		if unit == "MiB" {
			n <<= 20
		}
		return n
	}
	readme := WalkLimits{Entries: row("walk entries"), Dirs: row("walk directories"), Depth: row("walk depth"), Names: row("walk names")}
	if DefaultWalk != readme {
		t.Errorf("DefaultWalk is %+v, the README's Limits rows say %+v", DefaultWalk, readme)
	}
}
