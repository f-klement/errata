package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core/gitindex"
)

// The tree a read under a root goes through, and on a witness's run the
// one place that reads the fixture as a clone holds it (ADR 0015): find
// for a read, a stat or a resolution of a file, top for a walk or a
// listing, and runIndex for a glob, each consulting git's index below the
// fixture where a witness runs. A reader that goes through them needs no
// check of its own. A real run reads the working tree (ADR 0008).

// errOutside refuses a symlink that leads out of the repository. It names
// neither where the link points nor whether that exists: the message reaches
// a CI log that a pull request can read.
var errOutside = errors.New("a symlink leads outside the repository")

// maxLinks is how many symlinks one read follows, as on Linux, before it
// calls the chain a loop.
const maxLinks = 40

// tree is the directory a read under a root goes through: an *os.Root
// open on it, and its path with its symlinks resolved, which resolve reads
// a link's target against.
type tree struct {
	*os.Root
	real  string
	held  bool            // the fixture the engine opened, which close leaves open
	dirs  *chain          // the directories below the root open now
	index *gitindex.Index // on a witness, git's index below the fixture
	git   *gitIndexOnce   // on another run, git's index once a read misses (leftOut)
}

// chain is the directories a tree holds open below its root, each opened
// from the one before it: a/, a/b/, a/b/c/ for a look at a/b/c/x. A look
// at a name goes through the directory that holds it, so it costs one
// lookup, where a look through the root walks every directory on the way
// again; a file set's files, in git's order, share their directories.
// It holds at most maxChain, so a deep path cannot hold a descriptor for
// each of its directories; a look below that goes through the deepest one
// by the rest of its path.
type chain struct {
	names []string   // the directory names below the root, in order
	roots []*os.Root // roots[i] is open on names[:i+1]
}

// maxChain is the most directories a tree holds open below its root, the
// depth a walk may go (DefaultWalk).
const maxChain = 64

// at is the directory to look up rel in, a path relative to t's root, and
// the name to look up there: rel's last name, or the rest of rel below the
// deepest directory the chain holds, as when a directory on the way is
// too deep or cannot be opened.
func (t tree) at(rel string) (*os.Root, string) {
	names := splitPath(rel)
	if len(names) == 0 {
		return t.Root, rel // the root itself
	}
	dirs := names[:len(names)-1]
	c := t.dirs
	keep := 0
	for keep < len(c.names) && keep < len(dirs) && c.names[keep] == dirs[keep] {
		keep++
	}
	// Where the chain parts from rel's directories, what lies past is
	// closed; a chain that ends on the way holds nothing past, so nothing
	// closes. A chain that runs on below them is kept: resolve looks at
	// each prefix of a path in turn, and the next look goes deeper again.
	if keep < len(dirs) {
		for _, r := range c.roots[keep:] {
			_ = r.Close()
		}
		c.names, c.roots = c.names[:keep], c.roots[:keep]
	}
	for len(c.names) < min(len(dirs), maxChain) {
		parent := t.Root
		if n := len(c.roots); n > 0 {
			parent = c.roots[n-1]
		}
		name := dirs[len(c.names)]
		r, err := parent.OpenRoot(name)
		if err != nil {
			// The look below goes through the rest of the path, and
			// gives the error the file system gives it there, such as
			// ENOTDIR for a name below a file.
			break
		}
		c.names, c.roots = append(c.names, name), append(c.roots, r)
	}
	held := min(len(dirs), len(c.names)) // rel's directories the chain holds
	parent := t.Root
	if held > 0 {
		parent = c.roots[held-1]
	}
	return parent, filepath.Join(names[held:]...)
}

// lstat is Lstat of rel in t, through the directory that holds it.
func (t tree) lstat(rel string) (fs.FileInfo, error) {
	r, name := t.at(rel)
	return r.Lstat(name)
}

// readlink is Readlink of rel in t, through the directory that holds it.
func (t tree) readlink(rel string) (string, error) {
	r, name := t.at(rel)
	return r.Readlink(name)
}

// fixtureKey is the context key of the fixture a witness's run reads.
type fixtureKey struct{}

// heldFixture is the fixture a witness's run reads: its path, as the run
// is given it as its root, and that path relative to the repository's
// root, its directory, held open, and git's index below it
// (fixtureIndex), which the tree the run reads (openTree) consults in
// each of its reads, listings and globs.
type heldFixture struct {
	path, rel string
	dir       *os.Root
	index     gitindex.Index
}

