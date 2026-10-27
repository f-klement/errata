package packageversion

import (
	"strings"
	"testing"
)

// Every file of the table reads any bytes without a crash, into non-empty
// versions at lines inside the file. A VERSION file's version, and a not
// literal value's word, is text of its line only where the file wrote it
// there, so only VERSION's is checked against its line.
func FuzzPackageVersion(f *testing.F) {
	seeds := map[string][]string{
		"package.json":   {`{"version": "1.0.0"}`, "{\n\"version\": 1\n}"},
		"pyproject.toml": {"[project]\nversion = \"1\"\n", "[project]\ndynamic = [\"version\"]\n[tool.poetry]\nversion = \"2\"\n[build-system]\nbuild-backend = \"poetry.core.masonry.api\"\n"},
		"Cargo.toml":     {"[package]\nversion.workspace = true\n", "[workspace.package]\nversion = \"3\"\n[package]\nversion = { workspace = true }\n"},
		"VERSION":        {"\xef\xbb\xbf# c\r\n  v1.2.3 x\n4\n"},
	}
	for i, k := range kinds {
		for _, body := range seeds[k.name] {
			f.Add(uint8(i), body)
		}
	}
	f.Fuzz(func(t *testing.T, kind uint8, data string) {
		k := kinds[int(kind)%len(kinds)]
		e, err := Build(one("d/"+k.name), parsers)
		if err != nil {
			t.Fatal(err)
		}
		x := e.(extractor)
		got, err := x.FromData(x.files[0], []byte(data))
		if err != nil {
			return
		}
		lines := strings.Split(data, "\n")
		for _, v := range got {
			if v.At.File != "d/"+k.name || v.At.Line < 1 || v.At.Line > len(lines) {
				t.Fatalf("%s: %q at %s:%d, outside the file's %d lines", k.name, v.Text, v.At.File, v.At.Line, len(lines))
			}
			if v.Text == "" {
				t.Fatalf("%s: an empty version at line %d", k.name, v.At.Line)
			}
			if k.name == "VERSION" && !strings.Contains(lines[v.At.Line-1], v.Text) {
				t.Fatalf("%s: %q at line %d, which is %q", k.name, v.Text, v.At.Line, lines[v.At.Line-1])
			}
		}
	})
}
