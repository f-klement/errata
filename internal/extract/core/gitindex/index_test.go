package gitindex

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core/clip"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// globbedOf is the paths of ix a glob matches against, joined with
// spaces.
func globbedOf(ix Index) string { return strings.Join(ix.Paths().paths, " ") }

// A glob of a real run leaves the fixtures of witnesses out, at the root
// and in any directory .errata; on a witness the index below the fixture
// holds the fixture's entries with its path cut off, leaving out the real
// tree's files and a sibling fixture, and a glob there leaves out a
// fixture nested in it, which Lookup still finds. Planted breaks: Paths
// without the Witnessed test; Below keeping every entry; the cut prefix
// without its slash, which kept a sibling fixture named a-b beside a.
func TestAnIndexLeavesOutTheFixturesOfWitnesses(t *testing.T) {
	ix := memIndex(t,
		".errata/other.yaml",
		".errata/witness/a-b/conf/c.yaml",
		".errata/witness/a/.errata/witness/b/x",
		".errata/witness/a/conf/a.yaml",
		"conf/a.yaml",
		"sub/.errata/witness/c/conf/d.yaml",
	)
	if got, want := globbedOf(ix), ".errata/other.yaml conf/a.yaml"; got != want {
		t.Errorf("a real run's glob lists %q, want %q", got, want)
	}
	below, err := ix.Below(".errata/witness/a")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(entryPaths(below.entries), " "), ".errata/witness/b/x conf/a.yaml"; got != want {
		t.Errorf("below the fixture: %q, want %q", got, want)
	}
	if got, want := globbedOf(below), "conf/a.yaml"; got != want {
		t.Errorf("a witness's glob lists %q, want %q", got, want)
	}
	if _, ok := below.Lookup(".errata/witness/b/x"); !ok {
		t.Errorf("the nested fixture's x is not found below the fixture")
	}
}

// Lookup finds an entry by its whole path, with its mode, in an index in
// git's order and in one out of it, and nothing at a path that only
// starts one. Planted breaks: the binary search's result taken without
// found, which gave conf/a.yaml for conf/a; the search kept for an index
// out of order, which missed b.
func TestIndexLookup(t *testing.T) {
	entries := []Entry{{Path: "a", Mode: 0o120000}, {Path: "conf/a.yaml", Mode: 0o100755}, {Path: "z", Mode: 0o100644}}
	for _, order := range []string{"sorted", "reversed"} {
		in := slices.Clone(entries)
		if order == "reversed" {
			slices.Reverse(in)
		}
		ix, err := New(in)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range entries {
			if got, ok := ix.Lookup(want.Path); !ok || got != want {
				t.Errorf("%s: %s: %v, %v", order, want.Path, got, ok)
			}
		}
		for _, p := range []string{"conf/a", "conf", "b", ""} {
			if got, ok := ix.Lookup(p); ok {
				t.Errorf("%s: %s found as %v", order, p, got)
			}
		}
	}
	if e, _ := memIndex(t, "x").Lookup("x"); e.Symlink() {
		t.Errorf("a file read as a symlink")
	}
	if !(Entry{Mode: 0o120000}).Symlink() {
		t.Errorf("a symlink not read as one")
	}
}

