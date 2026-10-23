package toolversion

import (
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

// Every file of the table reads any bytes without a crash, into versions at
// lines inside the file, and each version a line format returns is text of
// its line. A JSON or TOML version is the decoded string, which may differ
// from the text, so only its line is checked there.
func FuzzToolVersion(f *testing.F) {
	for i, k := range kinds {
		body := map[string]string{
			".tool-versions": "nodejs 20.11.1 lts # c\r\npython 3.12\n",
			".sdkmanrc":      "# c\njava = 21.0.2-tem\n",
			"go.mod":         "module m\n\ngo 1.22.0\ntoolchain go1.22.3\n",
			"rust-toolchain": "[toolchain]\nchannel = \"1.75\"\n",
			"package.json":   `{"engines": {"node": ">=20"}, "packageManager": "node@1+x"}`,
			"Cargo.toml":     "[package]\nrust-version = \"1.70\"\n",
		}[k.name]
		if body == "" {
			body = "\n# c\n  v1.2.3 x\n4\n"
		}
		f.Add(uint8(i), body, "node")
	}
	// go.mod seeds with every token modfile's lexer makes, for the count.
	goMod := kindIndex("go.mod")
	for _, body := range []string{
		"module m\nretract [v1.0.0, v1.1.0]\n",
		"module m\ngo 1.22\nx ,,,,\n",
		"module m\nx []{}\"a\"\"b\"`c`d\n",
		"module m\nrequire (\n\tex.com/a v1.0.0 // c\n)\ntoolchain go1.22.3\n",
	} {
		f.Add(goMod, body, "go")
	}
	f.Fuzz(func(t *testing.T, kind uint8, data, tool string) {
		// The count runs before modfile parses, so it holds on every
		// input modfile parses, whatever the reader makes of it.
		checkGoModNodes(t, []byte(data))
		k := kinds[int(kind)%len(kinds)]
		if k.tools != nil {
			tool = k.tools[0]
		}
		e, err := Build(Spec{Tool: tool, Files: []string{"d/" + k.name}}, parsers)
		if err != nil {
			return
		}
		x := e.(extractor)
		got, err := x.FromData(x.files[0], []byte(data))
		if err != nil {
			return
		}
		lines := strings.Split(data, "\n")
		tree := slices.Contains([]string{"rust-toolchain", "rust-toolchain.toml", "package.json", "pyproject.toml", "Cargo.toml"}, k.name)
		for _, v := range got {
			if v.At.File != "d/"+k.name || v.At.Line < 1 || v.At.Line > len(lines) {
				t.Fatalf("%s: %q at %s:%d, outside the file's %d lines", k.name, v.Text, v.At.File, v.At.Line, len(lines))
			}
			if !tree && !strings.Contains(lines[v.At.Line-1], v.Text) {
				t.Fatalf("%s: %q at line %d, which is %q", k.name, v.Text, v.At.Line, lines[v.At.Line-1])
			}
			if v.Text == "" {
				t.Fatalf("%s: an empty version at line %d", k.name, v.At.Line)
			}
		}
	})
}

// checkGoModNodes fails unless goModNodes is at least the lines and tokens
// modfile builds from data, for a file it parses.
func checkGoModNodes(t *testing.T, data []byte) {
	t.Helper()
	mod, err := modfile.ParseLax("go.mod", data, nil)
	if err != nil {
		return
	}
	built := 0
	for _, stmt := range mod.Syntax.Stmt {
		switch s := stmt.(type) {
		case *modfile.Line:
			built += 1 + len(s.Token)
		case *modfile.LineBlock:
			built += 2 + len(s.Token)
			for _, l := range s.Line {
				built += 1 + len(l.Token)
			}
		case *modfile.CommentBlock:
			built++
		}
	}
	if n := goModNodes(data); n < built {
		t.Fatalf("goModNodes counts %d, modfile built %d\n%q", n, built, data)
	}
}

// kindIndex is the index of the row for name in kinds.
func kindIndex(name string) uint8 {
	return uint8(slices.Index(kinds, kindOf(name)))
}
