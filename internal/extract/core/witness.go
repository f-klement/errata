package core

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core/gitindex"
)

// indexLimits are the bounds of a read of git's index. The file may be as
// large as the ceiling of max_bytes: an entry is about 70 bytes and its
// path, so it holds some 600000 files, and the Linux kernel's index, of
// 90000 files, is 10 MB. It lists at most as many entries as a walk
// visits, and each path is at most as long as a file an extractor may
// name.
var indexLimits = gitindex.Limits{Bytes: MaxMaxBytes, Entries: DefaultWalk.Entries, Name: maxFileName}

// readIndex is git's index at the repository top top (gitindex.Read).
func readIndex(top string) (gitindex.Index, error) { return gitindex.Read(top, indexLimits) }

// rootIndex is git's index as a run at root reads it (ADR 0018): the index
// of the repository that holds root, found as git finds it (repositoryOf),
// and when root sits below that repository's top, only its entries below
// root, with root's path cut from each (gitindex.Index.Within). Every path
// it lists is relative to root, so nothing above root is matched or
// named. A root that no repository holds is refused
// (gitindex.ErrNoRepository).
func rootIndex(root string) (gitindex.Index, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return gitindex.Index{}, fmt.Errorf("open the repository root: %w", bare(err))
	}
	top, below := repositoryOf(real)
	ix, err := readIndex(top)
	if err != nil || below == "" {
		return ix, err
	}
	return ix.Within(below)
}

// HeadTags is the names of the tags on HEAD that start with prefix, in
// the repository that holds root, found as git finds it (repositoryOf),
// read from git's files under the bounds of its index
// (gitindex.HeadTags). On a witness's run it is the real root's
// repository, as a program of the check starts there: a fixture holds
// files, and no .git.
func HeadTags(ctx context.Context, root, prefix string) ([]string, error) {
	if real, ok := check.RealRoot(ctx); ok {
		root = real
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("open the repository root: %w", bare(err))
	}
	top, _ := repositoryOf(real)
	return gitindex.HeadTags(ctx, top, prefix, indexLimits)
}

// fixtureIndex is the index of the fixture at rel, a path inside the
// repository root real, on a run of a witness: the real root's index below
// it (rootIndex, gitindex.Index.Below).
func fixtureIndex(real, rel string) (gitindex.Index, error) {
	ix, err := rootIndex(real)
	if err != nil {
		return gitindex.Index{}, err
	}
	return ix.Below(filepath.ToSlash(rel))
}
