package rule

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// rec is a record of Gatus's push keys at line, its fields name and group
// each at their own line, the line after it for group.
func rec(line int, name, group string) *check.Data {
	return obj(line, "name", str(name, line), "group", str(group, line+1))
}

// recordsOf is an extractor named gatus that returns each of ds as a
// record with the fields name and group.
func recordsOf(ds ...*check.Data) check.NamedExtractor {
	x := mapsOf("gatus", ds...)
	x.Description = check.Description{Shape: check.ShapeRecord, Fields: []string{"name", "group"}}
	return x
}

// agree over name.field compares that field of each record, as a flat
// value at the field's own place, and the message names each at its line
// under name.field. A record without the field adds nothing. Planted
// breaks: the field's place taken from the record, which put group at
// the record's line; the whole record compared, as its text.
func TestAgreeComparesAFieldOfEachRecord(t *testing.T) {
	gatus := recordsOf(rec(3, "a", "external"), rec(7, "b", "external"), obj(11, "name", str("c", 11)))
	other := named("other", fake{values: []check.Value{at("external", "g.yaml", 1)}})
	c := agreeRule(gatus, other)
	c.Rule().Agree = []string{"gatus.group", "other"}
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue {
		t.Fatalf("verdict %s: %s", res.Verdict, res.Message)
	}

	gatus = recordsOf(rec(3, "a", "external"), rec(7, "b", "internal"))
	c = agreeRule(gatus, other)
	c.Rule().Agree = []string{"gatus.group", "other"}
	res := Run(context.Background(), "", c)
	want := "agree: the values differ\n" +
		`  "external" at renovate.json:4 (gatus.group), g.yaml:1 (other)` + "\n" +
		`  "internal" at renovate.json:8 (gatus.group)`
	if res.Verdict != check.VerdictFalse || res.Message != want {
		t.Fatalf("verdict %s message:\n%s\nwant:\n%s", res.Verdict, res.Message, want)
	}
	if len(res.Extracted) != 2 || res.Extracted[0].Extractor != "gatus.group" || res.Extracted[0].Values[1].Text != "internal" || res.Extracted[0].Values[1].Data != nil {
		t.Errorf("extracted %+v", res.Extracted)
	}
}

// Two fields of the same records can be compared with each other, and
// the extractor runs once. A field that holds a mapping, and records
// none of which has the field, make agree unknown, unless allow_empty
// lists the extractor, which leaves one value, true as for any extractor
// allow_empty lets come back empty. Planted breaks: the extractor run for
// each entry; a mapping in a field compared as its text; allow_empty read
// by the entry gatus.group, which made the one value unknown.
func TestAgreeOverFieldsIsUnknownWithoutAFlatValue(t *testing.T) {
	runs := 0
	counted := countingExtractor{values: []check.Value{whole(rec(3, "x", "x"))}, runs: &runs}
	c := agreeRule(check.NamedExtractor{Name: "gatus", Extractor: counted, Description: check.Description{Shape: check.ShapeRecord, Fields: []string{"name", "group"}}})
	c.Rule().Agree = []string{"gatus.name", "gatus.group"}
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue || runs != 1 {
		t.Fatalf("verdict %s after %d runs: %s", res.Verdict, runs, res.Message)
	}

	nested := recordsOf(obj(3, "name", obj(3, "k", str("v", 3)), "group", str("g", 4)), rec(7, "b", "g"))
	c = agreeRule(nested)
	c.Rule().Agree = []string{"gatus.name", "gatus.group"}
	res := Run(context.Background(), "", c)
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, `extractor "gatus": gatus[0] holds {"k": "v"} in field "name" at renovate.json:3, and agree compares flat values; write an assert`) {
		t.Fatalf("verdict %s message %s", res.Verdict, res.Message)
	}

	bare := recordsOf(obj(3, "name", str("a", 3)))
	other := named("other", fake{values: []check.Value{at("a", "g.yaml", 1)}})
	c = agreeRule(bare, other)
	c.Rule().Agree = []string{"gatus.group", "other"}
	res = Run(context.Background(), "", c)
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, `extractor "gatus" found no record with field "group"`) {
		t.Fatalf("verdict %s message %s", res.Verdict, res.Message)
	}
	c.Rule().AllowEmpty = []string{"gatus"}
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue {
		t.Fatalf("with allow_empty: verdict %s message %s", res.Verdict, res.Message)
	}
}

