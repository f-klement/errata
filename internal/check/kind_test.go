package check

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fake is a kind of check errata does not have, as a plugin's would be.
type fake struct{ problem error }

func (*fake) Name() string              { return "fake" }
func (*fake) Nouns() (one, many string) { return "fake check", "fake checks" }
func (*fake) ScopeRefusal(s Scope) string {
	if s != ScopeLive {
		return "a fake check reads what is live"
	}
	return ""
}
func (f *fake) Validate(Check) []error {
	if f.problem != nil {
		return []error{f.problem}
	}
	return nil
}
func (*fake) ListFields(Check) Fields        { return nil }
func (*fake) ListLines(Check, Quoter) []Line { return nil }

// Validate asks a check's kind which scopes it takes and what is wrong
// with it, so a kind errata has no code for is validated by its own row.
// An unknown scope is refused once, before the kind is asked. The planted
// breaks: Validate asking the kind of an unknown scope, which added the
// kind's refusal, and Validate leaving the kind's errors out.
func TestValidateAsksTheKind(t *testing.T) {
	c := Check{ID: "f", Reason: "r", Scope: ScopeLive, Kind: &fake{}}
	if err := c.Validate(); err != nil {
		t.Fatalf("fake check at live: %v", err)
	}
	c.Scope = ScopeApplied
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `scope "applied": a fake check reads what is live`) {
		t.Errorf("fake check at applied: %v, want the kind's refusal", err)
	}
	c.Scope = "cluster"
	if err := c.Validate(); err == nil || strings.Contains(err.Error(), "reads what is live") {
		t.Errorf("fake check at an unknown scope: %v, want the unknown scope alone", err)
	}
	c.Scope, c.Kind = ScopeLive, &fake{problem: errors.New("the fake's own problem")}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "the fake's own problem") {
		t.Errorf("fake check with a problem: %v, want it", err)
	}
}

// Fields write their keys in order, and a string without HTML escapes
// through an encoder that writes none, as errata list's does. The planted
// breaks: the HTML escapes of Fields' own encoder left on, and a map in
// place of the ordered fields.
func TestFieldsKeepTheirOrder(t *testing.T) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(Fields{{"z", "<a & b>"}, {"a", Fields{{"y", 1}, {"b", []string{}}}}}); err != nil {
		t.Fatal(err)
	}
	if want := `{"z":"<a & b>","a":{"y":1,"b":[]}}` + "\n"; b.String() != want {
		t.Errorf("Fields = %s, want %s", b.String(), want)
	}
}
