// Package toolversion is the tool_version extractor: every version of one
// tool that the files pinning it name, such as the Node version in
// .tool-versions, .nvmrc and .node-version. Each file is read by its name, as
// the table in files.go says, and every version comes back as the file wrote
// it, with the file and the line.
package toolversion

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns every version of Tool in the files that pin it. Without
// Files, it reads each file the table lists for the tool that a walk of the
// repository finds (core.Walk); with Files, it reads exactly those, each of
// which must exist. Tool is spelled as the table spells it: node, not nodejs; go,
// not golang.
type Spec struct {
	Tool     string   `json:"tool"`
	Files    []string `json:"files,omitempty"`
	MaxBytes int64    `json:"max_bytes,omitempty"`
}

// Kind is the tool_version kind, which a rule writes under the key tool_version.
var Kind = core.Kind[Spec]{Key: "tool_version", Place: Place, Resolve: Resolve, Describe: Describe}

// Resolve is s with its max_bytes written out and its files copied, for
// errata list.
func Resolve(s Spec) Spec {
	s.Files = slices.Clone(s.Files)
	s.MaxBytes = core.MaxBytes(s.MaxBytes)
	return s
}

// Describe is what the kind's extractor returns: flat values, some not
// literal, as MayReturnNotLiteral says.
func Describe(Spec) check.Description {
	return check.Description{Label: "tool_version", NotLiteral: true}
}

// Parsers are the JSON and TOML readers the extractor reads package.json,
// pyproject.toml, Cargo.toml and rust-toolchain.toml with, as core.Parsers
// says.
type Parsers = core.Parsers

// maxTool is the longest tool name that loads, in bytes. Each line of a
// .tool-versions or .sdkmanrc file compares its name with the tool's, so the
// bound keeps that comparison a constant cost a line. The longest asdf plugin
// name is under 40 bytes.
const maxTool = 64

// aliases are the second spellings of a tool that .tool-versions files use:
// asdf's plugins are nodejs and golang, mise's node and go. A spec spells the
// tool as its command is spelled, and a line under either name pins it.
var aliases = map[string]string{"nodejs": "node", "golang": "go"}

