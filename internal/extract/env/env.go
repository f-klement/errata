// Package env is the env extractor: one variable across every file a rule
// lists that sets it, each file read by the reader its name chooses. It asks
// the envfile, shell, gitlab-ci, github-actions and jenkins readers, which
// the facade hands it, since no reader imports another, and lists the files
// a glob names from git's index (core.TrackedFiles), running nothing.
package env

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns every value of the variable Name in the files Files lists,
// each read by the reader its name chooses, or the one Readers gives its
// entry. An entry with *, ? or [ is a glob, matched one path segment at a
// time against the files git tracks; one that matches none adds no value.
// Any other entry names one file, which must exist. MaxBytes is the most
// bytes the files hold in all.
type Spec struct {
	Name     string            `json:"name"`
	Files    []string          `json:"files"`
	Readers  map[string]string `json:"readers,omitempty"`
	MaxBytes int64             `json:"max_bytes,omitempty"`
}

// Reader builds a reader's extractor of the variable name in file, which
// reads at most maxBytes: Build's for envfile and shell, Every's for the CI
// readers, whose values each name the job, step or stage they hold for.
type Reader func(file, name string, maxBytes int64) (check.Extractor, error)

// Readers are the readers env asks, by the key a rule writes for each
// under extract:, and the shell reader's test of a file's first bytes,
// IsScript, which reads at most ScriptBytes of them.
type Readers struct {
	ByName      map[string]Reader
	IsScript    func([]byte) bool
	ScriptBytes int
}

// readerNames are the readers env chooses among, in the order a message
// lists them.
var readerNames = []string{"envfile", "shell", "gitlab-ci", "github-actions", "jenkins"}

// maxName is the longest name that loads, in bytes, as the CI readers'.
const maxName = 1024

// maxEntries is the most entries files may list.
const maxEntries = 64

// maxMatchBytes is the most bytes of path the globs are matched against in
// all: each glob against every file git tracks, so their count times the
// bytes of the index's paths. path.Match costs time in proportion to the
// path, about 33 ns a byte on a 3 core worker node, and the index may hold
// 64 MiB of paths, so 64 globs over it took 167 s. The product is known
// once the index is read, so a spec over the bound is refused before any
// glob is matched. A dozen globs fit about 22 MB of paths: some 450000
// files of 50 bytes, or 320000 of 70.
const maxMatchBytes = 256 << 20

// maxFiles is the most files one extractor reads. Each costs an open, a
// stat and the head's read before its reader runs, even for an empty file,
// which max_bytes does not count, so a glob over the index's million files
// would run for minutes: 10000 empty files take 1.4 s on the 6 core build
// node and 3.7 s on a 3 core worker node.
const maxFiles = 10_000

// variableName is a name every reader env asks can read: a letter or _,
// then letters, digits and _, as a shell, GitHub and Jenkins name a
// variable. envfile and GitLab accept more, which the others do not.
var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Choose is the reader a file's name chooses, "" for a name that chooses
// none, which a shell shebang may still choose: envfile for a name that
// starts with .env, but .envrc, direnv's shell script, or ends in .env,
// such as prod.env, which compose's env_file: and systemd's
// EnvironmentFile= name; shell for a name
// that ends in .sh, and .envrc; gitlab-ci for .gitlab-ci.yml; github-actions
// for a .yml or .yaml file in .github/workflows; jenkins for Jenkinsfile.
// A local include of a GitLab CI file is read with the file that includes
// it.
func Choose(file string) string {
	p := path.Clean(filepath.ToSlash(file))
	base := path.Base(p)
	// ifs, not a switch's cases, which Go's coverage profile leaves
	// outside every block; so in Build.
	if base == ".envrc" || strings.HasSuffix(base, ".sh") {
		return "shell"
	}
	if strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env") {
		return "envfile"
	}
	if base == ".gitlab-ci.yml" {
		return "gitlab-ci"
	}
	if base == "Jenkinsfile" {
		return "jenkins"
	}
	if path.Dir(p) == ".github/workflows" && (path.Ext(base) == ".yml" || path.Ext(base) == ".yaml") {
		return "github-actions"
	}
	return ""
}

// isGlob reports whether a files entry is a glob.
func isGlob(entry string) bool { return strings.ContainsAny(entry, "*?[") }

// entry is one files entry: its pattern or file with slashes, cleaned, and
// the reader its readers: entry gives, "" for none.
type entry struct {
	clean, reader string
	glob          bool
}

