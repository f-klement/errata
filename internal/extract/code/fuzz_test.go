package code

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/core"
)

// checkGo fails t unless reading data as a Go file either is refused, or
// gives every name it declares a value at a line inside the file, computed
// or not, without a crash.
func checkGo(t *testing.T, data []byte) {
	t.Helper()
	g, err := readGo("f.go", data, core.DefaultMaxBytes)
	if err != nil {
		if !strings.HasPrefix(err.Error(), "f.go: ") {
			t.Fatalf("%q: a refusal that names no file: %v", data, err)
		}
		return
	}
	last := core.LastLine(data)
	for name := range g.decls {
		n, _, ok, err := g.value(name)
		if err != nil {
			return
		}
		if !ok || n.Shape != core.Scalar || n.Line < 1 || n.Line > last {
			t.Fatalf("%q: %s gives %+v, found %t, outside the file's %d lines", data, name, n, ok, last)
		}
	}
}

// Any bytes read as a Go file give each name it declares a value inside the
// file, or a refusal that names the file, and never a crash: go/constant
// panics on an operation its operands' kinds do not take, which the reader
// must never hand it.
func FuzzGo(f *testing.F) {
	f.Add(goSrc)
	f.Add("package p\nconst (\n\tA = iota * 1.5\n\tB\n\tC = B << A\n\tD = ^C % 3 &^ 'x'\n)\nvar E = -D / 0.5\n")
	f.Add("package p\ntype T float32\nconst X T = 1e38 * 10\nconst Y = !(X > 1) || \"a\" < \"b\"\n")
	f.Fuzz(func(t *testing.T, src string) {
		checkGo(t, []byte(src))
	})
}

