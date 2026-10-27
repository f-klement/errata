package core

import (
	"strings"
	"testing"
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
