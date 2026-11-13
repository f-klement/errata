package rst

import (
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// blocks are the constructs a drawn file is made of, each holding markers
// m<n><letter> that no other block holds, n being the block's number.
var blocks = []string{
	"A\n=\n\n",
	"Install\n-------\n\n",
	"~~~\nA\n~~~\n\n",
	"p m%[1]da *m%[1]db*\n``m%[1]dc`` x::\n\n   code m%[1]dd\n     m%[1]de\n\n",
	"- m%[1]da\n  m%[1]db\n\n",
	".. c m%[1]da\n   m%[1]db\n\n",
	".. code-block:: sh\n   :linenos:\n\n   m%[1]da\n\n   m%[1]db\n\n",
	"+--------+--------+\n| Host   | Port   |\n+========+========+\n| web    | m%02[1]da   |\n|        | m%02[1]db   |\n+--------+--------+\n\n",
	"====  ======\nHost  Port\n====  ======\nweb   m%[1]da\n      m%[1]db\n====  ======\n\n",
	".. note:: m%[1]da\n\n   m%[1]db\n\n",
	"\tq m%[1]da\n\n",
}

// marked are specs whose values are markers, each a value of one line.
var marked = []Spec{
	sel("comment", "pattern", `(m\d+[a-z])`),
	sel("section", "section", "A", "pattern", `(m\d+[a-z])`),
	sel("section", "section", "Install", "pattern", `(m\d+[a-z])`),
	sel("code", "pattern", `(m\d+[a-z])`),
	sel("table", "column", "Port", "key", "Host", "row", "web", "pattern", `(m\d+[a-z])`),
}

// Every value of every target is at a line inside its file, and is at its
// line of the unnormalised file: a marker a pattern finds in a section or a
// code block is on that line, a comment's is on the line of its .., with
// LF or CRLF line ends, with or without a byte order mark, and among lines
// of other markup. The planted break: the line of a match counted in the
// text rather than mapped back to the file.
func TestEveryValueIsAtItsLine(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var b strings.Builder
		for i := range rapid.IntRange(0, 12).Draw(t, "blocks") {
			if rapid.IntRange(0, 4).Draw(t, "junk") == 0 {
				b.WriteString(rapid.StringMatching("[ .:=*`|+_a-z\n-]{0,24}").Draw(t, "text") + "\n\n")
			}
			block := blocks[rapid.IntRange(0, len(blocks)-1).Draw(t, "block")]
			if strings.Contains(block, "%") {
				block = fmt.Sprintf(block, i)
			}
			b.WriteString(block)
		}
		body := b.String()
		if rapid.Bool().Draw(t, "crlf") {
			body = strings.ReplaceAll(body, "\n", "\r\n")
		}
		if rapid.Bool().Draw(t, "bom") {
			body = core.BOM + body
		}
		file := strings.Split(body, "\n")
		for _, s := range append(marked, specs...) {
			values, err := extracted(s, body)
			if err != nil && strings.Contains(err.Error(), "inconsistent with the titles") {
				return // the blocks' titles may draw levels docutils refuses
			}
			if err != nil && strings.Contains(err.Error(), "simple table that starts here has no bottom border") {
				return // junk such as "= =" opens a simple table no block closes
			}
			if err != nil {
				t.Fatalf("%+v: %v", s.Selector, err)
			}
			for _, v := range values {
				if v.At.Line < 1 || v.At.Line > len(file) {
					t.Fatalf("%+v: %q at line %d of %d", s.Selector, v.Text, v.At.Line, len(file))
				}
				line := file[v.At.Line-1]
				switch {
				case s.Pattern != `(m\d+[a-z])` || s.Target == "table":
				case s.Target == "comment" && !strings.Contains(line, ".."):
					t.Fatalf("%+v: %q at line %d, %q, which holds no ..", s.Selector, v.Text, v.At.Line, line)
				case s.Target != "comment" && !strings.Contains(line, v.Text):
					t.Fatalf("%+v: %q at line %d, %q", s.Selector, v.Text, v.At.Line, line)
				}
			}
		}
	})
}

// extracted is the values s reads from body.
func extracted(s Spec, body string) ([]check.Value, error) {
	s.File = "a.rst"
	e, err := Build(s)
	if err != nil {
		return nil, err
	}
	return e.(extractor).FromData([]byte(body))
}

// Each block of the drawn files gives its markers to the target that reads
// it, so the property above does not hold on nothing. The planted break: a
// block's markers left out, as a target that reads nothing would.
func TestTheBlocksHoldValues(t *testing.T) {
	var b strings.Builder
	for i, block := range blocks {
		if strings.Contains(block, "%") {
			block = fmt.Sprintf(block, i)
		}
		b.WriteString(block)
	}
	got := map[string]int{}
	for _, s := range marked {
		values, err := extracted(s, b.String())
		if err != nil {
			t.Fatal(err)
		}
		got[s.Target+" "+s.Section] = len(values)
	}
	for target, n := range map[string]int{"comment ": 2, "section A": 16, "section Install": 8, "code ": 4, "table ": 4} {
		if got[target] != n {
			t.Errorf("%s: %d values, want %d", target, got[target], n)
		}
	}
}
