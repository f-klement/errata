package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// FileReader is the extractor of one file that also returns the values in
// the file's bytes, as the yaml, json, toml and regex extractors do once
// they have read it. FromShared does it with the work the files of a set
// read before it have spent, and adds its own, so ADR 0003's budgets hold
// for the set as one extractor.
type FileReader interface {
	check.Extractor
	FromShared(data []byte, spent *Spent) ([]check.Value, error)
}

// Spent is the work of a budget that the readers of a set share, carried
// from one file to the next as the bytes are: the nodes YAML aliases and
// merge keys added (MaxExpanded), the bytes a path's filters hashed and
// compared (MaxFilterBytes), what the documents embedded steps parsed
// out of strings hold, and the time of the regex searches.
type Spent struct {
	Expanded int
	Filter   int64
	// deadline is when the regex searches of every file of a set must
	// end, timeLimit from when the set started reading, which a refusal
	// on time names; zero outside a set, where a search takes its own
	// time limit from when it starts.
	deadline  time.Time
	timeLimit time.Duration
	// stringBytes and stringNodes are the bytes of the documents in
	// strings and the nodes they can hold, each counted at its format's
	// bytes per node. One file's strings count against its max_bytes as
	// one more file, and the strings of every file of a set together
	// against the set's, stringLimit, which is 0 outside a set. The
	// values a path selects in a file of a set are held to stringLimit
	// too, as FileSet holds the values of all the files.
	stringBytes, stringNodes int64
	stringLimit              int64
}

// SharedAliases is err with the note that the files of a set share the
// budget on the nodes aliases and merge keys add, when s is a set's and an
// alias in a document a string holds spent the budget: a refusal of a few
// aliases in a later file names the limit the earlier files used up. Any
// other err is returned as it is.
func (s *Spent) SharedAliases(err error) error {
	if err != nil && s.stringLimit != 0 && s.Expanded > MaxExpanded {
		return fmt.Errorf("%w; the files of a set share the limits on aliases and merge keys", err)
	}
	return err
}

// BuildOne builds the reader of one file from its Source, which core has
// checked as CheckSource checks it: the extractor a spec builds with that
// file in file:. inSet is true for a file of a set, where a reader that
// would refuse a file for holding nothing it selects returns no value for
// it: a yaml file with no document of the kind.
type BuildOne func(src Source, inSet bool) (FileReader, error)

// BuildOneOrSet builds the extractor of a spec of kind that reads where w
// says: build's for its file, or a FileSet over its files that reads each
// with build's. A spec that sets both is refused.
func BuildOneOrSet(kind string, w Where, build BuildOne) (check.Extractor, error) {
	if w.Files == nil {
		src, err := CheckSource(kind, w.File, w.MaxBytes)
		if err != nil {
			return nil, err
		}
		e, err := build(src, false)
		if err != nil {
			return nil, err
		}
		return e, nil
	}
	if w.File != "" {
		return nil, fmt.Errorf("%s: file and files are both set; write file for one file, or files for the files a list or a glob names", kind)
	}
	return BuildFileSet(kind, w.Files, w.MaxBytes, build)
}

// FileSet is an extractor that reads every file a files: key names, each
// with the reader BuildOne builds, and returns their values in the files'
// order, each with its own file as its location. The files hold at most
// max_bytes in all, each read to what the files before it left, and their
// values are held to MaxValues and to max_bytes in all, as one file's are,
// a mapping handed over whole counting each of its nodes and the bytes of
// its keys and scalars.
// A regex search of every file shares one time limit, the one max_bytes
// gives one file.
type FileSet struct {
	Files
	limit int64
	build BuildOne
	// timeLimit is the time the regex searches of every file share,
	// regexTime of max_bytes: a test shortens it.
	timeLimit time.Duration
}

// BuildFileSet checks files and max_bytes, and builds the reader of the
// first entry, so a path or a pattern that does not load is refused now,
// not on each run.
func BuildFileSet(kind string, files []string, maxBytes int64, build BuildOne) (FileSet, error) {
	if len(files) == 0 {
		return FileSet{}, fmt.Errorf("%s: files lists no entry; name a file or a glob, or write file: for one file", kind)
	}
	f, err := CheckFiles(kind, files, maxBytes)
	if err != nil {
		return FileSet{}, err
	}
	limit := MaxBytes(maxBytes)
	s := FileSet{Files: f, limit: limit, build: build, timeLimit: regexTime(limit)}
	if _, err := s.one(f.Entries[0].Clean, limit); err != nil {
		return FileSet{}, err
	}
	return s, nil
}

// one is the reader of file, a file of the set, which reads at most
// maxBytes of it.
func (s FileSet) one(file string, maxBytes int64) (FileReader, error) {
	src, err := CheckSource(s.kind, file, maxBytes)
	if err != nil {
		return nil, err
	}
	return s.build(src, true)
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (s FileSet) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return s.Extract(ctx, root)
}

// Extract reads every file the entries name and returns their values.
func (s FileSet) Extract(ctx context.Context, root string) ([]check.Value, error) {
	named, err := s.List(ctx, root)
	if err != nil {
		return nil, err
	}
	out := []check.Value{}
	left := s.limit // the bytes the files still to read may hold
	var held int64  // the bytes of the values so far
	count := 0      // the values so far, each node of a mapping one
	// The budgets the files so far spent.
	spent := Spent{stringLimit: s.limit, deadline: time.Now().Add(s.timeLimit), timeLimit: s.timeLimit}
	read := map[string]bool{} // where each file read so far resolves
	for _, n := range named {
		// Two names that resolve to one file, a link and its target, are
		// one file, read at the first; a name that does not resolve is
		// refused by the read, with the reason.
		if at, err := (Source{File: n.File}).Resolved(root); err == nil {
			if read[at] {
				continue
			}
			read[at] = true
		}
		data, err := Source{File: n.File, Limit: left}.Read(root)
		if errors.Is(err, ErrTooLarge) {
			// The limit the message names is what the files before left,
			// or max_bytes for the first.
			return nil, fmt.Errorf("%s: %w; the files the extractor reads hold at most max_bytes, %d, in all", s.kind, err, s.limit)
		}
		if err != nil {
			return nil, err
		}
		// At least 1, since a reader's 0 is the default of 32 MiB.
		r, err := s.one(n.File, max(left, 1))
		if err != nil {
			return nil, err
		}
		vs, err := r.FromShared(data, &spent)
		if errors.Is(err, ErrTooDense) {
			return nil, fmt.Errorf("%s: %w; in a set, max_bytes is what the files before it left, %d of %d", s.kind, err, left, s.limit)
		}
		if errors.Is(err, ErrFilterWork) {
			return nil, fmt.Errorf("%s: %w; the files of a set share the limit", s.kind, err)
		}
		if err != nil {
			return nil, err
		}
		left -= int64(len(data))
		for _, v := range vs {
			nodes, bytes := v.Size()
			if count += nodes; count > MaxValues {
				return nil, fmt.Errorf("%s: the files hold more than %d values, the limit, each node of a mapping one; narrow the path, the pattern or the files", s.kind, MaxValues)
			}
			if held += bytes; held > s.limit {
				return nil, fmt.Errorf("%s: the values of the files hold more than %d bytes in all, the extractor's max_bytes; set max_bytes: higher", s.kind, s.limit)
			}
			out = append(out, v)
		}
	}
	return out, nil
}
