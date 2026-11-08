// Package gitindex reads git's own files without running git (ADR 0002),
// and holds two readers that share the finding of the git directory and
// the bounded reads of its files.
//
// The index reader (this file) reads git's index, the list of files a
// clone of the repository holds (ADR 0008), and holds every rule errata
// keeps on its entries. Its entries are reachable only through the
// methods of Index, and every list of paths they give leaves out the
// fixtures of witnesses (check.Witnessed, ADR 0015), so no reader of the
// index, in core or elsewhere, globs a planted break.
//
// The tag reader (tags.go) lists the tags on HEAD from the refs, packed
// refs and objects git writes (HeadTags), for package_version's tag.
package gitindex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core/clip"
)

// Limits are the bounds a read of the index holds to, which core sets:
// Bytes is the largest index file, and the most bytes its paths may hold
// in all; Entries is the most entries it may list; Name is the longest
// path an entry may hold.
type Limits struct {
	Bytes   int64
	Entries int
	Name    int
}

// gitFile is the most bytes of a .git file Read reads: one line, gitdir:
// and a path, which is at most l.Name long.
func (l Limits) gitFile() int64 { return int64(l.Name) + 64 }

// ErrNoRepository refuses a root that holds no .git. Read is handed the
// top of the repository that holds a run's root, or the root itself when
// none above it does (core's repositoryOf), so the message names both.
var ErrNoRepository = errors.New("no git repository holds the root: neither it nor a directory above it holds a .git, so errata cannot list the files git tracks; run errata inside a git repository")

// ErrDevice refuses a device, such as /dev/null, before it is opened.
var ErrDevice = errors.New("it is a device, which errata does not open")

// The modes of an index entry git checks out as a file: a regular file,
// executable or not, and a symlink. A gitlink (a submodule's commit) and a
// sparse index's directory are no file of this repository.
const (
	modeType    = 0o170000
	modeFile    = 0o100000
	modeSymlink = 0o120000
	modeTree    = 0o040000 // a directory, as git writes it in a tree; no entry of the index has it (Dir)
)

// The flags of an index entry: whether a second flags word follows, in
// version 3 and later, and in that word, skip-worktree, which marks a file
// a sparse checkout leaves out of the working tree.
const (
	flagExtended     = 0x4000
	flagSkipWorktree = 0x4000
	nameMask         = 0x0fff
)

// Read is git's index at the repository root root, read under l, with
// every rule on its entries applied (Index). It reads .git/index itself, and runs no
// git: the index of a worktree or a submodule is in the directory its .git
// file names. Versions 2, 3 and 4 of the index are read, with SHA-1 or
// SHA-256 object names; a split index, whose entries are in a second file,
// is refused. A root that holds no .git is refused (a root below the top
// is read through the top's index, Within),
// and so is a .git file that names a directory that is not there, an index
// that is not a regular file, and one over l.Bytes bytes. A version 4
// index writes each path as what it keeps of the path before and a
// suffix, so 65 bytes of the file can make a path of 4096: the paths are
// held to l.Bytes in all too, which no index of plain paths can pass,
// since each of them is in the file.
func Read(root string, l Limits) (Index, error) {
	dir, err := gitDir(root, l)
	if err != nil {
		return Index{}, err
	}
	data, err := readCapped(filepath.Join(dir, "index"), l.Bytes)
	if errors.Is(err, fs.ErrNotExist) {
		return New(nil) // a repository that has never added a file
	}
	if err != nil {
		return Index{}, fmt.Errorf("read git's index: %w", bare(err))
	}
	// The object format is tried, not read from the config: an index read
	// with the wrong one fails the path length and trailer checks.
	entries, left, err := parseIndex(data, sha1Size, l)
	if err != nil {
		var again error
		if entries, left, again = parseIndex(data, sha256Size, l); again != nil {
			return Index{}, fmt.Errorf("read git's index: %w", err)
		}
	}
	return New(entries, left...)
}

// Entry is one file of git's index: its path, relative to the
// repository root and written with slashes, and its mode as git writes it,
// 0o100644 or 0o100755 for a file and 0o120000 for a symlink.
type Entry struct {
	Path string
	Mode uint32
}

// Symlink reports whether git checks e out as a symlink.
func (e Entry) Symlink() bool { return e.Mode&modeType == modeSymlink }

// Dir reports whether e is a directory of a listing (Index.Dir).
func (e Entry) Dir() bool { return e.Mode&modeType == modeTree }

// Index is the files of git's index that a run reads, each rule on the
// entries applied once, where the index is read: those git ls-files lists,
// in the index's order, but each once, a file in conflict too, and without
// a submodule or what a sparse checkout leaves out, each path inside the
// repository and in the form git writes (parseIndex, checkEntry, New). What
// a sparse checkout leaves out is kept apart, as left: a file git marks
// skip-worktree, and a directory a sparse index writes as one entry, with
// the slash git ends it with. A glob that would match one of them, or a
// path below one, is refused (Span.Match), and so is a read of a file one
// of them holds (LeftOut): the working tree lacks what a clone holds
// there, so the run cannot know the set or the file. On a witness it is
// the entries below the fixture (Below). Every list of its paths leaves
// out the fixtures of witnesses (check.Witnessed), so whoever reads the
// index, no glob of a real run reads a planted break (ADR 0015); a lookup
// of one path (Lookup) finds any entry, as a read of a file: a spec names
// finds it.
type Index struct {
	entries []Entry
	left    []string // what a sparse checkout leaves out, in the index's order
	sorted  bool     // whether entries and left are each in git's order, which git itself refuses to read without
}

