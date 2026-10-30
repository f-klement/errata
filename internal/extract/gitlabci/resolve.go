package gitlabci

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// maxExtends is how many levels of extends a job may inherit through: "The
// extends keyword supports up to eleven levels of inheritance".
const maxExtends = 11

// maxReference is how deeply !reference tags may nest: "You can nest
// !reference tags up to 10 levels deep". The same bound holds for arrays
// nested in a script, which GitLab flattens.
const maxReference = 10

// maxInstances is the most jobs one parallel:matrix may make: "The matrix
// can generate a maximum of 200 jobs" (parallel:matrix reference).
const maxInstances = 200

// defaultKeywords are the keywords default: sets for every job that does
// not set its own, from the default keyword's reference.
var defaultKeywords = map[string]bool{
	"after_script": true, "artifacts": true, "before_script": true, "cache": true, "hooks": true,
	"id_tokens": true, "image": true, "interruptible": true, "retry": true, "services": true,
	"tags": true, "timeout": true,
}

// topLevelDefaults are the keywords GitLab still reads at the top level of
// the file as defaults, deprecated in favour of default:.
var topLevelDefaults = []string{"image", "services", "cache", "before_script", "after_script"}

// scriptKeyword are the keywords whose text the job's shell runs: "$X" there
// is the command as the job runs it, which the shell expands, so it is
// literal text.
var scriptKeyword = map[string]bool{"script": true, "before_script": true, "after_script": true}

// defaultStages are the stages of a file that names none: "If stages is
// not defined in the .gitlab-ci.yml file, the default pipeline stages are:
// .pre, build, test, deploy, .post" (stages reference).
var defaultStages = []string{"build", "test", "deploy"}

// config is the merged configuration of a file and its local includes.
type config struct {
	*loader
	top       *core.Node
	main      string
	jobs      map[string]resolvedJob // each job with its extends merged in
	resolving map[string]bool        // the jobs whose extends are being merged
	refs      map[*core.Node]followed
	following map[*core.Node]bool // the !reference tags being followed
	condBytes int64               // the bytes of the candidates' conditions so far
	// every is Every's reading: a trigger job's variables are the values it
	// passes downstream, where a spec that names one is refused, and the
	// dotenv reports of earlier stages are read once for all jobs, where a
	// spec's one job walks the jobs before it, as it always has, so a Build
	// spends the work budget on no job it did not read before.
	every  bool
	staged *stagedReports // every job's stage and dotenv report, once read
}

// everyJob returns the values each job sees of the variable name, each
// with its job as its origin, at most limit and max_bytes of them in all.
func (c *config) everyJob(name string, limit int) ([]check.Value, error) {
	out := []check.Value{}
	var total int64
	for _, job := range c.top.Keys {
		if reserved[job] || strings.HasPrefix(job, ".") {
			continue
		}
		cands, err := c.variable(job, name)
		if err != nil {
			return nil, err
		}
		vs, err := c.values(cands, true, true, name)
		var none *check.NoValue
		if errors.As(err, &none) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			if len(out) == limit {
				return nil, core.TooManyValues(c.main)
			}
			if total += int64(len(v.Text)); total > c.limit {
				return nil, fmt.Errorf("%s: the values of every job hold more than %d bytes in all, the extractor's max_bytes; a job repeats each value it inherits, so set max_bytes: higher", c.main, c.limit)
			}
			v.Origin = "job " + core.Quote(job)
			out = append(out, v)
		}
	}
	return out, nil
}

// resolvedJob is a job with its extends merged in, the levels of extends
// below it, and the levels of !reference its own value took to resolve, so
// a chain that reaches it again from deeper is held to the same limits.
type resolvedJob struct {
	n        *core.Node
	ext, ref int
}

// followed is what a !reference selects, and the levels of !reference it
// took to get there, itself counted.
type followed struct {
	n      *core.Node
	levels int
}

// varFrom says where a variable's candidate came from, which decides the
// forms its value may take.
type varFrom int

const (
	fromTop varFrom = iota
	fromJob
	fromRule
	fromMatrix
)

// candidate is one value a job may see: a node, or none when nothing in the
// files sets it, with the condition under which the job sees it, "" for
// always. A value set by a rule holds only when that rule matches, and one
// set by a parallel:matrix instance only in that instance, so a job may see
// one of several.
type candidate struct {
	n    *core.Node // nil: no file sets it
	cond string
	from varFrom
}

// fileOf is the file a node came from. A node the merge made has none, and
// its parts name their own.
func (c *config) fileOf(n *core.Node) string {
	if f := c.info[n].file; f != "" {
		return f
	}
	return c.main
}

// at names n's place for a message.
func (c *config) at(n *core.Node) string {
	return fmt.Sprintf("%s:%d", c.fileOf(n), n.Line)
}

// checkDefaults refuses a keyword set both at the top level and under
// default:, as GitLab does: "is defined in top-level and default: entry".
func (c *config) checkDefaults() error {
	def, err := c.field(c.top, "default")
	if err != nil || def == nil {
		return err
	}
	for _, k := range topLevelDefaults {
		if c.top.Field[k] != nil && def.Field[k] != nil {
			return fmt.Errorf("%s: %s is set at the top level and under default:, which GitLab refuses", c.at(def.Field[k]), core.Quote(k))
		}
	}
	return nil
}

