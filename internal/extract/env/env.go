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
// entry. An entry with *, ? or [ is a glob, matched against the files git
// tracks as core.Files matches it; one that matches none adds no value.
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

// The bounds on files: are core's, which every files: key shares.
const (
	maxEntries    = core.MaxEntries
	maxMatchBytes = core.MaxMatchBytes
	maxFiles      = core.MaxFiles
)

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
	src, err := core.CheckSource("env", ".", s.MaxBytes)
	if err != nil {
		return nil, err
	}
	files, err := core.CheckFiles("env", s.Files, s.MaxBytes)
	if err != nil {
		return nil, err
	}
	x := extractor{name: s.Name, limit: src.Limit, readers: r, files: files, by: make([]string, len(files.Entries))}
	for f, reader := range s.Readers {
		i := slices.IndexFunc(files.Entries, func(e core.FileEntry) bool { return e.Clean == path.Clean(filepath.ToSlash(f)) })
		if i < 0 {
			return nil, fmt.Errorf("env: readers names %s, which files does not list", core.Quote(f))
		}
		if !slices.Contains(readerNames, reader) {
			return nil, fmt.Errorf("env: readers gives %s the reader %s; write one of %s", core.Quote(f), core.Quote(reader), strings.Join(readerNames, ", "))
		}
		x.by[i] = reader
	}
	// Each reader an entry chooses checks the name now, so a name it
	// refuses, such as GITHUB_TOKEN, fails the load and not each run. A
	// glob's own name chooses as its files' do: .github/workflows/*.yml.
	for i, e := range files.Entries {
		reader := x.by[i]
		if reader == "" {
			reader = Choose(e.Clean)
		}
		if reader == "" {
			continue
		}
		if _, err := r.ByName[reader](e.Clean, s.Name, s.MaxBytes); err != nil {
			return nil, fmt.Errorf("env: %w", err)
		}
	}
	return x, nil
}

// Kind is the env kind, which a rule writes under the key env.
var Kind = core.Kind[Spec]{Key: "env", Place: Place, Resolve: Resolve, Describe: Describe}

// Describe is what the kind's extractor returns: flat values, some not
// literal, since every reader env asks marks a value its file computes.
func Describe(Spec) check.Description {
	return check.Description{Label: "env", NotLiteral: true}
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
	name  string
	limit int64
	// files are the entries of files:, and by the reader readers: gives
	// each, by its index, "" for none.
	files   core.Files
	by      []string
	readers Readers
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
		if err != nil && errors.Is(err, core.ErrTooLarge) {
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

// BytesReader is an extractor that reads more files than the one it is
// built for and says how many bytes they hold, which env charges to its
// max_bytes: gitlab-ci, with its includes.
type BytesReader interface {
	ExtractRead(root string) ([]check.Value, int64, error)
}

// targets lists the files the entries name, each once, in the entries'
// order: a file, or the files git tracks that a glob matches, in git's
// order (core.Files). A file takes the reader one entry that names it
// gives, whichever entry comes first, and two readers refuse it.
func (x extractor) targets(root string) ([]target, error) {
	named, err := x.files.List(root)
	if err != nil {
		return nil, err
	}
	out := make([]target, 0, len(named))
	for _, n := range named {
		t := target{file: n.File}
		first := n.By[0] // the entry that gave the reader, or named the file
		for _, i := range n.By {
			if r := x.by[i]; r != "" && r != t.reader {
				if t.reader != "" {
					return nil, fmt.Errorf("env: files entries %s and %s both name %s, and readers: gives them %s and %s; give the file one reader", core.Quote(x.files.Entries[first].Clean), core.Quote(x.files.Entries[i].Clean), core.Quote(n.File), t.reader, r)
				}
				t.reader, first = r, i
			}
		}
		out = append(out, t)
	}
	return out, nil
}