// New is the Index of entries, as parseIndex lists them or a test writes
// them, in their order, and of left, what a sparse checkout leaves out,
// each a file's path or a directory's with a slash after it. It refuses an
// entry with a . or .. name or an empty one though it stays inside the
// repository: git writes none (verify_path), and the tests of a fixture's
// files read the path as written. On a real run .errata/./witness/w/y
// would pass check.Witnessed and name a fixture's file; on a witness
// .errata/witness/w/a/../../../x would leave the fixture once its prefix
// is cut.
func New(entries []Entry, left ...string) (Index, error) {
	for _, e := range entries {
		if path.Clean(e.Path) != e.Path {
			return Index{}, fmt.Errorf("git's index lists %s, a path with a . or .. name or an empty one, which git does not write; errata does not read such an index, since the path may name a file of a witness's fixture in a form that is not its own", clip.Quote(e.Path))
		}
	}
	for _, p := range left {
		if name := strings.TrimSuffix(p, "/"); path.Clean(name) != name {
			return Index{}, fmt.Errorf("git's index lists %s, a path with a . or .. name or an empty one, which git does not write; errata does not read such an index, since the path may name a file of a witness's fixture in a form that is not its own", clip.Quote(p))
		}
	}
	sorted := slices.IsSortedFunc(entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) }) && slices.IsSorted(left)
	return Index{entries: entries, left: left, sorted: sorted}, nil
}

// LeftOut is what a sparse checkout leaves out at p, a path relative to
// ix's root written with slashes, or above it, and whether it leaves out
// anything there: p itself, a file git marks skip-worktree or a directory a
// sparse index lists as one entry, or a directory p is below. A directory
// is named with the slash git ends it with. Like Lookup it finds any
// entry, in the fixture of a witness too.
func (ix Index) LeftOut(p string) (string, bool) {
	has := func(q string) bool {
		if ix.sorted {
			_, found := slices.BinarySearch(ix.left, q)
			return found
		}
		return slices.Contains(ix.left, q)
	}
	if has(p) {
		return p, true
	}
	for dir := p; dir != "." && dir != "/"; dir = path.Dir(dir) {
		if has(dir + "/") {
			return dir + "/", true
		}
	}
	return "", false
}

// Below is the entries of ix below the directory dir, relative to ix's
// root and written with slashes, with dir and its slash cut from each
// path: the files a clone holds in the fixture at dir, a nested fixture's
// included, since the fixture stands for a repository. The cut is byte
// exact, as a clone on Linux names the files, and an entry below dir only
// in another case, such as .Errata/witness/w/x for .errata/witness/w, is
// refused: macOS and Windows open both as one directory, so the fixture
// would hold files there that it holds nowhere else. A dir the index lists
// no file below is refused too, as a clone in CI holds none of its files:
// a glob over it would match nothing, and under allow_empty its witness
// could hold on no file read. git tracks no empty directory, so a fixture
// a clone holds always has an entry. A dir that is no fixture, whose
// parent is not a directory witness in a directory .errata, is refused,
// since the paths left after the cut would hide the fixtures below it
// from the witness filter. What Below keeps is a part of ix, so
// the bounds ix was read under hold.
func (ix Index) Below(dir string) (Index, error) {
	prefix := dir + "/"
	segs := strings.Split(dir, "/")
	// The witness filter of Dir and Paths reads the path left after
	// the cut, so the cut must be at a fixture: below .errata/witness the
	// fixtures' own files would list as w/x.
	if n := len(segs); n < 3 || !check.WitnessesIn(segs[n-3], segs[n-2]) {
		return Index{}, fmt.Errorf("%s is no fixture of a witness, a directory in .errata/witness, so errata does not read the index below it as one", clip.Quote(dir))
	}
	below := []Entry{}
	for _, e := range ix.entries {
		if in, ok := strings.CutPrefix(e.Path, prefix); ok {
			below = append(below, Entry{Path: in, Mode: e.Mode})
			continue
		}
		if in, ok := foldedBelow(e.Path, segs); ok {
			return Index{}, fmt.Errorf("git's index lists %s, below the fixture %s in another case, and macOS and Windows open the two as one directory, so the fixture would not read there as a clone holds it; rename it to %s", clip.Quote(e.Path), clip.Quote(dir), clip.Quote(prefix+in))
		}
	}
	var left []string
	for _, p := range ix.left {
		if in, ok := strings.CutPrefix(p, prefix); ok && in != "" {
			left = append(left, in)
		}
	}
	if len(below) == 0 {
		return Index{}, fmt.Errorf("git's index lists no file below the fixture %s; a clone holds only the files git tracks, so commit the fixture's files", clip.Quote(dir))
	}
	if p, ok := fileAndDir(below, ix.sorted); ok {
		return Index{}, fmt.Errorf("git's index lists %s both as a file and with files below it, as a merge in conflict leaves it, so no clone holds the fixture %s as it lists it; resolve the conflict", clip.Quote(prefix+p), clip.Quote(dir))
	}
	// A part of entries in git's order is in it, and so is what is left once
	// one prefix is cut from every path.
	return Index{entries: below, left: left, sorted: ix.sorted}, nil
}

