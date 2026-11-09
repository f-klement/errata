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

// Every value read from etc/environment sits at the line its key was
// written on, whatever surrounds it: export, quotes, a comment after the
// value, comments, blank lines and lines pam_env skips between. Each key
// returns its values in file order, as pam_env reads them.
func TestPropertyPamEnvValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		items := extracttest.DrawItems(t, rapid.Just(""), rapid.StringMatching(`[A-Za-z0-9_]{1,6}`), rapid.StringMatching(`[a-z0-9 =:'"\\$\t{}é]{0,12}`))
		var w extracttest.LineWriter
		for i := range items {
			if rapid.Bool().Draw(t, "skipped") {
				w.Add("%s", rapid.SampledFrom([]string{"# c", "  # c \\", "", "\t", "A B=1", "=x", "x.y=1"}).Draw(t, "line"))
			}
			value := items[i].Value
			form := value
			// A value that starts with a quote or ends in one, or holds a
			// \, which could join the next line, is written in quotes, so it
			// reads back as itself.
			if value != "" && strings.ContainsAny(value[:1]+value[len(value)-1:], `'"`) || strings.Contains(value, `\`) || rapid.Bool().Draw(t, "quoted") {
				form = `"` + value + `"`
			}
			prefix := rapid.SampledFrom([]string{"", "export ", "  ", "\t"}).Draw(t, "prefix")
			comment := rapid.SampledFrom([]string{"", "#", "# c"}).Draw(t, "comment")
			items[i].Line = w.Add("%s%s=%s%s", prefix, items[i].Key, form, comment)
		}
		data := []byte(w.Text.String())
		if err := os.MkdirAll(filepath.Join(root, "etc"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "etc", "environment"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		fileLines := strings.Split(string(data), "\n")
		for _, it := range items {
			got := extracttest.MustExtract(t, Build, Spec{File: "etc/environment", Name: it.Key}, root)
			var gotTexts, want []string
			for _, v := range got {
				extracttest.CheckLine(t, v, fileLines, data)
				gotTexts = append(gotTexts, fmt.Sprintf("%s@%d", v.Text, v.At.Line))
			}
			for _, other := range items {
				if other.Key == it.Key {
					want = append(want, fmt.Sprintf("%s@%d", other.Value, other.Line))
				}
			}
			if strings.Join(gotTexts, " ") != strings.Join(want, " ") {
				t.Fatalf("key %q: got %q, want %q\n%s", it.Key, gotTexts, want, data)
			}
		}
	})
}

// Every value read from a file in etc/default sits at the line its key was
// written on, and a plain assignment, unquoted or in quotes, comes back as
// written: literal between lines that are not plain, and every value not
// literal when one line may carry a shell's state past it or run a command.
func TestPropertySourcedValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		items := extracttest.DrawItems(t, rapid.Just(""), rapid.StringMatching(`[A-Za-z_][A-Za-z0-9_]{0,6}`), rapid.StringMatching(`[a-z0-9_./:=-]{0,12}`))
		var w extracttest.LineWriter
		tainted := false // a line that carries state marks every value
		for i := range items {
			if rapid.Bool().Draw(t, "other") {
				line := rapid.SampledFrom([]string{"# c", "", "  ", "export OTHER_KEY=1", "OTHER_KEY=a b", "OTHER_KEY = 1", `OTHER_KEY="a\tb"`, "OTHER_KEY=it's"}).Draw(t, "line")
				w.Add("%s", line)
				// A quote and a \ carry, and so does a command a shell runs,
				// as in OTHER_KEY=a b and OTHER_KEY = 1.
				tainted = tainted || strings.HasPrefix(line, "OTHER_KEY")
			}
			form := rapid.SampledFrom([]string{"%s", `"%s"`, "'%s'", "%s  "}).Draw(t, "form")
			items[i].Line = w.Add("%s=%s", items[i].Key, fmt.Sprintf(form, items[i].Value))
		}
		data := []byte(w.Text.String())
		if err := os.MkdirAll(filepath.Join(root, "etc", "default"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "etc", "default", "app"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		fileLines := strings.Split(string(data), "\n")
		for _, it := range items {
			got := extracttest.MustExtract(t, Build, Spec{File: "etc/default/app", Name: it.Key}, root)
			var gotTexts, want []string
			for _, v := range got {
				extracttest.CheckLine(t, v, fileLines, data)
				gotTexts = append(gotTexts, fmt.Sprintf("%s@%d %t", v.Text, v.At.Line, v.NotLiteral))
			}
			for _, other := range items {
				if other.Key == it.Key {
					want = append(want, fmt.Sprintf("%s@%d %t", other.Value, other.Line, tainted))
				}
			}
			if strings.Join(gotTexts, " ") != strings.Join(want, " ") {
				t.Fatalf("key %q: got %q, want %q\n%s", it.Key, gotTexts, want, data)
			}
		}
	})
}
