package core_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/regex"
	"github.com/f-klement/errata/internal/extract/toml"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// A regex extractor with as: record returns one record for each match:
// its named groups as fields, in the order the pattern opens them, each at
// the line where its group starts, and the record at the line where the
// match starts. An unnamed group is no field, and a named group that took
// no part in a match is left out of that record. A CRLF file gives the
// same records. Planted breaks: every field at the match's line, which put
// group at line 2; the unnamed group kept, as a field ""; a group that
// took no part kept as "".
func TestRegexRecordReturnsEachMatchsNamedGroups(t *testing.T) {
	text := "  - name: a\n    group: g\n  - name: b # old\n    group: h\n"
	root := extracttest.Repo(t, map[string]string{"g.yaml": text, "crlf.yaml": strings.ReplaceAll(text, "\n", "\r\n")})
	pattern := `- name: (?P<name>\S+)( # (?P<note>\w+))?\n\s*(?:group): (?P<group>\S+)`
	for _, file := range []string{"g.yaml", "crlf.yaml"} {
		s := extract.Of(regex.Kind, regex.Spec{File: file, Pattern: pattern, As: "record"})
		got := mustRun(t, s, root)
		want := []struct{ text, lines string }{
			{`{"name": "a", "group": "g"}`, file + ":1 name=" + file + ":1 group=" + file + ":2"},
			{`{"name": "b", "note": "old", "group": "h"}`, file + ":3 name=" + file + ":3 note=" + file + ":3 group=" + file + ":4"},
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d records: %v", file, len(got), got)
		}
		for i, v := range got {
			if v.Data == nil || v.Text != want[i].text || v.Data.Show(1000) != want[i].text || v.At != v.Data.At {
				t.Errorf("%s: record %d is %q at %s, Data %v", file, i, v.Text, v.At, v.Data)
			} else if l := lines(v.Data); l != want[i].lines {
				t.Errorf("%s: record %d at %s, want %s", file, i, l, want[i].lines)
			}
		}
		if !s.Maps() || !slices.Equal(s.Fields(), []string{"name", "note", "group"}) {
			t.Errorf("Maps %v, Fields %v", s.Maps(), s.Fields())
		}
	}
	// A pattern that looks back, ^ or \b, is searched after a match from
	// the rune before it, which wraps the pattern in one more group; each
	// field keeps its own group's offsets.
	back := mustRun(t, extract.Of(regex.Kind, regex.Spec{File: "g.yaml", Pattern: `^\s*- name: (?P<name>\w+)\b.*\n\s*group: (?P<group>\S+)`, As: "record"}), root)
	if len(back) != 2 || back[1].Text != `{"name": "b", "group": "h"}` || lines(back[1].Data) != "g.yaml:3 name=g.yaml:3 group=g.yaml:4" {
		t.Errorf("a pattern that looks back: %v", back)
	}
	flat := extract.Of(regex.Kind, regex.Spec{File: "g.yaml", Pattern: pattern})
	if got := mustRun(t, flat, root); flat.Maps() || flat.Fields() != nil || len(got) != 2 || got[0].Data != nil || got[0].Text != "a" {
		t.Errorf("without as: %v", got)
	}
}

