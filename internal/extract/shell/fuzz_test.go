package shell

import (
	"bytes"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// The reader reads any script, in any dialect, into values at lines inside
// it, or refuses it; it never panics. A value marked not literal is text
// the script holds, as written. Two reads give the same values.
func FuzzShell(f *testing.F) {
	f.Add("#!/bin/sh\nexport X=1 Y=$(a)\n: \"${X:=b}\"\n", "X", uint8(0))
	f.Add("X=(a b) X+=c\ndeclare -i X\ncat <<E\n${X:-d}\nE\n", "X", uint8(1))
	f.Add("X=$'\\x41\\q' Y=\"${X:-'q'}\" Z=~/a\nlocal X=a:~/b", "X", uint8(2))
	f.Add("typeset -n X=Y; X=${X-$((1+2))}`a`", "X", uint8(3))
	// The parser yields the statement (0)& with the error after it, then
	// the error again: a loop that stopped at the first panicked.
	f.Add("(0)&0\xbc", "A", uint8(1))
	// With the limits lowered, short inputs reach the guard's refusal, the
	// walk's and the statement limit's, which must each be an error; a
	// seed sits past each.
	frames, every, tree := maxFrames, checkEvery, maxTreeDepth
	maxFrames, checkEvery, maxTreeDepth = 2000, 1, 200
	f.Cleanup(func() { maxFrames, checkEvery, maxTreeDepth = frames, every, tree })
	f.Add("x=$(("+strings.Repeat("(", 120)+"1"+strings.Repeat(")", 120)+"))\n", "x", uint8(1))
	// The parser reads 1 KiB at a time, so only nesting that spans a read
	// reaches the stack guard's refusal: a comment fills the first read.
	f.Add("#"+strings.Repeat("a", 1000)+"\nx=$(("+strings.Repeat("(", 1000)+"1"+strings.Repeat(")", 1000)+"))\n", "x", uint8(1))
	f.Add("a"+strings.Repeat(" | a", 120)+"\nX=1\n", "X", uint8(0))
	f.Add("X=1\necho"+strings.Repeat(" a", 2400)+"\n", "X", uint8(2))
	f.Fuzz(func(t *testing.T, data, name string, lang uint8) {
		language := []string{"", "bash", "posix", "mksh"}[lang%4]
		e, err := Build(Spec{File: "f", Name: name, Language: language, MaxBytes: 16 << 10})
		if err != nil {
			return
		}
		x := e.(core.EntryExtractor)
		got, err := x.FromData([]byte(data))
		if err != nil {
			if got != nil {
				t.Fatalf("values %v with error %v", got, err)
			}
			return
		}
		lines := strings.Count(data, "\n") + 1
		for _, v := range got {
			if v.At.Line < 1 || v.At.Line > lines {
				t.Fatalf("%q at line %d of %d", v.Text, v.At.Line, lines)
			}
			if v.NotLiteral && !strings.Contains(data, v.Text) {
				t.Fatalf("%q is marked not literal and is not the script's text", v.Text)
			}
		}
		again, err := x.FromData(bytes.Clone([]byte(data)))
		if err != nil || len(again) != len(got) {
			t.Fatalf("a second read gave %d values and %v, the first %d", len(again), err, len(got))
		}
		for i := range got {
			if again[i] != got[i] {
				t.Fatalf("value %d read as %+v, then %+v", i, got[i], again[i])
			}
		}
	})
}
