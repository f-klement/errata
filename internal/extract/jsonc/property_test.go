package jsonc

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// plainKey is a key JSON5 may write unquoted, as GenDoc draws some.
var plainKey = regexp.MustCompile(`"([a-z]+)":`)

// Every leaf of a JSON document comes back once, with its text, at its
// line, after the document is written as each dialect lets a file write
// it: comments at the ends of lines and on lines of their own, a comma
// after the last member or item, and in JSON5 keys unquoted and strings in
// single quotes.
func TestPropertyValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		doc := extracttest.GenDoc(t, 3, "")
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		json5 := rapid.Bool().Draw(t, "json5")
		lines := strings.Split(string(data), "\n")
		var out []string
		last := -1 // the index in out of the line before, not counting comment lines
		for _, l := range lines {
			body := strings.TrimLeft(l, " ")
			closes := last >= 0 && body != "" && (body[0] == ']' || body[0] == '}')
			if closes && !strings.HasSuffix(out[last], "[") && !strings.HasSuffix(out[last], "{") && rapid.Bool().Draw(t, "trailing comma") {
				out[last] += ","
			}
			if rapid.Bool().Draw(t, "comment line") {
				out = append(out, "  /* a comment, \"quoted\" */")
			}
			out = append(out, l)
			last = len(out) - 1
		}
		for i := range out {
			if rapid.Bool().Draw(t, "end comment") {
				out[i] += " // a comment, ] }"
			}
		}
		text := strings.Join(out, "\n")
		name, b := "f.jsonc", buildC
		if json5 {
			name, b = "f.json5", build5
			text = plainKey.ReplaceAllString(text, "$1:")
			if rapid.Bool().Draw(t, "single quotes") {
				text = strings.ReplaceAll(strings.ReplaceAll(text, `"quoted"`, "quoted"), `"`, "'")
			}
		}
		extracttest.CheckLeaves(t, b, root, name, []byte(text), doc, func(p string) Spec { return Spec{File: name, Path: p} })
	})
}
