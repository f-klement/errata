// Package gitlabci is the gitlab-ci extractor: the value one job of a GitLab
// CI configuration sees, for a CI/CD variable or a job keyword, at the line
// that set it. It reads the configuration as GitLab builds it from the
// repository's own files: local includes merged in their order, extends
// merged into the job, !reference tags followed, default keywords and
// top-level variables inherited, and the variables of rules returned as
// candidates. It reads the YAML with the parser the facade hands it, since
// no reader imports another, and touches no network: what only GitLab knows
// (a remote, project, template or component include, a project's settings,
// a pipeline's variables) makes the value unknown, naming it.
package gitlabci

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads one job of a GitLab CI file. Variable names a CI/CD variable,
// and Key a job keyword, such as image or script, or a key below one, such
// as image.name; exactly one of them is set.
type Spec struct {
	core.OneFile
	Job      string `json:"job"`
	Variable string `json:"variable,omitempty"`
	Key      string `json:"key,omitempty"`
}

// kindKey is the kind's key, which Kind and Build both use. Build names
// the constant rather than Kind.Key because an env reader's Read calls
// Build, and Build reading Kind would make an initialization cycle.
const kindKey = "gitlab-ci"

// Kind is the gitlab-ci kind, which a rule writes under the key gitlab-ci.
// It is an env reader, third in env's list.
var Kind = core.Kind[Spec]{Key: kindKey, Place: Place, Resolve: Resolve, Describe: Describe,
	Env: &check.EnvReader{Order: 30, Chooses: chooses}, EnvRead: envRead}

// chooses takes, for the env kind, a file named .gitlab-ci.yml.
func chooses(file string) bool { return path.Base(file) == ".gitlab-ci.yml" }

// envRead is the env kind's reader of the variable name in file: Every's,
// with the YAML parser the facade hands it.
func envRead(file, name string, maxBytes int64, p core.Parsers) (check.Extractor, error) {
	return Every(file, name, maxBytes, p.Noted)
}

// Resolve is s with its max_bytes written out, for errata list.
func Resolve(s Spec) Spec {
	s.OneFile = s.Resolved()
	return s
}

// Describe is what the kind's extractor returns: flat values, some not
// literal, since a variable that names another, such as $CI_COMMIT_SHA,
// and a number YAML retypes, such as 3.10, are values GitLab computes.
func Describe(Spec) check.Description {
	return check.Description{Label: "gitlab-ci", NotLiteral: true}
}

// Parser parses every YAML document of a file into core's tree, and calls
// note for each node with the tag the file wrote on it, such as !reference
// or !!float, and whether it is a plain scalar with no tag. It counts the
// nodes YAML aliases and merge keys add from expanded, the count of the
// files before, against core.MaxExpanded, and returns the count it ends
// at, so the bound holds for every file read together. The facade hands it
// yaml.ParseNoted.
type Parser func(data []byte, expanded int, note func(n *core.Node, tag string, plain bool)) ([]*core.Node, int, error)

// maxName is the longest job, variable or key that loads, in bytes. A name
// is compared with the keys of the mappings it is looked up in, so the bound
// keeps each comparison a constant cost. GitLab caps a job's name at 255
// characters.
const maxName = 1024

// variableName is what GitLab accepts as a variable's name: "Names can use
// only numbers, letters, and underscores."
var variableName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// reserved are the top-level keys of a GitLab CI file that are no job.
var reserved = map[string]bool{
	"default": true, "include": true, "stages": true, "variables": true, "workflow": true,
	"image": true, "services": true, "cache": true, "before_script": true, "after_script": true,
	"spec": true,
}

// Build checks s and returns its extractor.
func Build(s Spec, parse Parser) (check.Extractor, error) {
	src, err := s.Check(kindKey)
	if err != nil {
		return nil, err
	}
	if err := checkJob(s.Job); err != nil {
		return nil, err
	}
	x := extractor{Source: src, job: s.Job, variable: s.Variable, parse: parse}
	switch {
	case (s.Variable == "") == (s.Key == ""):
		return nil, errors.New("gitlab-ci: set exactly one of variable, for a CI/CD variable, and key, for a job keyword such as image")
	case s.Variable != "":
		if err := checkVariable(s.Variable); err != nil {
			return nil, err
		}
	default:
		if x.key, err = checkKey(s.Key); err != nil {
			return nil, err
		}
	}
	return x, nil
}

// Every builds the extractor of the CI/CD variable variable across every
// job of the GitLab CI file: the values each job sees, as Build's for that
// job, each with the job as its origin, job "name", the jobs in the order
// the file and its includes list them. A hidden job and a global keyword
// are no job, and a job that sees no value from the files gives none. A
// trigger job gives the values it passes to its downstream pipeline, none
// when its forward:yaml_variables is false. The env extractor builds it.
func Every(file, variable string, maxBytes int64, parse Parser) (check.Extractor, error) {
	src, err := core.OneFile{File: file, MaxBytes: maxBytes}.Check(kindKey)
	if err != nil {
		return nil, err
	}
	if err := checkVariable(variable); err != nil {
		return nil, err
	}
	return extractor{Source: src, variable: variable, parse: parse, every: true}, nil
}