// Within is the entries of ix below the directory dir, relative to ix's
// root and written with slashes, with dir and its slash cut from each
// path: what git's index lists below a root that sits below the top of
// its repository (ADR 0018), so every list of paths and every lookup is
// relative to that root and names nothing above it. What a sparse
// checkout leaves out below dir is cut the same way. Each entry the
// repository's own run passes over as a witness's fixture is passed over
// still: one whose fixture is below dir keeps its path below dir, which
// check.Witnessed reads as before. Within refuses:
//
//   - a dir in a fixture of a witness, in .errata/witness, or that is a
//     directory .errata (InFixture): a planted break is read only on its
//     witness's run (ADR 0015);
//   - a dir in a directory a sparse index lists as one entry: the
//     working tree lacks every file a clone holds below the root, so no
//     glob there can know its set;
//   - an entry below dir only in another case, such as Examples/x for
//     examples, which macOS and Windows open as one directory, so the
//     root would hold files there that the cut leaves out, as Below
//     refuses one below a fixture;
//   - a dir the index lists no file below: a clone holds no such
//     directory, so a glob there would match nothing on a tree CI
//     never sees.
//
// What Within keeps is a part of ix, so the bounds ix was read under hold.
func (ix Index) Within(dir string) (Index, error) {
	if err := InFixture(dir); err != nil {
		return Index{}, err
	}
	if at, left := ix.LeftOut(dir); left && strings.HasSuffix(at, "/") {
		holds := "the directory that holds the root " + clip.Quote(dir)
		if at == dir+"/" {
			holds = "the root itself"
		}
		return Index{}, fmt.Errorf("the sparse checkout leaves out %s, %s, which git's sparse index lists as one entry, so errata cannot list the files a clone holds there; add the directory to the sparse checkout (git sparse-checkout add), or run errata in a full checkout", clip.Quote(at), holds)
	}
	prefix := dir + "/"
	segs := strings.Split(dir, "/")
	within := []Entry{}
	for _, e := range ix.entries {
		in, ok := strings.CutPrefix(e.Path, prefix)
		if !ok {
			if _, folded := foldedBelow(e.Path, segs); folded {
				// Only the index's spelling of the root is named, never
				// the file below it: on Linux that is another directory,
				// beside the root, whose files are no business of the run.
				spelt := strings.Join(strings.SplitN(e.Path, "/", len(segs)+1)[:len(segs)], "/")
				return Index{}, fmt.Errorf("git's index spells the root %s as %s for some of its entries, and macOS and Windows open the two as one directory, so errata cannot tell the files the root holds; give --root as git's index spells it, %s, or rename the directory in git so that one spelling is left", clip.Quote(dir), clip.Quote(spelt), clip.Quote(spelt))
			}
			continue
		}
		within = append(within, Entry{Path: in, Mode: e.Mode})
	}
	var left []string
	for _, p := range ix.left {
		if in, ok := strings.CutPrefix(p, prefix); ok && in != "" {
			left = append(left, in)
		}
	}
	if len(within) == 0 && len(left) == 0 {
		return Index{}, fmt.Errorf("git's index lists no file below the root %s; a clone holds only the files git tracks, so errata cannot list what this root holds", clip.Quote(dir))
	}
	return Index{entries: within, left: left, sorted: ix.sorted}, nil
}

// InFixture refuses dir, a root's path below the top of its repository
// written with slashes, when it is in the fixture of a witness or in
// .errata/witness (check.Witnessed), or is itself a directory .errata, in
// any case: the files there are a planted break, which errata reads only
// on that witness's run (ADR 0015, ADR 0018). At a root .errata the
// directory witness has no parent named .errata, so neither a glob nor a
// walk would know to pass over it. Within refuses such a root for every
// list of the index, and core for every read under it.
func InFixture(dir string) error {
	if strings.EqualFold(path.Base(dir), ".errata") {
		return fmt.Errorf("the root %s is a directory .errata, which holds the repository's witnesses, read only on a witness's run; run errata at the repository's top, or at a directory outside .errata", clip.Quote(dir))
	}
	if !check.Witnessed(dir + "/") {
		return nil
	}
	return fmt.Errorf("the root %s is in the fixtures of the repository's witnesses, which errata reads only on a witness's run; run errata at the repository's top, or at a directory outside .errata/witness", clip.Quote(dir))
}

// fileAndDir is a path entries list both as an entry and with entries
// below it, and whether there is one. git writes such a pair for the
// stages of a conflict between a file and a directory (git read-tree -m),
// and no checkout holds both. It reads the whole cut, a nested fixture's
// entries too, so a pair inside a nested fixture makes the outer witness
// unknown, though no read of it sees the nested one. sorted says entries
// are in git's order; otherwise the paths are sorted first, so each test
// is a binary search.
func fileAndDir(entries []Entry, sorted bool) (string, bool) {
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	if !sorted {
		slices.Sort(paths)
	}
	for _, p := range paths {
		i, _ := slices.BinarySearch(paths, p+"/")
		if i < len(paths) && strings.HasPrefix(paths[i], p+"/") {
			return p, true
		}
	}
	return "", false
}