// job returns the job name with its extends merged in, as GitLab's extends
// does: "Performs a reverse deep merge based on the keys", each parent in
// its order, and "Keys from the last member always override anything
// defined on other levels", the job's own keys last. Each parent's own
// extends is merged first. ext is the levels of extends inherited through
// to reach it, and ref the levels of !reference; the job's own levels add
// to them, also when it was resolved before, and the second result is the
// levels of !reference its value took.
func (c *config) job(name string, ext, ref int) (*core.Node, int, error) {
	if j, ok := c.jobs[name]; ok {
		if err := c.depths(name, j.n, ext+j.ext, ref+j.ref); err != nil {
			return nil, 0, err
		}
		return j.n, j.ref, nil
	}
	raw := c.top.Field[name]
	if raw == nil {
		return nil, 0, fmt.Errorf("%s: no job %s in the file or the files it includes", c.main, core.Quote(name))
	}
	own, levels, err := c.deref(raw, ref)
	if err != nil {
		return nil, 0, err
	}
	if own.Shape != core.Mapping {
		return nil, 0, fmt.Errorf("%s: job %s is %s, not a mapping of keywords", c.at(own), core.Quote(name), shapeName(own))
	}
	parentsNode := own.Field["extends"]
	if parentsNode == nil {
		c.jobs[name] = resolvedJob{n: own, ref: levels}
		return own, levels, nil
	}
	if own != raw {
		return nil, 0, fmt.Errorf("%s: job %s is a !reference to a job that extends another; GitLab merges extends before it follows !reference, so it never merges this one", c.at(parentsNode), core.Quote(name))
	}
	if err := c.depths(name, parentsNode, ext+1, ref); err != nil {
		return nil, 0, err
	}
	if c.resolving[name] {
		return nil, 0, fmt.Errorf("%s: job %s extends itself, through its parents", c.at(parentsNode), core.Quote(name))
	}
	c.resolving[name] = true
	defer delete(c.resolving, name)
	parents := []*core.Node{parentsNode}
	if parentsNode.Shape == core.Sequence {
		parents = parentsNode.Items
	}
	merged := newTarget(own.Line)
	height := 0
	for _, p := range parents {
		if p.Shape != core.Scalar {
			return nil, 0, fmt.Errorf("%s: extends names a job by a string, not %s", c.at(p), shapeName(p))
		}
		pv := c.top.Field[p.Text]
		switch {
		case pv == nil:
			return nil, 0, fmt.Errorf("%s: job %s extends %s, which no file defines", c.at(p), core.Quote(name), core.Quote(p.Text))
		case c.isReference(pv):
			return nil, 0, fmt.Errorf("%s: job %s extends %s, a !reference; GitLab merges extends before it follows !reference, so a parent is a mapping", c.at(p), core.Quote(name), core.Quote(p.Text))
		}
		base, _, err := c.job(p.Text, ext+1, 0)
		if err != nil {
			return nil, 0, err
		}
		height = max(height, c.jobs[p.Text].ext+1)
		if err := c.merge(merged, merged.root, base); err != nil {
			return nil, 0, err
		}
	}
	if err := c.merge(merged, merged.root, own); err != nil {
		return nil, 0, err
	}
	c.jobs[name] = resolvedJob{n: merged.root, ext: height}
	return merged.root, 0, nil
}

// depths refuses a job reached through more levels of extends or of
// !reference than GitLab allows.
func (c *config) depths(name string, at *core.Node, ext, ref int) error {
	switch {
	case ext > maxExtends:
		return fmt.Errorf("%s: job %s inherits through more than %d levels of extends, GitLab's limit", c.at(at), core.Quote(name), maxExtends)
	case ref > maxReference:
		return fmt.Errorf("%s: !reference tags nest more than %d levels deep, GitLab's limit", c.at(at), maxReference)
	}
	return nil
}

// deref returns what n stands for: n itself, or for a !reference tag the
// node it selects, "keyword configuration from other job sections", in the
// configuration with includes and extends merged, followed again when that
// is a tag too. depth is the levels of !reference followed to reach n; the
// second result is the levels deref followed, each counted with the levels
// the selected value took, so no chain passes maxReference however it
// alternates jobs and tags, and a tag followed before is held to the limit
// from where it is reached again.
func (c *config) deref(n *core.Node, depth int) (*core.Node, int, error) {
	// The tags of this chain stay in following until it ends, so a chain
	// that leads back to a tag in it, directly or through a job's keys, is
	// refused rather than followed to the depth limit.
	var chain []*core.Node
	defer func() {
		for _, t := range chain {
			delete(c.following, t)
		}
	}()
	levels := 0
	for c.isReference(n) {
		if c.following[n] {
			return nil, 0, fmt.Errorf("%s: !reference leads back to itself", c.at(n))
		}
		if depth+levels >= maxReference {
			return nil, 0, fmt.Errorf("%s: !reference tags nest more than %d levels deep, GitLab's limit", c.at(n), maxReference)
		}
		if err := c.count(1, n.Line); err != nil {
			return nil, 0, err
		}
		c.following[n] = true
		chain = append(chain, n)
		f, ok := c.refs[n]
		if ok {
			if depth+levels+f.levels > maxReference {
				return nil, 0, fmt.Errorf("%s: !reference tags nest more than %d levels deep, GitLab's limit", c.at(n), maxReference)
			}
		} else {
			r, l, err := c.reference(n, depth+levels)
			if err != nil {
				return nil, 0, err
			}
			f = followed{r, l}
			c.refs[n] = f
		}
		levels += f.levels
		n = f.n
	}
	return n, levels, nil
}

