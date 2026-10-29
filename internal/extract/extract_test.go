package extract

import (
	encjson "encoding/json"
	"reflect"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/env"
	"github.com/f-klement/errata/internal/extract/envfile"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/filestat"
	"github.com/f-klement/errata/internal/extract/githubactions"
	"github.com/f-klement/errata/internal/extract/gitlabci"
	"github.com/f-klement/errata/internal/extract/ini"
	"github.com/f-klement/errata/internal/extract/jenkins"
	"github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/packageversion"
	"github.com/f-klement/errata/internal/extract/properties"
	"github.com/f-klement/errata/internal/extract/regex"
	"github.com/f-klement/errata/internal/extract/shell"
	"github.com/f-klement/errata/internal/extract/toml"
	"github.com/f-klement/errata/internal/extract/toolversion"
	"github.com/f-klement/errata/internal/extract/xml"
	"github.com/f-klement/errata/internal/extract/yaml"
)

func TestBuildRefusesNoKindAndTwoKinds(t *testing.T) {
	for name, s := range map[string]Spec{
		"none": {},
		"two":  decode(t, `{"yaml": {"file": "a", "path": "a"}, "json": {"file": "a", "path": "a"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Build(s)
			extracttest.WantError(t, err, "an extractor sets exactly one of yaml, json, toml, regex")
		})
	}
}

// Every kind with files: reads one place for the same files in any order
// and spelling, and another place for other files; a kind that takes file:
// too reads one place for file: f and files: [f], and Resolved copies the
// files. A kind that gains files: without Place cleaning and sorting them
// fails here. Planted breaks: the files of yaml left unsorted; a list of
// one file not read as file:; Resolved sharing the spec's slice.
func TestPlaceOfFiles(t *testing.T) {
	files := func(fs ...string) func(reflect.Value) {
		return func(v reflect.Value) { v.FieldByName("Files").Set(reflect.ValueOf(fs)) }
	}
	key := func(s Spec) string {
		b, err := encjson.Marshal(s.Place())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	took := 0
	for _, k := range kinds.order {
		spec := func(set func(reflect.Value)) Spec { return specOf(k, set) }
		if _, ok := k.Spec.FieldByName("Files"); !ok {
			continue
		}
		took++
		if a, b := key(spec(files("./b/*.yaml", "a/**"))), key(spec(files("a/**", "b/*.yaml"))); a != b {
			t.Errorf("%s: one set of files, two places: %s and %s", k.Key, a, b)
		}
		if a, b := key(spec(files("*.y"))), key(spec(files("*.z"))); a == b {
			t.Errorf("%s: two globs, one place %s", k.Key, a)
		}
		s := spec(files("a", "b"))
		r := reflect.ValueOf(s.Resolved().set[0].spec).FieldByName("Files")
		r.Index(0).SetString("changed")
		if got := reflect.ValueOf(s.set[0].spec).FieldByName("Files").Index(0).String(); got != "a" {
			t.Errorf("%s: Resolved shares the files, which now read %q", k.Key, got)
		}
		if _, ok := k.Spec.FieldByName("File"); !ok {
			continue
		}
		one := spec(func(v reflect.Value) { v.FieldByName("File").SetString("f") })
		if a, b := key(one), key(spec(files("./f"))); a != b {
			t.Errorf("%s: file f and files [./f] read two places: %s and %s", k.Key, a, b)
		}
	}
	if took < 7 {
		t.Errorf("%d kinds take files, want yaml, json, toml, regex, tool_version, package_version and env", took)
	}
}

// Two xml extractors over one file and path read one place, whatever their
// max_bytes, and over another path or namespace they do not.
func TestXMLPlace(t *testing.T) {
	a := Of(xml.Kind, xml.Spec{File: "./f.xml", Path: "/a", Namespaces: map[string]string{"m": "u"}, MaxBytes: 10})
	b := Of(xml.Kind, xml.Spec{File: "f.xml", Path: "/a", Namespaces: map[string]string{"m": "u"}})
	if !reflect.DeepEqual(a.Place(), b.Place()) {
		t.Errorf("one place: %+v and %+v", a.Place(), b.Place())
	}
	for _, c := range []Spec{
		Of(xml.Kind, xml.Spec{File: "f.xml", Path: "/b", Namespaces: map[string]string{"m": "u"}}),
		Of(xml.Kind, xml.Spec{File: "f.xml", Path: "/a", Namespaces: map[string]string{"m": "v"}}),
		Of(xml.Kind, xml.Spec{File: "g.xml", Path: "/a", Namespaces: map[string]string{"m": "u"}}),
	} {
		if reflect.DeepEqual(a.Place(), c.Place()) {
			t.Errorf("another place reads as one: %+v", c)
		}
	}
}

// Two yaml, json or toml extractors over one file and path read another
// place when their embedded steps differ, and one place when only their
// max_bytes does. The planted break: Place clearing embedded, which refused
// a rule that reads two keys of one embedded document as reading one place.
func TestEmbeddedPlace(t *testing.T) {
	one := func(format, path string) []core.Embedded { return []core.Embedded{{Format: format, Path: path}} }
	for _, c := range []struct{ a, b, other Spec }{
		{
			Of(yaml.Kind, yaml.Spec{File: "f", Path: "c", Embedded: one("toml", "x"), MaxBytes: 10}),
			Of(yaml.Kind, yaml.Spec{File: "./f", Path: "c", Embedded: one("toml", "x")}),
			Of(yaml.Kind, yaml.Spec{File: "f", Path: "c", Embedded: one("toml", "y")}),
		},
		{
			Of(json.Kind, json.Spec{File: "f", Path: "c", Embedded: one("yaml", "x"), MaxBytes: 10}),
			Of(json.Kind, json.Spec{File: "./f", Path: "c", Embedded: one("yaml", "x")}),
			Of(json.Kind, json.Spec{File: "f", Path: "c", Embedded: one("json", "x")}),
		},
		{
			Of(toml.Kind, toml.Spec{File: "f", Path: "c", Embedded: one("json", "x"), MaxBytes: 10}),
			Of(toml.Kind, toml.Spec{File: "./f", Path: "c", Embedded: one("json", "x")}),
			Of(toml.Kind, toml.Spec{File: "f", Path: "c"}),
		},
	} {
		if !reflect.DeepEqual(c.a.Place(), c.b.Place()) {
			t.Errorf("one place reads as two: %s", c.a.Kind())
		}
		if reflect.DeepEqual(c.a.Place(), c.other.Place()) {
			t.Errorf("another place reads as one: %s", c.a.Kind())
		}
	}
}

// Two specs that read one place have one Place: the file cleaned, the
// dialect chosen by name or written out, and a git name in any case or
// written as section.subsection. Specs that read different places differ.
func TestINIPlace(t *testing.T) {
	same := [][2]Spec{
		{iniSpec("f.ini", "s", "k"), Of(ini.Kind, ini.Spec{File: "./f.ini", Dialect: "plain", Section: "s", Key: "k", MaxBytes: 10})},
		{gitINI(".gitconfig", "Remote", strPtr("origin"), "URL"), gitINI("a/../.gitconfig", "remote.origin", nil, "url")},
		{Of(properties.Kind, properties.Spec{File: "a.properties", Key: "k"}), Of(properties.Kind, properties.Spec{File: "./a.properties", Key: "k", MaxBytes: 5})},
		{Of(envfile.Kind, envfile.Spec{File: ".env", Name: "A"}), Of(envfile.Kind, envfile.Spec{File: "./.env", Name: "A", MaxBytes: 5})},
		{Of(ini.Kind, ini.Spec{File: "hosts", Section: "web", Key: "k"}), Of(ini.Kind, ini.Spec{File: "hosts", Section: "web:hosts", Key: "k"})},
		{iniSpec("tox.ini", "testenv", "Deps"), iniSpec("tox.ini", "testenv", "deps")},
	}
	for _, p := range same {
		if a, b := p[0].Place(), p[1].Place(); !reflect.DeepEqual(a, b) {
			t.Errorf("%+v and %+v: places differ", a, b)
		}
	}
	differ := [][2]Spec{
		{iniSpec("f.ini", "s", "k"), iniSpec("f.ini", "s", "K")},
		{gitINI(".gitconfig", "remote", strPtr("origin"), "url"), gitINI(".gitconfig", "remote", strPtr("Origin"), "url")},
		{iniSpec("hosts", "s", "k"), Of(ini.Kind, ini.Spec{File: "hosts", Dialect: "plain", Section: "s", Key: "k"})},
		{Of(properties.Kind, properties.Spec{File: "a.properties", Key: "k"}), Of(properties.Kind, properties.Spec{File: "a.properties", Key: "K"})},
		{Of(envfile.Kind, envfile.Spec{File: ".env", Name: "A"}), Of(envfile.Kind, envfile.Spec{File: ".env"})},
	}
	for _, p := range differ {
		if reflect.DeepEqual(p[0].Place(), p[1].Place()) {
			t.Errorf("%+v and %+v: one place", p[0], p[1])
		}
	}
}

// Kind names the kind a spec sets for a load error, with an ini spec's
// dialect, chosen by the file's name or named.
func TestSpecKind(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Of(json.Kind, json.Spec{File: "a.json", Path: "$"}), "json"},
		{Of(properties.Kind, properties.Spec{File: "a.properties", Key: "k"}), "properties"},
		{Of(toolversion.Kind, toolversion.Spec{Tool: "node"}), "tool_version"},
		{Of(packageversion.Kind, packageversion.Spec{}), "package_version"},
		{Of(envfile.Kind, envfile.Spec{File: ".env", Name: "A"}), "envfile"},
		{Of(gitlabci.Kind, gitlabci.Spec{File: ".gitlab-ci.yml", Job: "build", Key: "image"}), "gitlab-ci"},
		{Of(shell.Kind, shell.Spec{File: "deploy.sh", Name: "X"}), "shell"},
		{Of(githubactions.Kind, githubactions.Spec{File: "ci.yml", Job: "test", Key: "runs-on"}), "github-actions"},
		{Of(jenkins.Kind, jenkins.Spec{File: "Jenkinsfile", Variable: "X"}), "jenkins"},
		{Of(env.Kind, env.Spec{Name: "X", Files: []string{".env"}}), "env"},
		{Of(filestat.ExistsKind, filestat.Spec{File: "a"}), "exists"},
		{Of(filestat.SizeKind, filestat.Spec{File: "a"}), "size"},
		{decode(t, `{"exists": {"file": "a"}, "size": {"file": "a"}}`), ""},
		{iniSpec("php.ini", "", "a"), "ini, php dialect on php.ini"},
		{iniSpec("etc/php.ini-production", "", "a"), "ini, php dialect on php.ini-production"},
		{gitINI(".gitconfig", "a", nil, "k"), "ini, git dialect on .gitconfig"},
		{iniSpec("conf/pytest.ini", "s", "k"), "ini, plain dialect on pytest.ini"},
		{Of(ini.Kind, ini.Spec{File: "a.ini", Dialect: "tox", Key: "k"}), "ini, tox dialect on a.ini"},
		{Of(ini.Kind, ini.Spec{File: "a.ini", Dialect: "nope", Key: "k"}), ""},
		{Spec{}, ""},
		{decode(t, `{"json": {"file": "a.json", "path": "$"}, "text": {"file": "a"}}`), ""},
	} {
		if got := c.spec.Kind(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.spec, got, c.want)
		}
	}
}

func iniSpec(file, section, key string) Spec {
	return Of(ini.Kind, ini.Spec{File: file, Section: section, Key: key})
}

func gitINI(file, section string, subsection *string, key string) Spec {
	return Of(ini.Kind, ini.Spec{File: file, Section: section, Subsection: subsection, Key: key})
}

func strPtr(s string) *string { return &s }

// Resolved writes out the dialect an ini spec's file chooses, keeps one the
// spec names, and leaves one errata does not read as written, for Build to
// refuse.
func TestResolvedINIDialect(t *testing.T) {
	for _, c := range []struct{ file, dialect, want string }{
		{"tox.ini", "", "tox"},
		{"php.ini", "", "php"},
		{".gitconfig", "", "git"},
		{"inventory/hosts", "", "ansible"},
		{"setup.cfg", "", "plain"},
		{"setup.cfg", "php", "php"},
		{"setup.cfg", "nginx", "nginx"},
	} {
		s := Of(ini.Kind, ini.Spec{File: c.file, Dialect: c.dialect, Key: "k"})
		if got := s.Resolved().set[0].spec.(ini.Spec).Dialect; got != c.want {
			t.Errorf("%s with dialect %q: Resolved has %q, want %q", c.file, c.dialect, got, c.want)
		}
		if got := s.set[0].spec.(ini.Spec).Dialect; got != c.dialect {
			t.Errorf("%s: Resolved changed the spec's dialect to %q", c.file, got)
		}
	}
}

// Maps is true for a yaml, json or toml spec with as: map, and false for
// each without it and for any other kind. Planted breaks: each of the three
// tests negated (!= for ==), which turned that kind's answer over.
func TestSpecMaps(t *testing.T) {
	for name, c := range map[string]struct {
		s    Spec
		want bool
	}{
		"yaml map":  {Of(yaml.Kind, yaml.Spec{File: "a.yaml", Path: "x[*]", As: "map"}), true},
		"yaml flat": {Of(yaml.Kind, yaml.Spec{File: "a.yaml", Path: "x[*]"}), false},
		"json map":  {Of(json.Kind, json.Spec{File: "a.json", Path: "x[*]", As: "map"}), true},
		"json flat": {Of(json.Kind, json.Spec{File: "a.json", Path: "x[*]"}), false},
		"toml map":  {Of(toml.Kind, toml.Spec{File: "a.toml", Path: "x[*]", As: "map"}), true},
		"toml flat": {Of(toml.Kind, toml.Spec{File: "a.toml", Path: "x[*]"}), false},
		"regex":     {Of(regex.Kind, regex.Spec{File: "a", Pattern: "(x)"}), false},
		// A record is a map to assert, from a path or a regex alike.
		"yaml record":  {Of(yaml.Kind, yaml.Spec{File: "a.yaml", Path: "x[*]", As: "record", Fields: map[string]string{"n": "n"}}), true},
		"json record":  {Of(json.Kind, json.Spec{File: "a.json", Path: "x[*]", As: "record", Fields: map[string]string{"n": "n"}}), true},
		"toml record":  {Of(toml.Kind, toml.Spec{File: "a.toml", Path: "x[*]", As: "record", Fields: map[string]string{"n": "n"}}), true},
		"regex record": {Of(regex.Kind, regex.Spec{File: "a", Pattern: "(?P<n>x)", As: "record"}), true},
		"ini":          {Of(ini.Kind, ini.Spec{File: "a.ini", Section: "s", Key: "k"}), false},
	} {
		if got := c.s.Maps(); got != c.want {
			t.Errorf("%s: Maps() = %t, want %t", name, got, c.want)
		}
	}
}

// Fields names the fields of the records a spec returns: a path record's
// fields sorted by name, a regex record's named groups in the order the
// pattern opens them, and nil for any spec that returns no record, a map
// with fields and a record without any included. Planted breaks: each
// condition of Spec.Fields and Spec.as negated.
func TestSpecFields(t *testing.T) {
	fields := map[string]string{"url": "u", "name": "n"}
	for name, c := range map[string]struct {
		s    Spec
		want []string
	}{
		"yaml record":  {Of(yaml.Kind, yaml.Spec{File: "a.yaml", Path: "x[*]", As: "record", Fields: fields}), []string{"name", "url"}},
		"json record":  {Of(json.Kind, json.Spec{File: "a.json", Path: "x[*]", As: "record", Fields: fields}), []string{"name", "url"}},
		"toml record":  {Of(toml.Kind, toml.Spec{File: "a.toml", Path: "x[*]", As: "record", Fields: fields}), []string{"name", "url"}},
		"regex record": {Of(regex.Kind, regex.Spec{File: "a", Pattern: "(?P<url>x)(y)(?P<name>z)", As: "record"}), []string{"url", "name"}},
		"regex flat":   {Of(regex.Kind, regex.Spec{File: "a", Pattern: "(?P<url>x)"}), nil},
		"yaml map":     {Of(yaml.Kind, yaml.Spec{File: "a.yaml", Path: "x[*]", As: "map", Fields: fields}), nil},
		"json flat":    {Of(json.Kind, json.Spec{File: "a.json", Path: "x[*]", Fields: fields}), nil},
		"record, none": {Of(toml.Kind, toml.Spec{File: "a.toml", Path: "x[*]", As: "record"}), nil},
		"ini":          {Of(ini.Kind, ini.Spec{File: "a.ini", Section: "s", Key: "k"}), nil},
	} {
		got := c.s.Fields()
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Fields() = %#v, want %#v", name, got, c.want)
		}
	}
}

// A regex that writes the default values_as out reads the place of one
// that leaves it out, and Resolved writes it out for errata list. Planted
// breaks: Place and Resolved copying the regex spec as it is.
func TestRegexValuesAsPlace(t *testing.T) {
	ref := func(valuesAs string) Spec {
		return Of(regex.Kind, regex.Spec{File: "f", Pattern: `((?{a}))`, ValuesAs: valuesAs})
	}
	if !reflect.DeepEqual(ref("literal").Place(), ref("").Place()) {
		t.Errorf("values_as literal and none read two places: %+v, %+v", ref("literal").Place(), ref("").Place())
	}
	if got := ref("").Resolved().set[0].spec.(regex.Spec).ValuesAs; got != "literal" {
		t.Errorf("Resolved gives values_as %q, want literal", got)
	}
}

// A regex's context decides which matches it keeps, so two regexes that
// differ only in it read two places, and agree compares them. Planted
// break: Place leaving the context out.
func TestRegexContextPlace(t *testing.T) {
	with := func(c *core.Context) []byte {
		key, err := encjson.Marshal(Of(regex.Kind, regex.Spec{File: "f", Pattern: `(v)`, Context: c}).Place())
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	if a, b := with(nil), with(&core.Context{Without: "#"}); string(a) == string(b) {
		t.Errorf("a regex with a context and one without read one place, %s", a)
	}
	if a, b := with(&core.Context{With: "x"}), with(&core.Context{With: "x"}); string(a) != string(b) {
		t.Errorf("one context read two places, %s and %s", a, b)
	}
}