// An index entry that is not in the form git writes is refused on every
// run, when the index is read, so before any entry is cut or passed over:
// one that leaves a fixture once its prefix is cut, one that reaches a
// fixture's file through another directory, one with an empty name and
// one with a . name, each of which names a fixture's file in a form that
// check.Witnessed or the cut does not see. Each is inside the repository,
// so checkEntry passes it. One written to a real index is refused too.
// Planted break: the Clean test made vacuous.
func TestAnIndexRefusesANonCanonicalEntry(t *testing.T) {
	for _, entry := range []string{
		".errata/witness/a/b/../../../x",
		"x/../.errata/witness/a/y",
		".errata/witness/a//y",
		".errata/./witness/a/y",
		".errata//witness/a/y",
	} {
		_, err := New([]Entry{{Path: ".errata/witness/a/y"}, {Path: entry}})
		extracttest.WantError(t, err, "git's index lists "+clip.Quote(entry)+", a path with a . or .. name or an empty one, which git does not write; errata does not read such an index, since the path may name a file of a witness's fixture in a form that is not its own")
	}
	root := repo(t, map[string]string{".errata/witness/a/y": ""})
	index := gitIndex(2, indexEntry{path: ".errata/witness/a/y"}, indexEntry{path: ".errata/./witness/a/y"})
	if err := os.WriteFile(filepath.Join(root, ".git", "index"), index, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readIndex(root)
	extracttest.WantError(t, err, `git's index lists ".errata/./witness/a/y", a path with a . or .. name`)
}

// The index below a fixture is refused when git's index lists no file
// below it, as a clone in CI holds none, and when it lists one below the
// fixture's path spelt in another case, alone or beside the fixture's own
// files: macOS and Windows open .Errata/witness/w as .errata/witness/w, so
// the fixture would read there what it holds nowhere else. The case rule
// is decided here, on every OS, and the cut is byte exact. A fixture whose
// only entries are another fixture's below it is no refusal, and a glob
// there lists none. Planted breaks: the count of entries below the fixture
// dropped, which kept an empty index; the test of another case dropped,
// which kept a.yaml alone beside the .Errata entry; the names compared as
// one byte prefix, which missed witneſs.
func TestBelowAFixtureTheIndexDoesNotList(t *testing.T) {
	_, err := memIndex(t, "conf/a.yaml").Below(".errata/witness/w")
	extracttest.WantError(t, err, `git's index lists no file below the fixture ".errata/witness/w"; a clone holds only the files git tracks, so commit the fixture's files`)
	for _, files := range [][]string{
		{".Errata/witness/w/x.yaml"},
		{".Errata/witness/w/x.yaml", ".errata/witness/w/a.yaml"},
	} {
		_, err := memIndex(t, files...).Below(".errata/witness/w")
		extracttest.WantError(t, err, `git's index lists ".Errata/witness/w/x.yaml", below the fixture ".errata/witness/w" in another case, and macOS and Windows open the two as one directory, so the fixture would not read there as a clone holds it; rename it to ".errata/witness/w/x.yaml"`)
	}
	// ſ folds to s, so EqualFold and check.Witnessed read witneſs as
	// witness, though the two differ in length in bytes.
	_, err = memIndex(t, ".errata/witneſs/w/x", ".errata/witness/w/a.yaml").Below(".errata/witness/w")
	extracttest.WantError(t, err, `git's index lists ".errata/witneſs/w/x", below the fixture ".errata/witness/w" in another case`)
	if below, err := memIndex(t, ".errata/witness/W2/x", ".errata/witness/w/a.yaml").Below(".errata/witness/w"); err != nil || globbedOf(below) != "a.yaml" {
		t.Errorf("a sibling whose name only starts the fixture's in another case: %q, %v", globbedOf(below), err)
	}
	below, err := memIndex(t, ".errata/witness/a/.errata/witness/b/x").Below(".errata/witness/a")
	if err != nil || globbedOf(below) != "" {
		t.Errorf("a fixture holding only a nested one: %q, %v, want none", globbedOf(below), err)
	}
}

// An index out of git's order, cut below a fixture, stays out of it: every
// entry below the fixture is found by Lookup, and the span of a glob with
// a literal directory is every path, so the glob still finds its files.
// git never writes such an index.
// Planted break: Below taking its part as in git's order, which binary
// searched the reversed list and missed entries.
func TestBelowAnIndexOutOfOrder(t *testing.T) {
	files := []string{".errata/witness/w/a", ".errata/witness/w/b/c", ".errata/witness/w/d", ".errata/witness/w/e/f", "z"}
	slices.Reverse(files)
	below, err := memIndex(t, files...).Below(".errata/witness/w")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a", "b/c", "d", "e/f"} {
		if e, ok := below.Lookup(p); !ok || e.Path != p {
			t.Errorf("%s below the fixture of a reversed index: %v, %v", p, e, ok)
		}
	}
	if got, err := below.Paths().Span("b").Match(func(p string) bool { return strings.HasPrefix(p, "b/") }, func(string) bool { return true }, func(string) bool { return false }); err != nil || !slices.Equal(got, []string{"b/c"}) {
		t.Errorf("the span of b: %q, %v", got, err)
	}
}

