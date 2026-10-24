package semver

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func mustVersion(t *testing.T, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

func mustRange(t *testing.T, s string) Range {
	t.Helper()
	r, err := ParseRange(s)
	if err != nil {
		t.Fatalf("ParseRange(%q): %v", s, err)
	}
	return r
}

// Each ecosystem's way of writing a version reads as the version it names.
func TestParseVersion(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"20", "20"},
		{"20.11.1", "20.11.1"},
		{"v20.11", "20.11"},
		{" 1.2.3\n", "1.2.3"},
		{"007.010", "7.10"},
		{"1.0.0-alpha.1", "1.0.0-alpha.1"},
		{"1.0.0-rc1", "1.0.0-rc1"},
		{"1.0.0+build.5", "1.0.0+build.5"},
		// Go modules.
		{"v1.2.3+incompatible", "1.2.3+incompatible"},
		{"v0.0.0-20191109021931-daa7c04131f5", "0.0.0-20191109021931-daa7c04131f5"},
		{"go1.22.1", "1.22.1"},
		{"go1.22rc1", "1.22-rc.1"},
		// PEP 440.
		{"3.12.0rc1", "3.12.0-rc.1"},
		{"1.0a1", "1.0-a.1"},
		{"1!2.0", "1!2.0"},
		{"1.0.post2", "1.0.post2"},
		{"1.0.dev0", "1.0.dev0"},
		{"1.0a1.post2.dev3", "1.0.a.1.post2.dev3"},
		{"1.0.post", "1.0.post0"},
		{"1.0+ubuntu.1", "1.0+ubuntu.1"},
		// RubyGems.
		{"3.3.0.preview1", "3.3.0-preview.1"},
		{"1.0.0.pre", "1.0.0-pre"},
		{"7.1.0.beta.2", "7.1.0-beta.2"},
		// Words in any case, kept as written (correctness delta review, D1).
		{"1.0RC1", "1.0-RC.1"},
		{"3.3.0.Preview1", "3.3.0-Preview.1"},
		{"1.0.0.PRE", "1.0.0-PRE"},
		{"1.0.POST2", "1.0.post2"},
		{"1.0.Dev3", "1.0.dev3"},
	} {
		if got := mustVersion(t, c.in).String(); got != c.want {
			t.Errorf("ParseVersion(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// A value that is not a version is refused with the reason, never read as
// some version.
func TestParseVersionRefuses(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "empty"},
		{"   ", "empty"},
		{"latest", "does not start with a number"},
		{"lts/iron", "does not start with a number"},
		{"v", "does not start with a number"},
		{"1.", "separator"},
		{"1..2", "separator"},
		{"1.0-", "empty part"},
		{"1.0-a..b", "empty part"},
		{"1.0-a_b", `'_'`},
		{"1.0+", "nothing follows the +"},
		{"1.0+a/b", `'/'`},
		{"x!1.0", "epoch"},
		{"1.0.dev1.post1", "comes after the post or dev release"},
		{"1.0_1", "follows the numbers"},
		{"1.0 2.0", "' '"},
		{"20.x", "x stands for a number"},
		// Text that starts with a number but names no version (correctness
		// review, finding 5).
		{"10ms", `"ms" after the numbers is not a pre-release, post or dev word`},
		{"1st", `"st" after the numbers`},
		{"1e10", `"e" after the numbers`},
		{"20H2", `"H" after the numbers`},
		{"2.0.0.racecar1", `"racecar" after the numbers`},
		{">=1.0", "does not start with a number"},
		{strings.Repeat("1", MaxLength+1), "longer than 1024 bytes"},
	} {
		_, err := ParseVersion(c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("ParseVersion(%q) = %v, want an error naming %q", c.in, err, c.want)
		}
	}
}

// ordered is a list of versions from lowest to highest, each one a rule of
// SemVer 2.0.0 or PEP 440 apart from the one before.
var ordered = []string{
	"0.0.0-0",
	"0.9",
	"1.0.0.dev1",
	"1.0.0-alpha",
	"1.0.0-alpha.1",
	"1.0.0-alpha.beta",
	"1.0.0-beta",
	"1.0.0-beta.2",
	"1.0.0-beta.11",
	"1.0.0-rc.1",
	"1.0.0",
	"1.0.0.post1.dev1",
	"1.0.0.post1",
	"1.0.0.post2",
	"1.0.1",
	"1.0.10",
	"1.1",
	"2",
	"18446744073709551616.0", // past uint64, still ordered
	"1!0.1",
}

func TestCompareOrders(t *testing.T) {
	for i, a := range ordered {
		for j, b := range ordered {
			want := cmpInt(i, j)
			if got := Compare(mustVersion(t, a), mustVersion(t, b)); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestCompareEqual(t *testing.T) {
	for _, pair := range [][2]string{
		{"20", "20.0.0"},
		{"v1.2.3", "1.2.3+incompatible"},
		{"1.0.0-rc.1", "1.0.0rc1"},
		{"0!1.0", "1.0"},
		{"1.0.0-01", "1.0.0-1"},
	} {
		if c := Compare(mustVersion(t, pair[0]), mustVersion(t, pair[1])); c != 0 {
			t.Errorf("Compare(%s, %s) = %d, want 0", pair[0], pair[1], c)
		}
	}
}

// The examples the owner decided on, and the precision rule around them.
func TestAgreeAt(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		agree bool
	}{
		{"20", "20.11.1", true},
		{"20", "v20.11", true},
		{"20.11.1", "v20.11", true},
		{"20", "18.19.0", false},
		{"20.11", "20.12.0", false},
		{"20.11", "20.12", false},
		{"20.11.1", "20.11.2", false},
		{"20.11.1", "20.11.1.0", true},
		{"20.11.1", "20.11.1.4", false},
		{"20", "20.0.0-rc.1", true},
		{"20.1.0-rc.1", "20.1.0", false},
		{"1!2.0", "2.0", false},
		{"1!2", "1!2.3.4", true},
		{"2", "1!2.3.4", false},
	} {
		a, b := mustVersion(t, c.a), mustVersion(t, c.b)
		if AgreeAt(a, b) != c.agree || AgreeAt(b, a) != c.agree {
			t.Errorf("AgreeAt(%s, %s) = %v and %v reversed, want %v", c.a, c.b, AgreeAt(a, b), AgreeAt(b, a), c.agree)
		}
	}
}

// syntax is one ecosystem's range syntax: each range with the intervals it
// reads as, and versions it admits and refuses.
type syntax struct {
	in, reads string
	yes, no   []string
}

var syntaxes = map[string][]syntax{
	"npm": {
		{"^20.1", ">=20.1.0 <21.0.0-0", []string{"20.1.0", "20.99.0", "20"}, []string{"20.0.9", "21.0.0", "21.0.0-rc.1", "19"}},
		{"^0.2.3", ">=0.2.3 <0.3.0-0", []string{"0.2.9"}, []string{"0.3.0"}},
		{"^0.0.3", ">=0.0.3 <0.0.4-0", []string{"0.0.3"}, []string{"0.0.4"}},
		{"^0.0", ">=0.0.0 <0.1.0-0", []string{"0.0.9"}, []string{"0.1.0"}},
		{"~1.2.3", ">=1.2.3 <1.3.0-0", []string{"1.2.9"}, []string{"1.3.0", "1.2.2"}},
		{"~1", ">=1.0.0 <2.0.0-0", []string{"1.9.9"}, []string{"2.0.0"}},
		{">=20", ">=20.0.0", []string{"20.0.0", "22", "20"}, []string{"19.9.9", "18"}},
		{">1.2", ">=1.3.0", []string{"1.3.0"}, []string{"1.2.9"}},
		{"<1.2", "<1.2.0-0", []string{"1.1.9"}, []string{"1.2.0", "1.2.0-rc.1"}},
		{"<=1.2", "<1.3.0-0", []string{"1.2.9"}, []string{"1.3.0"}},
		{"1.2.x", ">=1.2.0 <1.3.0-0", []string{"1.2.5", "1.2"}, []string{"1.3.0"}},
		{"1.X", ">=1.0.0 <2.0.0-0", []string{"1.5.0"}, []string{"2.0.0"}},
		{"*", "*", []string{"0.0.1", "99"}, nil},
		{"1.2.3 - 2.3", ">=1.2.3 <2.4.0-0", []string{"2.3.9", "1.2.3"}, []string{"2.4.0", "1.2.2"}},
		{">=1.0.0 <2.0.0 || >=3", ">=1.0.0 <2.0.0 || >=3.0.0", []string{"1.5.0", "3.1.0"}, []string{"2.5.0"}},
		{">= 20", ">=20.0.0", []string{"21.0.0"}, []string{"19.0.0"}},
		{"=1.2.3", "=1.2.3", []string{"1.2.3"}, []string{"1.2.4"}},
		{"v1.2.3", "=1.2.3", []string{"1.2.3"}, []string{"1.2.4"}},
	},
	"Cargo": {
		{"^1.2.3", ">=1.2.3 <2.0.0-0", []string{"1.9.0"}, []string{"2.0.0"}},
		{">=1.2, <1.5", ">=1.2.0 <1.5.0-0", []string{"1.4.9"}, []string{"1.5.0", "1.1.0"}},
		{"~1.2", ">=1.2.0 <1.3.0-0", []string{"1.2.7"}, []string{"1.3.0"}},
		{"=1.2.3", "=1.2.3", []string{"1.2.3"}, []string{"1.2.4"}},
		{"1.*", ">=1.0.0 <2.0.0-0", []string{"1.4.0"}, []string{"2.0.0"}},
	},
	"PEP 440": {
		{"~=2.2", ">=2.2.0 <3.0.0-0", []string{"2.9", "2.2.0"}, []string{"3.0", "2.1"}},
		{"~=1.4.5", ">=1.4.5 <1.5.0-0", []string{"1.4.9"}, []string{"1.5.0"}},
		{"==1.2", "=1.2.0", []string{"1.2.0"}, []string{"1.2.1"}},
		{"==1.2.*", ">=1.2.0 <1.3.0-0", []string{"1.2.7"}, []string{"1.3.0"}},
		{"!=1.5", "<1.5.0 || >1.5.0", []string{"1.4.0", "1.5.1"}, []string{"1.5.0"}},
		{"!=1.5.*", "<1.5.0 || >=1.6.0-0", []string{"1.4.0", "1.6.0"}, []string{"1.5.3"}},
		{">=3.8, !=3.9.0, <4", ">=3.8.0 <3.9.0 || >3.9.0 <4.0.0-0", []string{"3.8.1", "3.10"}, []string{"3.9.0", "4.0"}},
		{">=1.0rc1", ">=1.0.0-rc.1", []string{"1.0rc2", "1.0"}, []string{"1.0b1"}},
	},
	"Go modules": {
		{"v1.2.3", "=1.2.3", []string{"1.2.3", "v1.2.3+incompatible"}, []string{"1.2.4"}},
		{">=v1.21", ">=1.21.0", []string{"go1.22.1"}, []string{"1.20.0"}},
	},
	"RubyGems": {
		{"~> 3.2", ">=3.2.0 <4.0.0-0", []string{"3.9", "3.2.0"}, []string{"4.0", "3.1"}},
		{"~> 3.2.1", ">=3.2.1 <3.3.0-0", []string{"3.2.5"}, []string{"3.3.0"}},
		{"~> 3", ">=3.0.0 <4.0.0-0", []string{"3.9"}, []string{"4.0"}},
		{"~> 3.2, >= 3.2.1", ">=3.2.1 <4.0.0-0", []string{"3.2.1"}, []string{"3.2.0"}},
	},
}

// Each ecosystem's range syntax reads as the intervals it means, and admits
// and refuses versions by them.
func TestRangeSyntaxes(t *testing.T) {
	for name, cases := range syntaxes {
		t.Run(name, func(t *testing.T) {
			for _, c := range cases {
				r := mustRange(t, c.in)
				if got := r.String(); got != c.reads {
					t.Errorf("%q reads as %s, want %s", c.in, got, c.reads)
				}
				for _, v := range c.yes {
					if !Satisfies(mustVersion(t, v), r) {
						t.Errorf("%s does not satisfy %q", v, c.in)
					}
				}
				for _, v := range c.no {
					if Satisfies(mustVersion(t, v), r) {
						t.Errorf("%s satisfies %q", v, c.in)
					}
				}
			}
		})
	}
}

// A pre-release satisfies a range only through a comparator that names a
// pre-release of its numbers, as npm and Cargo decide.
func TestSatisfiesPrerelease(t *testing.T) {
	for _, c := range []struct {
		v, r string
		want bool
	}{
		{"2.0.0-beta.1", ">=1.0.0", false},
		{"1.2.3-beta.2", ">=1.2.3-beta.1", true},
		{"1.2.4-beta.2", ">=1.2.3-beta.1", false},
		{"1.2.3-beta.2", "^1.2.3-beta.1", true},
		{"1.0.dev1", ">=0.9", false},
		{"20", ">=20.0.0-rc.1 <20.0.0", false}, // a partial version stands for releases only
	} {
		if got := Satisfies(mustVersion(t, c.v), mustRange(t, c.r)); got != c.want {
			t.Errorf("Satisfies(%s, %q) = %v, want %v", c.v, c.r, got, c.want)
		}
	}
}

func TestParseRangeRefuses(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "empty"},
		{">=1 ||", "alternative beside || is empty"},
		{"===1.0", "=== compares text"},
		{">=", "has no version after it"},
		{"~=1", "at least two numbers"},
		{"~>1.x", "takes a version, not a wildcard"},
		{">*", "admits no version"},
		{"1.x.2", "a number follows a wildcard"},
		{"vx", "a v stands before a version, not a wildcard"},
		{">=v*", "a v stands before a version, not a wildcard"},
		{">=latest", `"latest" is not a version`},
		{strings.Repeat(" ", MaxLength+1), "longer than"},
	} {
		_, err := ParseRange(c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("ParseRange(%q) = %v, want an error naming %q", c.in, err, c.want)
		}
	}
}

func TestOverlap(t *testing.T) {
	for _, c := range []struct {
		ranges []string
		want   bool
	}{
		{[]string{">=20", "^20.1"}, true},
		{[]string{">=20", "<20"}, false},
		{[]string{"^1.2", "~1.5"}, true},
		{[]string{"^1.2", "^2"}, false},
		{[]string{">=1.0.0 <1.0.0"}, false},
		{[]string{"<=1.0.0", ">=1.0.0"}, true},
		{[]string{"<1.0.0", ">=1.0.0"}, false},
		{[]string{"<2.0.0", ">=2.0.0-alpha"}, false}, // they share only pre-releases
		{[]string{">1.0.0", "<1.0.1"}, true},         // 1.0.0.post1 lies between
		{[]string{">1.0.0", "<=1.0.0"}, false},
		// Post and dev releases beside a bound (correctness review, finding 3).
		{[]string{">1.0.0", "<1.0.0.post5"}, true},
		{[]string{">1.0.0.post1, <1.0.0.post3"}, true},
		{[]string{">1.0.0.post1, <1.0.0.post2"}, false},
		{[]string{">=1.0.post1.dev1, <1.0.post1"}, false},
		{[]string{">=1.0.post1.dev1, <=1.0.post1"}, true},
		{[]string{">=1.0a1.post1, <1.0"}, false},
		{[]string{">=1.0.dev1, <=1.0"}, true},
		{[]string{"^1 || ^3", "^2 || ^3", ">=3.5"}, true},
		{[]string{"^1 || ^3", "^2 || ^4"}, false},
		// Two alternatives of one range that overlap cover it once, not twice.
		{[]string{"^1 || ~1.2", ">=3"}, false},
		{[]string{"^1 || ~1.2", "~1.2.5"}, true},
		{[]string{"!=1.5", ">=1.5.0, <=1.5.0"}, false},
		{[]string{"!=1.5", ">=1.5, <=1.5"}, true}, // <=1.5 is every 1.5 release, as npm reads it
		{[]string{"*", "~> 3.2", ">=3.2.1"}, true},
		{[]string{"<0.0.0-alpha"}, false},
		{[]string{"<0.0.1"}, true},
	} {
		var rs []Range
		for _, s := range c.ranges {
			rs = append(rs, mustRange(t, s))
		}
		if got := Overlap(rs); got != c.want {
			t.Errorf("Overlap(%q) = %v, want %v", c.ranges, got, c.want)
		}
	}
}

func TestAgree(t *testing.T) {
	for _, c := range []struct {
		values        []string
		holds, ranges bool
		first, second int
	}{
		{[]string{"20", "20.11.1", "v20.11"}, true, false, -1, -1},
		{[]string{"20", "18.19.0"}, false, false, 0, 1},
		{[]string{"20.11", "20", "20.12"}, false, false, 0, 2},
		{[]string{"20.11.1", "20", "20.11.2"}, false, false, 0, 2},
		{[]string{"20.11.1", ">=20", "^20.1"}, true, false, -1, -1},
		{[]string{"18.19.0", ">=20"}, false, false, 0, 1},
		{[]string{">=20", "20.11", "^20.12"}, false, false, 1, 2},
		{[]string{"20", "^20.1"}, true, false, -1, -1},
		{[]string{">=20", "^20.1"}, true, true, -1, -1},
		{[]string{">=21", "^20.1"}, false, true, -1, -1},
		{[]string{"~> 3.2", "3.3.0", ">= 3.2.1, < 4"}, true, false, -1, -1},
	} {
		var vs []Value
		for _, s := range c.values {
			v, err := Parse(s)
			if err != nil {
				t.Fatalf("Parse(%q): %v", s, err)
			}
			vs = append(vs, v)
		}
		got := Agree(vs)
		want := Outcome{Holds: c.holds, OnlyRanges: c.ranges, First: c.first, Second: c.second}
		if got != want {
			t.Errorf("Agree(%q) = %+v, want %+v", c.values, got, want)
		}
	}
}

func TestParseValue(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"20", "version 20"},
		{"v20.11.1", "version 20.11.1"},
		{"20.x", "range >=20.0.0 <21.0.0-0"},
		{"^20.1", "range >=20.1.0 <21.0.0-0"},
	} {
		v, err := Parse(c.in)
		if err != nil || v.String() != c.want {
			t.Errorf("Parse(%q) = %s, %v; want %s", c.in, v, err, c.want)
		}
	}
	if _, err := Parse("lts/*"); err == nil || !strings.Contains(err.Error(), "neither a version nor a range") {
		t.Errorf("Parse(lts/*) = %v, want neither a version nor a range", err)
	}
	if (Value{}).String() != "nothing" {
		t.Error("the zero Value names something")
	}
}

