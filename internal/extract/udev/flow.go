package udev

import (
	"slices"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// stepWork is the work of one GOTO a way to a rule passes, each time the
// reader carries the way past a rule or compares two ways.
const stepWork = 8

// resolveGotos is, for each rule, the index of the rule its GOTO jumps to,
// or -1: the first rule after it in the file whose LABEL is the GOTO's, as
// rule_resolve_goto finds it. A GOTO with no such LABEL is passed over.
func resolveGotos(rules []*rule) []int {
	labels := map[string][]int{}
	for i, r := range rules {
		if r.hasLabel {
			labels[r.label] = append(labels[r.label], i)
		}
	}
	to := make([]int, len(rules))
	for i, r := range rules {
		to[i] = -1
		at := labels[r.gotoTo]
		if j, _ := slices.BinarySearch(at, i+1); r.hasGoto && j < len(at) {
			to[i] = at[j]
		}
	}
	return to
}

// step is one GOTO on a way to a rule: the index of its rule, and whether
// udev jumped there or went on past it.
type step struct {
	at    int
	taken bool
}

// way is one way udev reaches a rule: the GOTOs before it whose rule
// matched, in the order udev reads them, each jumped or not. A GOTO with
// no match key always jumps and adds no step.
type way []step

// flow is the ways udev may reach the rule it is at, in one file: none for
// a rule no way reaches, and the ways that jump to each rule further on.
type flow struct {
	rules   []*rule
	to      []int
	ways    []way
	pending map[int][]way
	meter   *core.Meter
}

// newFlow is the flow of rules, whose GOTOs jump to the rules to says,
// at the first rule.
func newFlow(rules []*rule, to []int, m *core.Meter) *flow {
	return &flow{rules: rules, to: to, ways: []way{nil}, pending: map[int][]way{}, meter: m}
}

// enter is the ways udev reaches rule i: from the rule before it and by
// the jumps to it, the two ways of one GOTO, jumped and not, taken as one.
func (fl *flow) enter(i int) ([]way, error) {
	fl.ways = append(fl.ways, fl.pending[i]...)
	delete(fl.pending, i)
	var err error
	fl.ways, err = fl.merge(fl.ways)
	return fl.ways, err
}

// merge is ways with each two that differ only in their last GOTO, one
// jumped and one not, taken as the way before it, and each way once.
func (fl *flow) merge(ways []way) ([]way, error) {
	for {
		i, j, ok, err := fl.pair(ways)
		if err != nil || !ok {
			return ways, err
		}
		if !slices.Equal(ways[i], ways[j]) {
			ways[i] = ways[i][:len(ways[i])-1]
		}
		ways = slices.Delete(ways, j, j+1)
	}
}

// pair is two ways merge takes as one, j after i: two the same, or two
// halves; ok is false for none.
func (fl *flow) pair(ways []way) (i, j int, ok bool, err error) {
	for i := range ways {
		for j := i + 1; j < len(ways); j++ {
			if err := fl.meter.Charge(stepWork * (1 + len(ways[i]))); err != nil {
				return 0, 0, false, err
			}
			if slices.Equal(ways[i], ways[j]) || halves(ways[i], ways[j]) {
				return i, j, true, nil
			}
		}
	}
	return 0, 0, false, nil
}

// halves reports two ways alike up to their last GOTO, the same one: pair
// asks it of two ways not the same, so one jumped there and the other did
// not.
func halves(a, b way) bool {
	n := len(a)
	return n > 0 && n == len(b) && slices.Equal(a[:n-1], b[:n-1]) && a[n-1].at == b[n-1].at
}

// leave carries the ways past rule i, r: a GOTO udev reaches jumps on each
// way where its match keys hold and goes on where they do not; one with no
// match key always jumps.
func (fl *flow) leave(i int, r *rule) error {
	to := fl.to[i]
	if to < 0 || len(fl.ways) == 0 {
		return nil
	}
	if err := fl.charge(fl.ways); err != nil {
		return err
	}
	if len(r.matches) == 0 {
		fl.pending[to] = append(fl.pending[to], fl.ways...)
		fl.ways = nil
		return nil
	}
	past := make([]way, len(fl.ways))
	for k, w := range fl.ways {
		fl.pending[to] = append(fl.pending[to], append(slices.Clip(w), step{i, true}))
		past[k] = append(slices.Clip(w), step{i, false})
	}
	fl.ways = past
	return nil
}

// charge charges the meter for carrying ways past a rule: a copy of each.
func (fl *flow) charge(ways []way) error {
	n := 0
	for _, w := range ways {
		n += 1 + len(w)
	}
	return fl.meter.Charge(stepWork * n)
}

// conds are the tests w adds to a rule, outermost first: for a GOTO
// jumped, each of its match keys; for one not, past's test.
func (fl *flow) conds(w way) []cond {
	var out []cond
	for _, s := range w {
		ms := fl.rules[s.at].matches
		if !s.taken {
			out = append(out, past(ms))
			continue
		}
		for _, m := range ms {
			out = append(out, cond{text: m.String(), tok: m})
		}
	}
	return out
}

// gotoLines names the lines of the GOTOs on ways, for a message.
func (fl *flow) gotoLines(ways []way) string {
	var lines []int
	for _, w := range ways {
		for _, s := range w {
			lines = append(lines, fl.rules[s.at].gotoLine)
		}
	}
	slices.Sort(lines)
	var out []string
	for _, n := range slices.Compact(lines) {
		out = append(out, strconv.Itoa(n))
	}
	return strings.Join(out, ", ")
}

// cond is one test a rule is reached under: a match key of the rule, or
// what a GOTO before it tests. tok is the single key and value it tests,
// nil for a GOTO's several keys not all holding, which no single key
// states.
type cond struct {
	text string
	tok  *token
}

// past is the test that holds after a GOTO whose rule tests matches does
// not jump: one key with the operator turned, or for several "not" before
// them.
func past(matches []*token) cond {
	if len(matches) == 1 {
		t := *matches[0]
		t.op = opMatch + opNoMatch - t.op
		return cond{text: t.String(), tok: &t}
	}
	texts := make([]string, len(matches))
	for i, t := range matches {
		texts[i] = t.String()
	}
	return cond{text: "not (" + strings.Join(texts, ", ") + ")"}
}