// Below cuts only at a fixture, at the root or nested in another, since
// the witness filter of Dir and Paths reads the path left after the
// cut: below .errata/witness a fixture's x would list as w/x, below
// .errata as witness/w/x. Planted break: the test of a fixture's place
// dropped, which listed w/x.
func TestBelowCutsOnlyAtAFixture(t *testing.T) {
	ix := memIndex(t, ".errata/witness/a/.errata/witness/b/x", ".errata/witness/w/x", "conf/a.yaml")
	for _, dir := range []string{".errata", ".errata/witness", "conf", "conf/witness/w", "witness/w"} {
		_, err := ix.Below(dir)
		extracttest.WantError(t, err, clip.Quote(dir)+" is no fixture of a witness, a directory in .errata/witness, so errata does not read the index below it as one")
	}
	for dir, want := range map[string]string{".errata/witness/w": "x", ".errata/witness/a/.errata/witness/b": "x", ".errata/witness/a": ""} {
		below, err := ix.Below(dir)
		if err != nil || globbedOf(below) != want {
			t.Errorf("%s: %q, %v, want %q", dir, globbedOf(below), err, want)
		}
	}
}

// The span of a glob's literal directory in an index in git's order is
// the directory itself, when the index lists it as a file, and the paths
// below it, and nothing beside them: not ab or a.b, which sort around a/,
// nor b. An index out of git's order, and a glob with no literal
// directory, span every path. Planted breaks: the order test negated,
// which spanned every path of the sorted index and searched the unsorted
// one; the directory itself left out; the span's start searched for a
// without its slash, which took a.b.
func TestPathsSpan(t *testing.T) {
	sorted := memIndex(t, "a", "a.b", "a/x", "a/y/z", "ab", "b").Paths()
	if !sorted.Sorted() {
		t.Fatal("an index in git's order read as out of it")
	}
	sp := sorted.Span("a")
	if !slices.Equal(sp.self, []string{"a"}) || !slices.Equal(sp.under, []string{"a/x", "a/y/z"}) || sp.Bytes() != 1+3+5 {
		t.Errorf("the span of a: %q and %q, %d bytes", sp.self, sp.under, sp.Bytes())
	}
	if sp := sorted.Span("a/y"); sp.self != nil || !slices.Equal(sp.under, []string{"a/y/z"}) {
		t.Errorf("the span of a/y: %q and %q", sp.self, sp.under)
	}
	if sp := sorted.Span("c"); len(sp.self)+len(sp.under) != 0 {
		t.Errorf("the span of c, which the index does not hold: %q and %q", sp.self, sp.under)
	}
	if sp := sorted.Span(""); !slices.Equal(sp.under, sorted.paths) || sp.self != nil {
		t.Errorf("the span of no directory: %q and %q", sp.self, sp.under)
	}
	files := []string{"a", "a.b", "a/x", "a/y/z", "ab", "b"}
	slices.Reverse(files)
	unsorted := memIndex(t, files...).Paths()
	if unsorted.Sorted() {
		t.Fatal("a reversed index read as in git's order")
	}
	if sp := unsorted.Span("a"); sp.self != nil || !slices.Equal(sp.under, files) {
		t.Errorf("the span of a in a reversed index: %q and %q, want every path", sp.self, sp.under)
	}
}

// listed is a listing of Dir as name/ for a directory, name@ for a
// symlink and name for a file, joined with spaces.
func listed(entries []Entry) string {
	var out []string
	for _, e := range entries {
		switch {
		case e.Dir():
			out = append(out, e.Path+"/")
		case e.Symlink():
			out = append(out, e.Path+"@")
		default:
			out = append(out, e.Path)
		}
	}
	return strings.Join(out, " ")
}