// Build checks s and returns its extractor.
func Build(s Spec, r Readers) (check.Extractor, error) {
	if s.Name == "" {
		return nil, errors.New("env: name is required")
	}
	if len(s.Name) > maxName || !variableName.MatchString(s.Name) {
		return nil, fmt.Errorf("env: name %s is no variable name every reader reads: write a letter or _, then letters, digits and _, %d bytes at most", core.Quote(s.Name), maxName)
	}
	if len(s.Files) == 0 {
		return nil, errors.New("env: files is required: list the files that set the variable, such as .env, deploy.sh or .github/workflows/*.yml")
	}
	if len(s.Files) > maxEntries {
		return nil, fmt.Errorf("env: files lists %d entries, over the limit of %d; a glob such as config/*.env names many files in one", len(s.Files), maxEntries)
	}
	src, err := core.CheckSource("env", ".", s.MaxBytes)
	if err != nil {
		return nil, err
	}
	x := extractor{name: s.Name, limit: src.Limit, readers: r, fileLimit: maxFiles, matchLimit: maxMatchBytes}
	listed := map[string]bool{}
	for _, f := range s.Files {
		if _, err := core.CheckSource("env", f, s.MaxBytes); err != nil {
			return nil, err
		}
		e := entry{clean: path.Clean(filepath.ToSlash(f)), glob: isGlob(f)}
		if listed[e.clean] {
			return nil, fmt.Errorf("env: files names %s twice", core.Quote(f))
		}
		listed[e.clean] = true
		if _, err := path.Match(e.clean, ""); err != nil {
			return nil, fmt.Errorf("env: files entry %s is no glob: %w", core.Quote(f), err)
		}
		x.entries = append(x.entries, e)
	}
	for f, reader := range s.Readers {
		i := slices.IndexFunc(x.entries, func(e entry) bool { return e.clean == path.Clean(filepath.ToSlash(f)) })
		if i < 0 {
			return nil, fmt.Errorf("env: readers names %s, which files does not list", core.Quote(f))
		}
		if !slices.Contains(readerNames, reader) {
			return nil, fmt.Errorf("env: readers gives %s the reader %s; write one of %s", core.Quote(f), core.Quote(reader), strings.Join(readerNames, ", "))
		}
		x.entries[i].reader = reader
	}
	// Each reader an entry chooses checks the name now, so a name it
	// refuses, such as GITHUB_TOKEN, fails the load and not each run. A
	// glob's own name chooses as its files' do: .github/workflows/*.yml.
	for _, e := range x.entries {
		reader := e.reader
		if reader == "" {
			reader = Choose(e.clean)
		}
		if reader == "" {
			continue
		}
		if _, err := r.ByName[reader](e.clean, s.Name, s.MaxBytes); err != nil {
			return nil, fmt.Errorf("env: %w", err)
		}
	}
	return x, nil
}

// Place is s with only what decides where it reads: each file cleaned, the
// files in order, since the same files in another order read the same
// values, each readers: entry under its cleaned file, and max_bytes left
// out.
func Place(s Spec) Spec {
	p := Spec{Name: s.Name}
	for _, f := range s.Files {
		p.Files = append(p.Files, path.Clean(filepath.ToSlash(f)))
	}
	slices.Sort(p.Files)
	// A spec with no readers has an empty map, which JSON leaves out as it
	// leaves out none.
	p.Readers = map[string]string{}
	for f, r := range s.Readers {
		p.Readers[path.Clean(filepath.ToSlash(f))] = r
	}
	return p
}

// Resolve is s with its max_bytes written out and its files and readers
// copied, so a change to one does not reach s.
func Resolve(s Spec) Spec {
	s.Files = slices.Clone(s.Files)
	if s.Readers != nil {
		r := make(map[string]string, len(s.Readers))
		for k, v := range s.Readers {
			r[k] = v
		}
		s.Readers = r
	}
	s.MaxBytes = core.MaxBytes(s.MaxBytes)
	return s
}

// extractor is a built env extractor.
type extractor struct {
	// fileLimit is the most files Extract reads, maxFiles, and matchLimit
	// the bytes of path its globs match, maxMatchBytes: a test lowers them
	// to witness the bounds with a few files.
	fileLimit, matchLimit int
	name                  string
	limit                 int64
	entries               []entry
	readers               Readers
}

// MayReturnNotLiteral reports true: every reader env asks returns a value
// its file computes, such as $(cmd) or ${{ expr }}, marked not literal.
func (extractor) MayReturnNotLiteral() bool { return true }

// target is a file to read and the reader its entry gives, "" for the one
// its name or its shebang chooses.
type target struct {
	file, reader string
}

