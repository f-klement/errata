package udev

import (
	"reflect"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Place writes a spec as udev reads it, so two spellings of one place are
// one, and a key udev does not read keeps its place as written. Planted
// break: Place leaving RUN as written.
func TestPlace(t *testing.T) {
	a := Spec{Where: core.Where{File: "./rules.d/a.rules", MaxBytes: 9}, Key: "RUN"}
	b := Spec{Where: core.Where{File: "rules.d/a.rules"}, Key: "RUN{program}"}
	if pa, pb := Place(a), Place(b); !reflect.DeepEqual(pa, pb) {
		t.Errorf("one place reads as two: %+v and %+v", pa, pb)
	}
	if got := Place(one("a.rules", "MOD")); got.Key != "MOD" {
		t.Errorf("a key udev does not read places as %+v", got)
	}
}

// Resolve writes out max_bytes and shares nothing with the spec. Planted
// break: Resolve not cloning tests:.
func TestResolve(t *testing.T) {
	given := Spec{Where: core.Where{File: "a.rules"}, Key: "MODE", Tests: map[string]string{"KERNEL": "a"}}
	r := Resolve(given)
	r.Tests["KERNEL"] = "b"
	if r.MaxBytes != core.DefaultMaxBytes || given.Tests["KERNEL"] != "a" {
		t.Errorf("Resolve gives %+v, and the spec is %+v", r, given)
	}
}

// Describe labels the file, of file: or the first of files:, and says a
// value may be not literal. Planted break: Describe reading file: alone.
func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want check.Description
	}{
		{one("etc/udev/rules.d/99-x.rules", "MODE"), check.Description{Label: "udev, rules in 99-x.rules", NotLiteral: true}},
		{Spec{Where: core.Where{Files: []string{"rules.d/*.rules"}}, Key: "MODE"}, check.Description{Label: "udev, rules in *.rules", NotLiteral: true}},
		{Spec{Key: "MODE"}, check.Description{Label: "udev, rules in .", NotLiteral: true}},
	} {
		if got := Describe(c.spec); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: %+v, want %+v", c.spec, got, c.want)
		}
	}
}
