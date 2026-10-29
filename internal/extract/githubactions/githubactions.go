// Package githubactions is the github-actions extractor: the value one step
// of a GitHub Actions workflow sees of an environment variable, from the
// workflow's, the job's and the step's env:, and the value of a job's or a
// step's key, such as runs-on, uses or a with: input. Each combination of a
// job's matrix is a job of its own, so a value that names the matrix gives
// one value for each, with the combination as its condition. It reads the
// YAML with the parser the facade hands it, since no reader imports another,
// and touches no network: an expression GitHub evaluates, a secret, a
// repository variable and a workflow in another repository are out of reach,
// and the value is not literal or unknown, naming them.
package githubactions

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads one job of a GitHub Actions workflow, and with Step one step
// of it, by its id or its name. Variable names an environment variable,
// and Key a key of the step, or of the job when Step is "", such as
// runs-on, uses or with.node-version; exactly one of them is set.
type Spec struct {
	File     string `json:"file"`
	Job      string `json:"job"`
	Step     string `json:"step,omitempty"`
	Variable string `json:"variable,omitempty"`
	Key      string `json:"key,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// Kind is the github-actions kind, which a rule writes under the key
// github-actions.
var Kind = core.Kind[Spec]{Key: "github-actions", Place: Place, Resolve: Resolve, Describe: Describe}

// Resolve is s with its max_bytes written out, for errata list.
func Resolve(s Spec) Spec {
	s.MaxBytes = core.MaxBytes(s.MaxBytes)
	return s
}

// Describe is what the kind's extractor returns: flat values, some not
// literal, as MayReturnNotLiteral says.
func Describe(Spec) check.Description {
	return check.Description{Label: "github-actions", NotLiteral: true}
}

// Parser parses every YAML document of a file into core's tree, and calls
// note for each node with the tag the file wrote on it, such as !!str, and
// whether it is a plain scalar with no tag. It counts the nodes YAML
// aliases add from expanded against core.MaxExpanded. The facade hands it
// yaml.ParseNoted.
type Parser func(data []byte, expanded int, note func(n *core.Node, tag string, plain bool)) ([]*core.Node, int, error)

// maxName is the longest job, step, variable or key that loads, in bytes.
// A name is compared with the keys and the step names it is looked up
// among, so the bound keeps each comparison a constant cost.
const maxName = 1024

// variableName is the name of a variable a step's shell can read: letters,
// digits and underscores, not starting with a digit, as GitHub's naming
// rules for its configuration variables have it.
var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Build checks s and returns its extractor.
func Build(s Spec, parse Parser) (check.Extractor, error) {
	src, err := core.CheckSource("github-actions", s.File, s.MaxBytes)
	if err != nil {
		return nil, err
	}
	for _, f := range []struct{ what, v string }{{"job", s.Job}, {"step", s.Step}} {
		if len(f.v) > maxName {
			return nil, fmt.Errorf("github-actions: %s %s is %d bytes long, over the limit of %d", f.what, core.Quote(f.v), len(f.v), maxName)
		}
	}
	if s.Job == "" {
		return nil, errors.New("github-actions: job is required; write the job's id, its key under jobs")
	}
	x := extractor{Source: src, job: s.Job, step: s.Step, variable: s.Variable, parse: parse}
	switch {
	case (s.Variable == "") == (s.Key == ""):
		return nil, errors.New("github-actions: set exactly one of variable, for an environment variable, and key, for a key of the step or the job such as runs-on")
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

// Every builds the extractor of the environment variable variable across
// every job of the workflow file, and every step whose env: sets it: the
// values each sees, as Build's for that job or step, with the job, and the
// step, as each value's origin. The env extractor builds it.
func Every(file, variable string, maxBytes int64, parse Parser) (check.Extractor, error) {
	src, err := core.CheckSource("github-actions", file, maxBytes)
	if err != nil {
		return nil, err
	}
	if err := checkVariable(variable); err != nil {
		return nil, err
	}
	return extractor{Source: src, variable: variable, parse: parse, every: true}, nil
}

// checkVariable refuses a name no step's env: can set.
func checkVariable(v string) error {
	if len(v) > maxName || !variableName.MatchString(v) {
		return fmt.Errorf("github-actions: variable %s is no variable name: write up to %d letters, digits and underscores, not starting with a digit", core.Quote(v), maxName)
	}
	// "You can't overwrite the value of the default environment variables
	// named GITHUB_* and RUNNER_*" (Variables reference).
	if up := strings.ToUpper(v); strings.HasPrefix(up, "GITHUB_") || strings.HasPrefix(up, "RUNNER_") {
		return fmt.Errorf("github-actions: variable %s is one the runner sets, and GitHub ignores an env: that sets a GITHUB_ or RUNNER_ name", core.Quote(v))
	}
	return nil
}

// checkKey splits a key into its parts.
func checkKey(key string) ([]string, error) {
	if len(key) > maxName {
		return nil, fmt.Errorf("github-actions: key %s is %d bytes long, over the limit of %d", core.Quote(key), len(key), maxName)
	}
	parts := strings.Split(key, ".")
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("github-actions: key %s has an empty part; write a key such as runs-on, or with.node-version", core.Quote(key))
		}
	}
	if parts[0] == "env" {
		return nil, errors.New("github-actions: key env: write variable: NAME, which applies GitHub's precedence of step over job over workflow")
	}
	return parts, nil
}

// Place is s with only what decides where it reads: the file cleaned and
// max_bytes left out.
func Place(s Spec) Spec {
	s.File, s.MaxBytes = filepath.Clean(s.File), 0
	return s
}

// extractor is a built github-actions extractor.
type extractor struct {
	core.Source
	job, step, variable string
	key                 []string // the key's parts, when variable is ""
	parse               Parser
	every               bool // the variable in every job and step, as Every builds it
	// valueLimit is the most values Every returns, 0 for core.MaxValues:
	// a test lowers it to witness the bound with a small file. The refusal
	// still names core.MaxValues.
	valueLimit int
}

// MayReturnNotLiteral reports true: a ${{ }} expression, and a number YAML
// retypes, such as 3.10, are values GitHub computes.
func (extractor) MayReturnNotLiteral() bool { return true }

// maxValues is the most values Every returns: core.MaxValues, or the
// valueLimit a test sets.
func (e extractor) maxValues() int {
	if e.valueLimit == 0 {
		return core.MaxValues
	}
	return e.valueLimit
}

// Extract reads the workflow and returns the values the step, or the job,
// sees.
func (e extractor) Extract(root string) ([]check.Value, error) {
	data, err := e.Read(root)
	if err != nil {
		return nil, err
	}
	return e.fromData(data)
}
