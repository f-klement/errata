package report

import (
	"bytes"
	"encoding/xml"
	"errors"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
)

var sample = check.Report{Results: []check.Result{
	{Check: check.Check{ID: "ok", Scope: check.ScopeRepo, Reason: "r"}, Verdict: check.VerdictTrue},
	{Check: check.Check{ID: "ci-node", Scope: check.ScopeRepo, Reason: "CI moved.\nDocs must follow."}, Verdict: check.VerdictFalse, Message: "says atlas, runs on builder"},
	{Check: check.Check{ID: "broken", Scope: check.ScopeRepo, Reason: "r"}, Verdict: check.VerdictUnknown},
	{Check: check.Check{ID: "owner", Scope: check.ScopeApplied, Reason: "r"}, Gap: "no provider can read and predict a live system"},
}}

func TestText(t *testing.T) {
	var buf bytes.Buffer
	if err := (Text{}).Write(&buf, sample); err != nil {
		t.Fatal(err)
	}
	want := `FALSE ci-node (repo)
  why:  CI moved.
        Docs must follow.
  said: says atlas, runs on builder

UNKNOWN broken (repo)
  why:  r

1 erratum, 1 unknown. Coverage: ran 3, skipped 1.
  skipped owner (applied): no provider can read and predict a live system
`
	if got := buf.String(); got != want {
		t.Fatalf("text output differs.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestTextSaysErratumForOne(t *testing.T) {
	cases := map[int]string{0: "0 errata", 1: "1 erratum", 2: "2 errata"}
	for n, want := range cases {
		if got := errataCount(n); got != want {
			t.Errorf("errataCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestTextEmptyReportStillPrintsCoverage(t *testing.T) {
	var buf bytes.Buffer
	if err := (Text{}).Write(&buf, check.Report{}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "0 errata, 0 unknown. Coverage: ran 0, skipped 0.\n" {
		t.Fatalf("got %q", got)
	}
}

func TestJUnit(t *testing.T) {
	var buf bytes.Buffer
	if err := (JUnit{}).Write(&buf, sample); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), "</testsuite>\n") {
		t.Errorf("output does not end with a newline after the suite: %q", buf.String()[max(0, buf.Len()-20):])
	}
	s := decode(t, buf.Bytes())
	if s.Tests != 4 || s.Failures != 1 || s.Errors != 1 || s.Skipped != 1 {
		t.Fatalf("counts = %+v", s)
	}
	c := s.Cases[1]
	if c.Name != "ci-node" || c.Classname != "errata.repo" || c.Failure == nil || c.Failure.Message != "says atlas, runs on builder" {
		t.Fatalf("case = %+v", c)
	}
	if !strings.Contains(c.Failure.Body, "CI moved.") {
		t.Fatalf("failure body lacks the reason: %q", c.Failure.Body)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestWriteErrorsPropagate(t *testing.T) {
	for name, r := range Formats {
		if err := r.Write(failingWriter{}, sample); err == nil {
			t.Errorf("%s: write error swallowed", name)
		}
	}
}

func TestFormatNames(t *testing.T) {
	if got := strings.Join(FormatNames(), ","); got != "junit,sarif,text" {
		t.Fatalf("FormatNames = %s", got)
	}
}

// fataler is the part of testing.TB that *rapid.T also has.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

func decode(t fataler, b []byte) junitSuite {
	t.Helper()
	var s junitSuite
	if err := xml.Unmarshal(b, &s); err != nil {
		t.Fatalf("output is not valid XML: %v\n%s", err, b)
	}
	return s
}

func genReport(t *rapid.T) check.Report {
	n := rapid.IntRange(0, 15).Draw(t, "n")
	rep := check.Report{}
	for range n {
		r := check.Result{
			Check: check.Check{
				ID:     rapid.StringMatching(`[a-z]{1,6}`).Draw(t, "id"),
				Scope:  rapid.SampledFrom([]check.Scope{check.ScopeRepo, check.ScopeLive, check.ScopeApplied}).Draw(t, "scope"),
				Reason: rapid.String().Draw(t, "reason"),
			},
			Message:  rapid.String().Draw(t, "message"),
			Duration: time.Duration(rapid.Int64Range(0, int64(time.Hour)).Draw(t, "dur")),
		}
		if rapid.Bool().Draw(t, "skip") {
			r.Gap = rapid.StringMatching(`[a-z ]{1,10}`).Draw(t, "why")
		} else {
			r.Verdict = rapid.SampledFrom([]check.Verdict{check.VerdictTrue, check.VerdictFalse, check.VerdictUnknown}).Draw(t, "v")
		}
		rep.Results = append(rep.Results, r)
	}
	return rep
}

// Whatever strings a check prints, the JUnit output is valid XML whose counts
// agree with the report. A CI system that cannot parse the file shows nothing.
func TestPropertyJUnitIsValidAndCountsAgree(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rep := genReport(t)
		var buf bytes.Buffer
		if err := (JUnit{}).Write(&buf, rep); err != nil {
			t.Fatal(err)
		}
		s := decode(t, buf.Bytes())
		ran, skipped := rep.Coverage()
		if s.Tests != len(rep.Results) || s.Skipped != skipped || s.Failures != len(rep.Errata()) || s.Errors != len(rep.Unknowns()) || len(s.Cases) != ran+skipped {
			t.Fatalf("suite %+v disagrees with report (ran %d, skipped %d, errata %d, unknown %d)", s, ran, skipped, len(rep.Errata()), len(rep.Unknowns()))
		}
	})
}

// The text output always ends with the coverage line, and names every
// skipped check.
func TestPropertyTextAlwaysPrintsCoverage(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rep := genReport(t)
		var buf bytes.Buffer
		if err := (Text{}).Write(&buf, rep); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "Coverage: ran ") {
			t.Fatal("no coverage line")
		}
		for _, r := range rep.Results {
			if r.Skipped() && !strings.Contains(buf.String(), "skipped "+r.Check.ID+" ") {
				t.Fatalf("skipped check %s not named", r.Check.ID)
			}
		}
	})
}

func FuzzJUnit(f *testing.F) {
	f.Add("ok", "message", "reason")
	f.Add("x", "\x00\x1b]8;;", "<![CDATA[")
	f.Fuzz(func(t *testing.T, id, msg, reason string) {
		rep := check.Report{Results: []check.Result{{
			Check:   check.Check{ID: id, Reason: reason, Scope: check.ScopeRepo},
			Verdict: check.VerdictFalse, Message: msg,
		}}}
		var buf bytes.Buffer
		if err := (JUnit{}).Write(&buf, rep); err != nil {
			t.Fatal(err)
		}
		decode(t, buf.Bytes())
	})
}
