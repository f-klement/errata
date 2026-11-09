package envfile

import (
	"fmt"
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
	f.Add("A=x\ry\nB=1\n", "B") // a lone CR in a value, written back, ends a line
	f.Add("0\r0=\nB=\n", "B")   // and one in a key (FuzzEnvfile, round 4)
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
				entry := string(en.Key) + "=" + quoted(string(en.Value))
				written.WriteString(entry + "\n")
				line += core.LastLine([]byte(entry)) // the entry's line ends, a lone CR's in the key or the value too, and the LF after it
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

// The two etc dialects read any file into keys and values at lines inside
// it. In etc/environment every literal value, written again in double
// quotes, reads back as itself at the line it was written on. A file in
// etc/default reads every statement compose does, at its line, and only
// marks values: a literal one is compose's literal value.
func FuzzEnvfileEtc(f *testing.F) {
	f.Add("export A=1\nB=\"x y\"#c\n# c \\\nC='x\nD=1 \\\n\tE=2\nF\n=x\n", "")
	f.Add("\ufeffA=1\r\nB=\"a\"b\"\r\n1.x=2\nA='x' B=y\rC=z\x00\n", "A")
	f.Fuzz(func(t *testing.T, data, name string) {
		for _, file := range []string{"etc/environment", "etc/default/f"} {
			x, err := Build(Spec{File: file, Name: name})
			if err != nil {
				return
			}
			e := x.(core.EntryExtractor)
			if _, err := e.FromData([]byte(data)); err != nil && strings.Contains(err.Error(), "units of work") {
				continue
			}
			coretest.CheckEntryValues(t, e, []byte(data), 1<<20+core.WorkPerByte*len(data))
		}
		checkPamWrittenBack(t, data)
		checkSourcedMarksOnly(t, data)
	})
}

// checkPamWrittenBack writes every literal entry pam_env reads in data as
// KEY="value" and reads it back.
func checkPamWrittenBack(t *testing.T, data string) {
	t.Helper()
	var written strings.Builder
	var want []string
	line := 1
	_ = readFile([]byte(data), nil, "etc/environment", func(en *core.Entry) error {
		if !en.Computed {
			entry := string(en.Key) + `="` + string(en.Value) + `"`
			want = append(want, fmt.Sprintf("%s=%q@%d", en.Key, en.Value, line))
			written.WriteString(entry + "\n")
			line += core.LastLine([]byte(entry))
		}
		return nil
	})
	var got []string
	err := readFile([]byte(written.String()), nil, "etc/environment", func(en *core.Entry) error {
		got = append(got, fmt.Sprintf("%s=%q@%d", en.Key, en.Value, en.Line))
		if en.Computed {
			t.Fatalf("%s=%q read back computed", en.Key, en.Value)
		}
		return nil
	})
	if err != nil || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("read back %q, want %q: %v\n%s", got, want, err, written.String())
	}
}

// checkSourcedMarksOnly holds a file in etc/default to compose's reading
// of data: the same refusal, or the same statements, keys and lines, and a
// literal value only where compose's is literal and the same.
func checkSourcedMarksOnly(t *testing.T, data string) {
	t.Helper()
	type read struct {
		key, value string
		line       int
		computed   bool
	}
	all := func(file string) ([]read, error) {
		var rs []read
		err := readFile([]byte(data), nil, file, func(en *core.Entry) error {
			rs = append(rs, read{string(en.Key), string(en.Value), en.Line, en.Computed})
			return nil
		})
		return rs, err
	}
	want, werr := all(".env")
	got, gerr := all("etc/default/f")
	// A file compose refuses is refused alike; etc/default's first read
	// finds it before visiting any entry.
	if (werr == nil) != (gerr == nil) || werr != nil && werr.Error() != gerr.Error() {
		t.Fatalf("etc/default refuses with %v, compose with %v", gerr, werr)
	}
	if werr == nil && len(got) != len(want) {
		t.Fatalf("etc/default read %d entries, compose %d", len(got), len(want))
	}
	for i, g := range got {
		w := want[i]
		if g.key != w.key || g.line != w.line || !g.computed && (w.computed || g.value != w.value) {
			t.Fatalf("entry %d: etc/default %+v, compose %+v", i, g, w)
		}
	}
}