// countingExtractor returns values and counts its runs.
type countingExtractor struct {
	values []check.Value
	runs   *int
}

func (c countingExtractor) Extract(context.Context, string) ([]check.Value, error) {
	*c.runs++
	return c.values, nil
}

func (c countingExtractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return c.Extract(ctx, root)
}

// assert reads a record as a map, a field by its name, and two fields of
// one record together: the landingpage push keys, each <group>_<name> of a
// Gatus endpoint. A key no record makes is an erratum whose message shows
// that key at its line, and under --all-values each record at its line.
// Planted break: the records handed over flat, which made r.group a type
// error.
func TestAssertReadsAFieldOfEachRecord(t *testing.T) {
	expr := "pushed.all(k, gatus.exists(r, r.group + '_' + r.name == k))"
	a, err := compileNamed(expr, []string{"gatus", "pushed"}, []string{"gatus"})
	if err != nil {
		t.Fatal(err)
	}
	gatus := recordsOf(rec(3, "landingpage-registered", "external"), rec(7, "landingpage-queue", "external"))
	pushed := func(keys ...string) check.NamedExtractor {
		var values []check.Value
		for i, k := range keys {
			values = append(values, at(k, "monitoring.py", i+1))
		}
		return named("pushed", fake{values: values})
	}
	c := check.Check{ID: "r", Reason: "r", Scope: check.ScopeRepo, Kind: &check.Rule{
		Extractors: []check.NamedExtractor{gatus, pushed("external_landingpage-registered", "external_landingpage-queue")}, Assert: expr, Compiled: a}}
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue {
		t.Fatalf("verdict %s: %s", res.Verdict, res.Message)
	}
	c.Rule().Extractors[1] = pushed("external_landingpage-registered", "external_page-queue")
	res := Run(context.Background(), "", c)
	if res.Verdict != check.VerdictFalse || strings.Contains(res.Message, `gatus[1]`) ||
		!strings.Contains(res.Message, `pushed[1] = "external_page-queue" at monitoring.py:2`) {
		t.Fatalf("verdict %s message:\n%s", res.Verdict, res.Message)
	}
	res = Run(check.WithAllValues(context.Background()), "", c)
	if res.Verdict != check.VerdictFalse || !strings.Contains(res.Message, `gatus[1] = {"name": "landingpage-queue", "group": "external"} at renovate.json:7`) {
		t.Fatalf("--all-values: verdict %s message:\n%s", res.Verdict, res.Message)
	}
}

// A field the config names that no record holds makes assert unknown, as
// it makes agree, since !has(r.f) || ... would then hold on every record:
// a path that misses, or a key the file renamed. One record holding the
// field is enough, and allow_empty on the extractor lets the field come
// back empty. Planted breaks: the check left out of assert; the field
// sought in the first record only; allow_empty not read.
func TestAssertIsUnknownOnAFieldNoRecordHolds(t *testing.T) {
	expr := "gatus.all(r, !has(r.group) || r.group == 'external')"
	a, err := compileNamed(expr, []string{"gatus"}, []string{"gatus"})
	if err != nil {
		t.Fatal(err)
	}
	run := func(allow []string, ds ...*check.Data) check.Result {
		c := check.Check{ID: "r", Reason: "r", Scope: check.ScopeRepo, Kind: &check.Rule{
			Extractors: []check.NamedExtractor{recordsOf(ds...)}, Assert: expr, Compiled: a, AllowEmpty: allow}}
		return Run(context.Background(), "", c)
	}
	nameOnly := func(line int, name string) *check.Data { return obj(line, "name", str(name, line)) }
	res := run(nil, nameOnly(3, "a"), nameOnly(7, "b"))
	if res.Verdict != check.VerdictUnknown || res.Message != `extractor "gatus" found no record with field "group"` {
		t.Fatalf("verdict %s message:\n%s", res.Verdict, res.Message)
	}
	if res := run(nil, nameOnly(3, "a"), rec(7, "b", "internal")); res.Verdict != check.VerdictFalse {
		t.Fatalf("one record holds the field: verdict %s: %s", res.Verdict, res.Message)
	}
	if res := run([]string{"gatus"}, nameOnly(3, "a"), nameOnly(7, "b")); res.Verdict != check.VerdictTrue {
		t.Fatalf("allow_empty: verdict %s: %s", res.Verdict, res.Message)
	}
}

