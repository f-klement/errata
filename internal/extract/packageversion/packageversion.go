// Package packageversion is the package_version extractor: the version a
// package gives itself in each of its manifests, such as version in
// package.json, project.version in pyproject.toml, package.version in
// Cargo.toml and a VERSION file. Each file is read by its name, as the table
// in manifests.go says, and every version comes back as the file wrote it,
// with the file and the line. A version the build computes, such as
// dynamic = ["version"] in pyproject.toml or version.workspace = true in a
// Cargo.toml that does not hold the workspace's version, is not literal.
package packageversion

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns the version of the package in each of its manifests. Without
// Files, it reads each file of the table that the repository root holds
// under the table's name, spelled in the same case, and passes over one that
// is not there or is a directory; with Files, it reads exactly those, at
// most maxFiles, each of which must exist.
type Spec struct {
	Files    []string `json:"files,omitempty"`
	MaxBytes int64    `json:"max_bytes,omitempty"`
}

// Kind is the package_version kind, which a rule writes under the key
// package_version.
var Kind = core.Kind[Spec]{Key: "package_version", Place: Place, Resolve: Resolve, Describe: Describe}

// Resolve is s with its max_bytes written out and its files copied, for
// errata list.
func Resolve(s Spec) Spec {
	s.Files = slices.Clone(s.Files)
	s.MaxBytes = core.MaxBytes(s.MaxBytes)
	return s
}

// Describe is what the kind's extractor returns: flat values, some not
// literal, since a version the build computes names no version until the
// build runs.
func Describe(Spec) check.Description {
	return check.Description{Label: "package_version", NotLiteral: true}
}

// Parsers are the JSON and TOML readers the extractor reads package.json,
// pyproject.toml and Cargo.toml with, as core.Parsers says.
type Parsers = core.Parsers

// maxFiles is the most files files: may name. A file gives at most two
// values, so the extractor stays under core.MaxValues by construction; a
// package has a manifest or two of each kind, and env takes as many.
const maxFiles = 64

// Build checks s and returns its extractor.
func Build(s Spec, p Parsers) (check.Extractor, error) {
	if len(s.Files) > maxFiles {
		return nil, fmt.Errorf("package_version: files names %d files, over the limit of %d; a rule over more packages names several extractors", len(s.Files), maxFiles)
	}
	x := extractor{parsers: p, explicit: len(s.Files) > 0}
	names := s.Files
	if !x.explicit {
		// The root's manifests, by the table's names; max_bytes is checked
		// on the first, and the names are the table's, so none is refused.
		names = nil
		for _, k := range kinds {
			names = append(names, k.name)
		}
	}
	seen := map[string]bool{}
	for _, name := range names {
		src, err := core.CheckSource("package_version", name, s.MaxBytes)
		if err != nil {
			return nil, err
		}
		clean := filepath.Clean(name)
		if seen[clean] {
			return nil, fmt.Errorf("package_version: files names %s twice", core.Quote(name))
		}
		seen[clean] = true
		_, base := core.FileParts(name)
		k := kindOf(base)
		if k == nil {
			return nil, fmt.Errorf("package_version: errata does not know how %s names a package's version; it reads %s", core.Quote(name), knownNames())
		}
		x.files = append(x.files, file{Source: src, kind: k})
	}
	return x, nil
}

// Place is s with only what decides where it reads: each file cleaned, the
// files in order, since the same files in another order read the same
// versions, and max_bytes left out.
func Place(s Spec) Spec {
	var p Spec
	for _, f := range s.Files {
		p.Files = append(p.Files, filepath.Clean(f))
	}
	slices.Sort(p.Files)
	return p
}

// file is one file the extractor reads, and how.
type file struct {
	core.Source
	kind *fileKind
}

// extractor is a built package_version extractor.
type extractor struct {
	files    []file
	explicit bool // files: named them, so each must exist
	parsers  Parsers
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (x extractor) ExtractWith(root string, _ map[string][]check.Value) ([]check.Value, error) {
	return x.Extract(root)
}

// Extract reads each file under root and returns its versions, the files in
// order and each file's in file order. Without files:, a file of the table
// that the root does not hold under that exact name, a symlink there that
// dangles inside the repository, and a directory there are passed over. A
// file that files: names must exist. The files hold at most max_bytes in
// all, as one file does.
func (x extractor) Extract(root string) ([]check.Value, error) {
	out := []check.Value{}
	var present map[string]bool
	if !x.explicit {
		var err error
		if present, err = rootNames(root); err != nil {
			return nil, err
		}
	}
	var read int64
	for _, f := range x.files {
		if !x.explicit && !present[f.File] {
			continue
		}
		data, err := f.Read(root)
		if !x.explicit && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, core.ErrDirectory)) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if read += int64(len(data)); read > f.Limit {
			return nil, fmt.Errorf("%s: the files the extractor reads hold more than %d bytes in all, the extractor's max_bytes; name fewer files in files, or set max_bytes: higher", f.File, f.Limit)
		}
		got, err := x.FromData(f, data)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

// rootNames are the names of the table that the directory root holds,
// spelled as the table spells them. A file system that folds case, as macOS
// and Windows ship theirs, opens a version directory or file by the name
// VERSION; the names the directory lists are as they were written, so only
// the table's own spelling is read.
func rootNames(root string) (map[string]bool, error) {
	d, err := os.Open(root)
	if err != nil {
		return nil, fmt.Errorf("open the repository root: %w", bare(err))
	}
	defer d.Close()
	present := map[string]bool{}
	for {
		// In batches, so a root of many entries keeps only the names it
		// looks for.
		names, err := d.Readdirnames(1024)
		for _, name := range names {
			if kindOf(name) != nil {
				present[name] = true
			}
		}
		if errors.Is(err, io.EOF) {
			return present, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list the repository root: %w", bare(err))
		}
	}
}

// FromData returns the versions in data, the bytes of f.
func (x extractor) FromData(f file, data []byte) ([]check.Value, error) {
	var out []check.Value
	add := func(text string, line int, notLiteral bool) error {
		if err := core.CheckVersion(line, text); err != nil {
			return err
		}
		out = append(out, check.Value{Text: text, At: check.Location{File: f.File, Line: line}, NotLiteral: notLiteral})
		return nil
	}
	if err := f.kind.read(&x, f, data, add); err != nil {
		var n named
		if errors.As(err, &n) {
			return nil, n.error
		}
		return nil, fmt.Errorf("%s: %w", f.File, err)
	}
	return out, nil
}

// named is an error whose message names the file already.
type named struct{ error }

// knownNames lists the file names the table reads, for a message.
func knownNames() string {
	var names []string
	for _, k := range kinds {
		names = append(names, k.name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// bare drops the path from a file system error, since the path there is
// absolute and differs per machine, as core's reads do.
func bare(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