// Dir lists the names directly below a directory, each once: a file with
// its mode, a symlink as one, and a name with paths below it as a
// directory, and nothing that only starts with the directory's name, such
// as a.b or a0 beside a/, or d!, d-x and d.y, which sort before d/. The
// listing of .errata names the directory witness, which a walk passes
// over, and a listing in a directory of witnesses or a fixture names
// nothing. An index out of git's order lists the same.
// Planted breaks: the jump past a directory's paths going one entry too
// far, which lost the name after it; the directory's mode dropped, which
// listed b as a file; the search for the start of the directory taken
// without its slash, which began the range of a/b/d at d! and lost e; the
// test of a directory of witnesses dropped, which listed the fixture w.
func TestIndexDir(t *testing.T) {
	files := []string{
		".errata/other.yaml", ".errata/witness/w/x",
		"a.b", "a/b-c", "a/b/c", "a/b/d!", "a/b/d-x", "a/b/d.y", "a/b/d/e", "a/f", "a0", "l", "z",
	}
	entries := make([]Entry, len(files))
	for i, f := range files {
		entries[i] = Entry{Path: f, Mode: 0o100644}
		if f == "l" {
			entries[i].Mode = 0o120000
		}
	}
	want := map[string]string{
		".":               ".errata/ a.b a/ a0 l@ z",
		"a":               "b-c b/ f",
		"a/b":             "c d! d-x d.y d/",
		"a/b/d":           "e",
		".errata":         "other.yaml witness/",
		".errata/witness": "",
		"z":               "",
		"q":               "",
	}
	sorted, err := New(entries)
	if err != nil {
		t.Fatal(err)
	}
	reversed := slices.Clone(entries)
	slices.Reverse(reversed)
	unsorted, err := New(reversed)
	if err != nil {
		t.Fatal(err)
	}
	for dir, w := range want {
		if got := listed(sorted.Dir(dir)); got != w {
			t.Errorf("Dir(%q): %q, want %q", dir, got, w)
		}
		got := strings.Fields(listed(unsorted.Dir(dir)))
		slices.Sort(got)
		ws := strings.Fields(w)
		slices.Sort(ws)
		if !slices.Equal(got, ws) {
			t.Errorf("Dir(%q) of the reversed index: %q, want %q", dir, got, ws)
		}
	}
	below, err := memIndex(t, ".errata/witness/w/.errata/witness/v/x", ".errata/witness/w/y").Below(".errata/witness/w")
	if err != nil {
		t.Fatal(err)
	}
	if got := listed(below.Dir(".errata/witness/v")); got != "" {
		t.Errorf("Dir of a fixture nested in the fixture: %q, want nothing", got)
	}
}

