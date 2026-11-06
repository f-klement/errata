package semver

import (
	"errors"
	"strings"
	"testing"
)

// satisfies checks that each of yes satisfies the range r of syn and none
// of no does.
func satisfies(t *testing.T, syn Syntax, r string, yes, no []string) {
	t.Helper()
	rng, err := syn.ParseRange(r)
	if err != nil {
		t.Errorf("%s: ParseRange(%q): %v", syn, r, err)
		return
	}
	for _, s := range yes {
		v, err := syn.ParseVersion(s)
		if err != nil {
			t.Errorf("%s: ParseVersion(%q): %v", syn, s, err)
		} else if !Satisfies(v, rng) {
			t.Errorf("%s: %q does not satisfy %q (%s), want it to", syn, s, r, rng)
		}
	}
	for _, s := range no {
		v, err := syn.ParseVersion(s)
		if err != nil {
			t.Errorf("%s: ParseVersion(%q): %v", syn, s, err)
		} else if Satisfies(v, rng) {
			t.Errorf("%s: %q satisfies %q (%s), want it not to", syn, s, r, rng)
		}
	}
}

// sameRange checks that each pair of ranges of syn reads as the same
// intervals, as the ecosystem's documentation says the one means the other.
func sameRange(t *testing.T, syn Syntax, pairs [][2]string) {
	t.Helper()
	for _, p := range pairs {
		a, errA := syn.ParseRange(p[0])
		b, errB := syn.ParseRange(p[1])
		if errA != nil || errB != nil {
			t.Errorf("%s: %q: %v; %q: %v", syn, p[0], errA, p[1], errB)
			continue
		}
		if a.String() != b.String() {
			t.Errorf("%s: %q reads as %s, and %q as %s; the documentation says they are one", syn, p[0], a, p[1], b)
		}
	}
}

// sameAdmits checks that each pair of ranges of syn admits the same of a
// sample of releases and pre-releases, as the ecosystem's documentation
// says the one means the other.
func sameAdmits(t *testing.T, syn Syntax, pairs [][2]string) {
	t.Helper()
	samples := []string{"0.0.0", "0.0.3", "0.0.4", "0.0.5", "0.1.0", "0.2.0", "0.2.3", "0.2.9", "0.3.0", "0.9.0", "1.0.0",
		"1.1.9", "1.2.0", "1.2.3", "1.2.9", "1.3.0", "1.9.9", "2.0.0", "2.0.0-alpha", "1.2.3-alpha", "1.3.0-alpha", "3.0.0"}
	for _, p := range pairs {
		a, errA := syn.ParseRange(p[0])
		b, errB := syn.ParseRange(p[1])
		if errA != nil || errB != nil {
			t.Errorf("%s: %q: %v; %q: %v", syn, p[0], errA, p[1], errB)
			continue
		}
		for _, s := range samples {
			v := mustVersionOf(t, syn, s)
			if Satisfies(v, a) != Satisfies(v, b) {
				t.Errorf("%s: %s satisfies %q (%s) %v and %q (%s) %v; the documentation says they are one", syn, s, p[0], a, Satisfies(v, a), p[1], b, Satisfies(v, b))
			}
		}
	}
}

// ascending checks that each version of syn is below the next.
func ascending(t *testing.T, syn Syntax, list []string) {
	t.Helper()
	for i := 1; i < len(list); i++ {
		a, errA := syn.ParseVersion(list[i-1])
		b, errB := syn.ParseVersion(list[i])
		if errA != nil || errB != nil {
			t.Errorf("%s: %q: %v; %q: %v", syn, list[i-1], errA, list[i], errB)
			continue
		}
		if Compare(a, b) >= 0 || Compare(b, a) <= 0 {
			t.Errorf("%s: %q is not below %q", syn, list[i-1], list[i])
		}
	}
}

// refused checks that each value is neither a version nor a range of syn,
// with an error naming why.
func refused(t *testing.T, syn Syntax, cases map[string]string) {
	t.Helper()
	for in, want := range cases {
		if _, err := syn.Parse(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: Parse(%q) = %v, want an error naming %q", syn, in, err, want)
		}
	}
}

