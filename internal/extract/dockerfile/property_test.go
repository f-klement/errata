package dockerfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A generated Dockerfile of stages whose LABEL and ENV instructions set
// several keys each, a key to a line or several to one, among comment and
// blank lines inside an instruction and heredocs that only look like one,
// with LF or CRLF line ends: every value sits inside the file at the line
// its key was written on, and each key's values come in file order.
func TestPropertyDockerfileValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		end := rapid.SampledFrom([]string{"\n", "\r\n"}).Draw(t, "end")
		w := extracttest.LineWriter{End: func() string { return end }}
		in := rapid.SampledFrom([]string{"LABEL", "ENV"}).Draw(t, "instruction")
		keys := []string{"a", "b.c", "D_1"}
		var want []string
		w.Add("FROM base")
		for range rapid.IntRange(1, 6).Draw(t, "instructions") {
			if rapid.Bool().Draw(t, "heredoc") {
				w.Add("RUN <<EOF")
				w.Add("%s a=no", in)
				w.Add("EOF")
			}
			n := rapid.IntRange(1, 4).Draw(t, "pairs")
			for i := range n {
				key := rapid.SampledFrom(keys).Draw(t, "key")
				value := rapid.StringMatching(`[a-z0-9.:/-]{0,8}`).Draw(t, "value")
				form := rapid.SampledFrom([]string{"%s=%s", "%s=\"%s\"", "%s='%s'"}).Draw(t, "form")
				pair := fmt.Sprintf(form, key, value)
				prefix := "    "
				if i == 0 {
					prefix = in + " "
				}
				cont := ""
				if i < n-1 {
					cont = " \\"
				}
				if i < n-1 && rapid.Bool().Draw(t, "same line") {
					// The next pair on this line: write no line end yet.
					w.Text.WriteString(prefix + pair + " ")
					want = append(want, fmt.Sprintf("%s=%s@%d", key, value, w.Line+1))
					continue
				}
				line := w.Add("%s%s%s", prefix, pair, cont)
				want = append(want, fmt.Sprintf("%s=%s@%d", key, value, line))
				if i < n-1 {
					switch rapid.IntRange(0, 2).Draw(t, "between") {
					case 1:
						w.Add("  # a comment")
					case 2:
						w.Add("")
					}
				}
			}
		}
		data := []byte(w.Text.String())
		if err := os.WriteFile(filepath.Join(root, "Dockerfile"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		got := extracttest.MustExtract(t, Build, Spec{OneFile: core.OneFile{File: "Dockerfile"}, Instruction: in}, root)
		fileLines := strings.Split(string(data), "\n")
		var texts []string
		for _, v := range got {
			extracttest.CheckLine(t, v, fileLines, data)
			texts = append(texts, fmt.Sprintf("%s=%s@%d", v.Key, v.Text, v.At.Line))
		}
		if g, w := strings.Join(texts, " "), strings.Join(want, " "); g != w {
			t.Fatalf("got  %s\nwant %s\n%q", g, w, data)
		}
	})
}
