package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
)

func notLiteral(text, file string, line int) check.Value {
	v := val(text, file, line)
	v.NotLiteral = true
	return v
}

// leftOutSample holds a rule that holds and a rule that is false, each with a
// value skip_not_literal left out, and a rule that holds with none.
var leftOutSample = check.Report{Results: []check.Result{
	{
		Check:   check.Check{ID: "held", Scope: check.ScopeRepo, Reason: "r", Rule: &check.Rule{Agree: []string{"chart", "shell"}}},
		Verdict: check.VerdictTrue,
		Message: `skip_not_literal left out 1 not literal value of extractor "shell"`,
		Extracted: []check.Extracted{
			{Extractor: "chart", Values: []check.Value{val("1.4.2", "Chart.yaml", 3)}},
			{Extractor: "shell", Values: []check.Value{val("1.4.2", "build.sh", 2)}, LeftOut: []check.Value{notLiteral("$(git describe)", "build.sh", 7)}},
		},
	},
	{
		Check:   check.Check{ID: "differs", Scope: check.ScopeRepo, Reason: "r", Rule: &check.Rule{Agree: []string{"chart", "ci"}}},
		Verdict: check.VerdictFalse,
		Message: "agree: the values differ\n" + `skip_not_literal left out 2 not literal values of extractor "ci"`,
		Extracted: []check.Extracted{
			{Extractor: "chart", Values: []check.Value{val("1.5.0", "Chart.yaml", 3)}},
			{Extractor: "ci", Values: []check.Value{val("1.4.2", "ci.yml", 20)}, LeftOut: []check.Value{notLiteral("${{ env.TAG }}", "ci.yml", 12), notLiteral("${{ inputs.tag }}", "ci.yml", 31)}},
		},
	},
	{Check: check.Check{ID: "plain", Scope: check.ScopeRepo, Reason: "r"}, Verdict: check.VerdictTrue},
}}