// as on a regex extractor takes record or nothing, and a record needs a
// named group whose name assert can read, each name once. Planted breaks:
// any as taken as record; the duplicate name kept, which gave a record
// the key x twice.
func TestRegexRecordRefusesWhatIsNoRecord(t *testing.T) {
	for pattern, want := range map[string]string{
		`(a)(b)`:              "regex: as: record reads the pattern's named groups, such as (?P<name>...), as the fields of each match's record, and the pattern names none",
		`(?P<1x>a)`:           `regex: as: record: the group name "1x" is no field name; a field name is a letter or _, then letters, digits and _`,
		`(?P<x>a)|(?P<x>b)`:   `regex: as: record: the pattern names two groups "x"; a record holds each field once`,
		`(?P<x>a)(?P<in>b)`:   `regex: as: record: the group name "in" is a word CEL reserves, which its language definition keeps out of field names, so assert may not read it as r.in; name the field otherwise`,
		`(?P<x>a)(?P<y>b)(c`:  "regex: pattern",
		`(?P<ok>a)(?P<x-y>b)`: "regex: pattern",
	} {
		_, err := extract.Build(extract.Of(regex.Kind, regex.Spec{File: "f", Pattern: pattern, As: "record"}))
		extracttest.WantError(t, err, want)
	}
	for _, as := range []string{"map", "records"} {
		s := extract.Of(regex.Kind, regex.Spec{File: "f", Pattern: `(?P<x>a)`, As: as})
		_, err := extract.Build(s)
		extracttest.WantError(t, err, fmt.Sprintf("regex: as %q is not one errata reads; write record to read each match's named groups into one record, or leave the key out for group 1", as))
		if s.Maps() || s.Fields() != nil {
			t.Errorf("as %s: Maps %v, Fields %v", as, s.Maps(), s.Fields())
		}
	}
}

// The records of a regex extractor count against MaxValues, each record
// and each field one node, and their field names and text against
// max_bytes, in one file and over the files of a set together. Planted
// breaks: the fields not counted, which returned 40000 records of three
// nodes; the names' bytes left out.
func TestRegexRecordBoundsNodesAndBytes(t *testing.T) {
	under := core.MaxValues / 3
	root := extracttest.Repo(t, map[string]string{
		"many": strings.Repeat("ab\n", under+1),
		"fits": strings.Repeat("ab\n", under),
		"long": strings.Repeat("ab\n", 10),
		"s/1":  strings.Repeat("ab\n", under/2+1),
		"s/2":  strings.Repeat("ab\n", under/2+1),
	})
	spec := func(file string, maxBytes int64) extract.Spec {
		return extract.Of(regex.Kind, regex.Spec{File: file, Pattern: `(?P<x>a)(?P<y>b)`, As: "record", MaxBytes: maxBytes})
	}
	if got := mustRun(t, spec("fits", 0), root); len(got) != under {
		t.Errorf("%d records of 3 nodes: %d", under, len(got))
	}
	_, err := run(t, spec("many", 0), root)
	extracttest.WantError(t, err, fmt.Sprintf("many: the records the pattern matches hold more than %d nodes in all, each record and each field one", core.MaxValues))
	// Each record holds x, a, y and b: 4 bytes, so 10 hold 40.
	if got := mustRun(t, spec("long", 40), root); len(got) != 10 {
		t.Errorf("40 bytes under 40: %d", len(got))
	}
	_, err = run(t, spec("long", 39), root)
	extracttest.WantError(t, err, "long: the records the pattern matches hold more than 39 bytes of field names and text in all, the extractor's max_bytes")
	_, err = run(t, extract.Of(regex.Kind, regex.Spec{Files: []string{"s/1", "s/2"}, Pattern: `(?P<x>a)(?P<y>b)`, As: "record"}), root)
	extracttest.WantError(t, err, fmt.Sprintf("regex: the files hold more than %d values, the limit, each node of a mapping one", core.MaxValues))
}

