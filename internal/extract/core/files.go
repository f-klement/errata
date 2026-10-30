package core

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/f-klement/errata/internal/check"
)

// A files: key lists entries, each a file or a glob. A glob is matched
// against the files git tracks (trackedFiles), so it names the files a
// clone holds, in git's order, and never one that is not checked in. env
// and the extractors that take a file set (FileSet) read it the same way.

// MaxEntries is the most entries files: may list.
const MaxEntries = 64

// MaxMatchBytes is the most bytes of path the globs of one files: key are
// matched against in all: each glob's weight (globWeight) times the bytes
// of the paths it is matched against, which are the files git tracks under
// its literal directory, or every one for a glob whose first segment holds
// a wildcard (span). path.Match costs time in proportion to the path,
// about 33 ns a byte on a 3 core worker node, and the index may hold 64
// MiB of paths, so 64 globs over it took 167 s. The sum is known once the
// index is read, so a spec over the bound is refused before any glob is
// matched. A dozen globs with a wildcard first fit about 22 MB of paths:
// some 450000 files of 50 bytes, or 320000 of 70; a glob such as
// deploy/*.env is charged only for the paths under deploy/.
const MaxMatchBytes = 256 << 20

// MaxFiles is the most files one files: key names. Each costs an open, a
// stat and a read before its reader runs, even for an empty file, which
// max_bytes does not count, so a glob over the index's million files would
// run for minutes: 10000 empty files take 0.9 s on the build node, with
// the 4 cores its runner pod gets, and allocate 12 KB each.
const MaxFiles = 10_000

// doubleStar is the segment of a glob that matches any number of whole
// segments, none among them.
const doubleStar = "**"

// FileEntry is one entry of a files: key: the file or the glob with
// slashes, cleaned, and, for a glob, its segments and its literal
// directory (literalDir).
type FileEntry struct {
	Clean string
	Glob  bool
	segs  []string
	dir   string
}

// Files is a files: key, checked. Its limits are MaxFiles and
// MaxMatchBytes; a reader's test lowers them to witness the bounds with a
// few files.
type Files struct {
	kind       string
	Entries    []FileEntry
	FileLimit  int
	MatchLimit int
}