// foldedBelow is what is left of p below the directory of segs when p's
// first names match segs in any case, as strings.EqualFold, and so
// check.Witnessed, match them: name by name, so a name whose folding
// changes its length in bytes, such as witneſs for witness, is matched.
func foldedBelow(p string, segs []string) (string, bool) {
	rest := p
	for _, seg := range segs {
		name, after, more := strings.Cut(rest, "/")
		if !more || !strings.EqualFold(name, seg) {
			return "", false
		}
		rest = after
	}
	return rest, true
}

// Dir is what git's index lists directly below the directory dir of ix,
// relative to ix's root and written with slashes, "." for the root: the
// names a clone holds there, each once, in git's order of the paths. Each
// is an Entry whose Path is the name: a file's has the file's mode, and a
// name with entries below it is a directory (Entry.Dir). A name listed
// both as a file and with entries below it, as a merge in conflict can
// leave it, is listed once, as the first of the two in the index's order;
// Below refuses a fixture that holds such a pair.
// A dir that is in the fixture of a witness, or is the directory witness
// in a directory .errata, lists nothing, so no listing names a fixture's
// file (check.Witnessed); the directory witness itself is listed in
// .errata, as a clone holds it, for a walk to pass over. In git's order
// the paths below one name are a range, so a directory costs one binary
// search and not a step for each path below it.
func (ix Index) Dir(dir string) []Entry {
	prefix := dir + "/"
	if dir == "." {
		prefix = ""
	}
	if check.Witnessed(prefix) {
		return nil
	}
	byPath := func(e Entry, p string) int { return strings.Compare(e.Path, p) }
	lo, hi := 0, len(ix.entries)
	if ix.sorted {
		lo, _ = slices.BinarySearchFunc(ix.entries, prefix, byPath)
		hi = lo + sort.Search(hi-lo, func(j int) bool { return !strings.HasPrefix(ix.entries[lo+j].Path, prefix) })
	}
	var out []Entry
	seen := map[string]bool{}
	for i := lo; i < hi; i++ {
		rest, ok := strings.CutPrefix(ix.entries[i].Path, prefix)
		if !ok {
			continue
		}
		name, _, below := strings.Cut(rest, "/")
		mode := ix.entries[i].Mode
		if below {
			mode = modeTree
			if ix.sorted {
				// Past the paths below name: "0" is the byte after "/".
				skip, _ := slices.BinarySearchFunc(ix.entries[i:hi], prefix+name+"0", byPath)
				i += skip - 1
			}
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, Entry{Path: name, Mode: mode})
		}
	}
	return out
}

// Lookup is the entry of ix at p, a path relative to ix's root written
// with slashes, and whether ix lists one there, in the fixture of a
// witness too: a read of a file a spec names finds it.
func (ix Index) Lookup(p string) (Entry, bool) {
	if ix.sorted {
		i, found := slices.BinarySearchFunc(ix.entries, p, func(e Entry, p string) int { return strings.Compare(e.Path, p) })
		if !found {
			return Entry{}, false
		}
		return ix.entries[i], true
	}
	// git never writes an index out of its order, so the scan over every
	// entry for each lookup is paid only on an index git itself refuses.
	i := slices.IndexFunc(ix.entries, func(e Entry) bool { return e.Path == p })
	if i < 0 {
		return Entry{}, false
	}
	return ix.entries[i], true
}

// Form is what ix lists at p, a path relative to ix's root written with
// slashes, and whether it lists anything there: the entry at p, or a
// directory (Entry.Dir) when p has entries below it. Like Lookup it finds
// any entry, in the fixture of a witness too.
func (ix Index) Form(p string) (Entry, bool) {
	if e, ok := ix.Lookup(p); ok {
		return e, true
	}
	prefix := p + "/"
	below := func(e Entry) bool { return strings.HasPrefix(e.Path, prefix) }
	if ix.sorted {
		i, _ := slices.BinarySearchFunc(ix.entries, prefix, func(e Entry, p string) int { return strings.Compare(e.Path, p) })
		if i < len(ix.entries) && below(ix.entries[i]) {
			return Entry{Path: p, Mode: modeTree}, true
		}
		return Entry{}, false
	}
	if slices.ContainsFunc(ix.entries, below) {
		return Entry{Path: p, Mode: modeTree}, true
	}
	return Entry{}, false
}

// Paths is the paths of ix a glob matches against, and what a sparse
// checkout leaves out of them, but those in the fixtures of witnesses
// (check.Witnessed), and whether they are in git's order.
func (ix Index) Paths() Paths {
	paths := make([]string, 0, len(ix.entries))
	for _, e := range ix.entries {
		if !check.Witnessed(e.Path) {
			paths = append(paths, e.Path)
		}
	}
	var left []string
	for _, p := range ix.left {
		if !check.Witnessed(p) {
			left = append(left, p)
		}
	}
	return Paths{paths: paths, left: left, sorted: ix.sorted}
}