// reference is the node a !reference tag selects, [job, key...], reached
// through depth levels of !reference, and the levels it took, itself
// counted: "valid if it is a non-empty array of strings" (GitLab's
// Reference tag), so [job] alone selects the job.
func (c *config) reference(n *core.Node, depth int) (*core.Node, int, error) {
	if len(n.Items) == 0 {
		return nil, 0, fmt.Errorf("%s: !reference takes a sequence of a job and the keys below it, such as [.setup, script]", c.at(n))
	}
	var names []string
	for _, it := range n.Items {
		if it.Shape != core.Scalar {
			return nil, 0, fmt.Errorf("%s: !reference names a key by a string, not %s", c.at(it), shapeName(it))
		}
		names = append(names, it.Text)
	}
	if c.top.Field[names[0]] == nil {
		return nil, 0, fmt.Errorf("%s: !reference names %s, which no file defines", c.at(n), core.Quote(names[0]))
	}
	cur, deepest, err := c.job(names[0], 0, depth+1)
	if err != nil {
		return nil, 0, err
	}
	for _, k := range names[1:] {
		var l int
		if cur, l, err = c.deref(cur, depth+1); err != nil {
			return nil, 0, err
		}
		deepest = max(deepest, l)
		next := cur.Field[k]
		if cur.Shape != core.Mapping || next == nil {
			return nil, 0, fmt.Errorf("%s: !reference %s selects nothing: %s has no key %s", c.at(n), core.Quote(fmt.Sprint(names)), core.Quote(names[0]), core.Quote(k))
		}
		cur = next
	}
	return cur, 1 + deepest, nil
}

// field is the value of key k in mapping n, with its !reference followed,
// or nil when n is no mapping or does not set k. A null value is unset, as
// GitLab reads a keyword set to null.
func (c *config) field(n *core.Node, k string) (*core.Node, error) {
	if n == nil || n.Shape != core.Mapping || n.Field[k] == nil {
		return nil, nil
	}
	v, _, err := c.deref(n.Field[k], 0)
	if err != nil || (v.Shape == core.Scalar && v.Type == core.TypeNull) {
		return nil, err
	}
	return v, nil
}

// jobToRun is the job a spec names, with its extends merged in.
func (c *config) jobToRun(name string) (*core.Node, error) {
	j, _, err := c.job(name, 0, 0)
	return j, err
}

// inherits reports whether the job takes what the top level sets under
// name, from its inherit: keyword's entry kind: true, false or a list of
// names. "inherit:default" and "inherit:variables" are both "true (default)".
// The boolean is the one Psych loads, so yes, On and FALSE are booleans
// too, and a quoted "false" or a !!str one is a string, which GitLab
// refuses.
func (c *config) inherits(job *core.Node, kind, name string) (bool, error) {
	inh, err := c.field(job, "inherit")
	if err != nil || inh == nil {
		return true, err
	}
	v, err := c.field(inh, kind)
	if err != nil || v == nil {
		return true, err
	}
	switch v.Shape {
	case core.Scalar:
		if isPsychBool(v.Text) && c.retyped(v, v.Text) {
			return psychTrue(v.Text), nil
		}
		if isPsychBool(v.Text) {
			return false, fmt.Errorf("%s: inherit:%s is %s as a string, quoted or tagged, which GitLab refuses: it takes a boolean or a list of names", c.at(v), kind, core.Quote(v.Text))
		}
	case core.Sequence:
		for _, it := range v.Items {
			if it.Shape == core.Scalar && it.Text == name {
				return true, nil
			}
		}
		return false, nil
	case core.Mapping:
		return false, fmt.Errorf("%s: inherit:%s is true, false or a list of names, not a mapping", c.at(v), kind)
	}
	return false, fmt.Errorf("%s: inherit:%s is true, false or a list of names, not %s", c.at(v), kind, core.Quote(v.Text))
}

// variable returns the values variable name may have in job, by the order
// GitLab's variable precedence gives "Variables in the .gitlab-ci.yml file,
// from highest to lowest precedence": rules:variables in the job, variables
// in the job (with a parallel:matrix instance's over them), workflow:rules:
// variables, and the default variables at the top level. A dotenv report
// of a job this one takes artifacts from ranks above them all, and the
// file says whether there is one, so such a report makes the value
// unknown. What ranks above that (a project's, group's or instance's
// settings, a pipeline's variables) is out of the files' reach, so errata
// reads the value the file gives.
func (c *config) variable(jobName, name string) ([]candidate, error) {
	job, err := c.jobToRun(jobName)
	if err != nil {
		return nil, err
	}
	t, err := c.field(job, "trigger")
	if err != nil {
		return nil, err
	}
	if t != nil && !c.every {
		return nil, fmt.Errorf("%s: job %s is a trigger job: it passes its variables to a downstream pipeline, which errata does not read", c.at(t), core.Quote(jobName))
	}
	if t != nil {
		forwards, err := c.forwards(t)
		if err != nil || !forwards {
			return []candidate{}, err
		}
	}
	if err := c.dotenv(jobName, job, name); err != nil {
		return nil, err
	}
	inherit, err := c.inherits(job, "variables", name)
	if err != nil {
		return nil, err
	}
	cands := []candidate{{}}
	if inherit {
		if cands, err = c.varIn(c.top, name, cands, fromTop); err != nil {
			return nil, err
		}
		wf, err := c.field(c.top, "workflow")
		if err != nil {
			return nil, err
		}
		if cands, err = c.rules(wf, name, cands, "workflow "); err != nil {
			return nil, err
		}
	}
	if cands, err = c.varIn(job, name, cands, fromJob); err != nil {
		return nil, err
	}
	if cands, err = c.matrix(jobName, job, name, cands); err != nil {
		return nil, err
	}
	return c.rules(job, name, cands, "")
}

