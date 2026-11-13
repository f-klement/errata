package regex

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// lib is a TypeScript module that names Blob in code, in a line comment, in
// a block comment over two lines, and in strings that hold a comment's
// opener, so the code after each string is still code.
const lib = "const a = new Blob(); // a Blob in prose\n" +
	"/* a Blob\n   and é Blob */ const b = Blob;\n" +
	"const u = \"https://x/\"; const c = Blob;\n" +
	"const m = 'image/*'; const d = Blob; // */ Blob\n"

// skip: [comments] reads past every comment the file's language writes and
// nothing else: a string's // or /* opens no comment, and each value keeps
// its line. Planted breaks: no blanking (the comments' Blobs come back);
// strings not skipped (the code after them is lost).
func TestSkipComments(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"lib.ts": lib})
	s := Spec{File: "lib.ts", Pattern: `\b(Blob)\b`}
	if got := extracttest.Texts(extracttest.MustRun(t, Build, s, root)); got != "Blob@1 Blob@1 Blob@2 Blob@3 Blob@3 Blob@4 Blob@5 Blob@5" {
		t.Fatalf("without skip got %q", got)
	}
	s.Skip = []string{Comments}
	if got := extracttest.Texts(extracttest.MustRun(t, Build, s, root)); got != "Blob@1 Blob@3 Blob@4 Blob@5" {
		t.Errorf("with skip got %q", got)
	}
}

// A blanked comment keeps the columns of the code after it on its line,
// one space for each character, é included, so a pattern that counts
// characters from the start of the line finds the code where the file has
// it. Planted break: a space for each byte of the comment.
func TestSkipKeepsColumns(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a.go": "/* é */ x\n"})
	s := Spec{File: "a.go", Pattern: `^.{8}(x)`, Skip: []string{Comments}}
	if got := extracttest.Texts(extracttest.MustRun(t, Build, s, root)); got != "x@1" {
		t.Errorf("got %q, want x at column 9", got)
	}
}