// The stages of a conflict between a file and a directory list d as a
// file and d/x below it, as git read-tree -m leaves them, and no checkout
// holds both: Below refuses the fixture that holds the pair, in an index
// in git's order and in one out of it, and keeps a sibling fixture and a
// pair whose names only start alike, d and d-e. Planted breaks: the
// pair's test made vacuous, which cut the fixture w; the sort of an index
// out of order dropped, which missed the reversed pair.
func TestBelowRefusesAFileThatIsAlsoADirectory(t *testing.T) {
	root := repo(t, map[string]string{".errata/witness/v/a": "x"})
	blob := strings.TrimSpace(git(t, root, "hash-object", "-w", ".errata/witness/v/a"))
	cmd := exec.CommandContext(t.Context(), "git", "update-index", "--index-info")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader("100644 " + blob + " 2\t.errata/witness/w/d\n100644 " + blob + " 3\t.errata/witness/w/d/x\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	ix, err := Read(root, testLimits)
	if err != nil {
		t.Fatal(err)
	}
	const why = `git's index lists ".errata/witness/w/d" both as a file and with files below it, as a merge in conflict leaves it, so no clone holds the fixture ".errata/witness/w" as it lists it; resolve the conflict`
	_, err = ix.Below(".errata/witness/w")
	extracttest.WantError(t, err, why)
	if _, err := ix.Below(".errata/witness/v"); err != nil {
		t.Errorf("the sibling fixture v: %v", err)
	}
	files := []string{".errata/witness/w/d", ".errata/witness/w/d/x", ".errata/witness/w/e"}
	slices.Reverse(files)
	_, err = memIndex(t, files...).Below(".errata/witness/w")
	extracttest.WantError(t, err, why)
	if _, err := memIndex(t, ".errata/witness/w/d", ".errata/witness/w/d-e/x").Below(".errata/witness/w"); err != nil {
		t.Errorf("d beside d-e/x: %v", err)
	}
}

// Form finds an entry with its mode, and a directory where the index
// lists paths below a name, in an index in git's order and in one out of
// it, and nothing at a name that only starts a path, such as d for d-e.
// Planted breaks: the directory's search taken without its slash, which
// found d for d-e; the scan of an index out of order dropped.
func TestIndexForm(t *testing.T) {
	files := []string{"a", "b/c/x", "d-e", "z"}
	reversed := slices.Clone(files)
	slices.Reverse(reversed)
	// "zz/" sorts after every entry, so the search for what lies below it
	// ends past the last one.
	for _, ix := range []Index{memIndex(t, files...), memIndex(t, reversed...)} {
		for p, want := range map[string]string{"a": "file", "b": "dir", "b/c": "dir", "b/c/x": "file", "d": "", "b/x": "", "q": "", "zz": ""} {
			e, ok := ix.Form(p)
			got := ""
			switch {
			case ok && e.Dir():
				got = "dir"
			case ok:
				got = "file"
			}
			if got != want || (ok && e.Path != p) {
				t.Errorf("Form(%q) of %v: %q at %q, want %q", p, ix.entries, got, e.Path, want)
			}
		}
	}
}

// sparseIndex is the Index of files, each a regular file, and of left,
// what a sparse checkout leaves out, in their order.
func sparseIndex(t *testing.T, files []string, left ...string) Index {
	t.Helper()
	var entries []Entry
	for _, f := range files {
		entries = append(entries, Entry{Path: f, Mode: 0o100644})
	}
	ix, err := New(entries, left...)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// LeftOut finds what a sparse checkout leaves out at a path: a file git
// marks skip-worktree, a directory a sparse index lists as one entry, a
// path below one, in an index in git's order and in one out of it, and
// nothing at a file the checkout holds, at one beside a directory left
// out, or at one whose name only starts with a file left out. Planted
// breaks: the directories above the path not searched, which missed
// d/e/f; the path itself not searched as a directory, which missed d.
func TestLeftOut(t *testing.T) {
	for _, order := range []string{"sorted", "reversed"} {
		left := []string{"b/y", "d/"}
		if order == "reversed" {
			slices.Reverse(left)
		}
		ix := sparseIndex(t, []string{"a", "b/x"}, left...)
		for p, want := range map[string]string{"b/y": "b/y", "d": "d/", "d/e": "d/", "d/e/f": "d/", "a": "", "b/x": "", "b/y/z": "", "d.txt": "", "b": ""} {
			got, ok := ix.LeftOut(p)
			if got != want || ok != (want != "") {
				t.Errorf("%s: LeftOut(%s) is %q, %v, want %q", order, p, got, ok, want)
			}
		}
	}
}

// A glob's span holds what a sparse checkout leaves out where the glob
// may match: below its literal directory, at it, and a directory left out
// above it; a glob with no literal directory spans all of it, and a
// witness's fixture is never in it. Planted breaks: the directories above
// not searched, which left d/ out of the span of d/e; the range below not
// taken, which left b/y out of the span of b; the Witnessed test dropped.
func TestASpanHoldsWhatASparseCheckoutLeavesOut(t *testing.T) {
	ix := sparseIndex(t, []string{"a", "b/x"}, ".errata/witness/w/f", "b/y", "c", "d/")
	for dir, want := range map[string][]string{
		"":    {"b/y", "c", "d/"},
		"b":   {"b/y"},
		"c":   {"c"},
		"d":   {"d/"},
		"d/e": {"d/"},
		"a":   nil,
	} {
		got := ix.Paths().Span(dir).left
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("the span of %q leaves out %q, want %q", dir, got, want)
		}
	}
	if got := ix.Paths().Span("b").Bytes(); got != len("b/x")+len("b/y") {
		t.Errorf("the span of b is charged %d bytes, want %d", got, len("b/x")+len("b/y"))
	}
}

// Match refuses a glob that matches a file git marks skip-worktree that the
// working tree lacks, and one that may match a path below a directory a
// sparse index lists as one entry, by the reason, and passes one that does
// neither. A skip-worktree file the working tree holds, as after git
// update-index --skip-worktree in a full checkout, is matched as any other,
// in git's order. Planted breaks: the loop over what is left out dropped;
// the directory test reading match and not under, which passed a glob over
// d/ since d/ itself is no file; present not asked, which refused b/y on
// disk; the sort dropped, which listed b/y after b/z.
func TestMatchRefusesWhatASparseCheckoutLeavesOut(t *testing.T) {
	ix := sparseIndex(t, []string{"a", "b/x", "b/z"}, "b/y", "d/")
	never := func(string) bool { return false }
	_, err := ix.Paths().Span("b").Match(func(p string) bool { return p == "b/y" }, never, never)
	extracttest.WantError(t, err, `matches "b/y", which git marks skip-worktree and the working tree does not hold, as a sparse checkout leaves a file out`)
	_, err = ix.Paths().Span("").Match(never, func(d string) bool { return d == "d" }, never)
	extracttest.WantError(t, err, `may match files below "d/", a directory the sparse checkout leaves out of the working tree, which git's sparse index lists as one entry`)
	got, err := ix.Paths().Span("").Match(func(p string) bool { return p == "b/x" }, never, never)
	if err != nil || !slices.Equal(got, []string{"b/x"}) {
		t.Errorf("a glob that meets nothing left out: %q, %v", got, err)
	}
	inB := func(p string) bool { return strings.HasPrefix(p, "b/") }
	got, err = ix.Paths().Span("b").Match(inB, never, func(p string) bool { return p == "b/y" })
	if err != nil || !slices.Equal(got, []string{"b/x", "b/y", "b/z"}) {
		t.Errorf("a skip-worktree file on disk: %q, %v; want it matched in git's order", got, err)
	}
}

// Below keeps what a sparse checkout leaves out below the fixture, with
// the fixture's path cut, so a witness's glob and read meet it as a real
// run's do. Planted break: Below dropping it.
func TestBelowKeepsWhatASparseCheckoutLeavesOut(t *testing.T) {
	ix := sparseIndex(t, []string{".errata/witness/w/a"}, ".errata/witness/w/b", ".errata/witness/w/d/", "x")
	below, err := ix.Below(".errata/witness/w")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(below.left, []string{"b", "d/"}) {
		t.Errorf("below the fixture a sparse checkout leaves out %q, want b and d/", below.left)
	}
}

// Within cuts the index to a root below the top: the paths below it, with
// its path cut, in git's order, and nothing above it or beside it, not
// app2/x, which sorts among them, nor x. A fixture below the root keeps
// its path below the root, so the real run's globs still pass over it and
// Below still finds it. What a sparse checkout leaves out below the root is
// cut too. Planted breaks: the prefix cut without its slash, which took
// app2/x as 2/x; the cut path not kept, which listed app/a.
func TestWithinCutsTheIndexToARoot(t *testing.T) {
	ix := sparseIndex(t, []string{"app/.errata/witness/w/f", "app/a", "app/b/c", "app2/x", "x"}, "app/d/", "app/e", "y")
	in, err := ix.Within("app")
	if err != nil {
		t.Fatal(err)
	}
	if got := listed(in.entries); got != ".errata/witness/w/f a b/c" {
		t.Errorf("within app: %q", got)
	}
	if got := globbedOf(in); got != "a b/c" {
		t.Errorf("a glob within app: %q, want the fixture passed over", got)
	}
	if !slices.Equal(in.left, []string{"d/", "e"}) {
		t.Errorf("within app a sparse checkout leaves out %q, want d/ and e", in.left)
	}
	if fixture, err := in.Below(".errata/witness/w"); err != nil || globbedOf(fixture) != "f" {
		t.Errorf("the fixture within app: %q, %v", globbedOf(fixture), err)
	}
}

// Within refuses a root in a witness's fixture, in .errata/witness or that
// is a directory .errata in any case, one in a directory a sparse index
// lists as one entry, one below which the index lists a file only in another
// case, and one below which it lists nothing, each by its reason. The case refusal names the index's spelling
// of the root alone, never a file below it. Planted breaks: each refusal
// removed; the case refusal quoting the entry, which named Examples/sibling.
func TestWithinRefuses(t *testing.T) {
	ix := sparseIndex(t, []string{".errata/witness/w/f", "A/Examples/secret", "Examples/sibling", "a/examples/z", "app/a", "examples/y"}, "out/")
	for dir, want := range map[string]string{
		".errata/witness":     `the root ".errata/witness" is in the fixtures of the repository's witnesses, which errata reads only on a witness's run`,
		".errata/witness/w":   `the root ".errata/witness/w" is in the fixtures of the repository's witnesses`,
		".errata/witness/w/s": `the root ".errata/witness/w/s" is in the fixtures of the repository's witnesses`,
		".errata":             `the root ".errata" is a directory .errata, which holds the repository's witnesses, read only on a witness's run`,
		"x/.Errata":           `the root "x/.Errata" is a directory .errata`,
		"out":                 `the sparse checkout leaves out "out/", the root itself, which git's sparse index lists as one entry`,
		"out/sub":             `the sparse checkout leaves out "out/", the directory that holds the root "out/sub"`,
		"examples":            `git's index spells the root "examples" as "Examples" for some of its entries, and macOS and Windows open the two as one directory, so errata cannot tell the files the root holds; give --root as git's index spells it, "Examples", or rename the directory in git`,
		"a/examples":          `git's index spells the root "a/examples" as "A/Examples" for some of its entries`,
		"none":                `git's index lists no file below the root "none"; a clone holds only the files git tracks`,
		"app/a":               `git's index lists no file below the root "app/a"`,
	} {
		_, err := ix.Within(dir)
		extracttest.WantError(t, err, want)
		// No refusal names a file outside the root: on Linux the entries
		// spelt in another case are another directory's.
		for _, outside := range []string{"sibling", "secret"} {
			if strings.Contains(err.Error(), outside) {
				t.Errorf("%s: the refusal names %q, a file outside the root: %v", dir, outside, err)
			}
		}
	}
}
