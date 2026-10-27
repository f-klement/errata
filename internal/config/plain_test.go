package config

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// A string field written as a plain scalar that loads as another string is a
// load error: sigs.k8s.io/yaml decodes with yaml/v2, which reads y as true,
// so id: y loaded as the id "true".
func TestParseRefusesPlainScalarsThatAreNotStrings(t *testing.T) {
	const head = "checks:\n- id: a\n  reason: r\n  scope: repo\n"
	for name, tc := range map[string]struct{ yaml, want string }{
		"id y": {"checks:\n- {id: y, reason: r, scope: repo, run: [x]}\n",
			`checks[0]: check "y": decode: id: YAML 1.1 reads y, unquoted, as the bool true, so it would load as "true"; write it quoted: "y"`},
		"reason yes": {"checks:\n- {id: a, reason: yes, scope: repo, run: [x]}\n",
			`checks[0]: check "a": decode: reason: YAML 1.1 reads yes, unquoted, as the bool true, so it would load as "true"; write it quoted: "yes"`},
		"scope on":     {"checks:\n- {id: a, reason: r, scope: on, run: [x]}\n", `scope: YAML 1.1 reads on, unquoted, as the bool true`},
		"run no":       {head + "  run: [sh, no]\n", `run[1]: YAML 1.1 reads no, unquoted, as the bool false, so it would load as "false"; write it quoted: "no"`},
		"run off":      {head + "  run: [off]\n", `run[0]: YAML 1.1 reads off, unquoted, as the bool false`},
		"run True":     {head + "  run: [True]\n", `run[0]: YAML 1.1 reads True, unquoted, as the bool true, so it would load as "true"`},
		"run 007":      {head + "  run: [echo, 007]\n", `run[1]: YAML 1.1 reads 007, unquoted, as the number 7, so it would load as "7"; write it quoted: "007"`},
		"run 1e3":      {head + "  run: [1e3]\n", `run[0]: YAML 1.1 reads 1e3, unquoted, as the number 1000`},
		"run 0x10":     {head + "  run: [0x10]\n", `run[0]: YAML 1.1 reads 0x10, unquoted, as the number 16`},
		"run .inf":     {head + "  run: [.inf]\n", `run[0]: YAML 1.1 reads .inf, unquoted, as the number +Inf, so it would load as "+Inf"`},
		"run ~":        {head + "  run: [x, ~]\n", `run[1]: YAML 1.1 reads ~, unquoted, as null, so it would load as ""; write it quoted: "~"`},
		"timeout 1e3":  {head + "  run: [x]\n  timeout: 1e3\n", `timeout: YAML 1.1 reads 1e3, unquoted, as the number 1000`},
		"a bool tag":   {head + "  run: [!!bool y]\n", `run[0]: YAML 1.1 reads y, unquoted, as the bool true`},
		"a binary tag": {head + "  run: [!!binary aGk=]\n", `run[0]: YAML 1.1 reads aGk=, unquoted, as the binary "hi", so it would load as "hi"`},
		"an alias":     {head + "  run: [&n no]\n- {id: b, reason: r, scope: repo, run: [*n]}\n", `checks[1]: check "b": decode: run[0]: YAML 1.1 reads no`},
		"a merge":      {"checks:\n- {<<: {reason: off}, id: a, scope: repo, run: [x]}\n", `checks[0]: check "a": decode: reason: YAML 1.1 reads off`},
		"assert":       {head + "  extract: {v: {regex: {file: f, pattern: '(a)'}}}\n  assert: TRUE\n", `assert: YAML 1.1 reads TRUE, unquoted, as the bool true`},
		"file":         {head + "  extract: {v: {toml: {file: 0x1F, path: a}}}\n  assert: v\n", `extract.v.toml.file: YAML 1.1 reads 0x1F, unquoted, as the number 31`},
		"extractor name": {head + "  extract: {y: {regex: {file: f, pattern: '(a)'}}}\n  assert: 'true'\n",
			`extract: YAML 1.1 reads the name y, unquoted, as the bool true, so it would load as "true"; write it quoted: "y"`},
		"extractor name 007": {head + "  extract: {007: {json: {file: f, path: a}}, b: {json: {file: g, path: a}}}\n  agree: [b]\n",
			`extract: YAML 1.1 reads the name 007, unquoted, as the number 7, so it would load as "7"`},
		"agree":       {head + "  extract: {a: {json: {file: f, path: a}}, b: {json: {file: g, path: a}}}\n  agree: [a, null]\n", `agree[1]: YAML 1.1 reads null, unquoted, as null`},
		"every check": {"checks:\n- {id: y, reason: r, scope: repo, run: [x]}\n- {id: n, reason: r, scope: repo, run: [x]}\n", `checks[1]: check "n": decode: id: YAML 1.1 reads n`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseRaw([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// The rule both ways, over the texts the review probed. A text that loads as
// another string is refused; a text that loads as itself loads, a bool or a
// number among them (5, true, 20.04), since the check holds what was written.
func TestParseRefusesOnlyATextThatLoadsAsAnother(t *testing.T) {
	refused := []string{
		"y", "Y", "yes", "Yes", "YES", "n", "N", "no", "NO", "True", "TRUE", "False", "FALSE",
		"on", "On", "ON", "off", "Off", "OFF", "~", "null", "Null", "NULL",
		"007", "0o17", "0x1F", "0X1F", "0b101", "1_000", "-0b1", "+0x1", "1.", ".5", "-.5",
		"1e3", "1E3", "1.5e-3", "1e+3", ".inf", ".Inf", ".INF", "-.inf", "+.inf", ".nan", ".NaN", ".NAN",
		"1.20", "!!bool yes", "!!binary aGk=",
		// The review listed these as loading; yaml/v2 reads each as a number.
		"0.", "1__0", "08", "09", "1_",
	}
	loads := []string{
		"true", "false", "0", "1", "-1", "5", "1.5", "20.04", "!!int 3", "!!float 1", "!!str yes",
		"hello", "yesterday", "onion", "nope", "ok", "true-ish", "NaN", "Inf", "infinity", "yes!",
		"deploy/web.yaml", "./f", "../x", "1.2.3", "1.27.2", "v1.2", "0.1.0", "30s", "1h30m", "2d", "1.5h",
		"x:y", "https://example.com/a?b=c", "10.43.0.5", "0.0.0.0", "-c", "--flag", "1-5", `"*/15"`,
		"1e", "0x", "0o", "+", ".", "1,000", "_1", "0b2", "0x1G",
		"190:20:30", "12:30", "1:30:00", "2001-12-14", "2001-12-14t21:59:43.10-05:00", "2001-12-14 21:59:43.10 -5",
		`"y"`, `'yes'`, `"1e3"`, `'~'`, `"true"`,
	}
	parse := func(text string) ([]string, error) {
		cfg, err := parseRaw([]byte("checks:\n- id: a\n  reason: " + text + "\n  scope: repo\n  run:\n    - " + text + "\n"))
		if err != nil {
			return nil, err
		}
		return []string{cfg.Checks[0].Reason, cfg.Checks[0].Run[0]}, nil
	}
	for _, text := range refused {
		value := text[strings.LastIndex(text, " ")+1:]
		_, err := parse(text)
		for _, field := range []string{"reason", "run[0]"} {
			if want := field + ": YAML 1.1 reads " + value + ", unquoted, as "; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error = %v, want it to contain %q", text, err, want)
			}
		}
	}
	for _, text := range loads {
		want := strings.TrimPrefix(text, "!!int ")
		want = strings.TrimPrefix(want, "!!float ")
		want = strings.TrimPrefix(want, "!!str ")
		want = strings.Trim(want, `"'`)
		got, err := parse(text)
		if err != nil {
			t.Errorf("%s: %v, want it to load", text, err)
			continue
		}
		if got[0] != want || got[1] != want {
			t.Errorf("%s: reason and run[0] are %q, want %q", text, got, want)
		}
	}
}

// A string field written so that YAML 1.1 reads a string still loads, with
// the text as written.
func TestParseKeepsStringsThatAreWrittenAsStrings(t *testing.T) {
	for name, tc := range map[string]struct{ value, want string }{
		"double quoted":  {`"yes"`, "yes"},
		"single quoted":  {`'1e3'`, "1e3"},
		"a str tag":      {"!!str on", "on"},
		"a literal":      {"|-\n      off", "off"},
		"a date":         {"2001-12-14", "2001-12-14"},
		"two words":      {"y n", "y n"},
		"over two lines": {"y\n      n", "y n"},
		"sexagesimal":    {"1:20", "1:20"},
		"a dot":          {"...", "..."},
		// Read alone, the second line would end the document and leave 1.
		"a line of dots": {"1\n\n      ...", "1\n..."},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := parseRaw([]byte("checks:\n- id: a\n  reason: r\n  scope: repo\n  run:\n    - " + tc.value + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Checks[0].Run[0]; got != tc.want {
				t.Fatalf("run[0] = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every string field of a check, found from the types the file decodes
// into, is refused when written y: a string field added later is covered
// without a line here. The refusal comes before validation, so each file
// holds only the path to the field.
func TestParseRefusesYInEveryStringField(t *testing.T) {
	paths := stringFields(reflect.TypeFor[checkEntry](), nil)
	if len(paths) < 20 {
		t.Fatalf("found %d string fields: %v; the walk of the types misses some", len(paths), paths)
	}
	for _, p := range paths {
		t.Run(strings.Join(p, "."), func(t *testing.T) {
			raw := "checks: [" + flowTo(p) + "]\n"
			field := strings.ReplaceAll(strings.Join(p, "."), ".[0]", "[0]")
			want := field + ": YAML 1.1 reads y, unquoted, as the bool true"
			if p[len(p)-1] == "<name>" {
				field = strings.Join(p[:len(p)-1], ".")
				want = field + ": YAML 1.1 reads the name y, unquoted, as the bool true"
			}
			_, err := parseRaw([]byte(raw))
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("parse(%s): error = %v, want it to contain %q", raw, err, want)
			}
		})
	}
}

// stringFields lists the path to every string a value of t holds: a struct
// field by its JSON key, a list item as [0], a map value under the name v
// and a map's string key as <name>.
func stringFields(t reflect.Type, path []string) [][]string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	at := func(p string) []string { return append(append([]string{}, path...), p) }
	switch t.Kind() {
	case reflect.String:
		return [][]string{path}
	case reflect.Struct:
		var out [][]string
		for f := range t.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			out = append(out, stringFields(f.Type, at(name))...)
		}
		return out
	case reflect.Slice:
		return stringFields(t.Elem(), at("[0]"))
	case reflect.Map:
		out := stringFields(t.Elem(), at("v"))
		if t.Key().Kind() == reflect.String {
			out = append(out, at("<name>"))
		}
		return out
	default:
		return nil
	}
}

// flowTo writes a flow YAML value that holds y, unquoted, at path p.
func flowTo(p []string) string {
	if len(p) == 0 {
		return "y"
	}
	switch p[0] {
	case "[0]":
		return "[" + flowTo(p[1:]) + "]"
	case "<name>":
		return "{y: {}}"
	default:
		if len(p) > 1 && p[1] == "<name>" {
			return fmt.Sprintf("{%s: {y: {}}}", p[0])
		}
		return fmt.Sprintf("{%s: %s}", p[0], flowTo(p[1:]))
	}
}

// yaml/v3 builds a node graph with a cycle for an anchor that names itself.
// parse never walks one, since sigs.k8s.io/yaml refuses such a file first,
// but the walk ends on it all the same.
func TestPlainStringsEndsOnACycle(t *testing.T) {
	for _, raw := range []string{
		"checks: [&a {id: y, <<: *a}]\n",
		"checks: [&a {id: y, run: [*a]}]\n",
		"checks: [&a {id: y, extract: {v: *a}}]\n",
	} {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		got := plainStrings(&doc)
		if len(got) != 1 || got[0].id != "y" || len(got[0].errs) != 1 || !strings.Contains(got[0].errs[0].Error(), "id: YAML 1.1 reads y") {
			t.Errorf("%q: plainStrings = %+v, want the one id refused", raw, got)
		}
		if _, err := parseRaw([]byte(raw)); err == nil || strings.Contains(err.Error(), "YAML 1.1 reads") {
			t.Errorf("%q: parse error = %v, want the YAML decoder's refusal first", raw, err)
		}
	}
}

// An extractor name that loads as its text, such as 1 or 20.04, loads, and
// agree names it the same way.
func TestParseKeepsAnExtractorNameThatLoadsAsWritten(t *testing.T) {
	cfg, err := parseRaw([]byte("checks:\n- id: a\n  reason: r\n  scope: repo\n  extract: {1: {json: {file: f, path: a}}, 20.04: {json: {file: g, path: a}}}\n  agree: [1, 20.04]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Checks[0].Rule.Agree; len(got) != 2 || got[0] != "1" || got[1] != "20.04" {
		t.Fatalf("agree = %q, want [1 20.04]", got)
	}
}
