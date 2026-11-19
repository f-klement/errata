// Package treecopy makes throwaway copies of a repository's working tree,
// for a program check to run in so that what it writes reaches neither the
// repository nor another check (ADR 0019).
//
// A copy is a repository of its own that borrows the objects of the one it
// copies (objects/info/alternates), with that repository's config, refs,
// stash and HEAD copied in, and HEAD's tree checked out. On that it lays
// what the disk holds and HEAD does not: every file the working tree
// changed, added or deleted, staged or not, each file git is told to
// assume unchanged or to skip, every untracked file git does not ignore,
// and every entry git ignores, each copied; and git's index, when one of
// those is tracked, so git in the copy lists what git in the repository
// lists. A copy costs about 0.1 s and the size of the tracked tree and the
// ignored files, whatever the size of the objects.
//
// A tree whose ignored files weigh more than the budget is not copied:
// those are dependencies and build outputs (node_modules, target, a
// virtualenv), which a copy would either pay for in full or share with the
// repository and with every other copy.
package treecopy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/f-klement/errata/internal/program"
)

// Budget is the most bytes the files git ignores may hold for the tree to
// be copied. A fresh clone ignores nothing, a source tree a few MiB of
// caches and single outputs; dependencies and build outputs weigh tens to
// thousands of MiB, and copying 32 MiB costs about what the checkout does.
const Budget = 32 << 20

// gitTimeout bounds each git command a copy runs.
const gitTimeout = time.Minute

// maxOutput is the most bytes of a git command's output a copy holds; the
// byte past it stops the command, and the copy is not made.
const maxOutput = 64 << 20

// steering are the variables that point git at a repository other than the
// one at its working directory, as a hook that runs errata sets them. A
// check run in a copy would inherit them and read the repository's index,
// so a tree under one of them is not copied.
var steering = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY"}

// unsupported are the config keys under which a repository's config, refs
// or index do not carry over as files: a work tree set elsewhere, config
// per worktree, refs in a reftable, and an index split in two.
const unsupported = `^(core\.worktree|core\.splitindex|extensions\.worktreeconfig|extensions\.refstorage)$`

// Tree is a working tree that copies are made of: the repository's top,
// the git directory of its work tree and the one it shares with its other
// work trees, the root a check runs at as a path below the top, the
// directory copies are made in, the budget of ignored bytes, and what the
// disk held over HEAD when the tree was last read (Refresh).
type Tree struct {
	top, gitDir, common, prefix string
	home                        string
	budget                      int64
	snap                        snapshot
	snapErr                     error
}

// snapshot is what a copy lays over HEAD: the paths git status names, the
// paths git assumes unchanged or skips, the entries git ignores, and
// whether the copy takes the repository's index, as it must when a
// tracked path is among them.
type snapshot struct {
	changed, flagged, ignored []string
	index                     bool
}

// Open is the tree that holds root, ready to be copied into home with at
// most budget bytes of ignored files. It is an error, and the checks run
// where they are, when git is not installed, root is in no repository or
// one with no commit, git is steered to another repository (steering), the
// repository has submodules, which a copy would hold empty, its working
// tree is a sparse checkout, or was one, which a copy would hold whole, its
// config sets a key a copy cannot follow (unsupported), a tracked path has
// a filter, such as LFS, which would run for every copy, the ignored files
// weigh more than budget, or home is inside the repository, where each
// copy would hold the others, or cannot be made.
func Open(ctx context.Context, root, home string, budget int64) (*Tree, error) {
	for _, name := range steering {
		if _, set := os.LookupEnv(name); set {
			return nil, fmt.Errorf("%s is set, which a copy would inherit", name)
		}
	}
	out, err := git(ctx, root, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir", "--show-prefix", "HEAD")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != 5 {
		return nil, fmt.Errorf("git rev-parse printed %q, not a top, two git directories, a prefix and a commit", out)
	}
	t := &Tree{top: lines[0], gitDir: lines[1], common: lines[2], prefix: filepath.FromSlash(lines[3]), home: home, budget: budget}
	if err := t.refuse(ctx); err != nil {
		return nil, err
	}
	sweep(home)
	return t, t.Refresh(ctx)
}

