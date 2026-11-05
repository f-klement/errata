// Package dependency is the dependency extractor: one package's version in
// every manifest and lockfile a rule lists, the way env asks every reader
// for one variable. Each file is read by the reader its name chooses, as the
// table in readers.go says: go.mod and go.work, npm's, pnpm's and Yarn's
// files, Python's, Cargo's, Bundler's and Composer's. A manifest states a
// range of versions the package may take, such as ^1.2, and a lockfile the
// version a package manager resolved, such as 1.2.5; each value says which
// it is and the reader that found it, in its origin. With module: true it
// returns the module path each go.mod declares instead.
//
// package_version reads the version a package gives itself in its own
// manifests; this extractor reads a package's entries in the files that
// depend on it, the package's own entry in a lockfile included, and never
// the manifest's own version. Both read their JSON and TOML through
// core.Source.ParseManifest, and go.mod through core/gomod.
package dependency

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns every version of the package Name in the files Files
// lists, each read by the reader its name chooses. An entry with *, ? or [
// is a glob, matched against the files git tracks as core.Files matches
// it; one that matches none adds no value. Any other entry names one file,
// which must exist. Only keeps the values of one role, range or resolved.
// Module returns each go.mod's module path in place of Name's versions.
// MaxBytes is the most bytes the files hold in all.
type Spec struct {
	Name     string   `json:"name,omitempty"`
	Module   bool     `json:"module,omitempty"`
	Files    []string `json:"files"`
	Only     string   `json:"only,omitempty"`
	MaxBytes int64    `json:"max_bytes,omitempty"`
}

// Bound is the max_bytes s sets, 0 for the default (core.Bounded).
func (s Spec) Bound() int64 { return s.MaxBytes }

// Kind is the dependency kind, which a rule writes under the key
// dependency.
var Kind = core.Kind[Spec]{Key: "dependency", Place: Place, Resolve: Resolve, Describe: Describe}

// Describe is what the kind's extractor returns: flat values, some not
// literal, since a range such as workspace:* or a Cargo dependency that
// inherits the workspace's names no version in the file.
func Describe(Spec) check.Description {
	return check.Description{Label: "dependency", NotLiteral: true}
}

// Place is s with only what decides where it reads: each file cleaned and
// the files in order, since the same files in another order read the same
// values, and max_bytes left out.
func Place(s Spec) Spec {
	p := Spec{Name: s.Name, Module: s.Module, Only: s.Only}
	for _, f := range s.Files {
		p.Files = append(p.Files, path.Clean(filepath.ToSlash(f)))
	}
	slices.Sort(p.Files)
	return p
}

// Resolve is s with its max_bytes written out and its files copied, for
// errata list.
func Resolve(s Spec) Spec {
	s.Files = slices.Clone(s.Files)
	s.MaxBytes = core.MaxBytes(s.MaxBytes)
	return s
}

// The roles a value takes, which only: keeps one of.
const (
	// Range is a manifest's statement of the versions it accepts, such as
	// ^1.2 in package.json or ~> 7.1 in a Gemfile.
	Range = "range"
	// Resolved is the version a lockfile records the package manager
	// installed, such as 1.2.5 in package-lock.json.
	Resolved = "resolved"
)

// maxName is the longest name that loads, in bytes. The longest npm name
// is 214 bytes, and a Go module path stays well under this.
const maxName = 1024

// Parsers are the JSON, TOML and YAML readers the extractor reads its
// files with, as core.Parsers says.
type Parsers = core.Parsers

// Build checks s and returns its extractor.
func Build(s Spec, p Parsers) (check.Extractor, error) {
	switch {
	case s.Module && s.Name != "":
		return nil, errors.New("dependency: module: true returns each go.mod's module path, so it takes no name; leave one of them out")
	case s.Module && s.Only != "":
		return nil, errors.New("dependency: only keeps ranges or resolved versions, and a module path is neither; leave only out")
	case !s.Module && s.Name == "":
		return nil, errors.New("dependency: name is required: write the package's name, such as golang.org/x/mod, lodash or serde, or module: true for go.mod's module path")
	case len(s.Name) > maxName:
		return nil, fmt.Errorf("dependency: name is %d bytes long, over the limit of %d", len(s.Name), maxName)
	case strings.ContainsFunc(s.Name, func(r rune) bool { return unicode.IsSpace(r) || check.ActedOn(r) }):
		return nil, fmt.Errorf("dependency: name %s holds a blank or a character a terminal acts on, which no package's name holds", core.Quote(s.Name))
	case s.Only != "" && s.Only != Range && s.Only != Resolved:
		return nil, fmt.Errorf("dependency: only is %s; write %s for the versions manifests accept, or %s for the versions lockfiles record", core.Quote(s.Only), Range, Resolved)
	case len(s.Files) == 0:
		return nil, errors.New("dependency: files is required: list the manifests and lockfiles, such as go.mod, package.json, package-lock.json or **/Cargo.toml")
	}
	src, err := core.CheckSource("dependency", ".", s.MaxBytes)
	if err != nil {
		return nil, err
	}
	files, err := core.CheckFiles("dependency", s.Files, s.MaxBytes)
	if err != nil {
		return nil, err
	}
	x := extractor{name: s.Name, module: s.Module, only: s.Only, limit: src.Limit, files: files, parsers: p}
	// A file an entry names outright must have a reader, so a name errata
	// does not read fails the load and not each run.
	for _, e := range files.Entries {
		if e.Glob {
			continue
		}
		if _, err := x.readerOf(e.Clean); err != nil {
			return nil, err
		}
	}
	return x, nil
}

