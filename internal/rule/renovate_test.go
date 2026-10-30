package rule

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// renovate_match answers what Renovate's matchRegexOrGlob answers, for each
// case of testdata/renovate-cases.json: every pattern against every string,
// regexes, globs of every form minimatch reads and *, with the answer
// testdata/renovate-oracle.mjs gave from Renovate's own code, minimatch
// 10.2.6 and re2 1.27.0. Where Renovate throws, errata is unknown.
func TestRenovateMatchAgreesWithRenovate(t *testing.T) {
	data, err := os.ReadFile("testdata/renovate-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][3]any
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 6000 {
		t.Fatalf("%d cases; the file holds over 6000", len(cases))
	}
	matched, thrown := 0, 0
	for _, c := range cases {
		s, p := c[0].(string), c[1].(string)
		got, err := renovateMatches(s, p)
		if c[2] == "throw" {
			thrown++
			if err == nil || !strings.Contains(err.Error(), "minimatch throws on the glob") {
				t.Errorf("renovate_match(%q, %q) = %v, %v; Renovate throws", s, p, got, err)
			}
			continue
		}
		want := c[2].(bool)
		if err != nil || got != want {
			t.Errorf("renovate_match(%q, %q) = %v, %v; Renovate says %v", s, p, got, err, want)
		}
		if want {
			matched++
		}
	}
	// Both answers are common, so neither a matcher that always says true
	// nor one that always says false passes, and some globs throw.
	if matched < len(cases)/10 || matched > len(cases)*9/10 || thrown == 0 {
		t.Errorf("%d of %d cases match and %d throw", matched, len(cases), thrown)
	}
}

// renovate_match is a CEL helper: a rule over Argo CD's manager reads it
// as k3s does, every Application matched by one of the patterns.
func TestRenovateMatchInCEL(t *testing.T) {
	for _, c := range []struct {
		expr string
		want bool
	}{
		{`v.all(f, ['/^infra/argocd/applications/.*\\.yaml$/'].exists(p, renovate_match(f, p)))`, true},
		{`v.all(f, ['^infra/argocd/applications/.*\\.yaml$'].exists(p, renovate_match(f, p)))`, false},
		{`v.all(f, renovate_match(f, 'infra/argocd/**'))`, true},
		{`v.exists(f, renovate_match(f, '!/a\\.yaml$/'))`, true},
		{`renovate_match('anything at all', '*')`, true},
	} {
		got, err := eval(t, "size(v) == 2 && ("+c.expr+")",
			"infra/argocd/applications/a.yaml", "infra/argocd/applications/b.yaml")
		if err != nil || got != c.want {
			t.Errorf("%s = %v, %v; want %v", c.expr, got, err, c.want)
		}
	}
}

// What errata cannot compute as Renovate does makes the rule unknown,
// naming the pattern or the string and the reason.
func TestRenovateMatchRefuses(t *testing.T) {
	long := strings.Repeat("a", MaxArgument+1)
	for _, c := range []struct {
		s, p, want string
	}{
		{"a", "/(?=a)/", `"/(?=a)/" is written as a regex between slashes, and RE2 does not compile it`},
		{"a", "/\\u0061/", "RE2 does not compile it"},
		{"a x", "[[:alpha:]] x", `minimatch throws on the glob "[[:alpha:]] x", as JavaScript refuses the regex it builds: invalid regular expression`},
		{"a", strings.Repeat("{a,b}", 7), `the glob "` + strings.Repeat("{a,b}", 7) + `" is not matched against "a": its braces expand to more than 64 globs`},
		{"xa", "x" + strings.Repeat("!(a)", 12), "its tree passes 4096 nodes"},
		{strings.Repeat("a", 1000) + "b", strings.Repeat("*(a|aa)", 140), "matching takes more than the step budget"},
		{"\xff", "?", "a string that is not UTF-8"},
		{long, "*.yaml", "renovate_match: an argument is 1025 bytes; a helper reads at most 1024"},
		{"a", long, "renovate_match: an argument is 1025 bytes"},
	} {
		_, err := renovateMatches(c.s, c.p)
		if strings.Contains(c.want, "renovate_match:") {
			_, err = eval(t, "size(v) == 2 && renovate_match(v[0], v[1])", c.s, c.p)
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("renovate_match(%q, %q): error %v, want one containing %q", c.s, c.p, err, c.want)
		}
	}
	// * is every string, before anything is read.
	if got, err := renovateMatches("\xff", "*"); err != nil || !got {
		t.Errorf(`renovate_match("\\xff", "*") = %v, %v; want true`, got, err)
	}
}

// A call is charged its bytes and their product over renovateCostDivisor.
// 100 calls of a string and a glob of 1024 bytes each cost 204900 for
// their bytes, under the limit, and 1638400 more for their products, which
// the cost limit stops after about 60 calls. 10 such calls cost about
// 184000 and run: a divisor much smaller than 64 would stop them. The two
// cases bound the divisor to roughly 11 to 131; they do not pin it.
func TestRenovateMatchIsCharged(t *testing.T) {
	long := strings.Repeat("a", MaxArgument)
	const ten = "[0, 1, 2, 3, 4, 5, 6, 7, 8, 9]"
	_, err := eval(t, "size(v) == 2 && "+ten+".all(i, "+ten+".all(j, renovate_match(v[0], v[1])))", long, "*"+long[1:])
	want := "the evaluation exceeded the cost limit of 1000000"
	if err == nil || err.Error() != want {
		t.Errorf("100 calls of 2048 bytes: error %v, want %q", err, want)
	}
	if got, err := eval(t, "size(v) == 2 && "+ten+".all(i, renovate_match(v[0], v[1]))", long, "*"+long[1:]); err != nil || !got {
		t.Errorf("10 calls of 2048 bytes: %v, %v; want true", got, err)
	}
}

// A string or a pattern of exactly MaxArgument bytes is read.
func TestRenovateMatchReadsAnArgumentAtTheBound(t *testing.T) {
	long := strings.Repeat("a", MaxArgument)
	for _, c := range []struct {
		s, p string
		want bool
	}{{long, "*a", true}, {"a", long, false}} {
		got, err := eval(t, "size(v) == 2 && renovate_match(v[0], v[1])", c.s, c.p)
		if err != nil || got != c.want {
			t.Errorf("renovate_match of %d and %d bytes: %v, %v; want %v", len(c.s), len(c.p), got, err, c.want)
		}
	}
}
