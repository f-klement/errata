package extract

import (
	"reflect"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/ini"
	"github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/properties"
	"github.com/f-klement/errata/internal/extract/text"
	"github.com/f-klement/errata/internal/extract/xml"
	"github.com/f-klement/errata/internal/extract/yaml"
)

func TestBuildRefusesNoKindAndTwoKinds(t *testing.T) {
	for name, s := range map[string]Spec{
		"none": {},
		"two":  {YAML: &yaml.Spec{File: "a", Path: "a"}, JSON: &json.Spec{File: "a", Path: "a"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Build(s)
			extracttest.WantError(t, err, "an extractor sets exactly one of yaml, json, toml, regex")
		})
	}
}

// The kinds table is the only place Build counts and dispatches, so every
// field of Spec must have a row, under its JSON name, in field order. A Spec
// with only that field set builds through that row: the error names the kind.
func TestKindsCoverSpec(t *testing.T) {
	typ := reflect.TypeFor[Spec]()
	kinds := Spec{}.kinds()
	if typ.NumField() != len(kinds) {
		t.Fatalf("Spec has %d fields and kinds lists %d", typ.NumField(), len(kinds))
	}
	for i := range typ.NumField() {
		f := typ.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if kinds[i].name != name {
			t.Errorf("field %s is %q in JSON, row %d is %q", f.Name, name, i, kinds[i].name)
		}
		var s Spec
		reflect.ValueOf(&s).Elem().Field(i).Set(reflect.New(f.Type.Elem()))
		_, err := Build(s)
		extracttest.WantError(t, err, name+": file is required")
	}
}

// Place keeps every kind: for each row of the kinds table, a Spec with only
// that kind set, its file a/../f and a max_bytes, has a place that sets the
// same kind, with the file cleaned and no max_bytes, and two such specs of
// different files have different places. A kind added to Spec and kinds
// without a case in Place fails here. The planted break: Place without its
// text case, which gave every text extractor the empty place, so a rule of
// two text extractors was refused as reading one place.
func TestPlaceKeepsEveryKind(t *testing.T) {
	kindSpec := func(i int, file string) Spec {
		var s Spec
		f := reflect.ValueOf(&s).Elem().Field(i)
		f.Set(reflect.New(f.Type().Elem()))
		f.Elem().FieldByName("File").SetString(file)
		f.Elem().FieldByName("MaxBytes").SetInt(10)
		return s
	}
	for i, k := range (Spec{}).kinds() {
		p := reflect.ValueOf(kindSpec(i, "a/../f").Place()).Field(i)
		if p.IsNil() {
			t.Errorf("%s: Place drops the kind", k.name)
			continue
		}
		if file, n := p.Elem().FieldByName("File").String(), p.Elem().FieldByName("MaxBytes").Int(); file != "f" || n != 0 {
			t.Errorf("%s: Place keeps file %q and max_bytes %d, want f and 0", k.name, file, n)
		}
		if reflect.DeepEqual(kindSpec(i, "f").Place(), kindSpec(i, "g").Place()) {
			t.Errorf("%s: files f and g have one place", k.name)
		}
	}
}

// Two xml extractors over one file and path read one place, whatever their
// max_bytes, and over another path or namespace they do not.
func TestXMLPlace(t *testing.T) {
	a := Spec{XML: &xml.Spec{File: "./f.xml", Path: "/a", Namespaces: map[string]string{"m": "u"}, MaxBytes: 10}}
	b := Spec{XML: &xml.Spec{File: "f.xml", Path: "/a", Namespaces: map[string]string{"m": "u"}}}
	if !reflect.DeepEqual(a.Place(), b.Place()) {
		t.Errorf("one place: %+v and %+v", a.Place().XML, b.Place().XML)
	}
	for _, c := range []Spec{
		{XML: &xml.Spec{File: "f.xml", Path: "/b", Namespaces: map[string]string{"m": "u"}}},
		{XML: &xml.Spec{File: "f.xml", Path: "/a", Namespaces: map[string]string{"m": "v"}}},
		{XML: &xml.Spec{File: "g.xml", Path: "/a", Namespaces: map[string]string{"m": "u"}}},
	} {
		if reflect.DeepEqual(a.Place(), c.Place()) {
			t.Errorf("another place reads as one: %+v", c.XML)
		}
	}
}

// Two specs that read one place have one Place: the file cleaned, the
// dialect chosen by name or written out, and a git name in any case or
// written as section.subsection. Specs that read different places differ.
func TestINIPlace(t *testing.T) {
	same := [][2]Spec{
		{iniSpec("f.ini", "s", "k"), {INI: &ini.Spec{File: "./f.ini", Dialect: "plain", Section: "s", Key: "k", MaxBytes: 10}}},
		{gitINI(".gitconfig", "Remote", strPtr("origin"), "URL"), gitINI("a/../.gitconfig", "remote.origin", nil, "url")},
		{{Properties: &properties.Spec{File: "a.properties", Key: "k"}}, {Properties: &properties.Spec{File: "./a.properties", Key: "k", MaxBytes: 5}}},
		{{INI: &ini.Spec{File: "hosts", Section: "web", Key: "k"}}, {INI: &ini.Spec{File: "hosts", Section: "web:hosts", Key: "k"}}},
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
		{iniSpec("hosts", "s", "k"), {INI: &ini.Spec{File: "hosts", Dialect: "plain", Section: "s", Key: "k"}}},
		{{Properties: &properties.Spec{File: "a.properties", Key: "k"}}, {Properties: &properties.Spec{File: "a.properties", Key: "K"}}},
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
		{Spec{JSON: &json.Spec{File: "a.json", Path: "$"}}, "json"},
		{Spec{Properties: &properties.Spec{File: "a.properties", Key: "k"}}, "properties"},
		{iniSpec("php.ini", "", "a"), "ini, php dialect on php.ini"},
		{iniSpec("etc/php.ini-production", "", "a"), "ini, php dialect on php.ini-production"},
		{gitINI(".gitconfig", "a", nil, "k"), "ini, git dialect on .gitconfig"},
		{iniSpec("conf/pytest.ini", "s", "k"), "ini, plain dialect on pytest.ini"},
		{Spec{INI: &ini.Spec{File: "a.ini", Dialect: "tox", Key: "k"}}, "ini, tox dialect on a.ini"},
		{Spec{INI: &ini.Spec{File: "a.ini", Dialect: "nope", Key: "k"}}, ""},
		{Spec{}, ""},
		{Spec{JSON: &json.Spec{File: "a.json", Path: "$"}, Text: &text.Spec{File: "a"}}, ""},
	} {
		if got := c.spec.Kind(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.spec, got, c.want)
		}
	}
}

func iniSpec(file, section, key string) Spec {
	return Spec{INI: &ini.Spec{File: file, Section: section, Key: key}}
}

func gitINI(file, section string, subsection *string, key string) Spec {
	return Spec{INI: &ini.Spec{File: file, Section: section, Subsection: subsection, Key: key}}
}

func strPtr(s string) *string { return &s }
