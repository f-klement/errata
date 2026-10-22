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
	switch {
	case maxBytes < 0 || maxBytes > MaxMaxBytes:
		return Source{}, fmt.Errorf("%s: max_bytes is %d; write a number of bytes from 1 to %d, or leave it out for %d",
			kind, maxBytes, MaxMaxBytes, DefaultMaxBytes)
	case maxBytes == 0:
		maxBytes = DefaultMaxBytes
	}
	return Source{File: file, Limit: maxBytes}, nil
}

// errOutside refuses a symlink that leads out of the repository. It names
// neither where the link points nor whether that exists: the message reaches
// a CI log that a pull request can read.
var errOutside = errors.New("a symlink leads outside the repository")

// maxLinks is how many symlinks one read follows, as on Linux, before it
// calls the chain a loop.
const maxLinks = 40

// Read reads the file under root. A file over the limit is refused before
// it is read, and a file that grows past it while it is read is refused too.
// Every error names the file as the spec wrote it, never an absolute path.
func (s Source) Read(root string) ([]byte, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("open the repository root: %w", bare(err))
	}
	rel, err := resolve(realRoot, s.File)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.File, err)
	}
	r, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, fmt.Errorf("open the repository root: %w", bare(err))
	}
	defer r.Close()
	// The open goes through os.Root, which refuses a link that leads out,
	// so a link swapped in after resolve is refused too.
	f, err := r.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.File, bare(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.File, bare(err))
	}
	if info.Size() > s.Limit {
		return nil, tooLarge(s.File, info.Size(), s.Limit)
	}
	data, err := io.ReadAll(io.LimitReader(f, s.Limit))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.File, bare(err))
	}
	// A byte past the limit means the file grew while it was read. The
	// limit is never added to, so no limit overflows.
	if n, _ := f.Read(make([]byte, 1)); n > 0 {
		return nil, tooLarge(s.File, int64(len(data))+1, s.Limit)
	}
	return data, nil
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
// relative to realRoot, where it ends. It walks one name at a time as the
// kernel does, so a .. in a link's target goes up from where the link
// points. It looks only at paths inside realRoot, and refuses a link that
// leads out before it touches anything outside. A route may pass through a
// directory above the root on its way back in, since those directories are
// real: realRoot is the root with its own links resolved. A .. in file itself
// is resolved by name first, by Clean, before any link.
func resolve(realRoot, file string) (string, error) {
	todo := splitPath(filepath.Clean(file))
	cur := realRoot
	links := 0
	for len(todo) > 0 {
		name := todo[0]
		todo = todo[1:]
		if name == ".." {
			cur = filepath.Dir(cur) // cur holds no link, so .. is its parent
			continue
		}
		next := filepath.Join(cur, name)
		if !within(realRoot, next) {
			if !within(next, realRoot) {
				return "", errOutside
			}
			cur = next // a directory above the root, on the way back to it
			continue
		}
		info, err := os.Lstat(next)
		if err != nil {
			return "", bare(err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		if links++; links > maxLinks {
			return "", errors.New("too many levels of symbolic links")
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", bare(err)
		}
		if filepath.IsAbs(target) {
			cur = filepath.VolumeName(target) + string(filepath.Separator)
		}
		todo = append(splitPath(target), todo...)
	}
	if !within(realRoot, cur) {
		return "", errOutside
	}
	return filepath.Rel(realRoot, cur)
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