// Each of yaml, json and toml with as: record and fields returns one
// record for each node the path selects: each field the node its path
// selects from the element, whole, keys sorted by name, each node at its
// own line. A field whose path selects nothing is left out. Planted
// breaks: the fields in Go's map order; a field that selects nothing kept
// as an empty string; every field at the element's line.
func TestRecordReadsTheFieldsOfEachElement(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"e.yaml": "endpoints:\n  - name: a\n    url: http://loki\n    alerts:\n      - description: QUERY\n    tags: [x, y]\n  - name: b\n",
		"e.json": "{\"endpoints\": [\n  {\"name\": \"a\", \"url\": \"http://loki\",\n   \"alerts\": [{\"description\": \"QUERY\"}],\n   \"tags\": [\"x\", \"y\"]},\n  {\"name\": \"b\"}\n]}\n",
		"e.toml": "[[endpoints]]\nname = \"a\"\nurl = \"http://loki\"\ntags = [\"x\", \"y\"]\n[[endpoints.alerts]]\ndescription = \"QUERY\"\n[[endpoints]]\nname = \"b\"\n",
	})
	fields := map[string]string{"name": "name", "url": "url", "desc": "alerts[0].description", "tags": "tags", "missing": "nope.x"}
	for _, c := range []struct {
		spec  extract.Spec
		lines []string
	}{
		{extract.Of(yaml.Kind, yaml.Spec{File: "e.yaml", Path: "endpoints[*]", As: "record", Fields: fields}),
			[]string{"e.yaml:2 desc=e.yaml:5 name=e.yaml:2 tags=e.yaml:6 e.yaml:6 e.yaml:6 url=e.yaml:3", "e.yaml:7 name=e.yaml:7"}},
		{extract.Of(json.Kind, json.Spec{File: "e.json", Path: "endpoints[*]", As: "record", Fields: fields}),
			[]string{"e.json:2 desc=e.json:3 name=e.json:2 tags=e.json:4 e.json:4 e.json:4 url=e.json:2", "e.json:5 name=e.json:5"}},
		{extract.Of(toml.Kind, toml.Spec{File: "e.toml", Path: "endpoints[*]", As: "record", Fields: fields}),
			[]string{"e.toml:1 desc=e.toml:6 name=e.toml:2 tags=e.toml:4 e.toml:4 e.toml:4 url=e.toml:3", "e.toml:7 name=e.toml:8"}},
	} {
		got := mustRun(t, c.spec, root)
		want := []string{`{"desc": "QUERY", "name": "a", "tags": ["x", "y"], "url": "http://loki"}`, `{"name": "b"}`}
		if len(got) != len(want) {
			t.Fatalf("%s: %d records: %v", c.spec.Kind(), len(got), got)
		}
		for i, v := range got {
			if v.Data == nil || v.Text != want[i] || v.At != v.Data.At {
				t.Errorf("%s: record %d is %q at %s", c.spec.Kind(), i, v.Text, v.At)
			} else if l := lines(v.Data); l != c.lines[i] {
				t.Errorf("%s: record %d at %s, want %s", c.spec.Kind(), i, l, c.lines[i])
			}
		}
		if !c.spec.Maps() || !slices.Equal(c.spec.Fields(), []string{"desc", "missing", "name", "tags", "url"}) {
			t.Errorf("%s: Maps %v, Fields %v", c.spec.Kind(), c.spec.Maps(), c.spec.Fields())
		}
	}
	m := extract.Of(yaml.Kind, yaml.Spec{File: "e.yaml", Path: "endpoints[*]", As: "map"})
	if m.Fields() != nil {
		t.Errorf("as: map has fields %v", m.Fields())
	}
}