// Text names each value left out under the coverage line, for a rule that
// holds as for one that does not. The planted break is printing only the
// failed checks' values, which hid the value the true rule left out.
func TestTextShowsLeftOutValues(t *testing.T) {
	var buf bytes.Buffer
	if err := (Text{}).Write(&buf, leftOutSample); err != nil {
		t.Fatal(err)
	}
	want := `FALSE differs (repo)
  why:  r
  said: agree: the values differ
        skip_not_literal left out 2 not literal values of extractor "ci"

1 erratum, 0 unknown. Coverage: ran 3, skipped 0.
  left out of held (repo), not literal: "$(git describe)" at build.sh:7 (shell)
  left out of differs (repo), not literal: "${{ env.TAG }}" at ci.yml:12 (ci)
  left out of differs (repo), not literal: "${{ inputs.tag }}" at ci.yml:31 (ci)
`
	if got := buf.String(); got != want {
		t.Fatalf("text output differs.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// A left out value is quoted and cut as a value in a message is, and its
// control characters reach the terminal escaped.
func TestTextLeftOutValueIsQuotedAndCut(t *testing.T) {
	long := strings.Repeat("v", valueLimit+50)
	rep := check.Report{Results: []check.Result{{
		Check:   check.Check{ID: "c", Scope: check.ScopeRepo},
		Verdict: check.VerdictTrue,
		Extracted: []check.Extracted{{Extractor: "e\x1b[2J", LeftOut: []check.Value{
			notLiteral(long, "a.sh", 1),
			notLiteral("$(echo \x1b]8;;x\x07)", "b\u202e.sh", 2),
		}}},
	}}}
	var buf bytes.Buffer
	if err := (Text{}).Write(&buf, rep); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"` + long[:valueLimit] + `" (cut: the value is 250 bytes) at a.sh:1 (e\x1b[2J)`,
		`"$(echo \x1b]8;;x\a)" at b\u202e.sh:2 (e\x1b[2J)`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output does not hold %s:\n%s", want, buf.String())
		}
	}
	if r := rawActedOn(buf.Bytes()); r >= 0 {
		t.Fatalf("output holds %U raw", r)
	}
}

// JUnit names each value left out in the case's system-out, whatever the
// verdict, and a case with none has no system-out.
func TestJUnitShowsLeftOutValues(t *testing.T) {
	var buf bytes.Buffer
	if err := (JUnit{}).Write(&buf, leftOutSample); err != nil {
		t.Fatal(err)
	}
	s := decode(t, buf.Bytes())
	want := []string{
		"skip_not_literal left out these values, which are not literal:\n" +
			`  "$(git describe)" at build.sh:7 (shell)`,
		"skip_not_literal left out these values, which are not literal:\n" +
			`  "${{ env.TAG }}" at ci.yml:12 (ci)` + "\n" +
			`  "${{ inputs.tag }}" at ci.yml:31 (ci)`,
		"",
	}
	for i, c := range s.Cases {
		if c.SystemOut != want[i] {
			t.Errorf("case %s system-out:\n%s\nwant:\n%s", c.Name, c.SystemOut, want[i])
		}
	}
	if s.Cases[1].Failure == nil || !strings.Contains(s.Cases[1].Failure.Message, `left out 2 not literal values of extractor "ci"`) {
		t.Errorf("the failure does not count the values left out: %+v", s.Cases[1].Failure)
	}
	if !strings.Contains(buf.String(), "</failure>\n    <system-out>") {
		t.Errorf("system-out does not follow the failure, as the JUnit schema orders them:\n%s", buf.String())
	}
}

// A false rule's result links each value left out as a related location
// whose message says it is not literal and was left out, after the values it
// compared. The planted break is linking the left out values as compared
// ones, which read as values that disagree.
func TestSARIFLinksLeftOutValues(t *testing.T) {
	b := writeSARIF(t, leftOutSample)
	if errs := loadSchema(t).validate(b); len(errs) > 0 {
		t.Fatalf("output breaks the schema:\n%s", strings.Join(errs, "\n"))
	}
	out := decodeSARIF(t, b)
	if problems := messageProblems(out); len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
	if len(out.Runs[0].Results) != 1 {
		t.Fatalf("results %+v, want the one erratum", out.Runs[0].Results)
	}
	res := out.Runs[0].Results[0]
	if got := res.Locations[0].where(); got != "ci.yml:20" {
		t.Fatalf("primary = %s, want ci.yml:20, the second value compared", got)
	}
	var got []string
	for _, r := range res.Related {
		got = append(got, r.where()+" "+r.Message.Text)
	}
	want := `Chart.yaml:3 "1.5.0" (chart)` + "\n" +
		`ci.yml:12 "${{{{ env.TAG }}}}" (ci), not literal, so skip_not_literal left it out of the comparison` + "\n" +
		`ci.yml:31 "${{{{ inputs.tag }}}}" (ci), not literal, so skip_not_literal left it out of the comparison`
	if strings.Join(got, "\n") != want {
		t.Fatalf("related:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
	wantMsg := "agree: the values differ\n" +
		`skip_not_literal left out 2 not literal values of extractor "ci"` + "\n" +
		"The other values, as [related locations]: [value 1](1).\n" +
		"The values left out as not literal, as [related locations]: [value 2](2), [value 3](3)."
	if res.Message.Text != wantMsg {
		t.Fatalf("message:\n%s\nwant:\n%s", res.Message.Text, wantMsg)
	}
}

// A rule that holds with a value left out is a note notification counting
// what was left out; one that holds with none is nothing.
func TestSARIFNotesARuleThatHeldWithValuesLeftOut(t *testing.T) {
	out := decodeSARIF(t, writeSARIF(t, leftOutSample))
	inv := out.Runs[0].Invocations[0]
	if !inv.ExecutionSuccessful {
		t.Fatal("executionSuccessful is false with no unknown")
	}
	if len(inv.Notifications) != 1 {
		t.Fatalf("notifications %+v, want one for the rule that held", inv.Notifications)
	}
	n := inv.Notifications[0]
	want := `check held holds on the values left: skip_not_literal left out 1 not literal value of extractor "shell"` +
		"\nThe values left out as not literal are the locations of this notification."
	if n.Level != "note" || n.Message.Text != want || n.Rule == nil || n.Rule.ID != "held" || n.Rule.Index != 0 {
		t.Fatalf("notification %+v, want a note on held: %s", n, want)
	}
	if !bytes.Contains(writeSARIF(t, leftOutSample), []byte(`"text": "\"$(git describe)\" (shell), not literal, so skip_not_literal left it out of the comparison"`)) {
		t.Fatal("the note does not name the value left out as its location")
	}
}

// With no value compared, as an assert over extractors whose every value was
// left out, the result points at the check and still links the left out
// values.
func TestSARIFLinksLeftOutValuesOfAResultAtTheCheck(t *testing.T) {
	rep := check.Report{Results: []check.Result{{
		Check:     check.Check{ID: "a", Scope: check.ScopeRepo, Reason: "r", Rule: &check.Rule{Assert: "size(shell) > 0"}, At: check.Location{File: "errata.yaml", Line: 4}},
		Verdict:   check.VerdictFalse,
		Message:   "assert: the expression is false",
		Extracted: []check.Extracted{{Extractor: "shell", LeftOut: []check.Value{notLiteral("$(date)", "../out.sh", 1)}}},
	}}}
	out := decodeSARIF(t, writeSARIF(t, rep))
	if problems := messageProblems(out); len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
	res := out.Runs[0].Results[0]
	if got := res.Locations[0].where(); got != "errata.yaml:4" {
		t.Fatalf("primary = %s, want the check at errata.yaml:4", got)
	}
	if len(res.Related) != 1 || res.Related[0].Physical != nil ||
		res.Related[0].Message.Text != `"$(date)" (shell) at ../out.sh, outside the repository, not literal, so skip_not_literal left it out of the comparison` {
		t.Fatalf("related %+v", res.Related)
	}
	if !strings.HasSuffix(res.Message.Text, "\nThe values left out as not literal, as [related locations]: [value 1](1).") {
		t.Fatalf("message %q", res.Message.Text)
	}
}

// Left out values survive the cap on related locations: the compared values
// are cut to make room, and the sentence that counts what is not linked does
// not say "left out", which means what skip_not_literal does. The planted
// break is the cap of c8f93cc, which dropped every left out value and called
// the dropped values left out.
func TestSARIFCapKeepsLeftOutValues(t *testing.T) {
	values := []check.Value{val("b", "f.yaml", 1)}
	for i := range maxRelated {
		values = append(values, val("a", "f.yaml", i+2))
	}
	left := []check.Value{notLiteral("$(x)", "f.sh", 1), notLiteral("$(y)", "f.sh", 2)}
	res := agreeRule("c", nil, check.Extracted{Extractor: "e", Values: values, LeftOut: left})
	out := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{res}}))
	if problems := messageProblems(out); len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
	r := out.Runs[0].Results[0]
	if len(r.Related) != maxRelated {
		t.Fatalf("%d related locations, want %d", len(r.Related), maxRelated)
	}
	var notLiteral []string
	for _, rel := range r.Related {
		if strings.HasSuffix(rel.Message.Text, notLiteralRemark) {
			notLiteral = append(notLiteral, rel.where())
		}
	}
	if strings.Join(notLiteral, " ") != "f.sh:1 f.sh:2" {
		t.Fatalf("left out values linked: %v, want both", notLiteral)
	}
	want := "\nThe values left out as not literal, as [related locations]: [value 998](998), [value 999](999)." +
		"\n2 more values are not linked: code scanning rejects a result with more than 1000 locations."
	if !strings.HasSuffix(r.Message.Text, want) {
		t.Fatalf("message ends %q, want %q", r.Message.Text[max(0, len(r.Message.Text)-250):], want)
	}
}

// With more left out values than the cap holds, the left out values take the
// slots the compared ones leave, and the sentence counts what each side
// dropped.
func TestSARIFCapCountsLeftOutValuesItCannotLink(t *testing.T) {
	var left []check.Value
	for i := range maxRelated + 3 {
		left = append(left, notLiteral("$(x)", "f.sh", i+1))
	}
	res := agreeRule("c", nil, check.Extracted{Extractor: "e", Values: []check.Value{val("a", "f.yaml", 1), val("b", "f.yaml", 2)}, LeftOut: left})
	r := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{res}})).Runs[0].Results[0]
	want := "\n4 more values are not linked, 0 compared and 4 left out as not literal: code scanning rejects a result with more than 1000 locations."
	if len(r.Related) != maxRelated || !strings.HasSuffix(r.Message.Text, want) {
		t.Fatalf("%d related; message ends %q, want %q", len(r.Related), r.Message.Text[max(0, len(r.Message.Text)-200):], want)
	}
}

// Neither side pushes the other out: with both over the cap, the compared
// values keep every slot but leftOutReserve, the left out values keep those,
// and the sentence counts what each side dropped. The planted break is the
// order of 6d8f1db, where 1200 left out values took all 999 slots and no
// compared value was linked.
func TestSARIFCapSharesTheSlots(t *testing.T) {
	values := []check.Value{val("b", "f.yaml", 1)}
	var left []check.Value
	for i := range 1200 {
		values = append(values, val("a", "f.yaml", i+2))
		left = append(left, notLiteral("$(x)", "f.sh", i+1))
	}
	res := agreeRule("c", nil, check.Extracted{Extractor: "e", Values: values, LeftOut: left})
	out := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{res}}))
	if problems := messageProblems(out); len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
	r := out.Runs[0].Results[0]
	leftLinked := 0
	for _, rel := range r.Related {
		if strings.HasSuffix(rel.Message.Text, notLiteralRemark) {
			leftLinked++
		}
	}
	if len(r.Related) != maxRelated || leftLinked != leftOutReserve {
		t.Fatalf("%d related, %d of them left out, want %d and %d", len(r.Related), leftLinked, maxRelated, leftOutReserve)
	}
	want := fmt.Sprintf("\n%d more values are not linked, %d compared and %d left out as not literal: code scanning rejects a result with more than 1000 locations.",
		2400-maxRelated, 1200-(maxRelated-leftOutReserve), 1200-leftOutReserve)
	if !strings.HasSuffix(r.Message.Text, want) {
		t.Fatalf("message ends %q, want %q", r.Message.Text[max(0, len(r.Message.Text)-250):], want)
	}
}

// The line that counts the values left out stays in a result's message when
// the message is cut at messageLimit characters. The planted break is cutting
// the whole message, which cut the count off the end of a long one.
func TestSARIFKeepsTheCountInACutMessage(t *testing.T) {
	counted := `skip_not_literal left out 1 not literal value of extractor "shell"`
	long := strings.Repeat("d", messageLimit+500)
	res := agreeRule("c", nil,
		check.Extracted{Extractor: "chart", Values: []check.Value{val("1.5.0", "Chart.yaml", 3)}},
		check.Extracted{Extractor: "shell", Values: []check.Value{val("1.4.2", "build.sh", 2)}, LeftOut: []check.Value{notLiteral("$(git describe)", "build.sh", 7)}},
	)
	res.Message = long + "\n" + counted
	unknown := res
	unknown.Check.ID, unknown.Verdict = "u", check.VerdictUnknown
	out := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{res, unknown}}))
	msg := out.Runs[0].Results[0].Message.Text
	if !strings.Contains(msg, "(cut: the message is ") || !strings.Contains(msg, "\n"+counted+"\n") {
		t.Fatalf("result message does not keep the count after the cut: %q", msg[max(0, len(msg)-400):])
	}
	if n := len([]rune(strings.Split(msg, "\nThe other values")[0])); n > messageLimit+60 {
		t.Fatalf("the message before the links is %d characters, over %d and its remark", n, messageLimit)
	}
	note := out.Runs[0].Invocations[0].Notifications[0].Message.Text
	if !strings.Contains(note, "(cut: the message is ") || !strings.Contains(note, "\n"+counted) {
		t.Fatalf("unknown notification does not keep the count after the cut: %q", note[max(0, len(note)-400):])
	}
}

// An unknown names the values it left out as an erratum does: each is a
// location of its notification, with the message that says it is not literal
// and was left out. The planted break is the notification of c8f93cc, which
// only counted them.
func TestSARIFUnknownNamesLeftOutValues(t *testing.T) {
	rep := check.Report{Results: []check.Result{{
		Check:   check.Check{ID: "u", Scope: check.ScopeRepo, Reason: "r", Rule: &check.Rule{Agree: []string{"chart", "shell"}}},
		Verdict: check.VerdictUnknown,
		Message: "extractor \"chart\": boom\n" + `skip_not_literal left out 1 not literal value of extractor "shell"`,
		Extracted: []check.Extracted{
			{Extractor: "shell", Values: []check.Value{val("1.4.2", "build.sh", 2)}, LeftOut: []check.Value{notLiteral("$(git describe)", "build.sh", 7), notLiteral("$(date)", "../x.sh", 1)}},
		},
	}}}
	b := writeSARIF(t, rep)
	if errs := loadSchema(t).validate(b); len(errs) > 0 {
		t.Fatalf("output breaks the schema:\n%s", strings.Join(errs, "\n"))
	}
	var out struct {
		Runs []struct {
			Invocations []struct {
				Notifications []struct {
					Message   struct{ Text string }
					Locations []outLocation `json:"locations"`
				} `json:"toolExecutionNotifications"`
			} `json:"invocations"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	n := out.Runs[0].Invocations[0].Notifications[0]
	var got []string
	for _, l := range n.Locations {
		got = append(got, l.where()+" "+l.Message.Text)
	}
	want := `build.sh:7 "$(git describe)" (shell), not literal, so skip_not_literal left it out of the comparison` + "\n" +
		`nowhere "$(date)" (shell) at ../x.sh, outside the repository, not literal, so skip_not_literal left it out of the comparison`
	if strings.Join(got, "\n") != want {
		t.Fatalf("locations:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
	if !strings.Contains(n.Message.Text, "\nThe values left out as not literal are the locations of this notification.") {
		t.Fatalf("message %q does not point at its locations", n.Message.Text)
	}
}

// quoteValue costs what its clip costs, not the whole value as runes. The
// planted break is []rune(text)[:valueLimit], which allocated 4 bytes for
// each rune of a 16 MB value.
func TestQuoteValueAllocatesItsClip(t *testing.T) {
	text := strings.Repeat("é", 8<<20) // 16 MB of two byte runes
	want := `"` + strings.Repeat("é", valueLimit) + `" (cut: the value is 16777216 bytes)`
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got := quoteValue(text)
	runtime.ReadMemStats(&after)
	if got != want {
		t.Fatalf("quoteValue = %.80q..., want %.80q...", got, want)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
		t.Fatalf("quoteValue allocated %d bytes for a clip of %d characters", n, valueLimit)
	}
	if got := quoteValue("ab"); got != `"ab"` {
		t.Fatalf("quoteValue(ab) = %s", got)
	}
	exact := strings.Repeat("é", valueLimit)
	if got := quoteValue(exact); got != strconv.Quote(exact) {
		t.Fatalf("a value of exactly %d characters was cut: %.60s", valueLimit, got)
	}
}

// cut costs what its clip costs too: a rule's message can be tens of
// megabytes, and SARIF keeps messageLimit characters of it. The planted break
// is []rune(s)[:limit], which allocated 4 bytes for each rune of the message.
func TestCutAllocatesItsClip(t *testing.T) {
	s := strings.Repeat("é", 8<<20)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got := cut(s, "message", messageLimit)
	runtime.ReadMemStats(&after)
	if want := strings.Repeat("é", messageLimit) + " (cut: the message is 16777216 bytes)"; got != want {
		t.Fatalf("cut = %.60q..., want %.60q...", got, want)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
		t.Fatalf("cut allocated %d bytes for a clip of %d characters", n, messageLimit)
	}
}

// A rule that held with a note, such as an agree rule that compared ranges
// alone, has it said under the coverage line. A program check that passed
// prints nothing there, whatever its output, and a check that did not hold
// is noted nowhere but in its own entry. The planted breaks: printing the
// note of a check that failed, and printing a passing check's Message (a
// program check's output), as the first version of the line did.
func TestTextNotesWhatAHeldRuleSays(t *testing.T) {
	ranges := check.Result{
		Check:     check.Check{ID: "engines", Scope: check.ScopeRepo, Reason: "r"},
		Verdict:   check.VerdictTrue,
		Note:      "agree: only ranges were compared, since no value is a version, and they overlap",
		Message:   `skip_not_literal left out 1 not literal value of extractor "ci"`,
		Extracted: []check.Extracted{{Extractor: "ci", Values: []check.Value{val(">=20", "ci.yml", 3)}, LeftOut: []check.Value{notLiteral("${{ env.NODE }}", "ci.yml", 4)}}},
	}
	program := check.Result{Check: check.Check{ID: "lint", Scope: check.ScopeRepo, Reason: "r", Run: []string{"make", "lint"}}, Verdict: check.VerdictTrue, Message: "all 412 files checked"}
	unknown := check.Result{Check: check.Check{ID: "u", Scope: check.ScopeRepo, Reason: "r"}, Verdict: check.VerdictUnknown, Message: "could not read", Note: "a note"}
	var buf bytes.Buffer
	if err := (Text{}).Write(&buf, check.Report{Results: []check.Result{ranges, program, unknown, leftOutSample.Results[0]}}); err != nil {
		t.Fatal(err)
	}
	want := `UNKNOWN u (repo)
  why:  r
  said: could not read

0 errata, 1 unknown. Coverage: ran 4, skipped 0.
  left out of engines (repo), not literal: "${{ env.NODE }}" at ci.yml:4 (ci)
  left out of held (repo), not literal: "$(git describe)" at build.sh:7 (shell)
  note on engines (repo): agree: only ranges were compared, since no value is a version, and they overlap
`
	if got := buf.String(); got != want {
		t.Fatalf("text output differs.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// A left out value names its condition after its place, in the text and
// JUnit lines (where) and in SARIF's messages (label). Planted break: label
// without the condition, which named the value alone.
func TestLeftOutNamesItsCondition(t *testing.T) {
	v := notLiteral("$TAG", ".gitlab-ci.yml", 9)
	v.Condition = "build: [a]"
	f := named{value: v, extractor: "ci"}
	if got, want := where(f), `"$TAG" at .gitlab-ci.yml:9 when "build: [a]" (ci)`; got != want {
		t.Errorf("where: %q, want %q", got, want)
	}
	if got := label(f); !strings.Contains(got, `when \"build: [a]\" (ci)`) && !strings.Contains(got, `when "build: [a]" (ci)`) {
		t.Errorf("label: %q names no condition", got)
	}
}
