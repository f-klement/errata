package core

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	slashpath "path"
	"path/filepath"
	"slices"

	"github.com/f-klement/errata/internal/check"
)

// WalkLimits bound a walk of the repository: the files and directories it
// looks at, the directories it enters, how many directories deep it goes,
// and the bytes of the names of the directories it holds to walk next.
type WalkLimits struct {
	Entries int
	Dirs    int
	Depth   int
	Names   int
}

// DefaultWalk are the limits of every walk errata makes. The largest tree
// measured, a Go module cache of the sources of every module several
// projects use, holds 136007 files and directories, a seventh of Entries,
// and 27149 directories, under a seventh of Dirs; the deepest, a GitOps
// repository, nests 17 directories, a quarter of Depth. A directory costs
// the walk an open and a listing, 35 to 50 microseconds on the build node,
// where a file costs 3, so Dirs is the bound on time: a million directories
// took 82 s, and 199000 with a small pin in each take 26 to 30 s. Names
// holds a million directory names of 16 bytes waiting at once, in one
// directory or along the path walked. The README's Limits section gives the
// time and memory a walk at each limit takes.
var DefaultWalk = WalkLimits{Entries: 1_000_000, Dirs: 200_000, Depth: 64, Names: 16 << 20}

// SkippedDirs are the directories a walk does not enter, by name, wherever
// they are, each with the reason. Each holds files the repository installs,
// downloads, builds or keeps for its tools, not files it writes: a pin
// there is another project's. A key with a slash names a directory by its
// parent's name and its own, so .errata/witness is a directory witness in
// any directory .errata.
var SkippedDirs = map[string]string{
	".git":             "git's own store: objects, refs and hooks, no file of the working tree",
	"node_modules":     "the packages npm, Yarn and pnpm install, each with pins of its own",
	"bower_components": "the packages Bower installs",
	".yarn":            "Yarn's cache and its own releases",
	".pnpm-store":      "pnpm's store of packages",
	"vendor":           "dependencies Go, Composer and Bundler copy into the repository, each with its own go.mod or version files",
	".venv":            "a Python virtual environment, with the packages installed in it",
	"venv":             "a Python virtual environment, by the other name python -m venv is given",
	".tox":             "the virtual environments tox builds",
	".nox":             "the virtual environments nox builds",
	".direnv":          "the environments direnv builds",
	"__pycache__":      "Python's compiled files",
	".terraform":       "the providers and modules terraform init downloads",
	".gradle":          "Gradle's caches",
	".cache":           "the caches tools keep in the repository",
	"testdata":         "test fixtures, which the go command passes over too: a fixture is no claim of the repository",
	check.WitnessDir:   "the fixtures of witnesses, each a break planted for a check to report false on, which globs pass over too",
}

// skipped reports whether a walk passes over the directory name in the
// directory rel: by its name, or by its parent's name and its own.
func skipped(rel, name string) bool {
	if _, skip := SkippedDirs[name]; skip {
		return true
	}
	_, skip := SkippedDirs[slashpath.Base(rel)+"/"+name]
	return skip
}

// walkBatch is how many entries of a directory a walk reads at a time, so a
// directory of a million entries is never held whole.
const walkBatch = 1024

// Walk calls visit once for each directory below root, the root too, that
// holds files want accepts, with the directory's path relative to root and
// written with slashes ("." for the root) and the names of those files,
// sorted. want sees each file's directory and name. A directory's files come
// before its subdirectories, and the subdirectories in the order of their
// names. The walk enters none of the directories SkippedDirs names, nor a
// directory below the root that holds an entry named .git, a submodule, a
// worktree or a repository of its own. A symlink is a file to Walk: a
// symlink to a directory is not entered, and one whose name want accepts is
// visited, for the Source that reads it to follow or refuse.
//
// Walk opens each directory by its name from the open directory above it,
// never by its whole path, and holds the names of the directories it has
// still to walk, not their paths, so a long path costs its length once. A
// walk past a limit, a path longer than a file may be, a directory Walk
// cannot open or read, or an error visit returns ends the walk with that
// error: a walk is refused, never cut short.
func Walk(root string, l WalkLimits, want func(dir, name string) bool, visit func(dir string, names []string) error) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("open the repository root: %w", bare(err))
	}
	r, err := os.OpenRoot(realRoot)
	if err != nil {
		return fmt.Errorf("open the repository root: %w", bare(err))
	}
	defer r.Close()
	w := walker{limits: l, want: want, visit: visit, list: listDir}
	return w.dir(r, ".", 0)
}

