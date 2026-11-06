package semver

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"
)

// chain is items of item(k, i) for k from 0, joined by sep, up to MaxLength
// bytes: one hostile value, distinct for each i.
func chain(i int, sep string, item func(k, i int) string) string {
	var b strings.Builder
	for k := 0; ; k++ {
		c := item(k, i)
		if k > 0 {
			c = sep + c
		}
		if b.Len()+len(c) > MaxLength {
			return b.String()
		}
		b.WriteString(c)
	}
}

// syntaxShape is one hostile shape of value in one syntax, for the bounds
// docs/reference.md#compare states: values that hold the most intervals
// for their length, refused at MaxIntervals (refused), or ranges that fold
// into one interval beside one version that satisfies them all.
type syntaxShape struct {
	syn     Syntax
	name    string
	value   func(i int) string
	version string // "" for ranges compared alone
	refused bool
}

func syntaxShapes() []syntaxShape {
	f := func(format string) func(k, i int) string {
		return func(k, i int) string { return fmt.Sprintf(format, k, i) }
	}
	return []syntaxShape{
		{NPM, "alternatives", func(i int) string { return chain(i, "||", f("~%d.%d")) }, "", true},
		{NPM, "folded", func(i int) string { return chain(i, " ", f(">=%d.%d")) }, "9999.0.0", false},
		{NPM, "named", func(i int) string {
			return chain(i, " ", func(k, i int) string { return fmt.Sprintf(">=0.%d.%d-0", i, k) })
		}, "9999.0.0", true},
		{Cargo, "folded", func(i int) string { return chain(i, ",", f(">=%d.%d")) }, "9999.0.0", false},
		{Cargo, "named", func(i int) string {
			return chain(i, ",", func(k, i int) string { return fmt.Sprintf(">=0.%d.%d-0", i, k) })
		}, "9999.0.0", true},
		{PEP440, "not equal", func(i int) string { return chain(i, ",", f("!=%d.%d")) }, "", true},
		{PEP440, "folded", func(i int) string { return chain(i, ",", f(">=%d.%d")) }, "9999.0.0", false},
		{PEP440, "locals", func(i int) string { return chain(i, ",", f("!=1.%d+%d")) }, "9999.0.0", true},
		{Go, "alternatives", func(i int) string { return chain(i, "||", f("=%d.%d.0")) }, "", true},
		{Go, "folded", func(i int) string { return chain(i, " ", f(">=%d.%d.0")) }, "9999.0.0", false},
		{RubyGems, "not equal", func(i int) string { return chain(i, ",", f("!=%d.%d")) }, "", true},
		{RubyGems, "folded", func(i int) string { return chain(i, ",", f(">=%d.%d")) }, "9999.0.0", false},
	}
}

// readAllIn is readAll in a syntax.
func readAllIn(syn Syntax, texts []string) (Outcome, error) {
	r := Reader{Syntax: syn}
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

// BenchmarkHostileSyntax is each syntax's hostile shapes, 100000 distinct
// values of about MaxLength bytes, the most one extractor returns, with
// one version among them or none: the figures under every syntax in
// docs/reference.md#compare.
func BenchmarkHostileSyntax(b *testing.B) {
	for _, s := range syntaxShapes() {
		b.Run(string(s.syn)+"/"+s.name, func(b *testing.B) {
			texts := hostile(100_000, s.value)
			if s.version != "" {
				texts = append(texts, s.version)
			}
			b.ResetTimer()
			for range b.N {
				out, err := readAllIn(s.syn, texts)
				if s.refused != (err != nil) || err == nil && !out.Holds {
					b.Fatal(out, err)
				}
			}
		})
	}
}

// Each shape is what the benchmark says: every value parses in its syntax
// at about MaxLength bytes, and a shape the bound refuses holds many
// intervals in one value. The planted break: a shape whose separator its
// syntax does not read, which would measure the refusal of one value.
func TestHostileSyntaxShapes(t *testing.T) {
	for _, s := range syntaxShapes() {
		v := s.value(7)
		if len(v) < MaxLength-16 || len(v) > MaxLength {
			t.Errorf("%s/%s: %d bytes", s.syn, s.name, len(v))
		}
		r := Reader{Syntax: s.syn}
		if _, err := r.Parse(v); err != nil {
			t.Errorf("%s/%s: %v", s.syn, s.name, err)
			continue
		}
		if s.refused && r.intervals < 50 {
			t.Errorf("%s/%s holds %d intervals in one value", s.syn, s.name, r.intervals)
		}
	}
}

// A Version takes no more memory than before the syntaxes, 128 bytes on a
// 64 bit machine, since a range keeps several for each interval and the
// figures of docs/reference.md#compare count them: its flags and its
// syntax pack after its strings. The planted break: a field added, which
// makes 136.
func TestVersionSize(t *testing.T) {
	if n := unsafe.Sizeof(Version{}); unsafe.Sizeof(uintptr(0)) == 8 && n > 128 {
		t.Errorf("a Version takes %d bytes, want 128 or fewer", n)
	}
}
