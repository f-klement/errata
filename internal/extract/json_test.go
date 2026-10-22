package extract

import (
	"strings"
	"testing"
)

func jsonSpec(path string) Spec { return Spec{JSON: &JSON{File: "f.json", Path: path}} }

func TestJSONValuesAndLines(t *testing.T) {
	root := repo(t, map[string]string{"f.json": `{
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
		if got := texts(mustRun(t, jsonSpec(path), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

func TestJSONRootScalar(t *testing.T) {
	root := repo(t, map[string]string{"f.json": "\n\n  \"v1\"\n"})
	if got := texts(mustRun(t, jsonSpec("$"), root)); got != "v1@3" {
		t.Errorf("got %q", got)
	}
}

func TestJSONPathOnAContainerIsAnError(t *testing.T) {
	root := repo(t, map[string]string{"f.json": `{"a": {"b": [1]}}`})
	_, err := run(t, jsonSpec("a"), root)
	wantError(t, err, `f.json: path "a" selects a mapping at line 1, not a scalar`)
	_, err = run(t, jsonSpec("a.b"), root)
	wantError(t, err, `f.json: path "a.b" selects a sequence at line 1, not a scalar`)
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
		root := repo(t, map[string]string{"f.json": body})
		_, err := run(t, jsonSpec("a"), root)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", body, err, want)
		}
	}
}
