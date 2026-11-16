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
	r := setRead{
		s: s, ctx: ctx, root: root,
		out:   []check.Value{},
		left:  s.limit,
		spent: Spent{stringLimit: s.limit, deadline: time.Now().Add(s.timeLimit), timeLimit: s.timeLimit},
		read:  map[string]bool{},
	}
	defer r.close()
	for _, n := range named {
		if err := r.file(n.File); err != nil {
			return nil, err
		}
	}
	return r.out, nil
}

// setRead is an Extract of a set under way: the values so far and the
// budgets the files so far spent.
type setRead struct {
	s     FileSet
	ctx   context.Context
	root  string
	out   []check.Value
	left  int64           // the bytes the files still to read may hold
	held  int64           // the bytes of the values so far
	count int             // the values so far, each node of a mapping one
	spent Spent           // the budgets the files so far spent
	read  map[string]bool // where each file read so far resolves
	files *Reader         // opened at the first file, for them all
}

// close closes the reader of the files, if one was opened.
func (r *setRead) close() {
	if r.files != nil {
		r.files.Close()
	}
}

// file reads the file of the set named file and adds its values.
func (r *setRead) file(file string) error {
	if r.files == nil {
		var err error
		if r.files, err = OpenReader(r.ctx, r.root); err != nil {
			return err
		}
	}
	if r.seen(file) {
		return nil
	}
	data, err := r.files.Read(Source{File: file, Limit: r.left})
	if errors.Is(err, ErrTooLarge) {
		// The limit the message names is what the files before left,
		// or max_bytes for the first.
		return fmt.Errorf("%s: %w; the files the extractor reads hold at most max_bytes, %d, in all", r.s.kind, err, r.s.limit)
	}
	if err != nil {
		return err
	}
	vs, err := r.values(file, data)
	if err != nil {
		return err
	}
	r.left -= int64(len(data))
	return r.keep(vs)
}

// seen reports whether file resolves to a file read before. Two names that
// resolve to one file, a link and its target, are one file, read at the
// first; a name that does not resolve is refused by the read, with the
// reason.
func (r *setRead) seen(file string) bool {
	if at, err := r.files.Resolved(Source{File: file}); err == nil {
		if r.read[at] {
			return true
		}
		r.read[at] = true
	}
	return false
}

// values are the values the reader of file reads from its data, within
// the budgets the files before it left.
func (r *setRead) values(file string, data []byte) ([]check.Value, error) {
	// At least 1, since a reader's 0 is the default of 32 MiB.
	one, err := r.s.one(file, max(r.left, 1))
	if err != nil {
		return nil, err
	}
	vs, err := one.FromShared(data, &r.spent)
	if errors.Is(err, ErrTooDense) {
		return nil, fmt.Errorf("%s: %w; in a set, max_bytes is what the files before it left, %d of %d", r.s.kind, err, r.left, r.s.limit)
	}
	if errors.Is(err, ErrFilterWork) {
		return nil, fmt.Errorf("%s: %w; the files of a set share the limit", r.s.kind, err)
	}
	return vs, err
}

// keep adds vs to the values, and refuses values that pass MaxValues nodes
// or max_bytes in all.
func (r *setRead) keep(vs []check.Value) error {
	for _, v := range vs {
		nodes, bytes := v.Size()
		if r.count += nodes; r.count > MaxValues {
			return fmt.Errorf("%s: the files hold more than %d values, the limit, each node of a mapping one; narrow the path, the pattern or the files", r.s.kind, MaxValues)
		}
		if r.held += bytes; r.held > r.s.limit {
			return fmt.Errorf("%s: the values of the files hold more than %d bytes in all, the extractor's max_bytes; set max_bytes: higher", r.s.kind, r.s.limit)
		}
		r.out = append(r.out, v)
	}
	return nil
}
