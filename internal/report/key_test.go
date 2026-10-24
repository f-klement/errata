package report

import (
	"testing"

	"github.com/f-klement/errata/internal/check"
)

// The text and SARIF reports name a value's key beside its location, and
// a value with no key as before.
func TestReportsNameTheKey(t *testing.T) {
	keyed := named{value: check.Value{Text: "8080", At: check.Location{File: ".env", Line: 2}, Key: "API_PORT"}, extractor: "env"}
	plainValue := named{value: check.Value{Text: "8080", At: check.Location{File: "values.yaml", Line: 1}}, extractor: "chart"}
	for _, c := range []struct{ got, want string }{
		{where(keyed), `"8080" at .env:2 key "API_PORT" (env)`},
		{where(plainValue), `"8080" at values.yaml:1 (chart)`},
		{label(keyed), `"8080" key "API_PORT" (env)`},
		{label(plainValue), `"8080" (chart)`},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
