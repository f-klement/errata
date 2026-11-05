package literal

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/core"
)

// parseTime is the fastest of runs parses of src as the row named lang.
func parseTime(t *testing.T, lang, src string, runs int) time.Duration {
	t.Helper()
	l, _ := core.LanguageNamed(lang)
	data := []byte(src)
	fastest := time.Duration(math.MaxInt64)
	for range runs {
		start := time.Now()
		if _, err := Parse("f", l, data); err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		fastest = min(fastest, time.Since(start))
	}
	return fastest
}

// A read is linear in the file on the shapes that made it quadratic before
// the root kept each key's place (ADR 0003): m names, then the last one
// assigned m times; m names, then a method called on the last m times; a
// dotted name's member assigned m times; and a mapping that writes one key
// m times. Four times the input takes at most eight times as long, and a
// tenth of a second. Review 1, finding 2: 428,890 bytes of the first shape
// took 4.4 s and 1,748,890 bytes 96 s. Planted break: set calling
// core.Node.Set for a key it has, which searches the keys.
func TestReadsAreLinear(t *testing.T) {
	names := func(m int) string {
		var b strings.Builder
		for i := range m {
			fmt.Fprintf(&b, "N%d = %d\n", i, i)
		}
		return b.String()
	}
	for _, c := range []struct {
		name, lang string
		shape      func(m int) string
	}{
		{"reassigned", "python", func(m int) string { return names(m) + strings.Repeat(fmt.Sprintf("N%d = 1\n", m-1), m) }},
		{"a method called", "python", func(m int) string { return names(m) + strings.Repeat(fmt.Sprintf("N%d.append(1)\n", m-1), m) }},
		{"a member assigned", "ruby", func(m int) string {
			var b strings.Builder
			for i := range m {
				fmt.Fprintf(&b, "s.k%d = %d\n", i, i)
			}
			return b.String() + strings.Repeat(fmt.Sprintf("s.k%d = 1\n", m-1), m)
		}},
		{"a key written again", "python", func(m int) string { return "D = {" + strings.Repeat("'a': 1, ", m) + "}\n" }},
	} {
		const m = 5000
		quarter := parseTime(t, c.lang, c.shape(m), 3)
		whole := parseTime(t, c.lang, c.shape(4*m), 3)
		t.Logf("%s: %d names took %v, a quarter of them %v", c.name, 4*m, whole, quarter)
		if limit := 8*quarter + 100*time.Millisecond; whole > limit {
			t.Errorf("%s: %d took %v, over %v, eight times the %v of a quarter and a tenth of a second", c.name, 4*m, whole, limit, quarter)
		}
	}
}
