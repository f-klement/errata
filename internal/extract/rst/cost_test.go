package rst

import (
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// costShapes are files that each pass of the parse could read again and
// again, as each makes a file of about n bytes: markup that starts and
// never ends, a simple table with as many columns as short rows, and many
// comments, code blocks, sections and containers.
var costShapes = []struct {
	name  string
	shape func(n int) string
	spec  Spec
}{
	{"emphasis without an end", repeat("A\n=\n\n", "*a "), sel("section", "section", "A")},
	{"strong without an end", repeat("A\n=\n\n", "**a "), sel("section", "section", "A")},
	{"literal without an end", repeat("A\n=\n\n", "``a "), sel("section", "section", "A")},
	{"interpreted text without an end", repeat("A\n=\n\n", "`a "), sel("section", "section", "A")},
	{"roles without an end", repeat("A\n=\n\n", ":a:`b :c-d. "), sel("section", "section", "A")},
	{"ends a start does not take", repeat("A\n=\n\n", "a* a` a`` a** "), sel("section", "section", "A")},
	{"a wide simple table", func(n int) string {
		border := strings.Repeat("= ", n/8) + "\n"
		return border + "H P\n" + border + strings.Repeat("x\n\n", n/6) + border
	}, sel("table", "column", "P")},
	{"comments", repeat("", ".. a\n   b\n"), sel("comment")},
	{"code blocks", repeat("", ".. code:: sh\n\n   a\n\n"), sel("code")},
	{"sections", repeat("", "A\n=\n\n:: \n\n  a\n\n"), sel("section", "section", "A")},
	{"nested containers", repeat("", ".. note::\n\n   .. note::\n\n      x\n"), sel("comment")},
}

// repeat is a shape of head, then unit repeated to fill n bytes.
func repeat(head, unit string) func(n int) string {
	return func(n int) string { return head + strings.Repeat(unit, n/len(unit)) }
}

// Every shape costs time and memory in proportion to the file: four times
// the bytes take at most six times the time, with a tenth of a second for
// the timer, and allocate at most eight times the bytes, since a slice
// that doubles may double once more in the larger file, where a quadratic
// pass allocates sixteen times. A ratio, not a time, so it holds on any
// node. Each shape is small, so the test runs on
// every mutant. Planted breaks, each seen failing on its shape: the search
// for an end started again at each start, which a finder keeps from
// happening (emphasis), and a simple table's short line cut at each of
// the table's columns (a wide simple table).
func TestCostIsLinearInTheFile(t *testing.T) {
	for _, c := range costShapes {
		t.Run(c.name, func(t *testing.T) {
			measure := func(size int) (time.Duration, uint64) {
				body := c.shape(size)
				var took time.Duration
				alloc := extracttest.Allocated(func() {
					start := time.Now()
					if _, err := extracted(c.spec, body); err != nil {
						t.Fatal(err)
					}
					took = time.Since(start)
				})
				return took, alloc
			}
			t1, a1 := measure(64 << 10)
			t4, a4 := measure(256 << 10)
			t.Logf("64 KiB: %v, %d bytes; 256 KiB: %v, %d bytes", t1, a1, t4, a4)
			if limit := 6*t1 + 100*time.Millisecond*extracttest.RaceSlowdown; t4 > limit {
				t.Errorf("four times the bytes took %v, over %v", t4, limit)
			}
			if a4 > 8*a1 {
				t.Errorf("four times the bytes allocated %d, over eight times %d", a4, a1)
			}
		})
	}
}