// node-semver's README, Ranges: every example it gives, with the versions
// it says match and do not.
func TestNPMReadsAsNodeSemver(t *testing.T) {
	satisfies(t, NPM, ">=1.2.7", []string{"1.2.7", "1.2.8", "2.5.3", "1.3.9"}, []string{"1.2.6", "1.1.0"})
	satisfies(t, NPM, ">1", []string{"2.0.0", "3.1.0"}, []string{"1.0.1", "1.1.0"})
	satisfies(t, NPM, ">=1.2.7 <1.3.0", []string{"1.2.7", "1.2.8", "1.2.99"}, []string{"1.2.6", "1.3.0", "1.1.0"})
	satisfies(t, NPM, "1.2.7 || >=1.2.9 <2.0.0", []string{"1.2.7", "1.2.9", "1.4.6"}, []string{"1.2.8", "2.0.0"})
	// Prerelease Tags.
	satisfies(t, NPM, ">1.2.3-alpha.3", []string{"1.2.3-alpha.7", "3.4.5"}, []string{"3.4.5-alpha.9"})
	satisfies(t, NPM, "~1.2.3-beta.2", []string{"1.2.3-beta.4"}, []string{"1.2.4-beta.2"})
	satisfies(t, NPM, "^1.2.3-beta.2", []string{"1.2.3-beta.4"}, []string{"1.2.4-beta.2"})
	satisfies(t, NPM, "^0.0.3-beta", []string{"0.0.3-pr.2"}, []string{"0.0.4-beta"})
	sameRange(t, NPM, [][2]string{
		// Hyphen Ranges.
		{"1.2.3 - 2.3.4", ">=1.2.3 <=2.3.4"},
		{"1.2 - 2.3.4", ">=1.2.0 <=2.3.4"},
		{"1.2.3 - 2.3", ">=1.2.3 <2.4.0-0"},
		{"1.2.3 - 2", ">=1.2.3 <3.0.0-0"},
		// X-Ranges.
		{"*", "x"},
		{"1.x", ">=1.0.0 <2.0.0-0"},
		{"1.2.x", ">=1.2.0 <1.3.0-0"},
		{"1", ">=1.0.0 <2.0.0-0"},
		{"1.x.x", ">=1.0.0 <2.0.0-0"},
		{"1.2", ">=1.2.0 <1.3.0-0"},
		// Tilde Ranges.
		{"~1.2.3", ">=1.2.3 <1.3.0-0"},
		{"~1.2", ">=1.2.0 <1.3.0-0"},
		{"~1", ">=1.0.0 <2.0.0-0"},
		{"~0.2.3", ">=0.2.3 <0.3.0-0"},
		{"~0.2", ">=0.2.0 <0.3.0-0"},
		{"~0", ">=0.0.0 <1.0.0-0"},
		{"~1.2.3-beta.2", ">=1.2.3-beta.2 <1.3.0-0"},
		// Caret Ranges.
		{"^1.2.3", ">=1.2.3 <2.0.0-0"},
		{"^0.2.3", ">=0.2.3 <0.3.0-0"},
		{"^0.0.3", ">=0.0.3 <0.0.4-0"},
		{"^1.2.3-beta.2", ">=1.2.3-beta.2 <2.0.0-0"},
		{"^0.0.3-beta", ">=0.0.3-beta <0.0.4-0"},
		{"^1.2.x", ">=1.2.0 <2.0.0-0"},
		{"^0.0.x", ">=0.0.0 <0.1.0-0"},
		{"^0.0", ">=0.0.0 <0.1.0-0"},
		{"^1.x", ">=1.0.0 <2.0.0-0"},
		{"^0.x", ">=0.0.0 <1.0.0-0"},
		// A leading v is stripped; node-semver reads ~> as ~, where
		// RubyGems' ~> 1.2 runs to 2.0.
		{">= v1.2.3", ">=1.2.3"},
		{"~>1.2", "~1.2"},
		// An empty comparator set is *, and numbers after a wildcard are
		// wildcards too (review of 9b9081f7, finding 6). The planted break:
		// afterWild unset for npm.
		{"1.0.0 ||", "1.0.0 || *"},
		{"1.*.3", "1.x"},
		{"1.x.0", "1.x"},
	})
	refused(t, NPM, map[string]string{
		"1.0rc1":                         "not a number as npm writes one",
		">=1.2, <1.5":                    "not a number as npm writes one",
		"!=1.0.0":                        "not a number as npm writes one",
		"01.2.3":                         "no leading zero",
		"1.2.3.4":                        "at most three numbers",
		"1.2-beta":                       "only after three numbers",
		"1.2.3-01":                       "leading zero",
		"1!2.0.0":                        "not a number as npm writes one",
		"~=2.2":                          "not a number as npm writes one",
		"1.x-beta":                       "with nothing after",
		strings.Repeat("1", MaxLength+1): "longer than 1024 bytes",
	})
}

