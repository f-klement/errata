package githubactions

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// nodeInfo is what the tree leaves out of a node that the extractor needs:
// the tag the file wrote on it, and whether it is a plain scalar with no
// tag.
type nodeInfo struct {
	tag   string
	plain bool
}

// workflow is one parsed workflow file and what the extractor has spent on
// it.
type workflow struct {
	file  string
	limit int64 // max_bytes, which the values hold at most in all
	info  map[*core.Node]nodeInfo
	work  int            // the steps the matrix took to build and read
	ids   map[string]int // each matrix key's id, by its folded name
	held  int            // the jobs the last matrix built held at its end, which a test reads
	// templates are the values with expressions read in a matrix job
	templates map[*core.Node]madeTemplate
}

// scope is the level a value is set at. The matrix context is a job's, so
// a workflow's env: cannot name it.
type scope int

const (
	inWorkflow scope = iota
	inJob
)

// candidate is a node whose value the step or the job sees, and the level
// it is set at.
type candidate struct {
	n  *core.Node
	in scope
}

// fromData resolves the values from the workflow file's bytes.
func (e extractor) fromData(data []byte) ([]check.Value, error) {
	if err := e.CheckNodes(data, core.YAMLFormat); err != nil {
		return nil, err
	}
	w := &workflow{file: e.File, limit: e.Limit, info: map[*core.Node]nodeInfo{}, ids: map[string]int{}, templates: map[*core.Node]madeTemplate{}}
	docs, _, err := e.parse(data, 0, func(n *core.Node, tag string, plain bool) {
		w.info[n] = nodeInfo{tag: tag, plain: plain}
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.File, err)
	}
	switch {
	case len(docs) != 1:
		return nil, fmt.Errorf("%s: holds %d YAML documents; a workflow is one", e.File, len(docs))
	case docs[0].Shape != core.Mapping:
		return nil, fmt.Errorf("%s: line %d: the workflow is %s, not a mapping", e.File, docs[0].Line, shapeName(docs[0]))
	}
	top := docs[0]
	jobs := top.Field["jobs"]
	if jobs == nil || jobs.Shape != core.Mapping {
		return nil, fmt.Errorf("%s: has no jobs: mapping, which a workflow needs", e.File)
	}
	job := jobs.Field[e.job]
	switch {
	case job == nil:
		return nil, fmt.Errorf("%s: jobs holds no job %s", e.File, core.Quote(e.job))
	case job.Shape != core.Mapping:
		return nil, fmt.Errorf("%s: line %d: job %s is %s, not a mapping", e.File, job.Line, core.Quote(e.job), shapeName(job))
	}
	if calls := job.Field["uses"]; calls != nil && (e.step != "" || e.variable != "") {
		return nil, w.called(e.job, calls)
	}
	var step *core.Node
	var before []*core.Node
	if e.step != "" {
		if step, before, err = w.findStep(e.job, job, e.step); err != nil {
			return nil, err
		}
	}
	m := &jobMatrix{job: e.job, node: job}
	var expr *expressionError
	switch m.combos, err = w.matrix(e.job, job); {
	case errors.As(err, &expr):
		// Only a value that names the matrix needs it, such as
		// ${{ matrix.os }}; runs-on: ubuntu-latest does not.
		m.unknown = err
	case err != nil:
		return nil, err
	}
	var cands []candidate
	if e.variable != "" {
		cands, err = w.variable(top, e.job, job, e.step, step, before, e.variable)
	} else {
		holder := job
		if step != nil {
			holder = step
		}
		cands, err = w.key(holder, e.key)
	}
	if err != nil {
		return nil, err
	}
	return w.values(cands, m)
}

// called is the refusal of a step or a variable of a job that calls a
// reusable workflow: the called workflow's steps run with its own env:,
// and "Any environment variables set in an env context defined at the
// workflow level in the caller workflow are not propagated to the called
// workflow" (Reusing workflow configurations).
func (w *workflow) called(job string, uses *core.Node) error {
	at := fmt.Sprintf("%s:%d", w.file, uses.Line)
	text := uses.Text
	if strings.HasPrefix(text, "./") || strings.HasPrefix(text, "$/") {
		return fmt.Errorf("%s: job %s calls the workflow %s, whose steps run with its own env:, which the caller's does not reach; read that file with file: instead",
			at, core.Quote(job), core.Quote(text))
	}
	return fmt.Errorf("%s: job %s calls the reusable workflow %s, from another repository, which errata cannot read offline",
		at, core.Quote(job), core.Quote(text))
}

