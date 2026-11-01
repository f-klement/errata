// Package core holds what every reader of internal/extract shares: the
// file a spec names, held inside the repository and to its size cap
// (Source), the bounds on nodes, values, depth and bytes (limits.go), the
// tree the YAML, JSON and TOML readers build and the path grammar that
// selects from it, the line index, the clocked regex search the regex and
// text extractors run, and how a message quotes file text. No reader
// imports another; each imports this package.
package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/f-klement/errata/internal/check"
)

// CheckPath parses a path, refusing an empty one by the field's name.
func CheckPath(kind, p string) (Path, error) {
	if p == "" {
		return Path{}, fmt.Errorf("%s: path is required; write $ for the whole document", kind)
	}
	parsed, err := parsePath(p)
	if err != nil {
		return Path{}, fmt.Errorf("%s: %w", kind, err)
	}
	return parsed, nil
}

// Source is the file an extractor reads, and the most bytes it reads of it.
type Source struct {
	File  string
	Limit int64
}

// CheckSource refuses a file outside the repository: an absolute path, or
// one whose .. leaves the root. A repository's config must not read files
// that are not in the repository. A symlink that leads out is refused when
// the file is read, since only the file system knows where it points. It
// refuses on every OS a path that Windows reads as absolute or climbing
// out, such as C:x, as the rule refuses such a value's location
// (check.Location.Inside), so the config fails at load and not each run.
// It refuses a path that holds a control character, such as a newline, a
// direction control or a line or paragraph separator, which a terminal or
// a report viewer would act on (check.HoldsControl). A max_bytes of zero
// is the default limit, and one below zero or over maxMaxBytes is refused.
func CheckSource(kind, file string, maxBytes int64) (Source, error) {
	if file == "" {
		return Source{}, fmt.Errorf("%s: file is required", kind)
	}
	if len(file) > maxFileName {
		return Source{}, fmt.Errorf("%s: file %s is %d bytes long, over the limit of %d; write a shorter path", kind, Quote(file), len(file), maxFileName)
	}
	if !filepath.IsLocal(file) {
		return Source{}, fmt.Errorf("%s: file %s is outside the repository: write a path relative to the repository root that does not leave it", kind, Quote(file))
	}
	if (check.Location{File: file}).OutsideOnWindows() {
		return Source{}, fmt.Errorf("%s: file %s is absolute, names a drive or climbs out where a backslash separates names, as on Windows, so it is refused on every OS: write a path relative to the repository root that does not leave it", kind, Quote(file))
	}
	if check.HoldsControl(file) {
		return Source{}, fmt.Errorf("%s: file %s holds a control character, a direction control or a line or paragraph separator, which a terminal or a report viewer would act on; rename the file", kind, Quote(file))
	}
	if maxBytes < 0 || maxBytes > MaxMaxBytes {
		return Source{}, fmt.Errorf("%s: max_bytes is %d; write a number of bytes from 1 to %d, or leave it out for %d",
			kind, maxBytes, MaxMaxBytes, DefaultMaxBytes)
	}
	return Source{File: file, Limit: MaxBytes(maxBytes)}, nil
}

