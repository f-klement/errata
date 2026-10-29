package config

import (
	"reflect"
	"testing"
)

// Each row of the kind decision builds the kind Kinds names for it, so the
// list engine's and report's tests hold their tables to is the list of
// what loads. The planted break: the rule's row naming a generated check
// as its kind.
func TestKindsAreWhatTheRowsBuild(t *testing.T) {
	cfg, err := parseRaw([]byte("checks:\n" +
		"- {id: p, reason: r, scope: repo, run: [x]}\n" +
		"- {id: r, reason: r, scope: repo, extract: {v: {json: {file: f.json, path: a}}}, agree: [v]}\n" +
		"- {id: g, reason: r, scope: repo, generated: {file: f, run: [g]}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := Kinds()
	if len(kinds) != len(cfg.Checks) {
		t.Fatalf("Kinds names %d kinds, and the config loads one of each of %d", len(kinds), len(cfg.Checks))
	}
	for i, c := range cfg.Checks {
		if got, want := reflect.TypeOf(c.Kind), reflect.TypeOf(kinds[i]); got != want {
			t.Errorf("check %s is a %s, and Kinds names %s for its row", c.ID, got, want)
		}
	}
	if Kinds()[0] == kinds[0] {
		t.Error("Kinds returned the same value twice, which a caller could change for the next")
	}
}

// Exactly one kind row is the default. defaultRow takes the first it
// finds, so a second marked row would be ignored without a word. Planted
// break: the rule row marked isDefault too.
func TestOneKindRowIsTheDefault(t *testing.T) {
	n := 0
	for _, row := range kindRows {
		if row.isDefault {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d kind rows are marked isDefault, and an entry that writes no kind's keys needs one", n)
	}
}
