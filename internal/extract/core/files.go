package core

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/f-klement/errata/internal/check"
)

// A files: key lists entries, each a file or a glob. A glob is matched
// against the files git tracks (TrackedFiles), so it names the files a
// clone holds, in git's order, and never one that is not checked in. env
// and the extractors that take a file set (FileSet) read it the same way.

// MaxEntries is the most entries files: may list.
const MaxEntries = 64

// MaxMatchBytes is the most bytes of path the globs of one files: key are
// matched against in all: each glob against every file git tracks, so the
// sum of the globs' weights (globWeight) times the bytes of the index's
// paths. path.Match costs time in proportion to the path, about 33 ns a
// byte on a 3 core worker node, and the index may hold 64 MiB of paths, so
// 64 globs over it took 167 s. The product is known once the index is
// read, so a spec over the bound is refused before any glob is matched. A
// dozen globs fit about 22 MB of paths: some 450000 files of 50 bytes, or
// 320000 of 70.
const MaxMatchBytes = 256 << 20

// MaxFiles is the most files one files: key names. Each costs an open, a
// stat and a read before its reader runs, even for an empty file, which
// max_bytes does not count, so a glob over the index's million files would
// run for minutes: 10000 empty files take 1.4 s on the 6 core build node
// and 3.7 s on a 3 core worker node.
const MaxFiles = 10_000

// doubleStar is the segment of a glob that matches any number of whole
// segments, none among them.
const doubleStar = "**"

// FileEntry is one entry of a files: key: the file or the glob with
// slashes, cleaned, and, for a glob, its segments.
type FileEntry struct {
	Clean string
	Glob  bool
	segs  []string
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
// a glob matches, in git's order. The index is read once, and only when an
// entry is a glob. A glob that matches no file names none. A glob that
// matches a path holding a control character is refused, so no reader opens
// the file or names it in an error (check.HoldsControl); a file: entry was
// checked at load (CheckSource).
func (f Files) List(root string) ([]Named, error) {
	var tracked []string     // nil until read; TrackedFiles never returns nil
	seen := map[string]int{} // each file's index in out
	var out []Named
	for k, e := range f.Entries {
		files := []string{e.Clean}
		if e.Glob {
			if tracked == nil {
				var err error
				if tracked, err = TrackedFiles(root); err != nil {
					return nil, fmt.Errorf("%s: files lists the glob %s: %w", f.kind, Quote(e.Clean), err)
				}
				if err := f.checkMatch(tracked); err != nil {
					return nil, err
				}
			}
			files = nil
			for _, t := range tracked {
				if !matchGlob(e.segs, t) {
					continue
				}
				if check.HoldsControl(t) {
					return nil, fmt.Errorf("%s: files lists the glob %s, which matches %s, a tracked path that holds a control character, which a message would print as it is; rename the file", f.kind, Quote(e.Clean), Quote(t))
				}
				files = append(files, t)
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

// checkMatch refuses entries whose globs, each matched against every
// tracked path, would read more than MatchLimit bytes of path, before the
// first is matched: the bytes are the globs' weights times the paths' bytes.
func (f Files) checkMatch(tracked []string) error {
	globs, weight, paths := 0, 0, 0
	for _, e := range f.Entries {
		if e.Glob {
			globs++
			weight += globWeight(e.segs)
		}
	}
	for _, t := range tracked {
		paths += len(t)
	}
	if weight*paths > f.MatchLimit {
		return fmt.Errorf("%s: files lists %d globs, and matching each against the %d bytes of path git tracks takes more than %d bytes, the limit; list fewer globs", f.kind, globs, paths, MaxMatchBytes)
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
