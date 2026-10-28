package core

import "testing"

// A name with *, ? or [ anywhere is a glob, and one with none is not, ]
// alone included. Planted break: [ left out of the set, which read
// app/[slug].tsx as a name.
func TestHasGlob(t *testing.T) {
	for file, want := range map[string]bool{
		"infra/*.yaml":   true,
		"a?.yaml":        true,
		"app/[slug].tsx": true,
		"*":              true,
		"infra/a.yaml":   false,
		"a]b":            false,
		"":               false,
	} {
		if got := HasGlob(file); got != want {
			t.Errorf("%q: %v, want %v", file, got, want)
		}
	}
}
