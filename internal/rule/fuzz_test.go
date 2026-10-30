package rule

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// FuzzAssert compiles an arbitrary expression over one extractor and
// evaluates it over an arbitrary value, and calls each helper and string
// function on the value.
// A load error always names assert or the extractor, and no helper panics:
// CEL turns a panic in a binding into an "internal error", which fails here.
func FuzzAssert(f *testing.F) {
	for _, seed := range [][2]string{
		{"duration(v[0]) >= cron_interval(v[1]) && duration(v[0]) <= cron_interval(v[1]) * 2", "17 */6 * * *"},
		{"port_split(v[0]).host == port_split(v[1]).container", "127.0.0.1:8080:80/udp"},
		{"same_set(v, ['a']) || subset(v.map(x, int(x)), [1, 2])", "1"},
		{"v.all(a, v.all(b, a + b != ''))", "2d"},
		{"int(v[0]) * 2 > double(v[1]) * 2.0", "9223372036854775807"},
		{"cron_interval(v[0]) == duration('1h')", "H/15 H(0-3) * * MON-FRI"},
		// Each of these once made a helper panic, which CEL reported as an
		// internal error instead of the cause.
		{"cron_interval(v[0]) > duration('0s')", "1/9223372036854775807 * * * *"},
		{"subset([1], v.map(x, int(x)))", "a"},
		{"same_set(v, [v[5]])", "a"},
		{"semver(v[0]) < semver('2') && semver_satisfies(v[0], '^1.2 || ~> 3.2')", "1.2.3"},
		{"v[0].split(',').filter(b, b != '') == v[1].replace(' ', ',').split(',')", "loki,mimir"},
	} {
		f.Add(seed[0], seed[1])
	}
	// Every helper over the fuzzed value, through CEL, whatever expr is. One
	// expression each, so no helper's error hides another's panic, and the
	// set functions with an error in each argument.
	var helpers []*assertion
	for _, h := range []string{
		"cron_interval(v[0]) > duration('0s')", "duration(v[0]) > duration('0s')", "port_split(v[0]).host == ''",
		"int(v[0]) > 0", "uint(v[0]) > 0u", "double(v[0]) > 0.0",
		"timestamp('2020-03-08T12:00:00Z').getHours(v[0]) > 0",
		"same_set(v, v.map(x, x + v[0]))", "subset(v.map(x, int(x)), [1])", "subset([1], v.map(x, int(x)))",
		"same_set(v, [v[5]])", "same_set([v[5]], v)",
		"semver(v[0]) > semver('1')", "semver_satisfies(v[0], '>=1')", "semver_satisfies('1.2.3', v[0])",
		"size(v[0].split(v[0])) >= 0", "size(v[0].split('', 3)) >= 0", "v.join(v[0]) != 'x'",
		"v[0].replace(v[0], v[0] + v[0]) != 'x'", "v[0].replace('', v[0], 2) != 'x'",
	} {
		a, err := compile(h, []string{"v"}, limits{cost: 10_000, timeout: time.Second, grace: stopGrace})
		if err != nil {
			f.Fatal(err)
		}
		helpers = append(helpers, a)
	}
	f.Fuzz(func(t *testing.T, expr, value string) {
		for _, h := range helpers {
			if _, err := h.Eval(context.Background(), asExtracted(map[string][]string{"v": {value}}, nil, nil)); err != nil && strings.Contains(err.Error(), "internal error") {
				t.Fatalf("a helper over %q: %v", value, err)
			}
		}
		if d, err := cronInterval(value); err == nil && d <= 0 {
			t.Fatalf("cronInterval(%q) = %s", value, d)
		}
		if d, err := parseDuration(value); err != nil && d != 0 {
			t.Fatalf("parseDuration(%q) = %s beside %v", value, d, err)
		}
		if m, err := splitPort(value); err == nil && (m["container"] == "" || !slices.Contains(portProtocols, m["protocol"])) {
			t.Fatalf("splitPort(%q) = %v", value, m)
		}

		a, err := compile(expr, []string{"v"}, limits{cost: 10_000, timeout: time.Second, grace: stopGrace})
		if err != nil {
			if msg := err.Error(); !strings.HasPrefix(msg, "assert: ") && msg != `extractor "v" is declared but assert does not use it` {
				t.Fatalf("load error %q names neither assert nor the extractor", msg)
			}
			return
		}
		if _, err := a.Eval(context.Background(), asExtracted(map[string][]string{"v": {value, value}}, nil, nil)); err != nil && strings.Contains(err.Error(), "internal error") {
			t.Fatalf("%s over %q: %v", expr, value, err)
		}
	})
}
