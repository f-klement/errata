package githubactions

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// maxJobs is GitHub's limit: "A matrix will generate a maximum of 256 jobs
// per workflow run" (Workflow syntax, strategy.matrix).
const maxJobs = 256

// maxProduct is the most combinations errata builds from a matrix's
// variables before exclude: removes some. It is errata's limit, so the
// product of a few long lists cannot fill memory before the exclusions.
const maxProduct = 65536

// maxCondition is the most bytes of a combination's name errata keeps. A
// report quotes at most 64 of them (check.Value.ConditionNote).
const maxCondition = 256

// noteBytes is how much of a condition a report quotes.
const noteBytes = 64

// minName is the fewest bytes of a job's name a condition keeps when the
// name and the values pass noteBytes.
const minName = 16

// bytesPerStep is how many bytes of text a comparison reads for one step
// of the count, as the yaml reader charges a merge key's text: one long
// value compared against every combination costs its length each time.
const bytesPerStep = 64

// combo is one combination of a matrix: its keys in their order, the
// matrix's own variables first, each key's id, and their values. unsure is
// an expression a comparison met that decides whether the job runs or what
// it holds, or nil.
type combo struct {
	keys   []string
	ids    []int
	vals   []*core.Node
	unsure *core.Node
}

// jobMatrix is the matrix of the job read. unknown is why a value that
// names the matrix has none errata can give: an expression writes the
// matrix, or decides whether one of its jobs runs. names are the jobs'
// names, made once a value needs them.
type jobMatrix struct {
	job     string
	node    *core.Node
	combos  []combo
	names   []string
	named   bool
	unknown error
}

// fold is s with its ASCII capitals made small, for the owner and
// repository names of quiet, which GitHub keeps to ASCII.
func fold(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// upperRune is r as the runner compares a context's keys. GitHub says the
// matrix's variable names are case insensitive, with OS and os as its
// example. The runner keeps a context's keys in a Dictionary built with
// StringComparer.OrdinalIgnoreCase (actions/runner,
// src/Sdk/DTPipelines/Pipelines/ContextData/DictionaryContextData.cs).
// On Linux .NET compares that way through a table of each character's
// simple upper case from ICU, less two letters it keeps from mapping to
// ASCII: the dotless i and the long s (dotnet/runtime release/8.0,
// src/native/libs/System.Globalization.Native/pal_casing.c,
// GlobalizationNative_InitOrdinalCasingPage). So é and É are one key, and
// the Kelvin sign, which is its own upper case, is not k.
func upperRune(r rune) rune {
	if r == '\u0131' || r == '\u017f' {
		return r
	}
	return unicode.ToUpper(r)
}

// runeAt is the rune at s[i] in upper case and its length. A byte that
// starts no rune is itself, as a negative number no rune is.
func runeAt(s string, i int) (rune, int) {
	r, n := utf8.DecodeRuneInString(s[i:])
	if r == utf8.RuneError && n == 1 {
		return -1 - rune(s[i]), 1
	}
	return upperRune(r), n
}

// foldKey is the matrix key k as the runner compares it: two keys are one
// when their foldKey is.
func foldKey(k string) string {
	var b strings.Builder
	for i := 0; i < len(k); {
		r, n := runeAt(k, i)
		if r < 0 {
			b.WriteByte(k[i])
		} else {
			b.WriteRune(r)
		}
		i += n
	}
	return b.String()
}

// equalKey reports whether the keys a and b are one, as foldKey does, and
// stops at the first rune that differs.
func equalKey(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		x, n := runeAt(a, i)
		y, m := runeAt(b, j)
		if x != y {
			return false
		}
		i, j = i+n, j+m
	}
	return i == len(a) && j == len(b)
}

// keyID is the number of the matrix key k, the same for every spelling of
// it: "The variable names are case insensitive." Two keys compare as two
// numbers, whatever their length.
func (w *workflow) keyID(k string) int {
	f := foldKey(k)
	id, ok := w.ids[f]
	if !ok {
		id = len(w.ids)
		w.ids[f] = id
	}
	return id
}

