package envfile

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
)

// quoted writes v as a " value that reads back as v: \, " and $ escaped.
func quoted(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(v) + `"`
}

// The envfile reader reads any file into keys and values at lines inside
// it, refuses what it refuses before the values fill memory, and every
// literal key and value it reads, written again in double quotes, reads
// back as itself at the line it was written on.
func FuzzEnvfile(f *testing.F) {
	f.Add("export A=1\nB=\"${A} \\t\\0101\"\n# c\nC='x\ny' D=${Z:-d}\n", "B")
	f.Add("A=\nB=${A:-{}}${A+y}$$\n\u212a: \"\\$A\"\nE\nlast", "")
	f.Add("\ufeffA=${B:?m}\r\nx.y-z[0]=\"a\n}${A:-x}\n}\"", "x.y-z[0]")
	f.Fuzz(func(t *testing.T, data, name string) {
		x, err := Build(Spec{File: "f", Name: name})
		if err != nil {
			return
		}
		e := x.(core.EntryExtractor)
		// The reference read in CheckEntryValues has no meter, so a file the
		// metered read refuses for work, such as a chain of keys that each
		// double the one before, is left out: unmetered it could fill memory.
		if _, err := e.FromData([]byte(data)); err != nil && strings.Contains(err.Error(), "units of work") {
			return
		}
		// What a substitution writes is charged, so the values hold at most
		// the read's budget: 1 MiB and core.WorkPerByte for each byte.
		coretest.CheckEntryValues(t, e, []byte(data), 1<<20+core.WorkPerByte*len(data))
		var written strings.Builder
		var keys, values []string
		var lines []int
		line := 1
		_ = readEnv([]byte(data), func(en *core.Entry) error {
			if !en.Computed && len(en.Key) > 0 && isKey(string(en.Key)) {
				keys, values, lines = append(keys, string(en.Key)), append(values, string(en.Value)), append(lines, line)
				written.WriteString(string(en.Key) + "=" + quoted(string(en.Value)) + "\n")
				line += 1 + strings.Count(string(en.Value), "\n")
			}
			return nil
		})
		i := 0
		err = readEnv([]byte(written.String()), func(en *core.Entry) error {
			if i == len(keys) || string(en.Key) != keys[i] || string(en.Value) != values[i] || en.Line != lines[i] || en.Computed {
				t.Fatalf("entry %d read back as %q=%q at line %d\n%s", i, en.Key, en.Value, en.Line, written.String())
			}
			i++
			return nil
		})
		if err != nil || i != len(keys) {
			t.Fatalf("%d of %d read back: %v\n%s", i, len(keys), err, written.String())
		}
	})
}
