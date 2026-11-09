package sudoers

import (
	"reflect"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Place writes a spec without the white space around its setting and its
// user, and without max_bytes, so two spellings of one place are one.
// Planted break: Place leaving the user untrimmed.
func TestPlace(t *testing.T) {
	a := Spec{Where: core.Where{File: "./etc/sudoers", MaxBytes: 9}, User: " %admin "}
	b := Spec{Where: where("etc/sudoers"), User: "%admin"}
	if pa, pb := Place(a), Place(b); !reflect.DeepEqual(pa, pb) {
		t.Errorf("one place reads as two: %+v and %+v", pa, pb)
	}
	if got := Place(Spec{Where: where("sudoers"), Defaults: " secure_path "}); got.Defaults != "secure_path" {
		t.Errorf("defaults places as %q", got.Defaults)
	}
}

// Resolve writes out max_bytes and shares nothing with the spec. Planted
// break: Resolve not cloning files:.
func TestResolve(t *testing.T) {
	given := Spec{Where: core.Where{Files: []string{"sudoers.d/*"}}, Defaults: "x"}
	r := Resolve(given)
	r.Files[0] = "y"
	if r.MaxBytes != core.DefaultMaxBytes || given.Files[0] != "sudoers.d/*" {
		t.Errorf("Resolve gives %+v, and the spec is %+v", r, given)
	}
}

// Describe labels the kind, describes user: as records of Fields, and
// marks not literal a template, and a user or a group, whose grants
// through another entry that may name them the host decides; a netgroup
// matches only itself and ALL. Planted break: Describe
// leaving the shape flat for user:.
func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want check.Description
	}{
		{defaults("etc/sudoers", "secure_path"), check.Description{Label: "sudoers"}},
		{defaults("sudoers.j2", "secure_path"), check.Description{Label: "sudoers", NotLiteral: true}},
		{user("sudoers", "%admin"), check.Description{Label: "sudoers", Shape: check.ShapeRecord, Fields: Fields, NotLiteral: true}},
		{user("sudoers", "+ops"), check.Description{Label: "sudoers", Shape: check.ShapeRecord, Fields: Fields}},
		{user("sudoers", "alice"), check.Description{Label: "sudoers", Shape: check.ShapeRecord, Fields: Fields, NotLiteral: true}},
		{Spec{Where: core.Where{Files: []string{"d/a", "d/b.j2"}}, User: "ALL"}, check.Description{Label: "sudoers", Shape: check.ShapeRecord, Fields: Fields, NotLiteral: true}},
	} {
		if got := Describe(c.spec); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: %+v, want %+v", c.spec, got, c.want)
		}
	}
}

// Build refuses a spec that selects nothing or two things, a setting that
// is no name, a user that is no entry, and a file or files it cannot read.
// Planted break: checkSelector accepting defaults and user together.
func TestBuildRefusals(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Where: where("sudoers")}, "write defaults: for a Defaults setting"},
		{Spec{Where: where("sudoers"), Defaults: "a", User: "b"}, "set exactly one of defaults, user and grants"},
		{defaults("sudoers", "!a"), "is no name of a setting"},
		{defaults("sudoers", "a=1"), "is no name of a setting"},
		{user("sudoers", "a,b"), "holds a tab or one of"},
		{user("sudoers", "!bob"), "which is negated"},
		{user("sudoers", "ADMINS"), "a User_Alias"},
		{Spec{Where: core.Where{File: "a", Files: []string{"b"}}, User: "x"}, "file and files are both set"},
		{Spec{Where: core.Where{Files: []string{}}, User: "x"}, "files lists no entry"},
		{user("/etc/sudoers", "x"), "outside the repository"},
	} {
		if _, err := Build(c.spec); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.spec, err, c.want)
		}
	}
	for _, ok := range []Spec{user("sudoers", "%:dom users"), user("sudoers", "#1000"), user("sudoers", "+ops"), user("sudoers", "ALL")} {
		if _, err := Build(ok); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
}