// refuse is why t cannot be copied as it is, or nil.
func (t *Tree) refuse(ctx context.Context) error {
	if _, err := os.Lstat(filepath.Join(t.top, ".gitmodules")); err == nil {
		return errors.New("the repository has submodules, which a copy would hold empty")
	}
	if _, err := os.Lstat(filepath.Join(t.gitDir, "info", "sparse-checkout")); err == nil {
		return errors.New("the working tree is a sparse checkout, which a copy would hold whole")
	}
	if inside(t.top, t.home) {
		return fmt.Errorf("the directory for copies, %s, is inside the repository", t.home)
	}
	if err := os.MkdirAll(t.home, 0o700); err != nil {
		return fmt.Errorf("the directory for copies cannot be made: %w", err)
	}
	keys, err := gitSome(ctx, t.top, "config", "--name-only", "--get-regexp", unsupported)
	if err != nil {
		return err
	}
	if len(keys) > 0 {
		return fmt.Errorf("the repository's config sets %s, which a copy cannot follow", strings.Join(strings.Fields(string(keys)), ", "))
	}
	// Every tracked path but those whose filter is unspecified or unset:
	// :(attr:filter) alone matches only a filter set to true, not lfs.
	filtered, err := git(ctx, t.top, "ls-files", "-z", "--", ".", ":(exclude,attr:!filter)", ":(exclude,attr:-filter)")
	if err != nil {
		return err
	}
	if len(filtered) > 0 {
		return errors.New("a tracked path has a filter, such as LFS, which would run for every copy")
	}
	return nil
}

// inside reports whether dir is top or below it, once both resolve their
// symlinks. A path that does not resolve is taken as written.
func inside(top, dir string) bool {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	rel, err := filepath.Rel(top, dir)
	return err == nil && filepath.IsLocal(rel)
}

// Home is the directory copies of the repository at root are made in, and
// what else errata keeps of it: a directory of the user's cache named by a
// hash of root's absolute path, so each repository has its own, the same
// from run to run.
func Home(root string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(cache, "errata", hex.EncodeToString(sum[:8])), nil
}

// Refresh reads again what the disk holds over HEAD, for the copies made
// after it, as after a check that ran in the repository and may have
// written there. An error stays with the tree, and Make returns it.
func (t *Tree) Refresh(ctx context.Context) error {
	t.snap, t.snapErr = t.read(ctx)
	return t.snapErr
}

