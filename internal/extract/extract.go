// Package extract builds the extractors a rule names in errata.yaml. Each one
// reads one file and returns every value it finds there, with its location.
//
// The types below are the seam between the config package, which decodes a
// rule, and this package, which runs its extractors. Their JSON names are the
// keys a user writes under a rule's extract:.
package extract

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/f-klement/errata/internal/check"
)

// Spec is one named entry under a rule's extract:. Exactly one field is set.
type Spec struct {
	YAML  *YAML  `json:"yaml,omitempty"`
	JSON  *JSON  `json:"json,omitempty"`
	TOML  *TOML  `json:"toml,omitempty"`
	Regex *Regex `json:"regex,omitempty"`
	Text  *Text  `json:"text,omitempty"`
	XML   *XML   `json:"xml,omitempty"`

	INI        *INI        `json:"ini,omitempty"`
	Properties *Properties `json:"properties,omitempty"`
}

// Each kind takes MaxBytes, the largest file it reads. Zero, or the key
// left out, is the default of 32 MiB.

// YAML reads a path in a YAML file. A file holding several documents needs
// Kind or Name, or both, to select one.
type YAML struct {
	File     string `json:"file"`
	Path     string `json:"path"`
	Kind     string `json:"kind,omitempty"`
	Name     string `json:"name,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// JSON reads a path in a JSON file.
type JSON struct {
	File     string `json:"file"`
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// TOML reads a path in a TOML file.
type TOML struct {
	File     string `json:"file"`
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// Regex returns capture group 1 of every match of Pattern in File.
type Regex struct {
	File     string `json:"file"`
	Pattern  string `json:"pattern"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// Place returns s with only what decides where it reads: each file cleaned,
// so f, ./f and a/../f are one file, and max_bytes left out, since a limit
// does not change what is read. Two specs with one place return the same
// values at the same locations.
func (s Spec) Place() Spec {
	var p Spec
	if s.YAML != nil {
		y := *s.YAML
		y.File, y.MaxBytes = filepath.Clean(y.File), 0
		p.YAML = &y
	}
	if s.JSON != nil {
		j := *s.JSON
		j.File, j.MaxBytes = filepath.Clean(j.File), 0
		p.JSON = &j
	}
	if s.TOML != nil {
		t := *s.TOML
		t.File, t.MaxBytes = filepath.Clean(t.File), 0
		p.TOML = &t
	}
	if s.Regex != nil {
		r := *s.Regex
		r.File, r.MaxBytes = filepath.Clean(r.File), 0
		p.Regex = &r
	}
	if s.Text != nil {
		x := *s.Text
		x.File, x.MaxBytes = filepath.Clean(x.File), 0
		p.Text = &x
	}
	if s.XML != nil {
		x := *s.XML
		x.File, x.MaxBytes = filepath.Clean(x.File), 0
		p.XML = &x
	}
	if s.INI != nil {
		i := iniPlace(*s.INI)
		p.INI = &i
	}
	if s.Properties != nil {
		x := *s.Properties
		x.File, x.MaxBytes = filepath.Clean(x.File), 0
		p.Properties = &x
	}
	return p
}

// Build checks s and returns its extractor. A Spec that sets no kind, or more
// than one, is a load error, and so is a file outside the repository, a
// max_bytes below zero or over its ceiling, a path that does not parse, or a
// pattern that does not compile.
func Build(s Spec) (check.Extractor, error) {
	var names []string
	var set []extractorKind
	for _, k := range s.kinds() {
		names = append(names, k.name)
		if k.set {
			set = append(set, k)
		}
	}
	if len(set) != 1 {
		return nil, fmt.Errorf("an extractor sets exactly one of %s", strings.Join(names, ", "))
	}
	return set[0].build()
}

// extractorKind is one key of Spec: whether it is set, and how to build it.
// The count of set kinds and the dispatch both come from kinds, so a kind
// cannot be counted without being built. TestKindsCoverSpec fails when Spec
// gains a field that kinds does not list.
type extractorKind struct {
	name  string
	set   bool
	build func() (check.Extractor, error)
}

func (s Spec) kinds() []extractorKind {
	return []extractorKind{
		{"yaml", s.YAML != nil, func() (check.Extractor, error) { return buildYAML(*s.YAML) }},
		{"json", s.JSON != nil, func() (check.Extractor, error) {
			return buildTree("json", s.JSON.File, s.JSON.Path, s.JSON.MaxBytes, jsonFormat, parseJSON)
		}},
		{"toml", s.TOML != nil, func() (check.Extractor, error) {
			return buildTree("toml", s.TOML.File, s.TOML.Path, s.TOML.MaxBytes, tomlFormat, parseTOML)
		}},
		{"regex", s.Regex != nil, func() (check.Extractor, error) { return buildRegex(*s.Regex) }},
		{"text", s.Text != nil, func() (check.Extractor, error) { return buildText(*s.Text) }},
		{"xml", s.XML != nil, func() (check.Extractor, error) { return buildXML(*s.XML) }},
		{"ini", s.INI != nil, func() (check.Extractor, error) { return buildINI(*s.INI) }},
		{"properties", s.Properties != nil, func() (check.Extractor, error) { return buildProperties(*s.Properties) }},
	}
}

// Kind names the kind of extractor s sets, for a message: its key, and for
// ini the dialect it reads in and the file's name, since what a dialect's
// tool computes can hang on the file: "ini, plain dialect on pytest.ini".
// It is "" when s sets no kind or more than one, or names no dialect errata
// reads.
func (s Spec) Kind() string {
	kind := ""
	for _, k := range s.kinds() {
		if k.set {
			if kind != "" {
				return ""
			}
			kind = k.name
		}
	}
	if kind == "ini" {
		d, err := chooseDialect(s.INI.Dialect, s.INI.File)
		if err != nil {
			return ""
		}
		_, base := fileParts(s.INI.File)
		kind += ", " + d.name + " dialect on " + base
	}
	return kind
}

// checkPath parses a path, refusing an empty one by the field's name.
func checkPath(kind, p string) (path, error) {
	if p == "" {
		return path{}, fmt.Errorf("%s: path is required; write $ for the whole document", kind)
	}
	parsed, err := parsePath(p)
	if err != nil {
		return path{}, fmt.Errorf("%s: %w", kind, err)
	}
	return parsed, nil
}

// source is the file an extractor reads, and the most bytes it reads of it.
type source struct {
	file  string
	limit int64
}

// checkSource refuses a file outside the repository: an absolute path, or
// one whose .. leaves the root. A repository's config must not read files
// that are not in the repository. A symlink that leads out is refused when
// the file is read, since only the file system knows where it points. A
// max_bytes of zero is the default limit, and one below zero or over
// maxMaxBytes is refused.
func checkSource(kind, file string, maxBytes int64) (source, error) {
	if file == "" {
		return source{}, fmt.Errorf("%s: file is required", kind)
	}
	if len(file) > maxFileName {
		return source{}, fmt.Errorf("%s: file %s is %d bytes long, over the limit of %d; write a shorter path", kind, quote(file), len(file), maxFileName)
	}
	if !filepath.IsLocal(file) {
		return source{}, fmt.Errorf("%s: file %s is outside the repository: write a path relative to the repository root that does not leave it", kind, quote(file))
	}
	switch {
	case maxBytes < 0 || maxBytes > maxMaxBytes:
		return source{}, fmt.Errorf("%s: max_bytes is %d; write a number of bytes from 1 to %d, or leave it out for %d",
			kind, maxBytes, maxMaxBytes, defaultMaxBytes)
	case maxBytes == 0:
		maxBytes = defaultMaxBytes
	}
	return source{file: file, limit: maxBytes}, nil
}

// errOutside refuses a symlink that leads out of the repository. It names
// neither where the link points nor whether that exists: the message reaches
// a CI log that a pull request can read.
var errOutside = errors.New("a symlink leads outside the repository")

// maxLinks is how many symlinks one read follows, as on Linux, before it
// calls the chain a loop.
const maxLinks = 40

// read reads the file under root. A file over the limit is refused before
// it is read, and a file that grows past it while it is read is refused too.
// Every error names the file as the spec wrote it, never an absolute path.
func (s source) read(root string) ([]byte, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("open the repository root: %w", bare(err))
	}
	rel, err := resolve(realRoot, s.file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.file, err)
	}
	r, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, fmt.Errorf("open the repository root: %w", bare(err))
	}
	defer r.Close()
	// The open goes through os.Root, which refuses a link that leads out,
	// so a link swapped in after resolve is refused too.
	f, err := r.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.file, bare(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.file, bare(err))
	}
	if info.Size() > s.limit {
		return nil, tooLarge(s.file, info.Size(), s.limit)
	}
	data, err := io.ReadAll(io.LimitReader(f, s.limit))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.file, bare(err))
	}
	// A byte past the limit means the file grew while it was read. The
	// limit is never added to, so no limit overflows.
	if n, _ := f.Read(make([]byte, 1)); n > 0 {
		return nil, tooLarge(s.file, int64(len(data))+1, s.limit)
	}
	return data, nil
}

