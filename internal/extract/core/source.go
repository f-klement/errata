// Package core holds what every reader of internal/extract shares: the
// file a spec names, held inside the repository and to its size cap
// (Source), the bounds on nodes, values, depth and bytes (limits.go), the
// tree the YAML, JSON and TOML readers build and the path grammar that
// selects from it, the line index, the clocked regex search the regex and
// text extractors run, and how a message quotes file text. No reader
// imports another; each imports this package.
package core

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
// the file is read, since only the file system knows where it points. A
// max_bytes of zero is the default limit, and one below zero or over
// maxMaxBytes is refused.
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

// Resolved is the path, relative to the root, where the file ends once its
// symlinks are followed, or the refusal Read would give for a link that
// leads out or a file that is not there. Two Sources with one Resolved read
// one file.
func (s Source) Resolved(root string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("open the repository root: %w", bare(err))
	}
	rel, _, err := resolve(realRoot, s.File)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", s.File, err)
	}
	return rel, nil
}

// Read reads the file under root. A file over the limit is refused before
// it is read, and a file that grows past it while it is read is refused too.
// Every error names the file as the spec wrote it, never an absolute path.
func (s Source) Read(root string) ([]byte, error) {
	f, _, err := s.open(root)
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
func (s Source) Head(root string, n int) ([]byte, int64, error) {
	f, size, err := s.open(root)
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
func (s Source) open(root string) (*os.File, int64, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, 0, fmt.Errorf("open the repository root: %w", bare(err))
	}
	rel, mode, err := resolve(realRoot, s.File)
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", s.File, err)
	}
	// Opening a device can act on it: a watchdog arms, a serial port
	// raises DTR. So a device is refused by its type before the open.
	if mode&fs.ModeDevice != 0 {
		return nil, 0, fmt.Errorf("read %s: %w", s.File, errDevice)
	}
	r, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, 0, fmt.Errorf("open the repository root: %w", bare(err))
	}
	defer r.Close()
	// The open goes through os.Root, which refuses a link that leads out,
	// so a link swapped in after resolve is refused too. It does not block,
	// so a FIFO, which git never checks out, opens at once, and the type
	// check of the open file refuses it, and a socket, before a byte is
	// read. That check also refuses a device swapped in after resolve, and
	// O_NOCTTY keeps such a terminal from becoming errata's own.
	f, err := r.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
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
// relative to realRoot, where it ends, and the type of the file there as
// Lstat gives it. It walks one name at a time as the
// kernel does, so a .. in a link's target goes up from where the link
// points. It looks only at paths inside realRoot, and refuses a link that
// leads out before it touches anything outside. A route may pass through a
// directory above the root on its way back in, since those directories are
// real: realRoot is the root with its own links resolved. A .. in file itself
// is resolved by name first, by Clean, before any link.
func resolve(realRoot, file string) (string, fs.FileMode, error) {
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
		info, err := os.Lstat(next)
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
		target, err := os.Readlink(next)
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