// read is what the disk holds over HEAD now.
func (t *Tree) read(ctx context.Context) (snapshot, error) {
	status, err := git(ctx, t.top, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=all")
	if err != nil {
		return snapshot{}, err
	}
	tags, err := git(ctx, t.top, "ls-files", "-v", "-z")
	if err != nil {
		return snapshot{}, err
	}
	ignored, err := git(ctx, t.top, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return snapshot{}, err
	}
	s := snapshot{ignored: names(ignored), flagged: parseFlagged(tags)}
	var tracked bool
	s.changed, tracked = parseStatus(status)
	s.index = tracked || len(s.flagged) > 0
	return s, within(t.top, s.ignored, t.budget)
}

// names is the paths of a -z listing.
func names(out []byte) []string {
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// parseStatus is the paths of git status --porcelain=v1 -z with
// --no-renames, and whether any of them is tracked, an entry other than
// "??".
func parseStatus(out []byte) (paths []string, tracked bool) {
	for _, entry := range strings.Split(string(out), "\x00") {
		if len(entry) < 4 {
			continue
		}
		paths = append(paths, entry[3:])
		tracked = tracked || entry[:2] != "??"
	}
	return paths, tracked
}

// parseFlagged is the paths of git ls-files -v -z that git assumes
// unchanged, tagged with a lower-case letter, or skips, tagged S. git
// status does not list them, whatever the disk holds there.
func parseFlagged(out []byte) []string {
	var paths []string
	for _, entry := range strings.Split(string(out), "\x00") {
		if len(entry) < 3 {
			continue
		}
		if tag := entry[0]; tag == 'S' || (tag >= 'a' && tag <= 'z') {
			paths = append(paths, entry[2:])
		}
	}
	return paths
}

// within is an error when the entries, below top, hold more than budget
// bytes. It stops reading once they do, so a large node_modules costs the
// walk to its budget, not its size.
func within(top string, entries []string, budget int64) error {
	var held int64
	over := errors.New("over")
	for _, e := range entries {
		err := filepath.WalkDir(filepath.Join(top, filepath.FromSlash(e)), func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() {
				held += fi.Size()
			}
			if held > budget {
				return over
			}
			return nil
		})
		if errors.Is(err, over) {
			return fmt.Errorf("the files git ignores hold more than %d MiB, the most a copy takes", budget>>20)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Copy is one copy of a tree. Remove deletes it.
type Copy struct {
	dir, root string
	// release gives the copy's directory back for the next copy to be
	// made in, nil for a directory no other copy will use.
	release func() error
}

// Root is the root a check runs at in the copy: the place of the tree's
// root below the copy's top.
func (c *Copy) Root() string { return c.root }

// Remove deletes the copy and gives its directory back.
func (c *Copy) Remove() error {
	err := os.RemoveAll(c.dir)
	if c.release != nil {
		err = errors.Join(err, c.release())
	}
	return err
}

// Make makes a copy of the tree as the disk held it at the last Refresh,
// in the directory of slot, a worker of the caller's that makes one copy
// at a time. The slot's directory is the same from run to run, so a cache
// keyed by a path, as Go's build cache is, finds the copy's paths again;
// when another run holds it, the copy is made in a directory of its own.
// On an error nothing of the copy is left.
func (t *Tree) Make(ctx context.Context, slot int) (_ *Copy, err error) {
	if t.snapErr != nil {
		return nil, t.snapErr
	}
	c, err := t.claim(slot)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, c.Remove())
		}
	}()
	if err = t.repository(ctx, c.dir); err != nil {
		return nil, err
	}
	if err = t.overlay(ctx, c.dir); err != nil {
		return nil, err
	}
	return c, nil
}

// claim is an empty directory for slot's copy: copy-<slot> in home while
// this run holds its lock, which a run that ended without removing it
// left behind; when another run holds it, the first spare-<n> whose lock
// is free, emptied the same way. Where there are no locks (locking), or
// when every spare is held, it is an unlocked-* directory of its own,
// made anew, under a prefix no sweep removes.
func (t *Tree) claim(slot int) (*Copy, error) {
	if err := os.MkdirAll(t.home, 0o700); err != nil {
		return nil, err
	}
	if locking {
		if c, ok, err := t.take(fmt.Sprintf("copy-%d", slot)); ok {
			return c, err
		}
		for n := range spares {
			if c, ok, err := t.take(fmt.Sprintf("spare-%d", n)); ok {
				return c, err
			}
		}
	}
	dir, err := os.MkdirTemp(t.home, "unlocked-")
	if err != nil {
		return nil, err
	}
	return &Copy{dir: dir, root: filepath.Join(dir, t.prefix)}, nil
}

// spares is how many spare directories claim tries before it makes one
// of its own: more than any number of runs on one repository at once.
const spares = 64

// take is the directory name in home, emptied, when its lock is free: ok
// is false when another run holds it.
func (t *Tree) take(name string) (_ *Copy, ok bool, _ error) {
	dir := filepath.Join(t.home, name)
	release, ok := lock(dir + ".lock")
	if !ok {
		return nil, false, nil
	}
	if err := errors.Join(os.RemoveAll(dir), os.Mkdir(dir, 0o700)); err != nil {
		return nil, true, errors.Join(err, release())
	}
	return &Copy{dir: dir, root: filepath.Join(dir, t.prefix), release: release}, true, nil
}

// sweep removes each spare directory in home that no run holds: one a
// run killed while another held its slot left behind. A slot's own
// directory is emptied when it is next claimed, so it stays.
func sweep(home string) {
	entries, err := os.ReadDir(home)
	if err != nil || !locking {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "spare-") {
			continue
		}
		dir := filepath.Join(home, e.Name())
		if release, ok := lock(dir + ".lock"); ok {
			_ = os.RemoveAll(dir)
			_ = release()
		}
	}
}

