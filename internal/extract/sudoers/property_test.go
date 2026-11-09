package sudoers

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

// written is one value a generated line sets: the text a read gives, its
// condition, and its line.
type written struct {
	text, condition string
	line            int
}

// sep draws white space between words.
func sep(t *rapid.T, label string) string {
	return rapid.SampledFrom([]string{" ", "\t", "  "}).Draw(t, label)
}

// defaultsLine draws a Defaults line of secure_path, with a scope or none,
// and the value it sets.
func defaultsLine(t *rapid.T) (string, written) {
	scope := rapid.SampledFrom([]string{"", ":alice", "@web1", ">root", "!/bin/ls"}).Draw(t, "scope")
	v := rapid.StringMatching(`[a-z0-9/:.]{1,8}`).Draw(t, "value")
	w := written{text: v}
	if scope != "" {
		w.condition = "Defaults" + scope
	}
	eq := rapid.SampledFrom([]string{"=", " = ", "\t=\t"}).Draw(t, "eq")
	tail := rapid.SampledFrom([]string{"", " # c", "\t#x"}).Draw(t, "tail")
	return "Defaults" + scope + sep(t, "sep") + "secure_path" + eq + v + tail, w
}

// grantLines draws a grant to alice, or to bob, which alice's spec does
// not select, over one line or two joined by a \, and the command it
// grants at the line the command is on.
func grantLines(t *rapid.T) (first, second string, w written, alice bool) {
	who := rapid.SampledFrom([]string{"alice", "bob", "ALL"}).Draw(t, "user")
	cmd := rapid.StringMatching(`/[a-z]{1,4}(/[a-z0-9]{1,4})?`).Draw(t, "command")
	if rapid.Bool().Draw(t, "args") {
		cmd += " " + rapid.StringMatching(`[a-z0-9-]{1,5}`).Draw(t, "arg")
	}
	head := who + sep(t, "s1") + "ALL" + rapid.SampledFrom([]string{"=", " = "}).Draw(t, "eq") +
		rapid.SampledFrom([]string{"", "(root) ", "NOPASSWD: ", "(ALL:ALL) NOPASSWD:"}).Draw(t, "spec")
	w = written{text: cmd}
	if rapid.Bool().Draw(t, "joined") {
		return head + " \\", sep(t, "s2") + cmd, w, who != "bob"
	}
	return head + cmd, "", w, who != "bob"
}

// Every value a sudoers extractor returns sits at the line its value is
// written on, inside the file, whatever the white space, comments and
// line ends, with lines that end in a LF, a CRLF or a lone CR, a command
// on the line after a \ that joins it, and lines sudo passes over between
// them; each spec returns its values in file order.
func TestPropertySudoersValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		w := extracttest.LineWriter{End: func() string { return rapid.SampledFrom([]string{"\n", "\r\n", "\r"}).Draw(t, "end") }}
		var defs, grants []written
		for range rapid.IntRange(1, 10).Draw(t, "n") {
			if rapid.Bool().Draw(t, "skip") {
				w.Add("%s", rapid.SampledFrom([]string{"# Defaults secure_path=/x", "  ", "#alice ALL = /x", "Defaults env_reset"}).Draw(t, "skipped"))
			}
			if rapid.Bool().Draw(t, "defaults") {
				text, d := defaultsLine(t)
				d.line = w.Add("%s", text)
				defs = append(defs, d)
				continue
			}
			first, second, g, alice := grantLines(t)
			g.line = w.Add("%s", first)
			if second != "" {
				g.line = w.Add("%s", second)
			}
			if alice {
				grants = append(grants, g)
			}
		}
		data := []byte(w.Text.String())
		if err := os.WriteFile(filepath.Join(root, "sudoers"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		got := extracttest.MustExtract(t, Build, defaults("sudoers", "secure_path"), root)
		checkWritten(t, "defaults", got, defs, data, func(i int) string { return got[i].Text })
		got = extracttest.MustExtract(t, Build, user("sudoers", "alice"), root)
		checkWritten(t, "grants", got, grants, data, func(i int) string { return got[i].Data.Field("command").Text })
	})
}

// checkWritten holds the values got, whose text text gives, to want, in
// order, each at its line with its condition.
func checkWritten(t *rapid.T, what string, got []check.Value, want []written, data []byte, text func(int) string) {
	var g, w []string
	for i, v := range got {
		g = append(g, fmt.Sprintf("%s@%d[%s]", text(i), v.At.Line, v.Condition))
	}
	for _, x := range want {
		w = append(w, fmt.Sprintf("%s@%d[%s]", x.text, x.line, x.condition))
	}
	if strings.Join(g, " ") != strings.Join(w, " ") {
		t.Fatalf("%s: got %q, want %q\n%q", what, g, w, data)
	}
}
