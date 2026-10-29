package rule

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/semver"
)

// semver(s) orders versions by every operator, at full precision.
func TestSemverInCEL(t *testing.T) {
	for _, c := range []struct {
		expr string
		want bool
	}{
		{"semver(v[0]) < semver(v[1])", true},
		{"semver(v[0]) <= semver(v[1])", true},
		{"semver(v[0]) > semver(v[1])", false},
		{"semver(v[0]) >= semver(v[1])", false},
		{"semver(v[0]) == semver(v[1])", false},
		{"semver(v[0]) != semver(v[1])", true},
		{"semver(v[0]) == semver('v1.2.0+build')", true},
		{"semver('20') == semver('20.0.0')", true},
		{"semver('1.0.0-rc.1') < semver('1.0.0')", true},
		{"semver('3.12.0rc1') == semver('3.12.0-rc.1')", true},
		{"string(semver('v1.2')) == '1.2'", true},
		{"type(semver('1')) == type(semver('2'))", true},
		{"semver_satisfies(v[1], '^1.2')", true},
		{"semver_satisfies(v[1], '~> 2.0')", false},
		{"semver_satisfies('20', '^20.1')", true},
		{"semver_satisfies('2.0.0-beta.1', '>=1.0.0')", false},
		{"v.all(x, semver_satisfies(x, '>=1.2, <2'))", true},
	} {
		// Every expression uses v, as Compile requires.
		got, err := eval(t, "size(v) == 2 && ("+c.expr+")", "1.2", "1.10.0")
		if err != nil || got != c.want {
			t.Errorf("%s = %v, %v; want %v", c.expr, got, err, c.want)
		}
	}
}

// A value that is not a version or not a range makes the rule unknown,
// naming the value and the reason; one past the length limit too.
func TestSemverErrors(t *testing.T) {
	long := strings.Repeat("1", MaxArgument+1)
	for _, c := range []struct {
		expr, want string
	}{
		{"semver(v[0]) > semver('1.0')", `semver: "lts/iron" is not a version: it does not start with a number`},
		{"semver_satisfies(v[0], '>=1')", `semver_satisfies: "lts/iron" is not a version`},
		{"semver_satisfies('1.0.0', v[0])", `semver_satisfies: "lts/iron" is not a range`},
		{"semver_satisfies('1.0.0', '===1.0')", "=== compares text"},
		{"semver(v[1]) > semver('1.0')", "semver: the argument is 1025 bytes"},
		{"semver_satisfies(v[1], '*')", "semver_satisfies: an argument is 1025 bytes"},
		{"semver_satisfies('1', v[1])", "semver_satisfies: an argument is 1025 bytes"},
		{"semver('1.0') == semver('1.0') && semver(v[0]) == semver('1')", `"lts/iron" is not a version`},
	} {
		_, err := eval(t, "size(v) == 2 && ("+c.expr+")", "lts/iron", long)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one naming %q", c.expr, err, c.want)
		}
	}
	for _, expr := range []string{"semver(v[0]) < '2'", "int(semver(v[0])) == 1", "semver(v[0]) == '1'"} {
		if _, err := Compile(expr, []string{"v"}, Lists{}); err == nil || !strings.Contains(err.Error(), "no matching overload") {
			t.Errorf("%s loads: %v", expr, err)
		}
	}
}

func semverRule(extractors ...check.NamedExtractor) check.Check {
	c := agreeRule(extractors...)
	c.Rule.Compare = check.CompareSemver
	return c
}

