package filestat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// builds are the two kinds, by name.
var builds = map[string]func(Spec) (check.Extractor, error){"exists": BuildExists, "size": BuildSize}

// repo is a repository with a file, a file behind a link, a directory and a
// link to it, a link that dangles inside, two links that lead out (to a file
// that is there and to one that is not) and a loop of links.
func repo(t *testing.T) string {
	t.Helper()
	root := extracttest.Repo(t, map[string]string{"f": "a: 1\n", "real/g.yaml": "abc", "empty": ""})
	extracttest.Symlink(t, "real/g.yaml", filepath.Join(root, "link"))
	extracttest.Symlink(t, "real", filepath.Join(root, "dirlink"))
	extracttest.Symlink(t, "nowhere", filepath.Join(root, "dangling"))
	extracttest.Symlink(t, "/etc/hostname", filepath.Join(root, "out"))
	extracttest.Symlink(t, "/errata-no-such-file", filepath.Join(root, "outgone"))
	extracttest.Symlink(t, "loop", filepath.Join(root, "loop"))
	return root
}

// One value, at the file as the spec wrote it and at no line: true for a
// regular file, named outright, through a link or with a .. resolved by
// name; false for no file, a link that dangles inside the repository and a
// name below a file. Planted breaks: the fs.ErrNotExist test dropped, which
// made a missing file unknown; the two texts swapped.
func TestExists(t *testing.T) {
	root := repo(t)
	for file, want := range map[string]string{
		"f":              "true",
		"./f":            "true",
		"link":           "true",
		"real/../f":      "true",
		"dirlink/g.yaml": "true",
		"empty":          "true",
		"gone":           "false",
		"real/gone":      "false",
		"dangling":       "false",
		"f/x":            "false",
	} {
		got := extracttest.MustRun(t, BuildExists, Spec{File: file}, root)
		if len(got) != 1 || got[0].Text != want || got[0].At != (check.Location{File: file}) {
			t.Errorf("%s: %+v, want one value %s at %s and no line", file, got, want, file)
		}
	}
}

// What exists cannot decide is an error, so the rule is unknown: a
// directory, named outright or through a link, which a consumer such as
// kustomize may read in a file's place; a link that leads out, whether its
// target is there or not, since errata looks at nothing outside; a loop of
// links; a root that is not there, or is a file. Planted breaks: the error
// after the fs.ErrNotExist test dropped, which read each as true; the
// directory test dropped from fileThere, which read a directory as true.
func TestExistsRefuses(t *testing.T) {
	root := repo(t)
	for _, c := range []struct{ root, file, want string }{
		{root, "real", "stat real: is a directory"},
		{root, "dirlink", "stat dirlink: is a directory"},
		{filepath.Join(root, "f"), "legacy/old.conf", "open the repository root: not a directory"},
		{root, "out", "stat out: a symlink leads outside the repository"},
		{root, "outgone", "stat outgone: a symlink leads outside the repository"},
		{root, "loop", "stat loop: too many levels of symbolic links"},
		{filepath.Join(t.TempDir(), "gone"), "f", "open the repository root: no such file or directory"},
	} {
		got, err := extracttest.Run(t, BuildExists, Spec{File: c.file}, c.root)
		if err == nil || err.Error() != c.want || got != nil {
			t.Errorf("%s: %+v, %v; want the error %q", c.file, got, err, c.want)
		}
	}
}

// One value, the size in bytes in decimal, at the file as the spec wrote
// it: of the file, of the file a link leads to, of an empty file, and of a
// sparse file of 100 MiB, over the 64 MiB a reader may read, since nothing
// is read. Planted breaks: the Lstat of the name and not of where its links
// end, which gave the link's own size; a size limit on the value, which
// refused the large file.
func TestSize(t *testing.T) {
	root := repo(t)
	big, err := os.Create(filepath.Join(root, "big"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(100 << 20); err != nil {
		t.Fatal(err)
	}
	if err := big.Close(); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{
		"f":              "5",
		"./f":            "5",
		"link":           "3",
		"dirlink/g.yaml": "3",
		"empty":          "0",
		"big":            "104857600",
	} {
		got := extracttest.MustRun(t, BuildSize, Spec{File: file}, root)
		if len(got) != 1 || got[0].Text != want || got[0].At != (check.Location{File: file}) {
			t.Errorf("%s: %+v, want one value %s at %s and no line", file, got, want, file)
		}
	}
}

// A size is only a regular file's, so for anything else the rule is
// unknown: no file, a dangling link, a directory named outright or through
// a link, a link that leads out and a loop. Planted breaks: the directory
// test dropped, which gave a directory's size; the Stat error dropped.
func TestSizeRefuses(t *testing.T) {
	root := repo(t)
	for file, want := range map[string]string{
		"gone":     "stat gone: no such file or directory",
		"dangling": "stat dangling: no such file or directory",
		"real":     "stat real: is a directory",
		"dirlink":  "stat dirlink: is a directory",
		"out":      "stat out: a symlink leads outside the repository",
		"loop":     "stat loop: too many levels of symbolic links",
	} {
		got, err := extracttest.Run(t, BuildSize, Spec{File: file}, root)
		if err == nil || err.Error() != want || got != nil {
			t.Errorf("%s: %+v, %v; want the error %q", file, got, err, want)
		}
	}
}

// Both kinds refuse at load what any file refuses, and a glob: a file with
// *, ? or [, which a files: entry reads as a glob and which would read here
// as a name that is no file, so a rule that a file stays removed would hold
// whatever the glob matched. File sets (#124) came as files: and leave
// existence over a glob undecided; this test is where deciding it starts.
// Planted break: the glob test dropped.
func TestBuildRefuses(t *testing.T) {
	for kind, build := range builds {
		for file, want := range map[string]string{
			"":               "file is required",
			"/etc/passwd":    `file "/etc/passwd" is outside the repository`,
			"../f":           `file "../f" is outside the repository`,
			"infra/*.yaml":   `file "infra/*.yaml" holds *, ? or [, which errata reads as a glob; ` + kind + " reads the one file it names",
			"a?.yaml":        `file "a?.yaml" holds *, ? or [`,
			"app/[slug].tsx": `file "app/[slug].tsx" holds *, ? or [`,
		} {
			_, err := build(Spec{File: file})
			extracttest.WantError(t, err, kind+": "+want)
		}
	}
}
