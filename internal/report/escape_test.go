package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
)

// actedOnRunes is one of each kind of rune a terminal acts on that the JSON
// and XML encoders write raw: DEL, C1 (NEL, CSI, ST, OSC), the direction
// controls, and the line and paragraph separators.
var actedOnRunes = string([]rune{
	0x7f, 0x85, 0x9b, 0x9c, 0x9d,
	0x061c, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069,
	0x2028, 0x2029,
})

// rawActedOn returns the first rune of out, but a newline, that a terminal
// acts on, or -1 when there is none.
func rawActedOn(out []byte) rune {
	for _, r := range string(out) {
		if r != '\n' && isActedOn(r) {
			return r
		}
	}
	return -1
}

// JUnit and SARIF go to stdout, so a CI log or a terminal shows them. Each
// writes the runes a terminal acts on in its own escape, and a reader of the
// format gets the text back as it was.
func TestJUnitAndSARIFEscapeWhatATerminalActsOn(t *testing.T) {
	payload := "a" + actedOnRunes + "z"
	rep := check.Report{Results: []check.Result{
		{Check: check.Check{ID: "p", Scope: check.ScopeRepo, Reason: "why " + payload, Run: []string{"x"}}, Verdict: check.VerdictFalse, Message: payload},
		{Check: check.Check{ID: "s", Scope: check.ScopeLive, Reason: "r"}, Gap: payload},
	}}

	var junit bytes.Buffer
	if err := (JUnit{}).Write(&junit, rep); err != nil {
		t.Fatal(err)
	}
	if r := rawActedOn(junit.Bytes()); r >= 0 {
		t.Errorf("JUnit holds %U raw:\n%s", r, junit.Bytes())
	}
	if !strings.Contains(junit.String(), "&#x9B;") || !strings.Contains(junit.String(), "&#x202E;") {
		t.Errorf("JUnit does not write CSI and U+202E as character references:\n%s", junit.Bytes())
	}
	s := decode(t, junit.Bytes())
	if got := s.Cases[0].Failure; got == nil || got.Message != payload || got.Body != "why "+payload {
		t.Errorf("JUnit failure reads back as %+v, want the message and reason as written", got)
	}
	if got := s.Cases[1].Skipped; got == nil || got.Message != payload {
		t.Errorf("JUnit skipped reads back as %+v, want the gap as written", got)
	}

	b := writeSARIF(t, rep)
	if r := rawActedOn(b); r >= 0 {
		t.Errorf("SARIF holds %U raw:\n%s", r, b)
	}
	if !bytes.Contains(b, []byte(`\u009b`)) || !bytes.Contains(b, []byte(`\u202e`)) {
		t.Errorf("SARIF does not write CSI and U+202E as JSON escapes:\n%s", b)
	}
	out := decodeSARIF(t, b)
	if got := out.Runs[0].Results[0].Message.Text; got != payload {
		t.Errorf("SARIF message reads back as %q, want %q", got, payload)
	}
}
