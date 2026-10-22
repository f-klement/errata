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
