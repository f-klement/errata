package core

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
)

// trackedFiles is the files a glob matches against at root: those
// readIndex lists, without the fixtures of witnesses (check.Witnessed),
// so a glob such as **/*.yaml never reads a planted break. On a run of a
// witness (check.RealRoot) root is the fixture, which holds no .git: the
// files are then the real root's index entries below the fixture, with the
// fixture's path cut, so a glob reads the files the fixture commits and
// not an untracked one. On a run whose fixture is held (OnFixture), they
// are the list OnFixture read, and git's index is not read again. The
// real index is read under its own bounds, and
// what is left of it is a part of it, so the bounds of a glob hold. A
// fixture the index lists no file below is refused, as a clone in CI
// holds none of its files: a glob over it would match nothing, and under
// allow_empty its witness could hold on no file read. git tracks no empty
// directory, so a fixture a clone holds always has an entry.
//
// An entry with a . or .. name or an empty one is refused on every run,
// though readIndex lists it when it stays inside the repository: git
// writes none (verify_path), and both tests read the path as written. On a
// real run .errata/./witness/w/y passes check.Witnessed and names a
// fixture's file; on a witness .errata/witness/w/a/../../../x leaves the
// fixture once its prefix is cut.
func trackedFiles(ctx context.Context, root string) ([]string, error) {
	if held, ok := ctx.Value(fixtureKey{}).(heldFixture); ok && filepath.Clean(root) == filepath.Clean(held.path) {
		return slices.Clone(held.globbed), nil
	}
	globbed, _, err := indexBelow(ctx, root)
	return globbed, err
}

// indexBelow is trackedFiles read from git's index, globbed, and on a
// witness every entry below the fixture, below, with the fixture's path
// cut: the files of a fixture nested in it included, since the fixture
// stands for a repository and a file: into a nested fixture reads it
// there. Off a witness below is nil.
func indexBelow(ctx context.Context, root string) (globbed, below []string, _ error) {
	real, witness := check.RealRoot(ctx)
	index, prefix := root, ""
	if witness {
		rel, err := filepath.Rel(real, root)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, nil, fmt.Errorf("the witness at %s is not inside the repository root", Quote(root))
		}
		index, prefix = real, filepath.ToSlash(rel)+"/"
	}
	files, err := readIndex(index)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range files {
		if path.Clean(f) != f {
			return nil, nil, fmt.Errorf("git's index lists %s, a path with a . or .. name or an empty one, which git does not write; errata does not read such an index, since the path may name a file of a witness's fixture in a form that is not its own", Quote(f))
		}
	}
	if !witness {
		return slices.DeleteFunc(files, check.Witnessed), nil, nil
	}
	globbed, below = []string{}, []string{}
	for _, f := range files {
		in, ok := strings.CutPrefix(f, prefix)
		if !ok {
			continue
		}
		below = append(below, in)
		if !check.Witnessed(in) {
			globbed = append(globbed, in)
		}
	}
	if len(below) == 0 {
		return nil, nil, fmt.Errorf("git's index lists no file below the fixture %s; a clone holds only the files git tracks, so commit the fixture's files", Quote(strings.TrimSuffix(prefix, "/")))
	}
	return globbed, below, nil
}
