package core

import (
	"fmt"
	"os"
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
		{[]string{"a\tb.yaml"}, `yaml: file "a\tb.yaml" holds a control character, which a message would print as it is; rename the file`},
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
	got, err := f.List(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Named{{"z.yaml", []int{0}}, {"d/b.yaml", []int{1, 2}}, {"d/e/a.yaml", []int{1}}, {"missing.yaml", []int{3}}}
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
	if got, err := plain.List(root); err != nil || len(got) != 1 {
		t.Errorf("a plain list outside git: %v, %v", got, err)
	}
	glob, _ := CheckFiles("json", []string{"*.yaml"}, 0)
	_, err := glob.List(root)
	extracttest.WantError(t, err, `json: files lists the glob "*.yaml": the repository root holds no .git`)
}

// The files are held to FileLimit, witnessed at 2 with the message naming
// MaxFiles, and the globs' weights times the paths' bytes to MatchLimit,
// witnessed at the exact count: a/**/b/*.c weighs 3 and *.c 1, over 13
// bytes of path, 52. Planted breaks: either bound dropped, or one more;
// each glob weighed 1.
func TestListBounds(t *testing.T) {
	root := repo(t, map[string]string{"a.c": "", "b.c": "", "a/b/c.c": ""}) // 3, 3 and 7 bytes
	f, _ := CheckFiles("regex", []string{"*.c"}, 0)
	f.FileLimit = 2
	if _, err := f.List(root); err != nil {
		t.Errorf("2 files under a limit of 2: %v", err)
	}
	f, _ = CheckFiles("regex", []string{"*.c", "a/b/*.c"}, 0)
	f.FileLimit = 2
	_, err := f.List(root)
	extracttest.WantError(t, err, fmt.Sprintf(`regex: files names more than %d files, the limit, at "a/b/*.c"; narrow the glob`, MaxFiles))

	f, _ = CheckFiles("regex", []string{"a/**/b/*.c", "*.c"}, 0)
	f.MatchLimit = 4 * 13
	if _, err := f.List(root); err != nil {
		t.Errorf("52 bytes under a limit of 52: %v", err)
	}
	f.MatchLimit = 4*13 - 1
	_, err = f.List(root)
	extracttest.WantError(t, err, fmt.Sprintf("regex: files lists 2 globs, and matching each against the 13 bytes of path git tracks takes more than %d bytes, the limit", MaxMatchBytes))
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
