package minimatch

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"testing"
)

// braceExpand and unescape answer what minimatch's own exports of the same
// name answer, for each case of testdata/parts.json, which
// testdata/parts.mjs wrote from minimatch 10.2.6: random strings of braces,
// sets, sequences of every form, escapes and $, and of the brackets and
// backslashes unescape reads. The brace cases are those inside the port's
// bounds, where it gives what brace-expansion gives.
func TestPartsAgreeWithMinimatch(t *testing.T) {
	data, err := os.ReadFile("testdata/parts.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		BraceExpand [][2]json.RawMessage `json:"braceExpand"`
		Unescape    [][2]json.RawMessage `json:"unescape"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases.BraceExpand) < 1000 || len(cases.Unescape) < 1000 {
		t.Fatalf("%d brace and %d unescape cases; the file holds over 1000 of each", len(cases.BraceExpand), len(cases.Unescape))
	}
	for _, c := range cases.BraceExpand {
		s := jsonUnits(t, c[0])
		var want []json.RawMessage
		if err := json.Unmarshal(c[1], &want); err != nil {
			t.Fatal(err)
		}
		got, err := braceExpand(s)
		if err != nil {
			t.Errorf("braceExpand(%s): %v", c[0], err)
			continue
		}
		if !slices.EqualFunc(got, want, func(g units, w json.RawMessage) bool { return slices.Equal(g, jsonUnits(t, w)) }) {
			var text []string
			for _, g := range got {
				text = append(text, fromUnits(g))
			}
			t.Errorf("braceExpand(%s) = %q; minimatch says %s", c[0], text, c[1])
		}
	}
	for _, c := range cases.Unescape {
		if got := unescape(jsonUnits(t, c[0])); !slices.Equal(got, jsonUnits(t, c[1])) {
			t.Errorf("unescape(%s) = %q; minimatch says %s", c[0], fromUnits(got), c[1])
		}
	}
}

// jsonUnits is the JSON string raw as JavaScript's code units. A lone
// surrogate, which JSON.stringify writes as a \u escape, stays a unit of
// its own, where encoding/json would make it U+FFFD.
func jsonUnits(t *testing.T, raw json.RawMessage) units {
	t.Helper()
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		t.Fatalf("%s is no JSON string", raw)
	}
	s := []rune(string(raw[1 : len(raw)-1]))
	var out units
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out = encodeRune(out, s[i])
			continue
		}
		i++
		switch s[i] {
		case 'u':
			v, err := strconv.ParseUint(string(s[i+1:i+5]), 16, 16)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, rune(v))
			i += 4
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		default:
			out = append(out, s[i])
		}
	}
	return out
}