// OnFixture is ctx, the context of a run of a witness (check.OnWitness),
// with the fixture's directory, dir, which the engine opened when it
// checked where the fixture is, and fixture, the path the run is given as
// its root. Every read under that root goes through dir (openTree), so a
// symlink put at .errata, at .errata/witness or at the fixture after that
// check cannot move the read, and a read under any other root is refused.
// The caller keeps dir open for the run and closes it after. It lives
// here, not in check, since internal/rule imports check and reads no file.
//
// It reads git's index of the repository once (fixtureIndex), and refuses a
// fixture the index lists no file below, whatever kind of check runs on
// it: a clone in CI holds none of its files, so the witness must be
// unknown here as it is there. A read on the run then finds only the
// files the index lists below the fixture (tree.find), a nested
// fixture's included, so a fixture that commits some of its files reads
// as its clone would; a walk or a listing lists only them (tree.top), and
// a glob matches the list read here (runIndex).
func OnFixture(ctx context.Context, fixture string, dir *os.Root) (context.Context, error) {
	if _, witness := check.RealRoot(ctx); !witness {
		return nil, errors.New("a fixture is held only on a witness's run")
	}
	real, _ := check.RealRoot(ctx)
	rel, err := filepath.Rel(real, fixture)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("the witness at %s is not inside the repository root", Quote(fixture))
	}
	index, err := fixtureIndex(real, rel)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, fixtureKey{}, heldFixture{path: fixture, rel: rel, dir: dir, index: index}), nil
}

// notIndexed is the error of a read on a witness of a file git's index
// does not list below the fixture, or of a symlink it lists as a file. A
// clone holds no such file or link, so errors.Is reads it as
// fs.ErrNotExist, as the read in CI would fail. file is the path the spec
// wrote and entry, relative to the fixture and written with slashes, is
// the one the index lacks: the file itself, or a link on the way or its
// target, which the message then names as the one to commit. asFile marks
// a link the index lists as a file. asLink marks a file the index lists as
// a symlink: a clone follows the link there, to what may be no file of
// the fixture, so the read is unknown and not a file that is not there.
// missing names what the index lists at entry, such as "a file", where
// the disk holds nothing or another kind of thing (tree.form): a clone
// holds it, so the read is unknown too.
type notIndexed struct {
	file, entry    string
	asFile, asLink bool
	missing        string
}

func (e notIndexed) Error() string {
	whole := e.entry == filepath.ToSlash(filepath.Clean(e.file))
	if e.missing != "" {
		reads := " in git's index below the fixture, and the disk does not hold it as one, so a clone of the repository holds what this run cannot read; restore it, or commit what the disk holds"
		if whole {
			return e.file + " is " + e.missing + reads
		}
		return e.file + " needs " + e.entry + ", which is " + e.missing + reads
	}
	if e.asLink {
		const reads = " no symlink on disk, while git's index below the fixture lists it as one, so a clone of the repository reads another file there; commit what the disk holds, or restore the link"
		if whole {
			return e.file + " is" + reads
		}
		return e.file + " needs " + e.entry + ", which is" + reads
	}
	if e.asFile {
		return e.file + " needs " + e.entry + ", which git's index below the fixture lists as a file and not as a symlink, so a clone of the repository holds no link there; commit the link"
	}
	const lacks = " not in git's index below the fixture, so a clone of the repository does not hold it; commit "
	if whole {
		return e.file + " is" + lacks + "it"
	}
	return e.file + " needs " + e.entry + ", which is" + lacks + e.entry
}

func (e notIndexed) Is(target error) bool {
	return target == fs.ErrNotExist && !e.asLink && e.missing == ""
}

