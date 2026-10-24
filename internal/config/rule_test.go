package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract"
)

const goodRule = `
checks:
  - id: s3-endpoint-agrees
    reason: The S3 endpoint is in a Service and in the client's config.
    scope: repo
    extract:
      service: {yaml: {file: deploy/service.yaml, path: spec.clusterIP}}
      client:  {regex: {file: config/client.yaml, pattern: 'endpoint: "([^:]+):'}}
    agree: [service, client]
    normalize: [trim, lower]
    allow_empty: [client]
  - id: program
    reason: r
    scope: repo
    run: ['true']
`

func TestParseRule(t *testing.T) {
	checks, err := Parse([]byte(goodRule))
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(checks))
	}
	r := checks[0].Rule
	if r == nil {
		t.Fatal("rule decoded without a Rule")
	}
	var names []string
	for _, e := range r.Extractors {
		names = append(names, e.Name)
		if e.Extractor == nil {
			t.Errorf("extractor %q not built", e.Name)
		}
	}
	// Sorted by name, not in the order the map happened to decode.
	if got := strings.Join(names, " "); got != "client service" {
		t.Errorf("extractors = %q, want \"client service\"", got)
	}
	if got := strings.Join(r.Agree, " "); got != "service client" {
		t.Errorf("agree = %q", got)
	}
	if len(r.Normalize) != 2 || r.Normalize[0] != check.NormalizeTrim || r.Normalize[1] != check.NormalizeLower {
		t.Errorf("normalize = %v", r.Normalize)
	}
	if got := strings.Join(r.AllowEmpty, " "); got != "client" {
		t.Errorf("allow_empty = %q, want \"client\"", got)
	}
	// A rule's extractors take no timeout; a program check keeps the default.
	if checks[0].Timeout != 0 || checks[1].Timeout != DefaultTimeout {
		t.Errorf("timeouts = %s and %s, want 0 and %s", checks[0].Timeout, checks[1].Timeout, DefaultTimeout)
	}
	if checks[1].Rule != nil {
		t.Error("program check decoded with a Rule")
	}
	// The loader wires the real extractor: the client regex reads its file.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "client.yaml"), []byte("endpoint: \"minio:9000\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vals, err := r.Extractors[0].Extractor.Extract(root)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(vals) != 1 || vals[0].Text != "minio" || vals[0].At != (check.Location{File: "config/client.yaml", Line: 1}) {
		t.Errorf("values = %+v, want minio at config/client.yaml:1", vals)
	}
}

// compare: semver loads onto the rule, compare: text loads as written, and
// an agree rule that names none compares as text. The planted break: the
// loader leaving Compare unset fails the last case.
func TestParseCompare(t *testing.T) {
	head := "checks:\n- id: r\n  reason: r\n  scope: repo\n  extract: {v: {json: {file: f.json, path: a}}, w: {json: {file: g.json, path: a}}}\n  agree: [v, w]\n"
	for yaml, want := range map[string]check.Compare{
		head + "  compare: semver\n": check.CompareSemver,
		head + "  compare: text\n":   check.CompareText,
		head:                         check.CompareText,
	} {
		checks, err := Parse([]byte(yaml))
		if err != nil {
			t.Fatal(err)
		}
		if got := checks[0].Rule.Compare; got != want {
			t.Errorf("compare %q, want %q, from:\n%s", got, want, yaml)
		}
	}
	checks, err := Parse([]byte("checks:\n- id: r\n  reason: r\n  scope: repo\n  extract: {v: {json: {file: f.json, path: a}}}\n  assert: size(v) > 0\n"))
	if err != nil || checks[0].Rule.Compare != "" {
		t.Errorf("an assert rule has compare %q, %v; want none", checks[0].Rule.Compare, err)
	}
}

