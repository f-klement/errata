package literal

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A file denser than the node limit is refused.
func TestBounds(t *testing.T) {
	x, err := Build(Spec{OneFile: core.OneFile{File: "f", MaxBytes: 300}, Path: "X", Language: "python"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.(extractor).FromData([]byte(strings.Repeat(",", 200))); err == nil || !strings.Contains(err.Error(), "node") {
		t.Errorf("a file past the node limit: %v", err)
	}
}

// Build refuses a language with no literal syntax, a file whose name
// chooses none, and a path that is missing or does not parse.
func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{Spec{OneFile: core.OneFile{File: "a.go"}, Path: "x"}, "literal: a.go: its name chooses no language errata reads literal data of; set language: to one of kotlin, javascript, typescript, php, groovy, scala, swift, python, ruby, elixir, lua"},
		{Spec{OneFile: core.OneFile{File: "README"}, Path: "x"}, "literal: README: its name chooses no language"},
		{Spec{OneFile: core.OneFile{File: "a.py"}, Path: "x", Language: "go"}, `literal: language "go" is not one errata reads literal data of`},
		{Spec{OneFile: core.OneFile{File: "a.py"}, Path: "x", Language: "cobol"}, `literal: language "cobol" is not one`},
		{Spec{OneFile: core.OneFile{File: "a.py"}}, "literal: path is required"},
		{Spec{OneFile: core.OneFile{File: "a.py"}, Path: "a..b"}, `literal: path "a..b": column 3`},
		{Spec{OneFile: core.OneFile{File: "/a.py"}, Path: "a"}, "literal: "},
	} {
		_, err := Build(c.s)
		extracttest.WantError(t, err, c.want)
	}
}

// Place and Resolve write out the language the file's name chooses, and
// leave it out for a name that chooses a row the reader does not read.
func TestPlaceAndResolveWriteTheLanguage(t *testing.T) {
	if got := Place(Spec{OneFile: core.OneFile{File: "./config/settings.py", MaxBytes: 5}, Path: "a"}); got.Language != "python" || got.File != "config/settings.py" || got.MaxBytes != 0 {
		t.Errorf("Place: %+v", got)
	}
	if got := Resolve(Spec{OneFile: core.OneFile{File: "a.rb"}, Path: "a"}); got.Language != "ruby" || got.MaxBytes != core.DefaultMaxBytes {
		t.Errorf("Resolve: %+v", got)
	}
	if got := Resolve(Spec{OneFile: core.OneFile{File: "a.go"}, Path: "a"}); got.Language != "" {
		t.Errorf("Resolve of a.go: %+v", got)
	}
	if got := Resolve(Spec{OneFile: core.OneFile{File: "a.cfg"}, Path: "a", Language: "lua"}); got.Language != "lua" {
		t.Errorf("Resolve with a language: %+v", got)
	}
}

// The extractor reads its file from the repository.
func TestExtract(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"config/settings.py": "DEBUG = False\nPORT = 8080\n"})
	got := extracttest.Texts(extracttest.MustRun(t, Build, Spec{OneFile: core.OneFile{File: "config/settings.py"}, Path: "PORT"}, root))
	if got != "8080@2" {
		t.Errorf("got %s", got)
	}
}

// Java writes every value inside a class, which literal does not read, so it
// refuses a Java file as it refuses Go. Planted break: reads ignores
// InClasses.
func TestJavaIsTheCodeReaders(t *testing.T) {
	_, err := Build(Spec{OneFile: core.OneFile{File: "A.java"}, Path: "x"})
	extracttest.WantError(t, err, "literal: A.java: its name chooses no language errata reads literal data of")
	_, err = Build(Spec{OneFile: core.OneFile{File: "a.txt"}, Path: "x", Language: "java"})
	extracttest.WantError(t, err, `literal: language "java" is not one errata reads literal data of`)
	if got := Resolve(Spec{OneFile: core.OneFile{File: "A.java"}, Path: "x"}); got.Language != "" {
		t.Errorf("Resolve of A.java: %+v", got)
	}
}

// The extractor's own path, Build, the node limit and FromData, reads any
// file of its pieces into values on the file's lines or refuses it, for
// each row it reads; core/litparse's property test reads the parse alone.
// Planted break: FromData handing Parse another file name.
func TestFromDataValuesLieInsideTheFile(t *testing.T) {
	var rows []string
	for _, l := range core.Languages() {
		if reads(l) {
			rows = append(rows, l.Name)
		}
	}
	pieces := []string{"\n", "\r\n", "\r", " ", "X", "x", " = ", ",", ":", ";", ".", "1", "'a'", "\"b\"", "[", "]", "{", "}", "(", ")", "#", "//", "--", "$", "<?php "}
	rapid.Check(t, func(t *rapid.T) {
		lang := rapid.SampledFrom(rows).Draw(t, "language")
		src := strings.Join(rapid.SliceOfN(rapid.SampledFrom(pieces), 0, 40).Draw(t, "pieces"), "")
		x, err := Build(Spec{OneFile: core.OneFile{File: "f", MaxBytes: 60}, Path: "$[*]", Language: lang})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		vs, err := x.(extractor).FromData([]byte(src))
		if err != nil {
			if !strings.HasPrefix(err.Error(), "f: ") {
				t.Fatalf("%s %q: a refusal that names no file: %v", lang, src, err)
			}
			return
		}
		for _, v := range vs {
			if v.At.Line < 1 || v.At.Line > core.LastLine([]byte(src)) || v.At.File != "f" {
				t.Fatalf("%s %q: value %q at %+v, outside the file", lang, src, v.Text, v.At)
			}
		}
	})
}