// The Cargo book, Specifying dependencies: each table of requirements and
// what it means, and its rules for pre-releases.
func TestCargoReadsAsCargo(t *testing.T) {
	sameAdmits(t, Cargo, [][2]string{
		// Default requirements.
		{"1.2.3", ">=1.2.3, <2.0.0"},
		{"1.2", ">=1.2.0, <2.0.0"},
		{"1", ">=1.0.0, <2.0.0"},
		{"0.2.3", ">=0.2.3, <0.3.0"},
		{"0.2", ">=0.2.0, <0.3.0"},
		{"0.0.3", ">=0.0.3, <0.0.4"},
		{"0.0", ">=0.0.0, <0.1.0"},
		{"0", ">=0.0.0, <1.0.0"},
		// Caret: ^1.2.3 is exactly 1.2.3.
		{"^1.2.3", "1.2.3"},
		// Tilde requirements.
		{"~1.2.3", ">=1.2.3, <1.3.0"},
		{"~1.2", ">=1.2.0, <1.3.0"},
		{"~1", ">=1.0.0, <2.0.0"},
		// Wildcard requirements.
		{"1.*", ">=1.0.0, <2.0.0"},
		{"1.2.*", ">=1.2.0, <1.3.0"},
		// Comparison requirements.
		{"> 1", ">=2.0.0"},
		{"= 1.2.3", "=1.2.3"},
	})
	satisfies(t, Cargo, "*", []string{"0.0.0", "7.1.0"}, []string{"1.0.0-alpha"})
	satisfies(t, Cargo, ">= 1.2.0", []string{"1.2.0", "3.0.0"}, []string{"1.1.9"})
	satisfies(t, Cargo, "< 2", []string{"1.9.9"}, []string{"2.0.0", "2.0.0-alpha"})
	satisfies(t, Cargo, ">= 1.2, < 1.5", []string{"1.2.0", "1.4.9"}, []string{"1.5.0", "1.1.0"})
	satisfies(t, Cargo, "<1.2, ^1.2.2", nil, []string{"1.1.0", "1.2.2", "1.3.0"})
	// Pre-releases: excluded unless asked for, and then on the same
	// release only, newer pre-releases and later releases included.
	satisfies(t, Cargo, "1.0", []string{"1.0.0"}, []string{"1.0.0-alpha"})
	satisfies(t, Cargo, "1.0.0-alpha", []string{"1.0.0-beta", "1.0.0", "1.2.0"}, []string{"1.0.1-alpha", "1.0.1-beta"})
	// The semver crate's matches_caret and matches_less of a partial
	// version hold the major to the comparator's (src/eval.rs), so a
	// comparator that names 2.0.0-alpha.0 admits none of 2.0.0's
	// pre-releases beside ^1.2.3 or <2, as npm's <2.0.0-0 admits none
	// (review of 9b9081f7, finding 5). A full <2.0.0 is the version itself.
	// The planted break: upTo's bound made without atFloor.
	satisfies(t, Cargo, "^1.2.3, >=2.0.0-alpha.0", nil, []string{"2.0.0-alpha.1", "2.0.0"})
	satisfies(t, Cargo, "<2, >=2.0.0-alpha.0", nil, []string{"2.0.0-alpha.1"})
	satisfies(t, Cargo, "<2.0.0, >=2.0.0-alpha.0", []string{"2.0.0-alpha.1"}, nil)
	satisfies(t, NPM, "^1.2.3 >=2.0.0-alpha", nil, []string{"2.0.0-alpha"})
	satisfies(t, Cargo, "=1.2.3-alpha.3", []string{"1.2.3-alpha.3"}, []string{"1.2.3-alpha.4", "1.2.3"})
	// A bare value is a version, a partial one staying partial, as the
	// Cargo book's rust-version = "1.56" is one (the owner, 2026-11-06). A
	// requirement comes with its operator, as the dependency extractor
	// writes tokio = "1.35" as ^1.35; in a range a bare comparator is ^.
	// The planted break: Cargo's versions held to three numbers again.
	for in, want := range map[string]string{"1.35": "version 1.35", "1.2.3": "version 1.2.3", "^1.35": "range >=1.35.0 <2.0.0-0", "1.35, <1.40": "range >=1.35.0 <1.40.0-0"} {
		if v, err := Cargo.Parse(in); err != nil || v.String() != want {
			t.Errorf("Cargo.Parse(%q) = %v, %v, want %s", in, v, err, want)
		}
	}
	refused(t, Cargo, map[string]string{
		"v1.2.3":      "not a number as Cargo writes one",
		"^1 || ^2":    "has no ||",
		">=1.*":       "wildcard with no operator",
		"*, >=1":      "as the only comparator",
		">=1.0 <2.0":  "one operator and one version",
		"1.2.3+b, <2": "no build metadata",
		"1.2,":        "beside a comma is empty",
		"~>1.2":       "not a number as Cargo writes one",
		"1.0.0.post1": "at most three numbers",
		"1.0.0rc1":    "not a number as Cargo writes one",
	})
}