// An assert is compiled at load, so a rule that loads can run it.
func TestParseAssertRule(t *testing.T) {
	checks, err := Parse([]byte("checks:\n- id: a\n  reason: r\n  scope: repo\n  extract: {v: {json: {file: f.json, path: a}}, w: {json: {file: g.json, path: a}}}\n  assert: v[0] == w[0]\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := checks[0].Rule
	if r.Assert != "v[0] == w[0]" {
		t.Fatalf("assert = %q", r.Assert)
	}
	if r.Compiled == nil {
		t.Fatal("assert loaded without being compiled")
	}
	for values, want := range map[[2]string]bool{{"x", "x"}: true, {"x", "y"}: false} {
		got, err := r.Compiled.Eval(context.Background(), map[string][]string{"v": {values[0]}, "w": {values[1]}})
		if err != nil || got != want {
			t.Errorf("%v: %v, %v; want %v", values, got, err, want)
		}
	}
	if checks, err := Parse([]byte(goodRule)); err != nil || checks[0].Rule.Compiled != nil {
		t.Errorf("an agree rule: err %v, compiled %v; want no assert compiled", err, checks[0].Rule.Compiled)
	}
}

// Each case plants one break in an assert. The load error names the check
// and shows the expression with a caret under the column.
func TestParseRejectsAsserts(t *testing.T) {
	head := "checks:\n- id: bad-assert\n  reason: r\n  scope: repo\n  extract: {v: {json: {file: f.json, path: a}}}\n"
	cases := map[string]struct{ yaml, want string }{
		"type error": {
			head + "  assert: v == 'x'\n",
			"checks[0]: check \"bad-assert\": assert: found no matching overload for '_==_' applied to '(list(string), string)', at line 1 column 3 of the expression:\n    v == 'x'\n      ^",
		},
		"undeclared name": {
			head + "  assert: v[0] == w[0]\n",
			"assert: undeclared reference to 'w', at line 1 column 9 of the expression:\n    v[0] == w[0]\n            ^",
		},
		"result that is not a bool": {
			head + "  assert: v[0]\n",
			"assert: the expression has type string; assert needs a bool",
		},
		"parse error": {
			head + "  assert: v[0] ==\n",
			"assert: Syntax error: mismatched input '<EOF>'",
		},
		"error on the second line of a block": {
			head + "  assert: |\n    size(v) > 0 &&\n      int(v[0]) > '3'\n",
			"'_>_' applied to '(int, string)', at line 2 column 13 of the expression:\n      int(v[0]) > '3'\n                ^",
		},
		"nesting beyond the parser's limit": {
			head + "  assert: size(v) > 0 && size(" + strings.Repeat("[", 40) + "1" + strings.Repeat("]", 40) + ") > 0\n",
			`checks[0]: check "bad-assert": assert: the expression nests deeper than 32 levels`,
		},
		"extractor the expression does not use": {
			"checks:\n- id: bad-assert\n  reason: r\n  scope: repo\n  extract: {v: {json: {file: f.json, path: a}}, w: {json: {file: g.json, path: a}}}\n  assert: size(v) > 0\n",
			`checks[0]: check "bad-assert": extractor "w" is declared but assert does not use it`,
		},
		"extractor name CEL cannot read": {
			"checks:\n- id: bad-assert\n  reason: r\n  scope: repo\n  extract: {s3-endpoint: {json: {file: f.json, path: a}}}\n  assert: 'true'\n",
			`checks[0]: check "bad-assert": extractor "s3-endpoint": assert refers to an extractor by its name`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error:\n%v\nwant it to contain:\n%s", err, tc.want)
			}
			if !strings.Contains(err.Error(), `checks[0]: check "bad-assert"`) {
				t.Fatalf("error = %v, want it to name the check", err)
			}
		})
	}
}

