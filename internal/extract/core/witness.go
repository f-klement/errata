package core

import (
	"context"
	"fmt"
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
// not an untracked one. The real index is read under its own bounds, and
// what is left of it is a part of it, so the bounds of a glob hold.
func trackedFiles(ctx context.Context, root string) ([]string, error) {
	real, witness := check.RealRoot(ctx)
	if !witness {
		files, err := readIndex(root)
		return slices.DeleteFunc(files, check.Witnessed), err
	}
	rel, err := filepath.Rel(real, root)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("the witness at %s is not inside the repository root", Quote(root))
	}
	files, err := readIndex(real)
	if err != nil {
		return nil, err
	}
	prefix := filepath.ToSlash(rel) + "/"
	cut := []string{}
	for _, f := range files {
		if in, ok := strings.CutPrefix(f, prefix); ok && !check.Witnessed(in) {
			cut = append(cut, in)
		}
	}
	return cut, nil
}
