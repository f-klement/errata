package udev

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// written is one key and value a generator writes: its text, and for an
// assignment of MODE how errata returns it.
type written struct {
	text, match, mode string
}

// drawMatch draws a match key as a rule writes it, with the text errata's
// condition names it by.
func drawMatch(t *rapid.T) written {
	k := rapid.SampledFrom([]string{"KERNEL", "SUBSYSTEM", "ATTR{idVendor}", "ENV{ID_A}", "TAG"}).Draw(t, "match")
	o := rapid.SampledFrom([]string{"==", "!="}).Draw(t, "matchop")
	v := `"` + rapid.StringMatching(`[a-z0-9*?|]{0,4}`).Draw(t, "pattern") + `"`
	return written{text: k + spaced(t, o) + v, match: k + o + v}
}

// drawAssign draws an assignment of MODE, which errata returns, or of a
// key it does not.
func drawAssign(t *rapid.T) written {
	k := rapid.SampledFrom([]string{"MODE", "OWNER", "SYMLINK"}).Draw(t, "key")
	o := rapid.SampledFrom([]string{"=", ":=", "+="}).Draw(t, "op")
	v := rapid.StringMatching(`[a-z0-9/]{0,5}`).Draw(t, "value")
	w := written{text: k + spaced(t, o) + `"` + v + `"`}
	if k == "MODE" {
		w.mode = "MODE" + strings.Replace(o, "+=", "=", 1) + v
	}
	return w
}

// spaced is o with white space drawn around it.
func spaced(t *rapid.T, o string) string {
	sp := func() string { return rapid.SampledFrom([]string{"", " ", "\t", "  "}).Draw(t, "space") }
	return sp() + o + sp()
}

// ruleWriter writes the rules of one file, a rule's keys on lines a \
// joins, and the values a MODE spec returns.
type ruleWriter struct {
	w    extracttest.LineWriter
	want []string
}

// rule writes one rule of the keys ws, in order, at lines a separator
// drawn for each may break with a \.
func (rw *ruleWriter) rule(t *rapid.T, ws []written) {
	line := rapid.SampledFrom([]string{"", " ", "\t"}).Draw(t, "indent")
	var matches []string
	type at struct {
		mode string
		line int
	}
	var modes []at
	for i, w := range ws {
		if i > 0 {
			sep := rapid.SampledFrom([]string{", ", ",", " ", ",\\", " \\", ", \\"}).Draw(t, "sep")
			line += sep
			if strings.HasSuffix(sep, "\\") {
				rw.w.Add("%s", line)
				line = rapid.SampledFrom([]string{"", "  ", "\t"}).Draw(t, "indent")
			}
		}
		line += w.text
		if w.match != "" {
			matches = append(matches, w.match)
		}
		if w.mode != "" {
			modes = append(modes, at{w.mode, rw.w.Line + 1})
		}
	}
	rw.w.Add("%s", line)
	for _, m := range modes {
		rw.want = append(rw.want, fmt.Sprintf("%s@%d[%s]", m.mode, m.line, strings.Join(matches, ", ")))
	}
}

// Every value a udev extractor returns sits at the line its key was
// written on, with its rule's match keys as its condition, whatever the
// white space and commas between keys, the \ that join a rule's lines, the
// comments and empty lines between rules, and the line ends: a LF, a CRLF
// or a lone CR. Values come in file order.
func TestPropertyUdevValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "r.rules")
	rapid.Check(t, func(t *rapid.T) {
		rw := ruleWriter{w: extracttest.LineWriter{End: func() string { return rapid.SampledFrom([]string{"\n", "\r\n", "\r"}).Draw(t, "end") }}}
		for range rapid.IntRange(1, 8).Draw(t, "rules") {
			if rapid.Bool().Draw(t, "skip") {
				rw.w.Add("%s", rapid.SampledFrom([]string{"# MODE=\"x\"", " ", "\t", "\t# c"}).Draw(t, "skipped"))
			}
			ws := rapid.SliceOfN(rapid.Custom(drawMatch), 0, 2).Draw(t, "matches")
			ws = append(ws, rapid.SliceOfN(rapid.Custom(drawAssign), 1, 3).Draw(t, "assigns")...)
			rw.rule(t, rapid.Permutation(ws).Draw(t, "order"))
		}
		data := rw.w.Text.String()
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		got := extracttest.MustExtract(t, Build, one("r.rules", "MODE"), root)
		if g, w := shown(got), strings.Join(rw.want, " "); g != w {
			t.Fatalf("got  %s\nwant %s\n%q", g, w, data)
		}
	})
}

// shown is each value as key and text, its line and its condition.
func shown(vs []check.Value) string {
	var out []string
	for _, v := range vs {
		out = append(out, fmt.Sprintf("%s%s@%d[%s]", v.Key, v.Text, v.At.Line, v.Condition))
	}
	return strings.Join(out, " ")
}
