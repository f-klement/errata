package config

import (
	"strings"
	"testing"
)

// in_place loads on a program check and a generated check, each keeping
// what it writes, and a check that leaves it out runs in a copy. A rule
// that writes it is refused once, by the key and not again by validation.
// Planted break: InPlace never set from the entry, where kept loaded false.
func TestInPlaceLoadsOnAProgramAndAGenerator(t *testing.T) {
	cfg, err := parseRaw([]byte(`checks:
- {id: kept, reason: r, scope: repo, run: [x], in_place: true}
- {id: copied, reason: r, scope: repo, run: [x]}
- {id: generated, reason: r, scope: repo, in_place: true, generated: {file: f, run: [x]}}
`))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false, true} {
		if got := cfg.Checks[i].InPlace; got != want {
			t.Errorf("%s: InPlace %v, want %v", cfg.Checks[i].ID, got, want)
		}
	}
	_, err = parseRaw([]byte("checks:\n- id: r\n  reason: r\n  scope: repo\n  extract: {v: {json: {file: f.json, path: a}}}\n  agree: [v]\n  in_place: true\n"))
	if err == nil || strings.Count(err.Error(), "in_place applies") != 1 {
		t.Errorf("in_place on a rule: %v, want it refused once", err)
	}
}