// form refuses, on a witness, at, a path relative to the fixture on the
// way to file, where git's index lists a file, a symlink or a directory
// (gitindex.Index.Form) that the disk does not hold as one: nothing there
// (found false) or another kind of thing (disk, as Lstat gives it). A
// clone holds what the index lists, so the read is unknown, never a file
// that is not there, as ADR 0008 has it for a glob's file missing from
// the working tree. last says at is file's last name. It passes where the
// clone and this run both read nothing: a name below a committed file,
// which a clone holds as no directory, and a link put where the index
// lists a file, on the way, which linked refuses; and it leaves a file
// put where the index lists a symlink, at the end, to indexed (asLink).
// It passes anything on another run.
func (t tree) form(file, at string, disk fs.FileMode, found, last bool) error {
	if t.index == nil {
		return nil
	}
	e, listed := t.index.Form(filepath.ToSlash(at))
	if !listed {
		return nil
	}
	link := disk&fs.ModeSymlink != 0
	var holds bool
	what := "a file"
	switch {
	case e.Dir():
		holds, what = found && disk.IsDir(), "a directory"
	case e.Symlink():
		holds, what = found && (link || last && disk.IsRegular()), "a symlink"
	default:
		holds = !last || found && !link && !disk.IsDir()
	}
	if holds {
		return nil
	}
	return notIndexed{file: file, entry: filepath.ToSlash(at), missing: what}
}

// indexed refuses rel, where a read of file lands once resolve has
// followed every link on the way, on a witness whose index does not list
// it below the fixture, or lists it as a symlink, which the disk does not
// hold there since resolve followed none; it passes anything on another
// run.
func (t tree) indexed(file, rel string) error {
	if t.index == nil {
		return nil
	}
	e, ok := t.index.Lookup(filepath.ToSlash(rel))
	if !ok {
		return notIndexed{file: file, entry: filepath.ToSlash(rel)}
	}
	if e.Symlink() {
		return notIndexed{file: file, entry: filepath.ToSlash(rel), asLink: true}
	}
	return nil
}

// linked refuses the symlink at rel, on the way to file, on a witness
// whose index does not list it below the fixture as a symlink, and passes
// anything on another run. A clone checks out what the index lists as a
// file as a file, so a link put in its place on disk leads where no path
// of the clone does.
func (t tree) linked(file, rel string) error {
	if t.index == nil {
		return nil
	}
	e, ok := t.index.Lookup(filepath.ToSlash(rel))
	if !ok {
		return notIndexed{file: file, entry: filepath.ToSlash(rel)}
	}
	if !e.Symlink() {
		return notIndexed{file: file, entry: filepath.ToSlash(rel), asFile: true}
	}
	return nil
}

// heldAt is the fixture a run at root reads on a witness (OnFixture), and
// whether the run is a witness's. A witness whose fixture is not held, or
// is held at another path, is refused: the directory and the index held
// are the fixture's, and of no other root.
func heldAt(ctx context.Context, root string) (heldFixture, bool, error) {
	if _, witness := check.RealRoot(ctx); !witness {
		return heldFixture{}, false, nil
	}
	held, _ := ctx.Value(fixtureKey{}).(heldFixture)
	if held.dir == nil || filepath.Clean(root) != filepath.Clean(held.path) {
		return heldFixture{}, true, fmt.Errorf("the witness at %s is not a fixture opened inside the repository root", Quote(root))
	}
	return held, true, nil
}

// runIndex is git's index as a glob of a run at root reads it. On a real
// run it is the index at root, whose globs pass over the fixtures of
// witnesses (gitindex.Index). On a run of a witness (check.RealRoot) root
// is the fixture, which holds no .git: the index is the one OnFixture read
// for the fixture held, the real root's entries below the fixture with the
// fixture's path cut, so a glob reads the files the fixture commits and
// not an untracked one, and git's index is not read again.
func runIndex(ctx context.Context, root string) (gitindex.Index, error) {
	held, witness, err := heldAt(ctx, root)
	if err != nil {
		return gitindex.Index{}, err
	}
	if !witness {
		return readIndex(root)
	}
	return held.index, nil
}

// openTree opens root for a read. On a witness's run root is the fixture,
// and the read goes through the directory the engine opened when it
// checked the fixture (OnFixture), so a symlink swapped in at
// .errata, at .errata/witness or at the fixture since then moves nothing:
// the read stays in the directory that was checked, or is refused when
// that directory is gone. A root other than the fixture held is refused,
// since the held directory is no other. Any other run opens root by its
// path.
func openTree(ctx context.Context, root string) (tree, error) {
	held, witness, err := heldAt(ctx, root)
	if err != nil {
		return tree{}, err
	}
	if witness {
		// The repository's root is the caller's, as on a real run; only
		// the fixture below it is held.
		real, _ := check.RealRoot(ctx)
		realReal, err := filepath.EvalSymlinks(real)
		if err != nil {
			return tree{}, fmt.Errorf("open the repository root: %w", bare(err))
		}
		return tree{Root: held.dir, real: filepath.Join(realReal, held.rel), held: true, dirs: &chain{}, index: &held.index}, nil
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return tree{}, fmt.Errorf("open the repository root: %w", bare(err))
	}
	r, err := os.OpenRoot(realRoot)
	if err != nil {
		return tree{}, fmt.Errorf("open the repository root: %w", bare(err))
	}
	return tree{Root: r, real: realRoot, dirs: &chain{}, git: &gitIndexOnce{}}, nil
}

