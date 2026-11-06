package semver

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

// pep440Pattern is the version pattern of the PyPA's version specifiers
// specification, Appendix: parsing version strings, which the packaging
// library uses. parsePEP440 reads by hand what it matches, in one pass;
// this test holds the two together.
var pep440Pattern = regexp.MustCompile(`(?i)^v?` +
	`(?:([0-9]+)!)?` +
	`([0-9]+(?:\.[0-9]+)*)` +
	`(?:[-_.]?(alpha|a|beta|b|preview|pre|c|rc)[-_.]?([0-9]+)?)?` +
	`(?:-([0-9]+)|[-_.]?(post|rev|r)[-_.]?([0-9]+)?)?` +
	`(?:[-_.]?(dev)[-_.]?([0-9]+)?)?` +
	`(?:\+([a-z0-9]+(?:[-_.][a-z0-9]+)*))?$`)

var pep440Normal = map[string]string{"a": "a", "alpha": "a", "b": "b", "beta": "b", "c": "rc", "pre": "rc", "preview": "rc", "rc": "rc"}

// pep440Oracle is parsePEP440 by the pattern.
func pep440Oracle(t string) (Version, bool) {
	m := pep440Pattern.FindStringSubmatch(t)
	if m == nil {
		return Version{}, false
	}
	v := Version{epoch: "0", syn: codePEP440}
	if m[1] != "" {
		v.epoch, _ = parseNum(m[1])
	}
	for _, part := range strings.Split(m[2], ".") {
		n, _ := parseNum(part)
		v.release = append(v.release, n)
	}
	if m[3] != "" {
		v.pre = []ident{{text: pep440Normal[strings.ToLower(m[3])]}, {numeric: true, text: string(numOrZero(m[4]))}}
	}
	switch {
	case m[5] != "":
		v.hasPost, v.post = true, numOrZero(m[5])
	case m[6] != "":
		v.hasPost, v.post = true, numOrZero(m[7])
	}
	if m[8] != "" {
		v.hasDev, v.dev = true, numOrZero(m[9])
	}
	if m[10] != "" {
		parts := strings.FieldsFunc(strings.ToLower(m[10]), func(r rune) bool { return r == '.' || r == '-' || r == '_' })
		for i, p := range parts {
			if n, ok := parseNum(p); ok {
				parts[i] = string(n)
			}
		}
		v.build = strings.Join(parts, ".")
	}
	return v, true
}

// rubyPattern is Gem::Version's ANCHORED_VERSION_PATTERN, and rubyRuns its
// segments; parseRuby reads them by hand.
var (
	rubyPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9a-zA-Z]+)*(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	rubyRuns    = regexp.MustCompile(`[0-9]+|[a-zA-Z]+`)
)

func rubyOracle(t string) (Version, bool) {
	if !rubyPattern.MatchString(t) {
		return Version{}, false
	}
	v := Version{epoch: "0", syn: codeRubyGems}
	for _, seg := range rubyRuns.FindAllString(strings.ReplaceAll(t, "-", ".pre."), -1) {
		n, isNum := parseNum(seg)
		switch {
		case isNum && v.pre == nil:
			v.release = append(v.release, n)
		case isNum:
			v.pre = append(v.pre, ident{numeric: true, text: string(n)})
		default:
			v.pre = append(v.pre, ident{text: seg})
		}
	}
	return v, true
}

// draws are n strings of up to 7 tokens from tokens, the same on every run.
func draws(n int, tokens []string) []string {
	r := rand.New(rand.NewPCG(133, 440))
	out := make([]string, n)
	for i := range out {
		var b strings.Builder
		for range 1 + r.IntN(7) {
			b.WriteString(tokens[r.IntN(len(tokens))])
		}
		out[i] = b.String()
	}
	return out
}

// sameVersion is whether a and b are one version, written and compared
// alike.
func sameVersion(a, b Version) bool {
	return a.String() == b.String() && Compare(a, b) == 0 && a.build == b.build && a.Partial() == b.Partial()
}

// parsePEP440 accepts what the specification's pattern matches, and reads
// it as the pattern's groups say, on 200000 drawn strings of the pattern's
// own parts and a few others. The planted breaks: post's -N tried after
// the post words, and a separator before a local version's part accepted.
func TestPEP440ScanMatchesThePattern(t *testing.T) {
	tokens := []string{"1", "0", "12", "007", ".", "-", "_", "!", "+", "v", "V",
		"a", "alpha", "b", "beta", "c", "rc", "RC", "pre", "preview", "post", "rev", "r", "dev", "DEV",
		"x", "ubuntu", ".*", "abc", " "}
	for _, s := range draws(200_000, tokens) {
		want, ok := pep440Oracle(s)
		got, err := parsePEP440(s)
		if ok != (err == nil) || ok && !sameVersion(got, want) {
			t.Fatalf("%q: the scan reads %v, %v; the pattern %v, %v", s, got, err, want, ok)
		}
	}
}

// parseRuby accepts what Gem::Version's pattern matches and splits it into
// its segments as RubyGems does. The planted break: a - read as nothing.
func TestRubyScanMatchesThePattern(t *testing.T) {
	tokens := []string{"1", "0", "12", "007", ".", "-", "a", "pre", "RC", "beta", "x", "_", "+", " ", "--"}
	for _, s := range draws(200_000, tokens) {
		want, ok := rubyOracle(s)
		got, err := parseRuby(s)
		if ok != (err == nil) || ok && !sameVersion(got, want) {
			t.Fatalf("%q: the scan reads %v, %v; the pattern %v, %v", s, got, err, want, ok)
		}
	}
}