// CheckFiles checks the entries of a files: key of an extractor of kind:
// at least one, at most MaxEntries, each a file CheckSource takes, none
// named twice once cleaned, and each glob's segments patterns path.Match
// reads. maxBytes is the spec's, which CheckSource checks too.
func CheckFiles(kind string, files []string, maxBytes int64) (Files, error) {
	if len(files) > MaxEntries {
		return Files{}, fmt.Errorf("%s: files lists %d entries, over the limit of %d; a glob such as config/*.env names many files in one", kind, len(files), MaxEntries)
	}
	out := Files{kind: kind, FileLimit: MaxFiles, MatchLimit: MaxMatchBytes}
	listed := map[string]bool{}
	for _, f := range files {
		if _, err := CheckSource(kind, f, maxBytes); err != nil {
			return Files{}, err
		}
		e := FileEntry{Clean: path.Clean(filepath.ToSlash(f)), Glob: HasGlob(f)}
		if listed[e.Clean] {
			return Files{}, fmt.Errorf("%s: files names %s twice", kind, Quote(f))
		}
		listed[e.Clean] = true
		if e.Glob {
			e.segs = globSegments(e.Clean)
			e.dir = literalDir(e.segs)
		}
		for _, s := range e.segs {
			if _, err := path.Match(s, ""); err != nil {
				return Files{}, fmt.Errorf("%s: files entry %s is no glob: %w", kind, Quote(f), err)
			}
		}
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}

// globSegments is a cleaned glob split at each /, with a run of ** segments
// written as one, which matches what the run does.
func globSegments(glob string) []string {
	var out []string
	for _, s := range strings.Split(glob, "/") {
		if s == doubleStar && len(out) > 0 && out[len(out)-1] == doubleStar {
			continue
		}
		out = append(out, s)
	}
	return out
}

// literalDir is the leading segments of a glob that hold no *, ?, [ or \,
// joined with /: "deploy" for deploy/*.env, "" for one whose first segment
// holds one, such as */*.env or **/x. Such a segment matches only itself,
// so every path the glob matches is the directory or below it (span). A
// glob holds a segment with *, ? or [ (HasGlob), so the loop stops there.
func literalDir(segs []string) string {
	k := 0
	for k < len(segs) && !strings.ContainsAny(segs[k], `*?[\`) {
		k++
	}
	return strings.Join(segs[:k], "/")
}

// span is the paths of git's index a glob is matched against and charged
// for: its literal directory, when the index holds it as a file, which
// only a glob whose segments after it are all ** matches, and the paths
// below it, in git's order.
type span struct {
	self, under []string
}

// spanOf is the span of a glob of literal directory dir in tracked, which
// sorted says is in git's order: the paths below dir are a range of it,
// found by binary search. A glob with no literal directory, or an index
// out of git's order, which git refuses to read, spans every path.
func spanOf(dir string, tracked []string, sorted bool) span {
	if dir == "" || !sorted {
		return span{under: tracked}
	}
	var s span
	if i, found := slices.BinarySearch(tracked, dir); found {
		s.self = tracked[i : i+1]
	}
	prefix := dir + "/"
	lo, _ := slices.BinarySearch(tracked, prefix)
	hi := lo + sort.Search(len(tracked)-lo, func(j int) bool { return !strings.HasPrefix(tracked[lo+j], prefix) })
	s.under = tracked[lo:hi]
	return s
}

// bytes is the bytes of the paths s holds.
func (s span) bytes() int {
	n := 0
	for _, part := range [][]string{s.self, s.under} {
		for _, t := range part {
			n += len(t)
		}
	}
	return n
}

// matches is the paths of s the glob of e matches, in git's order.
func (e FileEntry) matches(s span) []string {
	var out []string
	for _, part := range [][]string{s.self, s.under} {
		for _, t := range part {
			if matchGlob(e.segs, t) {
				out = append(out, t)
			}
		}
	}
	return out
}

// globWeight is how many times matching a glob may read a path's bytes:
// once for a glob without **, and once for each segment that is not ** for
// one with it, since a ** retries what follows it at each later segment of
// the path and the match retries from the last ** only. That times one for
// each starBytes of its longest segment that holds a *, since path.Match
// tries what follows a * at each byte the * could end at: a segment of a *
// and 251 more bytes took 655 ns a byte of path on the build node at a
// weight of 1. A segment's length is weighed by starLen, which counts a
// character class as more bytes than it holds.
func globWeight(segs []string) int {
	stars, other, longest := 0, 0, 0
	for _, s := range segs {
		if s == doubleStar {
			stars++
			continue
		}
		other++
		if strings.Contains(s, "*") {
			longest = max(longest, starLen(s))
		}
	}
	weight := 1
	if stars > 0 {
		weight = max(other, 1)
	}
	return weight * max(1, (longest+starBytes-1)/starBytes)
}

// starBytes is the bytes of a segment that holds a * that weigh as one
// more pass over the path.
const starBytes = 8

// classFactor is how many bytes each byte of a character class, its
// brackets included, counts as in starLen. path.Match reads a class again
// at each byte it tests, rune by rune and range by range, so a class costs
// more for each of its bytes than a literal: twenty classes of 13 bytes
// after a * took 72 ns a weighed byte at a factor of 1, over the 33 the
// bound assumes, and take 21 ns at 3.
const classFactor = 3

// starLen is the length of seg for its weight: its bytes, with each byte of
// a character class counted classFactor times. A byte after a \ is a
// literal, so \[ starts no class. The segment was checked when the glob
// was built, so every class ends.
func starLen(seg string) int {
	n, class := 0, false
	for i := 0; i < len(seg); i++ {
		c, w := seg[i], 1
		if class || c == '[' {
			w = classFactor
		}
		switch {
		case c == '\\' && i+1 < len(seg):
			i++
			w *= 2
		case c == '[' && !class:
			class = true
		case c == ']' && class:
			class = false
		}
		n += w
	}
	return n
}

// matchGlob reports whether name, a path with slashes as git's index writes
// it, matches the glob of segs: a segment ** matches zero or more whole
// segments, and any other segment matches one segment as path.Match does,
// so * never crosses a /. It retries from the last ** it passed, which is
// enough: a later ** can match whatever an earlier one would have.
func matchGlob(segs []string, name string) bool {
	p := 0                   // the next segment of the glob
	rest, more := name, true // the part of name still to match, and whether it holds a segment
	star := -1               // the last ** passed
	var starRest string      // rest when it was passed
	for more {
		// Every turn either moves p on while rest stays, cuts a segment
		// off rest, or cuts one off starRest, so the loop ends.
		seg, after, found := strings.Cut(rest, "/")
		if p < len(segs) && segs[p] == doubleStar {
			star, starRest = p, rest
			p++
			continue
		}
		if p < len(segs) {
			// The segments were checked when the glob was built, so Match
			// returns no error here.
			if ok, _ := path.Match(segs[p], seg); ok {
				p++
				rest, more = after, found
				continue
			}
		}
		if star < 0 {
			return false
		}
		// The ** takes one more segment of name, and the glob after it
		// starts again from the segment after that.
		_, starRest, more = strings.Cut(starRest, "/")
		p, rest = star+1, starRest
	}
	for p < len(segs) && segs[p] == doubleStar {
		p++
	}
	return p == len(segs)
}

// Named is one file a files: key names, and the indexes in Entries of the
// entries that name it, in their order.
type Named struct {
	File string
	By   []int
}

// List is the files the entries name, each once, in the entries' order: a
// file, which need not exist until it is read, or the files git tracks that
// a glob matches, in git's order, but those of witnesses (trackedFiles),
// each glob matched only against its span. The index is read once, and only
// when an entry is a glob. A glob that matches no file names none. A glob
// that matches a path holding a control character, a direction control or a
// line or paragraph separator is refused, so no reader opens the file or
// names it in an error (check.HoldsControl); a file: entry was checked at
// load (CheckSource).
func (f Files) List(ctx context.Context, root string) ([]Named, error) {
	var spans []span         // each entry's, nil until the index is read
	seen := map[string]int{} // each file's index in out
	var out []Named
	for k, e := range f.Entries {
		files := []string{e.Clean}
		if e.Glob {
			if spans == nil {
				tracked, err := trackedFiles(ctx, root)
				if err != nil {
					return nil, fmt.Errorf("%s: files lists the glob %s: %w", f.kind, Quote(e.Clean), err)
				}
				spans = f.spans(tracked)
				if err := f.checkMatch(spans); err != nil {
					return nil, err
				}
			}
			files = e.matches(spans[k])
			for _, t := range files {
				if check.HoldsControl(t) {
					return nil, fmt.Errorf("%s: files lists the glob %s, which matches %s, a tracked path that holds a control character, a direction control or a line or paragraph separator, which a terminal or a report viewer would act on; rename the file", f.kind, Quote(e.Clean), Quote(t))
				}
			}
		}
		for _, file := range files {
			i, ok := seen[file]
			if ok {
				out[i].By = append(out[i].By, k)
				continue
			}
			if len(out) == f.FileLimit {
				return nil, fmt.Errorf("%s: files names more than %d files, the limit, at %s; narrow the glob", f.kind, MaxFiles, Quote(e.Clean))
			}
			seen[file] = len(out)
			out = append(out, Named{File: file, By: []int{k}})
		}
	}
	return out, nil
}

// spans is the span of each entry that is a glob in tracked, by the
// entry's index; an entry that is a file has none. The index is tested for
// git's order once, and only when a glob has a literal directory.
func (f Files) spans(tracked []string) []span {
	sorted := false
	if slices.ContainsFunc(f.Entries, func(e FileEntry) bool { return e.dir != "" }) {
		sorted = slices.IsSorted(tracked)
	}
	out := make([]span, len(f.Entries))
	for k, e := range f.Entries {
		if e.Glob {
			out[k] = spanOf(e.dir, tracked, sorted)
		}
	}
	return out
}

// checkMatch refuses entries whose globs, each matched against its span,
// would read more than MatchLimit bytes of path, before the first is
// matched: the bytes are each glob's weight times its span's bytes.
func (f Files) checkMatch(spans []span) error {
	globs, read := 0, 0
	for k, e := range f.Entries {
		if e.Glob {
			globs++
			read += globWeight(e.segs) * spans[k].bytes()
		}
	}
	if read > f.MatchLimit {
		return fmt.Errorf("%s: files lists %d globs, and matching each against the paths git tracks, below its directory when it starts with one, reads %d bytes of path, more than %d, the limit; list fewer globs, or start each with its directory", f.kind, globs, read, MaxMatchBytes)
	}
	return nil
}

// Clean is files with each entry cleaned as CheckFiles cleans it, for a
// spec's place.
func Clean(files []string) []string {
	var out []string
	for _, f := range files {
		out = append(out, path.Clean(filepath.ToSlash(f)))
	}
	return out
}
