package hcl

import (
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core/lineend"
)

// Every value lies inside its file, on the line errata counts for the
// start of its expression, whatever the file holds: attributes at the top
// and in blocks, literals and expressions the file computes, heredocs and
// calls across lines, CRLF and LF line ends, blank lines, and comments
// that hold a lone CR, which ends a line for errata and not for HCL. The
// oracle counts the line of the offset each expression was written at
// with lineend, as every built in reader counts lines. Each value's text
// and its not literal mark are the ones the generator wrote.
func TestPropertyEveryValueLiesOnItsExpressionsLine(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g := &gen{t: t}
		var top, inBlocks []string
		for i := range rapid.IntRange(0, 4).Draw(t, "items") {
			if rapid.Bool().Draw(t, "block") {
				g.write("b \"l" + strconv.Itoa(i) + "\" {" + g.end())
				for j := range rapid.IntRange(0, 3).Draw(t, "attributes") {
					name := "c" + strconv.Itoa(j)
					inBlocks = append(inBlocks, g.attribute("  ", name, "b.l"+strconv.Itoa(i)+"."+name))
				}
				g.write("}" + g.end())
				continue
			}
			name := "a" + strconv.Itoa(i)
			top = append(top, g.attribute("", name, name))
		}
		data := g.b.String()
		files := file("t.tf", data)
		if got := strings.Join(shown(extractPath(t, "[*]", files)), "\n"); got != strings.Join(orNothing(top, "[*]"), "\n") {
			t.Fatalf("[*]: got\n%s\nwant\n%s\nfrom %q", got, strings.Join(orNothing(top, "[*]"), "\n"), data)
		}
		if got := strings.Join(shown(extractPath(t, "b[*][*]", files)), "\n"); got != strings.Join(orNothing(inBlocks, "b[*][*]"), "\n") {
			t.Fatalf("b[*][*]: got\n%s\nwant\n%s\nfrom %q", got, strings.Join(orNothing(inBlocks, "b[*][*]"), "\n"), data)
		}
		last := len(lineend.Starts([]byte(data)))
		for _, v := range append(extractPath(t, "[*]", files).Values, extractPath(t, "b[*][*]", files).Values...) {
			if v.File != "t.tf" || v.Line < 1 || v.Line > last {
				t.Fatalf("the value %q is at %s:%d, outside the file of %d lines\n%q", *v.Text, v.File, v.Line, last, data)
			}
		}
	})
}

// orNothing is want, or the no value a path that matches nothing gives.
func orNothing(want []string, path string) []string {
	if len(want) == 0 {
		return []string{"no value: nothing in t.tf matches the path " + path}
	}
	return want
}

// gen writes an HCL file and the values it expects of it.
type gen struct {
	t *rapid.T
	b strings.Builder
}

func (g *gen) write(s string) { g.b.WriteString(s) }

// end is a line end, LF or CRLF, maybe with a blank line or a comment
// after it: a line comment or a block comment, each holding a lone CR.
func (g *gen) end() string {
	nl := rapid.SampledFrom([]string{"\n", "\r\n"}).Draw(g.t, "line end")
	return nl + rapid.SampledFrom([]string{"", nl, "# a\rb" + nl, "/* a\rb" + nl + "c */" + nl}).Draw(g.t, "after")
}

// attribute writes name = an expression after indent, and returns the
// value an extract shows for it, with cond its condition.
func (g *gen) attribute(indent, name, cond string) string {
	g.write(indent + name + " = ")
	at := g.b.Len()
	text, written, literal := g.expression()
	g.write(written + g.end())
	v := "t.tf:" + strconv.Itoa(lineend.Line([]byte(g.b.String()), at)) + "=" + text
	if !literal {
		v += " (not literal)"
	}
	return v + " key " + name + " when " + cond
}

// expression is an expression's text as an extract returns it, as the
// file writes it, and whether it is literal.
func (g *gen) expression() (string, string, bool) {
	n := strconv.Itoa(rapid.IntRange(0, 99).Draw(g.t, "n"))
	switch rapid.IntRange(0, 5).Draw(g.t, "expression") {
	case 0:
		return n, n, true
	case 1:
		return "s" + n, `"s` + n + `"`, true
	case 2:
		return "var.v" + n, "var.v" + n, false
	case 3:
		return `"${var.v}` + n + `"`, `"${var.v}` + n + `"`, false
	case 4:
		return "x\n" + n + "\n", "<<EOT\nx\n" + n + "\nEOT", true
	}
	call := "max(\r\n  1,\n  " + n + ")"
	return call, call, false
}
