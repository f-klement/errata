package report

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
)

func val(text, file string, line int) check.Value {
	return check.Value{Text: text, At: check.Location{File: file, Line: line}}
}

// agreeRule is a false agree rule over the extractors in ex, in that order,
// that blames the first value of ex. A test of another blame sets Blamed.
func agreeRule(id string, ex ...check.Extracted) check.Result {
	names := make([]string, len(ex))
	for i, e := range ex {
		names[i] = e.Extractor
	}
	return check.Result{
		Check:     check.Check{ID: id, Reason: "r", Scope: check.ScopeRepo, Kind: &check.Rule{Agree: names}},
		Verdict:   check.VerdictFalse,
		Message:   "agree: the values differ",
		Extracted: ex,
		Blamed:    &check.Blame{},
	}
}

var sarifSample = check.Report{Results: []check.Result{
	{Check: check.Check{ID: "ok", Scope: check.ScopeRepo, Reason: "holds"}, Verdict: check.VerdictTrue},
	agreeRule("ci-node",
		check.Extracted{Extractor: "readme", Values: []check.Value{val("atlas", "README.md", 12)}},
		check.Extracted{Extractor: "ci", Values: []check.Value{val("builder", ".gitlab-ci.yml", 4), val("builder", ".gitlab-ci.yml", 9)}},
	),
	{Check: check.Check{ID: "program", Scope: check.ScopeRepo, Reason: "CI moved.\nDocs must follow.", Kind: &check.Program{Run: []string{"false"}}}, Verdict: check.VerdictFalse, Message: "says atlas, runs on builder"},
	{Check: check.Check{ID: "broken", Scope: check.ScopeRepo, Reason: "why broken"}, Verdict: check.VerdictUnknown, Message: "program not found"},
	{Check: check.Check{ID: "owner", Scope: check.ScopeApplied, Reason: "r"}, Gap: "no provider can read and predict a live system"},
}}

// The shape of the output the tests read back. It is decoded from the
// written bytes, not taken from the writer's own types, so a field the
// writer names wrongly fails here.
type sarifOut struct {
	Schema  string `json:"$schema"`
	Version string `json:"version"`
	Runs    []struct {
		Tool struct {
			Driver struct {
				Name           string `json:"name"`
				Version        string `json:"version"`
				InformationURI string `json:"informationUri"`
				Rules          []struct {
					ID                   string                 `json:"id"`
					Name                 string                 `json:"name"`
					ShortDescription     struct{ Text string }  `json:"shortDescription"`
					FullDescription      struct{ Text string }  `json:"fullDescription"`
					Help                 struct{ Text string }  `json:"help"`
					DefaultConfiguration struct{ Level string } `json:"defaultConfiguration"`
					Properties           struct{ Scope string } `json:"properties"`
				} `json:"rules"`
			} `json:"driver"`
		} `json:"tool"`
		Invocations []struct {
			ExecutionSuccessful bool `json:"executionSuccessful"`
			Notifications       []struct {
				Level   string
				Message struct{ Text string }
				Rule    *struct {
					ID    string
					Index int
				} `json:"associatedRule"`
			} `json:"toolExecutionNotifications"`
		} `json:"invocations"`
		Results []struct {
			RuleID    string `json:"ruleId"`
			RuleIndex int    `json:"ruleIndex"`
			Level     string
			Message   struct{ Text string }
			Locations []outLocation `json:"locations"`
			Related   []outLocation `json:"relatedLocations"`
		} `json:"results"`
	} `json:"runs"`
}

type outLocation struct {
	ID       int `json:"id"`
	Physical *struct {
		Artifact struct {
			URI       string `json:"uri"`
			URIBaseID string `json:"uriBaseId"`
		} `json:"artifactLocation"`
		Region *struct {
			StartLine int `json:"startLine"`
		} `json:"region"`
	} `json:"physicalLocation"`
	Message struct{ Text string }
}

// where is a location as "file:line", the line 0 when there is no region,
// and "nowhere" when there is no physical location.
func (l outLocation) where() string {
	if l.Physical == nil {
		return "nowhere"
	}
	line := 0
	if l.Physical.Region != nil {
		line = l.Physical.Region.StartLine
	}
	return l.Physical.Artifact.URI + ":" + strconv.Itoa(line)
}

func writeSARIF(t fataler, rep check.Report) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := (SARIF{}).Write(&buf, rep); err != nil {
		t.Fatalf("write: %v", err)
	}
	return buf.Bytes()
}

func decodeSARIF(t fataler, b []byte) sarifOut {
	t.Helper()
	var out sarifOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, b)
	}
	if len(out.Runs) != 1 || len(out.Runs[0].Invocations) != 1 {
		t.Fatalf("want one run with one invocation:\n%s", b)
	}
	return out
}

