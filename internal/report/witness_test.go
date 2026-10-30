package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
)

// witnessed is a report of a check that held, a witness that held, one the
// check held on and one that was unknown.
func witnessed() check.Report {
	c := check.Check{ID: "c", Reason: "r", Scope: check.ScopeRepo, Kind: &check.Program{Run: []string{"x"}}, Witness: []string{"a", "b", "d"}}
	return check.Report{Results: []check.Result{
		{Check: c, Verdict: check.VerdictTrue},
		{Check: c, Verdict: check.VerdictTrue, Witness: "a", Message: "c is false on its witness a, as it must be"},
		{Check: c, Verdict: check.VerdictFalse, Witness: "b", Message: "c held on its witness b, so it could not be shown to fail"},
		{Check: c, Verdict: check.VerdictUnknown, Witness: "d", Message: "did not finish within 1s"},
	}}
}

// The text report names a witness's result id [witness name], counts the
// witnesses held after the checks, and says nothing of witnesses for a
// run with none. The planted breaks: the header with the check's id
// alone; the count left out; the count written for a run with none.
func TestTextNamesWitnesses(t *testing.T) {
	var buf bytes.Buffer
	if err := (Text{}).Write(&buf, witnessed()); err != nil {
		t.Fatal(err)
	}
	want := `FALSE c [witness b] (repo)
  why:  r
  said: c held on its witness b, so it could not be shown to fail

UNKNOWN c [witness d] (repo)
  why:  r
  said: did not finish within 1s

1 erratum, 1 unknown. Coverage: ran 1, skipped 0. 1 witness held.
`
	if got := buf.String(); got != want {
		t.Errorf("text output differs.\ngot:\n%s\nwant:\n%s", got, want)
	}
	buf.Reset()
	if err := (Text{}).Write(&buf, check.Report{Results: witnessed().Results[:1]}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); strings.Contains(got, "witness") {
		t.Errorf("a run without witnesses says %q", got)
	}
}

// JUnit writes each witness as a testcase id [witness name]. The planted
// break: the testcase named by the check's id.
func TestJUnitNamesWitnesses(t *testing.T) {
	var buf bytes.Buffer
	if err := (JUnit{}).Write(&buf, witnessed()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`tests="4" failures="1" errors="1"`,
		`<testcase name="c [witness a]"`,
		`<testcase name="c [witness b]" classname="errata.repo" time="0">` + "\n" + `    <failure message="c held on its witness b, so it could not be shown to fail">r</failure>`,
		`<testcase name="c [witness d]"`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("JUnit\n%s\nlacks %q", buf.String(), want)
		}
	}
}

// SARIF puts a held witness's erratum at the check, with a message that
// names it, and names the witness in an unknown's notification. The
// planted break: the notification naming the check's id alone.
func TestSARIFNamesWitnesses(t *testing.T) {
	out := decodeSARIF(t, writeSARIF(t, witnessed()))
	res := out.Runs[0].Results
	if len(res) != 1 || res[0].RuleID != "c" || res[0].Locations[0].where() != "errata.yaml:1" ||
		res[0].Message.Text != "c held on its witness b, so it could not be shown to fail" {
		t.Errorf("results %+v, want the erratum of witness b at the check", res)
	}
	notes := out.Runs[0].Invocations[0].Notifications
	if len(notes) != 1 || !strings.HasPrefix(notes[0].Message.Text, "check c [witness d] is unknown: did not finish within 1s") {
		t.Errorf("notifications %+v, want the unknown of witness d", notes)
	}
}

// errata list prints the fixtures a check names under witness, in JSON
// always and in text when there are any. The planted breaks: the JSON
// key dropped; the text line dropped; the text line written for a check
// that names none.
func TestListWitnesses(t *testing.T) {
	checks := listChecks()
	checks[0].Witness = []string{"a", "b"}
	if got := listedCheck(t, checks, 0)["witness"]; len(got.([]any)) != 2 {
		t.Errorf("JSON witness %v, want [a b]", got)
	}
	if got := listedCheck(t, checks, 1)["witness"]; got == nil || len(got.([]any)) != 0 {
		t.Errorf("JSON witness %v of a check with none, want []", got)
	}
	var b bytes.Buffer
	if err := (ListText{}).WriteList(&b, checks, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(b.String(), "  witness: "); n != 1 || !strings.Contains(b.String(), "  witness: a, b\n") {
		t.Errorf("text listing\n%s\nwant one line witness: a, b", b.String())
	}
}
