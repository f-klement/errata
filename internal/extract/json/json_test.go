package json

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// run builds s and extracts from root.
func run(t *testing.T, s Spec, root string) ([]check.Value, error) {
	t.Helper()
	return extracttest.Run(t, Build, s, root)
}

// mustRun is run for an extraction that must succeed.
func mustRun(t *testing.T, s Spec, root string) []check.Value {
	t.Helper()
	return extracttest.MustRun(t, Build, s, root)
}

// fromData is what every extractor runs on the bytes of its file.
type fromData interface {
	FromData(data []byte) ([]check.Value, error)
}

func jsonSpec(path string) Spec { return Spec{File: "f.json", Path: path} }

func TestJSONValuesAndLines(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.json": `{
  "name": "errata",
  "n": 1e3,
  "f": 1.50,
  "neg": -0,
  "ok": true,
  "none": null,
  "esc": "a\"bé",
  "list": [1,
    2, {"x": "deep"}],
  "a.b": {"c": "dotted"},
  "empty": {}
}`})
	for path, want := range map[string]string{
		"name":      "errata@2",
		"n":         "1e3@3",
		"f":         "1.50@4",
		"neg":       "-0@5",
		"ok":        "true@6",
		"none":      "null@7",
		"esc":       `a"bé@8`,
		"list[0]":   "1@9",
		"list[1]":   "2@10",
		"list[*].x": "deep@10",
		"['a.b'].c": "dotted@11",
		"empty[*]":  "",
		"list[9]":   "",
		"name.x":    "",
	} {
		if got := extracttest.Texts(mustRun(t, jsonSpec(path), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

func TestJSONRootScalar(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.json": "\n\n  \"v1\"\n"})
	if got := extracttest.Texts(mustRun(t, jsonSpec("$"), root)); got != "v1@3" {
		t.Errorf("got %q", got)
	}
}

func TestJSONPathOnAContainerIsAnError(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.json": `{"a": {"b": [1]}}`})
	_, err := run(t, jsonSpec("a"), root)
	extracttest.WantError(t, err, `f.json: path "a" selects a mapping at line 1, not a scalar`)
	_, err = run(t, jsonSpec("a.b"), root)
	extracttest.WantError(t, err, `f.json: path "a.b" selects a sequence at line 1, not a scalar`)
}

// Each body is a planted break of the JSON reader's refusals.
func TestJSONThatDoesNotParseIsAnError(t *testing.T) {
	for body, want := range map[string]string{
		"":                       "f.json: the file ends before the JSON value is complete",
		"   ":                    "f.json: the file ends before the JSON value is complete",
		`{"a": 1`:                "f.json: unexpected end of JSON input",
		`[1, 2`:                  "f.json: unexpected end of JSON input",
		`{"a":`:                  "f.json: the file ends before the JSON value is complete",
		"{\"a\": 1,\n \"a\": 2}": `f.json: line 2: key "a" is repeated`,
		"{\"a\": 1}\n{}":         "f.json: line 2: data after the top-level value",
		`{"a": 1} x`:             "f.json: line 1: data after the top-level value",
		`{"a" 1}`:                "f.json: invalid character '1' after object key",
		`{"a": tru}`:             "f.json: invalid character",
		`[1,]`:                   "f.json: invalid character ',' looking for beginning of value",
	} {
		root := extracttest.Repo(t, map[string]string{"f.json": body})
		_, err := run(t, jsonSpec("a"), root)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", body, err, want)
		}
	}
}

// A file that does not parse is an error that names the file and gives the
// parser's reason.
func TestJSONFileThatDoesNotParseIsAnError(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"bad": "a: [\n"})
	_, err := run(t, Spec{File: "bad", Path: "a"}, root)
	if want := "bad: invalid character 'a' looking for beginning of value"; err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}

// The depth of a value is the count of path steps that reach it. The reader
// reads a value at exactly core.MaxDepth and refuses one a level deeper,
// which is the planted break.
func TestJSONDepthBoundary(t *testing.T) {
	deep := func(first string, n int) string { return first + strings.Repeat("[0]", n) }
	for _, c := range []struct {
		file, body, path string
	}{
		{"f.json", extracttest.Nest(core.MaxDepth, "1"), deep("$", core.MaxDepth)},
		{"f.json", strings.Repeat(`{"a":`, core.MaxDepth) + "1" + strings.Repeat("}", core.MaxDepth), "$" + strings.Repeat(".a", core.MaxDepth)},
	} {
		s := Spec{File: c.file, Path: c.path}
		if got := extracttest.Texts(mustRun(t, s, extracttest.Repo(t, map[string]string{c.file: c.body}))); got != "1@1" {
			t.Errorf("%s at depth %d: got %q", c.file, core.MaxDepth, got)
		}
		_, err := run(t, s, extracttest.Repo(t, map[string]string{c.file: strings.Replace(c.body, "1", "[1]", 1)}))
		want := c.file + ": line 1: nested more than 1000 levels deep, the limit; a regex extractor can still read the file"
		if err == nil || err.Error() != want {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}

// JSON counts lines with a cursor that only moves forward, so a file of
// newlines costs no memory for each one. The planted break is the index of
// every newline, 8 bytes each, which took 1.2 GB for a JSON file of 32 MiB
// with one node, and here 52 times the file. What JSON still allocates is
// encoding/json's buffer, which grows to hold a run of white space: 4 times
// the file, under the bound of 5. TestRegexLinesCostNoMemoryForEachNewline
// is the same for the regex search.
func TestJSONLinesCostNoMemoryForEachNewline(t *testing.T) {
	data := []byte("1" + strings.Repeat("\n", 1<<18) + "x")
	e, err := Build(Spec{File: "f", Path: "$"})
	if err != nil {
		t.Fatal(err)
	}
	n := extracttest.Allocated(func() { _, err = e.(fromData).FromData(data) })
	extracttest.WantError(t, err, "line 262145: data after the top-level value")
	if bound := 5 * uint64(len(data)); n > bound {
		t.Errorf("%d bytes allocated for a file of %d, over %d", n, len(data), bound)
	}
}