// worstRange is a range of MaxLength bytes or just under, of as many
// alternatives as fit, each its own interval, offset by k so that ranges
// built with different k interleave.
func worstRange(k int) string {
	var b strings.Builder
	for i := 0; ; i++ {
		alt := fmt.Sprintf("~%d.%d", i, k)
		if b.Len()+len(alt)+2 > MaxLength {
			return b.String()
		}
		if i > 0 {
			b.WriteString("||")
		}
		b.WriteString(alt)
	}
}

// readAll is the rule's path: a Reader over every text, stopping at the
// bound, then Agree over what it read.
func readAll(texts []string) (Outcome, error) {
	var r Reader
	values := make([]Value, 0, len(texts))
	for _, s := range texts {
		v, err := r.Parse(s)
		if err != nil {
			return Outcome{}, err
		}
		values = append(values, v)
	}
	return Agree(values), nil
}

// hostile is n distinct values of about MaxLength bytes each, made by shape
// from the value's index.
func hostile(n int, shape func(i int) string) []string {
	texts := make([]string, n)
	for i := range texts {
		texts[i] = shape(i)
	}
	return texts
}

// notEqualChain is a range of != comparators up to MaxLength bytes, the
// shape that holds the most intervals for its length, offset by i so every
// value is distinct.
func notEqualChain(i int) string {
	var b strings.Builder
	for k := 0; ; k++ {
		c := fmt.Sprintf("!=%d.%d ", k, i)
		if b.Len()+len(c) > MaxLength {
			return b.String()
		}
		b.WriteString(c)
	}
}

