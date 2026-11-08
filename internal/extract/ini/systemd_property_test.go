package ini

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Every value of a unit file sits at the line its key was written on,
// whatever line ends, comments and BOM come before, and a value a \
// continues over comment lines sits at its first line, holding its
// lines joined with a space for each \. Planted break: a continued value
// numbered at its last line.
func TestPropertySystemdValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		items := extracttest.DrawItems(t, rapid.StringMatching(`[A-Z][a-z]{0,6}`), rapid.StringMatching(`[A-Z][A-Za-z]{0,6}`),
			rapid.StringMatching(`[a-z0-9/]([a-z0-9.;#=:"' -]{0,10}[a-z0-9])?`))
		w := extracttest.LineWriter{End: anyEnd(t)}
		if rapid.Bool().Draw(t, "bom") {
			w.Text.WriteString(core.BOM)
		}
		for i := range items {
			if i == 0 || items[i].Section != items[i-1].Section {
				w.Add("[%s]", items[i].Section)
			}
			if rapid.Bool().Draw(t, "comment") {
				w.Add("%s X=y", rapid.SampledFrom([]string{"#", "  ;", "\t#"}).Draw(t, "mark"))
			}
			value := items[i].Value
			if !rapid.Bool().Draw(t, "continued") {
				items[i].Line = w.Add("%s%s%s", items[i].Key, rapid.SampledFrom([]string{"=", " = ", "=\t"}).Draw(t, "sep"), value)
				continue
			}
			// The value split in two at a space, which the \ stands for.
			cut := rapid.IntRange(0, len(value)).Draw(t, "cut")
			if rest := strings.TrimLeft(value[cut:], " \t"); strings.HasPrefix(rest, "#") || strings.HasPrefix(rest, ";") {
				cut = len(value) // a line that starts so is a comment
			}
			items[i].Line = w.Add("%s=%s\\", items[i].Key, value[:cut])
			if rapid.Bool().Draw(t, "inner comment") {
				w.Add("# inside")
			}
			// An empty line would run into the line ends around it, as
			// \r\n and \n\r are one line end to systemd, so it holds a space.
			w.Add(" %s", value[cut:])
			items[i].Value = strings.Trim(value[:cut]+"  "+value[cut:], " ")
		}
		checkItems(t, root, "a.service", []byte(w.Text.String()), items, func(it extracttest.Item) Spec { return unit(it.Section, it.Key) })
	})
}

// Every assignment of an environment file sits at the line its key
// starts on, whatever the line ends, comments, quotes across lines and
// \ joins before it. Planted break: the key's line taken where the value
// ends.
func TestPropertyEnvironmentFileValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		items := extracttest.DrawItems(t, rapid.Just(""), rapid.StringMatching(`[A-Za-z_][A-Za-z0-9_]{0,6}`), rapid.StringMatching(`[a-z0-9]([a-z0-9 =#;\n]{0,10}[a-z0-9])?`))
		end := anyEnd(t)
		var b strings.Builder
		line := 1
		for i := range items {
			if rapid.Bool().Draw(t, "comment") {
				b.WriteString("# c \\" + end())
				line++
			}
			items[i].Line = line
			v := items[i].Value
			b.WriteString(items[i].Key + rapid.SampledFrom([]string{"=", " = ", "\t=  "}).Draw(t, "sep"))
			if strings.Contains(v, "\n") || rapid.Bool().Draw(t, "quoted") {
				b.WriteString("'" + v + "'")
				line += strings.Count(v, "\n")
			} else {
				b.WriteString(v)
			}
			b.WriteString(end())
			line++
		}
		data := []byte(b.String())
		if err := os.WriteFile(filepath.Join(root, "a.env"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		fileLines := strings.Split(string(core.LineFeeds(data)), "\n")
		for _, it := range items {
			var want, got []string
			for _, o := range items {
				if o.Key == it.Key {
					want = append(want, fmt.Sprintf("%q@%d", o.Value, o.Line))
				}
			}
			for _, v := range extracttest.MustExtract(t, BuildSystemd, sd("a.env", "", "", it.Key), root) {
				extracttest.CheckLine(t, v, fileLines, data)
				if !strings.Contains(fileLines[v.At.Line-1], it.Key) {
					t.Fatalf("%q: line %d is %q, which does not hold the key\n%q", it.Key, v.At.Line, fileLines[v.At.Line-1], data)
				}
				got = append(got, fmt.Sprintf("%q@%d", v.Text, v.At.Line))
			}
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Fatalf("%s: got %s, want %s\n%q", it.Key, got, want, data)
			}
		}
	})
}

// An environment file reads any bytes into assignments at lines inside
// it, holding no more bytes than the file, and the extractor returns
// exactly the values of the variable's.
func FuzzSystemdEnvironmentFile(f *testing.F) {
	f.Add("A=1\n# c \\\nB = 'x\ny' \"z\\\"\" w\\\n\rA=\\\r\n", "A")
	f.Add("\xef\xbb\xbfA=1\x00A=2", "A")
	f.Add("export A=1\nA.B=2\nA", "A")
	f.Fuzz(func(t *testing.T, data, variable string) {
		x, err := BuildSystemd(sd("a.env", "", "", variable))
		if err != nil {
			return
		}
		coretest.CheckEntryValues(t, x.(core.EntryExtractor), []byte(data), len(data))
	})
}

// Environment= splits any value into words that hold no more bytes than
// the value, since every escape is shorter than what it stands for, and
// each word a refusal follows was visited before it. Planted break: a
// refused word visited.
func FuzzEnvironWords(f *testing.F) {
	f.Add(`A=1 "B=2 3" 'C=\x41' D=é E=\q`)
	f.Add(`\U0001F600 \400 "open`)
	f.Fuzz(func(t *testing.T, v string) {
		total := 0
		err := environWords([]byte(v), nil, nil, func(w []byte) error {
			total += len(w)
			return nil
		})
		if err != nil && !errors.Is(err, errEnvironSyntax) {
			t.Fatalf("%q: %v", v, err)
		}
		if total > len(v) {
			t.Fatalf("%q: words of %d bytes", v, total)
		}
	})
}

// Podman's split reads any value into words that hold no more bytes than
// the value, every escape being shorter than what it stands for, and no
// word is empty, since an empty word ends the value.
func FuzzPodmanWords(f *testing.F) {
	f.Add(`A=1 "B=2 3" 'C=\x41' D=\u00e9 E="open`)
	f.Add("A=1 \"\" B=\\q\x00C")
	f.Fuzz(func(t *testing.T, v string) {
		total := 0
		if err := podmanWords([]byte(v), nil, func(w []byte) error {
			if len(w) == 0 {
				t.Fatalf("%q: an empty word", v)
			}
			total += len(w)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if total > len(v) {
			t.Fatalf("%q: words of %d bytes", v, total)
		}
	})
}