// fields goes with as: record and nothing else, as: record needs fields,
// a field's name must be one assert reads and its path must parse, and a
// field that selects more than one node in an element is refused at the
// element's line, and so is a scalar or a sequence that holds none of the
// fields, while a mapping that holds none is an empty record.
// Two fields over one path are refused, since agree over them would
// compare a node with itself (the review's probe F).
// Planted breaks: fields taken with as: map; a field that selects two nodes
// read as its first; two fields of one path accepted; a scalar or sequence
// with no field kept as an empty record; a mapping with no field refused.
func TestRecordRefusesWhatIsNoRecord(t *testing.T) {
	for _, c := range []struct {
		spec extract.Spec
		want string
	}{
		{extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a[*]", As: "map", Fields: map[string]string{"n": "n"}}),
			"yaml: fields takes as: record, which reads each field from the element the path selects into one record"},
		{extract.Of(json.Kind, json.Spec{File: "r.json", Path: "a[*]", Fields: map[string]string{"n": "n"}}),
			"json: fields takes as: record"},
		{extract.Of(toml.Kind, toml.Spec{File: "r.toml", Path: "a[*]", As: "record"}),
			"toml: as: record takes fields, a name for each path read from the element the path selects, such as fields: {name: name, url: url}"},
		{extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a[*]", As: "record", Fields: map[string]string{}}),
			"yaml: as: record takes fields"},
		{extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a[*]", As: "record", Fields: map[string]string{"a-b": "n"}}),
			`yaml: fields: "a-b" is no field name; a field name is a letter or _, then letters, digits and _, so assert reads it as r.name`},
		{extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a[*]", As: "record", Fields: map[string]string{"null": "n"}}),
			`yaml: fields: "null" is a word CEL reserves, which its language definition keeps out of field names, so assert may not read it as r.null; name the field otherwise`},
		{extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a[*]", As: "record", Fields: map[string]string{"n": "x[?"}}),
			`yaml: fields: n: path "x[?"`},
		{extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a[*]", As: "record", Fields: map[string]string{"second": "name", "first": "name", "url": "url"}}),
			`yaml: fields: "first" and "second" both read the path "name"; two fields of one path hold the same node, so agree over them would compare it with itself; name the path once`},
		{extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a[*]", As: "list"}),
			`yaml: as "list" is not one errata reads; write map to hand assert each mapping the path selects whole, record to read the fields of each element it selects into one, or leave the key out for its scalars`},
	} {
		_, err := extract.Build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
	root := extracttest.Repo(t, map[string]string{"r.yaml": "a:\n  - n: x\n  - n: y\n    t: [1, 2]\n"})
	_, err := run(t, extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a[*]", As: "record", Fields: map[string]string{"n": "n", "t": "t[*]"}}), root)
	extracttest.WantError(t, err, `r.yaml: path "a[*]": field "t" selects 2 nodes in the element at line 3; a field holds one value, so select one, such as [0]`)
	// A scalar or a sequence that holds none of the fields is no record a
	// rule could read, such as the string a ConfigMap's key holds before an
	// embedded step reads it. A mapping that holds none is an empty record,
	// since a field may be optional (the delta review's probes P1 and P7).
	empty := mustRun(t, extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a[*]", As: "record", Fields: map[string]string{"t": "t", "u": "u"}}), root)
	if len(empty) != 2 || empty[0].Text != "{}" || empty[0].At.Line != 2 || empty[1].Text != `{"t": ["1", "2"]}` {
		t.Errorf("a mapping that holds none of the fields: %v", empty)
	}
	_, err = run(t, extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a[1].t", As: "record", Fields: map[string]string{"n": "n"}}), root)
	extracttest.WantError(t, err, `selects a sequence at line 4, which holds none of the fields n`)
	scalar := extracttest.Repo(t, map[string]string{"c.yaml": "data:\n  config.yaml: |\n    url: x\n"})
	_, err = run(t, extract.Of(yaml.Kind, yaml.Spec{File: "c.yaml", Path: "data['config.yaml']", As: "record", Fields: map[string]string{"url": "url"}}), scalar)
	extracttest.WantError(t, err, `selects a scalar at line 2, which holds none of the fields url`)
	got := mustRun(t, extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a[*]", As: "record", Fields: map[string]string{"n": "n", "t": "t[0]", "self": "$"}}), root)
	if len(got) != 2 || got[1].Text != `{"n": "y", "self": {"n": "y", "t": ["1", "2"]}, "t": "1"}` {
		t.Errorf("got %v", got)
	}
}

// A record read in a document a string holds keeps the lines of the outer
// file, as Gatus's endpoints in a ConfigMap do, and the records of a file
// set are each at their own file. Planted break: a field's line taken from
// the document's own lines, which put url at line 2 of the file.
func TestRecordReadsEmbeddedDocumentsAndFileSets(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"g.yaml":   "data:\n  config.yaml: |\n    endpoints:\n      - name: a\n        url: http://loki\n",
		"s/1.json": `[{"n": "1"}]`,
		"s/2.json": `[{"n": "2"}]`,
	})
	got := mustRun(t, extract.Of(yaml.Kind, yaml.Spec{File: "g.yaml", Path: "data['config.yaml']", Embedded: steps("yaml", "endpoints[*]"),
		As: "record", Fields: map[string]string{"name": "name", "url": "url"}}), root)
	if len(got) != 1 || lines(got[0].Data) != "g.yaml:4 name=g.yaml:4 url=g.yaml:5" {
		t.Errorf("embedded: %v", got)
	}
	got = mustRun(t, extract.Of(json.Kind, json.Spec{Files: []string{"s/1.json", "s/2.json"}, Path: "[*]", As: "record", Fields: map[string]string{"n": "n"}}), root)
	if len(got) != 2 || lines(got[0].Data) != "s/1.json:1 n=s/1.json:1" || lines(got[1].Data) != "s/2.json:1 n=s/2.json:1" {
		t.Errorf("set: %v", got)
	}
}

// The records a path extractor returns count against MaxValues, the
// record and each field's nodes, and the bytes of the field names, keys
// and scalars against max_bytes. Planted breaks: the record itself not
// counted, which returned 50000 records of one field; the field names'
// bytes left out.
func TestRecordBoundsNodesAndBytes(t *testing.T) {
	half := core.MaxValues / 2
	list := func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat(`{"k": "v"}, `, n), ", ") + "]" }
	root := extracttest.Repo(t, map[string]string{"many.json": list(half + 1), "fits.json": list(half), "long.json": list(2)})
	spec := func(file string, maxBytes int64, name string) extract.Spec {
		return extract.Of(json.Kind, json.Spec{File: file, Path: "[*]", As: "record", Fields: map[string]string{name: "k"}, MaxBytes: maxBytes})
	}
	if got := mustRun(t, spec("fits.json", 0, "f"), root); len(got) != half {
		t.Errorf("%d records of 2 nodes: %d", half, len(got))
	}
	_, err := run(t, spec("many.json", 0, "f"), root)
	extracttest.WantError(t, err, fmt.Sprintf("many.json: the mappings the path selects hold more than %d nodes in all", core.MaxValues))
	// Each record holds a name of 100 bytes and the scalar v, so 2 hold
	// 202 bytes, more than the file's 24.
	name := strings.Repeat("f", 100)
	if got := mustRun(t, spec("long.json", 202, name), root); len(got) != 2 {
		t.Errorf("202 bytes under 202: %d", len(got))
	}
	_, err = run(t, spec("long.json", 201, name), root)
	extracttest.WantError(t, err, "long.json: the mappings the path selects hold more than 201 bytes of keys and scalars in all")
}

// A field name is none of the words CEL's language definition reserves,
// for a path record's fields and a regex record's groups alike, while a
// name that only starts or ends like one is a name. Planted break: in
// left out of the reserved words.
func TestFieldNamesAreNoWordCELReserves(t *testing.T) {
	for _, w := range []string{
		"as", "break", "const", "continue", "else", "false", "for", "function", "if", "import",
		"in", "let", "loop", "namespace", "null", "package", "return", "true", "var", "void", "while",
	} {
		_, err := core.CheckFields("yaml", "record", map[string]string{w: "n"})
		extracttest.WantError(t, err, fmt.Sprintf(`yaml: fields: %q is a word CEL reserves`, w))
		_, err = extract.Build(extract.Of(regex.Kind, regex.Spec{File: "f", Pattern: "(?P<" + w + ">a)", As: "record"}))
		extracttest.WantError(t, err, fmt.Sprintf(`the group name %q is a word CEL reserves`, w))
	}
	if _, err := core.CheckFields("yaml", "record", map[string]string{"input": "a", "as_of": "b", "Null": "c"}); err != nil {
		t.Errorf("names that only look like reserved words: %v", err)
	}
}