func TestSARIFValidatesAgainstTheSchema(t *testing.T) {
	s := loadSchema(t)
	b := writeSARIF(t, sarifSample)
	if errs := s.validate(b); len(errs) > 0 {
		t.Fatalf("output breaks the SARIF 2.1.0 schema:\n%s\n%s", strings.Join(errs, "\n"), b)
	}
	if !bytes.HasSuffix(b, []byte("}\n")) {
		t.Errorf("output does not end with a newline")
	}
}

// Each planted break is one way a SARIF file goes wrong. The validator must
// refuse each with its own message, so a validator that passes everything
// cannot pass this test.
func TestSchemaValidatorRefusesPlantedBreaks(t *testing.T) {
	s := loadSchema(t)
	breaks := []struct {
		name  string
		plant func(doc map[string]any)
		want  string
	}{
		{"no version", func(d map[string]any) { delete(d, "version") }, `at "": missing required property "version"`},
		{"wrong version", func(d map[string]any) { d["version"] = "2.0.0" }, `at "/version": 2.0.0 is not one of [2.1.0]`},
		{"unknown property", func(d map[string]any) { d["extra"] = true }, `additional property "extra" is not allowed`},
		{"bad level", func(d map[string]any) { result(d)["level"] = "fatal" }, `at "/runs/0/results/0/level": fatal is not one of`},
		{"line 0", func(d map[string]any) { region(d)["startLine"] = json.Number("0") }, `startLine": 0 is less than the minimum 1`},
		{"fractional line", func(d map[string]any) { region(d)["startLine"] = json.Number("1.5") }, `number is not of type integer`},
		{"message without text", func(d map[string]any) { result(d)["message"] = map[string]any{} }, `at "/runs/0/results/0/message": anyOf: 0 of 2 alternatives hold`},
		{"uri with a space", func(d map[string]any) { artifact(d)["uri"] = "a b.md" }, `"a b.md" is not a uri-reference`},
		{"schema not a uri", func(d map[string]any) { d["$schema"] = "sarif.json" }, `"sarif.json" is not a uri`},
		{"rules repeated", func(d map[string]any) {
			drv := run(d)["tool"].(map[string]any)["driver"].(map[string]any)
			rules := drv["rules"].([]any)
			drv["rules"] = append(rules, rules[0])
		}, `/runs/0/tool/driver/rules": items 0 and 5 are equal, but uniqueItems is set`},
		{"no execution flag", func(d map[string]any) {
			delete(run(d)["invocations"].([]any)[0].(map[string]any), "executionSuccessful")
		}, `missing required property "executionSuccessful"`},
		{"results not an array", func(d map[string]any) { run(d)["results"] = "none" }, `at "/runs/0/results": string is not of type array`},
		{"trailing data", nil, `not JSON: data after the JSON value`},
	}
	clean := writeSARIF(t, sarifSample)
	for _, br := range breaks {
		t.Run(br.name, func(t *testing.T) {
			var doc map[string]any
			if err := decodeJSON(clean, &doc); err != nil {
				t.Fatal(err)
			}
			var b []byte
			if br.plant == nil {
				b = append(append([]byte{}, clean...), "{}"...)
			} else {
				br.plant(doc)
				var err error
				if b, err = json.Marshal(doc); err != nil {
					t.Fatal(err)
				}
			}
			errs := s.validate(b)
			if !strings.Contains(strings.Join(errs, "\n"), br.want) {
				t.Fatalf("planted break %q: want a refusal containing %q, got %q", br.name, br.want, errs)
			}
		})
	}
}

