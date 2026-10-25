package extract

import (
	"reflect"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/envfile"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/githubactions"
	"github.com/f-klement/errata/internal/extract/gitlabci"
	"github.com/f-klement/errata/internal/extract/ini"
	"github.com/f-klement/errata/internal/extract/jenkins"
	"github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/properties"
	"github.com/f-klement/errata/internal/extract/shell"
	"github.com/f-klement/errata/internal/extract/text"
	"github.com/f-klement/errata/internal/extract/toolversion"
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
		required := "file"
		if name == "tool_version" {
			required = "tool" // it reads the files that pin the tool
		}
		extracttest.WantError(t, err, name+": "+required+" is required")
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
	// A kind that reads several files, tool_version, names its one in
	// Files.
	kindSpec := func(i int, file string) Spec {
		var s Spec
		f := reflect.ValueOf(&s).Elem().Field(i)
		f.Set(reflect.New(f.Type().Elem()))
		if one := f.Elem().FieldByName("File"); one.IsValid() {
			one.SetString(file)
		} else {
			f.Elem().FieldByName("Files").Set(reflect.ValueOf([]string{file}))
		}
		f.Elem().FieldByName("MaxBytes").SetInt(10)
		return s
	}
	fileOf := func(v reflect.Value) string {
		if one := v.FieldByName("File"); one.IsValid() {
			return one.String()
		}
		return v.FieldByName("Files").Index(0).String()
	}
	for i, k := range (Spec{}).kinds() {
		p := reflect.ValueOf(kindSpec(i, "a/../f").Place()).Field(i)
		if p.IsNil() {
			t.Errorf("%s: Place drops the kind", k.name)
			continue
		}
		if file, n := fileOf(p.Elem()), p.Elem().FieldByName("MaxBytes").Int(); file != "f" || n != 0 {
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
		{{EnvFile: &envfile.Spec{File: ".env", Name: "A"}}, {EnvFile: &envfile.Spec{File: "./.env", Name: "A", MaxBytes: 5}}},
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
		{{EnvFile: &envfile.Spec{File: ".env", Name: "A"}}, {EnvFile: &envfile.Spec{File: ".env"}}},
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
		{Spec{ToolVersion: &toolversion.Spec{Tool: "node"}}, "tool_version"},
		{Spec{EnvFile: &envfile.Spec{File: ".env", Name: "A"}}, "envfile"},
		{Spec{GitLabCI: &gitlabci.Spec{File: ".gitlab-ci.yml", Job: "build", Key: "image"}}, "gitlab-ci"},
		{Spec{Shell: &shell.Spec{File: "deploy.sh", Name: "X"}}, "shell"},
		{Spec{GitHubActions: &githubactions.Spec{File: "ci.yml", Job: "test", Key: "runs-on"}}, "github-actions"},
		{Spec{Jenkins: &jenkins.Spec{File: "Jenkinsfile", Variable: "X"}}, "jenkins"},
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

// Resolved keeps every kind and writes out the max_bytes Build reads to: for
// each row of the kinds table, a Spec with only that kind set and no
// max_bytes resolves to one with the same kind and core's default, a
// max_bytes it sets stays, and the Spec it was called on is not changed. A
// kind added to Spec without a case in Resolved fails here. The planted
// break: Resolved without its toml case, which printed no extractor for a
// toml entry.
func TestResolvedKeepsEveryKind(t *testing.T) {
	for i, k := range (Spec{}).kinds() {
		for _, set := range []int64{0, 10} {
			var s Spec
			f := reflect.ValueOf(&s).Elem().Field(i)
			f.Set(reflect.New(f.Type().Elem()))
			// tool_version names its files in a list, every other kind in File.
			file := func(v reflect.Value) string {
				if fs := v.FieldByName("Files"); fs.IsValid() {
					if fs.Len() != 1 {
						return ""
					}
					return fs.Index(0).String()
				}
				return v.FieldByName("File").String()
			}
			if fs := f.Elem().FieldByName("Files"); fs.IsValid() {
				fs.Set(reflect.ValueOf([]string{"f"}))
			} else {
				f.Elem().FieldByName("File").SetString("f")
			}
			f.Elem().FieldByName("MaxBytes").SetInt(set)
			r := reflect.ValueOf(s.Resolved()).Field(i)
			if r.IsNil() {
				t.Errorf("%s: Resolved drops the kind", k.name)
				continue
			}
			want := set
			if set == 0 {
				want = core.DefaultMaxBytes
			}
			if got := r.Elem().FieldByName("MaxBytes").Int(); got != want || file(r.Elem()) != "f" {
				t.Errorf("%s with max_bytes %d: Resolved has max_bytes %d and file %q, want %d and f", k.name, set, got, file(r.Elem()), want)
			}
			if got := f.Elem().FieldByName("MaxBytes").Int(); got != set {
				t.Errorf("%s: Resolved changed the spec's max_bytes to %d", k.name, got)
			}
		}
	}
}

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
		s := Spec{INI: &ini.Spec{File: c.file, Dialect: c.dialect, Key: "k"}}
		if got := s.Resolved().INI.Dialect; got != c.want {
			t.Errorf("%s with dialect %q: Resolved has %q, want %q", c.file, c.dialect, got, c.want)
		}
		if s.INI.Dialect != c.dialect {
			t.Errorf("%s: Resolved changed the spec's dialect to %q", c.file, s.INI.Dialect)
		}
	}
}