// The PyPA's version specifiers specification (PEP 440): normalisation,
// the ordering example, and each operator's examples.
func TestPEP440ReadsAsPEP440(t *testing.T) {
	for in, want := range map[string]string{
		// Normalisation.
		"1.1RC1": "1.1rc1", "1.1alpha1": "1.1a1", "1.1beta2": "1.1b2", "1.1c3": "1.1rc3",
		"1.1.a1": "1.1a1", "1.1-a1": "1.1a1", "1.0a.1": "1.0a1", "1.2a": "1.2a0",
		"1.2-post2": "1.2.post2", "1.2post2": "1.2.post2", "1.2.post-2": "1.2.post2",
		"1.0-1": "1.0.post1", "1.0.r3": "1.0.post3", "1.0.rev": "1.0.post0",
		"1.0.DEV": "1.0.dev0", "1.0pre1": "1.0rc1", "1.0preview1": "1.0rc1",
		"v1.0": "1.0", "1!2.0": "1!2.0", "1.0+ubuntu-1": "1.0+ubuntu.1", "1.0+Ubuntu_01": "1.0+ubuntu.1",
	} {
		v, err := PEP440.ParseVersion(in)
		if err != nil || v.String() != want {
			t.Errorf("PEP440.ParseVersion(%q) = %v, %v, want %s", in, v, err, want)
		}
	}
	// The specification's ordering example, local versions included.
	ascending(t, PEP440, []string{
		"1.dev0", "1.0.dev456", "1.0a1", "1.0a2.dev456", "1.0a12.dev456", "1.0a12",
		"1.0b1.dev456", "1.0b2", "1.0b2.post345.dev456", "1.0b2.post345", "1.0rc1.dev456",
		"1.0rc1", "1.0", "1.0+abc.5", "1.0+abc.7", "1.0+5", "1.0.post456.dev34", "1.0.post456", "1.0.15", "1.1.dev1",
	})
	ascending(t, PEP440, []string{"2.0", "1!1.0"})
	sameRange(t, PEP440, [][2]string{
		// Compatible release.
		{"~= 2.2", ">= 2.2, == 2.*"},
		{"~= 1.4.5", ">= 1.4.5, == 1.4.*"},
		{"~= 2.2.post3", ">= 2.2.post3, == 2.*"},
		{"~= 1.4.5a4", ">= 1.4.5a4, == 1.4.*"},
		{"~= 2.2.0", ">= 2.2.0, == 2.2.*"},
		{"~= 1.4.5.0", ">= 1.4.5.0, == 1.4.5.*"},
	})
	// Version matching and exclusion, given 1.1.post1, 1.1a1 and 1.1.
	satisfies(t, PEP440, "== 1.1", []string{"1.1", "1.1.0"}, []string{"1.1.post1", "1.1a1", "1.1.dev1"})
	satisfies(t, PEP440, "== 1.1.post1", []string{"1.1.post1"}, nil)
	satisfies(t, PEP440, "== 1.1.*", []string{"1.1.post1", "1.1"}, []string{"1.1a1"})
	satisfies(t, PEP440, "== 1.1a1", []string{"1.1a1"}, []string{"1.1"})
	satisfies(t, PEP440, "== 1.1.0", []string{"1.1"}, nil)
	satisfies(t, PEP440, "== 1.1.dev1", nil, []string{"1.1"})
	satisfies(t, PEP440, "!= 1.1", []string{"1.1.post1"}, []string{"1.1.0"})
	satisfies(t, PEP440, "!= 1.1.post1", []string{"1.1"}, []string{"1.1.post1"})
	satisfies(t, PEP440, "!= 1.1.*", []string{"1.2"}, []string{"1.1.post1"})
	// "if pre-releases are requested": a clause naming one requests them.
	satisfies(t, PEP440, "== 1.1.*, >= 1.1a1", []string{"1.1a1"}, nil)
	// Exclusive ordered comparison.
	satisfies(t, PEP440, ">1.7", []string{"1.7.1"}, []string{"1.7.0.post1", "1.7.0", "1.7.post1.dev2", "1.7+local"})
	satisfies(t, PEP440, ">1.7.post2", []string{"1.7.1", "1.7.0.post3"}, []string{"1.7.0", "1.7.post2+local"})
	satisfies(t, PEP440, ">1.7rc1, >=1.7a1", []string{"1.7rc2", "1.7"}, []string{"1.7rc1.post1"})
	satisfies(t, PEP440, "<3.0, >=2.9a1", []string{"2.9.1", "2.9b1"}, []string{"3.0rc1", "3.0.dev1", "3.0"})
	satisfies(t, PEP440, "<3.0rc1, >=3.0a1", []string{"3.0a2"}, []string{"3.0rc1"})
	satisfies(t, PEP440, "<1.0.post2, >=0.9a1", []string{"1.0", "1.0.post1", "0.9.5"}, []string{"1.0rc1", "1.0.post2"})
	// The ticket's cases: >3.11 admits 3.11.9, and <=3.11 refuses 3.11.4.
	satisfies(t, PEP440, ">3.11", []string{"3.11.9", "3.12"}, []string{"3.11.0", "3.11.0.post1"})
	satisfies(t, PEP440, "<=3.11", []string{"3.11", "3.10.9", "3.11+local"}, []string{"3.11.4"})
	// Pre-releases are excluded unless a clause names one.
	satisfies(t, PEP440, ">=1.0", []string{"2.0"}, []string{"2.0b1", "2.0.dev1"})
	satisfies(t, PEP440, ">=1.0b1", []string{"2.0b1", "1.0b2"}, []string{"0.9"})
	satisfies(t, PEP440, ">1.0b1", []string{"2.0", "2.0b1"}, nil)
	// < and > name a pre-release too: "Allowing pre-releases that are
	// earlier than, but not equal to a specific pre-release may be
	// accomplished by using <V.rc1", and packaging 25.0 agrees on each row
	// (review of 9b9081f7, finding 2). The planted break: < and > left out
	// of the operators that name one.
	satisfies(t, PEP440, "<1.0rc1", []string{"1.0.0a1", "0.9"}, []string{"1.0rc1"})
	satisfies(t, PEP440, "<1.0a1.post1", []string{"1.0.0.dev1"}, nil)
	satisfies(t, PEP440, ">1.0a1", []string{"1.1.0a1"}, nil)
	satisfies(t, PEP440, ">1.0.dev1", []string{"2.0.0.dev0"}, nil)
	satisfies(t, PEP440, "<2.0", nil, []string{"1.9a1"})
	refused(t, PEP440, map[string]string{
		">=1.0 <2":     "one operator and one version",
		"1.0 || 2.0":   "has no ||",
		"^1.2":         "an operator before every version",
		"==1.0.dev1.*": ".* follows the release numbers only",
		"==1.0+foo1.*": ".* follows the release numbers only",
		">=1.0+abc":    "only == and != take a local version",
		"~=1":          "at least two numbers",
		"===1.0":       "=== compares text",
		"1.0-":         "an operator before every version",
		"1.0.0-rc.1.2": "an operator before every version",
		">=1.0, ":      "beside a comma is empty",
	})
}