// forwards reports whether the trigger t passes the job's variables
// downstream: "yaml_variables: true (default), or false. When true,
// variables defined in the trigger job are passed to downstream pipelines"
// (trigger:forward). Like inherit:, it takes the boolean Psych loads.
func (c *config) forwards(t *core.Node) (bool, error) {
	fw, err := c.field(t, "forward")
	if err != nil {
		return false, err
	}
	if fw != nil && fw.Shape != core.Mapping {
		return false, fmt.Errorf("%s: trigger:forward is %s; errata reads it only as a mapping of yaml_variables and pipeline_variables", c.at(fw), shapeName(fw))
	}
	v, err := c.field(fw, "yaml_variables")
	if err != nil {
		return false, err
	}
	if v == nil {
		return true, nil
	}
	if v.Shape == core.Scalar && isPsychBool(v.Text) && c.retyped(v, v.Text) {
		return psychTrue(v.Text), nil
	}
	return false, fmt.Errorf("%s: trigger:forward:yaml_variables is true or false, which GitLab requires as a boolean", c.at(v))
}

// varIn is the variable name under n's variables:, or below when n does
// not set it.
func (c *config) varIn(n *core.Node, name string, below []candidate, from varFrom) ([]candidate, error) {
	vars, err := c.field(n, "variables")
	if err != nil || vars == nil {
		return below, err
	}
	if vars.Shape != core.Mapping {
		return nil, fmt.Errorf("%s: variables is %s, not a mapping of names to values", c.at(vars), shapeName(vars))
	}
	if vars.Field[name] == nil {
		return below, nil
	}
	v, _, err := c.deref(vars.Field[name], 0)
	if err != nil {
		return nil, err
	}
	return []candidate{{n: v, from: from}}, nil
}

// matrix returns the candidates a job's parallel:matrix gives: each
// instance is a job of its own, named "build: [a, x]" by its values, and
// GitLab merges an instance's variables over the job's. An instance that
// sets name gives its value; one that does not gives below. When no
// instance sets it, below holds for every instance.
//
// An entry's instances are the product of its variables' values, the last
// key's varying fastest, as GitLab's product does. Each is read as an index
// into each key's values, so an entry of many keys costs its keys once, and
// a name is written once an instance, to maxCondition bytes.
func (c *config) matrix(jobName string, job *core.Node, name string, below []candidate) ([]candidate, error) {
	par, err := c.field(job, "parallel")
	if err != nil || par == nil || par.Shape != core.Mapping {
		return below, err
	}
	mat, err := c.field(par, "matrix")
	if err != nil || mat == nil {
		return below, err
	}
	if mat.Shape != core.Sequence {
		return nil, fmt.Errorf("%s: parallel:matrix is %s, not a list of mappings", c.at(mat), shapeName(mat))
	}
	entries, err := c.flatten(mat, 0)
	if err != nil {
		return nil, err
	}
	var es []matrixEntry
	total := 0
	sets := false
	for _, e := range entries {
		if e.Shape != core.Mapping {
			return nil, fmt.Errorf("%s: a parallel:matrix entry is a mapping of variables, not %s", c.at(e), shapeName(e))
		}
		en := matrixEntry{keys: e.Keys, lists: make([][]*core.Node, len(e.Keys)), setKey: -1, count: 1}
		for i, k := range e.Keys {
			if err := c.count(1, e.Line); err != nil {
				return nil, err
			}
			v, _, err := c.deref(e.Vals[i], 0)
			if err != nil {
				return nil, err
			}
			vals := []*core.Node{v}
			if v.Shape == core.Sequence {
				if vals, err = c.flatten(v, 0); err != nil {
					return nil, err
				}
			}
			for _, val := range vals {
				if val.Shape != core.Scalar {
					return nil, fmt.Errorf("%s: a parallel:matrix value is a string, not %s", c.at(val), shapeName(val))
				}
			}
			if len(vals) > 1 {
				en.multi = append(en.multi, i)
			}
			if len(vals) > 0 && en.count > maxInstances/len(vals) {
				return nil, fmt.Errorf("%s: parallel:matrix makes more than %d jobs, GitLab's limit", c.at(mat), maxInstances)
			}
			en.count *= len(vals)
			if k == name {
				en.setKey = i
			}
			en.lists[i] = vals
		}
		if total += en.count; total > maxInstances {
			return nil, fmt.Errorf("%s: parallel:matrix makes more than %d jobs, GitLab's limit", c.at(mat), maxInstances)
		}
		sets = sets || (en.setKey >= 0 && en.count > 0)
		es = append(es, en)
	}
	if !sets {
		return below, nil
	}
	var same map[string]bool // with one instance, none to tell apart
	if total > 1 {
		same = c.sameInEvery(es)
	}
	var out []candidate
	for _, en := range es {
		at := make([]int, len(en.lists))
		for range en.count {
			cond := c.instanceName(jobName, en.keys, en.lists, at, same)
			if en.setKey >= 0 {
				if out, err = c.add(out, candidate{n: en.lists[en.setKey][at[en.setKey]], cond: cond, from: fromMatrix}); err != nil {
					return nil, err
				}
			} else {
				for _, b := range below {
					if out, err = c.add(out, candidate{n: b.n, cond: joinCond(cond, b.cond), from: b.from}); err != nil {
						return nil, err
					}
				}
			}
			for j := len(en.multi) - 1; j >= 0; j-- {
				k := en.multi[j]
				if at[k]++; at[k] < len(en.lists[k]) {
					break
				}
				at[k] = 0
			}
		}
	}
	return out, nil
}

// matrixEntry is one entry of a parallel:matrix: its keys, each key's
// values, and the instances they make.
type matrixEntry struct {
	keys   []string
	lists  [][]*core.Node
	multi  []int // the keys of more than one value, which the instances vary
	setKey int   // the key that sets the variable, or -1
	count  int   // the instances, the product of the lists' lengths
}

