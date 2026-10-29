package rule

import (
	"context"
	"strings"
	"testing"

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
	x.Fields = []string{"name", "group"}
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
	c := agreeRule(check.NamedExtractor{Name: "gatus", Extractor: counted, Maps: true, Fields: []string{"name", "group"}})
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

func (c countingExtractor) Extract(string) ([]check.Value, error) {
	*c.runs++
	return c.values, nil
}

// assert reads a record as a map, a field by its name, and two fields of
// one record together: the landingpage push keys, each <group>_<name> of a
// Gatus endpoint. A key no record makes is an erratum whose message shows
// each record at its line. Planted break: the records handed over flat,
// which made r.group a type error.
func TestAssertReadsAFieldOfEachRecord(t *testing.T) {
	expr := "pushed.all(k, gatus.exists(r, r.group + '_' + r.name == k))"
	a, err := Compile(expr, []string{"gatus", "pushed"}, Lists{Maps: []string{"gatus"}})
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
	if res.Verdict != check.VerdictFalse || !strings.Contains(res.Message, `gatus[1] = {"name": "landingpage-queue", "group": "external"} at renovate.json:7`) ||
		!strings.Contains(res.Message, `pushed[1] = "external_page-queue" at monitoring.py:2`) {
		t.Fatalf("verdict %s message:\n%s", res.Verdict, res.Message)
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
	a, err := Compile(expr, []string{"gatus"}, Lists{Maps: []string{"gatus"}})
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
	if _, err := Compile("gatus.all(e, cel.bind(u, e.name, u == 1))", []string{"gatus"}, Lists{Maps: []string{"gatus"}}); err == nil || !strings.Contains(err.Error(), "is compared") {
		t.Errorf("a bound field compared with 1: %v", err)
	}
	expr := "gatus.all(e, cel.bind(u, e.name, u.startsWith('landingpage-')))"
	a, err := Compile(expr, []string{"gatus"}, Lists{Maps: []string{"gatus"}})
	if err != nil {
		t.Fatal(err)
	}
	c := check.Check{ID: "r", Reason: "r", Scope: check.ScopeRepo, Kind: &check.Rule{
		Extractors: []check.NamedExtractor{recordsOf(rec(3, "landingpage-registered", "external"), rec(7, "landingpage-queue", "external"))}, Assert: expr, Compiled: a}}
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue {
		t.Fatalf("verdict %s: %s", res.Verdict, res.Message)
	}
}
