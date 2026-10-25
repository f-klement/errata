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
	w, top, jobs, err := e.parseWorkflow(data)
	if err != nil {
		return nil, err
	}
	if e.every {
		return w.everyJob(top, jobs, e.variable, e.maxValues())
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
	m, err := w.jobMatrix(e.job, job)
	if err != nil {
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

// parseWorkflow parses the workflow and returns it with its top mapping
// and its jobs: mapping.
func (e extractor) parseWorkflow(data []byte) (w *workflow, top, jobs *core.Node, err error) {
	if err := e.CheckNodes(data, core.YAMLFormat); err != nil {
		return nil, nil, nil, err
	}
	w = &workflow{file: e.File, limit: e.Limit, info: map[*core.Node]nodeInfo{}, ids: map[string]int{}, templates: map[*core.Node]madeTemplate{}}
	docs, _, err := e.parse(data, 0, func(n *core.Node, tag string, plain bool) {
		w.info[n] = nodeInfo{tag: tag, plain: plain}
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", e.File, err)
	}
	if len(docs) != 1 {
		return nil, nil, nil, fmt.Errorf("%s: holds %d YAML documents; a workflow is one", e.File, len(docs))
	}
	if docs[0].Shape != core.Mapping {
		return nil, nil, nil, fmt.Errorf("%s: line %d: the workflow is %s, not a mapping", e.File, docs[0].Line, shapeName(docs[0]))
	}
	top = docs[0]
	jobs = top.Field["jobs"]
	if jobs == nil || jobs.Shape != core.Mapping {
		return nil, nil, nil, fmt.Errorf("%s: has no jobs: mapping, which a workflow needs", e.File)
	}
	return w, top, jobs, nil
}

// jobMatrix builds the combinations of a job's matrix. A matrix that
// needs an expression GitHub evaluates is kept as the error, since only a
// value that names the matrix needs it, such as ${{ matrix.os }};
// runs-on: ubuntu-latest does not.
func (w *workflow) jobMatrix(name string, job *core.Node) (*jobMatrix, error) {
	m := &jobMatrix{job: name, node: job}
	var expr *expressionError
	var err error
	// ifs, not a switch's cases, which Go's coverage profile leaves outside
	// every block.
	m.combos, err = w.matrix(name, job)
	if errors.As(err, &expr) {
		m.unknown = err
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// everyJob returns the values of the variable name each job sees, its
// env: over the workflow's, and each step whose env: sets it sees, each
// with the job, and the step, as its origin: job "test", or job "test"
// step "build". A step is named by its id, else its name, else its line.
// A job that calls a reusable workflow runs no step of this file, and
// neither the workflow's env: nor its own reaches the called one's, so it
// gives no value. Whatever makes one step's value unknown makes every
// value's, and the values in all keep to core.MaxValues and max_bytes.
func (w *workflow) everyJob(top, jobs *core.Node, name string, limit int) ([]check.Value, error) {
	out := []check.Value{}
	var total int64
	add := func(cands []candidate, m *jobMatrix, origin string) error {
		vs, err := w.values(cands, m)
		if err != nil {
			return err
		}
		for _, v := range vs {
			if len(out) == limit {
				return core.TooManyValues(w.file)
			}
			if total += int64(len(v.Text)); total > w.limit {
				return fmt.Errorf("%s: the values of every job and step hold more than %d bytes in all, the extractor's max_bytes; each job repeats a value the workflow sets, so set max_bytes: higher", w.file, w.limit)
			}
			v.Origin = origin
			out = append(out, v)
		}
		return nil
	}
	for i, jobName := range jobs.Keys {
		job := jobs.Vals[i]
		if job.Shape != core.Mapping {
			return nil, fmt.Errorf("%s: line %d: job %s is %s, not a mapping", w.file, job.Line, core.Quote(jobName), shapeName(job))
		}
		if job.Field["uses"] != nil {
			continue
		}
		m, err := w.jobMatrix(jobName, job)
		if err != nil {
			return nil, err
		}
		origin := "job " + core.Quote(jobName)
		cands, err := w.variable(top, jobName, job, "", nil, nil, name)
		var none *check.NoValue
		if err != nil && !errors.As(err, &none) {
			return nil, err
		}
		if err == nil {
			if err := add(cands, m, origin); err != nil {
				return nil, err
			}
		}
		// The call is a statement of its own: Go's coverage profile leaves
		// code after a statement's first func literal outside every block.
		addStep := func(cands []candidate, step string) error { return add(cands, m, origin+" step "+step) }
		if err := w.everyStep(top, jobName, job, name, addStep); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// everyStep calls add with the candidates of each step of the job whose
// env: sets the variable name, and the step's name for its origin. A step's
// own env: wins over a GITHUB_ENV write before it: actions/runner's
// StepsRunner.RunAsync fills a step's env context from
// Global.EnvironmentVariables, which holds the workflow's and the job's
// env: (JobExtension.InitializeJob) and every GITHUB_ENV write after them
// (FileCommandManager's SetEnvFileCommand), and then merges the step's own
// env: over it ("Evaluate and merge step env"). So such a step's value is
// its own. A step without one sees the job's value, unless a step before it
// may have written GITHUB_ENV: then what it sees is out of reach, and the
// job's value is refused, read once for all the steps before.
func (w *workflow) everyStep(top *core.Node, jobName string, job *core.Node, name string, add func([]candidate, string) error) error {
	list := job.Field["steps"]
	if list == nil {
		return nil
	}
	var flat []*core.Node
	if err := w.flatten(list, &flat); err != nil {
		return err
	}
	labels := stepLabels(flat)
	before := w.newEarlier(top, job, name)
	var stopped error
	for i, s := range flat {
		n, err := w.envOf(s, "step "+labels[i], name)
		if err != nil {
			return err
		}
		if n == nil && stopped != nil {
			return stopped
		}
		if n != nil {
			cands, err := w.variable(top, jobName, job, labels[i], s, nil, name)
			if err != nil {
				return err
			}
			if err := add(cands, labels[i]); err != nil {
				return err
			}
		}
		if stopped == nil {
			stopped = before.step(s)
		}
	}
	return nil
}

// stepLabels names each step for an origin: by its id, or its name when no
// other step of the job has it, quoted, or else by the line it starts at,
// so two steps of one name are told apart.
func stepLabels(flat []*core.Node) []string {
	scalar := func(s *core.Node, k string) *core.Node {
		if v := s.Field[k]; v != nil && v.Shape == core.Scalar {
			return v
		}
		return nil
	}
	names := map[string]int{}
	for _, s := range flat {
		if n := scalar(s, "name"); n != nil {
			names[n.Text]++
		}
	}
	out := make([]string, len(flat))
	for i, s := range flat {
		out[i] = "at line " + strconv.Itoa(s.Line)
		if n := scalar(s, "name"); n != nil && names[n.Text] == 1 {
			out[i] = core.Quote(n.Text)
		}
		if id := scalar(s, "id"); id != nil {
			out[i] = core.Quote(id.Text)
		}
	}
	return out
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
