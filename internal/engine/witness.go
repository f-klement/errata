package engine

import (
	"context"
	"errors"
	"fmt"
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
// path later. On a file system that ignores case a part spelt otherwise on
// disk opens too; git's index decides whether the fixture is one a clone
// holds (core.OnFixture), on every OS alike.
func fixtureDir(root, name string) (*os.Root, error) {
	cur, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open the repository root: %w", err)
	}
	dir := check.Fixture(name)
	part := ""
	for _, seg := range strings.Split(dir, "/") {
		part = path.Join(part, seg)
		next, err := enter(cur, seg, cur.Lstat)
		_ = cur.Close()
		switch {
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
)

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