// MaxBytes is the limit a max_bytes of n sets: n, or DefaultMaxBytes for a
// zero, which is the key left out. CheckSource reads files to it, and
// errata list prints it, so the two cannot name different limits.
func MaxBytes(n int64) int64 {
	if n == 0 {
		return DefaultMaxBytes
	}
	return n
}

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
	files map[string]bool // on a witness, the files the index lists below the fixture
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
// is given it as its root, its directory, held open, and the files git's
// index lists below it, relative to it and written with slashes: every
// one, which a read finds (indexed), and those a glob matches, without
// the fixtures nested in it (globbed, as trackedFiles lists them).
type heldFixture struct {
	path    string
	dir     *os.Root
	indexed map[string]bool
	globbed []string
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
// It reads git's index of the repository once (indexBelow), and refuses a
// fixture the index lists no file below, whatever kind of check runs on
// it: a clone in CI holds none of its files, so the witness must be
// unknown here as it is there. A read on the run then finds only the
// files the index lists below the fixture (tree.indexed), a nested
// fixture's included, so a fixture that commits some of its files reads
// as its clone would, and a glob on the run matches the list read here.
func OnFixture(ctx context.Context, fixture string, dir *os.Root) (context.Context, error) {
	if _, witness := check.RealRoot(ctx); !witness {
		return nil, errors.New("a fixture is held only on a witness's run")
	}
	globbed, below, err := indexBelow(ctx, fixture)
	if err != nil {
		return nil, err
	}
	indexed := make(map[string]bool, len(below))
	for _, f := range below {
		indexed[f] = true
	}
	return context.WithValue(ctx, fixtureKey{}, heldFixture{path: fixture, dir: dir, indexed: indexed, globbed: globbed}), nil
}

// notIndexed is the error of a read on a witness of a file git's index
// does not list below the fixture. A clone holds no such file, so
// errors.Is reads it as fs.ErrNotExist, as the read in CI would fail.
// file is the path the spec wrote and entry, relative to the fixture and
// written with slashes, is the one the index lacks: the file itself, or
// a link on the way or its target, which the message then names as the
// one to commit.
type notIndexed struct{ file, entry string }

func (e notIndexed) Error() string {
	const lacks = " not in git's index below the fixture, so a clone of the repository does not hold it; commit "
	if e.entry == filepath.ToSlash(filepath.Clean(e.file)) {
		return e.file + " is" + lacks + "it"
	}
	return e.file + " needs " + e.entry + ", which is" + lacks + e.entry
}

func (e notIndexed) Is(target error) bool { return target == fs.ErrNotExist }

// indexed refuses rel, where a read of file lands, on a witness whose index
// does not list it below the fixture, and passes anything on another run.
func (t tree) indexed(file, rel string) error {
	if t.files == nil || t.files[filepath.ToSlash(rel)] {
		return nil
	}
	return notIndexed{file: file, entry: filepath.ToSlash(rel)}
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
	if real, witness := check.RealRoot(ctx); witness {
		held, _ := ctx.Value(fixtureKey{}).(heldFixture)
		rel, err := filepath.Rel(real, root)
		if held.dir == nil || filepath.Clean(root) != filepath.Clean(held.path) || err != nil || !filepath.IsLocal(rel) {
			return tree{}, fmt.Errorf("the witness at %s is not a fixture opened inside the repository root", Quote(root))
		}
		// The repository's root is the caller's, as on a real run; only
		// the fixture below it is held.
		realReal, err := filepath.EvalSymlinks(real)
		if err != nil {
			return tree{}, fmt.Errorf("open the repository root: %w", bare(err))
		}
		return tree{Root: held.dir, real: filepath.Join(realReal, rel), held: true, dirs: &chain{}, files: held.indexed}, nil
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return tree{}, fmt.Errorf("open the repository root: %w", bare(err))
	}
	r, err := os.OpenRoot(realRoot)
	if err != nil {
		return tree{}, fmt.Errorf("open the repository root: %w", bare(err))
	}
	return tree{Root: r, real: realRoot, dirs: &chain{}}, nil
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

// Reader reads files under one root through one tree, opened once, so
// the files an extractor reads share the root's open and the directories
// on their way (chain). Close it when the files are read.
type Reader struct{ t tree }

// OpenReader opens root for reads, as Read opens it (openTree).
func OpenReader(ctx context.Context, root string) (*Reader, error) {
	t, err := openTree(ctx, root)
	if err != nil {
		return nil, err
	}
	return &Reader{t: t}, nil
}

// Read is s.Read under the reader's root.
func (r *Reader) Read(s Source) ([]byte, error) { return s.readIn(r.t) }

// Resolved is s.Resolved under the reader's root.
func (r *Reader) Resolved(s Source) (string, error) { return s.resolvedIn(r.t) }

// Close closes what the reader holds open.
func (r *Reader) Close() { r.t.close() }

// OpenDir opens root to list it, as a read under root opens it (openTree):
// on a witness's run, the fixture the engine opened.
func OpenDir(ctx context.Context, root string) (*os.File, error) {
	t, err := openTree(ctx, root)
	if err != nil {
		return nil, err
	}
	defer t.close()
	d, err := t.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open the repository root: %w", bare(err))
	}
	return d, nil
}

// Resolved is the path, relative to the root, where the file ends once its
// symlinks are followed, or the refusal Read would give for a link that
// leads out or a file that is not there. Two Sources with one Resolved read
// one file.
func (s Source) Resolved(ctx context.Context, root string) (string, error) {
	t, err := openTree(ctx, root)
	if err != nil {
		return "", err
	}
	defer t.close()
	return s.resolvedIn(t)
}

// resolvedIn is Resolved in the tree t, which the caller opened.
func (s Source) resolvedIn(t tree) (string, error) {
	rel, mode, err := resolve(t, s.File)
	if err == nil && !mode.IsDir() {
		err = t.indexed(s.File, rel)
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", s.File, err)
	}
	return rel, nil
}

// Stat is what is at the file under root once the symlinks on the way are
// followed as Read follows them, by Lstat, and it opens nothing, so a device
// or a FIFO there is only looked at. A link that leads out, or a route
// through a directory outside the repository, is refused as Read refuses
// it, before anything outside is looked at. When nothing is at the name, or
// a name on the way to it is a file and no directory, the error is one
// that errors.Is reads as fs.ErrNotExist. Every error names the file as the
// spec wrote it.
func (s Source) Stat(ctx context.Context, root string) (fs.FileInfo, error) {
	// A root that is not there, or is no directory, says nothing of the
	// file, so its error keeps the text and drops the fs.ErrNotExist. The
	// root is opened before resolve, since resolve would read a file at the
	// root as a name below a file, ENOTDIR, which is a missing file.
	t, err := openTree(ctx, root)
	if err != nil {
		return nil, errors.New(err.Error())
	}
	defer t.close()
	rel, _, err := resolve(t, s.File)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", s.File, notThere(err))
	}
	// Through os.Root, which refuses a link that leads out, so a link
	// swapped in on the way after resolve is refused too. Lstat does not
	// follow a link swapped in at the name itself: the caller sees a
	// symlink, which is no regular file.
	info, err := t.lstat(rel)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", s.File, notThere(bare(err)))
	}
	// A directory is no entry of the index, and stays unknown to exists
	// and size, as on any run.
	if !info.IsDir() {
		if err := t.indexed(s.File, rel); err != nil {
			return nil, fmt.Errorf("stat %s: %w", s.File, err)
		}
	}
	return info, nil
}

