package compose

import (
	"reflect"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Build refuses a spec it cannot read before any run, each for its named
// reason. Planted break: checkKey not refusing an empty part.
func TestBuildRefuses(t *testing.T) {
	long := strings.Repeat("x", maxName+1)
	cases := []struct {
		s    Spec
		want string
	}{
		{Spec{Service: "web", Key: "image"}, "compose: file is required"},
		{Spec{OneFile: oneFile("../c.yaml"), Service: "web", Key: "image"}, "c.yaml"},
		{Spec{OneFile: oneFile("c.yaml"), Key: "image"}, "compose: service is required"},
		{Spec{OneFile: oneFile("c.yaml"), Service: long, Key: "image"}, "over the limit of 1024"},
		{Spec{OneFile: oneFile("c.yaml"), Service: "web"}, "compose: key is required"},
		{Spec{OneFile: oneFile("c.yaml"), Service: "web", Key: long}, "over the limit of 1024"},
		{Spec{OneFile: oneFile("c.yaml"), Service: "web", Key: "build..context"}, "has an empty part"},
		{Spec{OneFile: oneFile("c.yaml"), Service: "web", Key: "image", Overrides: []string{"/etc/o.yaml"}}, "o.yaml"},
		{Spec{OneFile: oneFile("c.yaml"), Service: "web", Key: "image", Overrides: make([]string, maxOverrides+1)}, "over the limit of 64"},
		{Spec{OneFile: oneFile("c.yaml"), Service: "web", Key: "image", Profiles: []string{""}}, "profile \"\" is empty"},
		{Spec{OneFile: oneFile("c.yaml"), Service: "web", Key: "image", Profiles: []string{long}}, "over 1024 bytes long"},
	}
	for _, c := range cases {
		if _, err := Build(c.s, yamlParse); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.s, err, c.want)
		}
	}
	for _, name := range []string{"", "A=B", "A\x00", strings.Repeat("A", maxName+1)} {
		if _, err := Every("compose.yaml", name, 0, yamlParse); err == nil || !strings.Contains(err.Error(), "is no name an environment holds") {
			t.Errorf("Every %q: %v", name, err)
		}
	}
	if _, err := Every("../compose.yaml", "A", 0, yamlParse); err == nil {
		t.Error("Every took a file outside the repository")
	}
}

// Place keeps what decides where the extractor reads: the overrides in
// their order, an empty list apart from none, the profiles as a set.
// Resolve writes max_bytes out and shares no list with its spec. Planted
// break: Place making an empty overrides: list nil.
func TestPlaceAndResolve(t *testing.T) {
	s := Spec{OneFile: core.OneFile{File: "./c.yaml", MaxBytes: 9}, Overrides: []string{"b/../o2.yaml", "o1.yaml"}, Service: "web", Key: "image", Profiles: []string{"b", "a", "b"}}
	want := Spec{OneFile: core.OneFile{File: "c.yaml"}, Overrides: []string{"o2.yaml", "o1.yaml"}, Service: "web", Key: "image", Profiles: []string{"a", "b"}}
	if got := Place(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Place: %+v, want %+v", got, want)
	}
	if got := Place(Spec{Overrides: []string{}}); got.Overrides == nil || reflect.DeepEqual(got, Place(Spec{})) {
		t.Errorf("Place of overrides: [] is %+v, the place of no overrides: key", got)
	}
	if s.Profiles[0] != "b" {
		t.Error("Place sorted the spec's own profiles")
	}
	r := Resolve(Spec{OneFile: oneFile("c.yaml"), Overrides: []string{"o"}, Profiles: []string{"p"}})
	if r.MaxBytes != core.DefaultMaxBytes {
		t.Errorf("Resolve max_bytes: %d", r.MaxBytes)
	}
	r2 := Resolve(r)
	r2.Overrides[0], r2.Profiles[0] = "x", "x"
	if r.Overrides[0] != "o" || r.Profiles[0] != "p" {
		t.Error("Resolve shares a list with its spec")
	}
	if d := Describe(s); !reflect.DeepEqual(d, check.Description{Label: "compose", NotLiteral: true}) {
		t.Errorf("Describe: %+v", d)
	}
}

// The env kind takes a file docker compose finds by its name, and no
// other, an override file neither, and reads it with Every.
func TestEnvChoice(t *testing.T) {
	for file, want := range map[string]bool{
		"compose.yaml": true, "deploy/docker-compose.yml": true, "compose.override.yml": false,
		"compose.prod.yaml": false, "compose.yaml.bak": false, "my-compose.yaml": false,
	} {
		if got := Kind.Env.Chooses(file); got != want {
			t.Errorf("Chooses(%s) = %t", file, got)
		}
	}
	x, err := envRead("compose.yaml", "PORT", 7, core.Parsers{YAML: yamlParse})
	if err != nil {
		t.Fatal(err)
	}
	if e := x.(extractor); !e.every || e.variable != "PORT" || e.Limit != 7 {
		t.Errorf("envRead: %+v", e)
	}
}