func run(d map[string]any) map[string]any { return d["runs"].([]any)[0].(map[string]any) }
func result(d map[string]any) map[string]any {
	return run(d)["results"].([]any)[0].(map[string]any)
}
func physicalOf(d map[string]any) map[string]any {
	return result(d)["locations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)
}
func region(d map[string]any) map[string]any { return physicalOf(d)["region"].(map[string]any) }
func artifact(d map[string]any) map[string]any {
	return physicalOf(d)["artifactLocation"].(map[string]any)
}

// The validator refuses a schema keyword it does not implement, since it
// would skip it and pass documents the schema refuses.
func TestSchemaValidatorRefusesUnknownKeyword(t *testing.T) {
	cases := map[string]string{
		`{"type": "object", "properties": {"a": {"maxLength": 3}}}`: `schema keyword "maxLength" at #/properties/a is not implemented`,
		`{"definitions": {"x": {"anyOf": [{"not": {}}]}}}`:          `schema keyword "not" at #/definitions/x/anyOf/0 is not implemented`,
		`{"items": {"format": "email"}}`:                            `schema format "email" at #/items is not implemented`,
	}
	for in, want := range cases {
		if _, err := compileSchema([]byte(in)); err == nil || err.Error() != want {
			t.Errorf("compileSchema(%s) = %v, want %q", in, err, want)
		}
	}
}

func TestSARIFRulesAreTheChecks(t *testing.T) {
	out := decodeSARIF(t, writeSARIF(t, sarifSample))
	drv := out.Runs[0].Tool.Driver
	if out.Version != "2.1.0" || out.Schema != "https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json" ||
		drv.Name != "errata" || drv.InformationURI != "https://github.com/f-klement/errata" {
		t.Fatalf("header = %q %q %q %q", out.Version, out.Schema, drv.Name, drv.InformationURI)
	}
	rules := out.Runs[0].Tool.Driver.Rules
	var ids []string
	for _, r := range rules {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, ","); got != "ok,ci-node,program,broken,owner" {
		t.Fatalf("rule ids = %s, want every check in report order", got)
	}
	p := rules[2]
	if p.Name != "program" || p.FullDescription.Text != "CI moved.\nDocs must follow." || p.Help.Text != p.FullDescription.Text || p.ShortDescription.Text != "CI moved." {
		t.Fatalf("rule program = %+v, want the reason as the full description and its first line as the short one", p)
	}
	if p.DefaultConfiguration.Level != "error" || p.Properties.Scope != "repo" || rules[4].Properties.Scope != "applied" {
		t.Fatalf("default level %q, scopes %q %q", p.DefaultConfiguration.Level, p.Properties.Scope, rules[4].Properties.Scope)
	}
}

func TestSARIFRuleDescriptions(t *testing.T) {
	at := strings.Repeat("é", descriptionLimit)
	cases := []struct {
		name, reason, short, full, help string
	}{
		{"empty reason falls back to the id", "  ", "c", "c", "c"},
		{"surrounding space trimmed", "\n  one\r\n two \n", "one", "one\r\n two", "one\r\n two"},
		{"exactly the limit is kept", at, at, at, at},
		{"one over the limit is cut", at + "x\nmore", at, at, at + "x\nmore"},
		{"a doubled brace is never split by the cut", strings.Repeat("a", descriptionLimit-1) + "{", strings.Repeat("a", descriptionLimit-1), strings.Repeat("a", descriptionLimit-1), strings.Repeat("a", descriptionLimit-1) + "{{"},
		{"a bracket and its word joiner are never split", strings.Repeat("a", descriptionLimit-1) + "](", strings.Repeat("a", descriptionLimit-1), strings.Repeat("a", descriptionLimit-1), strings.Repeat("a", descriptionLimit-1) + "]\u2060("},
		{"a bracket and its word joiner that fit are kept", strings.Repeat("a", descriptionLimit-2) + "](", strings.Repeat("a", descriptionLimit-2) + "]\u2060", strings.Repeat("a", descriptionLimit-2) + "]\u2060", strings.Repeat("a", descriptionLimit-2) + "]\u2060("},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := check.Report{Results: []check.Result{{Check: check.Check{ID: "c", Reason: c.reason}, Verdict: check.VerdictTrue}}}
			r := decodeSARIF(t, writeSARIF(t, rep)).Runs[0].Tool.Driver.Rules[0]
			if r.ShortDescription.Text != c.short || r.FullDescription.Text != c.full || r.Help.Text != c.help {
				t.Fatalf("short %q, full %q, help %q", r.ShortDescription.Text, r.FullDescription.Text, r.Help.Text)
			}
		})
	}
}

func TestSARIFOneRulePerID(t *testing.T) {
	twice := check.Result{Check: check.Check{ID: "dup", Reason: "first"}, Verdict: check.VerdictFalse}
	other := check.Result{Check: check.Check{ID: "other", Reason: "r"}, Verdict: check.VerdictFalse}
	second := twice
	second.Check.Reason = "second"
	out := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{twice, other, second}}))
	rules, results := out.Runs[0].Tool.Driver.Rules, out.Runs[0].Results
	if len(rules) != 2 || rules[0].FullDescription.Text != "first" {
		t.Fatalf("rules = %+v, want one per id, the first check's", rules)
	}
	if results[0].RuleIndex != 0 || results[1].RuleIndex != 1 || results[2].RuleIndex != 0 {
		t.Fatalf("rule indexes = %d %d %d, want 0 1 0", results[0].RuleIndex, results[1].RuleIndex, results[2].RuleIndex)
	}

	// "\x80" and "\x81" both encode as U+FFFD, so they are one rule, or
	// the rules would repeat in the file. FuzzSARIF found this.
	bad := check.Report{Results: []check.Result{{Check: check.Check{ID: "\x80"}, Verdict: check.VerdictTrue}, {Check: check.Check{ID: "\x81"}, Verdict: check.VerdictTrue}}}
	if rules := decodeSARIF(t, writeSARIF(t, bad)).Runs[0].Tool.Driver.Rules; len(rules) != 1 {
		t.Fatalf("ids \\x80 and \\x81 give %d rules, want 1", len(rules))
	}
}

