package core_test

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	jsonreader "github.com/f-klement/errata/internal/extract/json"
	tomlreader "github.com/f-klement/errata/internal/extract/toml"
	yamlreader "github.com/f-klement/errata/internal/extract/yaml"
)

func TestBuildRefusesPathThatDoesNotParse(t *testing.T) {
	for kind, s := range map[string]extract.Spec{
		"yaml": extract.Of(yamlreader.Kind, yamlreader.Spec{File: "f", Path: "a..b"}),
		"json": extract.Of(jsonreader.Kind, jsonreader.Spec{File: "f", Path: "a..b"}),
		"toml": extract.Of(tomlreader.Kind, tomlreader.Spec{File: "f", Path: "a..b"}),
	} {
		_, err := extract.Build(s)
		extracttest.WantError(t, err, kind+`: path "a..b": column 3: recursive descent`)
	}
}

// RFC 9535's examples that fall in the subset, through the JSON reader: the
// member value comparison of Table 12, the comparisons with null of Table
// 17, and Table 11's type mismatches as filters. A filter selects a
// container here, so each path takes a scalar after it.
func TestFilterRFC9535Examples(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"t12.json": `{
  "a": [3, 5, 1, 2, 4, 6,
        {"b": "j"},
        {"b": "k"},
        {"b": {}},
        {"b": "kilo"}
       ],
  "o": {"p": 1, "q": 2, "r": 3, "s": 5, "t": {"u": 6}},
  "e": "f"
}`,
		"t17.json": `{"a": null, "b": [null], "c": [{}], "null": 1}`,
		"t11.json": `{"obj": {"x": "y"}, "arr": [2, 3]}`,
	})
	for _, c := range []struct{ file, path, want string }{
		{"t12.json", "$.a[?@.b == 'kilo'].b", "kilo@6"},
		{"t12.json", "$.a[?@.b == 'k'].b", "k@4"},
		{"t12.json", "$.a[?@.b == ''].b", ""}, // {"b": {}} holds a mapping, which equals no string
		{"t12.json", "$.a[?@ == 4]", "4@2"},
		{"t12.json", "$.o[?@ == 5]", "5@8"},
		{"t12.json", "$.o[?@.u == 6].u", "6@8"},
		{"t12.json", "$[?@ == 'f']", "f@9"},
		{"t17.json", "$.b[?@==null]", "null@1"},
		{"t17.json", "$.c[?@.d==null]", ""},
		{"t17.json", "$[?@ == 1]", "1@1"},
		{"t17.json", "$[?@ == null]", "null@1"},
		{"t11.json", "$.arr[?@ == '2']", ""}, // 13 == '13' is false
		{"t11.json", "$.arr[?@ == 2]", "2@1"},
		{"t11.json", "$[?@ == 17]", ""},         // $.obj == 17 is false
		{"t11.json", "$[?@.absent == 'g']", ""}, // $.absent == 'g' is false
		{"t11.json", "$[?@.x == 'y'].x", "y@1"},
	} {
		got := extracttest.Texts(mustRun(t, extract.Of(jsonreader.Kind, jsonreader.Spec{File: c.file, Path: c.path}), root))
		if got != c.want {
			t.Errorf("%s %s: got %q, want %q", c.file, c.path, got, c.want)
		}
	}
}

