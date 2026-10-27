package core

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// checkFilterSelectsLess fails unless p, evaluated on roots, selects a
// subsequence of what p selects with each filter replaced by [*]: a filter
// only drops members, and keeps the order of those it keeps. A path that
// stops at a limit either way proves nothing and passes.
func checkFilterSelectsLess(t *testing.T, p Path, roots []*Node) {
	t.Helper()
	got, err := p.Eval(roots...)
	if err != nil {
		return
	}
	wild := Path{text: p.text, sels: slices.Clone(p.sels)}
	for i, sel := range wild.sels {
		if sel.kind == selFilter {
			wild.sels[i] = selector{kind: selWild}
		}
	}
	all, err := wild.Eval(roots...)
	if err != nil {
		return
	}
	for _, n := range all {
		if len(got) > 0 && got[0] == n {
			got = got[1:]
		}
	}
	if len(got) > 0 {
		t.Fatalf("%s selects a node that the path with [*] for each filter does not, or out of order", p.text)
	}
}

// checkQuerySplits fails unless, at each filter of p's own segments, a
// query term and its negation split the members the filter's other terms
// keep: with the term the filter keeps some of them, with the term after !
// it keeps the rest, and each keeps them in order. A path that stops at a
// limit proves nothing and passes.
func checkQuerySplits(t *testing.T, p Path, roots []*Node) {
	t.Helper()
	for k, sel := range p.sels {
		if sel.kind != selFilter {
			continue
		}
		for i, c := range sel.filter {
			if !c.query {
				continue
			}
			stopped := false
			with := func(f []term) []*Node {
				sels := slices.Clone(p.sels[:k+1])
				sels[k] = selector{kind: selFilter, filter: f, cost: sel.cost}
				got, err := Path{text: p.text, sels: sels}.Eval(roots...)
				stopped = stopped || err != nil
				return got
			}
			flipped := slices.Clone(sel.filter)
			flipped[i].not = !c.not
			yes, no := with(sel.filter), with(flipped)
			rest := with(slices.Delete(slices.Clone(sel.filter), i, i+1))
			if stopped {
				continue
			}
			for _, n := range rest {
				switch {
				case len(yes) > 0 && yes[0] == n:
					yes = yes[1:]
				case len(no) > 0 && no[0] == n:
					no = no[1:]
				default:
					t.Fatalf("%s: a node the filter keeps without term %d is kept neither with it nor with it negated, or out of order", p.text, i+1)
				}
			}
			if len(yes) > 0 || len(no) > 0 {
				t.Fatalf("%s: term %d or its negation keeps a node the other terms do not", p.text, i+1)
			}
		}
	}
}

// checkTyped fails unless n, a scalar errata read, has the type and the
// value of want, the decoder's value of the same text.
func checkTyped(t *testing.T, format string, n *Node, want any) {
	t.Helper()
	if n.Shape != Scalar {
		return
	}
	d, ok := numberOf(n.Text)
	var wantType ScalarType
	switch w := want.(type) {
	case int, int64, uint64:
		wantType = TypeNumber
		exact, _ := parseDecimal(fmt.Sprint(w))
		if !ok || d != exact {
			t.Fatalf("%s %q: errata reads %+v, the decoder %v", format, n.Text, d, w)
		}
	case float64:
		wantType = TypeNumber
		switch {
		case math.IsInf(w, 0) || math.IsNaN(w):
			if ok {
				t.Fatalf("%s %q: errata reads %+v, the decoder %v", format, n.Text, d, w)
			}
		case !ok || asFloat(d) != w:
			t.Fatalf("%s %q: errata reads %+v, %v as a double, the decoder %v", format, n.Text, d, asFloat(d), w)
		}
	case bool:
		wantType = TypeBool
		if strings.EqualFold(n.Text, "true") != w {
			t.Fatalf("%s %q: errata reads %q, the decoder %v", format, n.Text, n.Text, w)
		}
	case nil:
		wantType = TypeNull
	default: // a string, a YAML timestamp, a TOML date or time
		wantType = TypeString
	}
	if n.Type != wantType {
		t.Fatalf("%s %q: errata types it %d, the decoder's value is %T", format, n.Text, n.Type, want)
	}
}

// asFloat is d rounded to a double, as ParseFloat rounds it.
func asFloat(d decimal) float64 {
	if d.digits == "" {
		return 0
	}
	f, _ := strconv.ParseFloat("0."+d.digits+"e"+d.exp, 64)
	if d.neg {
		return -f
	}
	return f
}