// bare drops the path from a file system error, since the path there is
// absolute and differs per machine.
func bare(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// resolve follows the symlinks on the way to file and returns the path,
// relative to realRoot, where it ends. It walks one name at a time as the
// kernel does, so a .. in a link's target goes up from where the link
// points. It looks only at paths inside realRoot, and refuses a link that
// leads out before it touches anything outside. A route may pass through a
// directory above the root on its way back in, since those directories are
// real: realRoot is the root with its own links resolved. A .. in file itself
// is resolved by name first, by Clean, before any link.
func resolve(realRoot, file string) (string, error) {
	todo := splitPath(filepath.Clean(file))
	cur := realRoot
	links := 0
	for len(todo) > 0 {
		name := todo[0]
		todo = todo[1:]
		if name == ".." {
			cur = filepath.Dir(cur) // cur holds no link, so .. is its parent
			continue
		}
		next := filepath.Join(cur, name)
		if !within(realRoot, next) {
			if !within(next, realRoot) {
				return "", errOutside
			}
			cur = next // a directory above the root, on the way back to it
			continue
		}
		info, err := os.Lstat(next)
		if err != nil {
			return "", bare(err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		if links++; links > maxLinks {
			return "", errors.New("too many levels of symbolic links")
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", bare(err)
		}
		if filepath.IsAbs(target) {
			cur = filepath.VolumeName(target) + string(filepath.Separator)
		}
		todo = append(splitPath(target), todo...)
	}
	if !within(realRoot, cur) {
		return "", errOutside
	}
	return filepath.Rel(realRoot, cur)
}

// splitPath lists the names in p, without the empty ones and ".".
func splitPath(p string) []string {
	var out []string
	for _, n := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == filepath.Separator }) {
		if n != "." {
			out = append(out, n)
		}
	}
	return out
}

// within reports whether p is dir or a path below it.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && filepath.IsLocal(rel)
}

// treeExtractor reads a path in a JSON or TOML file.
type treeExtractor struct {
	source
	path   path
	format *format
	parse  func([]byte) (*node, error)
}

func buildTree(kind, file, p string, maxBytes int64, f *format, parse func([]byte) (*node, error)) (check.Extractor, error) {
	src, err := checkSource(kind, file, maxBytes)
	if err != nil {
		return nil, err
	}
	parsed, err := checkPath(kind, p)
	if err != nil {
		return nil, err
	}
	return treeExtractor{source: src, path: parsed, format: f, parse: parse}, nil
}

// Extract reads the file under root and returns every scalar its path selects.
func (e treeExtractor) Extract(root string) ([]check.Value, error) {
	data, err := e.read(root)
	if err != nil {
		return nil, err
	}
	return e.fromData(data)
}

func (e treeExtractor) fromData(data []byte) ([]check.Value, error) {
	if err := e.checkNodes(data, e.format); err != nil {
		return nil, err
	}
	tree, err := e.parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.file, err)
	}
	return e.values(e.path, tree)
}