// PEP 440's local versions: a public specifier matches a candidate with any
// local version, a local one only that local version, and two versions or
// two ranges that differ only in it do not agree. Local versions are
// ordered, so ranges alone compare them too: torch pinned ==2.1.0+cpu in
// one file and ==2.1.0+cu118 in another do not overlap (review of 9b9081f7,
// finding 3). The planted break: cmpLocal returning 0.
func TestPEP440Locals(t *testing.T) {
	satisfies(t, PEP440, "==1.0", []string{"1.0+abc", "1.0"}, nil)
	satisfies(t, PEP440, "==1.0+abc", []string{"1.0+ABC", "1.0+abc"}, []string{"1.0.0", "1.0+def"})
	satisfies(t, PEP440, "!=1.0+abc", []string{"1.0", "1.0+def"}, []string{"1.0+abc"})
	satisfies(t, PEP440, "!=1.0", []string{"1.1"}, []string{"1.0+abc"})
	v := func(s string) Version {
		x, err := PEP440.ParseVersion(s)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	if !AgreeAt(v("1.0+ubuntu-1"), v("1.0+ubuntu.1")) || AgreeAt(v("1.0.0"), v("1.0+abc")) || AgreeAt(v("1.0+abc.5"), v("1.0+5")) {
		t.Error("PEP 440 versions that differ in their local version agree, or equal ones do not")
	}
	// Any reads +abc as build metadata, never compared.
	if a, _ := ParseVersion("1.0+abc"); !AgreeAt(a, mustVersion(t, "1.0.0")) {
		t.Error("the unset syntax tells 1.0+abc from 1.0.0")
	}
	satisfies(t, PEP440, "<=1.0", []string{"1.0+abc"}, []string{"1.0.post1"})
	satisfies(t, PEP440, ">1.0.post1", []string{"1.0.post2"}, []string{"1.0.post1+abc"})
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"==2.1.0+cpu", "==2.1.0+cu118", false},
		{"==2.1.0+cpu", "!=2.1.0+cpu", false},
		{"==2.1.0+cpu", "==2.1.0", true},
		{"==2.1.0", "!=2.1.0+cpu", true},
		{"==2.1.0", "!=2.1.0", false},
		{"<=2.1.0", ">2.1.0", false},
	} {
		if got := Overlap([]Range{mustRangeOf(t, PEP440, c.a), mustRangeOf(t, PEP440, c.b)}); got != c.want {
			t.Errorf("%q and %q overlap %v, want %v", c.a, c.b, got, c.want)
		}
	}
	for in, want := range map[string]string{"==1.0+abc, !=1.1+x": "=1.0+abc", "==1.0": "=1.0", "!=1.0": "<1.0 || >1.0+*", "<=1.0": "<=1.0+*"} {
		if got := mustRangeOf(t, PEP440, in).String(); got != want {
			t.Errorf("%q reads as %s, want %s", in, got, want)
		}
	}
}

