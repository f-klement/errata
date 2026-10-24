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
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads one job of a GitLab CI file. Variable names a CI/CD variable,
// and Key a job keyword, such as image or script, or a key below one, such
// as image.name; exactly one of them is set.
type Spec struct {
	File     string `json:"file"`
	Job      string `json:"job"`
	Variable string `json:"variable,omitempty"`
	Key      string `json:"key,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
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
	src, err := core.CheckSource("gitlab-ci", s.File, s.MaxBytes)
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
		if len(s.Variable) > maxName || !variableName.MatchString(s.Variable) {
			return nil, fmt.Errorf("gitlab-ci: variable %s is no name GitLab accepts: write up to %d letters, digits and underscores", core.Quote(s.Variable), maxName)
		}
		if isPsychBool(s.Variable) || strings.EqualFold(s.Variable, "null") {
			// GitLab's YAML loader reads such a key as a boolean or
			// null, which its variables refuse as a name.
			return nil, fmt.Errorf("gitlab-ci: variable %s is no name GitLab accepts: its YAML loader reads it as a boolean or null", core.Quote(s.Variable))
		}
	default:
		if x.key, err = checkKey(s.Key); err != nil {
			return nil, err
		}
	}
	return x, nil
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
	s.File, s.MaxBytes = filepath.Clean(s.File), 0
	return s
}

// extractor is a built gitlab-ci extractor.
type extractor struct {
	core.Source
	job, variable string
	key           []string // the keyword and the keys below it, when variable is ""
	parse         Parser
}

// MayReturnNotLiteral reports true: a variable that names another, such as
// $CI_COMMIT_SHA, and a number YAML retypes, such as 3.10, are values GitLab
// computes.
func (extractor) MayReturnNotLiteral() bool { return true }

// Extract reads the file and the local files it includes under root, and
// returns the values the job sees.
func (e extractor) Extract(root string) ([]check.Value, error) {
	return e.resolve(func(s core.Source) ([]byte, error) { return s.Read(root) })
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
