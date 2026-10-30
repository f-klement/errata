package core

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
)

// CheckVersion lets a version through and refuses an empty one and one
// with a character a terminal acts on, naming the line. Each refusal is a
// planted break of the check.
func TestCheckVersion(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"1.2.3", ""},
		{"lts/*", ""},
		{"", "line 4: the version is empty"},
		{"1.0\a", "line 4: the version \"1.0\\a\" holds a control character"},
		{"1.0\u202e", "line 4: the version"},
		{"1.0\u2028", "line 4: the version"},
		{"1.0\xff", "line 4: the version"},
	} {
		err := CheckVersion(4, c.text)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%q: %v", c.text, err)
		case c.want != "" && (err == nil || !strings.HasPrefix(err.Error(), c.want)):
			t.Errorf("%q: %v, want %q", c.text, err, c.want)
		}
	}
}

// CheckVersion refuses every rune check.ActedOn names, the set the location
// guard refuses and the reports escape, and U+FFFD, and no other. Every
// rune is tried. Planted break: actedOn letting U+200E through, as a copy
// of the set that drifted from the guard's would.
func TestCheckVersionRefusesWhatTheGuardRefuses(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if 0xd800 <= r && r <= 0xdfff {
			continue
		}
		want := check.ActedOn(r) || r == utf8.RuneError
		if got := CheckVersion(1, "1"+string(r)) != nil; got != want {
			t.Errorf("CheckVersion refuses %U: %v, want %v", r, got, want)
		}
	}
}
