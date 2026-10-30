package minimatch

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// The port builds what minimatch builds, for each glob of cases.json in
// testdata/sources.json: the same marks for a negated, comment or empty
// glob, the same brace expansions, split into the same segments, each the
// same ** or string, or the same regex source with the same flags.
// testdata/sources.mjs wrote each from minimatch 10.2.6 itself, and
// "throw" where minimatch throws, which the port answers with a
// SyntaxError. A match can agree by chance where a source differs; this
// holds every step that builds one. A glob of cases.json with no row here
// fails, so a case added later is held to its source too: run sources.mjs
// on the globs of cases.json again.
func TestSegmentsAgreeWithMinimatch(t *testing.T) {
	data, err := os.ReadFile("testdata/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][2]json.RawMessage
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 4000 {
		t.Fatalf("%d globs; the file holds over 4000", len(cases))
	}
	built := map[string]bool{}
	for _, c := range cases {
		var glob string
		if err := json.Unmarshal(c[0], &glob); err != nil {
			t.Fatal(err)
		}
		built[glob] = true
		got, err := segmentsOf(glob)
		if err != nil {
			t.Errorf("%q: %v", glob, err)
			continue
		}
		var want any
		if err := json.Unmarshal(c[1], &want); err != nil {
			t.Fatal(err)
		}
		wantJSON, _ := json.Marshal(want)
		if string(got) != string(wantJSON) {
			t.Errorf("%q builds\n  %s\nminimatch builds\n  %s", glob, got, wantJSON)
		}
	}
	matched, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var matches [][3]any
	if err := json.Unmarshal(matched, &matches); err != nil {
		t.Fatal(err)
	}
	for _, c := range matches {
		if glob := c[1].(string); !built[glob] {
			t.Errorf("%q of cases.json has no row in sources.json", glob)
			built[glob] = true
		}
	}
}

// segmentsOf is the port's answer for glob in sources.mjs's form.
func segmentsOf(glob string) ([]byte, error) {
	m, err := compile(toUnits(glob))
	var thrown *SyntaxError
	switch {
	case errors.As(err, &thrown):
		return json.Marshal("throw")
	case err != nil:
		return nil, err
	}
	var marks strings.Builder
	for _, f := range []struct {
		on   bool
		mark string
	}{{m.negate, "n"}, {m.comment, "c"}, {m.empty, "e"}} {
		if f.on {
			marks.WriteString(f.mark)
		}
	}
	set := [][]string{}
	for _, segs := range m.set {
		row := []string{}
		for _, s := range segs {
			switch {
			case s.globstar:
				row = append(row, "**")
			case s.re == nil:
				row = append(row, "="+fromUnits(s.literal))
			default:
				flags := "i"
				if s.re.uflag {
					flags += "u"
				}
				src := s.re.source
				row = append(row, "/"+flags+"/"+fromUnits(src[1:len(src)-1]))
			}
		}
		set = append(set, row)
	}
	return json.Marshal([]any{marks.String(), set})
}
