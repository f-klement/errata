package config

import (
	"context"
	"errors"
	"strings"
	"testing"

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
	// Until the extractors land, Build returns one that is not implemented.
	if _, err := r.Extractors[0].Extractor.Extract("."); !errors.Is(err, extract.ErrNotImplemented) {
		t.Errorf("extractor error = %v", err)
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

// A YAML float such as .inf is a string where the field is a string, as it
// was before each check decoded on its own: .inf is a file extension.
func TestParseInfAndNaNInStrings(t *testing.T) {
	checks, err := Parse([]byte("checks:\n- {id: a, reason: .nan, scope: repo, run: [ls, .inf, -.inf]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(checks[0].Run, " "); got != "ls +Inf -Inf" {
		t.Errorf("run = %q, want \"ls +Inf -Inf\"", got)
	}
	if checks[0].Reason != "NaN" {
		t.Errorf("reason = %q, want \"NaN\"", checks[0].Reason)
	}
}

// Two specs that differ in any field read different places and load.
func TestParseAcceptsExtractorsThatDifferInOneField(t *testing.T) {
	_, err := Parse([]byte("checks:\n- id: a\n  reason: r\n  scope: repo\n  extract:\n    a: {yaml: {file: f, path: x}}\n    b: {yaml: {file: f, path: x, kind: Service}}\n  agree: [a, b]\n"))
	if err != nil {
		t.Fatal(err)
	}
}

// The YAML library reads the first document of a stream, so a check after a
// --- used to be dropped while the run passed.
func TestParseRefusesSeveralDocuments(t *testing.T) {
	cases := map[string]string{
		"a check in the second document": "checks:\n- {id: first, reason: r, scope: repo, run: ['true']}\n---\nchecks:\n- {id: gate, reason: r, scope: repo, run: ['false']}\n",
		"an empty first document":        "---\n---\nchecks:\n- {id: a, reason: r, scope: repo, run: [x]}\n",
		"a document after an end marker": "checks:\n- {id: a, reason: r, scope: repo, run: [x]}\n...\nchecks: []\n",
		"three documents":                "--- {checks: []}\n--- {checks: []}\n--- ~\n",
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
