package properties

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
)

// The properties reader reads any file into keys and values at lines
// inside it, and every key and value it reads, written again as
// Properties.store writes them, reads back as itself.
func FuzzProperties(f *testing.F) {
	f.Add("fruits  apple, \\\n   banana\n# c \\\n\\:\\= = x\n", "fruits")
	f.Add("a=\\u0041\\uD83D\\uDE00\\uD83Dx\r\n\\\n#c\r\\\n  b c\\\r\n d", "a")
	f.Add("\f k \t:= \\t\\n\\r\\f\\z\\\\\n!c\n\\u12", "k")
	f.Fuzz(func(t *testing.T, data, key string) {
		e, err := Build(Spec{File: "f", Key: key})
		if err != nil {
			return
		}
		coretest.CheckEntryValues(t, e.(core.EntryExtractor), []byte(data), javaLen([]byte(data)))
		var written strings.Builder
		var keys, values []string
		_ = readProperties([]byte(data), nil, func(en *core.Entry) error {
			if utf8.Valid(en.Key) && utf8.Valid(en.Value) && !strings.ContainsRune(string(en.Key)+string(en.Value), utf8.RuneError) {
				keys, values = append(keys, string(en.Key)), append(values, string(en.Value))
				written.WriteString(javaEscape(string(en.Key), true) + "=" + javaEscape(string(en.Value), false) + "\n")
			}
			return nil
		})
		i := 0
		err = readProperties([]byte(written.String()), nil, func(en *core.Entry) error {
			if string(en.Key) != keys[i] || string(en.Value) != values[i] || en.Line != i+1 {
				t.Fatalf("%q=%q at line %d read back as %q=%q at line %d", keys[i], values[i], i+1, en.Key, en.Value, en.Line)
			}
			i++
			return nil
		})
		if err != nil || i != len(keys) {
			t.Fatalf("%d of %d read back: %v\n%s", i, len(keys), err, written.String())
		}
	})
}
