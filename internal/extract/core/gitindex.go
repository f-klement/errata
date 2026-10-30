package core

import (
	"bytes"
	"encoding/binary"
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

// maxIndex is the largest git index readIndex reads, in bytes, the
// ceiling of max_bytes. An entry is about 70 bytes and its path, so it holds
// some 600000 files; the Linux kernel's index, of 90000 files, is 10 MB.
const maxIndex = MaxMaxBytes

// maxGitFile is the most bytes of a .git file readIndex reads: one line,
// gitdir: and a path, which is at most maxFileName long.
const maxGitFile = 4160 // maxFileName and 64 bytes

// The modes of an index entry git checks out as a file: a regular file,
// executable or not, and a symlink. A gitlink (a submodule's commit) and a
// sparse index's directory are no file of this repository.
const (
	modeType    = 0o170000
	modeFile    = 0o100000
	modeSymlink = 0o120000
)

// The flags of an index entry: whether a second flags word follows, in
// version 3 and later, and in that word, skip-worktree, which marks a file
// a sparse checkout leaves out of the working tree.
const (
	flagExtended     = 0x4000
	flagSkipWorktree = 0x4000
	nameMask         = 0x0fff
)

// readIndex lists the files of git's index at the repository root root
// that the working tree holds: those git ls-files lists, relative to root,
// with slashes, in git's order, but each once, a file in conflict too, and
// without a submodule or a file a sparse checkout leaves out
// (skip-worktree). It reads .git/index itself, and runs no git: the index
// of a worktree or a submodule is in the directory its .git file names.
// Versions 2, 3 and 4 of the index are read, with SHA-1 or SHA-256 object
// names; a split index, whose entries are in a second file, is refused. A
// root that is not the top of a git repository is refused, and so is a
// .git file that names a directory that is not there, an index that is not
// a regular file, and one over maxIndex bytes. It is unexported so that no
// reader lists the index but through trackedFiles, which leaves out the
// fixtures of witnesses (ADR 0015).
func readIndex(root string) ([]string, error) {
	dir, err := gitDir(root)
	if err != nil {
		return nil, err
	}
	data, err := readCapped(filepath.Join(dir, "index"), maxIndex)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil // a repository that has never added a file
	}
	if err != nil {
		return nil, fmt.Errorf("read git's index: %w", bare(err))
	}
	// The object format is tried, not read from the config: an index read
	// with the wrong one fails the path length and trailer checks.
	files, err := parseIndex(data, sha1Size, maxPathBytes)
	if err != nil {
		var again error
		if files, again = parseIndex(data, sha256Size, maxPathBytes); again != nil {
			return nil, fmt.Errorf("read git's index: %w", err)
		}
	}
	return files, nil
}

// maxPathBytes is the most bytes the paths of an index hold in all. A
// version 4 index writes each path as what it keeps of the path before and
// a suffix, so 65 bytes of the file can make a path of 4096: without a
// bound, a 64 MiB index of such entries holds gigabytes of paths. The bound
// is maxIndex, which no index of plain paths can pass, since each of them
// is in the file.
const maxPathBytes = maxIndex

// The sizes of an object name: SHA-1, and SHA-256, which a repository made
// with --object-format=sha256 uses.
const (
	sha1Size   = 20
	sha256Size = 32
)