// checkVariable refuses a variable name GitLab does not accept.
func checkVariable(v string) error {
	if len(v) > maxName || !variableName.MatchString(v) {
		return fmt.Errorf("gitlab-ci: variable %s is no name GitLab accepts: write up to %d letters, digits and underscores", core.Quote(v), maxName)
	}
	if isPsychBool(v) || strings.EqualFold(v, "null") {
		// GitLab's YAML loader reads such a key as a boolean or null,
		// which its variables refuse as a name.
		return fmt.Errorf("gitlab-ci: variable %s is no name GitLab accepts: its YAML loader reads it as a boolean or null", core.Quote(v))
	}
	return nil
}

func checkJob(job string) error {
	switch {
	case job == "":
		return errors.New("gitlab-ci: job is required")
	case len(job) > maxName:
		return fmt.Errorf("gitlab-ci: job %s is %d bytes long, over the limit of %d", core.Quote(job), len(job), maxName)
	case strings.HasPrefix(job, "."):
		return fmt.Errorf("gitlab-ci: job %s is hidden, and no pipeline runs it; name a job that extends it", core.Quote(job))
	case reserved[job]:
		return fmt.Errorf("gitlab-ci: %s is a global keyword of a GitLab CI file, not a job", core.Quote(job))
	}
	return nil
}

// checkKey splits a key into the keyword and the keys below it.
func checkKey(key string) ([]string, error) {
	if len(key) > maxName {
		return nil, fmt.Errorf("gitlab-ci: key %s is %d bytes long, over the limit of %d", core.Quote(key), len(key), maxName)
	}
	parts := strings.Split(key, ".")
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("gitlab-ci: key %s has an empty part; write a keyword such as image, or image.name", core.Quote(key))
		}
	}
	if parts[0] == "variables" {
		return nil, errors.New("gitlab-ci: key variables: write variable: NAME, which applies GitLab's precedence")
	}
	return parts, nil
}

// Place is s with only what decides where it reads: the file cleaned and
// max_bytes left out.
func Place(s Spec) Spec {
	s.OneFile = s.Place()
	return s
}

// extractor is a built gitlab-ci extractor.
type extractor struct {
	core.Source
	job, variable string
	key           []string // the keyword and the keys below it, when variable is ""
	parse         Parser
	every         bool // the variable in every job, as Every builds it
	// valueLimit is the most values Every returns, 0 for core.MaxValues:
	// a test lowers it to witness the bound with a small file, which the
	// mutation run's heap cap holds to 256 MiB. The refusal still names
	// core.MaxValues.
	valueLimit int
}

// maxValues is the most values Every returns: core.MaxValues, or the
// valueLimit a test sets.
func (e extractor) maxValues() int {
	if e.valueLimit == 0 {
		return core.MaxValues
	}
	return e.valueLimit
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(ctx, root)
}

// Extract reads the file and the local files it includes under root, and
// returns the values the job sees.
func (e extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	vs, _, err := e.ExtractRead(ctx, root)
	return vs, err
}

// ExtractRead is Extract, and the bytes of the file and the files it
// includes it read, for the env extractor, whose files hold its max_bytes
// in all, includes too.
func (e extractor) ExtractRead(_ context.Context, root string) ([]check.Value, int64, error) {
	var read int64
	vs, err := e.resolve(func(s core.Source) ([]byte, error) {
		data, err := s.Read(root)
		read += int64(len(data))
		return data, err
	})
	return vs, read, err
}

// resolve builds the configuration from the files read reads, and returns
// the values the job sees.
func (e extractor) resolve(read func(core.Source) ([]byte, error)) ([]check.Value, error) {
	l := &loader{read: read, parse: e.parse, limit: e.Limit, left: e.Limit, info: map[*core.Node]nodeInfo{}}
	top, err := l.load(e.File, false)
	if err != nil {
		return nil, err
	}
	if len(l.unreachable) > 0 {
		more := ""
		if n := len(l.unreachable) - 1; n > 0 {
			more = fmt.Sprintf(", and %d more", n)
		}
		return nil, fmt.Errorf("%s: the job's values may come from %s%s, which errata cannot read offline", e.File, l.unreachable[0], more)
	}
	c := &config{loader: l, top: top, main: e.File, jobs: map[string]resolvedJob{}, resolving: map[string]bool{},
		refs: map[*core.Node]followed{}, following: map[*core.Node]bool{}}
	if err := c.checkDefaults(); err != nil {
		return nil, err
	}
	if e.every {
		c.every = true
		return c.everyJob(e.variable, e.maxValues())
	}
	var cands []candidate
	if e.variable != "" {
		cands, err = c.variable(e.job, e.variable)
	} else {
		cands, err = c.keyword(e.job, e.key)
	}
	if err != nil {
		return nil, err
	}
	return c.values(cands, e.variable != "", e.variable != "" || !scriptKeyword[e.key[0]], e.variable)
}