// Paths is the paths of an Index a glob matches against, what a sparse
// checkout leaves out of them (Index), and whether both are in git's
// order.
type Paths struct {
	paths, left []string
	sorted      bool
}

// Span is the paths of Paths a glob is matched against and charged for: its literal directory, when the index holds it as a file, which
// only a glob whose segments after it are all ** matches, and the paths
// below it, in git's order; and what a sparse checkout leaves out at the
// directory, below it or above it, which Match refuses the glob for, and
// whether t is in git's order.
type Span struct {
	self, under, left []string
	sorted            bool
}

// Span is the span of a glob of literal directory dir in t: the paths
// below dir are a range of it, found by binary search, and so are those a
// sparse checkout leaves out below it; a directory it leaves out above dir
// is found by a search for each. A glob with no literal directory, or an
// index out of git's order, spans every path.
func (t Paths) Span(dir string) Span {
	if dir == "" || !t.sorted {
		return Span{under: t.paths, left: t.left, sorted: t.sorted}
	}
	s := Span{sorted: true}
	if i, found := slices.BinarySearch(t.paths, dir); found {
		s.self = t.paths[i : i+1]
	}
	s.under = below(t.paths, dir)
	for up := dir; up != "."; up = path.Dir(up) {
		if _, found := slices.BinarySearch(t.left, up+"/"); found {
			s.left = append(s.left, up+"/")
		}
	}
	if _, found := slices.BinarySearch(t.left, dir); found {
		s.left = append(s.left, dir)
	}
	// A directory left out at dir is below it too, as dir/, so the range
	// holds it; it was added above, and is not added twice.
	for _, p := range below(t.left, dir) {
		if p != dir+"/" {
			s.left = append(s.left, p)
		}
	}
	return s
}

// below is the paths of sorted, in git's order, that start with dir and a
// slash: a range, found by binary search.
func below(sorted []string, dir string) []string {
	prefix := dir + "/"
	lo, _ := slices.BinarySearch(sorted, prefix)
	hi := lo + sort.Search(len(sorted)-lo, func(j int) bool { return !strings.HasPrefix(sorted[lo+j], prefix) })
	return sorted[lo:hi]
}

// Sorted reports whether t is in git's order. An index out of it, which
// git refuses to read, spans every path for every glob (Span).
func (t Paths) Sorted() bool { return t.sorted }

// Bytes is the bytes of the paths s holds, those a sparse checkout leaves
// out included, since Match matches them too.
func (s Span) Bytes() int {
	n := 0
	for _, part := range [][]string{s.self, s.under, s.left} {
		for _, t := range part {
			n += len(t)
		}
	}
	return n
}

// Match is the paths of s that match reports true for, in git's order. A
// path that match takes and that holds a control character, a direction
// control or a line or paragraph separator is refused, so no reader opens
// the file or names it in an error (check.HoldsControl); one that no glob
// matches is passed over, so such a file elsewhere in the index breaks no
// glob. A file git marks skip-worktree that match takes is matched as any
// other when present reports the working tree holds it, as after git
// update-index --skip-worktree in a full checkout, and refused when it
// does not, as a sparse checkout leaves it out; a directory a sparse index
// lists as one entry that under reports the glob may match a path below is
// refused: the working tree lacks the files a clone holds there, so the
// run cannot know the set the glob names. under gets the directory without
// its slash.
func (s Span) Match(match, under, present func(string) bool) ([]string, error) {
	var out []string
	mixed := false // whether out holds a file of left
	for _, part := range [][]string{s.self, s.under} {
		for _, t := range part {
			if !match(t) {
				continue
			}
			if check.HoldsControl(t) {
				return nil, controlPath(t)
			}
			out = append(out, t)
		}
	}
	for _, t := range s.left {
		dir, isDir := strings.CutSuffix(t, "/")
		if isDir && !under(dir) || !isDir && !match(t) {
			continue
		}
		if check.HoldsControl(t) {
			return nil, controlPath(t)
		}
		if isDir {
			return nil, fmt.Errorf("may match files below %s, a directory the sparse checkout leaves out of the working tree, which git's sparse index lists as one entry, so errata cannot list the files the glob names; add the directory to the sparse checkout (git sparse-checkout add), or run errata in a full checkout", clip.Quote(t))
		}
		if !present(t) {
			return nil, fmt.Errorf("matches %s, which git marks skip-worktree and the working tree does not hold, as a sparse checkout leaves a file out, so errata cannot read every file the glob names; add it to the sparse checkout (git sparse-checkout add), or run errata in a full checkout", clip.Quote(t))
		}
		out = append(out, t)
		mixed = true
	}
	// A file matched from left goes into git's order among the others.
	if mixed && s.sorted {
		slices.Sort(out)
	}
	return out, nil
}

// controlPath refuses a tracked path t that holds a control character.
func controlPath(t string) error {
	return fmt.Errorf("matches %s, a tracked path that holds a control character, a direction control or a line or paragraph separator, which a terminal or a report viewer would act on; rename the file", clip.Quote(t))
}

