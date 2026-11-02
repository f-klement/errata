package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core/gitindex"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A run on a witness whose fixture OnFixture holds no Index of is
// refused, never read from git's index by its path; so is a fixture
// outside the repository root, which OnFixture refuses before the index
// is read. Planted breaks: runIndex reading the real index when no
// fixture is held; the IsLocal test dropped from OnFixture, which cut
// ../ paths from nothing and listed none.
func TestAWitnessIndexIsTheHeldFixtures(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{".errata/witness/w/a.yaml": ""})
	fixture := filepath.Join(root, ".errata", "witness", "w")
	_, err := runIndex(check.OnWitness(t.Context(), root), fixture)
	extracttest.WantError(t, err, "the witness at "+Quote(fixture)+" is not a fixture opened inside the repository root")

	outside := t.TempDir()
	held, err := os.OpenRoot(outside)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	_, err = OnFixture(check.OnWitness(t.Context(), root), outside, held)
	extracttest.WantError(t, err, "the witness at "+Quote(outside)+" is not inside the repository root")
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
// see the checked tree, and a walk and a listing of the root list it. Once
// that directory is gone the read is refused, as a read of a committed
// file the disk does not hold. A run on a witness whose fixture is not
// held is refused, never read by its path. Planted breaks: openTree
// opening the fixture by its path, which read "outside" and found y.pin;
// resolve's Lstat by path, which followed the target's x.pin and failed
// Resolved.
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
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	var listed []string
	for _, e := range entries {
		listed = append(listed, e.Name())
	}
	if err != nil || !slices.Equal(listed, []string{"x.pin"}) {
		t.Errorf("OpenDir: %v, %v; want the checked fixture's x.pin alone", listed, err)
	}

	if err := os.RemoveAll(filepath.Join(real, "moved")); err != nil {
		t.Fatal(err)
	}
	_, err = x.Read(ctx, fixture)
	extracttest.WantError(t, err, "read x.pin: x.pin is a file in git's index below the fixture, and the disk does not hold it as one")

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

// A run on a held fixture lists a glob's files from the list OnFixture
// read, so git's index is read once per witness: an index written after
// OnFixture changes nothing. That list leaves out a nested fixture, which
// a read still finds, and a file the index does not list is refused with
// an error that errors.Is reads as not there. Planted breaks: the held
// index dropped from runIndex, which read the index again and lost
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
	if ix, err := runIndex(ctx, fixture); err != nil || globbedOf(t, ix) != "a.yaml c.yaml" {
		t.Errorf("a glob's files on the held fixture: %q, %v, want a.yaml c.yaml", globbedOf(t, ix), err)
	}
	if data, err := (Source{File: ".errata/witness/b/x", Limit: 10}).Read(ctx, fixture); err != nil || string(data) != "x" {
		t.Errorf("the nested fixture's x: %q, %v", data, err)
	}
	_, err = Source{File: "u.yaml", Limit: 10}.Read(ctx, fixture)
	if want := "read u.yaml: u.yaml is not in git's index below the fixture, so a clone of the repository does not hold it; commit it"; err == nil || err.Error() != want || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the untracked u.yaml: %v, want %q, read as not there", err, want)
	}
}

