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

// OriginNote names the reader and the part of its file after a value's
// location, as the reader wrote it, and Notes puts the key, the origin and
// the condition in that order. Planted breaks: OriginNote quoting the
// origin, which wrote job "deploy" as job \"deploy\"; Notes without the
// origin; the origin after the condition.
func TestOriginNote(t *testing.T) {
	if got := (Value{}).OriginNote(); got != "" {
		t.Errorf("no origin: %q, want none", got)
	}
	v := Value{Origin: `gitlab-ci job "deploy"`, Condition: "if $A", Key: "K", HasKey: true}
	if got, want := v.OriginNote(), ` from gitlab-ci job "deploy"`; got != want {
		t.Errorf("OriginNote: %q, want %q", got, want)
	}
	if got, want := v.Notes(), ` key "K" from gitlab-ci job "deploy" when "if $A"`; got != want {
		t.Errorf("Notes: %q, want %q", got, want)
	}
}

// A location names its file and line, and the file alone when the line is
// below 1, as for a value that holds for the whole file. Planted breaks: the
// test at Line < 0, which printed f.yaml:0; at Line < 2, which dropped
// line 1.
func TestLocationString(t *testing.T) {
	for _, c := range []struct {
		at   Location
		want string
	}{
		{Location{File: "f.yaml", Line: 3}, "f.yaml:3"},
		{Location{File: "f.yaml", Line: 1}, "f.yaml:1"},
		{Location{File: "f.yaml"}, "f.yaml"},
		{Location{File: "f.yaml", Line: -1}, "f.yaml"},
	} {
		if got := c.at.String(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.at, got, c.want)
		}
	}
}

// Inside reads a path as text under two readings, Linux's and Windows's,
// and refuses one outside under either; OutsideOnWindows marks the paths
// only Windows's reading refuses. Each case names what it pins: the empty
// path; a leading slash or backslash; . passed over, so ./../x climbs out;
// a .. at depth 1 that ends at the root (a/..), and one below it; a drive
// as the first name, after . or .., and as a later name; each end of both
// letter ranges and the byte just past it; a one byte name; a device name.
// Planted breaks, each in local or isDrive: a backslash not a separator
// under Windows's reading; . read as a name; depth < 0 as <= 0; .. counted
// up, a name counted down; the drive refused at any depth; a drive's
// letter not tested; each bound of the letter ranges moved by one; a one
// byte name read as a drive.
func TestLocationInside(t *testing.T) {
	for _, c := range []struct {
		file             string
		inside, onlyWins bool
	}{
		{"", false, false},
		{"/x", false, false},
		{`\x`, false, true},
		{`\\host\share\x`, false, true},
		{"a/b.yaml", true, false},
		{".", true, false},
		{"./c", true, false},
		{"a/..", true, false},
		{"a/../b", true, false},
		{"a/b/../..", true, false},
		{"..", false, false},
		{"../x", false, false},
		{"./../x", false, false},
		{"a/./../../x", false, false},
		{`a\..\b`, true, false},
		{`a\..\..\x`, false, true},
		{`..\x`, false, true},
		{`a\b/../..`, false, false},
		{"C:x", false, true},
		{`C:\x`, false, true},
		{"C:", false, true},
		{"./C:x", false, true},
		{"a/../C:x", false, true},
		{"a/b/../../C:", false, true},
		{`a\..\c:\x`, false, true},
		{"b/c:d", true, false},
		{"a:x", false, true},
		{"z:x", false, true},
		{"A:x", false, true},
		{"Z:x", false, true},
		{"`:x", true, false},
		{"{:x", true, false},
		{"@:x", true, false},
		{"[:x", true, false},
		{"1:x", true, false},
		{"c", true, false},
		{":", true, false},
		{"aux.yaml", true, false},
		{"dir/con.txt", true, false},
	} {
		l := Location{File: c.file}
		if got := l.Inside(); got != c.inside {
			t.Errorf("Inside(%q) = %v, want %v", c.file, got, c.inside)
		}
		if got := l.OutsideOnWindows(); got != c.onlyWins {
			t.Errorf("OutsideOnWindows(%q) = %v, want %v", c.file, got, c.onlyWins)
		}
	}
	marked := Location{File: "a.yaml", Outside: true}
	if marked.Inside() || marked.OutsideOnWindows() {
		t.Errorf("a location marked outside: Inside %v, OutsideOnWindows %v, want both false", marked.Inside(), marked.OutsideOnWindows())
	}
}

// HoldsControl finds a C0 control, DEL and a C1 control such as NEL
// (U+0085) anywhere in a string, and nothing else: a space, a backslash, a
// letter outside ASCII, U+00A0 just past the C1 range, and a line
// separator, which the text report escapes. Planted break: the check
// reduced to a newline alone, which passed the rest.
func TestHoldsControl(t *testing.T) {
	for s, want := range map[string]bool{
		"a\nb": true, "\r": true, "a\x1b[2J": true, "\t": true, "\x00": true, "a\x7f": true, "a\u0085b": true, "\u009f": true,
		"": false, "a b.yaml": false, `a\nb`: false, "é.yaml": false, "\u00a0": false, "\u2028": false,
	} {
		if got := HoldsControl(s); got != want {
			t.Errorf("HoldsControl(%q) = %v, want %v", s, got, want)
		}
	}
}
