package report

import (
	"bytes"
	"encoding/xml"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

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

// When skips fail the run, the coverage line says so, since "0 errata, 0
// unknown" alone reads as a pass over a run that exits 1.
func TestTextSkipsFail(t *testing.T) {
	rep := check.Report{Results: []check.Result{sample.Results[0], sample.Results[3]}, SkipsFail: true}
	var buf bytes.Buffer
	if err := (Text{}).Write(&buf, rep); err != nil {
		t.Fatal(err)
	}
	want := `0 errata, 0 unknown. Coverage: ran 1, skipped 1. A skip fails this run.
  skipped owner (applied): no provider can read and predict a live system
`
	if got := buf.String(); got != want {
		t.Fatalf("text output differs.\ngot:\n%s\nwant:\n%s", got, want)
	}
	buf.Reset()
	if err := (Text{}).Write(&buf, check.Report{Results: sample.Results[:1], SkipsFail: true}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "0 errata, 0 unknown. Coverage: ran 1, skipped 0.\n" {
		t.Fatalf("a run with no skip says more than its counts: %q", got)
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

// When skips fail the run, a skipped check is an <error> naming its gap, so
// the suite counts a failure where the exit code does.
func TestJUnitSkipsFail(t *testing.T) {
	rep := sample
	rep.SkipsFail = true
	var buf bytes.Buffer
	if err := (JUnit{}).Write(&buf, rep); err != nil {
		t.Fatal(err)
	}
	s := decode(t, buf.Bytes())
	if s.Tests != 4 || s.Failures != 1 || s.Errors != 2 || s.Skipped != 0 {
		t.Fatalf("counts = %+v", s)
	}
	c := s.Cases[3]
	if c.Skipped != nil || c.Error == nil || c.Error.Message != "skipped: no provider can read and predict a live system" || c.Error.Body != "r" {
		t.Fatalf("case = %+v", c)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestWriteErrorsPropagate(t *testing.T) {
	for name, r := range Formats {
		if err := r("v1.2.3").Write(failingWriter{}, sample); err == nil {
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
	rep := check.Report{SkipsFail: rapid.Bool().Draw(t, "skips fail")}
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
// Each case with a value left out says so in its system-out.
func TestPropertyJUnitIsValidAndCountsAgree(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rep := genSARIFReport(t)
		var buf bytes.Buffer
		if err := (JUnit{}).Write(&buf, rep); err != nil {
			t.Fatal(err)
		}
		if r := rawActedOn(buf.Bytes()); r >= 0 {
			t.Fatalf("output holds %U raw, which a terminal acts on", r)
		}
		s := decode(t, buf.Bytes())
		ran, skipped := rep.Coverage()
		failing := rep.FailingSkips()
		if s.Tests != len(rep.Results) || s.Skipped != skipped-failing || s.Failures != len(rep.Errata()) || s.Errors != len(rep.Unknowns())+failing || len(s.Cases) != ran+skipped {
			t.Fatalf("suite %+v disagrees with report (ran %d, skipped %d, errata %d, unknown %d, skips fail %v)", s, ran, skipped, len(rep.Errata()), len(rep.Unknowns()), rep.SkipsFail)
		}
		// The suite fails exactly when the run does.
		if (s.Failures+s.Errors > 0) != rep.Failed() {
			t.Fatalf("suite %+v, but the run's Failed() is %v", s, rep.Failed())
		}
		for i, c := range s.Cases {
			left := len(leftOut(rep.Results[i]))
			if strings.HasPrefix(c.SystemOut, "skip_not_literal left out these values") != (left > 0) {
				t.Fatalf("case %d has %d values left out and system-out %q", i, left, c.SystemOut)
			}
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
		if strings.Contains(buf.String(), ". A skip fails this run.\n") != (rep.FailingSkips() > 0) {
			t.Fatalf("SkipsFail %v with %d skips, output:\n%s", rep.SkipsFail, rep.FailingSkips(), buf.String())
		}
	})
}

// A message, a reason or a gap can come from a repository file or a program's
// output, so the text output writes each control character as its escape:
// a file cannot clear the screen, set a link or overwrite a FALSE line.
func TestTextQuotesControlCharacters(t *testing.T) {
	rep := check.Report{Results: []check.Result{
		{
			Check:   check.Check{ID: "x", Scope: check.ScopeRepo, Reason: "why\x1b[2J\x00"},
			Verdict: check.VerdictFalse,
			Message: "\x1b]8;;https://evil.example\x07click\x1b]8;;\x07 ok\rTRUE \u009b31m \xff\xfe tab\there\r\nnext\x7f",
		},
		{Check: check.Check{ID: "y", Scope: check.ScopeLive, Reason: "r"}, Gap: "gap\x1bc"},
		// Direction controls reorder what a terminal or a web log shows, and
		// a line or paragraph separator breaks a line where none is seen.
		{
			Check:   check.Check{ID: "z\u202e", Scope: check.ScopeRepo, Reason: "\u202atrue\u202c \u202b\u202d"},
			Verdict: check.VerdictUnknown,
			Message: "\u2066a\u2067b\u2068c\u2069 \u200e\u200f\u061c line\u2028para\u2029end",
		},
		{Check: check.Check{ID: "w", Scope: check.ScopeLive, Reason: "r"}, Gap: "gap\u202eevil"},
	}}
	var buf bytes.Buffer
	if err := (Text{}).Write(&buf, rep); err != nil {
		t.Fatal(err)
	}
	want := `FALSE x (repo)
  why:  why\x1b[2J\x00
  said: \x1b]8;;https://evil.example\aclick\x1b]8;;\a ok\rTRUE \u009b31m \xff\xfe tab` + "\t" + `here
        next\x7f

UNKNOWN z\u202e (repo)
  why:  \u202atrue\u202c \u202b\u202d
  said: \u2066a\u2067b\u2068c\u2069 \u200e\u200f\u061c line\u2028para\u2029end

1 erratum, 1 unknown. Coverage: ran 2, skipped 2.
  skipped y (live): gap\x1bc
  skipped w (live): gap\u202eevil
`
	if got := buf.String(); got != want {
		t.Fatalf("text output differs.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// Whatever bytes a check's fields hold, the text output is UTF-8 with no
// control character but newline and tab, and text without one passes through
// unchanged.
func TestPropertyTextQuotesControls(t *testing.T) {
	// Arbitrary bytes rarely spell a C1 character, which takes two, or a
	// direction control, which takes three, so half the strings mix
	// printable text with the runes a terminal acts on.
	bytesOf := rapid.OneOf(
		rapid.Custom(func(t *rapid.T) string { return string(rapid.SliceOf(rapid.Byte()).Draw(t, "b")) }),
		rapid.StringOf(rapid.OneOf(acted, rapid.RuneFrom([]rune("aZ 9\\\n\t")))),
	)
	rapid.Check(t, func(t *rapid.T) {
		res := check.Result{
			Check:   check.Check{ID: bytesOf.Draw(t, "id"), Scope: check.Scope(bytesOf.Draw(t, "scope")), Reason: bytesOf.Draw(t, "reason")},
			Verdict: check.VerdictUnknown,
			Message: bytesOf.Draw(t, "message"),
			Extracted: []check.Extracted{{Extractor: bytesOf.Draw(t, "extractor"), LeftOut: []check.Value{{
				Text: bytesOf.Draw(t, "left out"), At: check.Location{File: bytesOf.Draw(t, "file")}, NotLiteral: true,
			}}}},
		}
		skip := check.Result{Check: check.Check{ID: "s", Scope: check.ScopeLive}, Gap: bytesOf.Draw(t, "gap")}
		var buf bytes.Buffer
		if err := (Text{}).Write(&buf, check.Report{Results: []check.Result{res, skip}}); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !utf8.ValidString(out) {
			t.Fatalf("output is not UTF-8: %q", out)
		}
		for _, r := range out {
			if r != '\n' && r != '\t' && isActedOn(r) {
				t.Fatalf("output holds %U, which a terminal acts on: %q", r, out)
			}
		}
		plain := rapid.StringMatching(`[ -~\p{L}\p{N}]{0,20}`).Draw(t, "plain")
		if got := QuoteControls(plain); got != plain {
			t.Fatalf("QuoteControls(%q) = %q, want it unchanged", plain, got)
		}
	})
}

// acted draws the runes a terminal or a viewer acts on instead of showing:
// control characters, direction controls, and line and paragraph separators.
var acted = rapid.RuneFrom(nil, unicode.Cc, unicode.Bidi_Control, unicode.Zl, unicode.Zp)

// isActedOn is the test's own statement of what no output may hold raw,
// written apart from the reporters so a change to theirs does not move it.
func isActedOn(r rune) bool {
	return unicode.In(r, unicode.Cc, unicode.Bidi_Control, unicode.Zl, unicode.Zp)
}

func FuzzJUnit(f *testing.F) {
	f.Add("ok", "message", "reason")
	f.Add("x", "\x00\x1b]8;;", "<![CDATA[")
	f.Add("x", actedOnRunes, "\xc2\x9b31m")
	f.Fuzz(func(t *testing.T, id, msg, reason string) {
		rep := check.Report{Results: []check.Result{{
			Check:   check.Check{ID: id, Reason: reason, Scope: check.ScopeRepo},
			Verdict: check.VerdictFalse, Message: msg,
		}}}
		var buf bytes.Buffer
		if err := (JUnit{}).Write(&buf, rep); err != nil {
			t.Fatal(err)
		}
		if r := rawActedOn(buf.Bytes()); r >= 0 {
			t.Fatalf("output holds %U raw, which a terminal acts on", r)
		}
		decode(t, buf.Bytes())
	})
}