// On a witness a symlink is followed only where git's index lists the
// entry as a symlink. A clone checks out what the index lists as a file
// as a file, so a link put in its place on disk leads where no path of the
// clone does: d is committed as a file and replaced on disk by a link to
// e, whose x is committed, so d/x is no file of a clone, and Read, Head,
// Stat and Resolved refuse it as not there, naming the link to commit. k,
// committed as a link to e, is followed. The other way round, l is
// committed as a link to e/x and replaced on disk by a file: a clone reads
// e/x there, so each read of l is refused as unknown, not as a file that
// is not there, and so is a read of m, committed as a link to l.
// Planted breaks: the mode test dropped from resolve, which
// read e/x through d; the symlink test dropped from indexed, which read
// the file at l; the message built from the spec's file alone, which named
// m and not l.
func TestAWitnessFollowsOnlyALinkTheIndexListsAsOne(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		".errata/witness/w/d":   "a file",
		".errata/witness/w/e/x": "x",
	})
	fixture := filepath.Join(root, ".errata", "witness", "w")
	extracttest.Symlink(t, "e", filepath.Join(fixture, "k"))
	extracttest.Symlink(t, "e/x", filepath.Join(fixture, "l"))
	extracttest.Symlink(t, "l", filepath.Join(fixture, "m"))
	git(t, root, "init", "-q")
	git(t, root, "add", "-A")
	for _, f := range []string{"d", "l"} {
		if err := os.Remove(filepath.Join(fixture, f)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fixture, "l"), []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	extracttest.Symlink(t, "e", filepath.Join(fixture, "d"))
	held, err := os.OpenRoot(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, err := OnFixture(check.OnWitness(t.Context(), root), fixture, held)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := (Source{File: "k/x", Limit: 10}).Read(ctx, fixture); err != nil || string(data) != "x" {
		t.Errorf("k/x through the committed link: %q, %v", data, err)
	}
	const why = "d/x needs d, which git's index below the fixture lists as a file and not as a symlink, so a clone of the repository holds no link there; commit the link"
	x := Source{File: "d/x", Limit: 10}
	_, err = x.Read(ctx, fixture)
	_, _, headErr := x.Head(ctx, fixture, 1)
	_, statErr := x.Stat(ctx, fixture)
	_, resolvedErr := x.Resolved(ctx, fixture)
	for op, err := range map[string]error{"Read": err, "Head": headErr, "Stat": statErr, "Resolved": resolvedErr} {
		if err == nil || !strings.HasSuffix(err.Error(), why) || !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s of d/x through the link git lists as a file: %v, want %q, read as not there", op, err, why)
		}
	}
	const linkWhy = "l is no symlink on disk, while git's index below the fixture lists it as one, so a clone of the repository reads another file there; commit what the disk holds, or restore the link"
	l := Source{File: "l", Limit: 10}
	_, err = l.Read(ctx, fixture)
	_, _, headErr = l.Head(ctx, fixture, 1)
	_, statErr = l.Stat(ctx, fixture)
	_, resolvedErr = l.Resolved(ctx, fixture)
	for op, err := range map[string]error{"Read": err, "Head": headErr, "Stat": statErr, "Resolved": resolvedErr} {
		if err == nil || !strings.HasSuffix(err.Error(), linkWhy) || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s of the file l, which git lists as a symlink: %v, want %q, not read as not there", op, err, linkWhy)
		}
	}
	// m is a committed link to l, so a read of m lands at l, which the
	// refusal names as the entry to commit.
	const throughWhy = "m needs l, which is no symlink on disk, while git's index below the fixture lists it as one, so a clone of the repository reads another file there; commit what the disk holds, or restore the link"
	m := Source{File: "m", Limit: 10}
	_, err = m.Read(ctx, fixture)
	_, statErr = m.Stat(ctx, fixture)
	for op, err := range map[string]error{"Read": err, "Stat": statErr} {
		if err == nil || !strings.HasSuffix(err.Error(), throughWhy) || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s of m through the committed link to the file l: %v, want %q, not read as not there", op, err, throughWhy)
		}
	}
}

