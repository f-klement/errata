package engine

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
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// witness runs c on its witness name, the fixture check.Fixture(name)
// below the root, and returns the witness's result (ADR 0015). The runner
// is given the fixture's absolute path as its root and a context that holds
// the repository's root (check.OnWitness): a rule reads the fixture's files
// and the real index cut to them, and a program and a generator start in
// the repository's root with ERRATA_ROOT at the fixture. The check's
// verdict there decides the witness's: false holds it, true is an erratum,
// since the check could not be shown to fail, and unknown stays unknown
// with the check's message; a report names the witness beside the check
// (check.Result.Name). A fixture that is not a directory of the repository,
// or is reached through a symlink at any part of its path, is unknown
// (fixtureDir), as a file a rule cannot read is. The directory fixtureDir
// opens is the one a rule reads the fixture through (core.OnFixture), so a
// symlink swapped in after the check moves no read.
func (e *Engine) witness(ctx context.Context, c check.Check, name string) (out check.Result) {
	start := time.Now()
	out = check.Result{Check: c, Witness: name}
	// out is a named result so this assignment reaches the caller.
	defer func() { out.Duration = time.Since(start) }()
	root, err := filepath.Abs(e.Root)
	var dir *os.Root
	if err == nil {
		dir, err = fixtureDir(root, name)
	}
	if err != nil {
		out.Verdict = check.VerdictUnknown
		out.Message = err.Error()
		return out
	}
	defer dir.Close()
	fixture := filepath.Join(root, filepath.FromSlash(check.Fixture(name)))
	// Every kind of check needs the fixture in git's index, as a clone in
	// CI holds only what the index lists (core.OnFixture).
	on, err := core.OnFixture(check.OnWitness(ctx, root), fixture, dir)
	if err != nil {
		out.Verdict = check.VerdictUnknown
		out.Message = err.Error()
		return out
	}
	res := e.Run(on, fixture, c)
	switch res.Verdict {
	case check.VerdictFalse:
		out.Verdict = check.VerdictTrue
		out.Message = fmt.Sprintf("%s is false on its witness %s, as it must be", c.ID, name)
	case check.VerdictTrue:
		out.Verdict = check.VerdictFalse
		out.Message = fmt.Sprintf("%s held on its witness %s, so it could not be shown to fail", c.ID, name)
	default:
		out.Verdict = check.VerdictUnknown
		out.Message = res.Message
	}
	return out
}

// fixtureDir opens the fixture of the witness name and refuses it unless it
// is a directory of the repository at root, reached through no symlink:
// each of .errata, .errata/witness and the fixture is looked at in turn,
// and a symlink at any of them is refused wherever it leads. A symlink
// inside the repository would let the fixtures sit where the index lists
// them under another path, which a real run's globs and walks do not pass
// over. Each part is opened from the part above it (enter), so what is
// returned is the directory that was looked at, whatever is put at its
// path later.
func fixtureDir(root, name string) (*os.Root, error) {
	return openFixture(root, name, onDisk)
}

// openFixture is fixtureDir, with each part's spelling checked through
// look: onDisk in production, and in a test a stand-in for a file system
// that ignores case.
func openFixture(root, name string, look lookup) (*os.Root, error) {
	cur, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open the repository root: %w", err)
	}
	dir := check.Fixture(name)
	part := ""
	for _, seg := range strings.Split(dir, "/") {
		part = path.Join(part, seg)
		next, err := enter(cur, seg, cur.Lstat)
		var as string
		if err == nil {
			if as, err = spelling(cur, next, seg, look); err == nil && as != seg {
				err = errSpelt
			}
			if err != nil {
				_ = next.Close()
			}
		}
		_ = cur.Close()
		switch {
		case errors.Is(err, errSpelt):
			return nil, fmt.Errorf("its fixture %s is spelt %s on disk, a name that only a file system which ignores case opens as %s; rename it, since the index and every other OS name it as written", dir, path.Join(path.Dir(part), as), seg)
		case errors.Is(err, errLink):
			return nil, fmt.Errorf("its fixture %s is reached through the symlink %s", dir, part)
		case errors.Is(err, errNoDir) && part == dir:
			return nil, fmt.Errorf("its fixture %s is no directory", dir)
		case errors.Is(err, errNoDir):
			return nil, fmt.Errorf("its fixture %s is not in the repository: %s is no directory", dir, part)
		case errors.Is(err, errMoved):
			return nil, fmt.Errorf("its fixture %s changed while errata opened it: %s is not the directory errata looked at", dir, part)
		case err != nil:
			return nil, fmt.Errorf("its fixture %s is not in the repository: %w", dir, unwrapPath(err))
		}
		cur = next
	}
	return cur, nil
}