// compare: semver over the owner's examples, with the message each gives.
// The failure message lists each value with its places and what it was
// read as.
func TestAgreeSemver(t *testing.T) {
	for _, c := range []struct {
		name    string
		a, b    []string
		verdict check.Verdict
		message string
	}{
		{"precision", []string{"20", "v20.11"}, []string{"20.11.1"}, check.VerdictTrue, ""},
		{"versions differ", []string{"20"}, []string{"18.19.0"}, check.VerdictFalse, `agree: the versions "20" and "18.19.0" differ at the precision both state
  "20" (version 20) at a.txt:1 (a)
  "18.19.0" (version 18.19.0) at b.txt:1 (b)`},
		{"range holds", []string{"20.11.1"}, []string{">=20", "^20.1"}, check.VerdictTrue, ""},
		{"range fails", []string{">=20"}, []string{"18.19.0", "18.19.0"}, check.VerdictFalse, `agree: the version "18.19.0" does not satisfy the range ">=20"
  ">=20" (range >=20.0.0) at a.txt:1 (a)
  "18.19.0" (version 18.19.0) at b.txt:1 (b), b.txt:2 (b)`},
		{"ranges overlap", []string{">=20"}, []string{"^20.1"}, check.VerdictTrue, ""},
		{"ranges apart", []string{">=21"}, []string{"~> 20.1"}, check.VerdictFalse, `agree: only ranges were compared, since no value is a version, and no release satisfies them all
  ">=21" (range >=21.0.0) at a.txt:1 (a)
  "~> 20.1" (range >=20.1.0 <21.0.0-0) at b.txt:1 (b)`},
		{"not a version", []string{"lts/iron", "20"}, []string{"latest"}, check.VerdictUnknown, `agree: compare semver reads every value as a version or a range, and cannot read 2 values
  "lts/iron" at a.txt:1 (a): it is neither a version nor a range: "lts/iron" is not a version: it does not start with a number
  "latest" at b.txt:1 (b): it is neither a version nor a range: "latest" is not a version: it does not start with a number`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var a, b []check.Value
			for i, s := range c.a {
				a = append(a, at(s, "a.txt", i+1))
			}
			for i, s := range c.b {
				b = append(b, at(s, "b.txt", i+1))
			}
			res := Run(context.Background(), "/repo", semverRule(named("a", fake{values: a}), named("b", fake{values: b})))
			if res.Verdict != c.verdict || res.Message != c.message {
				t.Errorf("verdict %s, message:\n%s\nwant %s:\n%s", res.Verdict, res.Message, c.verdict, c.message)
			}
			// Only a rule that held on ranges alone has a note.
			wantNote := ""
			if c.name == "ranges overlap" {
				wantNote = "agree: only ranges were compared, since no value is a version, and they overlap"
			}
			if res.Note != wantNote {
				t.Errorf("note %q, want %q", res.Note, wantNote)
			}
		})
	}
}

// refusesAfter reads as a semver.Reader does until its n values are read,
// then refuses the next as a Reader past semver.MaxIntervals does.
type refusesAfter struct{ n, read int }

func (r *refusesAfter) Parse(s string) (semver.Value, error) {
	r.read++
	if r.read > r.n {
		return semver.Value{}, &semver.BoundError{Values: r.read}
	}
	return semver.Parse(s)
}

// A value the reader refuses at its bound stops the comparison at once,
// naming the value, its places and the bound, and no value after it is read.
// semver's own tests reach the real bound by lowering it; this one stands in
// for a reader that reached it. The planted break: the bound's message
// changed, which this test names.
func TestAgreeSemverStopsAtTheBound(t *testing.T) {
	reader := &refusesAfter{n: 1}
	newSemverReader = func() semverReader { return reader }
	t.Cleanup(func() { newSemverReader = func() semverReader { return new(semver.Reader) } })
	res := Run(context.Background(), "/repo", semverRule(
		named("a", fake{values: []check.Value{at("^1", "a.txt", 1)}}),
		named("b", fake{values: []check.Value{at("^2", "b.txt", 1), at(">=3", "b.txt", 2)}})))
	want := `agree: compare semver stopped at "^2" at b.txt:1 (b): the ranges of the first 2 values hold more than 1000000 intervals, the most errata compares in one rule`
	if res.Verdict != check.VerdictUnknown || res.Message != want || res.Note != "" {
		t.Errorf("verdict %s, note %q, message:\n%s\nwant unknown:\n%s", res.Verdict, res.Note, res.Message, want)
	}
	if reader.read != 2 {
		t.Errorf("the reader read %d values; it must stop at the one it refused, the 2nd", reader.read)
	}
}

// normalize applies before a value is read as a version, and text compare
// is unchanged: "20" and "20.11.1" differ as text.
func TestAgreeSemverNormalizesAndTextStays(t *testing.T) {
	c := semverRule(named("a", fake{values: []check.Value{at(" V20 ", "a", 1)}}), named("b", fake{values: []check.Value{at("20.11.1", "b", 1)}}))
	c.Rule.Normalize = []check.Normalization{check.NormalizeTrim, check.NormalizeLower}
	if res := Run(context.Background(), "/repo", c); res.Verdict != check.VerdictTrue {
		t.Errorf("normalised semver: %s %s", res.Verdict, res.Message)
	}
	c.Rule.Compare = check.CompareText
	if res := Run(context.Background(), "/repo", c); res.Verdict != check.VerdictFalse {
		t.Errorf("text compare of 20 and 20.11.1: %s", res.Verdict)
	}
}