// The sizes of an object name: SHA-1, and SHA-256, which a repository made
// with --object-format=sha256 uses.
const (
	sha1Size   = 20
	sha256Size = 32
)

// gitDir is the directory of root's repository: root/.git, or the directory
// a .git file names, as a worktree's and a submodule's do.
func gitDir(root string, l Limits) (string, error) {
	dot := filepath.Join(root, ".git")
	info, err := os.Lstat(dot)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNoRepository
	}
	if err != nil {
		return "", fmt.Errorf("read .git: %w", bare(err))
	}
	if info.IsDir() {
		if err := isGitDir(dot, l); err != nil {
			return "", fmt.Errorf(".git is no git directory: %w", err)
		}
		return dot, nil
	}
	if !info.Mode().IsRegular() {
		return "", errors.New(".git is neither a directory nor a file")
	}
	data, err := readCapped(dot, l.gitFile())
	if err != nil {
		return "", fmt.Errorf("read .git: %w", bare(err))
	}
	// git's read_gitfile_gently drops the line breaks at the end and takes
	// the rest after "gitdir: " as the path, so a second line is part of
	// it, which names no directory.
	target, ok := strings.CutPrefix(strings.TrimRight(string(data), "\r\n"), "gitdir: ")
	if !ok || target == "" {
		return "", errors.New("the file .git names no gitdir")
	}
	if strings.ContainsAny(target, "\r\n") {
		return "", errors.New("the file .git holds more than its gitdir: line, which git refuses")
	}
	// A worktree's gitdir is outside the root by design, in the main
	// repository's .git/worktrees, so no place is refused: what is read
	// there is a list of paths, each of which is read through Source,
	// inside the root.
	dir := target
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	// A gitdir that is not there, as in a worktree mounted alone into a
	// container, is no repository that tracks nothing: every glob would
	// match nothing and the rule pass on files it did not read.
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("the file .git names the git directory %s, which is not there; mount the repository the worktree or submodule belongs to", clip.Quote(target))
	}
	// A directory that is there but no git directory, such as ., / or an
	// empty mount point, holds no index either, and is refused as git
	// refuses it.
	if err := isGitDir(dir, l); err != nil {
		return "", fmt.Errorf("the file .git names %s, which is no git directory: %w", clip.Quote(target), err)
	}
	return dir, nil
}

// IsGitDir reports whether dir is a directory git takes for a git
// directory (isGitDir), read under l: git's discovery passes over a .git
// directory that is not one and looks further up.
func IsGitDir(dir string, l Limits) bool { return isGitDir(dir, l) == nil }

// isGitDir refuses a directory git does not take for a git directory, as
// setup.c's is_git_directory tests it: HEAD a file, or a symlink whose
// target starts with refs/, which validate_headref takes without following
// it, since the branch may be packed; and objects and refs directories, in
// the common directory a worktree's commondir file names when it has one.
func isGitDir(dir string, l Limits) error {
	if !headRef(filepath.Join(dir, "HEAD")) {
		return errors.New("it holds no HEAD git takes: a file, or a symlink into refs/")
	}
	common, err := commonDir(dir, l)
	if err != nil {
		return err
	}
	for _, sub := range []string{"objects", "refs"} {
		if info, err := os.Stat(filepath.Join(common, sub)); err != nil || !info.IsDir() {
			return fmt.Errorf("it holds no %s directory", sub)
		}
	}
	return nil
}

// headRef reports whether head is a HEAD git takes: a regular file, or a
// symlink into refs/, as a repository made with core.preferSymlinkRefs
// has. A Readlink that fails gives "", which is no ref.
func headRef(head string) bool {
	info, err := os.Lstat(head) //nolint:gosec // G703: head is HEAD in the git directory gitDir found below the root the caller names, which errata reads by design
	if err != nil {
		return false
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, _ := os.Readlink(head)
		return strings.HasPrefix(target, "refs/")
	}
	return info.Mode().IsRegular()
}

// readCapped reads the regular file name, refusing one over limit bytes and
// anything else that is no regular file. A device, as a symlink at
// .git/index may name, is refused before the open, which can act on it.
// The open does not block, so a FIFO put in place of the file, which git
// never checks out, is refused rather than waited on, and O_NOCTTY keeps a
// terminal swapped in after the check from becoming errata's own.
func readCapped(name string, limit int64) ([]byte, error) {
	f, err := openRegular(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the file is over the limit of %d bytes", limit)
	}
	return data, nil
}

