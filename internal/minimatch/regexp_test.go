package minimatch

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// regexp.go answers what JavaScript's RegExp answers, for each case of
// testdata/regex.json: random sources of the escapes, classes, quantifiers,
// groups and lookaheads the matcher reads, under no flag, i, u and both,
// against random strings, lone surrogates and the ends of each range the
// matcher compares included, each with the answer testdata/regex.mjs gave
// under Node. Where new RegExp throws, compileJS returns a SyntaxError.
// minimatch builds every source with the i flag and few of these forms;
// the cases hold the rest of the grammar to the specification too.
func TestRegexAgreesWithJavaScript(t *testing.T) {
	data, err := os.ReadFile("testdata/regex.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][4]json.RawMessage
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	counts := map[any]int{}
	for _, c := range cases {
		src, in := jsonUnits(t, c[0]), jsonUnits(t, c[2])
		var flags string
		var want any
		if err := errors.Join(json.Unmarshal(c[1], &flags), json.Unmarshal(c[3], &want)); err != nil {
			t.Fatal(err)
		}
		var answer any
		r, err := compileJS(src, strings.Contains(flags, "i"), strings.Contains(flags, "u"))
		var thrown *SyntaxError
		switch {
		case errors.As(err, &thrown):
			answer = "throw"
		case err != nil:
			t.Errorf("/%s/%s: %v; JavaScript says %v", c[0], flags, err, want)
			continue
		default:
			answer, err = r.test(in, &budget{left: MaxSteps})
			if err != nil {
				t.Errorf("/%s/%s against %s: %v; JavaScript says %v", c[0], flags, c[2], err, want)
				continue
			}
		}
		counts[answer]++
		if answer != want {
			t.Errorf("/%s/%s against %s = %v; JavaScript says %v", c[0], flags, c[2], answer, want)
		}
	}
	for _, a := range []any{true, false, "throw"} {
		if counts[a] < 300 {
			t.Errorf("%d cases answer %v", counts[a], a)
		}
	}
}

// regex.json leaves out the two forms the matcher does not read, which no
// glob builds: a backreference, and Annex B's octal escape. Each is refused
// with its reason, rather than read as something else.
func TestRegexRefusesBackreferencesAndOctal(t *testing.T) {
	for src, want := range map[string]string{`a\1`: "backreferences are not read", `a\9`: "backreferences are not read", `\01`: "octal escapes are not read"} {
		_, err := compileJS(toUnits(src), true, false)
		var thrown *SyntaxError
		if !errors.As(err, &thrown) || thrown.Reason != want {
			t.Errorf("/%s/i: %v; want %q", src, err, want)
		}
	}
}
