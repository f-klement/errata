package semver

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// versionText draws a version as one of the five ecosystems may write it.
func versionText() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		var b strings.Builder
		b.WriteString(rapid.SampledFrom([]string{"", "v", "1!"}).Draw(t, "prefix"))
		n := rapid.IntRange(1, 4).Draw(t, "numbers")
		for i := range n {
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(rapid.SampledFrom([]string{"0", "1", "2", "10", "01"}).Draw(t, "number"))
		}
		switch rapid.IntRange(0, 3).Draw(t, "suffix") {
		case 1:
			b.WriteString("-" + rapid.SampledFrom([]string{"alpha", "alpha.1", "beta", "rc.1", "0", "x-y"}).Draw(t, "pre"))
		case 2:
			b.WriteString(rapid.SampledFrom([]string{"a1", "rc2", ".post1", ".dev0", "b1.post2", ".pre", ".beta.2"}).Draw(t, "words"))
		case 3:
			b.WriteString("+" + rapid.SampledFrom([]string{"build.1", "incompatible"}).Draw(t, "build"))
		}
		return b.String()
	})
}

// Compare is a total order: reflexive, antisymmetric and transitive, and a
// version's String reads back as an equal version with the same String.
func TestCompareIsAnOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		texts := rapid.SliceOfN(versionText(), 3, 3).Draw(t, "versions")
		vs := make([]Version, 3)
		for i, s := range texts {
			v, err := ParseVersion(s)
			if err != nil {
				t.Fatalf("ParseVersion(%q): %v", s, err)
			}
			again, err := ParseVersion(v.String())
			if err != nil || Compare(v, again) != 0 || again.String() != v.String() {
				t.Fatalf("%q reads as %s, which reads back as %s, %v", s, v, again, err)
			}
			vs[i] = v
		}
		a, b, c := vs[0], vs[1], vs[2]
		if Compare(a, a) != 0 {
			t.Fatalf("%s is not equal to itself", a)
		}
		if Compare(a, b) != -Compare(b, a) {
			t.Fatalf("Compare(%s, %s) = %d but reversed %d", a, b, Compare(a, b), Compare(b, a))
		}
		if Compare(a, b) <= 0 && Compare(b, c) <= 0 && Compare(a, c) > 0 {
			t.Fatalf("%s <= %s <= %s but %s > %s", a, b, c, a, c)
		}
	})
}

// A version that is not partial satisfies the range of itself, and the
// ranges at and above it, and not the one below it; a partial version
// satisfies its own X-range.
func TestSatisfiesBounds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := versionText().Draw(t, "version")
		v, _ := ParseVersion(s)
		if v.Partial() {
			if r := mustRangeT(t, v.String()+".x"); !Satisfies(v, r) {
				t.Fatalf("%s does not satisfy %s", v, r)
			}
			return
		}
		for _, op := range []string{"=", ">=", "<="} {
			if r := mustRangeT(t, op+v.String()); !Satisfies(v, r) {
				t.Fatalf("%s does not satisfy %s%s, read as %s", v, op, v, r)
			}
		}
		if r := mustRangeT(t, "<"+v.String()); Satisfies(v, r) {
			t.Fatalf("%s satisfies <%s", v, v)
		}
	})
}

func mustRangeT(t *rapid.T, s string) Range {
	r, err := ParseRange(s)
	if err != nil {
		t.Fatalf("ParseRange(%q): %v", s, err)
	}
	return r
}

// FuzzParse reads any text as a value. It never panics, a version reads back
// from its String when that is within MaxLength, and a range overlaps with itself exactly when it
// overlaps alone.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"20", "v20.11.1", "^20.1", "~> 3.2", "~=2.2", ">=3.8, !=3.9.0, <4", "1.2.3 - 2.3",
		"1.x || >=3", "v0.0.0-20191109021931-daa7c04131f5", "1!2.0a1.post2.dev3+local", "go1.22rc1",
		"===1.0", "lts/*", ">=", "*",
		// Found by fuzzing: 1024 bytes that String writes as 1025.
		"1" + strings.Repeat("H", MaxLength-1),
	} {
		f.Add(seed, "1.2.3")
	}
	f.Fuzz(func(t *testing.T, s, other string) {
		value, err := Parse(s)
		if err != nil {
			return
		}
		if value.version != nil {
			// String writes a separator the value may leave out, as in 1rc1
			// and 1-rc.1, so a value near MaxLength can write past it.
			if text := value.version.String(); len(text) <= MaxLength {
				again, err := ParseVersion(text)
				if err != nil || Compare(again, *value.version) != 0 {
					t.Fatalf("%q reads as %s, which reads back as %s, %v", s, value.version, again, err)
				}
			}
			if o, err := ParseRange(other); err == nil {
				Satisfies(*value.version, o)
			}
			return
		}
		r := *value.rng
		if Overlap([]Range{r}) != Overlap([]Range{r, r}) {
			t.Fatalf("%q overlaps alone and with itself differently", s)
		}
		if o, err := ParseVersion(other); err == nil {
			Satisfies(o, r)
		}
	})
}
