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
	"syscall"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core/gitindex"
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

// Reader reads files under one root through one tree, opened once, so
// the files an extractor reads share the root's open and the directories
// on their way (chain). Close it when the files are read.
type Reader struct {
	t tree
	// shared is what the run reads under the root, nil on a witness's run,
	// whose fixture each check opens again.
	shared *sharedFiles
}

// OpenReader opens root for reads, as Read opens it (openTree).
func OpenReader(ctx context.Context, root string) (*Reader, error) {
	t, err := openTree(ctx, root)
	if err != nil {
		return nil, err
	}
	return readerOf(ctx, root, t), nil
}

// readerOf is the Reader of the tree t, open at root: on a run other than
// a witness's, one that shares what it reads with the run's other readers
// of root (sharedAt).
func readerOf(ctx context.Context, root string, t tree) *Reader {
	r := &Reader{t: t}
	if !t.held {
		r.shared = sharedAt(ctx, root)
	}
	return r
}

// Read is s.Read under the reader's root. A file the run read before, for
// any extractor, is not read again: its bytes, or the refusal of its read,
// are the run's, held to s.Limit as a read of s holds them.
func (r *Reader) Read(s Source) ([]byte, error) {
	if r.shared == nil {
		return s.readIn(r.t)
	}
	return r.shared.read(s, r.t)
}

// Resolved is s.Resolved under the reader's root, looked up once in a run
// for every extractor, as Read reads a file once.
func (r *Reader) Resolved(s Source) (string, error) {
	if r.shared == nil {
		return s.resolvedIn(r.t)
	}
	return r.shared.resolved(s, r.t)
}

// Open is the file s names under the reader's root, open for a reader that
// reads it in pieces, and its size, refused as Read refuses the file: a
// file over s.Limit too. The caller closes it.
func (r *Reader) Open(s Source) (*os.File, int64, error) { return s.openIn(r.t) }

// Close closes what the reader holds open.
func (r *Reader) Close() { r.t.close() }

// OpenDir opens root to list it, as a read under root opens it (openTree):
// on a witness's run it lists what git's index lists at the top of the
// fixture, as a clone holds it, and on any other run the root's directory
// (tree.top).
func OpenDir(ctx context.Context, root string) (Listing, error) {
	t, err := openTree(ctx, root)
	if err != nil {
		return nil, err
	}
	defer t.close()
	d, err := t.top().list()
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
	rel, _, err := t.find(s.File)
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
	return s.statIn(t)
}

// Stat is s.Stat under the reader's root, through its open tree.
func (r *Reader) Stat(s Source) (fs.FileInfo, error) { return s.statIn(r.t) }

// statIn is Stat through the tree t.
func (s Source) statIn(t tree) (fs.FileInfo, error) {
	rel, mode, err := t.find(s.File)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", s.File, notThere(err))
	}
	// Through os.Root, which refuses a link that leads out, so a link
	// swapped in on the way after resolve is refused too. Lstat does not
	// follow a link swapped in at the name itself. What it sees must be
	// the kind of thing find looked at and checked against the index, so
	// a file swapped in where find saw a directory, or a link where it saw
	// a file, is refused. Only a change between the two looks meets it,
	// so no test plants it.
	info, err := t.lstat(rel)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", s.File, notThere(bare(err)))
	}
	if info.Mode().Type() != mode.Type() {
		return nil, fmt.Errorf("stat %s: it changed while errata looked at it; run again", s.File)
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
// Within a run, the file is read once for every extractor (Reader.Read).
func (s Source) Read(ctx context.Context, root string) ([]byte, error) {
	t, err := openTree(ctx, root)
	if err != nil {
		return nil, err
	}
	defer t.close()
	return readerOf(ctx, root, t).Read(s)
}

// readIn is Read in the tree t, which the caller opened, so the files of a
// set open their root once.
func (s Source) readIn(t tree) ([]byte, error) {
	data, _, err := s.readSized(t)
	return data, err
}

// readSized is readIn with the size of the file when it was opened.
func (s Source) readSized(t tree) ([]byte, int64, error) {
	f, size, err := s.openIn(t)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	data, err := s.ReadLimited(f)
	return data, size, err
}

// ReadLimited reads r, the file s names as Reader.Open opened it, to the
// limit, and refuses it when it holds a byte more: the file grew while it
// was read. The limit is never added to, so no limit overflows.
func (s Source) ReadLimited(r io.Reader) ([]byte, error) {
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
	rel, mode, err := t.find(s.File)
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
var errDevice = gitindex.ErrDevice

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