// A file of one whose name chooses no language is refused when the rule
// loads, and a file of a set when the set reads it, naming the file; a
// file whose lexer loses its place makes the rule unknown. Planted breaks:
// a file with no language read with its comments in; the span's refusal
// dropped.
func TestSkipRefusals(t *testing.T) {
	_, err := Build(Spec{File: "README.md", Pattern: `(x)`, Skip: []string{Comments}})
	extracttest.WantError(t, err, "regex: README.md: skip: [comments] finds comments by the language the file's name chooses, and its name chooses none errata lexes")
	root := extracttest.Tracked(t, map[string]string{"a.ts": "x\n", "b.md": "x\n", "c.go": "x /* x\n"})
	e, err := Build(Spec{Files: []string{"*.ts", "*.md"}, Pattern: `(x)`, Skip: []string{Comments}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Extract(t.Context(), root)
	extracttest.WantError(t, err, "regex: b.md: skip: [comments] finds comments by the language")
	e, err = Build(Spec{File: "c.go", Pattern: `(x)`, Skip: []string{Comments}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Extract(t.Context(), root)
	extracttest.WantError(t, err, "regex: c.go: line 1: a comment opens here and runs to the end of the file")
}

// skip takes comments, once, and nothing else; skip: [] is refused rather
// than read as no skip. Planted break: each refusal of checkSkip dropped.
func TestSkipValues(t *testing.T) {
	for skip, want := range map[string]string{
		"":                  "regex: skip lists nothing",
		"strings":           `regex: skip "strings" is not one errata reads past`,
		"comments,strings":  `regex: skip "strings" is not one errata reads past`,
		"comments,comments": "regex: skip lists comments twice",
	} {
		list := []string{}
		if skip != "" {
			list = strings.Split(skip, ",")
		}
		e, err := Build(Spec{File: "a.go", Pattern: `(x)`, Skip: list})
		extracttest.WantError(t, err, want)
		if e != nil {
			t.Errorf("skip %q: a refusal returns the extractor %#v", skip, e)
		}
	}
}

// skip composes with a reference, a context and as: record: each reads the
// blanked file. Planted breaks: the reference's searches, or the record
// search, not wrapped.
func TestSkipComposes(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a.py": "# keep: a\nkeep: b  # keep: c\n# ok\nkeep: d\n"})
	d := mustReference(t, Spec{File: "a.py", Pattern: `keep: ((?{v}))`, Skip: []string{Comments}})
	vs, err := d.ExtractWith(t.Context(), root, map[string][]check.Value{"v": {value("a", "x", 1), value("b", "x", 1), value("c", "x", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := extracttest.Texts(vs); got != "b@2" {
		t.Errorf("reference got %q", got)
	}
	s := Spec{File: "a.py", Pattern: `keep: (?P<v>\w)`, As: "record", Skip: []string{Comments},
		Context: &core.Context{Before: 1, Without: `ok`}}
	vs = extracttest.MustRun(t, Build, s, root)
	if len(vs) != 2 || vs[0].At.Line != 2 || vs[1].At.Line != 4 || vs[1].Data.Field("v").Text != "d" {
		t.Errorf("records got %q, want b at line 2 and d at line 4, since the context reads past # ok", extracttest.Texts(vs))
	}
	_, err = Build(Spec{File: "a.md", Pattern: `((?{v}))`, Skip: []string{Comments}})
	extracttest.WantError(t, err, "regex: a.md: skip: [comments]")
}

// errata list copies skip, and two specs that differ by skip read two
// places. Planted break: Resolve sharing the slice.
func TestSkipResolveAndPlace(t *testing.T) {
	s := Spec{File: "f.go", Pattern: `(x)`, Skip: []string{Comments}}
	r := Resolve(s)
	r.Skip[0] = "changed"
	if s.Skip[0] != Comments {
		t.Errorf("Resolve shares skip with the spec")
	}
	plain := s
	plain.Skip = nil
	if p := Place(s); len(p.Skip) != 1 || len(Place(plain).Skip) != 0 {
		t.Errorf("Place drops skip: %+v", p)
	}
}

// skip blanks what the lexer reads as a comment, and where the lexer
// misreads a construct it blanks code too (docs/reference.md, Code without
// its comments). These cases record today's reading, misreads included, so
// a lexer that learns JSX text, shell heredocs or the code inside an
// interpolation changes this test on purpose. A blanked comment reads as
// spaces, which \s and .* match, and a shebang is a comment. Planted
// break: no blanking, which changes every row.
func TestSkipPinsWhatTheLexerReads(t *testing.T) {
	for _, c := range []struct {
		name, file, data, pattern, without, with string
	}{
		{"a URL in TSX text blanks the code after it on its line", "a.tsx",
			"<p>see http://x</p>; const b = new Blob();\nnew Blob();\n", `\b(Blob)\b`,
			"Blob@1 Blob@2", "Blob@2"},
		{"a shell heredoc's # line is blanked", "a.sh",
			"cat <<EOF\n# kubectl apply -f x\nEOF\nkubectl apply -f y\n", `(kubectl apply)`,
			"kubectl apply@2 kubectl apply@4", "kubectl apply@4"},
		{"an apostrophe in a shell heredoc swaps strings and comments", "b.sh",
			"cat <<EOF\nIt's done\nEOF\n# kubectl apply old\necho 'kubectl apply x'\nkubectl apply y # it's\n", `(kubectl apply)`,
			"kubectl apply@4 kubectl apply@5 kubectl apply@6", "kubectl apply@4 kubectl apply@5 kubectl apply@6"},
		{"a comment inside an interpolation stays", "a.ts",
			"const s = `a ${ /* Blob */ Blob } b`;\n", `\b(Blob)\b`,
			"Blob@1 Blob@1", "Blob@1 Blob@1"},
		{"a pattern matches across a blanked comment", "a.go",
			"a /* x */ b\n", `(a\s+b)`, "", "a         b@1"},
		{"a value of .* ends in the blanks of a comment", "b.go",
			"x = 1 // one\n", `x = (.*)`, "1 // one@1", "1       @1"},
		{"a shebang is a comment", "c.sh",
			"#!/bin/bash\necho hi\n", `^#!(\S+)`, "/bin/bash@1", ""},
	} {
		root := extracttest.Repo(t, map[string]string{c.file: c.data})
		for _, skip := range [][]string{nil, {Comments}} {
			want := c.without
			if skip != nil {
				want = c.with
			}
			e, err := Build(Spec{File: c.file, Pattern: c.pattern, Skip: skip})
			if err != nil {
				t.Fatal(err)
			}
			vs, err := e.Extract(t.Context(), root)
			if err != nil {
				t.Fatalf("%s, skip %v: %v", c.name, skip, err)
			}
			if got := extracttest.Texts(vs); got != want {
				t.Errorf("%s, skip %v: got %q, want %q", c.name, skip, got, want)
			}
		}
	}
}