// On a witness a walk and a listing of the root name only what git's index
// lists below the fixture, as a clone holds it: never an untracked file or
// directory (u.pin, sub/u.pin, untracked/), and a committed directory the
// disk lacks (gone/) is walked as the clone holds it. A committed symlink
// (l.pin) is a file to the walk and a symlink to the listing. A nested
// fixture is passed over, as on any walk. An entry of the listing gives no
// file information, which the index does not hold. Both failed on 6f2f58a,
// which listed the disk. Planted breaks: top listing the disk on a
// witness; indexDir.enter keeping the directory it was in, which walked
// the root's names again below each directory to the depth limit; the
// listing typing a symlink as a file.
func TestAWitnessWalksAndListsOnlyWhatTheIndexLists(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		".errata/witness/w/a.pin":                   "a",
		".errata/witness/w/.errata/e.pin":           "e",
		".errata/witness/w/.errata/witness/n/d.pin": "d",
		".errata/witness/w/gone/c.pin":              "c",
		".errata/witness/w/sub/b.pin":               "b",
	})
	fixture := filepath.Join(root, ".errata", "witness", "w")
	extracttest.Symlink(t, "a.pin", filepath.Join(fixture, "l.pin"))
	git(t, root, "add", ".errata/witness/w/l.pin")
	if err := os.RemoveAll(filepath.Join(fixture, "gone")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(fixture, "untracked"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"u.pin", "sub/u.pin", "untracked/v.pin"} {
		if err := os.WriteFile(filepath.Join(fixture, f), []byte("u"), 0o600); err != nil {
			t.Fatal(err)
		}
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
	var walked []string
	err = Walk(ctx, fixture, DefaultWalk, func(string, string) bool { return true }, func(dir string, names []string) error {
		walked = append(walked, dir+":"+strings.Join(names, ","))
		return nil
	})
	if want := []string{".:a.pin,l.pin", ".errata:e.pin", "gone:c.pin", "sub:b.pin"}; err != nil || !slices.Equal(walked, want) {
		t.Errorf("Walk on the witness: %q, %v; want %q", walked, err, want)
	}
	d, err := OpenDir(ctx, fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var listed []string
	var entries []fs.DirEntry
	for {
		batch, err := d.ReadDir(2)
		for _, e := range batch {
			listed = append(listed, fmt.Sprintf("%s:%t:%v", e.Name(), e.IsDir(), e.Type()))
		}
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{".errata:true:d---------", "a.pin:false:----------", "gone:true:d---------", "l.pin:false:L---------", "sub:true:d---------"}; !slices.Equal(listed, want) {
		t.Errorf("OpenDir on the witness, two at a time: %q, want %q", listed, want)
	}
	for _, e := range entries {
		_, err := e.Info()
		extracttest.WantError(t, err, Quote(e.Name())+" is listed from git's index, which gives no file information")
	}
}

// On a witness a path git's index lists that the disk does not hold in
// that form is unknown, never a file that is not there, since a clone
// holds it: gone/c.pin deleted from the disk; d, a committed directory,
// replaced by an untracked link to a directory that holds x; f, a
// committed file, replaced by a directory; g, a committed file, replaced
// by a link to the committed kd/z; k, a committed link to kd, replaced by
// a directory that holds z. Read, Stat and Resolved each refuse with the
// entry to restore, and errors.Is does not read it as not there. A name
// below a committed file deleted from the disk, f2/x, is no file of a
// clone either, and stays not there. Each failed on dfc8360, which read
// each as not there or, for f, as a directory. Planted breaks: missing's
// refusal read as not there; form passing every path the index lists;
// form's pass of a name below a committed file dropped, which refused
// f2/x; form's link test for a committed file at the last name dropped,
// which followed g and read it as not there.
func TestAWitnessRefusesACommittedPathTheDiskHoldsOtherwise(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		".errata/witness/w/gone/c.pin": "c",
		".errata/witness/w/d/x":        "x",
		".errata/witness/w/f":          "f",
		".errata/witness/w/f2":         "f2",
		".errata/witness/w/g":          "g",
		".errata/witness/w/kd/z":       "z",
	})
	fixture := filepath.Join(root, ".errata", "witness", "w")
	extracttest.Symlink(t, "kd", filepath.Join(fixture, "k"))
	git(t, root, "init", "-q")
	git(t, root, "add", "-A")
	for _, p := range []string{"gone", "d", "f", "f2", "g", "k"} {
		if err := os.RemoveAll(filepath.Join(fixture, p)); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"other", "f", "k"} {
		if err := os.Mkdir(filepath.Join(fixture, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"other/x", "f/y", "k/z"} {
		if err := os.WriteFile(filepath.Join(fixture, f), []byte("local"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	extracttest.Symlink(t, "other", filepath.Join(fixture, "d"))
	extracttest.Symlink(t, "kd/z", filepath.Join(fixture, "g"))
	held, err := os.OpenRoot(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, err := OnFixture(check.OnWitness(t.Context(), root), fixture, held)
	if err != nil {
		t.Fatal(err)
	}
	const reads = " in git's index below the fixture, and the disk does not hold it as one, so a clone of the repository holds what this run cannot read; restore it, or commit what the disk holds"
	for file, why := range map[string]string{
		"gone/c.pin": "gone/c.pin needs gone, which is a directory" + reads,
		"d/x":        "d/x needs d, which is a directory" + reads,
		"f":          "f is a file" + reads,
		"g":          "g is a file" + reads,
		"k/z":        "k/z needs k, which is a symlink" + reads,
	} {
		s := Source{File: file, Limit: 10}
		_, err := s.Read(ctx, fixture)
		_, statErr := s.Stat(ctx, fixture)
		_, resolvedErr := s.Resolved(ctx, fixture)
		for op, err := range map[string]error{"Read": err, "Stat": statErr, "Resolved": resolvedErr} {
			if err == nil || !strings.HasSuffix(err.Error(), why) || errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s of %s: %v, want %q, not read as not there", op, file, err, why)
			}
		}
	}
	if _, err := (Source{File: "f2/x"}).Stat(ctx, fixture); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat of f2/x, below a committed file the disk lacks: %v, want not there", err)
	}
}

// A listing of git's index reads as an *os.File's ReadDir reads a
// directory of the same three files: n <= 0 gives every entry left and no
// error, even once none is left; n > 0 gives at most n, and io.EOF once
// none is left; n at or past what is left gives the rest. Planted breaks,
// the mutants the gate on dfc8360 left alive: n > 0 made n >= 0, which
// gave io.EOF for ReadDir(0) once read; n <= 0 made n < 0, which gave no
// entry for ReadDir(0).
func TestAnIndexListingReadsAsADirectoryDoes(t *testing.T) {
	dir := t.TempDir()
	var entries []gitindex.Entry
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, gitindex.Entry{Path: name, Mode: 0o100644})
	}
	for _, reads := range [][]int{{0, 0}, {-1, 1}, {3, 1, 0}, {2, 2, 1}, {5, 0}, {1, 2, 0, 1}} {
		f, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		l := &indexListing{entries: slices.Clone(entries)}
		for _, n := range reads {
			want, wantErr := f.ReadDir(n)
			got, err := l.ReadDir(n)
			if len(got) != len(want) || !errors.Is(err, wantErr) {
				t.Errorf("reads %v, ReadDir(%d): %d entries, %v; a directory gives %d, %v", reads, n, len(got), err, len(want), wantErr)
			}
		}
		_ = f.Close()
	}
}
