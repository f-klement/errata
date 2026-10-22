package properties

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// javaEscape writes s as Properties.store writes a key (key set) or a
// value: \ = : # ! escaped, white space escaped in a key and at the start of
// a value, \t \n \r \f, and every other character outside printable ASCII
// as \uXXXX, in UTF-16.
func javaEscape(s string, key bool) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == ' ' && (key || i == 0):
			b.WriteString(`\ `)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\f':
			b.WriteString(`\f`)
		case strings.ContainsRune(`\=:#!`, r):
			b.WriteString(`\` + string(r))
		case r < 0x20 || r > 0x7e:
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%04x`, u)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestPropertyPropertiesValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		items := extracttest.DrawItems(t, rapid.Just(""), rapid.StringMatching(`[a-z][a-z.:= é]{0,6}`), rapid.StringMatching(`[a-z0-9 =:#!\\\té]{0,12}`))
		var w extracttest.LineWriter
		for i := range items {
			if rapid.Bool().Draw(t, "comment") {
				w.Add("%s comment \\", rapid.SampledFrom([]string{"#", "!", "  #"}).Draw(t, "mark"))
			}
			value := javaEscape(items[i].Value, false)
			// A line break, which a \ escapes, inside the value, at a
			// point that splits no escape.
			if cut := rapid.IntRange(0, len(value)).Draw(t, "cut"); cut > 0 && !strings.HasSuffix(value[:cut], `\`) && !strings.HasPrefix(value[cut:], " ") {
				value = value[:cut] + "\\\n    " + value[cut:]
			}
			items[i].Line = w.Add("%s%s%s", javaEscape(items[i].Key, true), rapid.SampledFrom([]string{"=", " = ", ":", " "}).Draw(t, "sep"), value)
			w.Line += strings.Count(value, "\n")
		}
		checkItemsEscaped(t, root, []byte(w.Text.String()), items)
	})
}

// checkItemsEscaped is checkItems for a properties file, whose lines hold
// each key escaped.
func checkItemsEscaped(t *rapid.T, root string, data []byte, items []extracttest.Item) {
	escaped := make([]extracttest.Item, len(items))
	copy(escaped, items)
	for i := range escaped {
		escaped[i].Key = javaEscape(items[i].Key, true)
	}
	if err := os.WriteFile(filepath.Join(root, "a.properties"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	fileLines := strings.Split(string(data), "\n")
	for i, it := range items {
		got := extracttest.MustExtract(t, Build, Spec{File: "a.properties", Key: it.Key}, root)
		var gotTexts, want []string
		for _, v := range got {
			extracttest.CheckLine(t, v, fileLines, data)
			gotTexts = append(gotTexts, fmt.Sprintf("%s@%d", v.Text, v.At.Line))
		}
		for j, other := range items {
			if other.Key == it.Key {
				if !strings.HasPrefix(fileLines[other.Line-1], escaped[j].Key) {
					t.Fatalf("line %d is %q, which does not start with the key %q", other.Line, fileLines[other.Line-1], escaped[j].Key)
				}
				want = append(want, fmt.Sprintf("%s@%d", other.Value, other.Line))
			}
		}
		if strings.Join(gotTexts, " ") != strings.Join(want, " ") {
			t.Fatalf("item %d %+v: got %q, want %q\n%s", i, it, gotTexts, want, data)
		}
	}
}