// get returns the value of the key id in c. The lookup is a step even in
// a combination of no key, which an empty include: entry makes.
func (w *workflow) get(c *combo, id int) (*core.Node, error) {
	if err := w.count(1 + len(c.ids)); err != nil {
		return nil, err
	}
	for i, cid := range c.ids {
		if cid == id {
			return c.vals[i], nil
		}
	}
	return nil, nil
}

// set sets the key k, of the given id, of c to v.
func (w *workflow) set(c *combo, k string, id int, v *core.Node) error {
	if err := w.count(1 + len(c.ids)); err != nil {
		return err
	}
	for i, cid := range c.ids {
		if cid == id {
			c.vals[i] = v
			return nil
		}
	}
	c.keys = append(c.keys, k)
	c.ids = append(c.ids, id)
	c.vals = append(c.vals, v)
	return nil
}

// count adds n to the steps building and reading the matrix has taken,
// and refuses them past core.MaxExpanded, the bound YAML aliases have:
// include: and exclude: entries are each matched against every
// combination, so a short file could otherwise take a long time.
func (w *workflow) count(n int) error {
	if w.work += n; w.work > core.MaxExpanded {
		return fmt.Errorf("%s: the matrix takes more than %d steps to build, the limit; write fewer include: or exclude: entries", w.file, core.MaxExpanded)
	}
	return nil
}

// countBytes counts a step for each bytesPerStep bytes a comparison reads.
func (w *workflow) countBytes(n int) error { return w.count(n / bytesPerStep) }

// eqFold reports whether the keys a and b are one, as equalKey does, and
// counts what the comparison reads.
func (w *workflow) eqFold(a, b string) (bool, error) {
	if err := w.count(1 + min(len(a), len(b))/bytesPerStep); err != nil {
		return false, err
	}
	return equalKey(a, b), nil
}

// expressionError is the refusal of a part of the matrix that an
// expression writes. Only a value that names the matrix needs the matrix,
// so only such a value is refused with it.
type expressionError struct{ error }

// outOfReach is the refusal of a part of the matrix that an expression
// writes.
func (w *workflow) outOfReach(job string, n *core.Node, what string) error {
	return &expressionError{fmt.Errorf("%s:%d: %s of job %s is an expression, %s, which GitHub evaluates as the workflow runs",
		w.file, n.Line, what, core.Quote(job), core.Quote(n.Text))}
}

func isExpression(n *core.Node) bool {
	return n.Shape == core.Scalar && strings.Contains(n.Text, "${{")
}