// The semver helpers read what every helper reads, and agree's semver
// compare the same length, so one value is never a version to one and too
// long for the other.
func TestSemverReadsWhatAHelperReads(t *testing.T) {
	if semver.MaxLength != MaxArgument {
		t.Errorf("semver.MaxLength is %d, MaxArgument %d", semver.MaxLength, MaxArgument)
	}
}

// A helper reads an argument of exactly MaxArgument bytes and refuses one
// byte more (correctness review, finding 6): the boundary of the check in
// satisfies.
func TestSemverSatisfiesReadsAtTheLimit(t *testing.T) {
	at := ">=1" + strings.Repeat(" ", MaxArgument-3)
	if got, err := eval(t, "semver_satisfies(v[0], v[1])", "1.2.0", at); err != nil || !got {
		t.Errorf("a range of %d bytes: %v, %v", len(at), got, err)
	}
	version := "1.2.0" + strings.Repeat(" ", MaxArgument-5)
	if got, err := eval(t, "semver_satisfies(v[0], v[1])", version, ">=1"); err != nil || !got {
		t.Errorf("a version of %d bytes: %v, %v", len(version), got, err)
	}
	if _, err := eval(t, "semver_satisfies(v[0], v[1])", "1.2.0", at+" "); err == nil || !strings.Contains(err.Error(), "an argument is 1025 bytes") {
		t.Errorf("a range of %d bytes: %v", len(at)+1, err)
	}
}

// What a value was read as is clipped as the value is, so a range of 1024
// bytes, whose intervals take about seven times its bytes, keeps the
// message short (security review, finding 3). The planted break: clip
// returning its argument whole.
func TestAgreeSemverClipsWhatAValueWasReadAs(t *testing.T) {
	var parts []string
	for i := 1; len(strings.Join(parts, "||")) < 1000; i++ {
		parts = append(parts, strconv.Itoa(i))
	}
	long := strings.Join(parts, "||")
	res := Run(context.Background(), "/repo", semverRule(
		named("a", fake{values: []check.Value{at("0.5.0", "a.txt", 1)}}),
		named("b", fake{values: []check.Value{at(long, "b.txt", 1)}})))
	if res.Verdict != check.VerdictFalse || len(res.Message) > 4*maxQuoted+400 || !strings.Contains(res.Message, " bytes)") {
		t.Errorf("verdict %s, a message of %d bytes:\n%s", res.Verdict, len(res.Message), res.Message)
	}
}

// A value semver cannot read is named with an error that quotes at most a
// little of it, so 1024 bytes that do not print stay short.
func TestAgreeSemverQuotesAnUnreadValueShort(t *testing.T) {
	bad := ">=" + strings.Repeat("\x01", 1000)
	res := Run(context.Background(), "/repo", semverRule(
		named("a", fake{values: []check.Value{at("1.0.0", "a.txt", 1)}}),
		named("b", fake{values: []check.Value{at(bad, "b.txt", 1)}})))
	if res.Verdict != check.VerdictUnknown || len(res.Message) > 6*maxQuoted+400 {
		t.Errorf("verdict %s, a message of %d bytes:\n%s", res.Verdict, len(res.Message), res.Message)
	}
}

// semver() and semver_satisfies() cost the bytes they read, so a loop over
// long ranges reaches the cost limit, where each call once cost 1. The
// planted break: the trackers left out of the program.
func TestSemverCountsTowardTheCostLimit(t *testing.T) {
	for _, expr := range []string{"v.all(r, semver_satisfies('1.0.0', r))", "v.all(r, semver(r) > semver('0'))"} {
		a, err := compile(expr, []string{"v"}, limits{cost: 2000, timeout: time.Second, grace: stopGrace})
		if err != nil {
			t.Fatal(err)
		}
		long := make([]string, 4)
		for i := range long {
			long[i] = "1.0.0" + strings.Repeat(" ", 600)
		}
		if _, err := a.Eval(context.Background(), map[string][]string{"v": long}, nil, nil); err == nil || err.Error() != "the evaluation exceeded the cost limit of 2000" {
			t.Errorf("%s over four values of 605 bytes under a limit of 2000: %v", expr, err)
		}
		if ok, err := a.Eval(context.Background(), map[string][]string{"v": {"1.0.0", "1.0.0"}}, nil, nil); err != nil || !ok {
			t.Errorf("%s over two short values: %v, %v", expr, ok, err)
		}
	}
}