func TestSARIFResultsAreTheErrata(t *testing.T) {
	out := decodeSARIF(t, writeSARIF(t, sarifSample))
	res := out.Runs[0].Results
	if len(res) != 2 || res[0].RuleID != "ci-node" || res[1].RuleID != "program" {
		t.Fatalf("results = %+v, want ci-node and program only", res)
	}
	if res[0].RuleIndex != 1 || res[1].RuleIndex != 2 || res[0].Level != "error" {
		t.Fatalf("result rule index %d %d, level %q", res[0].RuleIndex, res[1].RuleIndex, res[0].Level)
	}
	if res[1].Message.Text != "says atlas, runs on builder" {
		t.Fatalf("message = %q, want the check's message", res[1].Message.Text)
	}
	l := res[0].Locations[0].Physical.Artifact
	if l.URIBaseID != "%SRCROOT%" {
		t.Fatalf("uriBaseId = %q", l.URIBaseID)
	}
}

func TestSARIFProgramCheckPointsAtTheConfig(t *testing.T) {
	cases := []struct{ msg, want string }{
		{"", "check p found its claim false"},
		{" \n", "check p found its claim false"},
		{"said", "said"},
	}
	for _, c := range cases {
		msg, want := c.msg, c.want
		rep := check.Report{Results: []check.Result{{Check: check.Check{ID: "p", Reason: "r", Kind: &check.Program{Run: []string{"x"}}}, Verdict: check.VerdictFalse, Message: msg}}}
		res := decodeSARIF(t, writeSARIF(t, rep)).Runs[0].Results[0]
		if len(res.Locations) != 1 || res.Locations[0].where() != "errata.yaml:1" || len(res.Related) != 0 {
			t.Fatalf("locations %+v related %+v, want errata.yaml line 1 only", res.Locations, res.Related)
		}
		if res.Message.Text != want {
			t.Fatalf("message for %q = %q, want %q", msg, res.Message.Text, want)
		}
	}
}

// A loaded check carries the line its entry starts on, and a program check's
// result, or a rule's that found no value, points there: the file relative to
// the root, or a message naming it when the file is outside the repository.
func TestSARIFResultPointsAtTheCheck(t *testing.T) {
	program := func(at check.Location) check.Result {
		return check.Result{Check: check.Check{ID: "p", Reason: "r", Kind: &check.Program{Run: []string{"x"}}, At: at}, Verdict: check.VerdictFalse, Message: "said"}
	}
	noValue := program(check.Location{File: "ci/errata.yaml", Line: 9})
	noValue.Check.Kind = &check.Rule{Assert: "size(v) > 0"}
	noValue.Extracted = []check.Extracted{{Extractor: "v"}}
	cases := []struct {
		name  string
		res   check.Result
		where string
		text  string
	}{
		{"program check", program(check.Location{File: "errata.yaml", Line: 14}), "errata.yaml:14", "said"},
		{"another config file", program(check.Location{File: "ci/checks.yaml", Line: 3}), "ci/checks.yaml:3", "said"},
		{"line not known", program(check.Location{File: "errata.yaml"}), "errata.yaml:0", "said"},
		{"rule with no value", noValue, "ci/errata.yaml:9", "said"},
		{"outside the repository", program(check.Location{File: "/etc/errata.yaml", Line: 2}), "nowhere",
			"said\nThe check is in /etc/errata.yaml, outside the repository, so this result has no place in a file."},
		// A config given as ci.yaml from a directory outside the root: under
		// %SRCROOT% the same path would name another file.
		{"outside, by a relative path", program(check.Location{File: "ci.yaml", Line: 2, Outside: true}), "nowhere",
			"said\nThe check is in ci.yaml, outside the repository, so this result has no place in a file."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{c.res}})).Runs[0].Results[0]
			if len(res.Locations) != 1 || res.Locations[0].where() != c.where {
				t.Fatalf("locations %+v, want %s", res.Locations, c.where)
			}
			if res.Message.Text != c.text {
				t.Fatalf("message = %q, want %q", res.Message.Text, c.text)
			}
			if c.where == "nowhere" && res.Locations[0].Message.Text != "the check is in "+c.res.Check.At.File+", outside the repository" {
				t.Fatalf("location message = %q", res.Locations[0].Message.Text)
			}
		})
	}
}