// matrix returns the combinations of the job's matrix, as GitHub builds
// them, or nil for a job with none: the product of the variables, in their
// order with the last varying fastest, less those an exclude: entry
// matches, and then each include: entry added to every combination whose
// variables it agrees with, or a combination of its own when it agrees
// with none. "All include combinations are processed after exclude"
// (Workflow syntax, strategy.matrix.exclude).
func (w *workflow) matrix(job string, j *core.Node) ([]combo, error) {
	strategy := j.Field["strategy"]
	if strategy == nil {
		return nil, nil
	}
	if isExpression(strategy) {
		return nil, w.outOfReach(job, strategy, "the strategy")
	}
	if strategy.Shape != core.Mapping {
		return nil, fmt.Errorf("%s:%d: the strategy of job %s is %s, not a mapping", w.file, strategy.Line, core.Quote(job), shapeName(strategy))
	}
	m := strategy.Field["matrix"]
	switch {
	case m == nil:
		return nil, nil
	case isExpression(m):
		return nil, w.outOfReach(job, m, "the matrix")
	case m.Shape != core.Mapping:
		return nil, fmt.Errorf("%s:%d: the matrix of job %s is %s, not a mapping", w.file, m.Line, core.Quote(job), shapeName(m))
	}
	var names []string
	var ids []int
	var lists [][]*core.Node
	spelled := map[int]string{}
	for i, k := range m.Keys {
		v := m.Vals[i]
		if k == "include" || k == "exclude" {
			continue
		}
		id := w.keyID(k)
		if seen, ok := spelled[id]; ok {
			return nil, fmt.Errorf("%s:%d: the matrix of job %s has the variables %s and %s, which GitHub reads as one: its names are case insensitive",
				w.file, v.Line, core.Quote(job), core.Quote(seen), core.Quote(k))
		}
		spelled[id] = k
		switch {
		case isExpression(v):
			return nil, w.outOfReach(job, v, "the matrix variable "+core.Quote(k))
		case v.Shape != core.Sequence:
			return nil, fmt.Errorf("%s:%d: the matrix variable %s is %s, not a list of values", w.file, v.Line, core.Quote(k), shapeName(v))
		case len(v.Items) == 0:
			return nil, fmt.Errorf("%s:%d: the matrix variable %s holds no value, which GitHub refuses", w.file, v.Line, core.Quote(k))
		}
		names = append(names, k)
		ids = append(ids, id)
		lists = append(lists, v.Items)
	}
	variable := make(map[int]int, len(ids))
	for i, id := range ids {
		variable[id] = i
	}
	exclude, err := w.entries(job, m.Field["exclude"], "exclude", variable)
	if err != nil {
		return nil, err
	}
	include, err := w.entries(job, m.Field["include"], "include", variable)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 && len(include) == 0 {
		return nil, fmt.Errorf("%s:%d: the matrix of job %s has no variable and no include: entry, so it gives no job", w.file, m.Line, core.Quote(job))
	}
	combos, err := w.product(job, names, ids, lists)
	if err != nil {
		return nil, err
	}
	// GitHub refuses a matrix of more than maxJobs jobs. A job an
	// expression may remove, or may make, is not certain to be one, so the
	// limit is decided on the certain jobs. The others are bounded apart:
	// the product's are at most maxProduct, and errata keeps no job past
	// maxJobs + 1, since a matrix of more than maxJobs that hangs on an
	// expression gives no value that names it. hangs is the first such
	// expression.
	certain, total := 0, 0
	var hangs *core.Node
	kept := combos[:0]
	for _, c := range combos {
		out := false
		for _, ex := range exclude {
			v, expr, err := w.agrees(&c, ex)
			if err != nil {
				return nil, err
			}
			if v == agree {
				out = true
				break
			}
			if v == unsure && c.unsure == nil {
				c.unsure = expr
			}
		}
		if out {
			continue
		}
		kept = append(kept, c)
		total++
		if c.unsure == nil {
			certain++
		} else if hangs == nil {
			hangs = c.unsure
		}
	}
	combos = kept
	// include: adds jobs and removes none, so more than the limit now is
	// more after it too.
	if certain > maxJobs {
		return nil, w.tooMany(job, m)
	}
	// Only the combinations of the variables take an entry; one an entry
	// makes takes no later entry, as the documented example shows.
	original := len(combos)
	for _, in := range include {
		added := false
		var maybe *core.Node
		for i := range original {
			v, expr, err := w.agrees(&combos[i], in)
			switch {
			case err != nil:
				return nil, err
			case v == disagree:
				continue
			case v == unsure:
				// Whether the entry joins this job is unknown, and so
				// whether it is a job of its own.
				if combos[i].unsure == nil {
					combos[i].unsure = expr
				}
				if maybe == nil {
					maybe = expr
				}
				continue
			}
			added = true
			for k, key := range in.n.Keys {
				if in.variable[k] < 0 {
					if err := w.set(&combos[i], key, in.ids[k], in.n.Vals[k]); err != nil {
						return nil, err
					}
				}
			}
		}
		// An entry that joins a combination makes no job of its own. If
		// it joins only combinations exclude: may have removed, it is a
		// job of its own only when exclude: removed them all, and then it
		// takes their place: the jobs are no more than total says.
		if added {
			continue
		}
		if maybe == nil {
			certain++
		} else if hangs == nil {
			hangs = maybe
		}
		if certain > maxJobs {
			return nil, w.tooMany(job, m)
		}
		total++
		// The entry's own job needs no unsure of its own: an entry is
		// unsure of its job only after it marked a combination before it
		// unsure, and jobs() names the first.
		if total <= maxJobs {
			n := len(in.n.Keys)
			combos = append(combos, combo{keys: in.n.Keys[:n:n], ids: in.ids[:n:n], vals: in.n.Vals[:n:n]})
		}
	}
	w.held = len(combos)
	if total > maxJobs {
		return nil, &expressionError{fmt.Errorf("%s:%d: whether the matrix of job %s gives more than %d jobs, GitHub's limit, hangs on %s",
			w.file, hangs.Line, core.Quote(job), maxJobs, doubt(hangs))}
	}
	if len(combos) == 0 {
		return nil, fmt.Errorf("%s:%d: the matrix of job %s gives no job: exclude: removes every combination", w.file, m.Line, core.Quote(job))
	}
	return combos, nil
}