// Build checks s and returns its extractor.
func Build(s Spec, p Parsers) (check.Extractor, error) {
	if err := checkTool(s.Tool); err != nil {
		return nil, err
	}
	x := extractor{tool: s.Tool, parsers: p, explicit: len(s.Files) > 0, maxBytes: s.MaxBytes, walk: core.DefaultWalk}
	if !x.explicit {
		// The walk finds the files; max_bytes is checked here, on a name
		// that is always the repository's.
		if _, err := core.CheckSource("tool_version", ".", s.MaxBytes); err != nil {
			return nil, err
		}
		x.search = map[string]bool{}
		for _, k := range kinds {
			if k.searched && k.pins(s.Tool) {
				x.search[k.name] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, name := range s.Files {
		src, err := core.CheckSource("tool_version", name, s.MaxBytes)
		if err != nil {
			return nil, err
		}
		clean := filepath.Clean(name)
		if seen[clean] {
			return nil, fmt.Errorf("tool_version: files names %s twice", core.Quote(name))
		}
		seen[clean] = true
		_, base := core.FileParts(name)
		k := kindOf(base)
		switch {
		case k == nil:
			return nil, fmt.Errorf("tool_version: errata does not know how %s pins a tool; it reads %s", core.Quote(name), knownNames())
		case !k.pins(s.Tool):
			return nil, fmt.Errorf("tool_version: %s pins %s, not %s", core.Quote(name), k.pinned(), s.Tool)
		}
		x.files = append(x.files, file{Source: src, kind: k})
	}
	var err error
	if x.paths, err = treePaths(s.Tool); err != nil {
		return nil, err
	}
	return x, nil
}

// checkTool refuses a tool name that no file could spell: empty, too long,
// or holding a character outside letters, digits and ._-:/@+, the
// characters of asdf's plugin names and mise's backends, such as
// npm:@biomejs/biome; and an alias.
func checkTool(tool string) error {
	switch {
	case tool == "":
		return errors.New("tool_version: tool is required, such as node or python")
	case len(tool) > maxTool:
		return fmt.Errorf("tool_version: tool %s is %d bytes long, over the limit of %d", core.Quote(tool), len(tool), maxTool)
	case strings.ContainsFunc(tool, func(c rune) bool { return !isToolRune(c) }):
		return fmt.Errorf("tool_version: tool %s holds a character other than a letter, a digit or one of ._-:/@+", core.Quote(tool))
	}
	if canonical, ok := aliases[tool]; ok {
		return fmt.Errorf("tool_version: write tool: %s; a .tool-versions line under %s pins it too", canonical, tool)
	}
	return nil
}

// isToolRune reports whether c may be part of a tool's name.
func isToolRune(c rune) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.ContainsRune("._-:/@+", c)
}

// Place is s with only what decides where it reads: each file cleaned, the
// files in order, since the same files in another order read the same
// versions, and max_bytes left out.
func Place(s Spec) Spec {
	p := Spec{Tool: s.Tool}
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

// extractor is a built tool_version extractor.
type extractor struct {
	tool     string
	files    []file
	explicit bool // files: named them, so each must exist
	maxBytes int64
	search   map[string]bool // without files:, the names the walk finds
	walk     core.WalkLimits // core.DefaultWalk; a test lowers them
	parsers  Parsers
	paths    map[string][]core.Path // the paths a tree file is read at, by its name
}

// MayReturnNotLiteral reports true: a version such as lts/* or system names
// no version until a tool resolves it.
func (extractor) MayReturnNotLiteral() bool { return true }

// Extract reads each file under root and returns its versions of the tool,
// the files in order and each file's in file order. Without files:, the
// files are those a walk of the repository finds, a directory's in the
// table's order before its subdirectories'; a symlink among them that
// dangles inside the repository is passed over, as a file that is not
// there. A file that files: names must exist.
func (x extractor) Extract(root string) ([]check.Value, error) {
	r := reading{x: x, root: root, out: []check.Value{}, read: map[string][]check.Value{}}
	if x.explicit {
		for _, f := range x.files {
			if err := r.file(f); err != nil {
				return nil, err
			}
		}
		return r.out, nil
	}
	err := core.Walk(root, x.walk, func(_, name string) bool { return x.search[name] }, func(dir string, names []string) error {
		// A directory's files in the table's order, .tool-versions first.
		slices.SortFunc(names, func(a, b string) int {
			return slices.Index(kinds, kindOf(a)) - slices.Index(kinds, kindOf(b))
		})
		for _, name := range names {
			src, err := core.CheckSource("tool_version", path.Join(dir, name), x.maxBytes)
			if err != nil {
				return err
			}
			if err := r.file(file{Source: src, kind: kindOf(name)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.out, nil
}

// reading is one Extract's state: the values so far, the bytes read and
// the bytes of the values, and the versions of each file read, by where its
// symlinks end, so links to one file read it once.
type reading struct {
	x          extractor
	root       string
	out        []check.Value
	bytes      int64 // read from every file
	valueBytes int64
	read       map[string][]check.Value
}

// file reads f, or takes its versions from the file it links to when that
// was read before, and adds them at f's own path.
func (r *reading) file(f file) error {
	resolved, err := f.Resolved(r.root)
	if err == nil {
		got, seen := r.read[resolved]
		if !seen {
			var data []byte
			if data, err = f.Read(r.root); err == nil {
				// The files of one extractor are read up to its max_bytes
				// in all, as one file is: a walk may find many, and
				// symlinks or copies may repeat one.
				if r.bytes += int64(len(data)); r.bytes > f.Limit {
					return fmt.Errorf("%s: the files the extractor reads hold more than %d bytes in all, the extractor's max_bytes; name the files to read in files, or set max_bytes: higher", f.File, f.Limit)
				}
				if got, err = r.x.FromData(f, data); err != nil {
					return err
				}
				r.read[resolved] = got
			}
		}
		if err == nil {
			return r.add(f, got)
		}
	}
	if !r.x.explicit && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// add adds the versions got at f's path, within the limits on values.
func (r *reading) add(f file, got []check.Value) error {
	for _, v := range got {
		if r.valueBytes += int64(len(v.Text)); r.valueBytes > f.Limit {
			return fmt.Errorf("%s: the versions the extractor selects hold more than %d bytes in all, the extractor's max_bytes; name fewer files, or set max_bytes: higher", f.File, f.Limit)
		}
		v.At.File = f.File
		if r.out = append(r.out, v); len(r.out) > core.MaxValues {
			return core.TooManyValues(f.File)
		}
	}
	return nil
}

// FromData returns the versions of the tool in data, the bytes of f.
func (x extractor) FromData(f file, data []byte) ([]check.Value, error) {
	var out []check.Value
	add := func(text string, line int) error {
		if err := core.CheckVersion(line, text); err != nil {
			return err
		}
		if len(out) == core.MaxValues {
			return named{core.TooManyValues(f.File)}
		}
		out = append(out, check.Value{Text: text, At: check.Location{File: f.File, Line: line}, NotLiteral: notLiteral(text)})
		return nil
	}
	err := f.kind.read(&x, f, data, add)
	if err == nil && f.kind.single && len(out) == 0 {
		err = errNoVersion
	}
	if err != nil {
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
