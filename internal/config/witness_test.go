package config

import (
	"slices"
	"strings"
	"testing"
)

// witnessed is one program check of id with the witness key written as
// value, as a config writes it under checks.
func witnessed(id, value string) string {
	return program(id) + "    witness: " + value + "\n"
}

// The witness key loads its names in order onto every kind of check, and
// a check without it names none. The planted break: the names left off the
// check.
func TestWitnessLoads(t *testing.T) {
	rule := "  - id: r\n    reason: r\n    scope: repo\n    extract: {a: {regex: {file: f, pattern: '(.)'}}}\n    assert: size(a) > 0\n    witness: [c]\n"
	gen := "  - id: g\n    reason: r\n    scope: repo\n    generated: {file: f, run: [x]}\n    witness: [d]\n"
	cfg, err := parse([]byte("checks:\n"+witnessed("p", "[b, a]")+rule+gen+program("none")), BuiltIn)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]string{{"b", "a"}, {"c"}, {"d"}, nil} {
		if got := cfg.Checks[i].Witness; !slices.Equal(got, want) {
			t.Errorf("check %s names %q, want %q", cfg.Checks[i].ID, got, want)
		}
	}
}

// An empty witness list and a bare name do not load, each with its own
// message. The planted breaks: each refusal removed, which loaded [] as
// no witness and read the bare name through the decoder's message.
func TestWitnessRefusesAnEmptyListAndABareName(t *testing.T) {
	for value, want := range map[string]string{
		"[]":    "witness lists no fixture: name one to 8 directories of .errata/witness, or leave the key out",
		"drift": "witness takes a list of fixture names, such as witness: [name]; \"drift\" is not accepted",
	} {
		_, err := parse([]byte("checks:\n"+witnessed("p", value)), BuiltIn)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("witness: %s gave %v, want %q", value, err, want)
		}
	}
}
