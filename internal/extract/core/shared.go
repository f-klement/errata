package core

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"

	"github.com/f-klement/errata/internal/check"
)

// MaxShared is the most bytes of files one run keeps, under one root, to
// hand to every extractor that reads them (sharedFiles): the most one
// extractor may read, so a run keeps no more than one extractor could
// hold. A file read once the run keeps this much is read again by each
// extractor that names it, as every file was before the run kept any.
const MaxShared = MaxMaxBytes

// sharedFiles is what the extractors of one run read under one root, each
// file's bytes, or the refusal of its read, by the name a spec gave it, so
// a file is read once however many extractors name it. Each extractor still
// holds the file to its own limit (kept.within), so a rule sees the bytes,
// or the refusal, its own read would give. What it keeps holds for one
// generation of the run (check.Generation): once a check may have written
// to the repository, it keeps nothing of before. Safe for concurrent use:
// a read that finds another read of the same file under way waits for it
// rather than read the file again.
type sharedFiles struct {
	mu    sync.Mutex
	gen   uint64                // the generation kept and found hold for
	kept  map[string]*entry     // each file's read, under way or done
	found map[string]resolution // where each name resolves (Reader.Resolved)
	bytes int64                 // the bytes of the files kept, at most limit
	limit int64
}

// resolution is where a name resolves, or the refusal of the lookup.
type resolution struct {
	rel string
	err error
}

// entry is one read of a file: done is closed once k holds what it read,
// and none is true when it kept nothing for the readers after it, as for a
// file over the reading extractor's limit.
type entry struct {
	done chan struct{}
	k    kept
	none bool
}

// kept is one file's read: its bytes and their count, or the refusal of
// the read, which names the file by the name it is kept under.
type kept struct {
	data []byte
	size int64
	err  error
}

// keptOf is what a read of data, from a file of size bytes when it was
// opened, keeps: a file that grew while it was read counts at the bytes
// read, so a later read with a smaller limit refuses it as its own open
// would.
func keptOf(data []byte, size int64) kept {
	return kept{data: data, size: max(size, int64(len(data)))}
}

// sharedKey is the run's key of the files it keeps under one root.
type sharedKey struct{ root string }

// sharedAt is the files the run in ctx keeps under root, emptied when the
// run's generation moved on since, or nil outside a run, where nothing is
// kept from one read to the next.
func sharedAt(ctx context.Context, root string) *sharedFiles {
	gen, ok := check.Generation(ctx)
	if !ok {
		return nil
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	f, _ := check.Shared(ctx, sharedKey{root}, func() (*sharedFiles, error) {
		return &sharedFiles{gen: gen, kept: map[string]*entry{}, found: map[string]resolution{}, limit: MaxShared}, nil
	})
	f.at(gen)
	return f
}

// at empties f when gen is a later generation than the one it holds. A
// reader that took an earlier generation before the run moved on empties
// nothing, so it cannot drop what the later generation kept.
func (f *sharedFiles) at(gen uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if gen > f.gen {
		f.gen, f.kept, f.found, f.bytes = gen, map[string]*entry{}, map[string]resolution{}, 0
	}
}

// read is the bytes of the file s names, held to s.Limit: those the run
// kept, or a read through t that the run keeps for the next extractor when
// they fit. A read of the file under way is waited for. The bytes are the
// caller's own copy, so no extractor sees what another did to them.
func (f *sharedFiles) read(s Source, t tree) ([]byte, error) {
	f.mu.Lock()
	e, ok := f.kept[s.File]
	if !ok {
		e = &entry{done: make(chan struct{})}
		f.kept[s.File] = e
	}
	gen := f.gen
	f.mu.Unlock()
	if !ok {
		return f.fill(e, s, t, gen)
	}
	<-e.done
	if e.none {
		return s.readIn(t)
	}
	return e.k.within(s)
}

// fill reads the file s names into e, the run's entry for it at
// generation gen, and returns the read. A refusal on size is not kept: it
// names the limit of the read that met it, and a later read may allow
// more. Bytes past the run's limit are not kept either; the readers that
// waited still get them.
func (f *sharedFiles) fill(e *entry, s Source, t tree, gen uint64) ([]byte, error) {
	defer close(e.done)
	data, size, err := s.readSized(t)
	switch {
	case errors.Is(err, ErrTooLarge):
		e.none = true
		f.forget(s.File, e)
		return nil, err
	case err != nil:
		e.k = kept{err: err}
		return nil, err
	}
	e.k = keptOf(data, size)
	f.count(s.File, e, gen)
	return e.k.within(s)
}

// count adds e's bytes to what the run keeps, or drops e when they would
// take the run past its limit, or when the run moved to a later generation
// while e was read.
func (f *sharedFiles) count(file string, e *entry, gen uint64) {
	n := int64(len(e.k.data))
	f.mu.Lock()
	defer f.mu.Unlock()
	if gen == f.gen && f.bytes+n <= f.limit {
		f.bytes += n
		return
	}
	f.remove(file, e)
}

// forget removes e, the entry of file, when the run still holds it.
func (f *sharedFiles) forget(file string, e *entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remove(file, e)
}

// remove is forget, with f.mu held.
func (f *sharedFiles) remove(file string, e *entry) {
	if f.kept[file] == e {
		delete(f.kept, file)
	}
}

// within is the kept read held to s.Limit, as a read of s would hold it: a
// refusal as it was, a file over the limit refused by its size as the open
// refuses it, else a copy of the bytes.
func (k kept) within(s Source) ([]byte, error) {
	if k.err != nil {
		return nil, k.err
	}
	if k.size > s.Limit {
		return nil, tooLarge(s.File, k.size, s.Limit)
	}
	return bytes.Clone(k.data), nil
}

// resolved is where the file s names resolves under t, or the refusal, as
// the run found it the first time any extractor asked in this generation.
// Two lookups at once both look; the first to finish is the one kept.
func (f *sharedFiles) resolved(s Source, t tree) (string, error) {
	f.mu.Lock()
	at, ok := f.found[s.File]
	f.mu.Unlock()
	if ok {
		return at.rel, at.err
	}
	at.rel, at.err = s.resolvedIn(t)
	f.mu.Lock()
	defer f.mu.Unlock()
	if first, ok := f.found[s.File]; ok {
		return first.rel, first.err
	}
	f.found[s.File] = at
	return at.rel, at.err
}