// foldedChain is a range of >= comparators up to MaxLength bytes, which
// folds into one interval: the most parse work for the fewest intervals.
func foldedChain(i int) string {
	var b strings.Builder
	for k := 0; ; k++ {
		c := fmt.Sprintf(">=%d.%d ", k, i)
		if b.Len()+len(c) > MaxLength {
			return b.String()
		}
		b.WriteString(c)
	}
}

// namedChain is a range of >= comparators that each name a pre-release, up
// to MaxLength bytes, folding into one interval. With distinct, each names
// another release, so each counts toward the bound; without, they name
// pre-releases of one release, which is kept once.
func namedChain(distinct bool) func(i int) string {
	return func(i int) string {
		var b strings.Builder
		for k := 0; ; k++ {
			c := fmt.Sprintf(">=0.%d.0-%d ", i, k)
			if distinct {
				c = fmt.Sprintf(">=0.%d.%d-0 ", i, k)
			}
			if b.Len()+len(c) > MaxLength {
				return b.String()
			}
			b.WriteString(c)
		}
	}
}

// BenchmarkHostileNamedDistinct is 100000 values of pre-releases of
// distinct releases, beside one version: the Reader refuses at the bound.
func BenchmarkHostileNamedDistinct(b *testing.B) {
	texts := append([]string{"9999.0.0"}, hostile(100_000, namedChain(true))...)
	b.ResetTimer()
	for range b.N {
		if _, err := readAll(texts); err == nil {
			b.Fatal("the bound did not refuse")
		}
	}
}