// The driver names the errata that wrote the file, by the version the CLI
// passes in, and a build without one leaves the key out.
func TestSARIFCarriesTheVersion(t *testing.T) {
	var buf bytes.Buffer
	if err := (SARIF{Version: "v0.2.0"}).Write(&buf, sarifSample); err != nil {
		t.Fatal(err)
	}
	if got := decodeSARIF(t, buf.Bytes()).Runs[0].Tool.Driver.Version; got != "v0.2.0" {
		t.Fatalf("driver version = %q, want v0.2.0", got)
	}
	if strings.Contains(string(writeSARIF(t, sarifSample)), `"version": "v`) {
		t.Fatal("a SARIF with no version wrote one")
	}
	if got := Formats["sarif"]("v9.9.9"); got != (SARIF{Version: "v9.9.9"}) {
		t.Fatalf("Formats[sarif] = %+v, want the version passed in", got)
	}
}

// texts is one extractor per value, named e0, e1 and so on.
func texts(file string, vs ...string) []check.Extracted {
	out := make([]check.Extracted, len(vs))
	for i, v := range vs {
		out[i] = check.Extracted{Extractor: "e" + strconv.Itoa(i), Values: []check.Value{val(v, file, i+1)}}
	}
	return out
}

// The result points at the value the rule blames, counted across its
// extractors, and every other value is a related location. A rule that
// blames none, or a blame outside the values, puts the result at the check
// in the config with every value related. Planted break: the values of
// the extractors before the blamed one left uncounted, which pointed at
// e0.yaml:1 for e1.yaml:1.
func TestSARIFPointsAtTheBlamedValue(t *testing.T) {
	ex := []check.Extracted{
		{Extractor: "e0", Values: []check.Value{val("a", "e0.yaml", 1), val("a", "e0.yaml", 2)}},
		{Extractor: "empty"},
		{Extractor: "e1", Values: []check.Value{val("b", "e1.yaml", 1), val("c", "e1.yaml", 2)}},
	}
	for _, c := range []struct {
		name    string
		blamed  *check.Blame
		want    string
		related int
	}{
		{"the first value", &check.Blame{}, "e0.yaml:1", 3},
		{"within the first extractor", &check.Blame{Value: 1}, "e0.yaml:2", 3},
		{"after an empty extractor", &check.Blame{Extractor: 2}, "e1.yaml:1", 3},
		{"the last value", &check.Blame{Extractor: 2, Value: 1}, "e1.yaml:2", 3},
		{"none", nil, "errata.yaml:7", 4},
		{"an extractor with no value", &check.Blame{Extractor: 1}, "errata.yaml:7", 4},
		{"past the values", &check.Blame{Extractor: 2, Value: 2}, "errata.yaml:7", 4},
		{"past the extractors", &check.Blame{Extractor: 3}, "errata.yaml:7", 4},
		{"below zero", &check.Blame{Extractor: -1}, "errata.yaml:7", 4},
		{"a value below zero", &check.Blame{Value: -1}, "errata.yaml:7", 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := agreeRule("c", ex...)
			r.Check.At = check.Location{File: "errata.yaml", Line: 7}
			r.Blamed = c.blamed
			res := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{r}})).Runs[0].Results[0]
			if got := res.Locations[0].where(); got != c.want {
				t.Fatalf("primary location %s, want %s", got, c.want)
			}
			if len(res.Related) != c.related {
				t.Fatalf("%d related locations, want %d", len(res.Related), c.related)
			}
			for i, r := range res.Related {
				if r.ID != i+1 || r.Physical.Artifact.URIBaseID != "%SRCROOT%" {
					t.Fatalf("related %d = %+v, want id %d", i, r, i+1)
				}
			}
		})
	}
}

