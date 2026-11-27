package dockerfile

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/command"
	"github.com/moby/buildkit/frontend/dockerfile/parser"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The reader reads any file into values at lines inside it, or refuses
// it, and never panics; two reads give the same values. Each word of an
// instruction the reader reads that holds no quote, escape or variable
// sits on the line the reader gives it, as errata counts lines: its text
// is on that line of the file (extracttest.RawLine).
func FuzzDockerfile(f *testing.F) {
	f.Add("ARG V=1\nFROM a:$V AS b\nENV A=1 \\\n  B=2\nLABEL c=\"d\"\nEXPOSE 80\n", "ENV", "", "b")
	f.Add("# escape=`\nFROM a\nENV P=C:\\x `\n# c\n\n Q=1\n", "ENV", "Q", "")
	f.Add("\uFEFF FROM a\r\nLABEL a=0\rv=1 \\\r\n w=2\n", "LABEL", "", "0")
	f.Add("FROM a\nRUN <<EOF\nENV X=1\nEOF\nENV A \\\n x \\\n y\n", "ENV", "A", "")
	f.Add("FROM --platform=\"a b\" \\\n --x=`c \\\n img AS x\nFROM x\nARG A B=$A\n", "ARG", "", "x")
	f.Add("ARG A=${B:-c}\nFROM a\nENV --unset A\nEXPOSE ${A} 1 \\\\\n", "EXPOSE", "", "")
	f.Fuzz(func(t *testing.T, data, instruction, key, stage string) {
		checkWordLines(t, []byte(data))
		// A parse limit of 16 KiB, max_bytes divided by parseShare.
		e, err := Build(Spec{OneFile: core.OneFile{File: "Dockerfile", MaxBytes: parseShare * 16 << 10}, Instruction: instruction, Key: key, Stage: stage})
		if err != nil {
			return
		}
		x := e.(core.EntryExtractor)
		if _, err := x.FromData([]byte(data)); err != nil && strings.Contains(err.Error(), "units of work") {
			return
		}
		// An expansion writes what it charges, so the values hold at most
		// the read's budget.
		coretest.CheckEntryValues(t, x, []byte(data), 1<<20+core.WorkPerByte*len(data))
		got, err := x.FromData([]byte(data))
		again, err2 := x.FromData(bytes.Clone([]byte(data)))
		if (err == nil) != (err2 == nil) || len(again) != len(got) {
			t.Fatalf("a second read gave %d values and %v, the first %d and %v", len(again), err2, len(got), err)
		}
		for i := range got {
			if again[i] != got[i] {
				t.Fatalf("value %d read as %+v, then %+v", i, got[i], again[i])
			}
		}
	})
}

// plain is a word the parser and the lexer leave as written.
var plain = regexp.MustCompile(`^[A-Za-z0-9_.:/@+-]+$`)

// checkWordLines fails unless each plain word of each FROM, EXPOSE, ARG,
// ENV and LABEL of data is on the line the reader finds it at.
func checkWordLines(t *testing.T, data []byte) {
	t.Helper()
	res, err := parser.Parse(bytes.NewReader(data))
	if err != nil {
		return
	}
	r := &reading{lines: newLines(data), cur: core.NewLineCursor(data), escape: byte(res.EscapeToken)}
	for _, n := range res.AST.Children {
		at := r.positions(n)
		words := wordsOf(n)
		step := 1
		switch n.Value {
		case command.Env, command.Label:
			step = 3
		case command.From, command.Expose, command.Arg:
		default:
			continue
		}
		for i := 0; i < len(words); i += step {
			checkWord(t, data, r, &at, words[i])
			if step == 3 && i+1 < len(words) {
				at.skip("=")
				checkWord(t, data, r, &at, words[i+1])
			}
		}
	}
}

// checkWord finds word with at, and fails unless a plain word is on the
// line the reader gives it.
func checkWord(t *testing.T, data []byte, r *reading, at *positions, word string) {
	t.Helper()
	line, err := r.lineOf(at, word)
	if err != nil {
		t.Fatal(err)
	}
	if !plain.MatchString(word) {
		return
	}
	if raw := extracttest.RawLine(data, line); !strings.Contains(raw, word) {
		t.Fatalf("%q at line %d, which is %q\n%q", word, line, raw, data)
	}
}