// repository makes dir a repository of its own with t's objects, config,
// refs, stash and HEAD, and HEAD's tree checked out.
func (t *Tree) repository(ctx context.Context, dir string) error {
	g := filepath.Join(dir, ".git")
	if err := os.MkdirAll(filepath.Join(g, "objects", "info"), 0o750); err != nil {
		return err
	}
	alternates := filepath.Join(t.common, "objects") + "\n"
	if err := os.WriteFile(filepath.Join(g, "objects", "info", "alternates"), []byte(alternates), 0o600); err != nil {
		return err
	}
	for _, f := range []struct {
		from, name string
		must       bool
	}{
		{t.common, "config", true}, {t.gitDir, "HEAD", true}, {t.common, "refs", true},
		{t.common, "packed-refs", false}, {t.common, "shallow", false},
		{t.common, "info/exclude", false}, {t.common, "info/attributes", false},
		{t.common, "logs/refs/stash", false},
	} {
		if err := place(f.from, g, f.name); err != nil {
			return err
		}
		if _, err := os.Lstat(filepath.Join(g, filepath.FromSlash(f.name))); err != nil && f.must {
			return fmt.Errorf("the repository has no %s to copy", f.name)
		}
	}
	_, err := git(ctx, dir, "read-tree", "--reset", "-u", "HEAD")
	return err
}

// overlay lays on the copy at dir what the disk held over HEAD: each path
// git status names, or git assumes unchanged or skips, and each ignored
// entry, copied or removed as the disk holds it, and git's index when the
// copy needs it, brought up to date with the copy's files.
func (t *Tree) overlay(ctx context.Context, dir string) error {
	for _, list := range [][]string{t.snap.changed, t.snap.flagged, t.snap.ignored} {
		for _, name := range list {
			if err := place(t.top, dir, name); err != nil {
				return err
			}
		}
	}
	if !t.snap.index {
		return nil
	}
	if err := copyFile(filepath.Join(t.gitDir, "index"), filepath.Join(dir, ".git", "index"), 0o600); err != nil {
		return err
	}
	// The index holds the repository's files' stat data, which no file of
	// the copy matches; refreshed, git's plumbing in the copy reads a
	// file as changed only when it is.
	_, err := gitSome(ctx, dir, "-c", "core.fsmonitor=false", "update-index", "-q", "--refresh")
	return err
}

// place puts at name below to what the disk holds at name below from: a
// symlink as the same link, a directory with every entry below it, a file
// as a copy with its mode, and nothing, removing what to holds there, when
// from holds nothing.
func place(from, to, name string) error {
	src, dst := filepath.Join(from, filepath.FromSlash(name)), filepath.Join(to, filepath.FromSlash(name))
	fi, err := os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return os.RemoveAll(dst)
	}
	if err != nil {
		return err
	}
	if err := vacate(dst); err != nil {
		return err
	}
	if fi.IsDir() {
		return copyTree(src, dst)
	}
	return copyEntry(src, dst, fi)
}

// vacate removes what dst holds and makes the directories above it.
func vacate(dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Dir(dst), 0o750)
}

// copyTree copies the directory src to dst, with every entry below it.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o750)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return copyEntry(path, to, fi)
	})
}

// copyEntry copies the file or symlink src, which fi describes, to dst.
// Anything else, such as a socket, is left out.
func copyEntry(src, dst string, fi fs.FileInfo) error {
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case fi.Mode().IsRegular():
		return copyFile(src, dst, fi.Mode().Perm())
	}
	return nil
}

// copyFile writes the bytes of src to a new file dst with mode perm.
func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

// git runs git with args in dir and returns its stdout, or an error that
// ends with the last line of its stderr. It never prompts, takes no lock
// it may skip, so it writes nothing to a repository it only reads, and runs
// no hook: hooks are for a checkout the user makes, and a copy is none.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	p := program.Run(ctx, program.Spec{
		Argv:    append([]string{"git", "--no-optional-locks", "-c", "core.hooksPath=" + os.DevNull}, args...),
		Dir:     dir,
		Env:     []string{"GIT_TERMINAL_PROMPT=0", "LC_ALL=C"},
		Timeout: gitTimeout,
		Bound:   maxOutput,
	})
	if p.Err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], p.Err, lastLine(p.Stderr))
	}
	return p.Stdout, nil
}

// gitSome is git for a command that exits 1 to say it found nothing, as
// git config --get-regexp does, or that something needs an update, as
// update-index --refresh does: that exit is no error.
func gitSome(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, err := git(ctx, dir, args...)
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, nil
	}
	return out, err
}

// lastLine is the last line of stderr that is not blank.
func lastLine(stderr []byte) string {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	return lines[len(lines)-1]
}
