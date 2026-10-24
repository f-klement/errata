package check

import (
	"strings"
	"testing"
)

// ConditionNote quotes a value's condition after its location, cut at 64
// bytes where a character starts, and is empty for a value that always
// holds. Planted breaks: the cut at 65 bytes, which quoted the whole long
// condition; the cut at byte 64 whatever it holds, which split the ü
// across it into \xc3.
func TestConditionNote(t *testing.T) {
	long := strings.Repeat("a", 65)
	for cond, want := range map[string]string{
		"":                  "",
		"if $CI_COMMIT_TAG": ` when "if $CI_COMMIT_TAG"`,
		"build: [a]":        ` when "build: [a]"`,
		long[:64]:           ` when "` + long[:64] + `"`,
		long:                ` when "` + long[:64] + `"...`,
		long[:63] + "üü":    ` when "` + long[:63] + `"...`,
		long[:62] + "üü":    ` when "` + long[:62] + `ü"...`,
	} {
		if got := (Value{Condition: cond}).ConditionNote(); got != want {
			t.Errorf("%q: %q, want %q", cond, got, want)
		}
	}
	if (&NoValue{Reason: "why"}).Error() != "why" {
		t.Error("NoValue's message is not its reason")
	}
}

// ConditionNote's cut at its boundaries: a condition of 63 or 64 bytes is
// quoted whole and one of 65 is cut to 64; a character that ends at byte
// 64 is kept, and one across it is dropped whole. A condition with no
// character start in its first 64 bytes, which no valid UTF-8 has, is cut
// to nothing rather than read before its start. Planted breaks: the cut
// from 64 bytes, which read byte 64 of the 64-byte condition and panicked;
// the back-off down to end >= 0, which cut the continuation bytes at -1
// and panicked; the empty test negated.
func TestConditionNoteBoundaries(t *testing.T) {
	a := strings.Repeat("a", 65)
	for _, c := range []struct{ cond, want string }{
		{"", ""},
		{a[:63], ` when "` + a[:63] + `"`},
		{a[:64], ` when "` + a[:64] + `"`},
		{a, ` when "` + a[:64] + `"...`},
		{a[:62] + "ü", ` when "` + a[:62] + `ü"`},
		{a[:62] + "üb", ` when "` + a[:62] + `ü"...`},
		{a[:63] + "üb", ` when "` + a[:63] + `"...`},
		{a[:61] + "€b", ` when "` + a[:61] + `€"...`},
		{a[:62] + "€", ` when "` + a[:62] + `"...`},
		{strings.Repeat("\x80", 65), ` when ""...`},
	} {
		if got := (Value{Condition: c.cond}).ConditionNote(); got != c.want {
			t.Errorf("%d bytes %q: %q, want %q", len(c.cond), c.cond, got, c.want)
		}
	}
}
