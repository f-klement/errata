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
// (fixtureDir), as a file a rule cannot read is.
func (e *Engine) witness(ctx context.Context, c check.Check, name string) (out check.Result) {
	start := time.Now()
	out = check.Result{Check: c, Witness: name}
	// out is a named result so this assignment reaches the caller.
	defer func() { out.Duration = time.Since(start) }()
	root, err := filepath.Abs(e.Root)
	if err == nil {
		err = fixtureDir(root, name)
	}
	if err != nil {
		out.Verdict = check.VerdictUnknown
		out.Message = err.Error()
		return out
	}
	fixture := filepath.Join(root, filepath.FromSlash(check.Fixture(name)))
	res := e.Run(check.OnWitness(ctx, root), fixture, c)
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

// fixtureDir refuses the fixture of the witness name unless it is a
// directory of the repository at root, reached through no symlink: each of
// .errata, .errata/witness and the fixture is looked at in turn, and a
// symlink at any of them is refused wherever it leads. A symlink inside the
// repository would let the fixtures sit where the index lists them under
// another path, which a real run's globs and walks do not pass over.
func fixtureDir(root, name string) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open the repository root: %w", err)
	}
	defer r.Close()
	dir := check.Fixture(name)
	part := ""
	for _, seg := range strings.Split(dir, "/") {
		part = path.Join(part, seg)
		info, err := r.Lstat(filepath.FromSlash(part))
		switch {
		case err != nil:
			return fmt.Errorf("its fixture %s is not in the repository: %w", dir, unwrapPath(err))
		case info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("its fixture %s is reached through the symlink %s", dir, part)
		case !info.IsDir() && part == dir:
			return fmt.Errorf("its fixture %s is no directory", dir)
		case !info.IsDir():
			return fmt.Errorf("its fixture %s is not in the repository: %s is no directory", dir, part)
		}
	}
	return nil
}

// unwrapPath is err without the path an *os.PathError adds, which the
// message names already.
func unwrapPath(err error) error {
	if pe := (*os.PathError)(nil); errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