// noDirectory is the error of a lookup through a name that is a file and no
// directory, such as a/b where a is a file: nothing is at the name, so
// errors.Is reads it as fs.ErrNotExist.
type noDirectory struct{ err error }

func (e noDirectory) Error() string { return e.err.Error() }

func (e noDirectory) Is(target error) bool { return target == fs.ErrNotExist }

// notThere is err, read as fs.ErrNotExist when it is ENOTDIR.
func notThere(err error) error {
	if errors.Is(err, syscall.ENOTDIR) {
		return noDirectory{err}
	}
	return err
}

// Read reads the file under root. A file over the limit is refused before
// it is read, and a file that grows past it while it is read is refused too.
// Every error names the file as the spec wrote it, never an absolute path.
func (s Source) Read(ctx context.Context, root string) ([]byte, error) {
	t, err := openTree(ctx, root)
	if err != nil {
		return nil, err
	}
	defer t.close()
	return s.readIn(t)
}

// readIn is Read in the tree t, which the caller opened, so the files of a
// set open their root once.
func (s Source) readIn(t tree) ([]byte, error) {
	f, _, err := s.openIn(t)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return s.readLimited(f)
}

// readLimited reads r to the limit, and refuses it when it holds a byte
// more: the file grew while it was read. The limit is never added to, so no
// limit overflows.
func (s Source) readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, s.Limit))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.File, bare(err))
	}
	if n, _ := r.Read(make([]byte, 1)); n > 0 {
		return nil, tooLarge(s.File, int64(len(data))+1, s.Limit)
	}
	return data, nil
}

// Head is the first n bytes of the file under root, all of it when it is
// shorter, and the file's size in bytes, refused as Read refuses the file.
func (s Source) Head(ctx context.Context, root string, n int) ([]byte, int64, error) {
	f, size, err := s.open(ctx, root)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(n)))
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", s.File, bare(err))
	}
	return data, size, nil
}

// open opens the file under root, refusing a link that leads out, a file
// that is not there and one over the limit, and returns its size.
func (s Source) open(ctx context.Context, root string) (*os.File, int64, error) {
	t, err := openTree(ctx, root)
	if err != nil {
		return nil, 0, err
	}
	defer t.close()
	return s.openIn(t)
}

// openIn is open in the tree t, which the caller opened.
func (s Source) openIn(t tree) (*os.File, int64, error) {
	rel, mode, err := resolve(t, s.File)
	if err == nil && !mode.IsDir() {
		err = t.indexed(s.File, rel)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", s.File, err)
	}
	// Opening a device can act on it: a watchdog arms, a serial port
	// raises DTR. So a device is refused by its type before the open.
	if mode&fs.ModeDevice != 0 {
		return nil, 0, fmt.Errorf("read %s: %w", s.File, errDevice)
	}
	// The open goes through os.Root, which refuses a link that leads out,
	// so a link swapped in after resolve is refused too. It does not block,
	// so a FIFO, which git never checks out, opens at once, and the type
	// check of the open file refuses it, and a socket, before a byte is
	// read. That check also refuses a device swapped in after resolve, and
	// O_NOCTTY keeps such a terminal from becoming errata's own.
	dir, name := t.at(rel)
	f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", s.File, bare(err))
	}
	info, err := f.Stat()
	if err == nil {
		err = regular(info)
	}
	if err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("read %s: %w", s.File, bare(err))
	}
	if info.Size() > s.Limit {
		_ = f.Close()
		return nil, 0, tooLarge(s.File, info.Size(), s.Limit)
	}
	return f, info.Size(), nil
}

// errDevice refuses a device, such as /dev/null, before it is opened.
var errDevice = errors.New("it is a device, which errata does not open")

// ErrDirectory refuses a directory where a file was named, as the read of
// one would. A reader that passes over a file it looked for and did not
// find tests for it with errors.Is, to pass over a directory at that name too.
var ErrDirectory = errors.New("is a directory")

// regular refuses a file that is not a regular file: a directory as the
// read of one would, and a FIFO, a socket or a device as no file of the
// repository.
func regular(info fs.FileInfo) error {
	if info.IsDir() {
		return ErrDirectory
	}
	if !info.Mode().IsRegular() {
		return errors.New("it is not a regular file")
	}
	return nil
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
func resolve(t tree, file string) (string, fs.FileMode, error) {
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
			return "", 0, bare(err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			cur, mode = next, info.Mode()
			continue
		}
		if links++; links > maxLinks {
			return "", 0, errors.New("too many levels of symbolic links")
		}
		// On a witness a link the fixture does not commit is not in a
		// clone, so nothing is followed through it. git's index lists a
		// symlink as an entry of its own, so a committed one is followed.
		if err := t.indexed(file, at); err != nil {
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