// gitDir is the directory of root's repository: root/.git, or the directory
// a .git file names, as a worktree's and a submodule's do.
func gitDir(root string) (string, error) {
	dot := filepath.Join(root, ".git")
	info, err := os.Lstat(dot)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("the repository root holds no .git, so errata cannot list the files git tracks; run errata at the top of a git repository")
	}
	if err != nil {
		return "", fmt.Errorf("read .git: %w", bare(err))
	}
	if info.IsDir() {
		if err := isGitDir(dot); err != nil {
			return "", fmt.Errorf(".git is no git directory: %w", err)
		}
		return dot, nil
	}
	if !info.Mode().IsRegular() {
		return "", errors.New(".git is neither a directory nor a file")
	}
	data, err := readCapped(dot, maxGitFile)
	if err != nil {
		return "", fmt.Errorf("read .git: %w", bare(err))
	}
	// git's read_gitfile_gently drops the line breaks at the end and takes
	// the rest after "gitdir: " as the path, so a second line is part of
	// it, which names no directory.
	target, ok := strings.CutPrefix(strings.TrimRight(string(data), "\r\n"), "gitdir: ")
	if !ok || target == "" {
		return "", errors.New("the file .git names no gitdir")
	}
	if strings.ContainsAny(target, "\r\n") {
		return "", errors.New("the file .git holds more than its gitdir: line, which git refuses")
	}
	// A worktree's gitdir is outside the root by design, in the main
	// repository's .git/worktrees, so no place is refused: what is read
	// there is a list of paths, each of which is read through Source,
	// inside the root.
	dir := target
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	// A gitdir that is not there, as in a worktree mounted alone into a
	// container, is no repository that tracks nothing: every glob would
	// match nothing and the rule pass on files it did not read.
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("the file .git names the git directory %s, which is not there; mount the repository the worktree or submodule belongs to", Quote(target))
	}
	// A directory that is there but no git directory, such as ., / or an
	// empty mount point, holds no index either, and is refused as git
	// refuses it.
	if err := isGitDir(dir); err != nil {
		return "", fmt.Errorf("the file .git names %s, which is no git directory: %w", Quote(target), err)
	}
	return dir, nil
}

// isGitDir refuses a directory git does not take for a git directory, as
// setup.c's is_git_directory tests it: HEAD a file, or a symlink whose
// target starts with refs/, which validate_headref takes without following
// it, since the branch may be packed; and objects and refs directories, in
// the common directory a worktree's commondir file names when it has one.
func isGitDir(dir string) error {
	if !headRef(filepath.Join(dir, "HEAD")) {
		return errors.New("it holds no HEAD git takes: a file, or a symlink into refs/")
	}
	common := dir
	data, err := readCapped(filepath.Join(dir, "commondir"), maxGitFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read its commondir: %w", bare(err))
	}
	if err == nil {
		common = strings.TrimRight(string(data), "\r\n")
		if !filepath.IsAbs(common) {
			common = filepath.Join(dir, common)
		}
	}
	for _, sub := range []string{"objects", "refs"} {
		if info, err := os.Stat(filepath.Join(common, sub)); err != nil || !info.IsDir() {
			return fmt.Errorf("it holds no %s directory", sub)
		}
	}
	return nil
}

// headRef reports whether head is a HEAD git takes: a regular file, or a
// symlink into refs/, as a repository made with core.preferSymlinkRefs
// has. A Readlink that fails gives "", which is no ref.
func headRef(head string) bool {
	info, err := os.Lstat(head)
	if err != nil {
		return false
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, _ := os.Readlink(head)
		return strings.HasPrefix(target, "refs/")
	}
	return info.Mode().IsRegular()
}

// readCapped reads the regular file name, refusing one over limit bytes and
// anything else that is no regular file. A device, as a symlink at
// .git/index may name, is refused before the open, which can act on it.
// The open does not block, so a FIFO put in place of the file, which git
// never checks out, is refused rather than waited on, and O_NOCTTY keeps a
// terminal swapped in after the check from becoming errata's own.
func readCapped(name string, limit int64) ([]byte, error) {
	// A Stat that fails is left to the open, which fails the same way.
	if info, err := os.Stat(name); err == nil && info.Mode()&fs.ModeDevice != 0 {
		return nil, errDevice
	}
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("it is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the file is over the limit of %d bytes", limit)
	}
	return data, nil
}

