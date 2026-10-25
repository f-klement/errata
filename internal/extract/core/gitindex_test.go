package core

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// git runs git in dir with a fixed identity and no user or system config,
// and fails the test on an error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// repo is a git repository of files, every one added, made with init's
// extra arguments.
func repo(t *testing.T, files map[string]string, init ...string) string {
	t.Helper()
	root := extracttest.Repo(t, files)
	git(t, root, append([]string{"init", "-q"}, init...)...)
	git(t, root, "add", "-A")
	return root
}

// lsFiles is git ls-files of root, as TrackedFiles lists files.
func lsFiles(t *testing.T, root string) []string {
	t.Helper()
	out := strings.TrimSuffix(git(t, root, "ls-files", "-z"), "\x00")
	if out == "" {
		return []string{}
	}
	return strings.Split(out, "\x00")
}

func tracked(t *testing.T, root string) []string {
	t.Helper()
	got, err := TrackedFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var indexFiles = map[string]string{
	"a.sh": "x", "b/c.yml": "x", "b/d/e.env": "x", ".env": "x", "z": "x",
	"long/" + strings.Repeat("n", 5000)[:200] + "/f": "x",
}

// TrackedFiles lists what git ls-files lists, in each index version git
// writes and with either object format. Planted breaks: the padding of
// versions 2 and 3 rounded from the path's start, which misread the second
// entry; version 4's drop taken from the start of the path before.
func TestTrackedFilesIsLsFiles(t *testing.T) {
	for _, c := range []struct {
		name    string
		init    []string
		version string
	}{
		{"v2", nil, "2"},
		{"v3", nil, "3"},
		{"v4", nil, "4"},
		{"sha256", []string{"--object-format=sha256"}, "2"},
		{"sha256 v4", []string{"--object-format=sha256"}, "4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, indexFiles, c.init...)
			git(t, root, "update-index", "--index-version", c.version)
			if c.version == "3" {
				// An intent to add sets the extended flags, which only
				// version 3 and later write.
				if err := os.WriteFile(filepath.Join(root, "y"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				git(t, root, "add", "-N", "y")
			}
			want := lsFiles(t, root)
			if got := tracked(t, root); !slices.Equal(got, want) {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

// A repository with no index has added nothing, and lists no file.
func TestTrackedFilesOfAnEmptyRepository(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a": "x"})
	git(t, root, "init", "-q")
	if got := tracked(t, root); len(got) != 0 {
		t.Errorf("got %q, want none", got)
	}
}

// A worktree's .git is a file naming its git directory, where its index
// is. Planted break: the index read from the main repository's .git, which
// listed the main checkout's files.
func TestTrackedFilesOfAWorktree(t *testing.T) {
	root := repo(t, map[string]string{"a": "x"})
	git(t, root, "commit", "-q", "-m", "a")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", wt)
	if err := os.WriteFile(filepath.Join(wt, "b"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", "b")
	if got := tracked(t, wt); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("got %q, want a and b", got)
	}
}

// A submodule's commit, a file a sparse checkout leaves out and the stages
// of a conflict are no file to list, or one: ls-files lists the first two,
// and each stage. Planted breaks: the mode test dropped, which listed the
// submodule; the skip-worktree test dropped; the dedupe dropped, which
// listed the conflicted file three times.
func TestTrackedFilesLeavesOutWhatIsNoFile(t *testing.T) {
	root := repo(t, map[string]string{"a": "x", "skip": "x"})
	blob := strings.TrimSpace(git(t, root, "hash-object", "-w", "a"))
	git(t, root, "update-index", "--add", "--cacheinfo", "160000,"+blob+",sub")
	git(t, root, "update-index", "--skip-worktree", "skip")
	cmd := exec.CommandContext(t.Context(), "git", "update-index", "--index-info")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader("100644 " + blob + " 1\tc\n100644 " + blob + " 2\tc\n100644 " + blob + " 3\tc\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := tracked(t, root); !slices.Equal(got, []string{"a", "c"}) {
		t.Errorf("got %q, want a and c", got)
	}
}

// Refusals, each by its reason: no .git, a .git file that names no
// directory, and a split index. Planted break: the link extension read
// past, which listed only the split index's own few entries.
func TestTrackedFilesRefuses(t *testing.T) {
	_, err := TrackedFiles(extracttest.Repo(t, map[string]string{"a": "x"}))
	extracttest.WantError(t, err, "the repository root holds no .git")

	_, err = TrackedFiles(extracttest.Repo(t, map[string]string{".git": "nothing\n"}))
	extracttest.WantError(t, err, "the file .git names no gitdir")

	root := repo(t, map[string]string{"a": "x"})
	git(t, root, "update-index", "--split-index")
	_, err = TrackedFiles(root)
	extracttest.WantError(t, err, "it is a split index")
}

// An index cut anywhere, or with a byte changed in its header or its first
// entry, is refused or read, and never read past its end. Planted break:
// the length check of an entry's fixed part dropped, which panicked on the
// index cut inside its first entry.
func TestParseIndexOfBrokenIndexes(t *testing.T) {
	root := repo(t, indexFiles)
	for _, v := range []string{"2", "4"} {
		git(t, root, "update-index", "--index-version", v)
		data, err := os.ReadFile(filepath.Join(root, ".git", "index"))
		if err != nil {
			t.Fatal(err)
		}
		for n := range data {
			if _, err := parseIndex(data[:n], sha1Size, maxPathBytes); err == nil {
				t.Errorf("v%s cut to %d bytes: read", v, n)
			}
		}
		for i := range min(len(data), 12+80) {
			for _, b := range []byte{0, 0x7f, 0xff} {
				broken := slices.Clone(data)
				broken[i] = b
				_, _ = parseIndex(broken, sha1Size, maxPathBytes)
			}
		}
	}
}

// indexEntry is one entry an index the tests write holds: its path, for
// version 4 what it drops of the path before, its mode, and whether it has
// extended flags.
type indexEntry struct {
	path     string
	drop     int
	mode     uint32
	extended uint16
	hasExt   bool
}

// header is an index header of version and count, with no entry.
func header(version, count uint32) []byte {
	b := []byte("DIRC\x00\x00\x00\x00\x00\x00\x00\x00")
	binary.BigEndian.PutUint32(b[4:], version)
	binary.BigEndian.PutUint32(b[8:], count)
	return b
}

// writeEntry is the bytes of e in an index of version with SHA-1 names, as
// git writes it: the stat data with the mode at byte 24, a zero object
// name, the flags, the extended flags, and the path, padded in versions 2
// and 3, prefix compressed in version 4. suffix is the path version 4
// appends.
func writeEntry(version uint32, e indexEntry, suffix string) []byte {
	b := make([]byte, 40+sha1Size+2)
	mode := e.mode
	if mode == 0 {
		mode = 0o100644
	}
	binary.BigEndian.PutUint32(b[24:], mode)
	flags := uint16(min(len(e.path), nameMask))
	if e.hasExt {
		flags |= flagExtended
	}
	binary.BigEndian.PutUint16(b[40+sha1Size:], flags)
	if e.hasExt {
		b = binary.BigEndian.AppendUint16(b, e.extended)
	}
	if version == 4 {
		b = append(b, putVarint(e.drop)...)
		return append(append(b, suffix...), 0)
	}
	b = append(b, e.path...)
	return append(b, make([]byte, 8-len(b)%8)...)
}

// putVarint is git's varint.c encoding of v, indexVarint's inverse.
func putVarint(v int) []byte {
	out := []byte{byte(v & 0x7f)}
	for v >>= 7; v > 0; v >>= 7 {
		v--
		out = append([]byte{0x80 | byte(v&0x7f)}, out...)
	}
	return out
}

// gitIndex is a version 2, 3 or 4 index of entries, version 4's each with
// its suffix after the drop, and its trailer.
func gitIndex(version uint32, entries ...indexEntry) []byte {
	b := header(version, uint32(len(entries)))
	prev := ""
	for _, e := range entries {
		keep := max(0, len(prev)-e.drop)
		b = append(b, writeEntry(version, e, e.path[min(keep, len(e.path)):])...)
		prev = e.path
	}
	return append(b, make([]byte, sha1Size)...)
}

// Every refusal at the boundary of what it guards, by its reason, and the
// index one byte inside it read: the 12 byte header, the entry count, an
// entry's fixed part, version 3's extended flags, version 2's padding, and
// an extension's header, an extension with data, a version 4 path with no
// suffix and paths of exactly the budget. Planted breaks: each comparison
// moved by one (< to <=, > to >=), which changed the reason or refused the
// index inside; the extended flags' length check and an extension's skip
// each computed with - for +; an extension's room computed with + for -
// in either term, which read its size into the trailer; the link
// signature tested before the size, which called a cut link extension a
// split index.
func TestParseIndexBoundaries(t *testing.T) {
	one := gitIndex(2, indexEntry{path: "abc"})
	fixed := 12 + 40 + sha1Size + 2
	ext := gitIndex(3, indexEntry{path: "abc", hasExt: true})
	padded := len(one) - sha1Size
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"a header alone", header(2, 0), "it does not end with the object name of its content"},
		{"count at the limit", header(2, uint32(DefaultWalk.Entries)), "an entry runs past the end of the file"},
		{"cut after the fixed part", one[:fixed], "an entry's path runs past the end of the file"},
		{"cut after the extended flags", ext[:fixed+2], "an entry's path runs past the end of the file"},
		{"cut inside the extended flags", ext[:fixed+1], "an entry's extended flags are not where its version puts them"},
		{"cut after the padding", one[:padded], "it does not end with the object name of its content"},
		{"cut inside the padding", one[:padded-1], "an entry's padding runs past the end of the file"},
	} {
		_, err := parseIndex(c.data, sha1Size, maxPathBytes)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// An extension of no data, its header and the trailer ending the file.
	withExt := append(slices.Clone(one[:padded]), "TREE\x00\x00\x00\x00"...)
	withExt = append(withExt, make([]byte, sha1Size)...)
	if got, err := parseIndex(withExt, sha1Size, maxPathBytes); err != nil || !slices.Equal(got, []string{"abc"}) {
		t.Errorf("an empty extension: %q, %v", got, err)
	}
	if got, err := parseIndex(ext, sha1Size, maxPathBytes); err != nil || !slices.Equal(got, []string{"abc"}) {
		t.Errorf("version 3 with extended flags: %q, %v", got, err)
	}
	// An extension of 4 bytes of data, skipped whole.
	withData := append(slices.Clone(one[:padded]), "TREE\x00\x00\x00\x04abcd"...)
	withData = append(withData, make([]byte, sha1Size)...)
	if got, err := parseIndex(withData, sha1Size, maxPathBytes); err != nil || !slices.Equal(got, []string{"abc"}) {
		t.Errorf("an extension of 4 bytes: %q, %v", got, err)
	}
	// An extension whose size runs 4 bytes past its data, into the trailer.
	short := append(slices.Clone(one[:padded]), "TREE\x00\x00\x00\x04"...)
	short = append(short, make([]byte, sha1Size)...)
	if _, err := parseIndex(short, sha1Size, maxPathBytes); err == nil || err.Error() != "an extension runs past the end of the file" {
		t.Errorf("an extension past its data: %v", err)
	}
	// A link extension cut before its size is reached is cut, not a split
	// index, which would send the user to git update-index.
	cutLink := append(slices.Clone(one[:padded]), "link\x00\x00\x00\x08"...)
	cutLink = append(cutLink, make([]byte, sha1Size)...)
	if _, err := parseIndex(cutLink, sha1Size, maxPathBytes); err == nil || err.Error() != "an extension runs past the end of the file" {
		t.Errorf("a cut link extension: %v", err)
	}
	// A version 4 path that keeps part of the one before and adds nothing.
	if got, err := parseIndex(gitIndex(4, indexEntry{path: "ab"}, indexEntry{path: "a", drop: 1}), sha1Size, maxPathBytes); err != nil || !slices.Equal(got, []string{"ab", "a"}) {
		t.Errorf("a version 4 path with no suffix: %q, %v", got, err)
	}
	// Paths of exactly the budget are read.
	if got, err := parseIndex(gitIndex(2, indexEntry{path: "abc"}, indexEntry{path: "de"}), sha1Size, 5); err != nil || len(got) != 2 {
		t.Errorf("paths of exactly the budget: %q, %v", got, err)
	}
	if maxGitFile != maxFileName+64 {
		t.Errorf("maxGitFile is %d, want maxFileName and 64", maxGitFile)
	}
}

// The header's checks, each by its reason. Planted breaks: the version
// bounds widened to 1 and 5; the entry count's limit dropped.
func TestParseIndexHeader(t *testing.T) {
	withTrailer := func(b []byte) []byte { return append(b, make([]byte, sha1Size)...) }
	for _, c := range []struct {
		data []byte
		want string
	}{
		{[]byte("DIRX\x00\x00\x00\x02\x00\x00\x00\x00"), "does not start with DIRC"},
		{header(2, 0)[:11], "does not start with DIRC"},
		{withTrailer(header(1, 0)), "it is version 1"},
		{withTrailer(header(5, 0)), "it is version 5"},
		{withTrailer(header(2, uint32(DefaultWalk.Entries+1))), "entries, over the limit of"},
	} {
		_, err := parseIndex(c.data, sha1Size, maxPathBytes)
		extracttest.WantError(t, err, c.want)
	}
	for _, v := range []uint32{2, 3, 4} {
		if got, err := parseIndex(withTrailer(header(v, 0)), sha1Size, maxPathBytes); err != nil || len(got) != 0 {
			t.Errorf("version %d with no entry: %q, %v", v, got, err)
		}
	}
}

// A path is held to 4096 bytes and to one inside the repository, and the
// paths in all to the budget, which a version 4 index of entries that each
// keep the path before passes with few bytes of its own. An entry with no
// path, which only a split index writes, is refused. Planted breaks: each
// check dropped, which read the long path, the path that leaves the
// repository, the empty path, and the 300 copies of a 4000 byte path.
func TestParseIndexPathBounds(t *testing.T) {
	long := strings.Repeat("a", maxFileName)
	if got, err := parseIndex(gitIndex(4, indexEntry{path: long}), sha1Size, maxPathBytes); err != nil || len(got) != 1 {
		t.Errorf("a path of %d bytes: %v", maxFileName, err)
	}
	_, err := parseIndex(gitIndex(4, indexEntry{path: long + "b"}), sha1Size, maxPathBytes)
	extracttest.WantError(t, err, "an entry's path is 4097 bytes long, over the limit of 4096")
	_, err = parseIndex(gitIndex(2, indexEntry{path: ""}), sha1Size, maxPathBytes)
	extracttest.WantError(t, err, "an entry has no path")
	for _, p := range []string{"../x", "/etc/passwd", "a/../../x"} {
		_, err := parseIndex(gitIndex(2, indexEntry{path: p}), sha1Size, maxPathBytes)
		extracttest.WantError(t, err, "leaves the repository, which git does not write")
	}
	// 300 entries that each drop the last byte of a 4000 byte path and add
	// one: 1.2 MB of paths from 24 KB of index.
	base := strings.Repeat("p", 3999)
	entries := []indexEntry{{path: base + "0"}}
	for i := 1; i < 300; i++ {
		entries = append(entries, indexEntry{path: base + string(rune('0'+i%10)), drop: 1})
	}
	data := gitIndex(4, entries...)
	if _, err := parseIndex(data, sha1Size, 1<<20); err == nil || !strings.Contains(err.Error(), "its paths hold more than 1048576 bytes in all") {
		t.Errorf("1.2 MB of paths under a budget of 1 MiB: %v", err)
	}
	if len(data) > 1<<15 {
		t.Errorf("the index is %d bytes, more than the shape needs", len(data))
	}
}

// git's varint: 127 is one byte, 128 two (0x80 0x00), since a
// continuation adds one; a number whose bytes all continue is refused, and
// so is one longer than 8 bytes. Planted breaks: the added one dropped,
// which read 0x80 0x00 as 0; the cap at 8 bytes moved to 9.
func TestIndexVarint(t *testing.T) {
	nine := append(bytes.Repeat([]byte{0x80}, 8), 0x00)
	eight := append(bytes.Repeat([]byte{0x80}, 7), 0x00)
	for _, c := range []struct {
		b    []byte
		v    int
		size int
	}{
		{[]byte{0x00}, 0, 1},
		{[]byte{0x7f, 0xff}, 127, 1},
		{[]byte{0x80, 0x00}, 128, 2},
		{[]byte{0x81, 0x7f}, 383, 2},
		{[]byte{0x80, 0x80}, 0, 0},
		{nil, 0, 0},
		{nine, 0, 0},
	} {
		if v, n := indexVarint(c.b); v != c.v || n != c.size {
			t.Errorf("%x: %d, %d; want %d, %d", c.b, v, n, c.v, c.size)
		}
	}
	if _, n := indexVarint(eight); n != 8 {
		t.Errorf("%x: %d bytes, want 8", eight, n)
	}
	for _, v := range []int{0, 1, 127, 128, 383, 16511, 1 << 40} {
		if got, n := indexVarint(putVarint(v)); got != v || n != len(putVarint(v)) {
			t.Errorf("%d: read %d in %d bytes", v, got, n)
		}
	}
}

// A .git file whose gitdir is not there is refused, never read as a
// repository that tracks nothing, whether its path is relative or
// absolute; a .git file of maxGitFile bytes is read and one byte more is
// refused. Planted breaks: the gitdir's check dropped, which listed no
// file; readCapped's limit taken as >=.
func TestTrackedFilesRefusesWhatIsNotThere(t *testing.T) {
	for _, gitdir := range []string{"/nonexistent/repo/.git/worktrees/x", "../.git/modules/sub"} {
		_, err := TrackedFiles(extracttest.Repo(t, map[string]string{".git": "gitdir: " + gitdir + "\n"}))
		extracttest.WantError(t, err, "the file .git names the git directory "+strconv.Quote(gitdir)+", which is not there")
	}

	main := repo(t, map[string]string{"a": "x"})
	// The line padded with line breaks to maxGitFile bytes, which git
	// drops from the end.
	line := "gitdir: " + filepath.Join(main, ".git")
	exact := line + strings.Repeat("\n", maxGitFile-len(line))
	if got, err := TrackedFiles(extracttest.Repo(t, map[string]string{".git": exact})); err != nil || !slices.Equal(got, []string{"a"}) {
		t.Errorf("a .git file of %d bytes: %q, %v", maxGitFile, got, err)
	}
	_, err := TrackedFiles(extracttest.Repo(t, map[string]string{".git": exact + "\n"}))
	extracttest.WantError(t, err, "read .git: the file is over the limit of")

}

// parseIndex never panics, and lists at most one file for each entry's
// fixed part the data holds, each a path inside the repository.
func FuzzParseIndex(f *testing.F) {
	f.Add(gitIndex(2, indexEntry{path: "a"}, indexEntry{path: "b/c"}))
	f.Add(gitIndex(3, indexEntry{path: "abc", hasExt: true, extended: flagSkipWorktree}))
	f.Add(gitIndex(4, indexEntry{path: "a/b"}, indexEntry{path: "a/c", drop: 1}))
	f.Add(header(2, 0))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, hash := range []int{sha1Size, sha256Size} {
			files, err := parseIndex(data, hash, 1<<16)
			if err != nil {
				continue
			}
			if len(files) > len(data)/(40+hash+2) {
				t.Fatalf("%d files from %d bytes", len(files), len(data))
			}
			for _, p := range files {
				if !filepath.IsLocal(p) || len(p) > maxFileName {
					t.Fatalf("listed %q", p)
				}
			}
		}
	})
}

// A gitdir that is there but no git directory, as git's is_git_directory
// tests it, is refused, never read as a repository that tracks nothing:
// ".", "/", an empty directory and a link to one, a .git directory that is
// empty, a HEAD that is a directory, a worktree's whose commondir names a
// directory without objects, and a .git file with a second line, whose path
// git reads as both lines. A worktree, whose objects and refs are in its
// commondir, is read. Planted breaks: isGitDir dropped from either caller;
// HEAD's check dropped; any HEAD that is no symlink taken, which read the
// directory; commondir not followed, which refused the worktree; the second
// line passed over.
func TestTrackedFilesRefusesWhatIsNoGitDir(t *testing.T) {
	gitFile := func(t *testing.T, files map[string]string, gitdir string) error {
		t.Helper()
		files[".git"] = "gitdir: " + gitdir + "\n"
		_, err := TrackedFiles(extracttest.Repo(t, files))
		return err
	}
	for _, c := range map[string]struct {
		files  map[string]string
		gitdir string
		want   string
	}{
		"dot":              {map[string]string{}, ".", `the file .git names ".", which is no git directory: it holds no HEAD git takes: a file, or a symlink into refs/`},
		"slash":            {map[string]string{}, "/", "which is no git directory: it holds no HEAD git takes: a file, or a symlink into refs/"},
		"empty":            {map[string]string{"empty/.keep": ""}, "empty", "which is no git directory: it holds no HEAD git takes: a file, or a symlink into refs/"},
		"no objects":       {map[string]string{"g/HEAD": "ref: refs/heads/main\n", "g/refs/x": ""}, "g", "it holds no objects directory"},
		"no refs":          {map[string]string{"g/HEAD": "ref: refs/heads/main\n", "g/objects/x": ""}, "g", "it holds no refs directory"},
		"HEAD a directory": {map[string]string{"g/HEAD/.keep": "", "g/objects/x": "", "g/refs/x": ""}, "g", "which is no git directory: it holds no HEAD git takes: a file, or a symlink into refs/"},
		"bad commondir":    {map[string]string{"g/HEAD": "x\n", "g/commondir": "../nowhere\n", "g/objects/x": "", "g/refs/x": ""}, "g", "it holds no objects directory"},
	} {
		extracttest.WantError(t, gitFile(t, c.files, c.gitdir), c.want)
	}
	link := extracttest.Repo(t, map[string]string{"empty/.keep": ""})
	extracttest.Symlink(t, "empty", filepath.Join(link, "to-empty"))
	if err := os.WriteFile(filepath.Join(link, ".git"), []byte("gitdir: to-empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := TrackedFiles(link)
	extracttest.WantError(t, err, "which is no git directory: it holds no HEAD git takes: a file, or a symlink into refs/")

	_, err = TrackedFiles(extracttest.Repo(t, map[string]string{".git/.keep": ""}))
	extracttest.WantError(t, err, ".git is no git directory: it holds no HEAD git takes: a file, or a symlink into refs/")

	main := repo(t, map[string]string{"a": "x"})
	_, err = TrackedFiles(extracttest.Repo(t, map[string]string{".git": "gitdir: " + filepath.Join(main, ".git") + "\njunk\n"}))
	extracttest.WantError(t, err, "the file .git holds more than its gitdir: line, which git refuses")
}

// A HEAD that is a symlink into refs/ is taken without following it, as
// git's validate_headref takes it, so a branch that pack-refs has packed,
// which leaves the link dangling, is still a repository. A HEAD symlink
// elsewhere is refused, as git refuses it, even when it leads to a file.
// So is a link to refsX/heads/main, which only starts with refs. Planted
// breaks: HEAD followed by Stat, which refused the dangling link; any
// symlink taken, which read the one that leads to ../other/HEAD; the
// prefix without its slash, which read the refsX link.
func TestTrackedFilesTakesAHEADLinkIntoRefs(t *testing.T) {
	files := map[string]string{".git/objects/x": "", ".git/refs/x": "", ".git/packed-refs": "0000000000000000000000000000000000000000 refs/heads/main\n", "other/HEAD": "ref: refs/heads/main\n"}
	packed := extracttest.Repo(t, files)
	extracttest.Symlink(t, "refs/heads/main", filepath.Join(packed, ".git", "HEAD"))
	if got := tracked(t, packed); len(got) != 0 {
		t.Errorf("a HEAD link to a packed branch: %q", got)
	}
	elsewhere := extracttest.Repo(t, files)
	extracttest.Symlink(t, "../other/HEAD", filepath.Join(elsewhere, ".git", "HEAD"))
	_, err := TrackedFiles(elsewhere)
	extracttest.WantError(t, err, ".git is no git directory: it holds no HEAD git takes: a file, or a symlink into refs/")
	noSlash := extracttest.Repo(t, files)
	extracttest.Symlink(t, "refsX/heads/main", filepath.Join(noSlash, ".git", "HEAD"))
	_, err = TrackedFiles(noSlash)
	extracttest.WantError(t, err, ".git is no git directory: it holds no HEAD git takes: a file, or a symlink into refs/")
}