// lister is what a walk reads a directory through: an *os.File, or in a
// test a reader that fails.
type lister interface {
	ReadDir(n int) ([]fs.DirEntry, error)
	Close() error
}

// listDir opens the directory r is for reading.
func listDir(r *os.Root) (lister, error) { return r.Open(".") }

// walker is one walk's state.
type walker struct {
	limits  WalkLimits
	want    func(dir, name string) bool
	visit   func(dir string, names []string) error
	list    func(*os.Root) (lister, error)
	entries int
	dirs    int // the directories entered
	names   int // the bytes of the names of the directories waiting
}

// dir walks the directory r is open on, at rel, depth directories below the
// root.
func (w *walker) dir(r *os.Root, rel string, depth int) error {
	files, dirs, err := w.read(r, rel)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		if err := w.visit(rel, files); err != nil {
			return err
		}
	}
	for i, name := range dirs {
		w.names -= len(name)
		dirs[i] = ""
		p := slashpath.Join(rel, name)
		if depth+1 > w.limits.Depth {
			return fmt.Errorf("walk: %s is nested more than %d directories deep, the limit; name the files to read in files", Quote(p), w.limits.Depth)
		}
		if w.dirs++; w.dirs > w.limits.Dirs {
			return fmt.Errorf("walk: the repository holds more than %d directories outside the directories errata passes over, the limit, and stopped at %s; name the files to read in files", w.limits.Dirs, Quote(p))
		}
		child, err := r.OpenRoot(name)
		if err != nil {
			return fmt.Errorf("walk: read %s: %w", Quote(p), bare(err))
		}
		err = w.dir(child, p, depth+1)
		_ = child.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// read lists the directory r is open on, at rel: the names of the files want
// accepts and of the subdirectories to walk, each sorted. A directory below
// the root that holds .git gives neither. The listing is closed before any
// subdirectory is opened.
func (w *walker) read(r *os.Root, rel string) (files, dirs []string, err error) {
	f, err := w.list(r)
	if err != nil {
		return nil, nil, fmt.Errorf("walk: read %s: %w", Quote(rel), bare(err))
	}
	defer f.Close()
	held := 0
	for {
		batch, err := f.ReadDir(walkBatch)
		for _, e := range batch {
			if w.entries++; w.entries > w.limits.Entries {
				return nil, nil, fmt.Errorf("walk: the repository holds more than %d files and directories outside the directories errata passes over, the limit, and stopped in %s; name the files to read in files", w.limits.Entries, Quote(rel))
			}
			name := e.Name()
			if n := len(rel) + 1 + len(name); n > maxFileName {
				return nil, nil, fmt.Errorf("walk: %s is %d bytes long, over the limit of %d", Quote(slashpath.Join(rel, name)), n, maxFileName)
			}
			if name == ".git" && rel != "." {
				w.names -= held
				return nil, nil, nil
			}
			// ReadDir gives an entry's type as Lstat does, so a symlink
			// is never a directory here, whatever it points at.
			if e.IsDir() {
				if !skipped(rel, name) {
					if w.names += len(name); w.names > w.limits.Names {
						return nil, nil, fmt.Errorf("walk: the directories waiting to be walked have names of more than %d bytes in all, the limit, in %s; name the files to read in files", w.limits.Names, Quote(rel))
					}
					held += len(name)
					dirs = append(dirs, name)
				}
			} else if w.want(rel, name) {
				files = append(files, name)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("walk: read %s: %w", Quote(rel), bare(err))
		}
	}
	slices.Sort(files)
	slices.Sort(dirs)
	return files, dirs, nil
}
