package envfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Every value an envfile extractor returns sits at the line its key was
// written on, inside the file, whatever the form of the assignment: export,
// = or :, white space, a quoted value across lines, and comments and blank
// lines between. Each key returns its values in file order.
func TestPropertyEnvfileValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		items := extracttest.DrawItems(t, rapid.Just(""), rapid.StringMatching(`[A-Za-z_][A-Za-z0-9_.\-]{0,6}`), rapid.StringMatching(`[a-z0-9 =:#'"\\$\t\n{}é]{0,12}`))
		var w extracttest.LineWriter
		for i := range items {
			if rapid.Bool().Draw(t, "comment") {
				w.Add("%s comment", rapid.SampledFrom([]string{"#", "  #", "\t#"}).Draw(t, "mark"))
			}
			if rapid.Bool().Draw(t, "blank") {
				w.Add("%s", rapid.SampledFrom([]string{"", "  ", "\t"}).Draw(t, "space"))
			}
			value := items[i].Value
			form := quoted(value)
			// A value of letters and digits alone is written unquoted too.
			if strings.Trim(value, "abcdefghijklmnopqrstuvwxyz0123456789") == "" && rapid.Bool().Draw(t, "unquoted") {
				form = value
			} else if !strings.ContainsAny(value, `'\`) && rapid.Bool().Draw(t, "single") {
				form = "'" + value + "'"
			}
			prefix := rapid.SampledFrom([]string{"", "export ", "  ", "export\t"}).Draw(t, "prefix")
			sep := rapid.SampledFrom([]string{"=", " = ", ":", ": ", "=\t"}).Draw(t, "sep")
			items[i].Line = w.Add("%s%s%s%s", prefix, items[i].Key, sep, form)
			w.Line += strings.Count(form, "\n")
		}
		data := []byte(w.Text.String())
		if err := os.WriteFile(filepath.Join(root, ".env"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		fileLines := strings.Split(string(data), "\n")
		for _, it := range items {
			got := extracttest.MustExtract(t, Build, Spec{File: ".env", Name: it.Key}, root)
			var gotTexts, want []string
			for _, v := range got {
				extracttest.CheckLine(t, v, fileLines, data)
				gotTexts = append(gotTexts, fmt.Sprintf("%s@%d", v.Text, v.At.Line))
			}
			for _, other := range items {
				if other.Key == it.Key {
					if !strings.Contains(fileLines[other.Line-1], other.Key) {
						t.Fatalf("line %d is %q, which does not hold the key %q", other.Line, fileLines[other.Line-1], other.Key)
					}
					want = append(want, fmt.Sprintf("%s@%d", other.Value, other.Line))
				}
			}
			if strings.Join(gotTexts, " ") != strings.Join(want, " ") {
				t.Fatalf("key %q: got %q, want %q\n%s", it.Key, gotTexts, want, data)
			}
		}
	})
}