func (w *workflow) tooMany(job string, m *core.Node) error {
	return fmt.Errorf("%s:%d: the matrix of job %s gives more than %d jobs, GitHub's limit", w.file, m.Line, core.Quote(job), maxJobs)
}

// product is every combination of the variables' values, the last
// variable varying fastest: "The order of the variables in the matrix
// determines the order in which the jobs are created."
func (w *workflow) product(job string, names []string, ids []int, lists [][]*core.Node) ([]combo, error) {
	if len(names) == 0 {
		return nil, nil
	}
	total := 1
	for _, l := range lists {
		if total *= len(l); total > maxProduct {
			return nil, fmt.Errorf("%s:%d: the matrix of job %s has more than %d combinations before exclude:, errata's limit",
				w.file, l[0].Line, core.Quote(job), maxProduct)
		}
	}
	if err := w.count(total * len(names)); err != nil {
		return nil, err
	}
	combos := make([]combo, total)
	at := make([]int, len(names))
	n := len(names)
	for c := range combos {
		vals := make([]*core.Node, n)
		for i := range names {
			vals[i] = lists[i][at[i]]
		}
		// The keys are shared until an include: entry adds one, which
		// the full slice expression makes a copy.
		combos[c] = combo{keys: names[:n:n], ids: ids[:n:n], vals: vals}
		for i := len(at) - 1; i >= 0; i-- {
			if at[i]++; at[i] < len(lists[i]) {
				break
			}
			at[i] = 0
		}
	}
	return combos, nil
}

// entry is an include: or exclude: entry with its keys' ids, and for each
// key the index of the matrix variable it names, or -1. compared holds the
// keys that name a variable, each as its index and its variable's: only
// those decide a comparison, so a comparison walks no other key.
type entry struct {
	n        *core.Node
	ids      []int
	variable []int
	compared [][2]int
}

// entries returns the mappings of an include: or exclude: list, each key
// folded once: an alias repeats an entry without its bytes, so a list of
// them shares one.
func (w *workflow) entries(job string, list *core.Node, what string, variable map[int]int) ([]*entry, error) {
	switch {
	case list == nil:
		return nil, nil
	case isExpression(list):
		return nil, w.outOfReach(job, list, "the matrix's "+what+":")
	case list.Shape != core.Sequence:
		return nil, fmt.Errorf("%s:%d: the matrix's %s: is %s, not a list", w.file, list.Line, what, shapeName(list))
	}
	made := map[*core.Node]*entry{}
	out := make([]*entry, 0, len(list.Items))
	for _, e := range list.Items {
		switch {
		case isExpression(e):
			return nil, w.outOfReach(job, e, "an entry of the matrix's "+what+":")
		case e.Shape != core.Mapping:
			return nil, fmt.Errorf("%s:%d: an entry of the matrix's %s: is %s, not a mapping", w.file, e.Line, what, shapeName(e))
		}
		x := made[e]
		if x == nil {
			x = &entry{n: e, ids: make([]int, len(e.Keys)), variable: make([]int, len(e.Keys))}
			for i, k := range e.Keys {
				x.ids[i] = w.keyID(k)
				v, ok := variable[x.ids[i]]
				if !ok {
					v = -1
				}
				x.variable[i] = v
				if v >= 0 {
					x.compared = append(x.compared, [2]int{i, v})
				}
			}
			made[e] = x
		}
		out = append(out, x)
	}
	return out, nil
}

// verdict is what a comparison of a combination with an entry gives:
// GitHub evaluates an expression before it compares, so one that meets an
// expression is unsure.
type verdict int