// goTime is the fastest of runs reads of the name in src, a Go file.
func goTime(t *testing.T, src, name string, runs int) time.Duration {
	t.Helper()
	fastest := time.Duration(math.MaxInt64)
	for range runs {
		start := time.Now()
		g, err := readGo("f.go", []byte(src), core.DefaultMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := g.value(name); err != nil {
			t.Fatal(err)
		}
		fastest = min(fastest, time.Since(start))
	}
	return fastest
}

// A Go read is linear in the file (ADR 0003): m constants each built from
// the two before it, which the evaluation reads once each; m vars the
// program assigns m times; and an iota block of m specs. Four times the
// input takes at most eight times as long, and a tenth of a second. It is
// slow under make test's race and coverage, so make slow runs it.
func TestGoReadsAreLinear(t *testing.T) {
	slowOnly(t)
	for _, c := range []struct {
		name  string
		shape func(m int) string
		ask   func(m int) string
	}{
		{"constants built from constants", func(m int) string {
			var b strings.Builder
			b.WriteString("package p\nconst C0, C1 = 1, 1\n")
			for i := 2; i < m; i++ {
				fmt.Fprintf(&b, "const C%d = C%d/2 + C%d/2 + C%d%%2\n", i, i-1, i-2, i)
			}
			return b.String()
		}, func(m int) string { return fmt.Sprintf("C%d", min(m-1, 200)) }},
		{"a var assigned", func(m int) string {
			var b strings.Builder
			b.WriteString("package p\nvar V = 1\nfunc f() {\n")
			for range m {
				b.WriteString("\tV = 2\n")
			}
			return b.String() + "}\n"
		}, func(int) string { return "V" }},
		{"an iota block", func(m int) string {
			return "package p\nconst (\n\tA = 1 << iota\n" + strings.Repeat("\t_\n", m) + "\tZ\n)\n"
		}, func(int) string { return "Z" }},
	} {
		const m = 5000
		quarter := goTime(t, c.shape(m), c.ask(m), 3)
		whole := goTime(t, c.shape(4*m), c.ask(4*m), 3)
		t.Logf("%s: %d took %v, a quarter of them %v", c.name, 4*m, whole, quarter)
		if limit := 8*quarter + 100*time.Millisecond; whole > limit {
			t.Errorf("%s: %d took %v, over %v, eight times the %v of a quarter and a tenth of a second", c.name, 4*m, whole, limit, quarter)
		}
	}
}

// Each constant is evaluated once, however often others name it: C25 of a
// chain whose every constant names the two before it evaluates 26
// constants, where evaluating each name again would take some 240000, and
// C60 some 10^12.
// Planted break: the memo not read.
func TestEachConstantIsEvaluatedOnce(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\nconst C0, C1 = 1, 1\n")
	for i := 2; i <= 25; i++ {
		fmt.Fprintf(&b, "const C%d = C%d + C%d\n", i, i-1, i-2)
	}
	g, err := readGo("f.go", []byte(b.String()), core.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	n, _, _, err := g.value("C25")
	if err != nil || n.Text != "121393" || g.evals != 26 {
		t.Errorf("C25 is %+v, %v, after %d evaluations", n, err, g.evals)
	}
}

// balanced joins terms with op into one expression nested about log2 of
// their number deep, as (a + b) + (c + d).
func balanced(terms []string, op string) string {
	if len(terms) == 1 {
		return terms[0]
	}
	half := len(terms) / 2
	return "(" + balanced(terms[:half], op) + " " + op + " " + balanced(terms[half:], op) + ")"
}

// sharedSpecs is an iota block of m specs that each repeat one expression
// of m literals, with iota in it when iota is set, and a constant Z that
// sums the specs: m squared leaves to evaluate, in a file of about 20m
// bytes.
func sharedSpecs(m int, iota bool) string {
	terms := make([]string, m)
	names := make([]string, m)
	for i := range m {
		terms[i], names[i] = "1", fmt.Sprintf("B%d", i)
	}
	if iota {
		terms[0] = "iota"
	}
	var b strings.Builder
	b.WriteString("package p\nconst (\n\tB0 = " + balanced(terms, "+") + "\n")
	for i := 1; i < m; i++ {
		fmt.Fprintf(&b, "\tB%d\n", i)
	}
	b.WriteString(")\nconst Z = " + balanced(names, "+") + "\n")
	return b.String()
}

// comparedStrings is a string of m bytes and a constant that compares it
// with itself m/16 times: m squared / 8 bytes go/constant joins to compare,
// in a file of about 2m bytes.
func comparedStrings(m int) string {
	tests := make([]string, max(m/16, 1))
	for i := range tests {
		tests[i] = "S == S"
	}
	return "package p\nconst S = \"" + strings.Repeat("x", m) + "\"\nconst Z = " + balanced(tests, "&&") + "\n"
}

// bigRationals is a rational of about 3300 bits, the quotient of two float
// literals of 1000 digits, summed m times: each addition costs go/constant
// some 40 microseconds, which the budget charges by the bits it adds.
func bigRationals(m int) string {
	digits := strings.Repeat("7", 499) + "." + strings.Repeat("3", 500)
	terms := make([]string, m)
	for i := range terms {
		terms[i] = "P"
	}
	return "package p\nconst N1 = " + digits + "\nconst N2 = " + strings.Replace(digits, "7", "9", 1) +
		"\nconst P = N1 / N2\nconst Z = " + balanced(terms, "+") + "\n"
}

// biggerRationals is a rational of about 8169 bits, numerator and
// denominator each under go/constant's cap of 4096, built from four float
// literals of 1000 and 230 digits and summed m times: each addition
// cross-multiplies, so it costs the square of its operands' size (delta
// review 2, N1), which the budget charges.
func biggerRationals(m int) string {
	long := strings.Repeat("7", 499) + "." + strings.Repeat("3", 500)
	short := strings.Repeat("3", 114) + "." + strings.Repeat("7", 115)
	terms := make([]string, m)
	for i := range terms {
		terms[i] = "P"
	}
	return "package p\nconst N1 = " + long + "\nconst N2 = " + strings.Replace(long, "7", "9", 1) +
		"\nconst Na = " + short + "\nconst Nb = " + strings.Replace(short, "3", "1", 1) +
		"\nconst P = N1 * Na / N2 / Nb\nconst Z = " + balanced(terms, "+") + "\n"
}

// convertedRationals is biggerRationals's rational converted to float64 m
// times and summed: each conversion costs its operand's bits, some 127
// units, and the sums of float64 values little, so only the charge by bits
// refuses it.
func convertedRationals(m int) string {
	src := biggerRationals(1)
	terms := make([]string, m)
	for i := range terms {
		terms[i] = "float64(P)"
	}
	return src[:strings.LastIndex(src, "const Z = ")] + "const Z = " + balanced(terms, "+") + "\n"
}

// readOrRefuse is the time the fastest of three reads of Z in src takes,
// and the read's error, if any.
func readOrRefuse(t *testing.T, src string) (time.Duration, error) {
	t.Helper()
	fastest := time.Duration(math.MaxInt64)
	var last error
	for range 3 {
		start := time.Now()
		g, err := readGo("f.go", []byte(src), core.DefaultMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, last = g.value("Z")
		fastest = min(fastest, time.Since(start))
	}
	return fastest, last
}

// The shapes that made the evaluation quadratic before it had a budget
// (review 1, F1; delta reviews 1 and 2, D1 and N1): an iota block whose
// every spec repeats an expression of m leaves, with iota in it; one string
// compared with itself m/16 times; and a big rational, and one near
// go/constant's cap, summed again and again. Each read at four times the
// size takes at most eight times as long, and a tenth of a second, whether
// it reads or the budget refuses it. It is slow (some 45 s under make
// test's race and coverage), so make slow runs it; TestGoWorkBudget checks
// the budget itself on every run.
func TestGoWorkIsLinear(t *testing.T) {
	slowOnly(t)
	for _, c := range []struct {
		name  string
		shape func(m int) string
	}{
		{"specs repeating an expression with iota", func(m int) string { return sharedSpecs(m, true) }},
		{"a string compared with itself", func(m int) string { return comparedStrings(64 * m) }},
		{"a big rational summed", bigRationals},
		{"a rational near the cap summed", biggerRationals},
	} {
		for _, m := range []int{250, 1000} {
			quarter, _ := readOrRefuse(t, c.shape(m))
			whole, err := readOrRefuse(t, c.shape(4*m))
			t.Logf("%s: %d bytes took %v (%v), a quarter of them %v", c.name, len(c.shape(4*m)), whole, err, quarter)
			if limit := 8*quarter + 100*time.Millisecond; whole > limit {
				t.Errorf("%s at %d: took %v, over %v, eight times the %v of a quarter and a tenth of a second", c.name, 4*m, whole, limit, quarter)
			}
		}
	}
}

// The budget refuses each of those shapes at a small size, a comment buys
// no work, one addition of two rationals near the cap costs the product of
// their words, and the specs that repeat an expression that reads no iota
// share its one value. Planted breaks: spend never refuses; the shared
// value not kept; charge counting no bits; the budget counting the file's
// bytes; chargeProduct charging nothing.
func TestGoWorkBudget(t *testing.T) {
	// A comment is no token, so it buys no work: the same block behind a
	// comment of 2 MiB is refused as without it, where a budget counted
	// from the file's bytes would read it.
	padded := "// " + strings.Repeat("x", 2<<20) + "\n" + sharedSpecs(1000, true)
	for _, src := range []string{sharedSpecs(1000, true), comparedStrings(64000), bigRationals(1000), biggerRationals(1000), convertedRationals(4000), padded} {
		if _, err := readOrRefuse(t, src); err == nil || !strings.Contains(err.Error(), "units of work, the limit for a file of") {
			t.Errorf("a file of %d bytes past the budget: %v", len(src), err)
		}
	}
	// One addition of two such rationals costs the product of their words,
	// some 128 by 128.
	pair, err := readGo("f.go", []byte(biggerRationals(2)), core.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := pair.value("Z"); err != nil || pair.work < 128*128 {
		t.Errorf("P + P near the cap: %v, after %d units", err, pair.work)
	}
	g, err := readGo("f.go", []byte(sharedSpecs(4000, false)), core.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if n, _, _, err := g.value("Z"); err != nil || n.Text != "16000000" || g.evals != 2 {
		t.Errorf("4000 specs sharing one expression: %+v, %v, after %d evaluations", n, err, g.evals)
	}
}