// A rule with agree beside assert gets the one refusal, not a second about
// the expression, and an assert is compiled even when an extractor does not
// build, so one pass reports both.
func TestParseAssertErrorsOnePass(t *testing.T) {
	_, err := Parse([]byte("checks:\n- id: a\n  reason: r\n  scope: repo\n  extract: {v: {json: {file: f.json, path: a}}}\n  agree: [v]\n  assert: v == 1\n"))
	if err == nil || !strings.Contains(err.Error(), "agree and assert are both set") || strings.Contains(err.Error(), "no matching overload") {
		t.Fatalf("error = %v, want only the agree and assert refusal", err)
	}
	_, err = Parse([]byte("checks:\n- id: a\n  reason: r\n  scope: repo\n  extract: {v: {}}\n  assert: v == 1\n"))
	for _, want := range []string{`extractor "v": an extractor sets exactly one`, "no matching overload for '_==_' applied to '(list(string), int)'"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
}

// Each case plants one break in a rule and names the refusal. Every refusal
// names the check.
func TestParseRejectsRules(t *testing.T) {
	const ex = "  extract: {v: {json: {file: f.json, path: a}}}\n"
	head := "checks:\n- id: bad-rule\n  reason: r\n  scope: repo\n"
	cases := map[string]struct{ yaml, want string }{
		"run and extract": {
			head + "  run: [x]\n" + ex + "  agree: [v]\n", "a check is a program check (run) or a rule (extract), never both",
		},
		"run and agree": {
			head + "  run: [x]\n  agree: [v]\n", "never both",
		},
		"neither run nor extract": {
			head, "run or extract is required",
		},
		"agree without extract": {
			head + "  agree: [v]\n", "extract is required for a rule",
		},
		"empty extract": {
			head + "  extract: {}\n  agree: [v]\n", "extract is required for a rule",
		},
		"neither agree nor assert": {
			head + ex, "a rule needs agree or assert",
		},
		"agree and assert": {
			head + ex + "  agree: [v]\n  assert: v == 'x'\n", "agree and assert are both set",
		},
		"agree names an undeclared extractor": {
			head + ex + "  agree: [v, w]\n", `agree names "w", which extract does not declare`,
		},
		"extractor agree leaves out": {
			head + "  extract: {v: {json: {file: f.json, path: a}}, w: {json: {file: g.json, path: a}}}\n  agree: [v]\n",
			`extractor "w" is declared but agree does not name it`,
		},
		"unknown normalize": {
			head + ex + "  agree: [v]\n  normalize: [trim, upper]\n", `normalize "upper" must be one of trim, lower`,
		},
		"timeout on a rule": {
			head + ex + "  agree: [v]\n  timeout: 5s\n", "timeout applies to a program check only",
		},
		// A zero timeout left the field at zero, which validation reads as
		// no key, so the rule loaded with a key that does nothing. The
		// planted break: the case for a rule removed from build.
		"zero timeout on a rule": {
			head + ex + "  agree: [v]\n  timeout: 0s\n", "timeout applies to a program check only: a rule's extractors take no timeout",
		},
		"extractor with no kind": {
			head + "  extract: {v: {}}\n  agree: [v]\n", `extractor "v": an extractor sets exactly one of yaml, json, toml, regex`,
		},
		"extractor with two kinds": {
			head + "  extract: {v: {json: {file: f, path: a}, toml: {file: f, path: a}}}\n  agree: [v]\n", `extractor "v": an extractor sets exactly one`,
		},
		"unknown key in an extractor": {
			head + "  extract: {v: {json: {file: f.json, pth: a}}}\n  agree: [v]\n", `unknown field "pth"`,
		},
		"unknown key on a rule": {
			head + ex + "  agrees: [v]\n", `unknown field "agrees"`,
		},
		"bad timeout on a rule": {
			head + ex + "  agree: [v]\n  timeout: soon\n", "timeout: time: invalid duration",
		},
		"agree names an extractor twice": {
			head + ex + "  agree: [v, v]\n", `agree names "v" twice`,
		},
		"allow_empty as a bool": {
			head + ex + "  agree: [v]\n  allow_empty: true\n", "allow_empty takes a list of extractor names, such as allow_empty: [name]; true is not accepted",
		},
		"allow_empty false": {
			head + ex + "  agree: [v]\n  allow_empty: false\n", "allow_empty takes a list of extractor names",
		},
		"allow_empty empty": {
			head + ex + "  agree: [v]\n  allow_empty: []\n", "allow_empty lists no extractor",
		},
		"allow_empty names an undeclared extractor": {
			head + ex + "  agree: [v]\n  allow_empty: [w]\n", `allow_empty names "w", which extract does not declare`,
		},
		"allow_empty on a program check": {
			head + "  run: [x]\n  allow_empty: [v]\n", "never both",
		},
		"skip_not_literal as a bool": {
			head + ex + "  agree: [v]\n  skip_not_literal: true\n", "skip_not_literal takes a list of extractor names, such as skip_not_literal: [name]; true is not accepted",
		},
		"skip_not_literal false": {
			head + ex + "  agree: [v]\n  skip_not_literal: false\n", "skip_not_literal takes a list of extractor names, such as skip_not_literal: [name]; false is not accepted",
		},
		"skip_not_literal as a scalar": {
			head + ex + "  agree: [v]\n  skip_not_literal: v\n", `skip_not_literal takes a list of extractor names, such as skip_not_literal: [name]; "v" is not accepted`,
		},
		"skip_not_literal as a quoted bool": {
			head + ex + "  agree: [v]\n  skip_not_literal: \"true\"\n", `skip_not_literal takes a list of extractor names, such as skip_not_literal: [name]; "true" is not accepted`,
		},
		"skip_not_literal as a mapping": {
			head + ex + "  agree: [v]\n  skip_not_literal: {v: true}\n", `skip_not_literal takes a list of extractor names, such as skip_not_literal: [name]; {"v":true} is not accepted`,
		},
		"allow_empty as a scalar": {
			head + ex + "  agree: [v]\n  allow_empty: v\n", `allow_empty takes a list of extractor names, such as allow_empty: [name]; "v" is not accepted`,
		},
		"skip_not_literal as a long string": {
			head + ex + "  agree: [v]\n  skip_not_literal: " + strings.Repeat("x", 100) + "\n",
			`skip_not_literal: [name]; "` + strings.Repeat("x", 64) + `"... is not accepted`,
		},
		// At maxQuotedKey, 64 bytes, a value is quoted whole and without
		// "...": a string by its text, which its quotes take to 66 bytes of
		// JSON, and any other value by its JSON. One byte more is cut.
		"skip_not_literal as a string of 64 bytes": {
			head + ex + "  agree: [v]\n  skip_not_literal: " + strings.Repeat("x", 64) + "\n",
			`skip_not_literal: [name]; "` + strings.Repeat("x", 64) + `" is not accepted`,
		},
		"skip_not_literal as a string of 65 bytes": {
			head + ex + "  agree: [v]\n  skip_not_literal: " + strings.Repeat("x", 65) + "\n",
			`skip_not_literal: [name]; "` + strings.Repeat("x", 64) + `"... is not accepted`,
		},
		"skip_not_literal as a mapping of 64 bytes": {
			head + ex + "  agree: [v]\n  skip_not_literal: {v: " + strings.Repeat("x", 56) + "}\n",
			`skip_not_literal: [name]; {"v":"` + strings.Repeat("x", 56) + `"} is not accepted`,
		},
		"skip_not_literal as a mapping of 65 bytes": {
			head + ex + "  agree: [v]\n  skip_not_literal: {v: " + strings.Repeat("x", 57) + "}\n",
			`skip_not_literal: [name]; {"v":"` + strings.Repeat("x", 57) + `"... is not accepted`,
		},
		// The value as JSON is {"v":"xxx...é...}, and its byte 64 is inside
		// é, so the cut falls before é rather than on half of it.
		"skip_not_literal as a long mapping": {
			head + ex + "  agree: [v]\n  skip_not_literal: {v: " + strings.Repeat("x", 57) + "é" + strings.Repeat("x", 20) + "}\n",
			`skip_not_literal: [name]; {"v":"` + strings.Repeat("x", 57) + `... is not accepted`,
		},
		"allow_empty as a number": {
			head + ex + "  agree: [v]\n  allow_empty: 1\n", `allow_empty takes a list of extractor names, such as allow_empty: [name]; 1 is not accepted`,
		},
		"skip_not_literal empty": {
			head + ex + "  agree: [v]\n  skip_not_literal: []\n", "skip_not_literal lists no extractor: name the extractors whose not literal values to leave out, or remove the key",
		},
		"skip_not_literal null": {
			head + ex + "  agree: [v]\n  skip_not_literal: ~\n", "skip_not_literal lists no extractor",
		},
		"skip_not_literal names a literal only extractor": {
			head + ex + "  agree: [v]\n  skip_not_literal: [v]\n", `skip_not_literal names "v", whose kind of extractor, json, returns only literal values, so it has nothing to leave out`,
		},
		"skip_not_literal names an ini extractor of a dialect that computes nothing": {
			head + "  extract: {v: {ini: {file: .gitconfig, section: core, key: bare}}}\n" + "  agree: [v]\n  skip_not_literal: [v]\n", `skip_not_literal names "v", whose kind of extractor, ini, git dialect on .gitconfig, returns only literal values, so it has nothing to leave out`,
		},
		"skip_not_literal names an undeclared extractor": {
			head + ex + "  agree: [v]\n  skip_not_literal: [w]\n", `skip_not_literal names "w", which extract does not declare`,
		},
		"skip_not_literal names an extractor twice": {
			head + ex + "  agree: [v]\n  skip_not_literal: [v, v]\n", `skip_not_literal names "v" twice`,
		},
		"skip_not_literal on a program check": {
			head + "  run: [x]\n  skip_not_literal: [v]\n", "a check is a program check (run) or a rule (extract), never both",
		},
		"skip_not_literal in another case": {
			head + ex + "  agree: [v]\n  Skip_Not_Literal: [v]\n", `key "Skip_Not_Literal" must be spelled "skip_not_literal"`,
		},
		"blank assert beside agree": {
			head + ex + "  agree: [v]\n  assert: '  '\n", "assert is blank: write the expression or remove the key",
		},
		"live rule": {
			"checks:\n- id: bad-rule\n  reason: r\n  scope: live\n" + ex + "  agree: [v]\n", `scope "live": a rule reads files, so its scope is repo`,
		},
		"key in another case on a check": {
			head + ex + "  Agree: [v]\n", `key "Agree" must be spelled "agree"`,
		},
		"key in another case beside the key": {
			head + ex + "  agree: [v]\n  Agree: [w]\n", `key "Agree" must be spelled "agree"`,
		},
		"extractor kind in another case": {
			head + "  extract: {v: {json: {file: f, path: a}, JSON: {file: g, path: b}}}\n  agree: [v]\n", `extract.v: key "JSON" must be spelled "json"`,
		},
		"extractor field in another case": {
			head + "  extract: {v: {yaml: {File: f, path: a}}}\n  agree: [v]\n", `extract.v.yaml: key "File" must be spelled "file"`,
		},
		"type error": {
			head + ex + "  agree: {v: w}\n", "decode: json: cannot unmarshal",
		},
		"two extractors with one spec": {
			head + "  extract:\n    a: &s {yaml: {file: deploy/service.yaml, path: spec.clusterIP}}\n    b: *s\n  agree: [a, b]\n",
			`extractors "a" and "b" read the same place, so comparing them compares a value with itself`,
		},
		"two extractors with one spec written out": {
			head + "  extract:\n    a: {regex: {file: f, pattern: 'v(.*)'}}\n    b: {regex: {file: f, pattern: 'v(.*)'}}\n  agree: [a, b]\n",
			`extractors "a" and "b" read the same place`,
		},
		"one file spelt f and ./f": {
			head + "  extract:\n    a: {yaml: {file: f, path: x}}\n    b: {yaml: {file: ./f, path: x}}\n  agree: [a, b]\n",
			`extractors "a" and "b" read the same place, so comparing them compares a value with itself`,
		},
		"one file spelt d/f and d/../d/f": {
			head + "  extract:\n    a: {json: {file: d/f, path: x}}\n    b: {json: {file: d/../d//f, path: x}}\n  agree: [a, b]\n",
			`extractors "a" and "b" read the same place`,
		},
		"one place under two limits": {
			head + "  extract:\n    a: {toml: {file: f, path: x}}\n    b: {toml: {file: f, path: x, max_bytes: 10}}\n  agree: [a, b]\n",
			`extractors "a" and "b" read the same place`,
		},
		// Since a72e292 a place is compared by its JSON, so no namespaces
		// and an empty map of them are one place, as they read one.
		"one xml place with and without an empty namespaces map": {
			head + "  extract:\n    a: {xml: {file: f.xml, path: /a}}\n    b: {xml: {file: f.xml, path: /a, namespaces: {}}}\n  agree: [a, b]\n",
			`extractors "a" and "b" read the same place`,
		},
		"one regex place spelt two ways": {
			head + "  extract:\n    a: {regex: {file: ./f, pattern: 'v(.*)', max_bytes: 5}}\n    b: {regex: {file: f, pattern: 'v(.*)'}}\n  agree: [a, b]\n",
			`extractors "a" and "b" read the same place`,
		},
		"compare unknown": {
			head + ex + "  agree: [v]\n  compare: version\n", `compare "version" must be one of text, semver`,
		},
		"compare in another case": {
			head + ex + "  agree: [v]\n  compare: SemVer\n", `compare "SemVer" must be one of text, semver`,
		},
		"compare blank": {
			head + ex + "  agree: [v]\n  compare: ''\n", "compare is blank: write text or semver, or remove the key for text",
		},
		"compare on assert": {
			head + ex + "  assert: size(v) > 0\n  compare: semver\n", `compare "semver" applies to agree; assert compares in its expression`,
		},
		"compare on a program check": {
			head + "  run: [x]\n  compare: text\n", "never both",
		},
		"normalize empty": {
			head + ex + "  agree: [v]\n  normalize: []\n", "normalize lists no option: name trim, lower or both, or remove the key",
		},
		"normalize null": {
			head + ex + "  agree: [v]\n  normalize: ~\n", "normalize lists no option",
		},
		"rule without reason": {
			"checks:\n- id: bad-rule\n  scope: repo\n" + ex + "  agree: [v]\n", "reason is required",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), `checks[0]: check "bad-rule"`) {
				t.Fatalf("error = %v, want it to name the check", err)
			}
		})
	}
}