const (
	disagree verdict = iota
	agree
	unsure
)

// agrees reports whether every variable the entry sets has its value in c,
// which is one of the product's combinations. An exclude: entry that
// agrees removes c: "An excluded configuration only has to be a partial
// match for it to be excluded." An include: entry that agrees is added to
// c, since it overwrites none of c's variables. A key of the entry the
// variables do not hold decides nothing. When no key disagrees and one
// compares an expression, the verdict is unsure, with that expression.
func (w *workflow) agrees(c *combo, e *entry) (verdict, *core.Node, error) {
	if err := w.count(1); err != nil {
		return disagree, nil, err
	}
	var expr *core.Node
	for _, iv := range e.compared {
		same, at, err := w.equal(c.vals[iv[1]], e.n.Vals[iv[0]])
		if err != nil || same == disagree {
			return disagree, nil, err
		}
		if same == unsure && expr == nil {
			expr = at
		}
	}
	if expr != nil {
		return unsure, expr, nil
	}
	return agree, nil, nil
}

// equal reports whether a and b hold the same value: the same text in the
// same shape, at any depth. A scalar that holds an expression could be any
// value, so a comparison with it is unsure. Each node compared is a step,
// and so is each bytesPerStep bytes of text read.
func (w *workflow) equal(a, b *core.Node) (verdict, *core.Node, error) {
	if err := w.count(1); err != nil {
		return disagree, nil, err
	}
	if a == nil || b == nil {
		return verdictOf(a == b), nil, nil
	}
	for _, n := range []*core.Node{a, b} {
		if n.Shape == core.Scalar {
			if err := w.countBytes(len(n.Text)); err != nil {
				return disagree, nil, err
			}
			if isExpression(n) {
				return unsure, n, nil
			}
		}
	}
	if a.Shape != b.Shape {
		return disagree, nil, nil
	}
	switch a.Shape {
	case core.Scalar:
		return verdictOf(a.Text == b.Text), nil, nil
	case core.Mapping:
		if len(a.Keys) != len(b.Keys) {
			return disagree, nil, nil
		}
		// GitHub does not document whether it compares a mapping's keys
		// in any case, as it reads them (lookup). A key that matches only
		// in another case pairs the values all the same, and when they
		// agree the verdict is unsure, at b.
		folded := false
		v, at, err := w.equalAll(len(a.Keys), func(i int) (*core.Node, *core.Node, error) {
			k := a.Keys[i]
			if err := w.countBytes(len(k)); err != nil {
				return nil, nil, err
			}
			if bv := b.Field[k]; bv != nil {
				return a.Vals[i], bv, nil
			}
			for j, bk := range b.Keys {
				same, err := w.eqFold(k, bk)
				if err != nil {
					return nil, nil, err
				}
				if same {
					folded = true
					return a.Vals[i], b.Vals[j], nil
				}
			}
			return a.Vals[i], nil, nil
		})
		if folded && v != disagree {
			return unsure, b, nil
		}
		return v, at, err
	case core.Sequence:
		if len(a.Items) != len(b.Items) {
			return disagree, nil, nil
		}
		return w.equalAll(len(a.Items), func(i int) (*core.Node, *core.Node, error) {
			return a.Items[i], b.Items[i], nil
		})
	}
	return disagree, nil, nil
}

// equalAll compares the n pairs pair gives: one that disagrees decides,
// and else one that is unsure.
func (w *workflow) equalAll(n int, pair func(int) (*core.Node, *core.Node, error)) (verdict, *core.Node, error) {
	out, at := agree, (*core.Node)(nil)
	for i := range n {
		a, b, err := pair(i)
		if err != nil {
			return disagree, nil, err
		}
		v, expr, err := w.equal(a, b)
		if err != nil || v == disagree {
			return disagree, nil, err
		}
		if v == unsure && at == nil {
			out, at = unsure, expr
		}
	}
	return out, at, nil
}

