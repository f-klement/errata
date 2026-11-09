package sudoers

import (
	"reflect"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
)

// grants: true returns a record for every command every user
// specification grants, whatever its user list, aliases resolved as for
// user:, a denied user's grant too, each literal unless a template marks
// it. Planted break: grants() deciding the user list for grants: true.
func TestEveryGrant(t *testing.T) {
	files := map[string]string{"sudoers": "User_Alias OPS = alice, !bob\nCmnd_Alias SVC = /a, /b\n" +
		"root ALL = (ALL) ALL\nOPS ALL = NOPASSWD: SVC\n%wheel, !ALL web = /c\n"}
	runReads(t, []readCase{
		{"every grant", files, Spec{Where: where("sudoers"), Grants: true},
			"sudoers:3={command=ALL host=ALL runas=ALL user=root}" +
				" | sudoers:2={command=/a host=ALL tags=NOPASSWD user=alice,!bob} | sudoers:2={command=/b host=ALL tags=NOPASSWD user=alice,!bob}" +
				" | sudoers:5={command=/c host=web user=%wheel,!ALL}"},
		{"a template", map[string]string{"sudoers.j2": "{{ u }} ALL = /a\nbob ALL = /b\n"}, Spec{Where: where("sudoers.j2"), Grants: true},
			"sudoers.j2:1={command=/a host=ALL user={{ u }}} !lit | sudoers.j2:2={command=/b host=ALL user=bob}"},
	})
}

// grants: true is one of the three selectors: with defaults: or user: it
// does not load, and it places, resolves and describes as records of
// Fields, literal but in a template. Planted break: checkSelector letting
// grants: true stand beside user:.
func TestGrantsSelector(t *testing.T) {
	for _, s := range []Spec{
		{Where: where("sudoers"), Grants: true, User: "bob"},
		{Where: where("sudoers"), Grants: true, Defaults: "x"},
		{Where: where("sudoers"), Grants: true, Defaults: "x", User: "bob"},
	} {
		if _, err := Build(s); err == nil || !strings.Contains(err.Error(), "set exactly one of defaults, user and grants") {
			t.Errorf("%+v: %v", s, err)
		}
	}
	if _, err := Build(Spec{Where: where("sudoers")}); err == nil || !strings.Contains(err.Error(), "grants: true for every grant") {
		t.Errorf("no selector: %v", err)
	}
	s := Spec{Where: where("./sudoers"), Grants: true}
	if p := Place(s); !p.Grants || p.File != "sudoers" {
		t.Errorf("Place gives %+v", p)
	}
	want := check.Description{Label: "sudoers", Shape: check.ShapeRecord, Fields: Fields}
	if got := Describe(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Describe gives %+v", got)
	}
	want.NotLiteral = true
	if got := Describe(Spec{Where: where("sudoers.j2"), Grants: true}); !reflect.DeepEqual(got, want) {
		t.Errorf("Describe of a template gives %+v", got)
	}
}
