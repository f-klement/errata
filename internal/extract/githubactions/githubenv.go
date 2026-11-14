package githubactions

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// namesGitHubEnv matches text that names the file GITHUB_ENV, in any case,
// since PowerShell reads $env:github_env as the variable: the variable
// itself, and the github context's env, "The path on the runner to the
// file that sets environment variables from workflow commands" (Contexts
// reference), written as a property or as an index, github['env'].
var namesGitHubEnv = regexp.MustCompile(`(?i)github_env|github\.env\b|github\s*\[\s*'env'\s*\]`)

// quietActions are the actions errata knows to set no environment
// variable for the steps after them, by the name before the @. Each
// action's action.yml declares only outputs, and its source, read at the
// commit given, calls no core.exportVariable and names no GITHUB_ENV:
//
//   - actions/checkout (f548e57, 2026-07-20): outputs ref and commit.
//   - actions/cache, with its restore and save actions (3edfce9,
//     2026-07-15): outputs cache-hit, cache-primary-key and
//     cache-matched-key, and keeps its state with core.saveState.
//   - actions/upload-artifact (cf430e0, 2026-10-07): outputs artifact-id,
//     artifact-url and artifact-digest.
//   - actions/download-artifact (9000827, 2026-10-07): outputs
//     download-path.
//
// A later version of one of them could start to export a variable, which
// errata would not see; docs/reference.md#github-actions says so.
var quietActions = map[string]bool{
	"actions/checkout":          true,
	"actions/cache":             true,
	"actions/cache/restore":     true,
	"actions/cache/save":        true,
	"actions/upload-artifact":   true,
	"actions/download-artifact": true,
}

// quiet reports whether the action a step's uses: names is one of
// quietActions. GitHub's owner and repository names are ASCII and case
// insensitive, so only ASCII capitals fold.
// A local action (./path) and a Docker one (docker://) are not.
func quiet(uses string) bool {
	name, _, _ := strings.Cut(uses, "@")
	return quietActions[fold(name)]
}

// earlier refuses a variable that a step before the one read may set as
// the job runs. "You can make an environment variable available to any
// subsequent steps in a workflow job by defining or updating the
// environment variable and writing this to the GITHUB_ENV environment
// file." (Workflow commands.) What a script writes there is known only
// once it runs, so a run: step before this one whose script, shell or env:
// names the file makes the value unknown, and so does an action not on
// quietActions, since an action writes the same file with
// core.exportVariable. Each node is read once: an alias repeats a script
// without its bytes.
func (w *workflow) earlier(top, j *core.Node, before []*core.Node, name string) error {
	e := w.newEarlier(top, j, name)
	for _, s := range before {
		if err := e.step(s); err != nil {
			return err
		}
	}
	return nil
}

// earlierSteps is earlier's state over the steps of one job, one step at a
// time, so Every reads each step once for every step after it.
type earlierSteps struct {
	w                      *workflow
	name                   string
	seen                   map[*core.Node]bool
	defaultShell           *core.Node
	defaultWhat, levelWhat string
	levelEnv               *core.Node
}

func (w *workflow) newEarlier(top, j *core.Node, name string) *earlierSteps {
	e := &earlierSteps{w: w, name: name, seen: map[*core.Node]bool{}}
	e.defaultShell, e.defaultWhat = shellDefault(j), "the job's"
	if e.defaultShell == nil {
		e.defaultShell, e.defaultWhat = shellDefault(top), "the workflow's"
	}
	// The job's, its container's and the workflow's env: reach every run:
	// step's script, so a value there that names the file hands it to each
	// of them.
	e.levelEnv, e.levelWhat = e.envNames(j), "the job's env:"
	// The run: scripts of a job with a container run in it, with its env:.
	if c := j.Field["container"]; e.levelEnv == nil && c != nil && c.Shape == core.Mapping {
		e.levelEnv, e.levelWhat = e.envNames(c), "the job's container.env:"
	}
	if e.levelEnv == nil {
		e.levelEnv, e.levelWhat = e.envNames(top), "the workflow's env:"
	}
	return e
}

// envNames is the first value of holder's env: that names GITHUB_ENV, or
// nil: holder is a step, a job, a job's container or the workflow. An
// env: is read once.
func (e *earlierSteps) envNames(holder *core.Node) *core.Node {
	env := holder.Field["env"]
	if env == nil || env.Shape != core.Mapping || e.seen[env] {
		return nil
	}
	e.seen[env] = true
	for _, v := range env.Vals {
		if e.names(v) {
			return v
		}
	}
	return nil
}

// names reports whether n is a scalar that names GITHUB_ENV, reading each
// node once.
func (e *earlierSteps) names(n *core.Node) bool {
	if n == nil || e.seen[n] {
		return false
	}
	e.seen[n] = true
	return n.Shape == core.Scalar && namesGitHubEnv.MatchString(n.Text)
}

// step refuses the variable for the steps after s when s may set it
// through GITHUB_ENV.
func (e *earlierSteps) step(s *core.Node) error {
	if uses := s.Field["uses"]; uses != nil && s.Field["run"] == nil {
		return e.action(s, uses)
	}
	run := s.Field["run"]
	if run == nil {
		return nil
	}
	found, where := e.inRun(s, run)
	return e.refuse(s, found, where)
}

// action refuses the variable after the step s, which runs the action
// uses, unless the action is one known to set no variable.
func (e *earlierSteps) action(s, uses *core.Node) error {
	if uses.Shape == core.Scalar && quiet(uses.Text) {
		return nil
	}
	return fmt.Errorf("%s:%d: %s runs the action %s, which can set %s for the steps after it through GITHUB_ENV; errata reads no action's code, and this action is not on its list of those that set none",
		e.w.file, uses.Line, describeStep(s), core.Quote(uses.Text), e.name)
}

// inRun is the node of the run: step s that names GITHUB_ENV, and where
// in the step it is: its run:, its shell, or its env:. It is nil when none
// does.
func (e *earlierSteps) inRun(s, run *core.Node) (*core.Node, string) {
	shell, shellWhat := s.Field["shell"], "its shell:"
	if shell == nil {
		shell, shellWhat = e.defaultShell, "its shell, "+e.defaultWhat+" defaults.run.shell"
	}
	switch {
	case e.names(run):
		return run, "its run:"
	case e.names(shell):
		return shell, shellWhat
	}
	return e.envNames(s), "its env:"
}

// refuse is the refusal of the variable after the run: step s, when found
// in it, or an env: of its job or workflow, names GITHUB_ENV.
func (e *earlierSteps) refuse(s, found *core.Node, where string) error {
	w, name := e.w, e.name
	if found == nil && e.levelEnv != nil {
		return fmt.Errorf("%s:%d: %s names GITHUB_ENV and reaches the run: of %s, which can set %s for the steps after it as the job runs; errata cannot read what it writes",
			w.file, e.levelEnv.Line, e.levelWhat, describeStep(s), name)
	}
	if found != nil {
		return fmt.Errorf("%s:%d: %s names GITHUB_ENV in %s, which can set %s for the steps after it as the job runs; errata cannot read what it writes",
			w.file, found.Line, describeStep(s), where, name)
	}
	return nil
}

// shellDefault is the defaults.run.shell of a job or the workflow, or nil.
func shellDefault(holder *core.Node) *core.Node {
	d := holder.Field["defaults"]
	if d == nil || d.Shape != core.Mapping {
		return nil
	}
	r := d.Field["run"]
	if r == nil || r.Shape != core.Mapping {
		return nil
	}
	return r.Field["shell"]
}