// extractor is a built dependency extractor.
type extractor struct {
	name    string
	module  bool
	only    string
	limit   int64
	files   core.Files
	parsers Parsers
}

// readerOf is the reader of file, by its base name, or the refusal of a
// file no reader reads, or, under module: true, one that is not a go.mod.
func (x extractor) readerOf(file string) (*reader, error) {
	r := readerFor(core.FileParts(file))
	switch {
	case r == nil:
		return nil, fmt.Errorf("dependency: errata does not know how %s names a package's version; it reads %s", core.Quote(file), knownNames())
	case x.module && r.name != "go.mod":
		return nil, fmt.Errorf("dependency: module: true reads the module path of a go.mod, and %s is no go.mod", core.Quote(file))
	}
	return r, nil
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (x extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return x.Extract(ctx, root)
}

// Extract reads each file the entries name, in their order, a glob's in
// git's order, and returns the package's values, each file's in the order
// of their lines. The files hold at most max_bytes in all, and so do the
// values.
func (x extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	named, err := x.files.List(ctx, root)
	if err != nil {
		return nil, err
	}
	out := []check.Value{}
	left := x.limit // the bytes the files read may still hold
	var held int64  // the bytes of the values so far
	for _, n := range named {
		r, err := x.readerOf(n.File)
		if err != nil {
			return nil, err
		}
		src := core.Source{File: n.File, Limit: left}
		data, err := src.Read(ctx, root)
		if errors.Is(err, core.ErrTooLarge) {
			return nil, fmt.Errorf("dependency: %w; the files the extractor reads hold at most max_bytes, %d, in all", err, x.limit)
		}
		if err != nil {
			return nil, err
		}
		left -= int64(len(data))
		got, err := x.FromData(r, src, data)
		if err != nil {
			return nil, err
		}
		for _, v := range got {
			if len(out) == core.MaxValues {
				return nil, fmt.Errorf("dependency: the files hold more than %d values of %s, the limit", core.MaxValues, x.what())
			}
			if held += int64(len(v.Text)); held > x.limit {
				return nil, fmt.Errorf("dependency: the values of %s hold more than %d bytes in all, the extractor's max_bytes; set max_bytes: higher", x.what(), x.limit)
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// what names what the extractor returns, for a message.
func (x extractor) what() string {
	if x.module {
		return "the module path"
	}
	return core.Quote(x.name)
}

// FromData returns the values in data, the bytes of the file src names,
// which r reads, in the order of their lines.
func (x extractor) FromData(r *reader, src core.Source, data []byte) ([]check.Value, error) {
	rd := reading{x: &x, r: r, src: src, want: r.fold(x.name)}
	if err := r.read(&rd, data); err != nil {
		var n named
		if errors.As(err, &n) {
			return nil, n.error
		}
		return nil, fmt.Errorf("%s: %w", src.File, err)
	}
	slices.SortStableFunc(rd.out, func(a, b check.Value) int { return a.At.Line - b.At.Line })
	return rd.out, nil
}

// reading is one file being read: the extractor, the file's reader and its
// source, and the values found so far.
type reading struct {
	x   *extractor
	r   *reader
	src core.Source
	out []check.Value
	// want is the package's name as the reader folds names, once a file.
	want string
}

// found is one value a reader found: its text and line, the part of the
// file it stands in, such as devDependencies, "" for a file of one part,
// its role, and whether the file only computes it.
type found struct {
	text, section, role string
	line                int
	notLiteral          bool
}

// add keeps f unless only: names the other role. Its origin names the
// reader, the part of the file and the role.
func (rd *reading) add(f found) error {
	if rd.x.only != "" && f.role != rd.x.only {
		return nil
	}
	if err := core.CheckVersion(f.line, f.text); err != nil {
		return err
	}
	origin := rd.r.name
	if f.section != "" {
		origin += " " + f.section
	}
	if f.role != "" {
		origin += " (" + f.role + ")"
	}
	rd.out = append(rd.out, check.Value{Text: f.text, At: check.Location{File: rd.src.File, Line: f.line}, NotLiteral: f.notLiteral, Origin: origin})
	return nil
}

// is reports whether written names the package, as the reader's ecosystem
// compares names.
func (rd *reading) is(written string) bool { return rd.r.fold(written) == rd.want }

// named is an error whose message names the file already.
type named struct{ error }

// knownNames lists the file names the readers read, for a message.
func knownNames() string {
	var names []string
	for _, r := range readers {
		names = append(names, r.pattern)
	}
	return strings.Join(names, ", ")
}