// A filter compares by the type the file's format gives a scalar. The
// planted break each row guards: a string that spells a number, a number
// spelled another way, a boolean that is a string, and a missing key that is
// not null.
func TestFilterComparesTypedValues(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"f.yaml": `ports:
  - {name: plain, port: 8080}
  - {name: quoted, port: "8080"}
  - {name: str, port: !!str 8080}
  - {name: hex, port: 0x1F90}
  - {name: float, port: 8080.0}
  - {name: exp, port: 8.08e3}
  - {name: under, port: 8_080}
  - {name: other, port: 8081}
flags:
  - {enabled: true, id: a}
  - {enabled: True, id: b}
  - {enabled: "true", id: c}
  - {enabled: ~, id: d}
  - {enabled: null, id: e}
  - {enabled: , id: f}
  - {id: g}
  - {enabled: false, id: h}
dates:
  - {d: 2026-10-17, id: a}
  - {d: "2026-10-17", id: b}
  - {d: 2026-10-17T00:00:00Z, id: c}
refs:
  - {v: !Ref api, id: a}
  - {v: !!binary aGk=, id: b}
`,
		"f.toml": `[[dep]]
name = "plain"
version = 8080
[[dep]]
name = "quoted"
version = "8080"
[[dep]]
name = "hex"
version = 0x1F90
[[dep]]
name = "float"
version = 8_080.0
[[dep]]
name = "when"
at = 1979-05-27T07:32:00Z
day = 1979-05-27
enabled = true
`,
		"f.json": `{"n": [
  {"id": "a", "v": 1000},
  {"id": "b", "v": 1e3},
  {"id": "c", "v": 1000.0},
  {"id": "d", "v": "1000"},
  {"id": "e", "v": -0},
  {"id": "f", "v": 12345678901234567890123},
  {"id": "g", "v": 9007199254740993},
  {"id": "h", "v": true},
  {"id": "i", "v": null},
  {"id": "z1", "v": 0e1000000000000000},
  {"id": "z2", "v": -0.0E-99999999999999999999},
  {"id": "z3", "v": 1e1000000000000000},
  {"id": "z4", "v": 10e999999999999999},
  {"id": "z5", "v": 0.001e-99999999999999999999999999999}
]}`,
		"z.yaml": "a: [{p: 0e1000000000000000, n: z1}, {p: -0.0E-99999999999999999999, n: z2}, {p: 0, n: z3}]\n",
		"z.toml": "a = [{p = 0e1000000000000000, n = 'z1'}, {p = -0.0E-99999999999999999999, n = 'z2'}, {p = 0.0, n = 'z3'}]\n",
	})
	for _, c := range []struct{ file, path, want string }{
		{"f.yaml", "ports[?@.port == 8080].name", "plain@2 hex@5 float@6 exp@7 under@8"},
		{"f.yaml", "ports[?@.port == 8.08e3].name", "plain@2 hex@5 float@6 exp@7 under@8"},
		{"f.yaml", "ports[?@.port == '8080'].name", "quoted@3 str@4"},
		{"f.yaml", "flags[?@.enabled == true].id", "a@11 b@12"},
		{"f.yaml", "flags[?@.enabled == 'true'].id", "c@13"},
		{"f.yaml", "flags[?@.enabled == null].id", "d@14 e@15 f@16"},
		{"f.yaml", "flags[?@.enabled == false].id", "h@18"},
		{"f.yaml", "dates[?@.d == '2026-10-17'].id", "a@20 b@21"},
		{"f.yaml", "dates[?@.d == '2026-10-17T00:00:00Z'].id", "c@22"},
		{"f.yaml", "refs[?@.v == 'api'].id", "a@24"},
		{"f.yaml", "refs[?@.v == 'aGk='].id", "b@25"},
		{"f.toml", "dep[?@.version == 8080].name", "plain@2 hex@8 float@11"},
		{"f.toml", "dep[?@.version == '8080'].name", "quoted@5"},
		{"f.toml", "dep[?@.at == '1979-05-27T07:32:00Z'].name", "when@14"},
		{"f.toml", "dep[?@.day == '1979-05-27'].name", "when@14"},
		{"f.toml", "dep[?@.enabled == true].name", "when@14"},
		{"f.toml", "dep[?@.enabled == 'true'].name", ""},
		{"f.json", "n[?@.v == 1000].id", "a@2 b@3 c@4"},
		{"f.json", "n[?@.v == '1000'].id", "d@5"},
		{"f.json", "n[?@.v == 12345678901234567890123].id", "f@7"},
		{"f.json", "n[?@.v == 12345678901234567890124].id", ""},
		{"f.json", "n[?@.v == 9007199254740993].id", "g@8"},
		{"f.json", "n[?@.v == 9007199254740992].id", ""},
		{"f.json", "n[?@.v == true].id", "h@9"},
		{"f.json", "n[?@.v == 'true'].id", ""},
		{"f.json", "n[?@.v == null].id", "i@10"},
		{"f.json", "n[?@.v == 'null'].id", ""},
		{"f.json", "n[?@.v == 0].id", "e@6 z1@11 z2@12"},
		{"f.json", "n[?@.v == 1e1000000000000000].id", "z3@13 z4@14"},
		{"f.json", "n[?@.v == 1e1000000000000001].id", ""},
		{"f.json", "n[?@.v == 0.1e1000000000000001].id", "z3@13 z4@14"},
		{"f.json", "n[?@.v == 1e-100000000000000000000000000002].id", "z5@15"},
		{"f.json", "n[?@.v == 1e-100000000000000000000000000001].id", ""},
		{"z.yaml", "a[?@.p == 0].n", "z1@1 z2@1 z3@1"},
		{"z.toml", "a[?@.p == 0].n", "z1@1 z2@1 z3@1"},
	} {
		var spec extract.Spec
		switch filepath.Ext(c.file) {
		case ".yaml":
			spec = extract.Of(yamlreader.Kind, yamlreader.Spec{File: c.file, Path: c.path})
		case ".json":
			spec = extract.Of(jsonreader.Kind, jsonreader.Spec{File: c.file, Path: c.path})
		default:
			spec = extract.Of(tomlreader.Kind, tomlreader.Spec{File: c.file, Path: c.path})
		}
		if got := extracttest.Texts(mustRun(t, spec, root)); got != c.want {
			t.Errorf("%s %s: got %q, want %q", c.file, c.path, got, c.want)
		}
	}
}