// close closes the directories t holds below its root, and t, unless it
// is the fixture the engine holds.
func (t tree) close() {
	for _, r := range t.dirs.roots {
		_ = r.Close()
	}
	t.dirs.names, t.dirs.roots = nil, nil
	if !t.held {
		_ = t.Close()
	}
}

// find is where a read of file lands in t, relative to t's root, once
// every link on the way is followed (resolve), and the type of what is
// there as Lstat gives it. On a witness a file there must be one git's
// index lists below the fixture, and not as a symlink (indexed), so every
// read, stat and resolution of a file on a witness finds what a clone
// holds; a directory is no entry of the index, and is the caller's to
// refuse or report. On any other run it passes what resolve finds.
func (t tree) find(file string) (string, fs.FileMode, error) {
	rel, mode, err := t.resolve(file)
	if err == nil && !mode.IsDir() {
		err = t.indexed(file, rel)
	}
	if err != nil {
		return "", 0, t.leftOut(file, err)
	}
	return rel, mode, nil
}

// leftOut is err, the error of a read of file that found nothing at the
// name or a file on the way where a directory should be, refused instead
// when git's index says the working tree lacks a file a clone holds there
// (gitindex.Index.LeftOut): a file git marks skip-worktree, as a sparse
// checkout leaves it out, or a directory above it a sparse index lists as
// one entry. Both the path file names and the one the read reached once
// resolve followed the links on the way (missedAt) are looked up, so a
// tracked link into a directory left out is refused too. The read is then
// unknown, and exists is not false (ADR 0006). The index is read only here,
// once a read misses, and once for the tree (gitIndexOnce): the one of the
// repository that holds the root, which may be a directory above it, as git
// finds it (repositoryOf). A root that no repository holds has no sparse
// checkout, and an index that cannot be read is refused, since it cannot
// say. On a witness the index is the fixture's. Any other error passes as
// it is.
func (t tree) leftOut(file string, err error) error {
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return err
	}
	ix, below := t.index, ""
	if ix == nil {
		g := t.git
		if g == nil {
			g = &gitIndexOnce{}
		}
		if !g.read {
			g.top, g.below = repositoryOf(t.real)
			g.ix, g.err = readIndex(g.top)
			g.read = true
		}
		if errors.Is(g.err, gitindex.ErrNoRepository) {
			return err
		}
		if g.err != nil {
			return fmt.Errorf("it is not in the working tree, and git's index, which says whether a sparse checkout leaves it out, cannot be read: %w", g.err)
		}
		ix, below = &g.ix, g.below
	}
	paths := []string{filepath.Clean(file)}
	var missed missedAt
	if errors.As(err, &missed) && missed.at != paths[0] {
		paths = append(paths, missed.at)
	}
	for _, p := range paths {
		at, left := ix.LeftOut(path.Join(below, filepath.ToSlash(p)))
		switch {
		case !left:
			continue
		case strings.HasSuffix(at, "/"):
			return fmt.Errorf("the sparse checkout leaves out %s, the directory that holds it, which git's sparse index lists as one entry, so errata cannot read what a clone holds there; add the directory to the sparse checkout (git sparse-checkout add), or run errata in a full checkout", Quote(at))
		default:
			return fmt.Errorf("git marks %s skip-worktree and the working tree does not hold it, as a sparse checkout leaves a file out, so errata cannot read what a clone holds there; add it to the sparse checkout (git sparse-checkout add), or run errata in a full checkout", Quote(at))
		}
	}
	return err
}

// maxAbove is how many directories above a root repositoryOf looks in for
// the .git that holds it.
const maxAbove = 64

