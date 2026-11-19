package treecopy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitEnv keeps the user's and the system's git config out of a test.
var gitEnv = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
	"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}

// run runs git with args in dir and returns its stdout.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// write puts body at name below root, making the directories above it.
func write(t *testing.T, root, name, body string, perm os.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
}

// committed is a repository on main whose one commit holds files, with
// the user's cache, where copies go, in a directory of the test's outside
// it.
func committed(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	for name, body := range files {
		write(t, root, name, body, 0o644)
	}
	run(t, root, "init", "-q", "-b", "main")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "files")
	return root
}

// home is the directory copies of root go to.
func home(t *testing.T, root string) string {
	t.Helper()
	h, err := Home(root)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// opened is the tree at root, ready to copy into its home.
func opened(t *testing.T, root string) *Tree {
	t.Helper()
	tree, err := Open(t.Context(), root, home(t, root), Budget)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return tree
}

// made is a copy of the tree at root in slot 0, removed when the test ends.
func made(t *testing.T, root string) *Copy {
	t.Helper()
	c, err := opened(t, root).Make(t.Context(), 0)
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	t.Cleanup(func() { _ = c.Remove() })
	return c
}

func read(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// left is the copies in root's home: its directories.
func left(t *testing.T, root string) []string {
	t.Helper()
	entries, _ := os.ReadDir(home(t, root))
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
}

// A copy of a clean tree holds its files in a repository of its own on
// the same branch, and Remove takes it away, leaving the slot's directory
// for the next copy, at the same path. Planted breaks: the lock not given
// back, where the next copy took a spare directory; the repository's HEAD
// not copied, where the copy had no branch.
func TestCopyOfACleanTreeAndItsRemoval(t *testing.T) {
	root := committed(t, map[string]string{"a.txt": "a\n", "dir/b.txt": "b\n"})
	tree := opened(t, root)
	c, err := tree.Make(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Root() == root || read(t, c.Root(), "a.txt") != "a\n" || read(t, c.Root(), "dir/b.txt") != "b\n" {
		t.Fatalf("copy at %s does not hold the tree", c.Root())
	}
	if got := run(t, c.Root(), "status", "--porcelain"); got != "" {
		t.Fatalf("git status in the copy: %q, want clean", got)
	}
	if got := run(t, c.Root(), "branch", "--show-current"); got != "main\n" {
		t.Fatalf("the copy's branch: %q, want main", got)
	}
	if fi, err := os.Lstat(filepath.Join(c.Root(), ".git")); err != nil || !fi.IsDir() {
		t.Fatalf("the copy's .git: %v, %v; want a git directory of its own", fi, err)
	}
	if err := c.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Lstat(c.Root()); !os.IsNotExist(err) {
		t.Fatalf("the copy is still there: %v", err)
	}
	again, err := tree.Make(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Remove() }()
	if again.Root() != c.Root() {
		t.Fatalf("the next copy in slot 0 is at %s, want %s", again.Root(), c.Root())
	}
	if got := strings.Count(run(t, root, "worktree", "list", "--porcelain"), "worktree "); got != 1 {
		t.Fatalf("git worktree list names %d worktrees, want the repository's own", got)
	}
}

// dirty is a committed repository with a stash and every kind of change
// the disk can hold over HEAD: a file changed and not staged, one changed
// and staged, one added and staged, one deleted, one untracked in a new
// directory, an executable, a symlink, one git assumes unchanged and one
// it skips, both changed, and two ignored entries, a directory and a file.
func dirty(t *testing.T) string {
	t.Helper()
	root := committed(t, map[string]string{
		"changed.txt": "old\n", "staged.txt": "old\n", "gone.txt": "here\n",
		"assumed.txt": "committed\n", "skipped.txt": "committed\n",
		".gitignore": "node_modules/\n.env\n",
	})
	write(t, root, "changed.txt", "stashed\n", 0o644)
	run(t, root, "stash", "-q")
	write(t, root, "changed.txt", "new on disk\n", 0o644)
	write(t, root, "staged.txt", "new in the index\n", 0o644)
	write(t, root, "added.txt", "added\n", 0o644)
	run(t, root, "add", "staged.txt", "added.txt")
	run(t, root, "update-index", "--assume-unchanged", "assumed.txt")
	run(t, root, "update-index", "--skip-worktree", "skipped.txt")
	write(t, root, "assumed.txt", "local\n", 0o644)
	write(t, root, "skipped.txt", "local\n", 0o644)
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "new/untracked.txt", "untracked\n", 0o644)
	write(t, root, "run.sh", "#!/bin/sh\n", 0o755)
	if err := os.Symlink("changed.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "node_modules/dep/index.js", "dep\n", 0o644)
	write(t, root, ".env", "SECRET=1\n", 0o600)
	return root
}

// A copy of a dirty tree holds what the disk holds, not HEAD: each change,
// addition and deletion, staged or not, the files git assumes unchanged or
// skips, each untracked file with its mode, the symlink as a symlink, and
// the ignored entries, and git in the copy, its plumbing too, tells what it
// tells in the repository, stash and branch included. Planted breaks: no
// overlay, which held HEAD's changed.txt; the index left as HEAD's, which
// git status in the copy read as an unstaged change; a copy that dropped
// the mode; the flagged files left as HEAD's; the index not refreshed,
// where git diff-files named every tracked file; the stash's log not
// copied.
func TestCopyHoldsWhatTheDiskHolds(t *testing.T) {
	root := dirty(t)
	c := made(t, root)
	for name, want := range map[string]string{
		"changed.txt": "new on disk\n", "staged.txt": "new in the index\n", "added.txt": "added\n",
		"assumed.txt": "local\n", "skipped.txt": "local\n",
		"new/untracked.txt": "untracked\n", "link": "new on disk\n",
		"node_modules/dep/index.js": "dep\n", ".env": "SECRET=1\n",
	} {
		if got := read(t, c.Root(), name); got != want {
			t.Errorf("%s in the copy: %q, want %q", name, got, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(c.Root(), "gone.txt")); !os.IsNotExist(err) {
		t.Errorf("gone.txt, deleted on disk, is in the copy: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(c.Root(), "run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("run.sh in the copy: %v, %v; want mode 0755", fi, err)
	}
	if target, err := os.Readlink(filepath.Join(c.Root(), "link")); err != nil || target != "changed.txt" {
		t.Errorf("link in the copy: %q, %v; want a symlink to changed.txt", target, err)
	}
	// diff-files first: git status refreshes the index it reads, and would
	// hide an index the copy left stale.
	for _, args := range [][]string{
		{"diff-files", "--name-only"},
		{"status", "--porcelain=v1", "--untracked-files=all"},
		{"ls-files", "--stage"},
		{"ls-files", "-v"},
		{"ls-files", "--others", "--ignored", "--exclude-standard", "--directory"},
		{"stash", "list"},
		{"branch", "--show-current"},
	} {
		if got, want := run(t, c.Root(), args...), run(t, root, args...); got != want {
			t.Errorf("git %v in the copy:\n%s\nin the repository:\n%s", args, got, want)
		}
	}
}

// An ignored directory and an ignored file are copied whole, so a check
// that needs installed dependencies finds them, git still ignores them,
// and a write into one, new or existing, stays in the copy. Planted break:
// the ignored entries linked, through which the write reached the
// repository.
func TestIgnoredEntriesAreCopied(t *testing.T) {
	root := dirty(t)
	c := made(t, root)
	for _, name := range []string{"node_modules/dep/index.js", ".env"} {
		if fi, err := os.Lstat(filepath.Join(c.Root(), name)); err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("%s in the copy: %v, %v; want a file", name, fi, err)
		}
	}
	write(t, c.Root(), "node_modules/new.js", "x", 0o644)
	write(t, c.Root(), "node_modules/dep/index.js", "written by a check\n", 0o644)
	write(t, c.Root(), ".env", "written by a check\n", 0o600)
	if _, err := os.Lstat(filepath.Join(root, "node_modules", "new.js")); !os.IsNotExist(err) {
		t.Errorf("a file written anew in the copy's node_modules reached the repository: %v", err)
	}
	if read(t, root, "node_modules/dep/index.js") != "dep\n" || read(t, root, ".env") != "SECRET=1\n" {
		t.Error("a write into an ignored file of the copy reached the repository")
	}
}

// What a check writes in its copy reaches neither the repository nor
// another copy: a changed file, a new one, a removed one. The second copy,
// made while the first holds slot 0, is in a directory of its own.
// Planted break: a copy that linked the tracked files rather than copy
// them, through which the write reached the repository.
func TestWritesStayInTheCopy(t *testing.T) {
	root := dirty(t)
	before := run(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	one, two := made(t, root), made(t, root)
	if !strings.HasPrefix(filepath.Base(two.dir), "spare-") {
		t.Errorf("the second copy in slot 0 is at %s, want a spare directory", two.dir)
	}
	write(t, one.Root(), "changed.txt", "written by a check\n", 0o644)
	write(t, one.Root(), "new/untracked.txt", "written by a check\n", 0o644)
	write(t, one.Root(), "made.txt", "made\n", 0o644)
	if err := os.Remove(filepath.Join(one.Root(), "added.txt")); err != nil {
		t.Fatal(err)
	}
	for _, at := range []string{root, two.Root()} {
		if read(t, at, "changed.txt") != "new on disk\n" || read(t, at, "new/untracked.txt") != "untracked\n" || read(t, at, "added.txt") != "added\n" {
			t.Errorf("a write in one copy reached %s", at)
		}
		if _, err := os.Lstat(filepath.Join(at, "made.txt")); !os.IsNotExist(err) {
			t.Errorf("made.txt, made in one copy, is in %s", at)
		}
	}
	if after := run(t, root, "status", "--porcelain=v1", "--untracked-files=all"); after != before {
		t.Errorf("git status of the repository changed:\n%s\nwas:\n%s", after, before)
	}
}

// A copy's refs, stash and config are its own: a branch made, a stash
// pushed and a key set in one copy reach neither the repository nor the
// other copy. Planted break: the refs shared with the repository through
// a symlink, where the branch reached it.
func TestRefsAreTheCopysOwn(t *testing.T) {
	root := dirty(t)
	one, two := made(t, root), made(t, root)
	run(t, one.Root(), "switch", "-q", "-c", "scratch")
	run(t, one.Root(), "stash", "-q")
	run(t, one.Root(), "config", "errata.test", "one")
	for _, at := range []string{root, two.Root()} {
		if got := run(t, at, "for-each-ref", "--format=%(refname)"); got != "refs/heads/main\nrefs/stash\n" {
			t.Errorf("refs in %s:\n%s", at, got)
		}
		if got := run(t, at, "stash", "list"); strings.Count(got, "\n") != 1 {
			t.Errorf("stash list in %s:\n%s", at, got)
		}
		if got := read(t, at, ".git/config"); strings.Contains(got, "errata") {
			t.Errorf("config in %s:\n%s", at, got)
		}
	}
}

// A root below the repository's top is the same place below the copy's,
// and the whole repository is copied around it.
func TestRootBelowTheTop(t *testing.T) {
	top := committed(t, map[string]string{"sub/f": "f\n", "g": "g\n"})
	c := made(t, filepath.Join(top, "sub"))
	if filepath.Base(c.Root()) != "sub" || read(t, c.Root(), "f") != "f\n" || read(t, c.Root(), "../g") != "g\n" {
		t.Fatalf("copy root %s does not stand where sub does", c.Root())
	}
}

// A copy holds the disk as it was at the last Refresh: a file made after
// Open is in a copy only once the tree is read again. Planted break:
// Refresh that kept the old snapshot.
func TestRefreshReadsTheDiskAgain(t *testing.T) {
	root := committed(t, map[string]string{"f": "f\n"})
	tree := opened(t, root)
	write(t, root, "later.txt", "later\n", 0o644)
	c, err := tree.Make(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(c.Root(), "later.txt")); !os.IsNotExist(err) {
		t.Errorf("a file made after the last read is in the copy: %v", err)
	}
	_ = c.Remove()
	if err := tree.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err = tree.Make(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Remove() }()
	if got := read(t, c.Root(), "later.txt"); got != "later\n" {
		t.Errorf("later.txt after Refresh: %q", got)
	}
}

// A slot's directory that a run left behind, killed before it removed its
// copy, is emptied by the next copy in that slot. Planted break: claim
// that did not empty it, where the old file was still there.
func TestASlotLeftBehindIsEmptied(t *testing.T) {
	root := committed(t, map[string]string{"f": "f\n"})
	write(t, home(t, root), "copy-0/left.txt", "left\n", 0o644)
	c := made(t, root)
	if _, err := os.Lstat(filepath.Join(c.dir, "left.txt")); !os.IsNotExist(err) {
		t.Errorf("the slot's old file is in the copy: %v", err)
	}
}

// A copy made while another holds its slot goes to the first free spare,
// locked as a slot is, and a spare that a killed run left behind is
// removed when the tree is next opened, while one a run holds stays.
// Planted breaks: spares made anew each time, where the second copy was
// not spare-0; the sweep left out, where the killed run's spare stayed.
func TestSparesAreSlotsAndLeftoversAreSwept(t *testing.T) {
	root := committed(t, map[string]string{"f": "f\n"})
	h := home(t, root)
	// The first copy holds slot 0 until the test ends.
	_, second := made(t, root), made(t, root)
	if got := filepath.Base(second.dir); got != "spare-0" {
		t.Fatalf("the copy made while slot 0 was held is at %s, want spare-0", got)
	}
	write(t, h, "spare-7/left.txt", "left by a killed run\n", 0o644)
	opened(t, root)
	if _, err := os.Lstat(filepath.Join(h, "spare-7")); !os.IsNotExist(err) {
		t.Errorf("the spare a killed run left is still there: %v", err)
	}
	if _, err := os.Lstat(second.Root()); err != nil {
		t.Errorf("the spare a run holds was swept: %v", err)
	}
}

// Open refuses each tree a copy would not be true to, with the reason.
// Planted break: the steering check dropped, which copied a tree under
// GIT_INDEX_FILE.
func TestOpenRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func(t *testing.T) string
	}{
		{"not a repository", "git rev-parse", func(t *testing.T) string {
			t.Helper()
			// git looks no higher, whatever repository holds the
			// temporary directory.
			root := t.TempDir()
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
			return root
		}},
		{"no commit", "git rev-parse", func(t *testing.T) string {
			t.Helper()
			root := t.TempDir()
			run(t, root, "init", "-q")
			return root
		}},
		{"submodules", "submodules", func(t *testing.T) string {
			t.Helper()
			return committed(t, map[string]string{".gitmodules": "", "f": ""})
		}},
		{"sparse", "sparse checkout", func(t *testing.T) string {
			t.Helper()
			root := committed(t, map[string]string{"a/f": "", "b/f": ""})
			run(t, root, "sparse-checkout", "set", "a")
			return root
		}},
		{"steered", "GIT_INDEX_FILE is set", func(t *testing.T) string {
			t.Helper()
			root := committed(t, map[string]string{"f": ""})
			t.Setenv("GIT_INDEX_FILE", filepath.Join(root, ".git", "index"))
			return root
		}},
		{"copies inside", "inside the repository", func(t *testing.T) string {
			t.Helper()
			root := committed(t, map[string]string{"f": ""})
			t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
			return root
		}},
		{"no home", "cannot be made", func(t *testing.T) string {
			t.Helper()
			root := committed(t, map[string]string{"f": ""})
			blocker := filepath.Join(t.TempDir(), "file")
			write(t, blocker, "", "", 0o644)
			t.Setenv("XDG_CACHE_HOME", blocker)
			return root
		}},
		{"config", "extensions.worktreeconfig", func(t *testing.T) string {
			t.Helper()
			root := committed(t, map[string]string{"f": ""})
			run(t, root, "config", "extensions.worktreeConfig", "true")
			return root
		}},
		{"filter", "filter", func(t *testing.T) string {
			t.Helper()
			return committed(t, map[string]string{".gitattributes": "*.bin filter=lfs\n*.txt -filter\n", "a.bin": "x", "b.txt": "y"})
		}},
		{"ignored over the budget", "more than 32 MiB", func(t *testing.T) string {
			t.Helper()
			root := committed(t, map[string]string{".gitignore": "big/\n"})
			write(t, root, "big/a", "", 0o644)
			if err := os.Truncate(filepath.Join(root, "big", "a"), Budget+1); err != nil {
				t.Fatal(err)
			}
			return root
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.setup(t)
			h, err := Home(root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Open(t.Context(), root, h, Budget)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Open: %v, want an error that says %q", err, tc.want)
			}
		})
	}
}

// A path whose filter is unset runs none, so the tree copies. Planted
// break: the unset filters left in the refusal, which refused it.
func TestAnUnsetFilterCopies(t *testing.T) {
	root := committed(t, map[string]string{".gitattributes": "*.txt -filter\n", "b.txt": "y"})
	if got := read(t, made(t, root).Root(), "b.txt"); got != "y" {
		t.Errorf("b.txt in the copy: %q", got)
	}
}

// A copy that cannot be made leaves nothing behind, and gives its slot
// back. Planted break: Make that kept the failed copy.
func TestMakeFailsWhole(t *testing.T) {
	root := committed(t, map[string]string{"f": "f\n"})
	tree := opened(t, root)
	common := tree.common
	tree.common = filepath.Join(root, "nowhere")
	if _, err := tree.Make(t.Context(), 0); err == nil {
		t.Fatal("Make succeeded with no repository to copy")
	}
	if got := left(t, root); len(got) != 0 {
		t.Fatalf("a failed copy left %v", got)
	}
	tree.common = common
	tree.gitDir = filepath.Join(root, "nowhere")
	if _, err := tree.Make(t.Context(), 0); err == nil {
		t.Fatal("Make succeeded with no HEAD to copy")
	}
	if got := left(t, root); len(got) != 0 {
		t.Fatalf("a failed copy left %v", got)
	}
	tree.gitDir = filepath.Join(root, ".git")
	c, err := tree.Make(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Remove() }()
	if filepath.Base(c.dir) != "copy-0" {
		t.Fatalf("the copy after a failed one is at %s, want slot 0's", c.dir)
	}
}

// A tree whose disk cannot be read again keeps the error, and Make returns
// it. Planted break: Make that ignored it and copied the old snapshot.
func TestAFailedRefreshStopsMake(t *testing.T) {
	root := committed(t, map[string]string{"f": "f\n"})
	tree := opened(t, root)
	tree.top = filepath.Join(root, "nowhere")
	if err := tree.Refresh(t.Context()); err == nil {
		t.Fatal("Refresh read a tree that is not there")
	}
	if _, err := tree.Make(t.Context(), 0); err == nil {
		t.Fatal("Make copied after a failed Refresh")
	}
}

// Home is a directory of the user's cache for each repository, the same
// for the same root, another for another. Planted break: a hash of
// nothing, which gave every repository one directory.
func TestHome(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	a, b := t.TempDir(), t.TempDir()
	ha, hb := home(t, a), home(t, b)
	if ha != home(t, a) || ha == hb || filepath.Dir(ha) != filepath.Join(cache, "errata") {
		t.Errorf("Home(%s) = %s, Home(%s) = %s", a, ha, b, hb)
	}
}

// parseStatus reads each entry's path after its two status letters, and
// calls the list tracked when any entry is not untracked.
func TestParseStatus(t *testing.T) {
	for _, tc := range []struct {
		out     string
		paths   []string
		tracked bool
	}{
		{"", nil, false},
		{"?? a\x00?? dir/b c\x00", []string{"a", "dir/b c"}, false},
		{"?? a\x00 M b\x00", []string{"a", "b"}, true},
		{"A  a\x00", []string{"a"}, true},
	} {
		paths, tracked := parseStatus([]byte(tc.out))
		if !slices.Equal(paths, tc.paths) || tracked != tc.tracked {
			t.Errorf("parseStatus(%q) = %q, %v; want %q, %v", tc.out, paths, tracked, tc.paths, tc.tracked)
		}
	}
}

// parseFlagged keeps the paths tagged with a lower-case letter or S.
func TestParseFlagged(t *testing.T) {
	got := parseFlagged([]byte("H kept\x00h assumed\x00S skipped\x00s both\x00M merge\x00"))
	if want := []string{"assumed", "skipped", "both"}; !slices.Equal(got, want) {
		t.Errorf("parseFlagged = %q, want %q", got, want)
	}
}

// within counts regular files under each entry against the budget, a
// missing entry as nothing.
func TestWithin(t *testing.T) {
	top := t.TempDir()
	write(t, top, "d/a", "1234", 0o644)
	write(t, top, "b", "12", 0o644)
	for _, tc := range []struct {
		budget int64
		ok     bool
	}{{6, true}, {5, false}} {
		if err := within(top, []string{"d/", "b", "missing"}, tc.budget); (err == nil) != tc.ok {
			t.Errorf("within with a budget of %d: %v", tc.budget, err)
		}
	}
}

// inside holds a directory to the top once both resolve their links.
func TestInside(t *testing.T) {
	top := t.TempDir()
	for _, tc := range []struct {
		dir  string
		want bool
	}{{top, true}, {filepath.Join(top, "a", "b"), true}, {filepath.Dir(top), false}, {top + "x", false}} {
		if got := inside(top, tc.dir); got != tc.want {
			t.Errorf("inside(%s, %s) = %v, want %v", top, tc.dir, got, tc.want)
		}
	}
}

// git's error ends with the last line of its stderr, the one that says why,
// and gitSome takes an exit of 1 as nothing found.
func TestGitErrorSaysWhy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	_, err := git(context.Background(), dir, "rev-parse", "HEAD")
	if err == nil || !strings.HasPrefix(err.Error(), "git rev-parse: ") || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("git outside a repository: %v", err)
	}
	root := committed(t, map[string]string{"f": ""})
	if out, err := gitSome(t.Context(), root, "config", "--get-regexp", "^nothing$"); out != nil || err != nil {
		t.Fatalf("gitSome on no match: %q, %v", out, err)
	}
	if _, err := gitSome(t.Context(), dir, "rev-parse", "HEAD"); err == nil {
		t.Fatal("gitSome outside a repository: no error")
	}
}