// A filter selects members of a sequence and values of a mapping in the
// file's order, and only where every comparison holds.
func TestFilterSelects(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": `servers:
  zeta: {listen: 80, server_name: z.example}
  alpha: {listen: 443, server_name: a.example}
  mid: {listen: 80, server_name: m.example}
spec:
  template:
    spec:
      containers:
        - name: api
          env: prod
          image: api:2
          args: [--port, "8080"]
          labels: {app.kubernetes.io/name: api}
        - name: worker
          env: prod
          image: worker:2
          args: [--queue]
        - name: api
          env: dev
          image: api:3
          args: [--port, "9090"]
ports: [80, 443, 80]
name: x
`})
	for path, want := range map[string]string{
		"servers[?@.listen == 80].server_name":                                              "z.example@2 m.example@4",
		"servers[?@.listen == 80 && @.server_name == 'm.example'].listen":                   "80@4",
		"spec.template.spec.containers[?@.name == 'api'].image":                             "api:2@11 api:3@20",
		"spec.template.spec.containers[?@.name == 'api' && @.env == 'dev'].image":           "api:3@20",
		"spec.template.spec.containers[?'prod' == @.env].name":                              "api@9 worker@14",
		"spec.template.spec.containers[?@.args[0] == '--port'].image":                       "api:2@11 api:3@20",
		"spec.template.spec.containers[?@.args[-1] == '--queue'].image":                     "worker:2@16",
		"spec.template.spec.containers[?@.args[1] == 8080].image":                           "",
		"spec.template.spec.containers[?@.labels['app.kubernetes.io/name'] == 'api'].image": "api:2@11",
		"spec.template.spec.containers[?@.missing == 'x'].image":                            "",
		"spec.template.spec.containers[?@.args == '--port'].image":                          "",
		"spec.template.spec.containers[?@.name == 'api'][?@ == 'prod']":                     "prod@10",
		"ports[?@ == 80]": "80@22 80@22",
		"name[?@ == 'x']": "",
	} {
		if got := extracttest.Texts(mustRun(t, yamlSpec(path), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// A filter compares the node an alias names, with its type.
func TestFilterThroughAliases(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": `base: &b {name: api, port: 8080}
list:
  - *b
  - {<<: *b, name: other}
  - {name: api, port: "8080"}
`})
	if got := extracttest.Texts(mustRun(t, yamlSpec("list[?@.port == 8080].name"), root)); got != "api@1 other@4" {
		t.Errorf("got %q", got)
	}
}

func TestBuildRefusesFilterThatDoesNotParse(t *testing.T) {
	for kind, s := range map[string]extract.Spec{
		"yaml": extract.Of(yamlreader.Kind, yamlreader.Spec{File: "f", Path: "a[?@.b != 1]"}),
		"json": extract.Of(jsonreader.Kind, jsonreader.Spec{File: "f", Path: "a[?@.b != 1]"}),
		"toml": extract.Of(tomlreader.Kind, tomlreader.Spec{File: "f", Path: "a[?@.b != 1]"}),
	} {
		_, err := extract.Build(s)
		extracttest.WantError(t, err, kind+`: path "a[?@.b != 1]": column 8: operator != is not supported in a filter; only == is`)
	}
}

// A number scalar is read once, however many aliases name it: the planted
// break is reading it again on every visit, which cost 10000 aliases of a
// 1 MB number 10 GB of allocation and 80 s. Here 200 aliases of a number of
// 64 KiB, which the break reads 200 times.
func TestFilterReadsANumberOnce(t *testing.T) {
	filterReadsANumberOnce(t, 1<<16)
}

// The same with a number of 1 MiB, which -race slows past make test's time.
func TestFilterReadsALargeNumberOnce(t *testing.T) {
	slowOnly(t)
	filterReadsANumberOnce(t, 1<<20)
}

// filterReadsANumberOnce filters 200 aliases of a number of n digits, and
// allows the allocation of reading it about once: 8 times n.
func filterReadsANumberOnce(t *testing.T, n int) {
	t.Helper()
	data := "b: &b 1." + strings.Repeat("0", n) + "1\nrefs: [" + strings.Repeat("*b, ", 199) + "*b]\n"
	docs, err := yamlreader.Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	p, err := core.ParsePath("refs[?@ == 2]")
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := p.Eval(docs...)
	runtime.ReadMemStats(&after)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d nodes, %v", len(got), err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8*uint64(n) {
		t.Errorf("200 aliases of a number of %d digits allocated %d bytes; it is read once, about %d", n, grew, n)
	}
}

// [*] and a filter take a mapping's values from its ordered list, never by
// looking a key up: a lookup hashes the whole key, and aliases of a mapping
// with long keys would make every visit cost their length. The planted
// break is a lookup, which finds nothing once the hash map is gone.
func TestMembersAreNotLookedUp(t *testing.T) {
	for format, data := range map[string]string{
		"yaml": "m: {b: 1, a: 2, c: 1}\n",
		"json": `{"m": {"b": 1, "a": 2, "c": 1}}`,
		"toml": "m = {b = 1, a = 2, c = 1}\n",
	} {
		var root *core.Node
		var err error
		switch format {
		case "yaml":
			var docs []*core.Node
			docs, err = yamlreader.Parse([]byte(data))
			if err == nil {
				root = docs[0]
			}
		case "json":
			root, err = jsonreader.Parse([]byte(data))
		default:
			root, err = tomlreader.Parse([]byte(data))
		}
		if err != nil {
			t.Fatal(err)
		}
		m := root.Field["m"]
		want := []*core.Node{m.Field["b"], m.Field["a"], m.Field["c"]}
		m.Field = nil
		for path, want := range map[string][]*core.Node{"m[*]": want, "m[?@ == 1]": {want[0], want[2]}} {
			p, err := core.ParsePath(path)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := p.Eval(root); err != nil || !slices.Equal(got, want) {
				t.Errorf("%s %s: got %v, %v", format, path, got, err)
			}
		}
	}
}

// Membership, existence and nesting, on each format, through the
// extractors. Each file holds the same rules list, which has the shape of
// Renovate's packageRules, and the same egress list, which has the shape of
// a NetworkPolicy's. The planted breaks each row guards: a member of a list
// that is only a prefix of the value, a key that is present only in another
// member, a value that is null where a key must exist, and a port that
// stands in a sibling of the list the inner filter reads.
func TestFilterQueriesOnEachFormat(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"f.yaml": `rules:
  - {id: a, datasources: [docker, helm], names: [python], types: [minor], schedule: ["* * * 3-5 *"]}
  - {id: b, datasources: [dockerfile], names: [alpine/k8s], allowed: "<1.38"}
  - {id: c, datasources: [docker], names: [alpine/k8s], enabled: false}
  - {id: d, datasources: [helm], age: 3 days, names: [python], types: [minor, patch]}
  - {id: e, datasources: [docker], age: null}
egress:
  - {name: dns, ports: [{port: 53}], to: [{cidr: 10.0.0.10/32}]}
  - {name: api, ports: [{port: 443}, {port: 6443}], to: [{cidr: 203.0.113.5/32}]}
  - {name: decoy, port: 6443, ports: [{port: 80}], to: [{cidr: 10.9.9.9/32}]}
`,
		"f.json": `{
"rules": [
  {"id": "a", "datasources": ["docker", "helm"], "names": ["python"], "types": ["minor"], "schedule": ["* * * 3-5 *"]},
  {"id": "b", "datasources": ["dockerfile"], "names": ["alpine/k8s"], "allowed": "<1.38"},
  {"id": "c", "datasources": ["docker"], "names": ["alpine/k8s"], "enabled": false},
  {"id": "d", "datasources": ["helm"], "age": "3 days", "names": ["python"], "types": ["minor", "patch"]},
  {"id": "e", "datasources": ["docker"], "age": null}
],
"egress": [
  {"name": "dns", "ports": [{"port": 53}], "to": [{"cidr": "10.0.0.10/32"}]},
  {"name": "api", "ports": [{"port": 443}, {"port": 6443}], "to": [{"cidr": "203.0.113.5/32"}]},
  {"name": "decoy", "port": 6443, "ports": [{"port": 80}], "to": [{"cidr": "10.9.9.9/32"}]}
]}
`,
		// TOML has no null: rule e's age is the empty string instead.
		"f.toml": `rules = [
  {id = "a", datasources = ["docker", "helm"], names = ["python"], types = ["minor"], schedule = ["* * * 3-5 *"]},
  {id = "b", datasources = ["dockerfile"], names = ["alpine/k8s"], allowed = "<1.38"},
  {id = "c", datasources = ["docker"], names = ["alpine/k8s"], enabled = false},
  {id = "d", datasources = ["helm"], age = "3 days", names = ["python"], types = ["minor", "patch"]},
  {id = "e", datasources = ["docker"], age = ""},
]
egress = [
  {name = "dns", ports = [{port = 53}], to = [{cidr = "10.0.0.10/32"}]},
  {name = "api", ports = [{port = 443}, {port = 6443}], to = [{cidr = "203.0.113.5/32"}]},
  {name = "decoy", port = 6443, ports = [{port = 80}], to = [{cidr = "10.9.9.9/32"}]},
]
`,
	})
	for _, c := range []struct{ path, want string }{
		{"rules[?@.datasources[?@ == 'docker']].id", "a c e"},
		{"rules[?@.names[?@ == 'alpine/k8s'] && @.allowed].allowed", "<1.38"},
		{"rules[?@.age].id", "d e"},
		{"rules[?!@.age].id", "a b c"},
		{"rules[?@.age && @.datasources[?@ == 'docker']].id", "e"},
		{"rules[?@.names[?@ == 'python'] && @.types[0] == 'minor' && !@.types[1]].id", "a"},
		{"rules[?@.names[?@ == 'python'] && !@.schedule].id", "d"},
		{"rules[?@.datasources[*]][?@ == 'helm']", ""},
		{"rules[?@.datasources[?@ == 'helm']].datasources[?@ == 'helm']", "helm helm"},
		{"rules[?!@.datasources[?@ == 'docker' || @ == 'helm']].id", "unparsed"},
		{"egress[?@.ports[?@.port == 6443]].to[*].cidr", "203.0.113.5/32"},
		{"egress[?!@.ports[?@.port == 6443]].name", "dns decoy"},
		{"egress[?@.ports[*].port].name", "dns api decoy"},
		{"egress[?@[?@ == 6443]].name", "decoy"},
		{"$[?@[?@.ports[?@.port == 6443]]][?@.name == 'api'].to[0].cidr", "203.0.113.5/32"},
	} {
		for _, file := range []string{"f.yaml", "f.json", "f.toml"} {
			var spec extract.Spec
			switch filepath.Ext(file) {
			case ".yaml":
				spec = extract.Of(yamlreader.Kind, yamlreader.Spec{File: file, Path: c.path})
			case ".json":
				spec = extract.Of(jsonreader.Kind, jsonreader.Spec{File: file, Path: c.path})
			default:
				spec = extract.Of(tomlreader.Kind, tomlreader.Spec{File: file, Path: c.path})
			}
			e, err := extract.Build(spec)
			if c.want == "unparsed" {
				extracttest.WantError(t, err, "|| is not supported in a filter")
				continue
			}
			if err != nil {
				t.Fatalf("%s %s: %v", file, c.path, err)
			}
			values, err := e.Extract(root)
			if err != nil {
				t.Fatalf("%s %s: %v", file, c.path, err)
			}
			var got []string
			for _, v := range values {
				got = append(got, v.Text)
			}
			if strings.Join(got, " ") != c.want {
				t.Errorf("%s %s: got %q, want %q", file, c.path, got, c.want)
			}
		}
	}
}