// Extract reads each file the entries name, in their order, a glob's in
// git's order, and returns the values of the variable, each with its
// reader and the part of the file it holds for as its origin.
func (x extractor) Extract(root string) ([]check.Value, error) {
	targets, err := x.targets(root)
	if err != nil {
		return nil, err
	}
	out := []check.Value{}
	left := x.limit // the bytes the files read may still hold
	var held int64  // the bytes of the values so far
	for _, t := range targets {
		head, size, err := core.Source{File: t.file, Limit: left}.Head(root, x.readers.ScriptBytes)
		if err != nil && strings.Contains(err.Error(), "bytes, over the limit of") {
			// The limit the message names is what the files before left,
			// or max_bytes for the first.
			return nil, fmt.Errorf("env: %w; the files env reads, and a GitLab CI file's includes, hold at most max_bytes, %d, in all", err, x.limit)
		}
		if err != nil {
			return nil, err
		}
		reader := t.reader
		if reader == "" {
			reader = Choose(t.file)
		}
		if reader == "" && x.readers.IsScript(head) {
			reader = "shell"
		}
		if reader == "" {
			return nil, fmt.Errorf("env: no reader reads %s: its name is none the readers' are, and it starts with no #! line of a shell; give it one under readers:, one of %s", core.Quote(t.file), strings.Join(readerNames, ", "))
		}
		// At least 1, since a reader's 0 is the default of 32 MiB: a file
		// that grows past what Head saw is then refused as grown.
		vs, read, err := x.read(root, t.file, reader, max(left, 1))
		if err != nil {
			return nil, err
		}
		left -= max(size, read)
		for _, v := range vs {
			if len(out) == core.MaxValues {
				return nil, fmt.Errorf("env: the files hold more than %d values of %s, the limit", core.MaxValues, x.name)
			}
			if held += int64(len(v.Text)); held > x.limit {
				return nil, fmt.Errorf("env: the values of %s hold more than %d bytes in all, the extractor's max_bytes; set max_bytes: higher", x.name, x.limit)
			}
			v.Key, v.HasKey = "", false
			v.Origin = strings.TrimSpace(reader + " " + v.Origin)
			out = append(out, v)
		}
	}
	return out, nil
}

// read is the values reader gives of the variable in file, none for a
// check.NoValue: a file that does not set it adds nothing; and the bytes
// it read, for a reader that reads more files than the one, as gitlab-ci
// reads its includes (BytesReader), else 0.
func (x extractor) read(root, file, reader string, maxBytes int64) ([]check.Value, int64, error) {
	r, err := x.readers.ByName[reader](file, x.name, maxBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("env: %w", err)
	}
	var vs []check.Value
	var read int64
	if br, ok := r.(BytesReader); ok {
		vs, read, err = br.ExtractRead(root)
	} else {
		vs, err = r.Extract(root)
	}
	var none *check.NoValue
	if errors.As(err, &none) {
		return nil, read, nil
	}
	return vs, read, err
}

// checkMatch refuses a spec whose globs, each matched against every tracked
// path, would match more than matchLimit bytes of path, before the first
// is matched: the bytes are the globs' count times the paths' bytes.
func (x extractor) checkMatch(tracked []string) error {
	globs, paths := 0, 0
	for _, e := range x.entries {
		if e.glob {
			globs++
		}
	}
	for _, f := range tracked {
		paths += len(f)
	}
	if globs*paths > x.matchLimit {
		return fmt.Errorf("env: files lists %d globs, and matching each against the %d bytes of path git tracks takes more than %d bytes, the limit; list fewer globs", globs, paths, maxMatchBytes)
	}
	return nil
}

// BytesReader is an extractor that reads more files than the one it is
// built for and says how many bytes they hold, which env charges to its
// max_bytes: gitlab-ci, with its includes.
type BytesReader interface {
	ExtractRead(root string) ([]check.Value, int64, error)
}

// targets lists the files the entries name, each once, in the entries'
// order: a file, or the files git tracks that a glob matches, in git's
// order. The index is read once, and only when an entry is a glob.
func (x extractor) targets(root string) ([]target, error) {
	var tracked []string      // nil until read; TrackedFiles never returns nil
	seen := map[string]int{}  // each file's index in out
	by := map[string]string{} // the entry that named each file, or gave it its reader
	var out []target
	for _, e := range x.entries {
		files := []string{e.clean}
		if e.glob {
			if tracked == nil {
				var err error
				if tracked, err = core.TrackedFiles(root); err != nil {
					return nil, fmt.Errorf("env: files lists the glob %s: %w", core.Quote(e.clean), err)
				}
				if err := x.checkMatch(tracked); err != nil {
					return nil, err
				}
			}
			files = nil
			for _, f := range tracked {
				// The pattern was checked when it was built, so Match
				// returns no error here.
				if ok, _ := path.Match(e.clean, f); ok {
					files = append(files, f)
				}
			}
		}
		for _, f := range files {
			i, ok := seen[f]
			if !ok {
				if len(out) == x.fileLimit {
					return nil, fmt.Errorf("env: files names more than %d files, the limit, at %s; narrow the glob", maxFiles, core.Quote(e.clean))
				}
				seen[f], by[f] = len(out), e.clean
				out = append(out, target{file: f, reader: e.reader})
				continue
			}
			// A reader one entry gives a file wins over none, whichever
			// entry comes first, and two readers for one file refuse it.
			if prev := out[i].reader; e.reader != "" && e.reader != prev {
				if prev != "" {
					return nil, fmt.Errorf("env: files entries %s and %s both name %s, and readers: gives them %s and %s; give the file one reader", core.Quote(by[f]), core.Quote(e.clean), core.Quote(f), prev, e.reader)
				}
				out[i].reader, by[f] = e.reader, e.clean
			}
		}
	}
	return out, nil
}