// cel.bind binds a record's field as it binds a map's value: a field
// compared with a number does not load, and one compared with a string
// runs over each record. Planted break: mapLiterals' refusals dropped,
// which let u == 1 load and never match. The refusal reads u's type, dyn,
// so it holds without following the bind.
func TestAssertBindsAFieldOfARecord(t *testing.T) {
	if _, err := compileNamed("gatus.all(e, cel.bind(u, e.name, u == 1))", []string{"gatus"}, []string{"gatus"}); err == nil || !strings.Contains(err.Error(), "is compared") {
		t.Errorf("a bound field compared with 1: %v", err)
	}
	expr := "gatus.all(e, cel.bind(u, e.name, u.startsWith('landingpage-')))"
	a, err := compileNamed(expr, []string{"gatus"}, []string{"gatus"})
	if err != nil {
		t.Fatal(err)
	}
	c := check.Check{ID: "r", Reason: "r", Scope: check.ScopeRepo, Kind: &check.Rule{
		Extractors: []check.NamedExtractor{recordsOf(rec(3, "landingpage-registered", "external"), rec(7, "landingpage-queue", "external"))}, Assert: expr, Compiled: a}}
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue {
		t.Fatalf("verdict %s: %s", res.Verdict, res.Message)
	}
}

// fielded is two extractors whose records have fields, e with name and
// url and g with name and uri, and one named m with as: map.
func fielded() []check.NamedExtractor {
	return []check.NamedExtractor{
		{Name: "e", Description: check.Description{Shape: check.ShapeRecord, Fields: []string{"name", "url"}}},
		{Name: "g", Description: check.Description{Shape: check.ShapeRecord, Fields: []string{"name", "uri"}}},
		{Name: "m", Description: check.Description{Shape: check.ShapeMap}},
	}
}