// parseIndex reads the entries of an index whose object names are hash
// bytes long, as git's Documentation/gitformat-index.adoc lays them out. An
// entry is 40 bytes of stat data and its mode, the object name, a flags
// word whose low 12 bits are the path's length (0xfff for 0xfff and over),
// in version 3 and later a second flags word when the first says so, and
// the path: in versions 2 and 3 whole and padded with 1 to 8 NULs to a
// multiple of 8 bytes, in version 4 as how many bytes to drop from the end
// of the path before and what to append, with one NUL. Every path is held
// to its length, so an index read with the wrong object size is refused,
// to maxFileName bytes and to a path inside the repository, as git's
// verify_path holds them, and the paths in all to budget bytes.
func parseIndex(data []byte, hash, budget int) ([]string, error) {
	if len(data) < 12 || string(data[:4]) != "DIRC" {
		return nil, errors.New("it does not start with DIRC, git's signature")
	}
	version := binary.BigEndian.Uint32(data[4:8])
	if version < 2 || version > 4 {
		return nil, fmt.Errorf("it is version %d; errata reads versions 2, 3 and 4", version)
	}
	count := int(binary.BigEndian.Uint32(data[8:12]))
	if count > DefaultWalk.Entries {
		return nil, fmt.Errorf("it lists %d entries, over the limit of %d", count, DefaultWalk.Entries)
	}
	fixed := 40 + hash + 2
	pos := 12
	left := budget // the bytes the paths may still hold
	unnamed := false
	files := []string{}
	var prev []byte
	var last string
	for range count {
		start := pos
		if len(data)-pos < fixed {
			return nil, errors.New("an entry runs past the end of the file")
		}
		mode := binary.BigEndian.Uint32(data[pos+24 : pos+28])
		flags := binary.BigEndian.Uint16(data[pos+40+hash:])
		pos += fixed
		var extended uint16
		if flags&flagExtended != 0 {
			if version < 3 || len(data)-pos < 2 {
				return nil, errors.New("an entry's extended flags are not where its version puts them")
			}
			extended = binary.BigEndian.Uint16(data[pos:])
			pos += 2
		}
		var name []byte
		if version == 4 {
			drop, n := indexVarint(data[pos:])
			if n == 0 || drop > len(prev) {
				return nil, errors.New("an entry's path drops more than the path before holds")
			}
			pos += n
			end := bytes.IndexByte(data[pos:], 0)
			if end < 0 {
				return nil, errors.New("an entry's path runs past the end of the file")
			}
			// A new slice, since prev is the path before.
			keep := len(prev) - drop
			name = make([]byte, 0, keep+end)
			name = append(name, prev[:keep]...)
			name = append(name, data[pos:pos+end]...)
			pos += end + 1
		} else {
			end := bytes.IndexByte(data[pos:], 0)
			if end < 0 {
				return nil, errors.New("an entry's path runs past the end of the file")
			}
			name = data[pos : pos+end]
			pos = start + (pos-start+end+8)&^7
			if pos > len(data) {
				return nil, errors.New("an entry's padding runs past the end of the file")
			}
		}
		if want := int(flags & nameMask); want != min(len(name), nameMask) {
			return nil, errors.New("an entry's path is not the length its flags give")
		}
		if len(name) > maxFileName {
			return nil, fmt.Errorf("an entry's path is %d bytes long, over the limit of %d", len(name), maxFileName)
		}
		if left -= len(name); left < 0 {
			return nil, fmt.Errorf("its paths hold more than %d bytes in all, the limit; a version 4 index repeats the path before in each entry", budget)
		}
		prev = name
		// A split index's entries that replace a shared one's have no
		// path; the link extension after them is refused below.
		if len(name) == 0 {
			unnamed = true
			continue
		}
		if !filepath.IsLocal(string(name)) {
			return nil, notLocal(string(name))
		}
		if mode&modeType != modeFile && mode&modeType != modeSymlink || extended&flagSkipWorktree != 0 {
			continue
		}
		// A file in conflict has an entry for each stage, one after another.
		// No path is empty, so the first is never last's "".
		if s := string(name); s != last {
			files = append(files, s)
			last = s
		}
	}
	if err := checkExtensions(data[pos:], hash); err != nil {
		return nil, err
	}
	if unnamed {
		return nil, errors.New("an entry has no path")
	}
	return files, nil
}

