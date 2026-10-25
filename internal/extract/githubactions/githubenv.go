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
// errata would not see; the README says so.
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
	seen := map[*core.Node]bool{}
	names := func(n *core.Node) bool {
		if n == nil || seen[n] {
			return false
		}
		seen[n] = true
		return n.Shape == core.Scalar && namesGitHubEnv.MatchString(n.Text)
	}
	defaultShell, defaultWhat := shellDefault(j), "the job's"
	if defaultShell == nil {
		defaultShell, defaultWhat = shellDefault(top), "the workflow's"
	}
	// The job's, its container's and the workflow's env: reach every run:
	// step's script, so a value there that names the file hands it to each
	// of them.
	envNames := func(holder *core.Node) *core.Node {
		env := holder.Field["env"]
		if env == nil || env.Shape != core.Mapping || seen[env] {
			return nil
		}
		seen[env] = true
		for _, v := range env.Vals {
			if names(v) {
				return v
			}
		}
		return nil
	}
	levelEnv, levelWhat := envNames(j), "the job's env:"
	// The run: scripts of a job with a container run in it, with its env:.
	if c := j.Field["container"]; levelEnv == nil && c != nil && c.Shape == core.Mapping {
		levelEnv, levelWhat = envNames(c), "the job's container.env:"
	}
	if levelEnv == nil {
		levelEnv, levelWhat = envNames(top), "the workflow's env:"
	}
	for _, s := range before {
		if uses := s.Field["uses"]; uses != nil && s.Field["run"] == nil {
			if uses.Shape == core.Scalar && quiet(uses.Text) {
				continue
			}
			return fmt.Errorf("%s:%d: %s runs the action %s, which can set %s for the steps after it through GITHUB_ENV; errata reads no action's code, and this action is not on its list of those that set none",
				w.file, uses.Line, describeStep(s), core.Quote(uses.Text), name)
		}
		run := s.Field["run"]
		if run == nil {
			continue
		}
		shell, shellWhat := s.Field["shell"], "its shell:"
		if shell == nil {
			shell, shellWhat = defaultShell, "its shell, "+defaultWhat+" defaults.run.shell"
		}
		found, where := (*core.Node)(nil), ""
		switch {
		case names(run):
			found, where = run, "its run:"
		case names(shell):
			found, where = shell, shellWhat
		default:
			if env := s.Field["env"]; env != nil && env.Shape == core.Mapping && !seen[env] {
				seen[env] = true
				for _, v := range env.Vals {
					if names(v) {
						found, where = v, "its env:"
						break
					}
				}
			}
		}
		if found == nil && levelEnv != nil {
			return fmt.Errorf("%s:%d: %s names GITHUB_ENV and reaches the run: of %s, which can set %s for the steps after it as the job runs; errata cannot read what it writes",
				w.file, levelEnv.Line, levelWhat, describeStep(s), name)
		}
		if found != nil {
			return fmt.Errorf("%s:%d: %s names GITHUB_ENV in %s, which can set %s for the steps after it as the job runs; errata cannot read what it writes",
				w.file, found.Line, describeStep(s), where, name)
		}
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