func TestSARIFRelatedLocationsListTheRest(t *testing.T) {
	res := decodeSARIF(t, writeSARIF(t, sarifSample)).Runs[0].Results[0]
	if got := res.Locations[0].where(); got != "README.md:12" {
		t.Fatalf("primary = %s, want README.md:12, the value the rule blames", got)
	}
	var got []string
	for _, r := range res.Related {
		got = append(got, r.where()+" "+r.Message.Text)
	}
	want := `.gitlab-ci.yml:4 "builder" (ci)` + "\n" + `.gitlab-ci.yml:9 "builder" (ci)`
	if strings.Join(got, "\n") != want {
		t.Fatalf("related:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
	// code scanning links a related location only from the message.
	wantMsg := "agree: the values differ\nThe other values, as [related locations]: [value 1](1), [value 2](2)."
	if res.Message.Text != wantMsg {
		t.Fatalf("message = %q, want %q", res.Message.Text, wantMsg)
	}
}

// An assert rule's result points at the value the rule blames, its first
// (rule.TestAssertBlamesTheFirstValue), and the others are related.
func TestSARIFAssertPointsAtTheFirstValue(t *testing.T) {
	r := check.Result{
		Check:   check.Check{ID: "a", Reason: "r", Kind: &check.Rule{Assert: "x == y"}},
		Verdict: check.VerdictFalse,
		Blamed:  &check.Blame{Extractor: 1},
		Extracted: []check.Extracted{
			{Extractor: "empty"},
			{Extractor: "x", Values: []check.Value{val("1", "x.yaml", 3), val("1", "x.yaml", 5)}},
			{Extractor: "y", Values: []check.Value{val("2", "y.yaml", 7)}},
		},
	}
	res := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{r}})).Runs[0].Results[0]
	var related []string
	for _, l := range res.Related {
		related = append(related, l.where())
	}
	if res.Locations[0].where() != "x.yaml:3" || strings.Join(related, ",") != "x.yaml:5,y.yaml:7" {
		t.Fatalf("primary %s, related %v", res.Locations[0].where(), related)
	}
}

func TestSARIFLocation(t *testing.T) {
	cases := []struct {
		file string
		line int
		want string
	}{
		{"docs/a.md", 1, "docs/a.md:1"},
		{"docs/a.md", 0, "docs/a.md:0"},
		{"docs/a.md", -3, "docs/a.md:0"},
		{"ab:c.yaml", 2, "./ab:c.yaml:2"},
		{"dir/a b%c?#.md", 2, "dir/a%20b%25c%3F%23.md:2"},
		{"\xff\xfe.yaml", 2, "%FF%FE.yaml:2"},
		{"", 4, "errata.yaml:4"},
	}
	for _, c := range cases {
		r := check.Result{Check: check.Check{ID: "c", Reason: "r"}, Verdict: check.VerdictFalse, Blamed: &check.Blame{},
			Extracted: []check.Extracted{{Extractor: "e", Values: []check.Value{val("v", c.file, c.line)}}}}
		res := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{r}})).Runs[0].Results[0]
		if got := res.Locations[0].where(); got != c.want {
			t.Errorf("location of %q line %d = %s, want %s", c.file, c.line, got, c.want)
		}
	}
}

func TestSARIFNotifications(t *testing.T) {
	inv := decodeSARIF(t, writeSARIF(t, sarifSample)).Runs[0].Invocations[0]
	if inv.ExecutionSuccessful {
		t.Fatal("executionSuccessful is true with an unknown in the report")
	}
	if len(inv.Notifications) != 2 {
		t.Fatalf("notifications = %+v, want the unknown and the skip", inv.Notifications)
	}
	u, s := inv.Notifications[0], inv.Notifications[1]
	if u.Level != "error" || u.Message.Text != "check broken is unknown: program not found\nwhy: why broken" || u.Rule.ID != "broken" || u.Rule.Index != 3 {
		t.Fatalf("unknown notification = %+v", u)
	}
	if s.Level != "note" || s.Message.Text != "check owner skipped (applied): no provider can read and predict a live system" || s.Rule.ID != "owner" || s.Rule.Index != 4 {
		t.Fatalf("skip notification = %+v", s)
	}

	bare := check.Report{Results: []check.Result{{Check: check.Check{ID: "u"}, Verdict: check.VerdictUnknown}}}
	if got := decodeSARIF(t, writeSARIF(t, bare)).Runs[0].Invocations[0].Notifications[0].Message.Text; got != "check u is unknown" {
		t.Fatalf("unknown with no message or reason = %q", got)
	}
}

// When skips fail the run, a skip is an error notification and the
// invocation did not succeed, as with an unknown.
func TestSARIFSkipsFail(t *testing.T) {
	skip := sarifSample.Results[4]
	if !skip.Skipped() {
		t.Fatalf("sarifSample.Results[4] = %+v, want the skip", skip)
	}
	for _, fail := range []bool{false, true} {
		rep := check.Report{Results: []check.Result{sarifSample.Results[0], skip}, SkipsFail: fail}
		inv := decodeSARIF(t, writeSARIF(t, rep)).Runs[0].Invocations[0]
		level := map[bool]string{false: "note", true: "error"}[fail]
		if inv.ExecutionSuccessful == fail || len(inv.Notifications) != 1 || inv.Notifications[0].Level != level {
			t.Fatalf("SkipsFail %v: invocation = %+v, want executionSuccessful %v and one %s", fail, inv, !fail, level)
		}
	}
}