// findStep returns the step whose id is want, or else the one step whose
// name is want, and the steps listed before it. A parallel: group's steps
// are steps of the job, in their order.
func (w *workflow) findStep(job string, j *core.Node, want string) (*core.Node, []*core.Node, error) {
	list := j.Field["steps"]
	if list == nil {
		return nil, nil, fmt.Errorf("%s: job %s has no steps", w.file, core.Quote(job))
	}
	var flat []*core.Node
	if err := w.flatten(list, &flat); err != nil {
		return nil, nil, err
	}
	byName, again := -1, -1
	var templated *core.Node
	for i, s := range flat {
		if id := s.Field["id"]; id != nil && id.Shape == core.Scalar && id.Text == want {
			return s, flat[:i], nil
		}
		name := s.Field["name"]
		if name == nil || name.Shape != core.Scalar {
			continue
		}
		switch {
		case name.Text == want && byName < 0:
			byName = i
		case name.Text == want && again < 0:
			again = i
		case templated == nil && strings.Contains(name.Text, "${{"):
			templated = name
		}
	}
	switch {
	case again >= 0:
		return nil, nil, fmt.Errorf("%s: lines %d and %d: job %s has two steps named %s; give the step an id: and name that",
			w.file, flat[byName].Line, flat[again].Line, core.Quote(job), core.Quote(want))
	case byName < 0 && templated != nil:
		return nil, nil, fmt.Errorf("%s: job %s has no step whose id or name is %s; a name that holds an expression, such as %s at line %d, matches only as written",
			w.file, core.Quote(job), core.Quote(want), core.Quote(templated.Text), templated.Line)
	case byName < 0:
		return nil, nil, fmt.Errorf("%s: job %s has no step whose id or name is %s", w.file, core.Quote(job), core.Quote(want))
	}
	return flat[byName], flat[:byName], nil
}

// flatten appends the steps of list to flat, each parallel: group's in its
// place.
func (w *workflow) flatten(list *core.Node, flat *[]*core.Node) error {
	if list.Shape != core.Sequence {
		return fmt.Errorf("%s: line %d: steps: is %s, not a sequence", w.file, list.Line, shapeName(list))
	}
	for _, s := range list.Items {
		if s.Shape != core.Mapping {
			return fmt.Errorf("%s: line %d: a step is %s, not a mapping", w.file, s.Line, shapeName(s))
		}
		if group := s.Field["parallel"]; group != nil {
			if err := w.flatten(group, flat); err != nil {
				return err
			}
			continue
		}
		*flat = append(*flat, s)
	}
	return nil
}

// variable returns where the step, or the job when step is nil, gets the
// variable name. "When more than one environment variable is defined with
// the same name, GitHub uses the most specific variable. For example, an
// environment variable defined in a step will override job and workflow
// environment variables with the same name, while the step executes. An
// environment variable defined for a job will override a workflow variable
// with the same name, while the job executes." (Workflow syntax, env.)
func (w *workflow) variable(top *core.Node, job string, j *core.Node, stepName string, step *core.Node, before []*core.Node, name string) ([]candidate, error) {
	if err := w.earlier(top, j, before, name); err != nil {
		return nil, err
	}
	// The container's env: is set on the container the steps run in.
	// GitHub documents no order between it and the env: of the levels, so
	// it decides only when none of them sets the variable.
	var inContainer *core.Node
	if c := j.Field["container"]; c != nil && c.Shape == core.Mapping {
		n, err := w.envOf(c, "job "+core.Quote(job)+"'s container", name)
		if err != nil {
			return nil, err
		}
		inContainer = n
	}
	levels := []struct {
		holder *core.Node
		what   string
		in     scope
	}{{step, "step " + core.Quote(stepName), inJob}, {j, "job " + core.Quote(job), inJob}, {top, "the workflow", inWorkflow}}
	var looked []string
	for _, l := range levels {
		if l.holder == nil {
			continue
		}
		looked = append(looked, l.what)
		n, err := w.envOf(l.holder, l.what, name)
		switch {
		case err != nil:
			return nil, err
		case n != nil && inContainer != nil:
			return nil, fmt.Errorf("%s:%d: the env: of %s and the job's container.env at line %d both set %s, and GitHub documents no order between them",
				w.file, n.Line, l.what, inContainer.Line, name)
		case n != nil:
			return []candidate{{n, l.in}}, nil
		}
	}
	if inContainer != nil {
		return []candidate{{inContainer, inJob}}, nil
	}
	if j.Field["container"] != nil {
		looked = append(looked, "its container")
	}
	return nil, &check.NoValue{Reason: fmt.Sprintf("no env: of %s sets %s; it may come from a step before that writes GITHUB_ENV, from an action, from the runner, or from the repository's variables and secrets, which errata cannot read",
		strings.Join(looked, ", "), name)}
}

