// Package check holds the domain model: a check, its scope and its result.
// It imports nothing from the rest of errata, so every other package can
// depend on it without a cycle.
package check

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Scope says what a check needs in order to run. See GLOSSARY.md.
type Scope string

// The three scopes, in the order of what they need: nothing, read access,
// read access plus a dry run.
const (
	ScopeRepo    Scope = "repo"
	ScopeLive    Scope = "live"
	ScopeApplied Scope = "applied"
)

// known reports whether s is one of the three scopes.
func (s Scope) known() bool {
	switch s {
	case ScopeRepo, ScopeLive, ScopeApplied:
		return true
	default:
		return false
	}
}

// Check is one test of one claim. It is a program check when Run is set and a
// rule when Rule is set; Validate refuses both and neither.
type Check struct {
	ID     string
	Reason string
	Scope  Scope
	// Run is the command of a check-protocol check: argv, not a shell string,
	// so a path with spaces needs no quoting.
	Run     []string
	Timeout time.Duration
	Rule    *Rule
}

// IsRule reports whether c is a rule rather than a program check.
func (c Check) IsRule() bool { return c.Rule != nil }

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate returns every problem with c joined into one error. A check that
// fails validation does not load.
func (c Check) Validate() error {
	var errs []error
	if !idPattern.MatchString(c.ID) {
		errs = append(errs, fmt.Errorf("id %q must be lower-case words joined by single hyphens", c.ID))
	}
	if strings.TrimSpace(c.Reason) == "" {
		errs = append(errs, errors.New("reason is required: say why the check exists and what breaks without it"))
	}
	if !c.Scope.known() {
		errs = append(errs, fmt.Errorf("scope %q must be one of repo, live, applied", c.Scope))
	}
	switch {
	case c.Rule != nil:
		errs = append(errs, c.Rule.validate(c)...)
	case len(c.Run) == 0:
		errs = append(errs, errors.New("run or extract is required: a program check has run, a rule has extract"))
	}
	if c.Timeout < 0 {
		errs = append(errs, errors.New("timeout must not be negative"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("check %q: %w", c.ID, errors.Join(errs...))
	}
	return nil
}

// Verdict is the result of a check that ran.
type Verdict string

const (
	// VerdictTrue means the claim holds.
	VerdictTrue Verdict = "true"
	// VerdictFalse means the claim is false. It goes into the errata.
	VerdictFalse Verdict = "false"
	// VerdictUnknown means the check could not evaluate. It is not an
	// erratum, since the claim was not shown false, but it fails the run: a
	// check that fails open lets a suite go green on a subset.
	VerdictUnknown Verdict = "unknown"
)

// Result is what one check produced. Exactly one of Verdict and Gap is set:
// a skipped check has no verdict, only its gap in the coverage.
type Result struct {
	Check    Check
	Verdict  Verdict
	Message  string
	Gap      string
	Duration time.Duration
	// Extracted holds the raw values a rule evaluated, one entry per
	// extractor that ran without an error, in the order the rule ran them.
	// Rule.Normalized gives the form they were compared in. A program check
	// leaves it empty.
	Extracted []Extracted
}

// Skipped reports whether the check did not run.
func (r Result) Skipped() bool { return r.Gap != "" }

// Failed reports whether r fails the run: an erratum or an unknown.
func (r Result) Failed() bool { return r.Verdict == VerdictFalse || r.Verdict == VerdictUnknown }

// Report is the output of one run.
type Report struct {
	Results []Result
}

// Errata returns the results whose claim the check showed to be false.
func (r Report) Errata() []Result { return r.with(VerdictFalse) }

// Unknowns returns the results of checks that could not evaluate.
func (r Report) Unknowns() []Result { return r.with(VerdictUnknown) }

// Failed reports whether the run fails: any erratum or any unknown.
func (r Report) Failed() bool { return len(r.Errata())+len(r.Unknowns()) > 0 }

func (r Report) with(v Verdict) []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Verdict == v {
			out = append(out, res)
		}
	}
	return out
}

// Coverage counts the checks that ran and the checks that were skipped.
func (r Report) Coverage() (ran, skipped int) {
	for _, res := range r.Results {
		if res.Skipped() {
			skipped++
		} else {
			ran++
		}
	}
	return ran, skipped
}