// A check whose extractor does not build is still validated, so one pass
// names both problems, and agree does not also report the extractor missing.
func TestParseReportsEveryProblemWithARule(t *testing.T) {
	_, err := Parse([]byte("checks:\n- id: a\n  scope: repo\n  extract: {v: {}}\n  agree: [v]\n- id: b\n  reason: r\n  scope: repo\n  extract: {w: {json: {file: f, path: a}}}\n  agree: [w, x]\n"))
	if err == nil {
		t.Fatal("broken rules loaded")
	}
	msg := err.Error()
	for _, want := range []string{`check "a": extractor "v"`, `check "a": reason is required`, `checks[1]: check "b": agree names "x"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, `agree names "v"`) {
		t.Errorf("error %q reports a declared extractor as undeclared", msg)
	}
}

// A check that does not decode is named by its id and does not stop the next
// check from being validated.
func TestParseNamesACheckThatDoesNotDecode(t *testing.T) {
	_, err := Parse([]byte("checks:\n- {id: a, reson: r, scope: repo, run: [x]}\n- {id: b, scope: repo, run: [x]}\n"))
	if err == nil {
		t.Fatal("broken checks loaded")
	}
	for _, want := range []string{`checks[0]: check "a": decode`, `checks[1]: check "b": reason is required`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// The top level is matched exactly too: a second list under Checks used to
// merge with checks and drop a check without an error.
func TestParseRejectsTopLevelKeyInAnotherCase(t *testing.T) {
	_, err := Parse([]byte("checks:\n- {id: a, reason: r, scope: repo, run: [x]}\nChecks:\n- {id: b, reason: r, scope: repo, run: [x]}\n"))
	if err == nil || !strings.Contains(err.Error(), `decode: key "Checks" must be spelled "checks"`) {
		t.Fatalf("error = %v, want the misspelt key named", err)
	}
}

// A YAML 1.1 float such as .inf in a string field is refused by its name,
// not by the JSON step, which cannot hold infinity; quoted, it is the file
// extension the author wrote.
func TestParseInfAndNaNInStrings(t *testing.T) {
	_, err := Parse([]byte("checks:\n- {id: a, reason: .nan, scope: repo, run: [ls, .inf, -.inf]}\n"))
	for _, want := range []string{
		`reason: YAML 1.1 reads .nan, unquoted, as the number NaN`,
		`run[1]: YAML 1.1 reads .inf, unquoted, as the number +Inf`,
		`run[2]: YAML 1.1 reads -.inf, unquoted, as the number -Inf`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
	checks, err := Parse([]byte("checks:\n- {id: a, reason: '.nan', scope: repo, run: [ls, '.inf', \"-.inf\"]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(checks[0].Run, " "); got != "ls .inf -.inf" || checks[0].Reason != ".nan" {
		t.Errorf("run = %q, reason = %q, want \"ls .inf -.inf\" and \".nan\"", got, checks[0].Reason)
	}
}

// Two specs that differ in a field that decides where they read load.
func TestParseAcceptsExtractorsThatDifferInOneField(t *testing.T) {
	for _, pair := range [][2]string{
		{"{yaml: {file: f, path: x}}", "{yaml: {file: f, path: x, kind: Service}}"},
		{"{yaml: {file: f, path: x}}", "{yaml: {file: f, path: x, name: web}}"},
		{"{yaml: {file: f, path: x}}", "{yaml: {file: g, path: x}}"},
		{"{json: {file: f, path: x}}", "{json: {file: f, path: y}}"},
		{"{toml: {file: f, path: x}}", "{toml: {file: ../f, path: x}}"},
		{"{regex: {file: f, pattern: 'v(.*)'}}", "{regex: {file: f, pattern: 'w(.*)'}}"},
		{"{yaml: {file: f, path: x}}", "{json: {file: f, path: x}}"},
	} {
		raw := "checks:\n- id: a\n  reason: r\n  scope: repo\n  extract:\n    a: " + pair[0] + "\n    b: " + pair[1] + "\n  agree: [a, b]\n"
		_, err := Parse([]byte(raw))
		if err != nil && strings.Contains(err.Error(), "read the same place") {
			t.Errorf("%s and %s: %v", pair[0], pair[1], err)
		}
	}
}

// The YAML library reads the first document of a stream, so a check after a
// --- used to be dropped while the run passed. The count comes from a YAML
// decoder, so every line break and encoding the library reads counts: the
// cases below are the review's shapes that a scan of LF lines missed.
func TestParseRefusesSeveralDocuments(t *testing.T) {
	const (
		a = "checks:\n- {id: first, reason: r, scope: repo, run: ['true']}\n"
		b = "checks:\n- {id: gate, reason: r, scope: repo, run: ['false']}\n"
	)
	cases := map[string]string{
		"a check in the second document": a + "---\n" + b,
		"an empty first document":        "---\n---\nchecks:\n- {id: a, reason: r, scope: repo, run: [x]}\n",
		"three documents":                "--- {checks: []}\n--- {checks: []}\n--- ~\n",
		"CR line ends":                   strings.ReplaceAll(a+"---\n"+b, "\n", "\r"),
		"--- then CR":                    a + "---\r" + b,
		"NEL around ---":                 a + "\u0085---\u0085" + b,
		"LS around ---":                  a + "\u2028---\u2028" + b,
		"PS around ---":                  a + "\u2029---\u2029" + b,
		"--- then NEL":                   a + "---\u0085" + b,
		"UTF-16LE":                       utf16Text(a+"---\n"+b, false),
		"UTF-16BE":                       utf16Text(a+"---\n"+b, true),
		"a trailing document of ~":       a + "--- ~\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(raw))
			if err == nil || !strings.Contains(err.Error(), "YAML documents; errata reads one, so put every check in one document under checks") {
				t.Fatalf("error = %v, want the several-documents refusal", err)
			}
		})
	}
	_, err := Parse([]byte(cases["three documents"]))
	if err == nil || !strings.Contains(err.Error(), "the file holds 3 YAML documents") {
		t.Fatalf("error = %v, want the count", err)
	}
}

// A document that starts after a ... marker without a --- of its own is one
// the library does not read and the decoder cannot: the file is refused with
// the decoder's error rather than loading the first document alone.
func TestParseRefusesADocumentTheDecoderCannotRead(t *testing.T) {
	const (
		a = "checks:\n- {id: first, reason: r, scope: repo, run: ['true']}\n"
		b = "checks:\n- {id: gate, reason: r, scope: repo, run: ['false']}\n"
	)
	for name, tc := range map[string]struct{ raw, want string }{
		"a document after ...":            {a + "...\n" + b, "decode: yaml: line 3: did not find expected <document start>"},
		"content on the line of ...":      {a + "... {checks: [{id: gate, reason: r, scope: repo, run: ['false']}]}\n", "decode: yaml: line 2: did not find expected <document start>"},
		"CRLF and a document after ...":   {strings.ReplaceAll(a+"...\n"+b, "\n", "\r\n"), "decode: yaml: line 3: did not find expected <document start>"},
		"a document after ... at the end": {a + "...\nchecks: []\n", "decode: yaml: line 3: did not find expected <document start>"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.raw))
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

// Empty documents at the end hold no check, so a file that ends in ---
// loads: nothing is lost.
func TestParseAcceptsTrailingEmptyDocuments(t *testing.T) {
	const a = "checks:\n- {id: first, reason: r, scope: repo, run: ['true']}\n"
	for name, raw := range map[string]string{
		"a last ---":             a + "---\n",
		"a last --- and comment": a + "---\n# end\n",
		"... then ---":           a + "...\n---\n",
		"two last ---":           a + "---\n---\n",
		"a last --- in CR":       strings.ReplaceAll(a+"---\n", "\n", "\r"),
	} {
		t.Run(name, func(t *testing.T) {
			checks, err := Parse([]byte(raw))
			if err != nil || len(checks) != 1 || checks[0].ID != "first" {
				t.Fatalf("Parse = %v, %v; want the one check", checks, err)
			}
		})
	}
}

// A rule with assert and no extractor is refused for the missing extract
// alone: compiling the expression against no names would add an error for
// every name it uses.
func TestParseRuleWithoutExtractorsCompilesNothing(t *testing.T) {
	_, err := Parse([]byte("checks:\n- id: a\n  reason: r\n  scope: repo\n  extract: {}\n  assert: v[0] == '1'\n"))
	if err == nil || !strings.Contains(err.Error(), "extract is required for a rule and must declare at least one extractor") {
		t.Fatalf("error = %v, want the missing extract", err)
	}
	if strings.Contains(err.Error(), "assert:") {
		t.Fatalf("error = %v, want no compile error beside it", err)
	}
}

// utf16Text is s in UTF-16 with a byte order mark, big endian when be is set.
func utf16Text(s string, be bool) string {
	var out []byte
	for _, u := range utf16.Encode([]rune("\ufeff" + s)) {
		if be {
			out = append(out, byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8))
		}
	}
	return string(out)
}

// One document loads however it is marked, and --- inside a value is not a
// document marker.
func TestParseAcceptsOneDocument(t *testing.T) {
	for name, raw := range map[string]string{
		"start marker":          "---\nchecks:\n- {id: a, reason: r, scope: repo, run: [x]}\n",
		"start and end markers": "%YAML 1.1\n---\nchecks:\n- {id: a, reason: r, scope: repo, run: [x]}\n...\n# trailing comment\n",
		"marker with content":   "--- {checks: [{id: a, reason: r, scope: repo, run: [x]}]}\n",
		"dashes in a value":     "checks:\n- id: a\n  reason: |\n    ---\n    a rule of dashes\n  scope: repo\n  run: [echo, '---', ---x]\n",
		"byte order mark":       "\ufeff---\nchecks:\n- {id: a, reason: r, scope: repo, run: [x]}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(raw)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// computes wraps an extractor that extract.Build made and declares that it
// can return a not literal value, as a shell or CI reader will.
type computes struct{ check.Extractor }

func (computes) MayReturnNotLiteral() bool { return true }

func buildComputing(s extract.Spec) (check.Extractor, error) {
	x, err := extract.Build(s)
	if err != nil {
		return nil, err
	}
	return computes{x}, nil
}

// skip_not_literal loads over extractors whose kind can return a not
// literal value, and is refused over the kinds errata has today, whose values
// are all literal.
func TestParseSkipNotLiteral(t *testing.T) {
	const yaml = `
checks:
  - id: version-agrees
    reason: r
    scope: repo
    extract:
      chart: {yaml: {file: Chart.yaml, path: version}}
      shell: {regex: {file: build.sh, pattern: 'VERSION=(.*)'}}
    agree: [chart, shell]
    skip_not_literal: [shell, chart]
`
	checks, err := ParseWith([]byte(yaml), buildComputing)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(checks[0].Rule.SkipNotLiteral, " "); got != "shell chart" {
		t.Errorf("skip_not_literal = %q, want \"shell chart\"", got)
	}
	// An ini extractor whose dialect's tool computes values may be listed.
	if _, err := Parse([]byte("checks:\n- id: a\n  reason: r\n  scope: repo\n  extract:\n    php: {ini: {file: php.ini, key: error_reporting}}\n    tox: {ini: {file: tox.ini, section: testenv, key: deps}}\n  agree: [php, tox]\n  skip_not_literal: [php, tox]\n")); err != nil {
		t.Errorf("skip_not_literal over php and tox: %v", err)
	}
	_, err = Parse([]byte(yaml))
	for _, want := range []string{
		`skip_not_literal names "shell", whose kind of extractor, regex, returns only literal values, so it has nothing to leave out`,
		`skip_not_literal names "chart", whose kind of extractor, yaml, returns only literal values`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse: error = %v, want it to contain %q", err, want)
		}
	}
}

// A builder that returns no extractor and no error built nothing, which is a
// load error naming the extractor, not a nil the rule would call at run time.
// The planted break is accepting it, which loaded skip_not_literal past the
// literal only refusal too.
func TestParseRefusesAnExtractorThatBuiltNothing(t *testing.T) {
	const yaml = `
checks:
  - id: built-nothing
    reason: r
    scope: repo
    extract:
      shell: {regex: {file: build.sh, pattern: 'VERSION=(.*)'}}
    agree: [shell]
    skip_not_literal: [shell]
`
	_, err := ParseWith([]byte(yaml), func(extract.Spec) (check.Extractor, error) { return nil, nil })
	if want := `extractor "shell": built nothing, and gave no error`; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}

// manyExtractors is a rule of n extractors, e0 to e<n-1>, that agree over
// all of them; with places of 8, they cycle through 8 places, and with 0
// each reads a place of its own.
func manyExtractors(n, places int) []byte {
	var b strings.Builder
	b.WriteString("checks:\n- id: a\n  reason: r\n  scope: repo\n  extract:\n")
	names := make([]string, n)
	for i := range n {
		names[i] = fmt.Sprintf("e%d", i)
		p := i
		if places > 0 {
			p = i % places
		}
		fmt.Fprintf(&b, "    e%d: {regex: {file: f%d, pattern: 'v(.*)'}}\n", i, p)
	}
	b.WriteString("  agree: [" + strings.Join(names, ", ") + "]\n")
	return []byte(b.String())
}

// parseAllocs is the bytes Parse allocates for raw, and its error.
func parseAllocs(raw []byte) (uint64, error) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := Parse(raw)
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc, err
}

// A rule of 5000 extractors loads, or is refused, in memory linear in their
// number: one comparison of every pair, with an error for each pair over one
// place, took 3 GB and more for 5000 over 8 places (security review of #90).
// Four times the extractors may take at most six times the memory, where a
// cost in the square of their number takes sixteen. The refusal names at
// most maxSamePlace extractors and counts the rest. The planted break: the
// pairwise loop with an error for each pair, which fails the ratio.
func TestManyExtractorsLoadInLinearMemory(t *testing.T) {
	small, _ := parseAllocs(manyExtractors(1250, 8))
	large, err := parseAllocs(manyExtractors(5000, 8))
	if large > 6*small || large > 1<<30 {
		t.Errorf("1250 extractors over 8 places allocated %d MiB, 5000 allocated %d MiB", small>>20, large>>20)
	}
	if err == nil || strings.Count(err.Error(), "read the same place") != maxSamePlace ||
		!strings.Contains(err.Error(), fmt.Sprintf("and %d more extractors read a place an extractor above reads", 5000-8-maxSamePlace)) {
		t.Errorf("5000 extractors over 8 places: %v", err)
	}
	if _, err := Parse(manyExtractors(5000, 0)); err != nil {
		t.Errorf("5000 extractors over 5000 places: %v", err)
	}
}