// envOf returns the value holder's env: gives name, or nil.
func (w *workflow) envOf(holder *core.Node, what, name string) (*core.Node, error) {
	env := holder.Field["env"]
	switch {
	case env == nil:
		return nil, nil
	case env.Shape == core.Scalar && strings.Contains(env.Text, "${{"):
		return nil, fmt.Errorf("%s:%d: the env: of %s is an expression, which GitHub evaluates as the workflow runs", w.file, env.Line, what)
	case env.Shape != core.Mapping:
		return nil, fmt.Errorf("%s:%d: the env: of %s is %s, not a mapping", w.file, env.Line, what, shapeName(env))
	}
	for i, k := range env.Keys {
		if k != name && strings.EqualFold(k, name) {
			// A Windows runner reads names in any case as one variable,
			// so which of the two the step sees hangs on the runner.
			return nil, fmt.Errorf("%s:%d: the env: of %s sets %s, which a Windows runner reads as %s", w.file, env.Vals[i].Line, what, core.Quote(k), name)
		}
	}
	n := env.Field[name]
	if n != nil && n.Shape != core.Scalar {
		return nil, fmt.Errorf("%s:%d: %s in the env: of %s is %s; a variable's value is a string", w.file, n.Line, name, what, shapeName(n))
	}
	return n, nil
}

// describeStep names a step for a message: by its id, or its name, or the
// line it starts at.
func describeStep(s *core.Node) string {
	for _, k := range []string{"id", "name"} {
		if v := s.Field[k]; v != nil && v.Shape == core.Scalar {
			return "the step " + core.Quote(v.Text)
		}
	}
	return "the step at line " + strconv.Itoa(s.Line)
}

// key returns the node of holder that the key's parts select, or none when
// the file does not set it.
func (w *workflow) key(holder *core.Node, parts []string) ([]candidate, error) {
	n := holder
	for _, p := range parts {
		if n.Shape != core.Mapping {
			return nil, fmt.Errorf("%s:%d: the key's part before %s is %s, not a mapping", w.file, n.Line, core.Quote(p), shapeName(n))
		}
		if n = n.Field[p]; n == nil {
			return nil, nil
		}
	}
	return []candidate{{n, inJob}}, nil
}

// values writes the candidates as values: a scalar's, or each item of a
// sequence of scalars (envOf refuses a variable that is not a scalar), once for each combination of the matrix when it
// names the matrix.
func (w *workflow) values(cands []candidate, m *jobMatrix) ([]check.Value, error) {
	var out []check.Value
	var total int64
	add := func(v check.Value) error {
		if len(out) == core.MaxValues {
			return core.TooManyValues(w.file)
		}
		if total += int64(len(v.Text)); total > w.limit {
			return fmt.Errorf("%s: the values hold more than %d bytes in all, the extractor's max_bytes; a matrix repeats a value for each of its jobs, so set max_bytes: higher", w.file, w.limit)
		}
		out = append(out, v)
		return nil
	}
	for _, cd := range cands {
		n := cd.n
		var items []*core.Node
		switch n.Shape {
		case core.Scalar:
			items = []*core.Node{n}
		case core.Sequence:
			items = n.Items
		default:
			return nil, fmt.Errorf("%s:%d: the key selects a mapping, not a value; name a key below it, such as with.node-version", w.file, n.Line)
		}
		for _, it := range items {
			if it.Shape != core.Scalar {
				return nil, fmt.Errorf("%s:%d: the list holds %s; name a key below it", w.file, it.Line, shapeName(it))
			}
			if err := w.expand(it, cd.in, m, add); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func shapeName(n *core.Node) string {
	if n.Shape == core.Scalar {
		return "a scalar"
	}
	return n.Shape.String()
}