// BenchmarkHostileNamedOneRelease is 100000 values of pre-releases of one
// release each, beside one version: every value is read, each keeping one.
func BenchmarkHostileNamedOneRelease(b *testing.B) {
	texts := append([]string{"9999.0.0"}, hostile(100_000, namedChain(false))...)
	b.ResetTimer()
	for range b.N {
		out, err := readAll(texts)
		if err != nil || !out.Holds {
			b.Fatal(out, err)
		}
	}
}

// BenchmarkHostileRangesAlone is 100000 distinct != chains, the most values
// one extractor returns, compared with no version: the Reader refuses at
// MaxIntervals, after about 5000 of them.
func BenchmarkHostileRangesAlone(b *testing.B) {
	texts := hostile(100_000, notEqualChain)
	b.ResetTimer()
	for range b.N {
		if _, err := readAll(texts); err == nil {
			b.Fatal("the bound did not refuse")
		}
	}
}

// BenchmarkHostileRangesAtBound is as many distinct worstRange values as
// stay within MaxIntervals, compared alone, so the sweep runs in full.
func BenchmarkHostileRangesAtBound(b *testing.B) {
	texts := hostile(MaxIntervals/150, worstRange)
	b.ResetTimer()
	for range b.N {
		if _, err := readAll(texts); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkHostileFoldedWithVersion is 100000 distinct ranges that fold to
// one interval each, beside one version that satisfies them all: the parse
// of every byte and a Satisfies for every range.
func BenchmarkHostileFoldedWithVersion(b *testing.B) {
	texts := append(hostile(100_000, foldedChain), "9999.0.0")
	b.ResetTimer()
	for range b.N {
		out, err := readAll(texts)
		if err != nil || !out.Holds {
			b.Fatal(out, err)
		}
	}
}

// BenchmarkParseRange is one range of 1024 bytes.
func BenchmarkParseRange(b *testing.B) {
	s := worstRange(3)
	for range b.N {
		if _, err := ParseRange(s); err != nil {
			b.Fatal(err)
		}
	}
}

// A Reader refuses the value that takes its ranges past the bound, before
// any value after it is parsed, with versions among the values or not. The
// test lowers the bound to 3, since a million intervals is too slow for
// every mutant. The planted break: the count left out, which reads all four
// ranges.
func TestReaderRefusesPastMaxIntervals(t *testing.T) {
	if MaxIntervals != 1_000_000 || maxIntervals != MaxIntervals {
		t.Fatalf("the bound is %d and %d, want 1000000, as the README states", MaxIntervals, maxIntervals)
	}
	maxIntervals = 3
	t.Cleanup(func() { maxIntervals = MaxIntervals })
	var r Reader
	for i, s := range []string{"1.5.0", ">=1", "^1 || ^2"} {
		if _, err := r.Parse(s); err != nil {
			t.Fatalf("value %d, %q, at the bound: %v", i, s, err)
		}
	}
	_, err := r.Parse(">=1")
	var bound *BoundError
	if !errors.As(err, &bound) || err.Error() != "the ranges of the first 4 values hold more than 3 intervals, the most errata compares in one rule" {
		t.Errorf("past the bound: %v", err)
	}
	// A value that is not a range, or not read, counts no interval.
	var fresh Reader
	for _, s := range []string{"1", "2", "lts/*", "3", ">=1 <2 !=1.5"} {
		if _, err := fresh.Parse(s); err != nil && s != "lts/*" {
			t.Errorf("%q: %v", s, err)
		}
	}
}

// fold is linear in the comparators: a range of 1024 bytes of != takes a
// bounded number of bytes, where intersecting one comparator at a
// time took the square (security review, finding 1). The planted break:
// fold intersecting a list with each comparator in turn.
func TestFoldIsLinear(t *testing.T) {
	var parts []string
	for i := 0; len(strings.Join(parts, " ")) < MaxLength-8; i++ {
		parts = append(parts, fmt.Sprintf("!=%d", i))
	}
	s := strings.Join(parts, " ")
	r, err := ParseRange(s)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.alts[0].ivs); n != len(parts)+1 {
		t.Fatalf("%d intervals for %d holes", n, len(parts))
	}
	// Each interval is about 300 bytes, so a linear parse allocates tens of
	// kilobytes per hundred holes; the square allocated 8 MB.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, _ = ParseRange(s)
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
		t.Errorf("%d bytes allocated for %d comparators", n, len(parts))
	}
}