// repositoryOf is the top of the git repository that holds root, a path
// with its links resolved, and root's path below it written with slashes,
// "" when root is the top. It looks as git's discovery looks: in root, then
// in each directory above it, at most maxAbove up, for a .git. A .git
// directory that is no git directory (gitindex.IsGitDir), such as an empty
// one, is passed over, and the look goes on up; a .git file stops it, and
// readIndex reads or refuses what it names. The look stops where the
// directory above is on another file system than root (deviceOf), as git
// stops without GIT_DISCOVERY_ACROSS_FILESYSTEM. A root that none holds is
// its own top, where readIndex refuses it as no repository.
func repositoryOf(root string) (string, string) {
	dev, known := deviceOf(root)
	dir := root
	for range maxAbove {
		dot := filepath.Join(dir, ".git")
		if info, err := os.Lstat(dot); err == nil && (!info.IsDir() || gitindex.IsGitDir(dot, indexLimits)) {
			rel, err := filepath.Rel(dir, root)
			if err != nil || rel == "." {
				return dir, ""
			}
			return dir, filepath.ToSlash(rel)
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		if d, ok := deviceOf(up); known && ok && d != dev {
			break
		}
		dir = up
	}
	return root, ""
}

// deviceOf is the device of the file system that holds dir, as the OS's
// Stat gives it, of a type that differs by OS and is only compared, and
// whether the OS gives one (statDevice); a test replaces it to stand for a
// mount point, so no test that does may run in parallel.
var deviceOf = statDevice

// missedAt is the error of a read whose resolution found nothing, and at,
// relative to the root, the path the read would have reached there once
// the links on the way were followed: the name it missed and the names of
// file after it.
type missedAt struct {
	err error
	at  string
}

func (e missedAt) Error() string { return e.err.Error() }

func (e missedAt) Unwrap() error { return e.err }

// gitIndexOnce is git's index as a tree read it once for leftOut, and the
// error of that read.
type gitIndexOnce struct {
	top, below string // the repository's top and the root below it (repositoryOf)
	ix         gitindex.Index
	err        error
	read       bool
}

// Listing is a directory's entries as a walk or a listing reads them: an
// *os.File on disk, or on a witness what git's index lists there
// (indexListing).
type Listing interface {
	ReadDir(n int) ([]fs.DirEntry, error)
	Close() error
}

// folder is a directory a walk lists and enters: one open on disk
// (diskDir), or on a witness one of git's index below the fixture
// (indexDir).
type folder interface {
	list() (Listing, error)
	enter(name string) (folder, error)
	close()
}

// top is the folder a walk or a listing of t's root reads. On a witness
// it is what git's index lists below the fixture, so a walk and a listing
// name only what a clone holds and never an untracked file, and a file
// they name is read from the disk through find. On any other run it is
// the root's directory on disk. Whoever walks it closes the folders it
// enters, and t.close closes top.
func (t tree) top() folder {
	if t.index != nil {
		return indexDir{index: t.index, rel: "."}
	}
	return diskDir{t.Root}
}

// diskDir is a directory open on disk.
type diskDir struct{ r *os.Root }

func (d diskDir) list() (Listing, error) { return d.r.Open(".") }

func (d diskDir) enter(name string) (folder, error) {
	r, err := d.r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return diskDir{r}, nil
}

func (d diskDir) close() { _ = d.r.Close() }

// indexDir is the directory rel of a fixture as git's index lists it,
// relative to the fixture and written with slashes. Entering a directory
// opens nothing, so a directory the index lists and the disk lacks is
// walked as the clone holds it, and each of its files reads as missing
// from the working tree.
type indexDir struct {
	index *gitindex.Index
	rel   string
}

func (d indexDir) list() (Listing, error) {
	return &indexListing{entries: d.index.Dir(d.rel)}, nil
}

func (d indexDir) enter(name string) (folder, error) {
	return indexDir{index: d.index, rel: path.Join(d.rel, name)}, nil
}

func (indexDir) close() {}

// indexListing reads the entries of an indexDir, n at a time as an
// *os.File's ReadDir gives them: io.EOF once none is left for n > 0, and
// the rest at once for n <= 0.
type indexListing struct{ entries []gitindex.Entry }

func (l *indexListing) ReadDir(n int) ([]fs.DirEntry, error) {
	if n > 0 && len(l.entries) == 0 {
		return nil, io.EOF
	}
	if n <= 0 {
		n = len(l.entries)
	}
	n = min(n, len(l.entries))
	out := make([]fs.DirEntry, n)
	for i, e := range l.entries[:n] {
		out[i] = indexEntry{e}
	}
	l.entries = l.entries[n:]
	return out, nil
}

func (*indexListing) Close() error { return nil }

// indexEntry is an entry of git's index as a listing gives it.
type indexEntry struct{ e gitindex.Entry }

func (d indexEntry) Name() string { return d.e.Path }
func (d indexEntry) IsDir() bool  { return d.e.Dir() }

func (d indexEntry) Type() fs.FileMode {
	switch {
	case d.e.Dir():
		return fs.ModeDir
	case d.e.Symlink():
		return fs.ModeSymlink
	}
	return 0
}

// Info is refused: git's index holds no size or time a reader can use, and
// a file is read from the disk (tree.find).
func (d indexEntry) Info() (fs.FileInfo, error) {
	return nil, fmt.Errorf("%s is listed from git's index, which gives no file information", Quote(d.e.Path))
}

// resolve follows the symlinks on the way to file and returns the path,
// relative to t's root, where it ends, and the type of the file there as
// Lstat gives it. It walks one name at a time as the
// kernel does, so a .. in a link's target goes up from where the link
// points. It looks only at paths inside the root, each through t's
// os.Root, and refuses a link that leads out before it touches anything
// outside. A route may pass through a directory above the root on its way
// back in, since those directories are real: t.real is the root with its
// own links resolved, and the route is followed by name, touching nothing
// above the root. A .. in file itself is resolved by name first, by Clean,
// before any link.
func (t tree) resolve(file string) (string, fs.FileMode, error) {
	realRoot := t.real
	todo := splitPath(filepath.Clean(file))
	cur := realRoot
	mode := fs.ModeDir // of cur
	links := 0
	for len(todo) > 0 {
		name := todo[0]
		todo = todo[1:]
		if name == ".." {
			cur, mode = filepath.Dir(cur), fs.ModeDir // cur holds no link, so .. is its parent
			continue
		}
		next := filepath.Join(cur, name)
		if !within(realRoot, next) {
			if !within(next, realRoot) {
				return "", 0, errOutside
			}
			cur, mode = next, fs.ModeDir // a directory above the root, on the way back to it
			continue
		}
		at, err := filepath.Rel(realRoot, next)
		if err != nil {
			return "", 0, err
		}
		info, err := t.lstat(at)
		if err != nil {
			// ENOTDIR needs no form check: the name before at is a file on
			// disk, which form looked at on the step before, and refused
			// there when the index lists a directory; where the index
			// lists a file, nothing below it is listed (Below refuses the
			// pair).
			if errors.Is(err, fs.ErrNotExist) {
				if err := t.form(file, at, 0, false, len(todo) == 0); err != nil {
					return "", 0, err
				}
			}
			return "", 0, missedAt{err: bare(err), at: filepath.Join(append([]string{at}, todo...)...)}
		}
		if err := t.form(file, at, info.Mode(), true, len(todo) == 0); err != nil {
			return "", 0, err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			cur, mode = next, info.Mode()
			continue
		}
		if links++; links > maxLinks {
			return "", 0, errors.New("too many levels of symbolic links")
		}
		// On a witness a link the fixture does not commit as a symlink is
		// not in a clone, so nothing is followed through it. git's index
		// lists a symlink as an entry of its own, with its mode, so a
		// committed one is followed.
		if err := t.linked(file, at); err != nil {
			return "", 0, err
		}
		target, err := t.readlink(at)
		if err != nil {
			return "", 0, bare(err)
		}
		if filepath.IsAbs(target) {
			cur = filepath.VolumeName(target) + string(filepath.Separator)
		}
		todo = append(splitPath(target), todo...)
	}
	if !within(realRoot, cur) {
		return "", 0, errOutside
	}
	rel, err := filepath.Rel(realRoot, cur)
	return rel, mode, err
}

// splitPath lists the names in p, without the empty ones and ".".
func splitPath(p string) []string {
	var out []string
	for _, n := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == filepath.Separator }) {
		if n != "." {
			out = append(out, n)
		}
	}
	return out
}

// within reports whether p is dir or a path below it.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && filepath.IsLocal(rel)
}