func mustRangeOf(t *testing.T, syn Syntax, s string) Range {
	t.Helper()
	r, err := syn.ParseRange(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// go.dev/doc/toolchain, Go version numbers, and the Go modules reference:
// the language version 1.21 is below 1.21rc1 and 1.21.0, and before Go 1.21
// the first release was 1.N.
func TestGoReadsAsGo(t *testing.T) {
	ascending(t, Go, []string{"1.21", "1.21rc1", "1.21rc2", "1.21.0", "1.21.1", "1.21.2"})
	ascending(t, Go, []string{"1.21.9", "1.22"})
	ascending(t, Go, []string{"1.20rc1", "1.20rc2", "1.20rc3", "1.20", "1.20.1"})
	ascending(t, Go, []string{"1.18beta2", "1.18rc1", "1.18"})
	// "Go 1.21.2 will refuse to load a module or workspace with a go 1.21.3
	// or go 1.22 line."
	ascending(t, Go, []string{"1.21.2", "1.21.3"})
	ascending(t, Go, []string{"1.21.2", "1.22"})
	// The owner's decision: go 1.21 is exactly 1.21, so >=1.21.4 fails.
	satisfies(t, Go, ">=1.21.4", []string{"1.21.4", "go1.22.0"}, []string{"1.21", "1.21rc1", "1.21.3"})
	satisfies(t, Go, ">=1.21", []string{"1.21", "1.21rc1", "1.21.0"}, []string{"1.20.9"})
	satisfies(t, Go, "<1.22", []string{"1.21.9"}, []string{"1.22", "1.22rc1", "1.22.0"})
	satisfies(t, Go, "v1.2.3", []string{"v1.2.3+incompatible"}, []string{"v1.2.4"})
	satisfies(t, Go, ">=v1.2.0 <v2.0.0, || =v3.0.0", []string{"v1.5.0-rc.1", "v3"}, []string{"v2.0.0"})
	for _, c := range []struct {
		a, b  string
		agree bool
	}{
		{"1.21", "1.21.0", false},
		{"go1.21.0", "1.21.0", true},
		{"1.20", "1.20.0", true},
		{"1", "1.0.0", true},
		{"v1.2", "v1.2.0", true},
		{"v1", "v1.0.0", true},
		{"1.21", "1.21.4", false},
	} {
		a, errA := Go.ParseVersion(c.a)
		b, errB := Go.ParseVersion(c.b)
		if errA != nil || errB != nil || AgreeAt(a, b) != c.agree || a.Partial() {
			t.Errorf("Go: %q and %q agree %v (%v, %v), want %v", c.a, c.b, AgreeAt(a, b), errA, errB, c.agree)
		}
	}
	for in, want := range map[string]string{
		"go1.22rc1": "1.22rc1", "1.21": "1.21", "1.20": "1.20.0", "v0.0.0-20191109021931-daa7c04131f5": "v0.0.0-20191109021931-daa7c04131f5", "v1.21": "v1.21",
	} {
		if v, err := Go.ParseVersion(in); err != nil || v.String() != want {
			t.Errorf("Go.ParseVersion(%q) = %v, %v, want %s", in, v, err, want)
		}
	}
	if got := mustRangeOf(t, Go, ">=1.21 <1.22").String(); got != ">=1.21 <1.22" {
		t.Errorf("a Go range reads as %s", got)
	}
	// Go has no rule for pre-releases, so ranges that share only one
	// overlap.
	if !Overlap([]Range{mustRangeOf(t, Go, ">=1.21rc1 <1.21.0"), mustRangeOf(t, Go, "<1.21.0")}) {
		t.Error("Go ranges that share 1.21rc1 do not overlap")
	}
	refused(t, Go, map[string]string{
		"1.2.3-rc.1": "ends with its third number",
		"^1.21":      "a Go version starts with a number",
		"1.21.x":     "ends with its third number",
		"v1.2-pre":   "only after three numbers",
		"1.021":      "a number follows the dot",
		"1.21RC1":    "where rc1 or beta2 may",
		"1.21rc01":   "where rc1 or beta2 may",
		"1.2.3.4":    "ends with its third number",
		"1x":         "a dot follows the first number",
	})
}

// The RubyGems guides, Versioning and compatibility and How dependency
// resolution works, and Gem::Version's rules for the segments of a
// version.
func TestRubyGemsReadsAsRubyGems(t *testing.T) {
	// "~> 2.2 means >= 2.2 and < 3.0"; "~> 2.2 allows 2.9.9, but ~> 2.2.0
	// rejects 2.3.0"; "~> 3.2.6 means >= 3.2.6 and < 3.3.0".
	satisfies(t, RubyGems, "~> 2.2", []string{"2.2", "2.9.9"}, []string{"3.0", "2.1", "3.0.a"})
	satisfies(t, RubyGems, "~> 2.2.0", []string{"2.2.9"}, []string{"2.3.0"})
	satisfies(t, RubyGems, "~> 3.2", []string{"3.9"}, []string{"4.0"})
	satisfies(t, RubyGems, "~> 3.2.6", []string{"3.2.6", "3.2.99"}, []string{"3.3.0", "3.2.5"})
	satisfies(t, RubyGems, "~> 2", []string{"2.0", "2.99"}, []string{"3.0", "1.9"})
	satisfies(t, RubyGems, ">= 2.2", []string{"2.2.0"}, []string{"2.1.9"})
	satisfies(t, RubyGems, ">= 2.2, < 4.0", []string{"3.9"}, []string{"4.0"})
	satisfies(t, RubyGems, "= 3.2.6", []string{"3.2.6"}, []string{"3.2.7"})
	satisfies(t, RubyGems, "7.1", []string{"7.1.0", "7.1"}, []string{"7.1.1"})
	satisfies(t, RubyGems, "!= 1.5", []string{"1.5.1"}, []string{"1.5.0"})
	// Unlike npm's, >1.2 admits 1.2.1, and <=1.2 refuses it.
	satisfies(t, RubyGems, "> 1.2", []string{"1.2.1"}, []string{"1.2.0"})
	satisfies(t, RubyGems, "<= 1.2", []string{"1.2"}, []string{"1.2.1"})
	// A prerelease, any version with a letter, is considered only when a
	// requirement names one.
	satisfies(t, RubyGems, ">= 1.0", nil, []string{"8.1.0.beta1"})
	satisfies(t, RubyGems, ">= 8.1.0.beta1", []string{"8.1.0.beta2", "8.1.0", "9.0.0.rc1"}, []string{"8.1.0.alpha"})
	satisfies(t, RubyGems, ">= 1.0.0.rc1", []string{"1.0.0.rc2"}, nil)
	ascending(t, RubyGems, []string{"1.0.0.alpha", "1.0.0.beta2", "1.0.0.rc1", "1.0.0"})
	ascending(t, RubyGems, []string{"1.0.a.b", "1.0.a", "1.0.a.1", "1.0.b"})
	ascending(t, RubyGems, []string{"1.a", "1.0.1"})
	for _, pair := range [][2]string{{"1.0", "1.0.0"}, {"1.0.0-1", "1.0.0.pre.1"}, {"1.0.0.pre1", "1.0.0.pre.1"}, {"1.0.a", "1.a"}, {"1.0.a.0", "1.0.a"}} {
		a, _ := RubyGems.ParseVersion(pair[0])
		b, _ := RubyGems.ParseVersion(pair[1])
		if Compare(a, b) != 0 {
			t.Errorf("RubyGems: %q and %q differ", pair[0], pair[1])
		}
	}
	for in, want := range map[string]string{"2.0.0.racecar1": "2.0.0.racecar.1", "1.0.0-rc-1": "1.0.0.pre.rc.pre.1", "3.3.0.Preview1": "3.3.0.Preview.1"} {
		if v, err := RubyGems.ParseVersion(in); err != nil || v.String() != want {
			t.Errorf("RubyGems.ParseVersion(%q) = %v, %v, want %s", in, v, err, want)
		}
	}
	// Letters compare by their bytes, so case matters.
	if a, b := mustVersionOf(t, RubyGems, "1.0.RC1"), mustVersionOf(t, RubyGems, "1.0.rc1"); Compare(a, b) == 0 {
		t.Error("RubyGems: 1.0.RC1 equals 1.0.rc1")
	}
	refused(t, RubyGems, map[string]string{
		"^1.0":       "not a version as RubyGems writes one",
		"~> 1 || 2":  "has no ||",
		"v1.0":       "not a version as RubyGems writes one",
		"1a":         "not a version as RubyGems writes one",
		"~> 1.0 2.0": "one operator and one version",
		"1.0+b":      "not a version as RubyGems writes one",
	})
}

func mustVersionOf(t *testing.T, syn Syntax, s string) Version {
	t.Helper()
	v, err := syn.ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Partials stay partial under every syntax but Go (the owner, 2026-10-24):
// 3.11 stands for every 3.11 release, so it satisfies >3.11, as 3.11.1
// does, and agrees with 3.11.4. Cargo's rust-version = "1.70" is one, so it
// does not agree with a toolchain channel of 1.82 (the owner, 2026-11-06).
// The planted break: Partial false for every syntax but Any.
func TestPartialsStayPartial(t *testing.T) {
	satisfies(t, PEP440, ">3.11", []string{"3.11"}, nil)
	satisfies(t, RubyGems, "> 1.2", []string{"1.2"}, nil)
	satisfies(t, NPM, "^1.2.5", []string{"1.2"}, nil)
	for _, syn := range []Syntax{NPM, Cargo, PEP440, RubyGems} {
		if a, b := mustVersionOf(t, syn, "3.11"), mustVersionOf(t, syn, "3.11.4"); !a.Partial() || !AgreeAt(a, b) {
			t.Errorf("%s: 3.11 is not partial, or does not agree with 3.11.4", syn)
		}
	}
	if a, b := mustVersionOf(t, Cargo, "1.70"), mustVersionOf(t, Cargo, "1.82"); AgreeAt(a, b) {
		t.Error("Cargo: rust-version 1.70 agrees with the channel 1.82")
	}
}

// Every syntax refuses a value past MaxLength and an empty one, and a
// syntax that is none of the five is refused by name.
func TestSyntaxBounds(t *testing.T) {
	for _, syn := range append([]Syntax{Any}, Syntaxes...) {
		for _, s := range []string{"", " ", strings.Repeat("1", MaxLength+1), ">=" + strings.Repeat("1", MaxLength), strings.Repeat(" ", MaxLength+1)} {
			if _, err := syn.Parse(s); err == nil && (syn != NPM || strings.TrimSpace(s) != "" || len(s) > MaxLength) {
				t.Errorf("%q: Parse of %d bytes held", syn, len(s))
			}
		}
	}
	// node-semver reads the empty range as *, as a package.json "dep": ""
	// means any version. The planted break: npm's empty range refused.
	for _, s := range []string{"", "  "} {
		if v, err := NPM.Parse(s); err != nil || v.String() != "range *" {
			t.Errorf("NPM.Parse(%q) = %v, %v, want the range *", s, v, err)
		}
	}
	if _, err := ParseSyntax("maven"); err == nil || err.Error() != `the syntax "maven" is none of npm, cargo, pep440, go, rubygems` {
		t.Errorf("ParseSyntax(maven): %v", err)
	}
	for _, s := range Syntaxes {
		if got, err := ParseSyntax(string(s)); err != nil || got != s {
			t.Errorf("ParseSyntax(%q) = %q, %v", s, got, err)
		}
	}
	bad := Syntax("maven")
	if _, err := bad.ParseVersion("1.0.0"); err == nil {
		t.Error("an unknown syntax read a version")
	}
	if _, err := bad.ParseRange(">=1"); err == nil {
		t.Error("an unknown syntax read a range")
	}
}

// A Reader reads in its syntax and counts the pieces local versions leave
// toward the bound.
func TestReaderReadsItsSyntax(t *testing.T) {
	r := Reader{Syntax: Cargo}
	if v, err := r.Parse("1.35, <1.40"); err != nil || !v.IsRange() {
		t.Fatalf("a Cargo Reader read 1.35, <1.40 as %v, %v", v, err)
	}
	maxIntervals = 2
	t.Cleanup(func() { maxIntervals = MaxIntervals })
	p := Reader{Syntax: PEP440}
	var bound *BoundError
	if _, err := p.Parse("!=1.0+a, !=1.0+b"); !errors.As(err, &bound) {
		t.Errorf("three intervals, past the bound of 2: %v", err)
	}
}

// Above >V lie no post releases of V but releases with more numbers, so
// >1.0 overlaps <1.0.1 (1.0.0.1) and not <=1.0.post5; above >1.0rc1 the
// first release is 1.0, so it does not overlap <1.0 or <1.0rc5, which
// hold only pre-releases of 1.0. The planted break: the test for a ceiling with no
// pre-release negated in holdsRelease.
func TestPEP440Ceiling(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{">1.0", "<1.0.1", true},
		{">1.0", "<=1.0.post5", false},
		{">1.0rc1", "<1.0", false},
		{">1.0rc1", "<1.0rc5", false},
		{">1.0rc1", "<=1.0", true},
	} {
		if got := Overlap([]Range{mustRangeOf(t, PEP440, c.a), mustRangeOf(t, PEP440, c.b)}); got != c.want {
			t.Errorf("%q and %q overlap %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