// A field the records do not have, misspelt in the expression, does not
// load, as a field the config misspells makes the rule unknown: a read, a
// has(), an in and a literal index, through a comprehension variable over
// the extractor or over its filter(), a cel.bind name bound to it or to a
// member, and an index into it. The refusal is where CEL places the read,
// at its dot, at has( or at the quoted name, and names the fields the
// records have, once for each extractor. A field they have, a map
// extractor's key, a literal's key, a read through by_file and a read of
// what map() computes load. Planted breaks: the pass left out of
// compileWith; a variable over the extractor not traced to its member; any
// index into the extractor not taken as a member; the extractor named once
// for each source; a read with no extractor to name refused; filter() not
// followed; map(x, x.name) followed as if it kept x.
func TestAssertRefusesAFieldTheRecordsDoNotHave(t *testing.T) {
	const (
		lacks  = `the records of extractor "e" have no field "ulr"; their fields are name, url`
		suffix = " && size(m) + size(g) >= 0"
	)
	for expr, want := range map[string]string{
		// The review's probes P2 and P3 (records-review-delta1.md).
		"e.all(x, !has(x.ulr) || x.ulr == 'nope')":   "assert: " + lacks + " (read 1 more time), at line 1 column 14 of the expression:\n    e.all(x, !has(x.ulr) || x.ulr == 'nope')" + suffix + "\n                 ^",
		"e.all(x, !('ulr' in x) || x.ulr == 'nope')": lacks + " (read 1 more time), at line 1 column 12",
		// Later reads are counted, not refused again: assert-delta4.md H2.
		"e.all(x, x.ulr == 'a' && has(x.ulr) && x['ulr'] != '')":  lacks + " (read 2 more times), at line 1 column 11",
		"e.all(x, x['ulr'] == 'a')":                               lacks + ", at line 1 column 12",
		"e[0].ulr == 'a'":                                         lacks,
		"e[size(e) - 1].ulr == 'a'":                               lacks,
		"cel.bind(xs, e, xs.all(x, has(x.ulr)))":                  lacks,
		"e.all(x, cel.bind(r, x, has(r.ulr)))":                    lacks,
		"cel.bind(r, e[0], r.ulr == 'a')":                         lacks,
		"e.exists(x, e.all(y, x.name == y.ulr))":                  lacks,
		"e.filter(x, x.name != '').all(y, !has(y.ulr))":           lacks,
		"e.map(x, x).exists(y, y.ulr == 'a')":                     lacks,
		"cel.bind(f, e.filter(x, x.name != ''), f[0].ulr == 'a')": lacks,
		"[e, g].filter(l, true).all(l2, l2.all(x, has(x.ulr)))":   lacks,
		"[e[0], g[0]].all(x, has(x.ulr))":                         lacks + `; the records of extractor "g" have no field "ulr"; their fields are name, uri`,
	} {
		_, err := Compile(expr+suffix, fielded())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", expr, err, want)
		}
	}
	for expr, n := range map[string]int{
		"e.all(x, has(x.ulr) && x.ulr == 'a')": 1,
		"[e[0], e[1]].all(x, has(x.ulr))":      1,
	} {
		if _, err := Compile(expr+suffix, fielded()); err == nil || strings.Count(err.Error(), lacks) != n {
			t.Errorf("%s: want %d refusals: %v", expr, n, err)
		}
	}
	for _, expr := range []string{
		"e.all(x, !has(x.url) || x.url != 'nope' && 'name' in x && x['name'] != '' && e[0].name != '') && size(m) >= 0",
		"m.all(x, !has(x.ulr) || 'ulr' in x || x['ulr'] == '') && size(e) >= 0",
		"[e[0], {'ulr': 'x'}].exists(x, has(x.ulr)) && size(m) >= 0",
		"cel.bind(want, {'ulr': 'x'}, e.exists(x, x.url == want.ulr)) && size(m) >= 0",
		"by_file(e)['f'].all(r, !has(r.ulr)) && size(m) >= 0",
		"{'k': e[0], 'n': 1}.z.ulr == '' && size(m) >= 0",
		"e.map(x, x.name).all(y, !has(y.ulr)) && size(m) >= 0",
		"[e[0], g[0]].all(x, has(x.name)) && size(m) >= 0",
	} {
		if _, err := Compile(expr+" && size(g) >= 0", fielded()); err != nil {
			t.Errorf("%s: %v", expr, err)
		}
	}
}

// A config may declare many fields, and an expression may misspell one many
// times: the field is refused once, at its first read, and the message lists
// the declared fields up to check.ShownBytes and says how many more there
// are, so neither the load nor the error grows with the two together. With
// a message for each read, each naming every field, 100000 fields and 780
// reads made a 620 MB error in 1.2 s. Planted breaks: every declared field
// listed; each read refused.
func TestAFieldRefusalStaysSmallOverManyFields(t *testing.T) {
	declared := make([]string, 10000)
	for i := range declared {
		declared[i] = "f" + strconv.Itoa(i)
	}
	xs := []check.NamedExtractor{{Name: "e", Description: check.Description{Shape: check.ShapeRecord, Fields: declared}}}
	expr := "e.all(x, [" + strings.TrimSuffix(strings.Repeat("x.zz, ", 300), ", ") + "].size() > 0)"
	start := time.Now()
	_, err := Compile(expr, xs)
	took := time.Since(start)
	if err == nil || strings.Count(err.Error(), `have no field "zz"`) != 1 || !strings.Contains(err.Error(), "f0, f1, f2") || !strings.Contains(err.Error(), " more (read 299 more times), at line 1 column 12") {
		t.Fatalf("one refusal with the fields cut: %.400v", err)
	}
	if n := len(err.Error()); n > 8<<10 || took > time.Second {
		t.Errorf("the error is %d bytes and the load took %v", n, took)
	}
}