// sameInEvery is the keys that hold one value, the same text, in every
// entry of a matrix that sets them: no instance differs from another in
// them.
func (c *config) sameInEvery(es []matrixEntry) map[string]bool {
	text := map[string]string{}
	same := map[string]bool{}
	for _, en := range es {
		for j, k := range en.keys {
			was, seen := same[k]
			switch {
			case seen && !was:
			case len(en.lists[j]) != 1:
				same[k] = false
			case !seen:
				text[k], same[k] = c.psychText(en.lists[j][0]), true
			default:
				same[k] = text[k] == c.psychText(en.lists[j][0])
			}
		}
	}
	return same
}

// noteBytes is the most bytes of a condition a report quotes
// (check.Value.ConditionNote).
const noteBytes = 64

// instanceName is GitLab's name for the instance whose values at gives,
// "build: [a, x]": each value as Psych loads it, as GitLab writes it with
// to_s, cut at maxCondition bytes. A name longer than a report quotes
// writes the values no instance differs in as "...", so the part of it a
// report shows tells the instances apart.
func (c *config) instanceName(jobName string, keys []string, lists [][]*core.Node, at []int, same map[string]bool) string {
	write := func(elide bool) string {
		var b strings.Builder
		b.WriteString(jobName)
		b.WriteString(": [")
		for i, l := range lists {
			if b.Len() > maxCondition {
				break
			}
			if i > 0 {
				b.WriteString(", ")
			}
			if elide && same[keys[i]] {
				b.WriteString("...")
				continue
			}
			b.WriteString(clipCondition(c.psychText(l[at[i]])))
		}
		b.WriteString("]")
		return clipCondition(b.String())
	}
	name := write(false)
	if len(name) > noteBytes {
		name = write(true)
	}
	return name
}

// maxCondition is the most bytes of a condition errata keeps. A report
// quotes at most 64 of them (check.Value.ConditionNote), and a condition is
// copied into each matrix instance's, so a long if: expression is cut once,
// where it is read.
const maxCondition = 256

// clipCondition is s cut at maxCondition bytes, on a character's start,
// with "..." after a cut.
func clipCondition(s string) string {
	if len(s) <= maxCondition {
		return s
	}
	end := maxCondition
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "..."
}

// add appends cd to out, refusing it past core.MaxValues candidates, or
// past max_bytes of condition text in all, before it is kept: a matrix
// times a job's or workflow's rules can make many more candidates than the
// file has lines.
func (c *config) add(out []candidate, cd candidate) ([]candidate, error) {
	if len(out) >= core.MaxValues {
		return nil, core.TooManyValues(c.main)
	}
	if c.condBytes += int64(len(cd.cond)); c.condBytes > c.limit {
		return nil, fmt.Errorf("%s: the conditions of the values hold more than %d bytes in all, the extractor's max_bytes; set max_bytes: higher", c.main, c.limit)
	}
	return append(out, cd), nil
}

// joinCond is two conditions that must both hold.
func joinCond(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return clipCondition(a + "; " + b)
}