// cut takes holes out in any order, overlapping or not, and at either end.
func TestCut(t *testing.T) {
	for _, c := range []struct{ in, reads string }{
		{">=1 <5 !=3 !=2 !=4", ">=1.0.0 <2.0.0 || >2.0.0 <3.0.0 || >3.0.0 <4.0.0 || >4.0.0 <5.0.0-0"},
		{">=1 <5 !=1.0.0 !=1.*", ">=2.0.0-0 <5.0.0-0"},
		{"!=2.* !=2.1.*", "<2.0.0 || >=3.0.0-0"},
		{">=1 <2 !=0.5 !=7", ">=1.0.0 <2.0.0-0"},
		{">=1 <=1 !=1", ">1.0.0 <2.0.0-0"},
		{">=1.0.0 <=1.0.0 !=1.0.0", "(no version)"},
		{"!=1.0.0 !=1.0.0", "<1.0.0 || >1.0.0"},
	} {
		if got := mustRange(t, c.in).String(); got != c.reads {
			t.Errorf("%q reads as %s, want %s", c.in, got, c.reads)
		}
	}
}

// An error quotes at most maxToken bytes of the value, and a range's
// String of a long value stays in proportion to it.
func TestErrorsQuoteLittle(t *testing.T) {
	long := ">=" + strings.Repeat("\x01", 300)
	_, err := Parse(long)
	if err == nil || len(err.Error()) > 4*maxToken+200 || !strings.Contains(err.Error(), "bytes)") {
		t.Errorf("error of %d bytes: %v", len(fmt.Sprint(err)), err)
	}
	if got := quoteToken(strings.Repeat("é", 40)); got != `"`+strings.Repeat("é", 32)+`"... (80 bytes)` {
		t.Errorf("quoteToken cuts inside a rune: %s", got)
	}
	if got := quoteToken(strings.Repeat("a", maxToken)); got != strconv.Quote(strings.Repeat("a", maxToken)) {
		t.Errorf("quoteToken clips at the limit: %s", got)
	}
}

