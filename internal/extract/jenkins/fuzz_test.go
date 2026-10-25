package jenkins

import (
	"bytes"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// The lexer reads any file into tokens in file order, each inside the file
// and on the line it starts on, or refuses it; the reader reads any file
// into values at lines inside it, or refuses it, and never panics. A value
// marked not literal is text the file holds, as written. Two reads give
// the same values.
func FuzzJenkins(f *testing.F) {
	f.Add("pipeline {\n  environment { X = 'a' }\n  stages { stage('s') { environment { X = \"${b}\" } } }\n}\n", "X", "s")
	f.Add("def X = '''a\n''' // c\nenv.X = /d/ + $/e$/$\n", "X", "")
	f.Add("@Library('l') _\nwithEnv(['X=1']) { env.X = 2 }\n", "X", "")
	f.Add("a = \"${ \"${ '}' }\" }\" /* */ env.X = 'q\\'q'\n", "X", "")
	f.Add("pipeline { parameters { string(name: 'X') } }\n", "X", "")
	f.Add("stage('a') { stage('a') { } }\n", "X", "a")
	f.Add("env.X = 'a' +\n\n  .b ?\n c : d\n", "X", "")
	f.Add(strings.Repeat("a {", 1001), "X", "")
	f.Add("a = "+strings.Repeat(`"${`, 501), "X", "")
	// A block or a bracket left open at the end, which once panicked.
	f.Add("env.X={", "X", "")
	f.Add("pipeline { environment { X = {", "X", "")
	f.Add("pipeline {\n environment {\n  X = foo(\n  Y = 'a'\n }\n}\n", "X", "")
	// A line end inside brackets before a /, a dollar slashy's \/$, case,
	// a withEnv of a variable, env['X'] and a matrix's axis.
	f.Add("pipeline { environment { H = (T\n / 2)\n X = 'b' // c\n } }\n", "X", "")
	f.Add("a = $/x\\/$ ; env.X = 'r' ; b = $/y/$\n", "X", "")
	f.Add("pipeline { environment { x = 'a' }\n stages { stage('s') { steps { withEnv(v) { } } } } }\n", "X", "s")
	f.Add("node { env['X'] = 'b'; env.setProperty('X', 1); env.X += 'c' }\n", "X", "")
	// A ) that closes nothing, ++ and the env spellings errata does not read.
	f.Add("pipeline { environment {\n Y = 1 )\n H = (T\n / 2)\n X = 'b'\n Z = 3 /)\n } }\n", "X", "")
	f.Add("def X = 1\nX++\nenv?.X = 'b'; ++this.env.X; env.putAt('X', 1)\n", "X", "")
	f.Add("pipeline { stages { stage('m') { matrix { axes { axis { name 'X'; values 'a' } }\n stages { stage('b') { } } } } } }\n", "X", "b")
	f.Fuzz(func(t *testing.T, data, name, stage string) {
		checkTokens(t, []byte(data))
		e, err := Build(Spec{File: "Jenkinsfile", Variable: name, Stage: stage, MaxBytes: 16 << 10})
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
				t.Fatalf("%q is marked not literal and is not the file's text", v.Text)
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

// checkTokens fails unless the lexer's tokens of data come in file order,
// each inside the file and at the line its start is on, a string starting
// and ending with its delimiters, until the end or an error.
func checkTokens(t *testing.T, data []byte) {
	t.Helper()
	l := newLexer(data, nil)
	end := 0
	for {
		tok, err := l.next()
		if err != nil {
			return
		}
		if tok.start < end || tok.end < tok.start || tok.end > len(data) {
			t.Fatalf("token %+v after offset %d, in %d bytes", tok, end, len(data))
		}
		if want := 1 + bytes.Count(data[:tok.start], []byte("\n")); tok.line != want {
			t.Fatalf("token %+v at line %d, want %d", tok, tok.line, want)
		}
		if tok.kind == tokEOF {
			if tok.start != len(data) {
				t.Fatalf("end of file at %d of %d", tok.start, len(data))
			}
			return
		}
		if tok.kind == tokString {
			text := tok.text(data)
			if !bytes.HasPrefix(text, []byte(opening[tok.quote])) || !bytes.HasSuffix(text, []byte(closing[tok.quote])) ||
				len(text) < len(opening[tok.quote])+len(closing[tok.quote]) {
				t.Fatalf("string %q of form %d", text, tok.quote)
			}
		}
		if tok.end == tok.start {
			t.Fatalf("empty token %+v", tok)
		}
		end = tok.end
	}
}
