package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A glob matches one segment at a time as path.Match does, and a segment
// ** matches zero or more whole segments, at the start, in the middle and
// at the end, a run of them as one. Planted breaks: ** matching one
// segment only, which missed a/b against a/**/b; no retry after a **,
// which missed a/x/b/c/b against a/**/b; a ** that crossed only to the
// first match of what follows, the same; the trailing ** loop dropped,
// which missed a against a/**; * crossing a /.
func TestMatchGlob(t *testing.T) {
	for _, c := range []struct {
		glob, name string
		want       bool
	}{
		{"*.yaml", "a.yaml", true},
		{"*.yaml", "d/a.yaml", false},
		{"*/*.yaml", "d/a.yaml", true},
		{"d/*", "d/e/a", false},
		{"**/*.yaml", "a.yaml", true},
		{"**/*.yaml", "d/e/f/a.yaml", true},
		{"**/*.yaml", "d/e/f/a.yml", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/b/c/b", true},
		{"a/**/b", "a/x/b/c", false},
		{"a/**/b", "b/x/b", false},
		{"a/**", "a", true},
		{"a/**", "a/b/c", true},
		{"a/**", "b/c", false},
		{"**", "x/y", true},
		{"a/**/**/b", "a/b", true},
		{"a/**/b/**/c", "a/x/b/y/b/z/c", true},
		{"a/**/b/**/c", "a/x/b/y/z", false},
		{"a/**/b?", "a/x/bc", true},
		{"a/**/[bc]", "a/x/y/c", true},
		{"a**", "abc", true}, // ** inside a segment is *
		{"a**", "a/bc", false},
		{"infra/**/*.xml", "infra/wazuh/overlay/manager/local_rules.xml", true},
	} {
		if got := matchGlob(globSegments(c.glob), c.name); got != c.want {
			t.Errorf("%s against %s: %v, want %v", c.glob, c.name, got, c.want)
		}
	}
}

// A run of ** is one segment, and the weight of a glob is once without **
// and its other segments with it. Planted breaks: the run kept, which
// weighed a/**/**/b as its four segments in the match; the weight of a
// glob of ** only taken as 0; the longest starred segment not counted, or
// a segment without a * counted; a class's bytes counted once, as the
// review's two class shapes were; a \ not read as an escape, which opened
// a class at \[.
func TestGlobSegmentsAndWeight(t *testing.T) {
	if got := globSegments("a/**/**/b"); !slices.Equal(got, []string{"a", "**", "b"}) {
		t.Errorf("segments of a/**/**/b: %q", got)
	}
	if got := globSegments("**/**"); !slices.Equal(got, []string{"**"}) {
		t.Errorf("segments of **/**: %q", got)
	}
	long := "*" + strings.Repeat("a", 250) + "b" // 252 bytes, 32 times starBytes
	for glob, want := range map[string]int{"*.yaml": 1, "a/b/*.c": 1, "a/**/b/*.c": 3, "**": 1, "**/x": 1,
		"*.yamlxy": 1, "*.yamlxyz": 2, long: 32, "**/" + long: 32, "a/**/b/" + long: 96, strings.Repeat("a", 300) + "/*.c": 1,
		"*.[ch]": 2, "a/b/[ab]*.c": 2, `*\[` + strings.Repeat("a", 20): 3,
		"**/*" + strings.Repeat("[a]", 60) + "b": 68, "**/*" + strings.Repeat("[zyxwvutsrqa]", 20) + "b": 98} {
		if got := globWeight(globSegments(glob)); got != want {
			t.Errorf("weight of %s: %d, want %d", glob, got, want)
		}
	}
}

// CheckFiles refuses what a file: refuses, an entry named twice once
// cleaned, a glob segment path.Match cannot read, and more than MaxEntries
// entries; it keeps each entry cleaned, with slashes, and says which are
// globs. Planted breaks: the duplicate check on the text as written, which
// let ./a and a through; a bad segment after a ** not checked.
func TestCheckFiles(t *testing.T) {
	f, err := CheckFiles("yaml", []string{"./a.yaml", "d/**/*.yaml"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Entries; len(got) != 2 || got[0].Clean != "a.yaml" || got[0].Glob || got[1].Clean != "d/**/*.yaml" || !got[1].Glob {
		t.Errorf("entries: %+v", got)
	}
	if f.FileLimit != MaxFiles || f.MatchLimit != MaxMatchBytes {
		t.Errorf("limits %d and %d, want %d and %d", f.FileLimit, f.MatchLimit, MaxFiles, MaxMatchBytes)
	}
	for _, c := range []struct {
		files []string
		want  string
	}{
		{[]string{"a.yaml", "./a.yaml"}, `yaml: files names "./a.yaml" twice`},
		{[]string{"**/[a.yaml"}, `yaml: files entry "**/[a.yaml" is no glob`},
		{[]string{"a[/]b"}, `yaml: files entry "a[/]b" is no glob`}, // loaded in v0.2.0's env
		{[]string{`a\/b*`}, `yaml: files entry "a\\/b*" is no glob`},
		{[]string{"../a.yaml"}, `yaml: file "../a.yaml" is outside the repository`},
		{[]string{"/etc/**"}, `yaml: file "/etc/**" is outside the repository`},
		// Each is a drive or climbs out as Windows reads it, refused at load
		// on every OS, as the rule refuses such a location. Planted break:
		// the check of check.Location.OutsideOnWindows removed from
		// CheckSource.
		{[]string{"C:x"}, `yaml: file "C:x" is absolute, names a drive or climbs out where a backslash separates names, as on Windows, so it is refused on every OS`},
		{[]string{"./c:/*.yaml"}, `yaml: file "./c:/*.yaml" is absolute, names a drive`},
		{[]string{`a\..\..\x`}, `yaml: file "a\\..\\..\\x" is absolute, names a drive or climbs out`},
		{[]string{`\*.yaml`}, `yaml: file "\\*.yaml" is absolute, names a drive or climbs out`},
		// Planted break: the control character check removed from CheckSource.
		{[]string{"a\tb.yaml"}, `yaml: file "a\tb.yaml" holds a control character, a direction control or a line or paragraph separator, which a terminal or a report viewer would act on; rename the file`},
		{make([]string, MaxEntries+1), fmt.Sprintf("yaml: files lists %d entries, over the limit of %d", MaxEntries+1, MaxEntries)},
	} {
		_, err := CheckFiles("yaml", c.files, 0)
		extracttest.WantError(t, err, c.want)
	}
	entries := make([]string, MaxEntries)
	for i := range entries {
		entries[i] = fmt.Sprintf("f%d", i)
	}
	if _, err := CheckFiles("yaml", entries, 0); err != nil {
		t.Errorf("%d entries: %v", MaxEntries, err)
	}
}

// List names each file once, in the entries' order and a glob's files in
// git's order, with every entry that names it; a file no glob names need
// not exist, and a glob that matches none names none. A file git does not
// track is not matched. Planted breaks: the files sorted by name, which put
// the plain entry's z.yaml last; a file a second entry names listed twice;
// the entries that name it not kept.
func TestList(t *testing.T) {
	root := repo(t, map[string]string{"z.yaml": "", "d/b.yaml": "", "d/e/a.yaml": "", "d/c.yml": ""})
	writeFile(t, root, "d/untracked.yaml", "")
	f, err := CheckFiles("yaml", []string{"z.yaml", "d/**/*.yaml", "d/b.yaml", "missing.yaml", "x/**"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.List(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Named{{File: "z.yaml", By: []int{0}}, {File: "d/b.yaml", By: []int{1, 2}}, {File: "d/e/a.yaml", By: []int{1}}, {File: "missing.yaml", By: []int{3}}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("List: %v, want %v", got, want)
	}
}

// writeFile writes body at rel under root, which git does not track until
// it is added.
func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A glob needs git's index; a list of files with no glob reads none.
// Planted break: the index read for any entry, which refused the plain
// list outside a repository.
func TestListNeedsGitForAGlob(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a.yaml": ""})
	plain, _ := CheckFiles("json", []string{"a.yaml"}, 0)
	if got, err := plain.List(t.Context(), root); err != nil || len(got) != 1 {
		t.Errorf("a plain list outside git: %v, %v", got, err)
	}
	glob, _ := CheckFiles("json", []string{"*.yaml"}, 0)
	_, err := glob.List(t.Context(), root)
	extracttest.WantError(t, err, `json: files lists the glob "*.yaml": no git repository holds the root`)
}

// The files are held to FileLimit, witnessed at 2 with the message naming
// MaxFiles, and each glob's weight times its span's bytes to MatchLimit,
// witnessed at the exact count: a/**/b/*.c weighs 3 over the 7 bytes of
// a/b/c.c, below its directory a, and *.c 1 over all 13 bytes of path, 34.
// Planted breaks: either bound dropped, or one more; each glob weighed 1.
func TestListBounds(t *testing.T) {
	root := repo(t, map[string]string{"a.c": "", "b.c": "", "a/b/c.c": ""}) // 3, 3 and 7 bytes
	f, _ := CheckFiles("regex", []string{"*.c"}, 0)
	f.FileLimit = 2
	if _, err := f.List(t.Context(), root); err != nil {
		t.Errorf("2 files under a limit of 2: %v", err)
	}
	f, _ = CheckFiles("regex", []string{"*.c", "a/b/*.c"}, 0)
	f.FileLimit = 2
	_, err := f.List(t.Context(), root)
	extracttest.WantError(t, err, fmt.Sprintf(`regex: files names more than %d files, the limit, at "a/b/*.c"; narrow the glob`, MaxFiles))

	f, _ = CheckFiles("regex", []string{"a/**/b/*.c", "*.c"}, 0)
	f.MatchLimit = 3*7 + 13
	if _, err := f.List(t.Context(), root); err != nil {
		t.Errorf("34 bytes under a limit of 34: %v", err)
	}
	f.MatchLimit = 3*7 + 13 - 1
	_, err = f.List(t.Context(), root)
	extracttest.WantError(t, err, fmt.Sprintf("regex: files lists 2 globs, and matching each against the paths git tracks, below its directory when it starts with one, reads 34 bytes of path, more than %d, the limit; list fewer globs, or start each with its directory", MaxMatchBytes))
}

// A glob's literal directory is its leading segments without a wildcard
// or a \, and none for a glob that starts with one. Planted break: a \
// read as a literal, which made a\b a directory.
func TestLiteralDir(t *testing.T) {
	for glob, want := range map[string]string{
		"deploy/*.env": "deploy", ".github/workflows/*.yml": ".github/workflows", "a/**/b/*.c": "a",
		"*/x.env": "", "**/x": "", "[ab]/x": "", "a?/x": "", `a\b/*`: "", "a/b*": "a", "a/b/c?": "a/b",
	} {
		if got := literalDir(globSegments(glob)); got != want {
			t.Errorf("%s: %q, want %q", glob, got, want)
		}
	}
}

// mixedIndex is an index in git's order of paths in and around the
// directories the globs of TestSpanMatchesAsTheWholeIndex name: deploy
// beside deploy-x, deploy.env and deploy0, which sort around deploy/, and
// c, a file where the glob c/** names a directory.
func mixedIndex() []string {
	segs := []string{"a", "b.env", "deploy", "deploy-x", "deploy.env", "deploy0", "x.yml", "z"}
	var out []string
	for _, a := range segs {
		for _, b := range segs {
			out = append(out, a+"/"+b, a+"/"+b+"/"+a)
		}
		out = append(out, a+".env")
	}
	out = append(out, "c", ".github/workflows/ci.yml", ".github/workflows/x/ci.yml", "deploy-x.env")
	slices.Sort(out)
	return slices.Compact(out)
}

// Each glob matched against its span gives what the whole index gives
// matched path by path: path.Match for a glob without **, matchGlob for
// every glob; in git's order, and in another order, where every glob
// spans the whole index. Planted breaks: the directory itself left out of
// the span, which missed c for c/**; the span's start searched for deploy
// without its /, which missed deploy/*; the order not tested, which lost
// files in the shuffled index.
func TestSpanMatchesAsTheWholeIndex(t *testing.T) {
	globs := []string{
		"deploy/*.env", "deploy/**", "deploy/**/*.env", "deploy/*/deploy", "deploy*/*", "deploy?/*",
		"a/b.en?", "a/*", "a/deploy/*", "z/**/a", "c/**", ".github/workflows/*.yml",
		".github/**/*.yml", "*/*.env", "**/*.env", "[ad]*/*", "*", "**", "x.yml/*/x.yml",
	}
	f, err := CheckFiles("yaml", globs, 0)
	if err != nil {
		t.Fatal(err)
	}
	sorted := mixedIndex()
	shuffled := slices.Clone(sorted)
	slices.Reverse(shuffled)
	for _, tracked := range [][]string{sorted, shuffled} {
		spans, _ := f.spans(memIndex(t, tracked...).Paths())
		for k, e := range f.Entries {
			var whole, byMatch []string
			for _, p := range tracked {
				if matchGlob(e.segs, p) {
					whole = append(whole, p)
				}
				if ok, _ := path.Match(e.Clean, p); ok {
					byMatch = append(byMatch, p)
				}
			}
			got, err := spans[k].Match(func(p string) bool { return matchGlob(e.segs, p) }, func(d string) bool { return matchesBelow(e.segs, d) }, func(string) bool { return false })
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, whole) {
				t.Errorf("%s: span gives %q, the whole index %q", e.Clean, got, whole)
			}
			if !strings.Contains(e.Clean, "**") && !slices.Equal(got, byMatch) {
				t.Errorf("%s: span gives %q, path.Match %q", e.Clean, got, byMatch)
			}
			if len(whole) == 0 && tracked[0] == sorted[0] {
				t.Errorf("%s matches nothing in the index, so it tests nothing", e.Clean)
			}
		}
	}
}

// A glob with a literal directory is charged for the paths below it only,
// so paths outside it, however many, do not move the charge: deploy/*.env
// over deploy/a.env and deploy/b.env reads 24 bytes beside 10 or 10000
// other paths, siblings that sort around deploy/ among them. A glob with a
// wildcard first is charged for every path, as before. Planted break:
// every glob spanning the whole index, which refused deploy/*.env.
func TestSpanChargeIgnoresPathsOutside(t *testing.T) {
	for _, n := range []int{10, 10000} {
		tracked := []string{"deploy-x/a.env", "deploy.env", "deploy/a.env", "deploy/b.env", "deploy0/a.env"}
		for i := range n {
			tracked = append(tracked, fmt.Sprintf("a/%d.env", i), fmt.Sprintf("z/%d.env", i))
		}
		slices.Sort(tracked)
		f, _ := CheckFiles("env", []string{"deploy/*.env"}, 0)
		f.MatchLimit = 24
		if err := f.checkMatch(f.spans(memIndex(t, tracked...).Paths())); err != nil {
			t.Errorf("deploy/*.env beside %d paths: %v", 2*n+3, err)
		}
		f.MatchLimit = 23
		extracttest.WantError(t, f.checkMatch(f.spans(memIndex(t, tracked...).Paths())), "reads 24 bytes of path")

		wild, _ := CheckFiles("env", []string{"*/*.env"}, 0)
		all := 0
		for _, p := range tracked {
			all += len(p)
		}
		wild.MatchLimit = all - 1
		extracttest.WantError(t, wild.checkMatch(wild.spans(memIndex(t, tracked...).Paths())), fmt.Sprintf("reads %d bytes of path", all))
	}
}

// On an index out of git's order a glob with a literal directory is
// charged for every path, and the refusal says so instead of naming its
// directory: deploy/*.env and deploy/*.yml over an index that lists z.env
// before deploy/a.env read all 17 bytes of path each, 34 in all, which a
// limit of 34 takes and lists deploy/a.env. Planted breaks: List passing
// false for whole, and spans never setting it, each of which gave the
// message of an index in order; the limit test dropped from the branch for
// an index out of order, and its > made >=, each of which refused 34 bytes
// under a limit of 34.
func TestListBoundOfAnIndexOutOfOrder(t *testing.T) {
	root := repo(t, map[string]string{"deploy/a.env": "", "z.env": ""})
	writeIndex(t, root, "z.env", "deploy/a.env")
	f, _ := CheckFiles("env", []string{"deploy/*.env", "deploy/*.yml"}, 0)
	f.MatchLimit = 34
	got, err := f.List(t.Context(), root)
	if err != nil || len(got) != 1 || got[0].File != "deploy/a.env" {
		t.Errorf("34 bytes under a limit of 34: %v, %v, want deploy/a.env", got, err)
	}
	f.MatchLimit = 33
	_, err = f.List(t.Context(), root)
	extracttest.WantError(t, err, fmt.Sprintf("env: files lists 2 globs, and matching each against every path git tracks, since the index is out of git's order and a glob's directory cannot be found in it, reads 34 bytes of path, more than %d, the limit; list fewer globs, and look at the index, which git itself refuses to read out of its order", MaxMatchBytes))
}

// CheckFiles marks an entry with *, ? or [ a glob, by HasGlob, and Clean
// cleans each entry to slashes. Planted breaks: ? not a glob; [ left out
// of HasGlob, which read [ab].yaml as a file.
func TestGlobEntriesAndClean(t *testing.T) {
	for entry, want := range map[string]bool{"a.yaml": false, "*.yaml": true, "a?.yaml": true, "[ab].yaml": true, "a/**": true} {
		f, err := CheckFiles("yaml", []string{entry}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if f.Entries[0].Glob != want {
			t.Errorf("%q: Glob is %v", entry, !want)
		}
	}
	if got := Clean([]string{"./a/../b", "c//d"}); !slices.Equal(got, []string{"b", "c/d"}) {
		t.Errorf("Clean: %q", got)
	}
}

// BenchmarkMatchGlob measures the time a glob takes per byte of path and
// per unit of its weight, the figure MaxMatchBytes is set from, about 33 ns
// a byte on a worker node: ordinary globs over 10000 paths of six
// segments, and the review's costly shapes over 2000 paths of sixteen
// segments of 255 bytes, and of 1000 segments.
func BenchmarkMatchGlob(b *testing.B) {
	short := make([]string, 10000)
	for i := range short {
		short[i] = fmt.Sprintf("infra/component%d/overlay/manager/sub/file%d.yaml", i%97, i)
	}
	seg := strings.Repeat("a", 255)
	long := make([]string, 2000)
	for i := range long {
		long[i] = strings.Repeat(seg+"/", 15) + seg
	}
	deep := make([]string, 200)
	for i := range deep {
		deep[i] = strings.Repeat("a/", 1000) + "c"
	}
	for _, c := range []struct {
		glob  string
		paths []string
	}{
		{"infra/*/overlay/manager/sub/*.yaml", short},
		{"infra/**/*.yaml", short},
		{"**/manager/**/*.yml", short},
		{"**/*" + strings.Repeat("a", 250) + "b", long},
		{"**/*" + strings.Repeat("[a]", 60) + "b", long},
		{"**/*" + strings.Repeat("[zyxwvutsrqa]", 20) + "b", long},
		{"**/" + strings.Repeat("*a", 120) + "b", long},
		{"**/*.yaml", long},
		{strings.Repeat("**/a/", 30) + "b", deep},
		{"**/" + strings.Repeat("a/", 60) + "b", deep},
	} {
		segs := globSegments(c.glob)
		bytes := 0
		for _, p := range c.paths {
			bytes += len(p)
		}
		name := c.glob
		if len(name) > 40 {
			name = name[:40]
		}
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				for _, p := range c.paths {
					matchGlob(segs, p)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(bytes*globWeight(segs)), "ns/weighed-byte")
		})
	}
}

// matchesBelow says whether a glob may match a path below a directory: a
// ** takes any number of the directory's names, a glob that ends at the
// directory or before it matches nothing below, and a glob whose literal
// names part from the directory's matches nothing there. Planted breaks:
// a ** that takes no more names, which missed b/c against **/x; a glob
// that ended at the directory taken, which said yes for b against b.
func TestMatchesBelow(t *testing.T) {
	for _, c := range []struct {
		glob, dir string
		want      bool
	}{
		{"**/*.yaml", "b", true},
		{"**/x", "b/c", true},
		{"b/*.yaml", "b", true},
		{"b/**", "b/c/d", true},
		{"b/c/*.yaml", "b", true},
		{"*/c/*.yaml", "b", true},
		{"*.yaml", "b", false},
		{"a/*.yaml", "b", false},
		{"b/c/*.yaml", "b/d", false},
		{"b", "b", false},
		{"b/*", "b/c", false},
		{"b/**/x", "b/c/d", true},
		{"*/*", "b/c", false},
	} {
		if got := matchesBelow(globSegments(c.glob), c.dir); got != c.want {
			t.Errorf("matchesBelow(%s, %s) = %v, want %v", c.glob, c.dir, got, c.want)
		}
	}
}

// A glob that would match a file a sparse checkout leaves out, or a path
// below a directory a sparse index lists as one entry, is refused, naming
// the sparse checkout, and one that meets neither lists what it matches,
// so a sparse checkout makes unknown only the sets it cuts. A file a
// files: entry names outright is listed, and its read refuses it
// (TestAReadOfAFileASparseCheckoutLeavesOut). Planted breaks: parseIndex
// dropping a skip-worktree file, or a sparse index's directory, again,
// which matched the glob against what the checkout holds alone.
func TestListOverASparseCheckout(t *testing.T) {
	files := map[string]string{"top.yaml": "", "a/x.yaml": "", "b/y.yaml": "", "b/c/z.yaml": ""}
	for _, c := range []struct {
		index bool
		glob  string
		want  string
	}{
		{false, "**/*.yaml", `yaml: files lists the glob "**/*.yaml", which matches "b/c/z.yaml", which git marks skip-worktree and the working tree does not hold, as a sparse checkout leaves a file out`},
		{false, "b/c/*.yaml", `yaml: files lists the glob "b/c/*.yaml", which matches "b/c/z.yaml", which git marks skip-worktree`},
		{true, "**/*.yaml", `yaml: files lists the glob "**/*.yaml", which may match files below "b/", a directory the sparse checkout leaves out of the working tree, which git's sparse index lists as one entry`},
		{true, "b/c/*.yaml", `yaml: files lists the glob "b/c/*.yaml", which may match files below "b/"`},
	} {
		root := extracttest.Sparse(t, files, c.index, "a")
		f, err := CheckFiles("yaml", []string{c.glob}, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.List(t.Context(), root)
		extracttest.WantError(t, err, c.want)
	}
	for _, index := range []bool{false, true} {
		root := extracttest.Sparse(t, files, index, "a")
		f, err := CheckFiles("yaml", []string{"*.yaml", "a/*.yaml", "b/y.yaml"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.List(t.Context(), root)
		want := []Named{{File: "top.yaml", By: []int{0}}, {File: "a/x.yaml", By: []int{1}}, {File: "b/y.yaml", By: []int{2}}}
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("sparse index %v: globs beside what is left out: %v, %v; want %v", index, got, err, want)
		}
	}
}

// A read, a stat and a resolution of a file a sparse checkout leaves out,
// marked skip-worktree or below a sparse index's directory, are refused,
// naming the sparse checkout, and none reads as a missing file, so exists
// is unknown and not false (ADR 0006); a file that no entry of the index
// names is still missing, in the same checkout and in a root with no .git.
// Planted breaks: leftOut passing err as it is, which read each as
// missing; parseIndex dropping what the checkout leaves out again.
func TestAReadOfAFileASparseCheckoutLeavesOut(t *testing.T) {
	files := map[string]string{"top.yaml": "", "a/x.yaml": "", "b/y.yaml": "", "b/c/z.yaml": ""}
	for _, c := range []struct {
		index bool
		file  string
		want  string
	}{
		{false, "b/y.yaml", `git marks "b/y.yaml" skip-worktree and the working tree does not hold it, as a sparse checkout leaves a file out`},
		{false, "./b/c/z.yaml", `git marks "b/c/z.yaml" skip-worktree and the working tree does not hold it`},
		{true, "b/y.yaml", `the sparse checkout leaves out "b/", the directory that holds it, which git's sparse index lists as one entry`},
		{true, "b/c/z.yaml", `the sparse checkout leaves out "b/", the directory that holds it`},
	} {
		root := extracttest.Sparse(t, files, c.index, "a")
		src := Source{File: c.file, Limit: DefaultMaxBytes}
		_, err := src.Stat(t.Context(), root)
		extracttest.WantError(t, err, "stat "+c.file+": "+c.want)
		if errors.Is(err, fs.ErrNotExist) {
			t.Errorf("sparse index %v: the stat of %s reads as a missing file", c.index, c.file)
		}
		_, err = src.Read(t.Context(), root)
		extracttest.WantError(t, err, "read "+c.file+": "+c.want)
		_, err = src.Resolved(t.Context(), root)
		extracttest.WantError(t, err, "read "+c.file+": "+c.want)
		_, err = Source{File: "a/gone.yaml", Limit: DefaultMaxBytes}.Stat(t.Context(), root)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("sparse index %v: a file the index does not name: %v, want a missing file", c.index, err)
		}
	}
	_, err := Source{File: "gone", Limit: DefaultMaxBytes}.Stat(t.Context(), extracttest.Repo(t, map[string]string{"a": ""}))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a root with no .git: %v, want a missing file", err)
	}
}

// A missing file in a root whose git index cannot be read is refused,
// since the index cannot say whether a sparse checkout leaves it out.
// Planted break: every error of the index's read taken as no repository.
func TestAReadMissesWhereTheIndexCannotBeRead(t *testing.T) {
	root := repo(t, map[string]string{"a": ""})
	writeFile(t, root, ".git/index", "not an index")
	_, err := Source{File: "gone", Limit: DefaultMaxBytes}.Stat(t.Context(), root)
	extracttest.WantError(t, err, "stat gone: it is not in the working tree, and git's index, which says whether a sparse checkout leaves it out, cannot be read: read git's index: it does not start with DIRC")
}

// Tracked lists each files: key as List does, against the index it read at
// its first glob: a .git taken away after that changes nothing, where List
// reads the index again. Planted break: Tracked reading the index for each
// key, which refused the second.
func TestTrackedReadsTheIndexOnce(t *testing.T) {
	root := repo(t, map[string]string{"a.yaml": "", "d/b.yaml": ""})
	tr := NewTracked(t.Context(), root)
	first, _ := CheckFiles("include", []string{"*.yaml"}, 0)
	if got, err := tr.List(first); err != nil || fmt.Sprint(got) != fmt.Sprint([]Named{{File: "a.yaml", By: []int{0}}}) {
		t.Fatalf("the first key: %v, %v", got, err)
	}
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	second, _ := CheckFiles("include", []string{"d/*.yaml"}, 0)
	if got, err := tr.List(second); err != nil || fmt.Sprint(got) != fmt.Sprint([]Named{{File: "d/b.yaml", By: []int{0}}}) {
		t.Errorf("the second key: %v, %v", got, err)
	}
	_, err := second.List(t.Context(), root)
	extracttest.WantError(t, err, "no git repository holds the root")
}

// skipWorktree is a full checkout of files, committed, with each of marked
// skip-worktree, as git update-index --skip-worktree marks a file whose
// local edits git should not see; the files stay on disk.
func skipWorktree(t *testing.T, files map[string]string, marked ...string) string {
	t.Helper()
	root := repo(t, files)
	git(t, root, "commit", "-q", "-m", "files")
	git(t, root, append([]string{"update-index", "--skip-worktree"}, marked...)...)
	return root
}

// In a full checkout, a file git marks skip-worktree that the working tree
// holds is listed by a glob and read as any other, in git's order; one the
// working tree lacks, once taken away, makes the glob unknown and its read
// too. Planted breaks: Span.Match asked nothing of the disk, which refused
// a/x.yaml on disk; the span of a glob with no literal directory taken as
// out of git's order, which listed a/x.yaml last.
func TestASkipWorktreeFileOnDisk(t *testing.T) {
	root := skipWorktree(t, map[string]string{"a/w.yaml": "", "a/x.yaml": "k: v\n", "a/y.yaml": ""}, "a/x.yaml")
	f, err := CheckFiles("yaml", []string{"a/*.yaml"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.List(t.Context(), root)
	want := []Named{{File: "a/w.yaml", By: []int{0}}, {File: "a/x.yaml", By: []int{0}}, {File: "a/y.yaml", By: []int{0}}}
	if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("a skip-worktree file on disk: %v, %v; want %v", got, err, want)
	}
	// A glob with no literal directory spans the whole index, and keeps
	// git's order too.
	whole, err := CheckFiles("yaml", []string{"**/*.yaml"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := whole.List(t.Context(), root); err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("a skip-worktree file on disk, by **/*.yaml: %v, %v; want %v", got, err, want)
	}
	if err := os.Remove(filepath.Join(root, "a", "x.yaml")); err != nil {
		t.Fatal(err)
	}
	_, err = f.List(t.Context(), root)
	extracttest.WantError(t, err, `which matches "a/x.yaml", which git marks skip-worktree and the working tree does not hold`)
	_, err = Source{File: "a/x.yaml", Limit: DefaultMaxBytes}.Stat(t.Context(), root)
	extracttest.WantError(t, err, `stat a/x.yaml: git marks "a/x.yaml" skip-worktree and the working tree does not hold it`)
}

// A file reached through a tracked symlink into a directory a sparse
// checkout leaves out is unknown, not missing, by the path the read
// reached: link names b/y, outside the cone. Planted break: leftOut looking
// up only the path the spec wrote, which read link as missing.
func TestAReadThroughALinkIntoWhatASparseCheckoutLeavesOut(t *testing.T) {
	for _, index := range []bool{false, true} {
		root := extracttest.Repo(t, map[string]string{"top": "", "b/y": "x"})
		extracttest.Symlink(t, "b/y", filepath.Join(root, "link"))
		git(t, root, "init", "-q")
		git(t, root, "add", "-A")
		git(t, root, "commit", "-q", "-m", "files")
		set := []string{"sparse-checkout", "set", "--cone"}
		if index {
			set = append(set, "--sparse-index")
		}
		git(t, root, set...)
		_, err := Source{File: "link", Limit: DefaultMaxBytes}.Stat(t.Context(), root)
		extracttest.WantError(t, err, "stat link: ")
		extracttest.WantError(t, err, "sparse checkout")
		if errors.Is(err, fs.ErrNotExist) {
			t.Errorf("sparse index %v: the link reads as a missing file", index)
		}
	}
}

// A root below the top of a sparse checkout finds the repository above it,
// as git does, so a file the checkout leaves out below the root is unknown
// there too, named by its path below the root; a missing file the index
// does not name is still missing. Planted breaks: repositoryOf looking only
// at the root, which read out/b as missing; rootRelative quoting the path
// from the top, which named s/out/b.
func TestARootBelowTheTopOfASparseCheckout(t *testing.T) {
	files := map[string]string{"s/top": "", "s/in/a": "", "s/out/b": ""}
	for _, index := range []bool{false, true} {
		root := filepath.Join(extracttest.Sparse(t, files, index, "s/in"), "s")
		_, err := Source{File: "out/b", Limit: DefaultMaxBytes}.Stat(t.Context(), root)
		extracttest.WantError(t, err, "stat out/b: ")
		extracttest.WantError(t, err, "sparse checkout")
		// The message names the path below the root, as every message does.
		want := map[bool]string{false: `git marks "out/b" skip-worktree`, true: `the sparse checkout leaves out "out/", the directory that holds it`}[index]
		extracttest.WantError(t, err, want)
		if strings.Contains(err.Error(), "s/out") {
			t.Errorf("sparse index %v: the message names the path from the top: %v", index, err)
		}
		_, err = Source{File: "in/gone", Limit: DefaultMaxBytes}.Stat(t.Context(), root)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("sparse index %v: a file the index does not name: %v, want a missing file", index, err)
		}
	}
}

// The look for the repository above a root passes over a .git directory
// that is no git directory, as git's discovery does: an empty .git above a
// plain directory leaves a missing file missing, and one between a root
// and the sparse checkout above it does not hide the checkout. Planted
// break: any .git taken as the repository, which made gone unknown and
// read out/b as missing.
func TestTheRepositoryAboveARootPassesOverAStrayGit(t *testing.T) {
	plain := extracttest.Repo(t, map[string]string{"p/q/a": ""})
	if err := os.Mkdir(filepath.Join(plain, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	_, err := Source{File: "gone", Limit: DefaultMaxBytes}.Stat(t.Context(), filepath.Join(plain, "p", "q"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a stray .git above a plain directory: %v, want a missing file", err)
	}
	files := map[string]string{"s/top": "", "s/in/a": "", "s/out/b": ""}
	top := extracttest.Sparse(t, files, false, "s/in")
	if err := os.Mkdir(filepath.Join(top, "s", ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	_, err = Source{File: "out/b", Limit: DefaultMaxBytes}.Stat(t.Context(), filepath.Join(top, "s"))
	extracttest.WantError(t, err, "sparse checkout")
}

// The look stops where the directory above is on another file system, as
// git's discovery stops without GIT_DISCOVERY_ACROSS_FILESYSTEM: with the
// root standing for a mount point (deviceOf replaced, since a test cannot
// mount one), the sparse checkout above it is not found, and out/b is
// missing. Planted break: the device test dropped, which found the
// checkout across the mount.
func TestTheRepositoryAboveARootStopsAtAFileSystem(t *testing.T) {
	files := map[string]string{"s/top": "", "s/in/a": "", "s/out/b": ""}
	top := extracttest.Sparse(t, files, false, "s/in")
	root := filepath.Join(top, "s")
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	was := deviceOf
	t.Cleanup(func() { deviceOf = was })
	deviceOf = func(dir string) (any, bool) {
		if dir == real {
			return 2, true
		}
		return 1, true
	}
	_, err = Source{File: "out/b", Limit: DefaultMaxBytes}.Stat(t.Context(), root)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a root on a file system of its own: %v, want a missing file", err)
	}
}

// WithFileLimit holds the files to its limit and names it in the refusal,
// where a test's lower FileLimit keeps MaxFiles in the message. Planted
// break: WithFileLimit setting FileLimit alone.
func TestWithFileLimit(t *testing.T) {
	root := repo(t, map[string]string{"a.c": "", "b.c": ""})
	f, _ := CheckFiles("marker", []string{"*.c"}, 0)
	_, err := f.WithFileLimit(1).List(t.Context(), root)
	extracttest.WantError(t, err, `marker: files names more than 1 files, the limit, at "*.c"`)
	if _, err := f.WithFileLimit(2).List(t.Context(), root); err != nil {
		t.Errorf("2 files under a limit of 2: %v", err)
	}
}

// A file a glob matches is marked Link when git tracks it as a symlink,
// whichever entry matched it first; a file only an entry naming it outright
// names is not, though it is one. Planted break: list never marking a link.
func TestListMarksLinks(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a.c": ""})
	extracttest.Symlink(t, "a.c", filepath.Join(root, "b.c"))
	git(t, root, "init", "-q")
	git(t, root, "add", "-A")
	f, _ := CheckFiles("marker", []string{"b.c", "*.c"}, 0)
	got, err := f.List(t.Context(), root)
	want := []Named{{File: "b.c", By: []int{0, 1}, Link: true}, {File: "a.c", By: []int{1}}}
	if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("List: %v %v, want %v", got, err, want)
	}
	f, _ = CheckFiles("marker", []string{"b.c"}, 0)
	if got, err := f.List(t.Context(), root); err != nil || got[0].Link {
		t.Errorf("a file named outright: %v %v", got, err)
	}
}

// Reader.Open opens a file as Read does, refusing one over its limit, and
// ReadLimited reads it. Planted break: Open skipping the size check.
func TestReaderOpen(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a": "12345"})
	r, err := OpenReader(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	f, size, err := r.Open(Source{File: "a", Limit: 5})
	if err != nil || size != 5 {
		t.Fatalf("Open: %d %v", size, err)
	}
	data, err := Source{File: "a", Limit: 5}.ReadLimited(f)
	_ = f.Close()
	if err != nil || string(data) != "12345" {
		t.Errorf("ReadLimited: %q %v", data, err)
	}
	_, _, err = r.Open(Source{File: "a", Limit: 4})
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("Open over the limit: %v", err)
	}
}

// globAt is the files the glob lists at root, joined with spaces.
func globAt(t *testing.T, root, glob string) (string, error) {
	t.Helper()
	f, err := CheckFiles("yaml", []string{glob}, 0)
	if err != nil {
		t.Fatal(err)
	}
	named, err := f.List(t.Context(), root)
	files := make([]string, len(named))
	for i, n := range named {
		files[i] = n.File
	}
	return strings.Join(files, " "), err
}

// A glob at a root below the top of its repository matches the files git
// tracks below the root, relative to it, and none above or beside it, also
// when the root is named through a symlink from outside the repository
// (ADR 0018). Planted breaks: rootIndex handing back the top's index uncut,
// which matched a.yaml and sub/b.yaml; rootIndex looking up from the root
// as named, not from its target, which found no repository above link.
func TestAGlobBelowTheTopOfARepository(t *testing.T) {
	top := repo(t, map[string]string{"a.yaml": "", "sub/b.yaml": "", "sub/c/d.yaml": "", "sub2/e.yaml": ""})
	link := filepath.Join(t.TempDir(), "link")
	extracttest.Symlink(t, filepath.Join(top, "sub"), link)
	for _, root := range []string{filepath.Join(top, "sub"), link} {
		if got, err := globAt(t, root, "**/*.yaml"); err != nil || got != "b.yaml c/d.yaml" {
			t.Errorf("%s: %q, %v, want b.yaml c/d.yaml", root, got, err)
		}
	}
}

// The repository of a root is the nearest one above it, as git finds it:
// a repository nested in another, untracked by it, lists its own files
// for a root inside it, and the outer one's are not read. Planted break:
// repositoryOf taking only a .git file, which passed over the inner .git
// directory, and the outer one, and refused the root.
func TestAGlobInANestedRepository(t *testing.T) {
	outer := repo(t, map[string]string{"in/s/outer.yaml": ""})
	if err := os.WriteFile(filepath.Join(outer, "in", "s", "inner.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Join(outer, "in"), "init", "-q")
	git(t, filepath.Join(outer, "in"), "add", "s/inner.yaml")
	if got, err := globAt(t, filepath.Join(outer, "in", "s"), "*.yaml"); err != nil || got != "inner.yaml" {
		t.Errorf("a root in a nested repository: %q, %v, want inner.yaml", got, err)
	}
}

// A sparse checkout below a root holds a glob there to the rule of ADR
// 0008: one that may match what the checkout leaves out below the root is
// unknown, naming the sparse checkout, and one that stays in the cone
// reads as in a full checkout. Planted break: Within dropping what the
// checkout leaves out, which matched in/a.yaml alone.
func TestAGlobBelowARootInASparseCheckout(t *testing.T) {
	files := map[string]string{"s/top.yaml": "", "s/in/a.yaml": "", "s/out/b.yaml": ""}
	for _, index := range []bool{false, true} {
		root := filepath.Join(extracttest.Sparse(t, files, index, "s/in"), "s")
		_, err := globAt(t, root, "**/*.yaml")
		extracttest.WantError(t, err, "sparse checkout")
		if got, err := globAt(t, root, "in/*.yaml"); err != nil || got != "in/a.yaml" {
			t.Errorf("sparse index %v: a glob in the cone: %q, %v", index, got, err)
		}
	}
}

// A sparse checkout that leaves out a directory above the root names it
// from the top, the one name it has, and says so. Planted break:
// rootRelative taking every path for one below the root, which named "s/"
// without saying it is a path from the top.
func TestASparseDirectoryAboveTheRootIsNamedFromTheTop(t *testing.T) {
	top := extracttest.Sparse(t, map[string]string{"top": "", "in/a": "", "s/sub/b": ""}, true, "in")
	root := filepath.Join(top, "s", "sub")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	_, err := Source{File: "b", Limit: DefaultMaxBytes}.Stat(t.Context(), root)
	extracttest.WantError(t, err, `the sparse checkout leaves out "s/" (a path from the top of the repository, at or above the root), the directory that holds it`)
}

// Every read at a root in a witness's fixture, in .errata/witness or at a
// directory .errata is refused, a file:, exists and a walk as a glob: the
// files there are a planted break, read only on its witness's run. A root
// whose own path holds the names but that is the top of its repository is
// no fixture of it. Planted breaks: openTree not asking inFixture, which
// read conf/a.yaml; inFixture asking for the root's absolute path, not its
// path below the top, which refused the repository at .errata/witness/top;
// the test of a root .errata dropped, which walked witness/w; every root
// named .errata refused before the look-up, which refused a repository
// whose top is named so.
func TestEveryReadAtARootInAFixtureIsRefused(t *testing.T) {
	top := repo(t, map[string]string{".errata/witness/w/conf/a.yaml": "port: 9090\n", "conf/a.yaml": "port: 8080\n"})
	const refused = "is in the fixtures of the repository's witnesses, which errata reads only on a witness's run"
	for _, rel := range []string{".errata/witness/w", ".errata/witness", ".errata/witness/w/conf"} {
		root := filepath.Join(top, filepath.FromSlash(rel))
		_, err := Source{File: "conf/a.yaml", Limit: DefaultMaxBytes}.Read(t.Context(), root)
		extracttest.WantError(t, err, refused)
		_, err = Source{File: "a.yaml", Limit: DefaultMaxBytes}.Stat(t.Context(), root)
		extracttest.WantError(t, err, refused)
		err = Walk(t.Context(), root, DefaultWalk, func(string, string) bool { return true }, func(string, []string) error { return nil })
		extracttest.WantError(t, err, refused)
	}
	// At a root .errata the directory witness has no parent .errata, so a
	// walk would enter it: such a root is refused for every read too.
	errataRoot := filepath.Join(top, ".errata")
	_, err := Source{File: "witness/w/conf/a.yaml", Limit: DefaultMaxBytes}.Read(t.Context(), errataRoot)
	extracttest.WantError(t, err, "is a directory .errata, which holds the repository's witnesses")
	err = Walk(t.Context(), errataRoot, DefaultWalk, func(string, string) bool { return true }, func(string, []string) error { return nil })
	extracttest.WantError(t, err, "is a directory .errata, which holds the repository's witnesses")
	own := repo(t, map[string]string{".errata/witness/top/conf/a.yaml": "port: 7070\n"})
	inner := filepath.Join(own, ".errata", "witness", "top")
	git(t, inner, "init", "-q")
	if _, err := (Source{File: "conf/a.yaml", Limit: DefaultMaxBytes}).Read(t.Context(), inner); err != nil {
		t.Errorf("a repository of its own whose path holds .errata/witness: %v", err)
	}
	// A repository whose top is named .errata is no root below a top: it
	// is read, its witness/w included, since its own fixtures would sit in
	// .errata/.errata/witness.
	named := filepath.Join(extracttest.Repo(t, map[string]string{".errata/conf/a.yaml": "port: 6060\n", ".errata/witness/w/conf/a.yaml": "port: 5050\n"}), ".errata")
	git(t, named, "init", "-q")
	git(t, named, "add", "-A")
	if _, err := (Source{File: "conf/a.yaml", Limit: DefaultMaxBytes}).Read(t.Context(), named); err != nil {
		t.Errorf("a repository whose top is named .errata: %v", err)
	}
	walked := false
	err = Walk(t.Context(), named, DefaultWalk, func(string, string) bool { return true }, func(dir string, _ []string) error {
		walked = walked || dir == "witness/w/conf"
		return nil
	})
	if err != nil || !walked {
		t.Errorf("the walk of a repository whose top is named .errata: entered witness/w/conf %v, %v", walked, err)
	}
}

// The look for the repository above a root goes 64 directories up, besides
// the root: a .git 64 up is found, one 65 up is not. Planted break: the
// loop counting the root among the 64, which missed the .git 64 up.
func TestTheRepositoryAboveARootIsAtMost64Up(t *testing.T) {
	for _, up := range []int{maxAbove, maxAbove + 1} {
		top := repo(t, map[string]string{"x": ""})
		root := filepath.Join(append([]string{top}, slices.Repeat([]string{"d"}, up)...)...)
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatal(err)
		}
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := repositoryOf(real)
		realTop, _ := filepath.EvalSymlinks(top)
		if found := got == realTop; found != (up == maxAbove) {
			t.Errorf("a .git %d up: found %v", up, found)
		}
	}
}