func TestSARIFCleanRun(t *testing.T) {
	b := writeSARIF(t, check.Report{Results: []check.Result{sarifSample.Results[0], sarifSample.Results[4]}})
	out := decodeSARIF(t, b)
	if !out.Runs[0].Invocations[0].ExecutionSuccessful {
		t.Fatal("executionSuccessful is false with no unknown")
	}
	if !bytes.Contains(b, []byte(`"results": []`)) {
		t.Fatalf("a clean run must still list results, as an empty array:\n%s", b)
	}
	if errs := loadSchema(t).validate(writeSARIF(t, check.Report{})); len(errs) > 0 {
		t.Fatalf("empty report breaks the schema: %v", errs)
	}
}

func genSARIFReport(t *rapid.T) check.Report {
	rep := genReport(t)
	for i := range rep.Results {
		r := &rep.Results[i]
		r.Check.ID = loadableID.Draw(t, "id")
		r.Check.Reason, r.Message = anyText.Draw(t, "reason"), anyText.Draw(t, "message")
		if r.Skipped() {
			r.Gap = "gap " + anyText.Draw(t, "gap")
		}
		if !rapid.Bool().Draw(t, "rule") {
			continue
		}
		r.Check.Kind = &check.Rule{Normalize: []check.Normalization{check.NormalizeTrim, check.NormalizeLower}}
		if rapid.Bool().Draw(t, "agree") {
			r.Check.Rule().Agree = []string{"x"}
		} else {
			r.Check.Rule().Assert = "x"
		}
		for range rapid.IntRange(0, 3).Draw(t, "extractors") {
			e := check.Extracted{Extractor: anyText.Draw(t, "ename")}
			for range rapid.IntRange(0, 4).Draw(t, "values") {
				e.Values = append(e.Values, check.Value{
					Text: anyText.Draw(t, "text"),
					At:   check.Location{File: anyPath.Draw(t, "file"), Line: rapid.IntRange(-2, 1<<20).Draw(t, "line")},
				})
			}
			for range rapid.IntRange(0, 2).Draw(t, "left out") {
				e.LeftOut = append(e.LeftOut, check.Value{
					Text:       anyText.Draw(t, "text"),
					At:         check.Location{File: anyPath.Draw(t, "file"), Line: rapid.IntRange(-2, 1<<20).Draw(t, "line")},
					NotLiteral: true,
				})
			}
			r.Extracted = append(r.Extracted, e)
		}
	}
	return rep
}

// heldWithLeftOut counts the rules in rep that hold with a value left out,
// each of which is a note notification.
func heldWithLeftOut(rep check.Report) int {
	n := 0
	for _, r := range rep.Results {
		if r.Verdict == check.VerdictTrue && len(leftOut(r)) > 0 {
			n++
		}
	}
	return n
}

// Whatever bytes a check or a repository file put into a report, the output
// is valid UTF-8 that validates against the SARIF schema, and it holds one
// result per erratum and one notification per unknown and per skip.
func TestPropertySARIFIsValidAndCountsAgree(t *testing.T) {
	s := loadSchema(t)
	rapid.Check(t, func(t *rapid.T) {
		rep := genSARIFReport(t)
		b := writeSARIF(t, rep)
		if !utf8.Valid(b) {
			t.Fatalf("output is not valid UTF-8")
		}
		if r := rawActedOn(b); r >= 0 {
			t.Fatalf("output holds %U raw, which a terminal acts on", r)
		}
		if errs := s.validate(b); len(errs) > 0 {
			t.Fatalf("output breaks the schema:\n%s", strings.Join(errs, "\n"))
		}
		out := decodeSARIF(t, b)
		_, skipped := rep.Coverage()
		inv := out.Runs[0].Invocations[0]
		held := heldWithLeftOut(rep)
		if len(out.Runs[0].Results) != len(rep.Errata()) || len(inv.Notifications) != len(rep.Unknowns())+skipped+held || inv.ExecutionSuccessful != (len(rep.Unknowns())+rep.FailingSkips() == 0) {
			t.Fatalf("%d results, %d notifications, successful %v; report has %d errata, %d unknown, %d skipped, %d held with values left out",
				len(out.Runs[0].Results), len(inv.Notifications), inv.ExecutionSuccessful, len(rep.Errata()), len(rep.Unknowns()), skipped, held)
		}
		for _, res := range out.Runs[0].Results {
			if len(res.Locations) != 1 || res.Message.Text == "" {
				t.Fatalf("result %+v needs one location and a message for code scanning", res)
			}
		}
		if problems := messageProblems(out); len(problems) > 0 {
			t.Fatalf("%s", strings.Join(problems, "\n"))
		}
	})
}