// doubt says what an unsure verdict at n hangs on: an expression, or a
// mapping whose keys match the other side's only in another case.
func doubt(n *core.Node) string {
	if n.Shape == core.Mapping {
		return "the case of this mapping's keys, which match the other side's only in another case: GitHub does not document whether it compares keys in any case"
	}
	return core.Quote(n.Text) + ", an expression GitHub evaluates as the workflow runs"
}

func verdictOf(same bool) verdict {
	if same {
		return agree
	}
	return disagree
}

// jobs makes, once, the jobs' names, and the refusal of a value that names
// the matrix when an expression decides whether one of its jobs runs or
// what it holds.
func (w *workflow) jobs(m *jobMatrix) error {
	if m.named {
		return nil
	}
	names, err := w.comboNames(m)
	if err != nil {
		return err
	}
	m.names, m.named = names, true
	for i, c := range m.combos {
		if c.unsure != nil {
			m.unknown = fmt.Errorf("%s:%d: whether job %s runs, and with which values, hangs on %s",
				w.file, c.unsure.Line, core.Quote(names[i]), doubt(c.unsure))
			break
		}
	}
	return nil
}

// comboNames names each combination as GitHub names its job. The name is
// the job's name:, else its id. When the name references the matrix, it
// is the name with each matrix reference replaced; otherwise the
// combination's values follow it, such as "test (18, ubuntu-latest)". A
// report quotes 64 bytes of a condition, so a longer name cuts the job's
// name to leave the values room and writes the values every combination
// shares as "...".
func (w *workflow) comboNames(m *jobMatrix) ([]string, error) {
	combos := m.combos
	if combos == nil {
		return nil, nil
	}
	base, pieces, refs := m.job, []piece(nil), false
	if n := m.node.Field["name"]; n != nil && n.Shape == core.Scalar && n.Text != "" {
		base = n.Text
		pieces, refs = w.nameTemplate(base)
	}
	var shared []*core.Node
	write := func(c *combo, elide bool) (string, error) {
		var b strings.Builder
		if refs {
			for _, p := range pieces {
				if b.Len() > maxCondition {
					break
				}
				if !p.ref {
					clipped(&b, p.text)
					continue
				}
				ref, err := w.lookup(c, p)
				if err != nil {
					return "", err
				}
				if ref != nil {
					inline(&b, ref)
				}
			}
			return clipCondition(b.String()), nil
		}
		var vb strings.Builder
		vb.WriteString(" (")
		for i := range c.keys {
			if i > 0 {
				vb.WriteString(", ")
			}
			if vb.Len() > maxCondition {
				break
			}
			if elide && shared[c.ids[i]] != nil {
				vb.WriteString("...")
				continue
			}
			inline(&vb, c.vals[i])
		}
		vb.WriteString(")")
		clipName(&b, base, noteBytes-vb.Len())
		b.WriteString(vb.String())
		return clipCondition(b.String()), nil
	}
	names := make([]string, len(combos))
	long := false
	for i := range combos {
		s, err := write(&combos[i], false)
		if err != nil {
			return nil, err
		}
		names[i] = s
		long = long || len(s) > noteBytes
	}
	if !long || refs || len(combos) == 1 {
		return names, nil
	}
	shared = sharedValues(combos, len(w.ids))
	for i := range combos {
		var err error
		if names[i], err = write(&combos[i], true); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// sharedValues returns, for each of the ids key ids, the scalar every
// combination gives it, or nil, in one pass over the combinations. Each
// value is compared once a combination, with the first one's text; GitHub
// allows 256 combinations.
func sharedValues(combos []combo, ids int) []*core.Node {
	shared := make([]*core.Node, ids)
	seen := make([]int, ids)
	for _, c := range combos {
		for i, id := range c.ids {
			v := c.vals[i]
			switch {
			case seen[id] == 0 && v.Shape == core.Scalar:
				shared[id] = v
			case seen[id] == 0, shared[id] == nil, shared[id] == v:
			case v.Shape != core.Scalar || v.Text != shared[id].Text:
				shared[id] = nil
			}
			seen[id]++
		}
	}
	for id, n := range seen {
		if n != len(combos) {
			shared[id] = nil
		}
	}
	return shared
}

// clipName writes the job's name to b, cut to room bytes, but no fewer
// than minName, and "...", when it is longer: a report quotes noteBytes of
// a condition, and the values after the name tell the jobs apart.
func clipName(b *strings.Builder, name string, room int) {
	if len(name) <= room {
		b.WriteString(name)
		return
	}
	end := max(room-len("..."), minName)
	if end >= len(name) {
		b.WriteString(name)
		return
	}
	for end > 0 && !utf8.RuneStart(name[end]) {
		end--
	}
	b.WriteString(name[:end])
	b.WriteString("...")
}

// clipped writes s to b, stopping past maxCondition bytes.
func clipped(b *strings.Builder, s string) {
	room := maxCondition + 1 - b.Len()
	if room <= 0 {
		return
	}
	if len(s) > room {
		s = s[:room]
	}
	b.WriteString(s)
}

// inline writes n in YAML's flow style, stopping past maxCondition bytes.
func inline(b *strings.Builder, n *core.Node) {
	switch {
	case b.Len() > maxCondition:
	case n.Shape == core.Scalar:
		clipped(b, n.Text)
	case n.Shape == core.Sequence:
		b.WriteString("[")
		for i, it := range n.Items {
			if i > 0 {
				b.WriteString(", ")
			}
			inline(b, it)
			if b.Len() > maxCondition {
				break
			}
		}
		b.WriteString("]")
	default:
		b.WriteString("{")
		for i, k := range n.Keys {
			if i > 0 {
				b.WriteString(", ")
			}
			clipped(b, k)
			b.WriteString(": ")
			inline(b, n.Vals[i])
			if b.Len() > maxCondition {
				break
			}
		}
		b.WriteString("}")
	}
}

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

// matrixRef is an expression that is a reference to the matrix context
// and nothing else, such as ${{ matrix.node }} or ${{ matrix.node.version }}.
// The context's name is matched as written: GitHub's docs say its variable
// names are case insensitive and say nothing of a context name's case.
var matrixRef = regexp.MustCompile(`^\s*matrix((?:\.[A-Za-z_][A-Za-z0-9_-]*)+)\s*$`)

// namesMatrix is an expression that references the matrix context: the
// name matrix, not a property such as env.matrix or a part of a longer
// name such as MATRIX_LABEL or matrix-size. GitHub's docs say nothing of
// the case of a context's name, so it is matched as written.
var namesMatrix = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])matrix(?:[^A-Za-z0-9_-]|$)`)

// piece is a part of a value with expressions: literal text, or a matrix
// reference, the id of its variable and the path below it.
type piece struct {
	text string
	ref  bool
	id   int
	path []string
}

// madeTemplate is a value's template, and whether its expressions are all
// matrix references.
type madeTemplate struct {
	pieces []piece
	ok     bool
}

// refPiece is the piece of the matrix reference whose path, after
// "matrix", is dotted.
func (w *workflow) refPiece(dotted string) piece {
	path := strings.Split(dotted[1:], ".")
	return piece{ref: true, id: w.keyID(path[0]), path: path[1:]}
}

// template splits text into its literal parts and its matrix references.
// It reports false when an expression is anything but a matrix reference,
// or is not closed.
func (w *workflow) template(text string) ([]piece, bool) {
	var out []piece
	for {
		start := strings.Index(text, "${{")
		if start < 0 {
			if text != "" {
				out = append(out, piece{text: text})
			}
			return out, true
		}
		end := strings.Index(text[start:], "}}")
		if end < 0 {
			return nil, false
		}
		m := matrixRef.FindStringSubmatch(text[start+3 : start+end])
		if m == nil {
			return nil, false
		}
		if start > 0 {
			out = append(out, piece{text: text[:start]})
		}
		out = append(out, w.refPiece(m[1]))
		text = text[start+end+2:]
	}
}

// nameTemplate splits a job's name into its literal parts and its matrix
// references, keeping any other expression as written, and reports whether
// any expression names the matrix. GitHub appends a job's values to its
// name only when the name references no matrix variable.
func (w *workflow) nameTemplate(text string) ([]piece, bool) {
	var out []piece
	refs := false
	for {
		start := strings.Index(text, "${{")
		end := -1
		if start >= 0 {
			end = strings.Index(text[start:], "}}")
		}
		if end < 0 {
			if text != "" {
				out = append(out, piece{text: text})
			}
			return out, refs
		}
		expr := text[start+3 : start+end]
		if start > 0 {
			out = append(out, piece{text: text[:start]})
		}
		if m := matrixRef.FindStringSubmatch(expr); m != nil {
			out = append(out, w.refPiece(m[1]))
		} else {
			out = append(out, piece{text: text[start : start+end+2]})
		}
		refs = refs || namesMatrix.MatchString(expr)
		text = text[start+end+2:]
	}
}

// lookup returns the node the reference p names in c, or nil when c does
// not set it. A mapping's keys match in any case, as the variables do.
func (w *workflow) lookup(c *combo, p piece) (*core.Node, error) {
	ref, err := w.get(c, p.id)
	if err != nil {
		return nil, err
	}
	for _, k := range p.path {
		if ref == nil || ref.Shape != core.Mapping {
			return nil, nil
		}
		var next *core.Node
		for i, rk := range ref.Keys {
			same, err := w.eqFold(rk, k)
			if err != nil {
				return nil, err
			}
			if same {
				next = ref.Vals[i]
				break
			}
		}
		ref = next
	}
	return ref, nil
}

// expand adds the value of the scalar n: its text, and for a value set in
// a job or a step whose expressions are all references to the matrix, the
// value each combination gives it, under the combination's name. Any other
// value that holds an expression is added as written, not literal. A
// value that names a matrix errata cannot build is refused.
func (w *workflow) expand(n *core.Node, in scope, m *jobMatrix, add func(check.Value) error) error {
	at := check.Location{File: w.file, Line: n.Line}
	if !strings.Contains(n.Text, "${{") {
		return add(check.Value{Text: n.Text, At: at, NotLiteral: w.retyped(n)})
	}
	if in != inJob || (m.combos == nil && m.unknown == nil) {
		return add(check.Value{Text: n.Text, At: at, NotLiteral: true})
	}
	// An alias repeats a value without its bytes, so its template is made
	// once.
	t, ok := w.templates[n]
	if !ok {
		t.pieces, t.ok = w.template(n.Text)
		w.templates[n] = t
	}
	pieces := t.pieces
	if !t.ok {
		return add(check.Value{Text: n.Text, At: at, NotLiteral: true})
	}
	if m.unknown == nil {
		if err := w.jobs(m); err != nil {
			return err
		}
	}
	if m.unknown != nil {
		return m.unknown
	}
	for i := range m.combos {
		v, err := w.instance(n, pieces, &m.combos[i])
		if err != nil {
			return err
		}
		v.Condition = m.names[i]
		if err := add(v); err != nil {
			return err
		}
	}
	return nil
}

// instance is the value the combination c gives the template n of pieces.
// A reference to a key c does not set is null, which GitHub writes as the
// empty string. A reference to a mapping or a list leaves the value as
// written, not literal. A value that is one reference alone is at the line
// of the matrix's value, which is where it is set.
func (w *workflow) instance(n *core.Node, pieces []piece, c *combo) (check.Value, error) {
	v := check.Value{At: check.Location{File: w.file, Line: n.Line}}
	var b strings.Builder
	for _, p := range pieces {
		if !p.ref {
			b.WriteString(p.text)
			continue
		}
		ref, err := w.lookup(c, p)
		if err != nil {
			return v, err
		}
		switch {
		case ref == nil:
		case ref.Shape != core.Scalar:
			return check.Value{Text: n.Text, At: v.At, NotLiteral: true}, nil
		default:
			b.WriteString(ref.Text)
			v.NotLiteral = v.NotLiteral || w.retyped(ref) || strings.Contains(ref.Text, "${{")
			if len(pieces) == 1 {
				v.At.Line = ref.Line
			}
		}
		if b.Len() > int(w.limit) {
			return v, fmt.Errorf("%s: a value holds more than %d bytes, the extractor's max_bytes", w.file, w.limit)
		}
	}
	v.Text = b.String()
	return v, nil
}