// The refusals of enter.
var (
	errLink  = errors.New("a symlink")
	errNoDir = errors.New("no directory")
	errMoved = errors.New("not the directory looked at")
	errSpelt = errors.New("spelt otherwise on disk")
)

// namer lists the names of a directory: an *os.File, or in a test a
// stand-in.
type namer interface {
	Readdirnames(n int) ([]string, error)
	Close() error
}

// listDir opens the directory r is open on for listing.
func listDir(r *os.Root) (namer, error) { return r.Open(".") }

// lookup is how spelling looks at a directory: respelt looks a name up in
// it, and list lists its names.
type lookup struct {
	respelt func(cur *os.Root, name string) (fs.FileInfo, error)
	list    func(*os.Root) (namer, error)
}

// onDisk is the lookup of the file system errata runs on.
var onDisk = lookup{
	respelt: func(cur *os.Root, name string) (fs.FileInfo, error) { return cur.Lstat(name) },
	list:    listDir,
}

// spelling is how the directory cur spells the entry a lookup of name
// finds, next being that entry opened: name itself when cur holds it as
// written, else the first entry that matches it in any case, as macOS and
// Windows look a name up. A fixture found under another spelling would run
// on those systems while the index, cut at the name as written, lists none
// of its files, so its verdict would be no fixture's. Listing costs a pass
// over cur, so it is paid only on a file system that ignores case: a
// lookup of name in upper case, such as .ERRATA, that finds next itself
// shows one. Every part is lower case (.errata, witness and a name
// check.ValidID takes), so a name equal to its upper case has no letter
// and no other spelling.
func spelling(cur, next *os.Root, name string, look lookup) (string, error) {
	other := strings.ToUpper(name)
	if other == name {
		return name, nil
	}
	info, err := look.respelt(cur, other)
	if err != nil {
		return name, nil // nothing at the other spelling: the lookup is exact
	}
	mine, err := next.Stat(".")
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, mine) {
		return name, nil // another file at the other spelling: the lookup is exact
	}
	d, err := look.list(cur)
	if err != nil {
		return "", err
	}
	defer d.Close()
	folded := ""
	for {
		names, err := d.Readdirnames(1024)
		for _, n := range names {
			if n == name {
				return n, nil
			}
			if folded == "" && strings.EqualFold(n, name) {
				folded = n
			}
		}
		if errors.Is(err, io.EOF) || (err == nil && len(names) == 0) {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if folded == "" {
		return "", fmt.Errorf("%s is not among the names its directory lists", name)
	}
	return folded, nil
}

// enter opens the directory name in cur, refusing a symlink and anything
// that is no directory, as lstat, cur.Lstat in production, gives what is
// at the name. The open follows a symlink inside cur, so a symlink swapped
// in between the lstat and the open would be followed: the directory
// opened is held to be the one lstat looked at (os.SameFile), and refused
// otherwise.
func enter(cur *os.Root, name string, lstat func(string) (fs.FileInfo, error)) (*os.Root, error) {
	info, err := lstat(name)
	switch {
	case err != nil:
		return nil, err
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, errLink
	case !info.IsDir():
		return nil, errNoDir
	}
	next, err := cur.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := next.Stat(".")
	if err == nil && !os.SameFile(info, opened) {
		err = errMoved
	}
	if err != nil {
		_ = next.Close()
		return nil, err
	}
	return next, nil
}

// unwrapPath is err without the path an *os.PathError adds, which the
// message names already.
func unwrapPath(err error) error {
	if pe := (*os.PathError)(nil); errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