// openRegular opens the regular file name for reading, as readCapped
// reads it, and refuses anything else. The caller closes it.
func openRegular(name string) (*os.File, error) {
	// A Stat that fails is left to the open, which fails the same way.
	if info, err := os.Stat(name); err == nil && info.Mode()&fs.ModeDevice != 0 {
		return nil, ErrDevice
	}
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("it is not a regular file")
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// parseIndex reads the entries of an index whose object names are hash
// bytes long, as git's Documentation/gitformat-index.adoc lays them out. An
// entry is 40 bytes of stat data and its mode, the object name, a flags
// word whose low 12 bits are the path's length (0xfff for 0xfff and over),
// in version 3 and later a second flags word when the first says so, and
// the path: in versions 2 and 3 whole and padded with 1 to 8 NULs to a
// multiple of 8 bytes, in version 4 as how many bytes to drop from the end
// of the path before and what to append, with one NUL. Every path is held
// to its length, so an index read with the wrong object size is refused,
// to l.Name bytes and to a path inside the repository, as git's
// verify_path holds them, and the paths in all to l.Bytes. Of a file in
// conflict, whose stages follow one another, the first entry is kept with
// its mode: stage 1's, the merge base, or stage 2's, ours, when the file
// has no base. What a sparse checkout leaves out is the second list (Index):
// each file git marks skip-worktree, once, and each directory a sparse
// index writes as one entry, of mode tree and marked skip-worktree, with
// the slash git ends its path with.
func parseIndex(data []byte, hash int, l Limits) ([]Entry, []string, error) {
	if len(data) < 12 || string(data[:4]) != "DIRC" {
		return nil, nil, errors.New("it does not start with DIRC, git's signature")
	}
	version := binary.BigEndian.Uint32(data[4:8])
	if version < 2 || version > 4 {
		return nil, nil, fmt.Errorf("it is version %d; errata reads versions 2, 3 and 4", version)
	}
	count := int(binary.BigEndian.Uint32(data[8:12]))
	if count > l.Entries {
		return nil, nil, fmt.Errorf("it lists %d entries, over the limit of %d", count, l.Entries)
	}
	fixed := 40 + hash + 2
	pos := 12
	left := l.Bytes // the bytes the paths may still hold
	unnamed := false
	entries := []Entry{}
	var prev []byte
	var leftOut []string
	var last, lastLeft string
	for range count {
		start := pos
		if len(data)-pos < fixed {
			return nil, nil, errors.New("an entry runs past the end of the file")
		}
		mode := binary.BigEndian.Uint32(data[pos+24 : pos+28])
		flags := binary.BigEndian.Uint16(data[pos+40+hash:])
		pos += fixed
		var extended uint16
		if flags&flagExtended != 0 {
			if version < 3 || len(data)-pos < 2 {
				return nil, nil, errors.New("an entry's extended flags are not where its version puts them")
			}
			extended = binary.BigEndian.Uint16(data[pos:])
			pos += 2
		}
		var name []byte
		if version == 4 {
			drop, n := indexVarint(data[pos:])
			if n == 0 || drop > len(prev) {
				return nil, nil, errors.New("an entry's path drops more than the path before holds")
			}
			pos += n
			end := bytes.IndexByte(data[pos:], 0)
			if end < 0 {
				return nil, nil, errors.New("an entry's path runs past the end of the file")
			}
			// A new slice, since prev is the path before.
			keep := len(prev) - drop
			name = make([]byte, 0, keep+end)
			name = append(name, prev[:keep]...)
			name = append(name, data[pos:pos+end]...)
			pos += end + 1
		} else {
			end := bytes.IndexByte(data[pos:], 0)
			if end < 0 {
				return nil, nil, errors.New("an entry's path runs past the end of the file")
			}
			name = data[pos : pos+end]
			pos = start + (pos-start+end+8)&^7
			if pos > len(data) {
				return nil, nil, errors.New("an entry's padding runs past the end of the file")
			}
		}
		if want := int(flags & nameMask); want != min(len(name), nameMask) {
			return nil, nil, errors.New("an entry's path is not the length its flags give")
		}
		if len(name) > l.Name {
			return nil, nil, fmt.Errorf("an entry's path is %d bytes long, over the limit of %d", len(name), l.Name)
		}
		if left -= int64(len(name)); left < 0 {
			return nil, nil, fmt.Errorf("its paths hold more than %d bytes in all, the limit; a version 4 index repeats the path before in each entry", l.Bytes)
		}
		prev = name
		// A split index's entries that replace a shared one's have no
		// path; the link extension after them is refused below.
		if len(name) == 0 {
			unnamed = true
			continue
		}
		if err := checkEntry(string(name), filepath.IsLocal); err != nil {
			return nil, nil, err
		}
		kind := mode & modeType
		file := kind == modeFile || kind == modeSymlink
		s := string(name)
		switch {
		case extended&flagSkipWorktree != 0 && kind == modeTree:
			// A sparse index's directory: its path ends with a slash.
			if !strings.HasSuffix(s, "/") {
				s += "/"
			}
			leftOut = append(leftOut, s)
		case extended&flagSkipWorktree != 0 && file:
			// A file in conflict has an entry for each stage, one after
			// another, and each names the path once.
			if s != lastLeft {
				leftOut = append(leftOut, s)
				lastLeft = s
			}
		case file && s != last:
			// No path is empty, so the first is never last's "".
			entries = append(entries, Entry{Path: s, Mode: mode})
			last = s
		}
	}
	if err := checkExtensions(data[pos:], hash); err != nil {
		return nil, nil, err
	}
	if unnamed {
		return nil, nil, errors.New("an entry has no path")
	}
	return entries, leftOut, nil
}

// checkEntry refuses an entry's path that local, filepath.IsLocal in
// production, refuses (notLocal), and on an OS where a backslash separates
// names, as on Windows, a path that holds one. git writes a backslash as
// part of a name, where Windows reads it as a separator: the path git
// tracks as one file would open another, and .errata\witness\w\x, one
// name to git, would pass check.Witnessed and path.Clean, which read
// slashes only, and name a fixture's file. There it also refuses a name
// Windows may take for the short name of .errata, such as ERRATA~1, which
// check.Witnessed does not see. Whether a backslash separates is local's
// verdict too: it does where local reads a\..\.. as climbing out, so a
// test hands it a stand-in for Windows's verdict on any OS.
// git for Windows does not write such a path; an index written on another
// OS can hold it.
func checkEntry(name string, local func(string) bool) error {
	if !local(name) {
		return notLocal(name, local)
	}
	if local(`a\..\..`) {
		return nil
	}
	if strings.Contains(name, `\`) {
		return fmt.Errorf("an entry's path %s holds a backslash, which Windows reads as a separator, so the path would name another file than the one git tracks; git for Windows does not write it", clip.Quote(name))
	}
	for _, n := range strings.Split(name, "/") {
		if shortErrata(n) {
			return fmt.Errorf("an entry's path %s holds %s, which Windows may open as .errata by its short name, so the path could name a witness's file; git for Windows does not write it", clip.Quote(name), clip.Quote(n))
		}
	}
	return nil
}

// shortPrefix leads the short name NTFS gives .errata among the first four
// names that shorten alike, in any case.
const shortPrefix = "errata~"

// shortErrata reports whether n is a short name Windows may give .errata,
// in any case: errata~ and digits, as ERRATA~1, the form NTFS gives the
// first four names that shorten alike, or the hashed form it gives from
// the fifth on, er, four hex digits, ~ and digits, as ER1A2B~1.
func shortErrata(n string) bool {
	if len(n) > len(shortPrefix) && strings.EqualFold(n[:len(shortPrefix)], shortPrefix) {
		return digits(n[len(shortPrefix):])
	}
	if len(n) > len("er1a2b~") && strings.EqualFold(n[:2], "er") && n[6] == '~' {
		for _, c := range strings.ToLower(n[2:6]) {
			if !strings.ContainsRune("0123456789abcdef", c) {
				return false
			}
		}
		return digits(n[7:])
	}
	return false
}

// digits reports whether s holds only the digits 0 to 9.
func digits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// notLocal is the refusal of an entry's path that local, filepath.IsLocal
// in production, refused. On Windows that is also a path inside the
// repository one of whose names Windows keeps for a device, such as aux,
// which a read would open in place of a file, or a path inside it that
// holds a colon, which Windows reads as a drive or a stream; the message
// names that reason rather than calling the path outside. A device name is
// a name local refuses alone, without a colon and other than "..": the
// verdict is Go's, so errata copies no list of the names, and follows Go
// where Windows does, as Windows 11 reads aux.yaml as a file. git for
// Windows does not write either; an index written on another OS can hold
// it.
func notLocal(name string, local func(string) bool) error {
	if (check.Location{File: name}).Inside() {
		for _, n := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
			if n != ".." && !strings.Contains(n, ":") && !local(n) {
				return fmt.Errorf("an entry's path %s holds %s, a name Windows keeps for a device, which a read would open in place of a file; git for Windows does not write it", clip.Quote(name), clip.Quote(n))
			}
		}
		if strings.Contains(name, ":") {
			return fmt.Errorf("an entry's path %s holds a colon, which Windows reads as a drive or a stream; git for Windows does not write it", clip.Quote(name))
		}
	}
	return fmt.Errorf("an entry's path %s leaves the repository, which git does not write", clip.Quote(name))
}

// checkExtensions refuses a split index: its link extension says that most
// entries are in a shared index beside it, which errata does not read.
// Every extension is a 4 byte signature, a 4 byte size and its data; the
// object name of the whole file ends it. More than hash bytes are left in
// the loop, at least 21, so the signature and the size are there; one cut
// before the trailer leaves less than its size.
func checkExtensions(rest []byte, hash int) error {
	for len(rest) > hash {
		// The size is tested first, so a cut extension that starts with
		// link is reported as cut, not as a split index.
		size := int(binary.BigEndian.Uint32(rest[4:8]))
		if size > len(rest)-8-hash {
			return errors.New("an extension runs past the end of the file")
		}
		if string(rest[:4]) == "link" {
			return errors.New("it is a split index, whose entries are in a shared index errata does not read; run git update-index --no-split-index")
		}
		rest = rest[8+size:]
	}
	if len(rest) != hash {
		return errors.New("it does not end with the object name of its content")
	}
	return nil
}

// indexVarint reads the number at the start of b in the encoding of git's
// varint.c, and how many bytes it took, 0 for none. Each byte but the last
// has its high bit set, and each continuation adds one before it shifts, so
// no number has two spellings. errata reads at most 8 bytes, 56 bits, far
// past any path's length, so the number fits an int.
func indexVarint(b []byte) (int, int) {
	v := 0
	for i := 0; i < len(b) && i < 8; i++ {
		c := b[i]
		if i > 0 {
			v++
		}
		v = v<<7 | int(c&0x7f)
		if c&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

// bare drops the path from a file system error, since the path there is
// absolute and differs per machine.
func bare(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