// FuzzSARIF builds one erratum of a shape the fuzzer picks: shape%4 is the
// relation (agree, assert, a check with no rule but values, a program check
// with none), and shape/4%4 is how many values it has. An unknown and a skip
// carry the same strings.
func FuzzSARIF(f *testing.F) {
	f.Add("ok", "message", "reason", "README.md", "v", 1, uint8(4*2))
	f.Add("x", "\x00\x1b]8;;", "\xff\xfe", "a:b c%.md", "\xc3\x28", 0, uint8(4*3+1))
	f.Add("x", "see [the fix](https://evil.example/login) {0}", "[r](1)", "../../etc/passwd", "[x](1) {{x}}", 2, uint8(4*3))
	f.Add("x", strings.Repeat("m", messageLimit+1), strings.Repeat("r", descriptionLimit+1), "//evil.example/x", strings.Repeat("v", valueLimit+1), 3, uint8(4*2+2))
	f.Add("x", "", "", "", "", -1, uint8(3))
	f.Add("x", actedOnRunes, "\xc2\x9b31m", "f", actedOnRunes, 1, uint8(4*2))
	s := loadSchema(f)
	f.Fuzz(func(t *testing.T, id, msg, reason, file, text string, line int, shape uint8) {
		var values []check.Value
		for i := range int(shape / 4 % 4) {
			values = append(values, val([]string{text, msg, reason}[i%3], file, line+i))
		}
		erratum := check.Result{
			Check:     check.Check{ID: id, Reason: reason},
			Verdict:   check.VerdictFalse,
			Message:   msg,
			Extracted: []check.Extracted{{Extractor: id, Values: values}},
		}
		switch shape % 4 {
		case 0:
			erratum.Check.Kind = &check.Rule{Agree: []string{id}}
		case 1:
			erratum.Check.Kind = &check.Rule{Assert: id}
		case 3:
			erratum.Check.Kind, erratum.Extracted = &check.Program{Run: []string{id}}, nil
		}
		rep := check.Report{Results: []check.Result{
			erratum,
			{Check: check.Check{ID: id, Reason: reason}, Verdict: check.VerdictUnknown, Message: msg},
			{Check: check.Check{ID: msg, Reason: reason}, Gap: text},
		}}
		b := writeSARIF(t, rep)
		if !utf8.Valid(b) {
			t.Fatalf("output is not valid UTF-8")
		}
		if r := rawActedOn(b); r >= 0 {
			t.Fatalf("output holds %U raw, which a terminal acts on", r)
		}
		if errs := s.validate(b); len(errs) > 0 {
			t.Fatalf("output breaks the schema:\n%s\n%s", strings.Join(errs, "\n"), b)
		}
		if problems := messageProblems(decodeSARIF(t, b)); len(problems) > 0 {
			t.Fatalf("%s\n%s", strings.Join(problems, "\n"), b)
		}
	})
}

// A value an extractor handed over whole (as: map) is named in a related
// location by its Shown form, which the rule gives it after its normalize
// (rule.TestAResultShowsAWholeValueAfterNormalize), not its text quoted a
// second time. A flat value beside it is still quoted. SARIF's plain text
// doubles each brace. Planted break: label quoting the text whatever the
// value, which wrote "{{\"a\": ...
func TestSARIFNamesAWholeValueAsTheMessageShowsIt(t *testing.T) {
	d := &check.Data{Kind: check.DataMap, Keys: []string{"a"}, Items: []*check.Data{{Kind: check.DataScalar, Text: " X "}}, At: check.Location{File: "r.json", Line: 3}}
	whole := check.Value{Text: d.Show(check.ShownBytes), At: d.At, Data: d, Shown: `{"a": "x"}`}
	res := check.Result{
		Blamed: &check.Blame{},
		Check: check.Check{ID: "c", Reason: "r", Scope: check.ScopeRepo, Kind: &check.Rule{
			Assert: "true", Normalize: []check.Normalization{check.NormalizeTrim, check.NormalizeLower}}},
		Verdict: check.VerdictFalse, Message: "assert: the expression is false",
		Extracted: []check.Extracted{{Extractor: "flat", Values: []check.Value{val("v", "f.yaml", 1)}}, {Extractor: "m", Values: []check.Value{whole}}},
	}
	out := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{res}})).Runs[0].Results[0]
	if len(out.Related) != 1 || out.Related[0].Message.Text != `{{"a": "x"}} (m)` {
		t.Fatalf("related %+v", out.Related)
	}
}