// notLocal is the refusal of an entry's path that filepath.IsLocal refused.
// On Windows that is also a path inside the repository one of whose names
// Windows keeps for a device, such as aux.yaml, which a read would open in
// place of a file, or a path inside it that holds a colon, which Windows
// reads as a drive or a stream; the message names that reason rather than
// calling the path outside. git for Windows does not write either; an
// index written on another OS can hold it.
func notLocal(name string) error {
	if (check.Location{File: name}).Inside() {
		for _, n := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
			if windowsDevice(n) {
				return fmt.Errorf("an entry's path %s holds %s, a name Windows keeps for a device, which a read would open in place of a file; git for Windows does not write it", Quote(name), Quote(n))
			}
		}
		if strings.Contains(name, ":") {
			return fmt.Errorf("an entry's path %s holds a colon, which Windows reads as a drive or a stream; git for Windows does not write it", Quote(name))
		}
	}
	return fmt.Errorf("an entry's path %s leaves the repository, which git does not write", Quote(name))
}

// windowsDevice reports whether Windows may read the name n as a device, as
// Go's filepath.IsLocal reads it there: CON, PRN, AUX, NUL, COM1 to COM9,
// LPT1 to LPT9 (with ¹, ² or ³ for the digit too), CONIN$ or CONOUT$, in any
// case, before any dot or colon and with trailing spaces dropped. Some
// Windows versions read such a name with an extension, such as aux.yaml,
// as a file; IsLocal refuses it on every version, and so does this.
func windowsDevice(n string) bool {
	if i := strings.IndexAny(n, ".:"); i >= 0 {
		n = n[:i]
	}
	// ASCII alone, as Windows compares: strings.ToUpper would read the
	// dotless ı as I.
	n = strings.Map(func(r rune) rune {
		if 'a' <= r && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}, strings.TrimRight(n, " "))
	switch n {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(n) < 4 || n[:3] != "COM" && n[:3] != "LPT" {
		return false
	}
	switch d := n[3:]; d {
	case "\u00b9", "\u00b2", "\u00b3":
		return true
	default:
		return len(d) == 1 && '1' <= d[0] && d[0] <= '9'
	}
}

// checkExtensions refuses a split index: its link extension says that most
// entries are in a shared index beside it, which errata does not read.
// Every extension is a 4 byte signature, a 4 byte size and its data; the
// object name of the whole file ends it. More than hash bytes are left in
// the loop, at least 21, so the signature and the size are there; one cut
// before the trailer leaves less than its size.
func checkExtensions(rest []byte, hash int) error {
	for len(rest) > hash {
		// The size is tested first, so a cut extension that starts with
		// link is reported as cut, not as a split index.
		size := int(binary.BigEndian.Uint32(rest[4:8]))
		if size > len(rest)-8-hash {
			return errors.New("an extension runs past the end of the file")
		}
		if string(rest[:4]) == "link" {
			return errors.New("it is a split index, whose entries are in a shared index errata does not read; run git update-index --no-split-index")
		}
		rest = rest[8+size:]
	}
	if len(rest) != hash {
		return errors.New("it does not end with the object name of its content")
	}
	return nil
}

// indexVarint reads the number at the start of b in the encoding of git's
// varint.c, and how many bytes it took, 0 for none. Each byte but the last
// has its high bit set, and each continuation adds one before it shifts, so
// no number has two spellings. errata reads at most 8 bytes, 56 bits, far
// past any path's length, so the number fits an int.
func indexVarint(b []byte) (int, int) {
	v := 0
	for i := 0; i < len(b) && i < 8; i++ {
		c := b[i]
		if i > 0 {
			v++
		}
		v = v<<7 | int(c&0x7f)
		if c&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}
