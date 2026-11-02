package core

import (
	"path/filepath"

	"github.com/f-klement/errata/internal/extract/core/gitindex"
)

// indexLimits are the bounds of a read of git's index. The file may be as
// large as the ceiling of max_bytes: an entry is about 70 bytes and its
// path, so it holds some 600000 files, and the Linux kernel's index, of
// 90000 files, is 10 MB. It lists at most as many entries as a walk
// visits, and each path is at most as long as a file an extractor may
// name.
var indexLimits = gitindex.Limits{Bytes: MaxMaxBytes, Entries: DefaultWalk.Entries, Name: maxFileName}

// readIndex is git's index at the repository root root (gitindex.Read).
func readIndex(root string) (gitindex.Index, error) { return gitindex.Read(root, indexLimits) }

// fixtureIndex is the index of the fixture at rel, a path inside the
// repository root real, on a run of a witness: the real root's index below
// it (gitindex.Index.Below).
func fixtureIndex(real, rel string) (gitindex.Index, error) {
	ix, err := readIndex(real)
	if err != nil {
		return gitindex.Index{}, err
	}
	return ix.Below(filepath.ToSlash(rel))
}