// A pre-release a comparator names is kept, so the Reader counts it toward
// the bound as it counts an interval, and pre-releases of one release are
// kept once (security delta review, D1). The planted breaks: the named
// versions left out of the count, and every one kept.
func TestReaderCountsNamedPrereleases(t *testing.T) {
	maxIntervals = 3
	t.Cleanup(func() { maxIntervals = MaxIntervals })
	if r := mustRange(t, ">=1.0.0-a >=1.0.0-b >=1.0.0-c.1 >=1.0-d"); len(r.alts[0].named) != 1 || len(r.alts[0].ivs) != 1 {
		t.Errorf("four pre-releases of 1.0.0 keep %d versions in %d intervals, want 1 and 1", len(r.alts[0].named), len(r.alts[0].ivs))
	}
	var r Reader
	if _, err := r.Parse(">=1.0.0-a >=1.0.0-b"); err != nil {
		t.Fatalf("one interval and one named release, at the bound: %v", err)
	}
	var bound *BoundError
	if _, err := r.Parse(">=1.0.0-a"); !errors.As(err, &bound) {
		t.Errorf("one more interval and named release, past the bound: %v", err)
	}
	var distinct Reader
	if _, err := distinct.Parse(">=1.0.0-a >=1.0.1-a >=1.0.2-a"); !errors.As(err, &bound) {
		t.Errorf("one interval and three named releases, past the bound of 3: %v", err)
	}
}