// rules returns the candidates under n's rules:. GitLab takes the first
// rule that matches, and which one does depends on the pipeline, so each
// rule that can match gives a candidate: its own variable, under the
// rule's condition, or below when it sets none. A rule with when: never
// adds none, since the job, or for workflow the pipeline, does not run
// when it matches. A rule without if, changes or exists always matches, so
// the rules after it never apply. When no rule matches, the job does not
// run either. prefix names whose rules they are in a condition.
func (c *config) rules(n *core.Node, name string, below []candidate, prefix string) ([]candidate, error) {
	rs, err := c.field(n, "rules")
	if err != nil || rs == nil {
		return below, err
	}
	if rs.Shape != core.Sequence {
		return nil, fmt.Errorf("%s: rules is %s, not a list of rules", c.at(rs), shapeName(rs))
	}
	var out []candidate
	addBelow := false
	items, err := c.flatten(rs, 0)
	if err != nil {
		return nil, err
	}
	for _, r := range items {
		if r.Shape != core.Mapping {
			return nil, fmt.Errorf("%s: a rule is a mapping, not %s", c.at(r), shapeName(r))
		}
		when, err := c.field(r, "when")
		if err != nil {
			return nil, err
		}
		if when == nil || when.Text != "never" {
			got, err := c.varIn(r, name, nil, fromRule)
			if err != nil {
				return nil, err
			}
			if got == nil {
				addBelow = true
			}
			if len(got) > 0 {
				g := got[0]
				if g.cond, err = c.ruleCondition(r, prefix); err != nil {
					return nil, err
				}
				if out, err = c.add(out, g); err != nil {
					return nil, err
				}
			}
		}
		if r.Field["if"] == nil && r.Field["changes"] == nil && r.Field["exists"] == nil {
			break
		}
	}
	if addBelow {
		for _, b := range below {
			if out, err = c.add(out, b); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// ruleCondition describes what a rule tests: "if $X", "changes [a, b]",
// "exists [c]", each that it has, with its !reference followed, joined
// with "and", cut at maxCondition bytes; "" for a rule that tests nothing
// and so always matches.
func (c *config) ruleCondition(r *core.Node, prefix string) (string, error) {
	var parts []string
	for _, k := range []string{"if", "changes", "exists"} {
		v, err := c.field(r, k)
		switch {
		case err != nil:
			return "", err
		case v == nil:
		case v.Shape == core.Scalar:
			parts = append(parts, k+" "+clipCondition(v.Text))
		default:
			parts = append(parts, k+" "+c.describe(v))
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return clipCondition(prefix + strings.Join(parts, " and ")), nil
}

// describe writes a rule's changes: or exists: list as [a, b], or its
// mapping form by its paths:, at most eight items, each cut.
func (c *config) describe(v *core.Node) string {
	if v.Shape == core.Mapping {
		if p := v.Field["paths"]; p != nil {
			v = p
		}
	}
	var items []string
	for _, it := range v.Items {
		if it.Shape == core.Scalar {
			items = append(items, clipCondition(it.Text))
		}
		if len(items) == 8 {
			items = append(items, "...")
			break
		}
	}
	return "[" + strings.Join(items, ", ") + "]"
}

// dotenv refuses a variable that a dotenv report may set: "Variables from
// dotenv reports in jobs listed in dependencies or needs" rank above every
// variable of the file (CI/CD variable precedence). The report is written
// when the pipeline runs, so its content is out of reach, but whether one
// reaches this job is in the files: a job it needs, or with no needs: the
// jobs dependencies: lists, or by default every job of an earlier stage,
// that declares artifacts:reports:dotenv.
func (c *config) dotenv(jobName string, job *core.Node, name string) error {
	sources, earlier, err := c.artifactSources(jobName, job)
	if err != nil {
		return err
	}
	switch {
	case earlier && c.every:
		src, d, err := c.earlierReport(job)
		if err != nil || d == nil {
			return err
		}
		return c.dotenvError(d, jobName, src, name)
	case earlier:
		if sources, err = c.earlierJobs(job); err != nil {
			return err
		}
	}
	for _, s := range sources {
		if d, err := c.report(s); err != nil || d != nil {
			if err == nil {
				err = c.dotenvError(d, jobName, s, name)
			}
			return err
		}
	}
	return nil
}

func (c *config) dotenvError(d *core.Node, jobName, src, name string) error {
	return fmt.Errorf("%s: job %s gets the dotenv report of job %s, which the pipeline writes as it runs and which outranks the file, so it may set %s", c.at(d), core.Quote(jobName), core.Quote(src), name)
}

// report is the artifacts:reports:dotenv the job named s declares, or nil.
func (c *config) report(s string) (*core.Node, error) {
	src, err := c.jobToRun(s)
	if err != nil {
		return nil, err
	}
	art, err := c.keywordOf(src, "artifacts")
	if err != nil {
		return nil, err
	}
	rep, err := c.field(art, "reports")
	if err != nil {
		return nil, err
	}
	return c.field(rep, "dotenv")
}

// artifactSources lists the jobs whose artifacts job receives: those its
// needs: names with artifacts, or with no needs: those dependencies: names,
// or with neither every job of an earlier stage, which earlier reports and
// earlierReport reads.
func (c *config) artifactSources(jobName string, job *core.Node) (sources []string, earlier bool, err error) {
	needs, err := c.field(job, "needs")
	if err != nil {
		return nil, false, err
	}
	if needs != nil {
		items, err := c.listOf(needs)
		if err != nil {
			return nil, false, err
		}
		var out []string
		for _, it := range items {
			if it.Shape == core.Scalar {
				out = append(out, it.Text)
				continue
			}
			if it.Field["project"] != nil || it.Field["pipeline"] != nil {
				return nil, false, fmt.Errorf("%s: job %s needs a job of another pipeline, whose dotenv report errata cannot read", c.at(it), core.Quote(jobName))
			}
			j, err := c.field(it, "job")
			if err != nil || j == nil || j.Shape != core.Scalar {
				if err == nil {
					err = fmt.Errorf("%s: a needs: entry names a job: or a job by a string", c.at(it))
				}
				return nil, false, err
			}
			if a, err := c.field(it, "artifacts"); err != nil || (a != nil && a.Text == "false") {
				if err != nil {
					return nil, false, err
				}
				continue
			}
			out = append(out, j.Text)
		}
		return out, false, nil
	}
	deps, err := c.field(job, "dependencies")
	if err != nil {
		return nil, false, err
	}
	if deps != nil {
		items, err := c.listOf(deps)
		if err != nil {
			return nil, false, err
		}
		var out []string
		for _, it := range items {
			if it.Shape != core.Scalar {
				return nil, false, fmt.Errorf("%s: dependencies names a job by a string, not %s", c.at(it), shapeName(it))
			}
			out = append(out, it.Text)
		}
		return out, false, nil
	}
	return nil, true, nil
}

// listOf is the items of a list, each !reference followed.
func (c *config) listOf(n *core.Node) ([]*core.Node, error) {
	if n.Shape != core.Sequence {
		return nil, fmt.Errorf("%s: %s, not a list", c.at(n), shapeName(n))
	}
	return c.flatten(n, 0)
}

// earlierJobs lists the jobs of the stages before job's, in the file's
// order.
func (c *config) earlierJobs(job *core.Node) ([]string, error) {
	order, _, err := c.stageOrder()
	if err != nil {
		return nil, err
	}
	mine, err := c.stageOf(order, job)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, k := range c.top.Keys {
		if reserved[k] || strings.HasPrefix(k, ".") {
			continue
		}
		j, err := c.jobToRun(k)
		if err != nil {
			return nil, err
		}
		s, err := c.stageOf(order, j)
		if err != nil {
			return nil, err
		}
		if s < mine {
			out = append(out, k)
		}
	}
	return out, nil
}

// stageOrder is each stage's place, .pre first and .post last, and .post's.
func (c *config) stageOrder() (map[string]int, int, error) {
	order := map[string]int{".pre": 0}
	stages := defaultStages
	list, err := c.field(c.top, "stages")
	if err != nil {
		return nil, 0, err
	}
	if list != nil {
		items, err := c.listOf(list)
		if err != nil {
			return nil, 0, err
		}
		stages = nil
		for _, it := range items {
			stages = append(stages, it.Text)
		}
	}
	for i, s := range stages {
		if _, ok := order[s]; !ok {
			order[s] = i + 1
		}
	}
	last := len(stages) + 1
	order[".post"] = last
	return order, last, nil
}

// stagedReports is every job's stage and dotenv report, read once for the
// configuration: a job with neither needs: nor dependencies: receives the
// artifacts of every job of an earlier stage, and Every asks that for each
// job, which a walk of every job for each would make quadratic.
type stagedReports struct {
	order map[string]int // each stage's place, .pre first and .post last
	jobs  []stagedJob    // the jobs, in the file's order
	// first[v] is the index in jobs of the first job of a stage before
	// stage v that declares a report or whose report does not read, noJob
	// for none.
	first []int
}

// stagedJob is a job's name, and its dotenv report or why it does not
// read.
type stagedJob struct {
	name   string
	report *core.Node
	err    error
}

// earlierReport is the first job, in the file's order, of a stage before
// job's that declares a dotenv report, with the report, or the error of the
// first whose report does not read; "" and nil for none.
func (c *config) earlierReport(job *core.Node) (string, *core.Node, error) {
	if c.staged == nil {
		st, err := c.stageReports()
		if err != nil {
			return "", nil, err
		}
		c.staged = st
	}
	mine, err := c.stageOf(c.staged.order, job)
	if err != nil {
		return "", nil, err
	}
	i := c.staged.first[mine]
	if i == noJob {
		return "", nil, nil
	}
	j := c.staged.jobs[i]
	return j.name, j.report, j.err
}

// stageReports reads the stages and every job's stage and report. A job
// whose stage does not read refuses every job that receives the artifacts
// of earlier stages, as it would be one of them or not.
func (c *config) stageReports() (*stagedReports, error) {
	order, last, err := c.stageOrder()
	if err != nil {
		return nil, err
	}
	st := &stagedReports{order: order}
	// firstIn[v] is the first job of stage v that stops a job of a later
	// stage, as an index in st.jobs, and noJob for none.
	firstIn := make([]int, last+1)
	for v := range firstIn {
		firstIn[v] = noJob
	}
	for _, k := range c.top.Keys {
		if reserved[k] || strings.HasPrefix(k, ".") {
			continue
		}
		j, err := c.jobToRun(k)
		if err != nil {
			return nil, err
		}
		v, err := c.stageOf(order, j)
		if err != nil {
			return nil, err
		}
		d, err := c.report(k)
		if err != nil || d != nil {
			firstIn[v] = min(firstIn[v], len(st.jobs))
		}
		st.jobs = append(st.jobs, stagedJob{name: k, report: d, err: err})
	}
	st.first = make([]int, last+1)
	best := noJob
	for v := range st.first {
		st.first[v] = best
		best = min(best, firstIn[v])
	}
	return st, nil
}

// noJob is the index of no job, past every one.
const noJob = math.MaxInt

// stageOf is the place of job's stage in order: "If stage is not defined,
// the job uses the test stage by default." A stage the file does not list
// is placed with .pre, as a lookup of it gives 0.
func (c *config) stageOf(order map[string]int, j *core.Node) (int, error) {
	s, err := c.field(j, "stage")
	if err != nil {
		return 0, err
	}
	name := "test"
	if s != nil {
		name = s.Text
	}
	return order[name], nil
}

// keyword returns the value of a job keyword, or of a key below one: the
// job's own after extends, or the default's, when the job does not set it
// and inherits it: "If the job already has a keyword defined, the job
// keyword takes precedence".
func (c *config) keyword(jobName string, key []string) ([]candidate, error) {
	job, err := c.jobToRun(jobName)
	if err != nil {
		return nil, err
	}
	v, err := c.keywordOf(job, key[0])
	if err != nil {
		return nil, err
	}
	for _, k := range key[1:] {
		if v == nil {
			break
		}
		if v.Shape != core.Mapping {
			return nil, fmt.Errorf("%s: %s is %s, with no key %s below it", c.at(v), core.Quote(key[0]), shapeName(v), core.Quote(k))
		}
		if v, err = c.field(v, k); err != nil {
			return nil, err
		}
	}
	if v == nil {
		return nil, nil
	}
	return []candidate{{n: v}}, nil
}

// keywordOf is a job's keyword, or the default's it inherits.
func (c *config) keywordOf(job *core.Node, keyword string) (*core.Node, error) {
	v, err := c.field(job, keyword)
	if err != nil || v != nil || !defaultKeywords[keyword] {
		return v, err
	}
	inherit, err := c.inherits(job, "default", keyword)
	if err != nil || !inherit {
		return nil, err
	}
	return c.defaultKeyword(keyword)
}

// defaultKeyword is what default: sets for keyword, or the top level for
// one GitLab still reads there.
func (c *config) defaultKeyword(keyword string) (*core.Node, error) {
	def, err := c.field(c.top, "default")
	if err != nil {
		return nil, err
	}
	if v, err := c.field(def, keyword); err != nil || v != nil {
		return v, err
	}
	for _, k := range topLevelDefaults {
		if k == keyword {
			return c.field(c.top, keyword)
		}
	}
	return nil, nil
}

// flatten lists the items of sequence s, each !reference followed, with a
// sequence among them replaced by its own items, as GitLab flattens nested
// arrays in a script and the !reference tags in rules. depth is the levels
// of nesting, of arrays and of !reference both, to reach s.
func (c *config) flatten(s *core.Node, depth int) ([]*core.Node, error) {
	if depth > maxReference {
		return nil, fmt.Errorf("%s: arrays nest more than %d levels deep, GitLab's limit", c.at(s), maxReference)
	}
	var out []*core.Node
	for _, it := range s.Items {
		if err := c.count(1, it.Line); err != nil {
			return nil, err
		}
		v, levels, err := c.deref(it, depth)
		if err != nil {
			return nil, err
		}
		if v.Shape != core.Sequence {
			out = append(out, v)
			continue
		}
		inner, err := c.flatten(v, depth+max(levels, 1))
		if err != nil {
			return nil, err
		}
		out = append(out, inner...)
	}
	return out, nil
}

// values turns the candidates into values. A variable's long form gives
// its value: (an empty string without one, "the default value is an empty
// string") and expand: false keeps a $ literal. A candidate that no file
// sets beside one that a file does is refused, since in some pipelines the
// job would see no value from the files at all; a variable no file sets is
// check.NoValue, naming where it may be set. expands is whether a $ in the
// value names a variable GitLab or the runner expands, and variable
// whether the values are a variable's.
func (c *config) values(cands []candidate, variable, expands bool, name string) ([]check.Value, error) {
	type key struct {
		n    *core.Node
		cond string
	}
	seen := map[key]bool{}
	var kept []candidate
	unset := false
	for _, cd := range cands {
		switch {
		case cd.n == nil:
			unset = true
		case !seen[key{cd.n, cd.cond}]:
			// A set, since one job's rules can give hundreds of
			// thousands of candidates.
			seen[key{cd.n, cd.cond}] = true
			kept = append(kept, cd)
		}
	}
	switch {
	case unset && len(kept) > 0:
		return nil, fmt.Errorf("%s: the value is set only when some rules or matrix instances match; when another matches, the job gets no value from the files", c.at(kept[0].n))
	case unset && variable:
		return nil, &check.NoValue{Reason: fmt.Sprintf("no file gives the job %s; it may come from the project's, group's or instance's settings, which errata cannot read", name)}
	}
	var out []check.Value
	var total int64
	for _, cd := range kept {
		add := func(n *core.Node, text string, expand bool) error {
			if len(out) == core.MaxValues {
				return core.TooManyValues(c.main)
			}
			if total += int64(len(text)); total > c.limit {
				return fmt.Errorf("%s: the values hold more than %d bytes in all, the extractor's max_bytes; !reference tags can repeat a value, so set max_bytes: higher", c.main, c.limit)
			}
			out = append(out, check.Value{
				Text:       text,
				At:         check.Location{File: c.fileOf(n), Line: n.Line},
				NotLiteral: c.retyped(n, text) || (expand && references(text)),
				Condition:  cd.cond,
			})
			return nil
		}
		if err := c.value(cd, variable, expands, add); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// retyped reports whether GitLab's YAML loader, Psych, gives the job a
// value other than the scalar's text. Psych keeps a quoted or block
// scalar's text, and a !!str one's; it types a plain scalar by its text,
// as it does one with any other tag, and a !!float or !!binary is never its
// text: "Float(...)" of 3 is 3.0, and !!binary decodes.
func (c *config) retyped(n *core.Node, text string) bool {
	info := c.info[n]
	switch info.tag {
	case "":
		return info.plain && !psychString(text)
	case "!!str":
		return false
	case "!!float", "!!binary":
		return true
	}
	return !psychString(text)
}

// value adds the values a candidate holds: a scalar's text, each item of a
// list, or a variable's long form, in the forms GitLab takes where it was
// set: a long form of value and expand in a job, of value, description,
// options and expand at the top level, and none in a rule.
func (c *config) value(cd candidate, variable, expands bool, add func(*core.Node, string, bool) error) error {
	n := cd.n
	switch {
	case n.Shape == core.Scalar:
		return add(n, n.Text, expands)
	case variable && n.Shape == core.Sequence:
		return fmt.Errorf("%s: a variable's value is a string, not a sequence", c.at(n))
	case n.Shape == core.Sequence:
		items, err := c.flatten(n, 0)
		if err != nil {
			return err
		}
		for _, it := range items {
			if it.Shape != core.Scalar {
				return fmt.Errorf("%s: the list holds %s; name a key below it", c.at(it), shapeName(it))
			}
			if err := add(it, it.Text, expands); err != nil {
				return err
			}
		}
		return nil
	}
	if !variable {
		return fmt.Errorf("%s: selects a mapping, not a value; name a key below it, such as image.name", c.at(n))
	}
	// A mapping is a variable's long form.
	allowed := map[varFrom][]string{fromTop: {"value", "description", "options", "expand"}, fromJob: {"value", "expand"}}[cd.from]
	for _, k := range n.Keys {
		ok := false
		for _, a := range allowed {
			ok = ok || a == k
		}
		if !ok {
			return fmt.Errorf("%s: a variable here takes no key %s, which GitLab refuses; it takes %s", c.at(n), core.Quote(k), takes(allowed))
		}
	}
	val, err := c.field(n, "value")
	if err != nil {
		return err
	}
	if expand := n.Field["expand"]; expand != nil {
		if expand.Shape != core.Scalar || !c.info[expand].plain || (expand.Text != "true" && expand.Text != "false") {
			return fmt.Errorf("%s: expand is true or false, which GitLab requires as a boolean", c.at(expand))
		}
		if expand.Text == "false" {
			expands = false
		}
	}
	if val == nil {
		return add(n, "", false)
	}
	if val.Shape != core.Scalar {
		return fmt.Errorf("%s: a variable's value is a string, not %s", c.at(val), shapeName(val))
	}
	return add(val, val.Text, expands)
}

// takes lists the keys a long form takes, for a message.
func takes(keys []string) string {
	if len(keys) == 0 {
		return "only a string"
	}
	return strings.Join(keys, ", ")
}
