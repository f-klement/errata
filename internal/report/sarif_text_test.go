package report

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
)

// loadableID draws ids that check.Check.Validate accepts, the ids the loader
// can load. It may draw one id twice; the writer then keeps one rule.
var loadableID = rapid.StringMatching(`[a-z0-9]{1,6}(-[a-z0-9]{1,6}){0,2}`).Filter(func(id string) bool {
	return check.Check{ID: id, Reason: "r", Scope: check.ScopeRepo, Run: []string{"x"}}.Validate() == nil
})

// anyPath draws arbitrary bytes, often behind a prefix that leaves the
// repository or holds a backslash.
var anyPath = rapid.Custom(func(t *rapid.T) string {
	prefix := rapid.SampledFrom([]string{"", "", "a/", "../", "a/../../", "/", "//evil.example/", `..\`, `a\..\..\`, "a/../"}).Draw(t, "prefix")
	return prefix + string(rapid.SliceOf(rapid.Byte()).Draw(t, "rest"))
})

// anyText draws arbitrary bytes mixed with the pieces of SARIF message
// syntax: brackets, braces, backslashes and whole links.
var anyText = rapid.Custom(func(t *rapid.T) string {
	var b strings.Builder
	for range rapid.IntRange(0, 6).Draw(t, "pieces") {
		b.WriteString(rapid.OneOf(
			rapid.SampledFrom([]string{"](", "[", "]", "(", ")", "{", "}", `\`, "[x](1)", "[t](https://evil.example/)", "{0}"}),
			rapid.Map(rapid.SliceOf(rapid.Byte()), func(b []byte) string { return string(b) }),
		).Draw(t, "piece"))
	}
	return b.String()
})

// embedded is one embedded link: the text a viewer shows, and its
// destination.
type embedded struct{ text, dest string }

// The two readers below are the test oracle. Neither knows how the writer
// escapes; each reads a plain text message the way a SARIF consumer does.

// strictLinks reads the embedded links of a plain text message by the
// grammar of SARIF 2.1.0 section 3.11.6:
//
//	escaped link character = "\" | "[" | "]";
//	normal link character = ? JSON string character ? − escaped link character;
//	link character = normal link character | ("\", escaped link character);
//	link text = { link character };
//	link destination = ? Any valid URI ?;
//	embedded link = "[", link text, "](", link destination, ")";
//
// (The − is the grammar's set difference.) A "[" that does not start an
// embedded link is a literal character, and the reader goes on after it. The
// destination runs to the first ")".
func strictLinks(s string) []embedded {
	rs := []rune(s)
	var links []embedded
	for i := 0; i < len(rs); i++ {
		if rs[i] != '[' {
			continue
		}
		if l, end, ok := strictLink(rs, i); ok {
			links = append(links, l)
			i = end
		}
	}
	return links
}

// strictLink reads the embedded link that starts at rs[i], if one does, and
// returns it with the index of its closing ")".
func strictLink(rs []rune, i int) (embedded, int, bool) {
	var text []rune
	for j := i + 1; j < len(rs); j++ {
		switch rs[j] {
		case '\\':
			if j+1 == len(rs) || !strings.ContainsRune(`\[]`, rs[j+1]) {
				return embedded{}, 0, false
			}
			j++
			text = append(text, rs[j])
		case '[':
			return embedded{}, 0, false
		case ']':
			if j+1 == len(rs) || rs[j+1] != '(' {
				return embedded{}, 0, false
			}
			k := slices.Index(rs[j+2:], ')')
			if k <= 0 {
				return embedded{}, 0, false
			}
			return embedded{text: string(text), dest: string(rs[j+2 : j+2+k])}, j + 2 + k, true
		default:
			text = append(text, rs[j])
		}
	}
	return embedded{}, 0, false
}

// viewerLink is the regular expression with which Microsoft's SARIF viewer
// finds embedded links (microsoft/sarif-vscode-extension at 9237cd7,
// src/panel/widgets.tsx, renderMessageTextWithEmbeddedLinks). It knows no
// escape, and it opens a destination that is not a number as a URL.
var viewerLink = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)

// viewerLinks reads the links as that viewer does: the leftmost matches,
// none overlapping, which is what its split with the g flag returns.
func viewerLinks(s string) []embedded {
	var links []embedded
	for _, m := range viewerLink.FindAllStringSubmatch(s, -1) {
		links = append(links, embedded{text: m[1], dest: m[2]})
	}
	return links
}

// braceProblem returns the first single brace in s. Section 3.11.5 writes a
// literal brace as two, so a single one is a placeholder or a malformed one.
func braceProblem(s string) error {
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] != '{' && rs[i] != '}' {
			continue
		}
		if i+1 == len(rs) || rs[i+1] != rs[i] {
			return fmt.Errorf("single %q at %d", rs[i], i)
		}
		i++
	}
	return nil
}

func TestReadersOnKnownInputs(t *testing.T) {
	cases := []struct {
		in             string
		strict, viewer []embedded
	}{
		{"see [the fix](https://evil.example/login)", []embedded{{"the fix", "https://evil.example/login"}}, []embedded{{"the fix", "https://evil.example/login"}}},
		{"see [the fix]\u2060(https://evil.example/login)", nil, nil},
		// The backslash form 8073037 wrote: no link by the grammar, a link
		// in the viewer.
		{`see \[the fix\](https://evil.example/login)`, nil, []embedded{{`the fix\`, "https://evil.example/login"}}},
		{`[a\]b](1)`, []embedded{{"a]b", "1"}}, nil},
		// The specification's own example has no "](" and is no link.
		{`[para\[0\]\\spans\[2\](1)`, nil, []embedded{{`2\`, "1"}}},
		{"[x [value 1](1)", []embedded{{"value 1", "1"}}, []embedded{{"x [value 1", "1"}}},
		{"[x\nas [related locations]: [value 1](1)", []embedded{{"value 1", "1"}}, []embedded{{"value 1", "1"}}},
		{"[x]", nil, nil},
		{"[x]()", nil, nil},
	}
	for _, c := range cases {
		if got := strictLinks(c.in); !slices.Equal(got, c.strict) {
			t.Errorf("strictLinks(%q) = %q, want %q", c.in, got, c.strict)
		}
		if got := viewerLinks(c.in); !slices.Equal(got, c.viewer) {
			t.Errorf("viewerLinks(%q) = %q, want %q", c.in, got, c.viewer)
		}
	}
	for in, want := range map[string]string{"a {0}": `single '{' at 2`, "a }": `single '}' at 2`, "{{0}} {{{{x}}}}": "<nil>", "{{{": `single '{' at 2`} {
		if got := fmt.Sprint(braceProblem(in)); got != want {
			t.Errorf("braceProblem(%q) = %s, want %s", in, got, want)
		}
	}
}

// ownLinks are the links errata writes into a result message with n related
// locations.
func ownLinks(n int) []embedded {
	var out []embedded
	for id := 1; id <= n; id++ {
		out = append(out, embedded{text: "value " + strconv.Itoa(id), dest: strconv.Itoa(id)})
	}
	return out
}

// messageProblems lists every message in out that a SARIF consumer would
// read as more than its text: a placeholder, or a link errata did not
// write. Both readers must find in a result message exactly errata's links
// to its related locations, in order, and nothing anywhere else. It also
// lists every URI that resolves outside the checkout.
func messageProblems(out sarifOut) []string {
	var problems []string
	read := func(where, text string, want []embedded) {
		if err := braceProblem(text); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v in %q", where, err, text))
		}
		if got := strictLinks(text); !slices.Equal(got, want) {
			problems = append(problems, fmt.Sprintf("%s: the grammar reads links %q, want %q, in %q", where, got, want, text))
		}
		if got := viewerLinks(text); !slices.Equal(got, want) {
			problems = append(problems, fmt.Sprintf("%s: the viewer reads links %q, want %q, in %q", where, got, want, text))
		}
	}
	root, _ := url.Parse("file:///r/")
	place := func(where string, l outLocation) {
		read(where+" location message", l.Message.Text, nil)
		if l.Physical == nil {
			return
		}
		u, err := url.Parse(l.Physical.Artifact.URI)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: uri %q: %v", where, l.Physical.Artifact.URI, err))
			return
		}
		if r := root.ResolveReference(u); r.Host != "" || !strings.HasPrefix(r.Path, "/r/") {
			problems = append(problems, fmt.Sprintf("%s: uri %q resolves to %s, outside the checkout", where, l.Physical.Artifact.URI, r))
		}
		// The Windows reading: decode once, as upload-sarif does, and take
		// a backslash as a separator.
		decoded, err := url.PathUnescape(l.Physical.Artifact.URI)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: uri %q does not decode: %v", where, l.Physical.Artifact.URI, err))
			return
		}
		win := path.Clean(strings.ReplaceAll(decoded, `\`, "/"))
		if strings.HasPrefix(win, "/") || win == ".." || strings.HasPrefix(win, "../") || regexp.MustCompile(`^[A-Za-z]:`).MatchString(win) {
			problems = append(problems, fmt.Sprintf("%s: uri %q reads as %s on Windows, outside the checkout", where, l.Physical.Artifact.URI, win))
		}
	}
	for _, r := range out.Runs[0].Tool.Driver.Rules {
		read("rule "+r.ID, r.ShortDescription.Text, nil)
		read("rule "+r.ID, r.FullDescription.Text, nil)
		read("rule "+r.ID, r.Help.Text, nil)
	}
	for i, n := range out.Runs[0].Invocations[0].Notifications {
		read("notification "+strconv.Itoa(i), n.Message.Text, nil)
	}
	for i, res := range out.Runs[0].Results {
		where := "result " + strconv.Itoa(i)
		read(where, res.Message.Text, ownLinks(len(res.Related)))
		for k, rel := range res.Related {
			if rel.ID != k+1 {
				problems = append(problems, fmt.Sprintf("%s: related location %d has id %d", where, k, rel.ID))
			}
			place(where+" related", rel)
		}
		for _, l := range res.Locations {
			place(where, l)
		}
	}
	return problems
}

func TestSARIFKeepsDataAsText(t *testing.T) {
	planted := "see [the fix](https://evil.example/login) and {0} {{x}} in C:\\dir"
	written := "see [the fix]\u2060(https://evil.example/login) and {{0}} {{{{x}}}} in C:\\dir"
	rep := check.Report{Results: []check.Result{
		{Check: check.Check{ID: "p", Reason: "[r](1) {y}", Run: []string{"x"}}, Verdict: check.VerdictFalse, Message: planted},
		{Check: check.Check{ID: "u", Reason: "[r](1) {y}"}, Verdict: check.VerdictUnknown, Message: planted},
		{Check: check.Check{ID: "s", Reason: "r", Scope: check.ScopeLive}, Gap: "no {0} [x](1)"},
	}}
	out := decodeSARIF(t, writeSARIF(t, rep))
	if got := out.Runs[0].Results[0].Message.Text; got != written {
		t.Errorf("result message = %q, want %q", got, written)
	}
	notes := out.Runs[0].Invocations[0].Notifications
	if want := "check u is unknown: " + written + "\nwhy: [r]\u2060(1) {{y}}"; notes[0].Message.Text != want {
		t.Errorf("unknown = %q, want %q", notes[0].Message.Text, want)
	}
	if want := "check s skipped (live): no {{0}} [x]\u2060(1)"; notes[1].Message.Text != want {
		t.Errorf("skip = %q, want %q", notes[1].Message.Text, want)
	}
	r := out.Runs[0].Tool.Driver.Rules[0]
	if want := "[r]\u2060(1) {{y}}"; r.ShortDescription.Text != want || r.FullDescription.Text != want || r.Help.Text != want {
		t.Errorf("rule texts = %q %q %q, want %q", r.ShortDescription.Text, r.FullDescription.Text, r.Help.Text, want)
	}
	if problems := messageProblems(out); len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
}

// Values that hold link syntax stay text, in the related locations' own
// messages, and both readers find errata's links and no other.
func TestSARIFDataLinkStaysText(t *testing.T) {
	values := texts("f.yaml", "a", "b", "a", "[x](1) {0}", "y](z)", "[open")
	rep := check.Report{Results: []check.Result{agreeRule("c", nil, values...)}}
	rep.Results[0].Message = "agree: the values differ [unclosed"
	out := decodeSARIF(t, writeSARIF(t, rep))
	res := out.Runs[0].Results[0]
	want := "agree: the values differ [unclosed\nThe other values, as [related locations]: [value 1](1), [value 2](2), [value 3](3), [value 4](4), [value 5](5)."
	if res.Message.Text != want {
		t.Fatalf("message = %q, want %q", res.Message.Text, want)
	}
	var got []string
	for _, r := range res.Related {
		got = append(got, r.Message.Text)
	}
	wantRelated := []string{`"a" (e0)`, `"a" (e2)`, "\"[x]\u2060(1) {{0}}\" (e3)", "\"y]\u2060(z)\" (e4)", `"[open" (e5)`}
	if !slices.Equal(got, wantRelated) {
		t.Fatalf("related messages = %q, want %q", got, wantRelated)
	}
	if problems := messageProblems(out); len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
}

func TestSARIFPathsOutsideTheRepository(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":  "../../etc/passwd",
		"/etc/passwd":       "/etc/passwd",
		"//evil.example/x":  "//evil.example/x",
		"a/../../b":         "a/../../b",
		"..":                "..",
		"/":                 "/",
		"../[x](1)/{0}.txt": "../[x]\u2060(1)/{{0}}.txt",
		// Each of these climbs out, names a drive or a host once a
		// backslash separates, as on Windows.
		`..\..\etc\passwd`: `..\..\etc\passwd`,
		`..\x`:             `..\x`,
		`\x`:               `\x`,
		`a\..\..\b`:        `a\..\..\b`,
		`\\host\share\x`:   `\\host\share\x`,
		`C:\x`:             `C:\x`,
		"C:/x":             "C:/x",
		"c:x":              "c:x",
		// A drive once .. is resolved.
		"a/../d:":  "a/../d:",
		`a\..\c:x`: `a\..\c:x`,
	}
	for file, shown := range cases {
		r := check.Result{Check: check.Check{ID: "c", Reason: "r"}, Verdict: check.VerdictFalse, Message: "m",
			Extracted: []check.Extracted{{Extractor: "e", Values: []check.Value{val("v", file, 3)}}}}
		out := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{r}}))
		res := out.Runs[0].Results[0]
		if res.Locations[0].Physical != nil {
			t.Errorf("%q: physical location %s, want none", file, res.Locations[0].where())
			continue
		}
		if want := `"v" (e) at ` + shown + ", outside the repository"; res.Locations[0].Message.Text != want {
			t.Errorf("%q: location message %q, want %q", file, res.Locations[0].Message.Text, want)
		}
		if want := "m\nThe value is at " + shown + ", outside the repository, so this result has no place in a file."; res.Message.Text != want {
			t.Errorf("%q: message %q, want %q", file, res.Message.Text, want)
		}
		if problems := messageProblems(out); len(problems) > 0 {
			t.Errorf("%q: %s", file, strings.Join(problems, "\n"))
		}
	}

	// A backslash is part of a Linux file name: the URI encodes it.
	kept := map[string]string{
		"a/../b":      "a/../b:3",
		"./x":         "./x:3",
		"..a/b":       "..a/b:3",
		"ab:c.yaml":   "./ab:c.yaml:3",
		`a\b`:         "a%5Cb:3",
		`a\..\b`:      "a%5C..%5Cb:3",
		`dir\file.md`: "dir%5Cfile.md:3",
		"%5C..%5Cx":   "%255C..%255Cx:3",
	}
	for file, want := range kept {
		r := check.Result{Check: check.Check{ID: "c", Reason: "r"}, Verdict: check.VerdictFalse,
			Extracted: []check.Extracted{{Extractor: "e", Values: []check.Value{val("v", file, 3)}}}}
		out := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{r}}))
		if got := out.Runs[0].Results[0].Locations[0].where(); got != want {
			t.Errorf("%q: location %s, want %s", file, got, want)
		}
		if problems := messageProblems(out); len(problems) > 0 {
			t.Errorf("%q: %s", file, strings.Join(problems, "\n"))
		}
	}

	// A related location outside keeps its id, link and message.
	rep := check.Report{Results: []check.Result{agreeRule("c", nil,
		check.Extracted{Extractor: "in", Values: []check.Value{val("b", "ok.yaml", 1)}},
		check.Extracted{Extractor: "out", Values: []check.Value{val("a", "../x", 2), val("a", "/y", 3)}},
	)}}
	out := decodeSARIF(t, writeSARIF(t, rep))
	res := out.Runs[0].Results[0]
	if res.Locations[0].where() != "ok.yaml:1" || len(res.Related) != 2 || res.Related[0].Physical != nil || res.Related[1].Physical != nil {
		t.Fatalf("locations %+v related %+v", res.Locations, res.Related)
	}
	if res.Related[1].ID != 2 || res.Related[1].Message.Text != `"a" (out) at /y, outside the repository` {
		t.Fatalf("related = %+v", res.Related[1])
	}
	if problems := messageProblems(out); len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
}

// simulateUpload is what upload-sarif sends for b, measured: it adds a
// fingerprint to each result with a file (random, as the hashes are), and
// run.automationDetails, then gzips the log as compact JSON.
func simulateUpload(t *testing.T, b []byte, rng *rand.Rand) int {
	t.Helper()
	var log map[string]any
	if err := json.Unmarshal(b, &log); err != nil {
		t.Fatal(err)
	}
	run := log["runs"].([]any)[0].(map[string]any)
	for _, r := range run["results"].([]any) {
		res := r.(map[string]any)
		if _, ok := res["locations"].([]any)[0].(map[string]any)["physicalLocation"]; ok {
			res["partialFingerprints"] = map[string]any{"primaryLocationLineHash": fmt.Sprintf("%016x:%d", rng.Uint64(), 1+rng.IntN(20))}
		}
	}
	run["automationDetails"] = map[string]any{"id": "errata/"}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(log); err != nil {
		t.Fatal(err)
	}
	return gzipped(buf.Bytes())
}

// cjk is n random CJK characters, which gzip barely compresses.
func cjk(rng *rand.Rand, n int) string {
	r := make([]rune, n)
	for k := range r {
		r[k] = rune(0x4e00 + rng.IntN(20000))
	}
	return string(r)
}

// manyErrata is n agree errata shaped as the review's size probe: two
// values in two files each, and a message of msg random CJK characters.
func manyErrata(rng *rand.Rand, n, msg int) check.Report {
	var rep check.Report
	for i := range n {
		rep.Results = append(rep.Results, check.Result{
			Check:   check.Check{ID: fmt.Sprintf("c%d", i%50), Reason: "r", Rule: &check.Rule{Agree: []string{"a", "b"}}},
			Verdict: check.VerdictFalse,
			Message: cjk(rng, msg),
			Extracted: []check.Extracted{
				{Extractor: "a", Values: []check.Value{val("x", fmt.Sprintf("d/f%d.yaml", i%300), i%500+1)}},
				{Extractor: "b", Values: []check.Value{val("y", fmt.Sprintf("d/g%d.yaml", i%300), i%500+1)}},
			},
		})
	}
	return rep
}

func writeWith(t *testing.T, rep check.Report, bu budget) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := (SARIF{}).writeBounded(&buf, rep, bu); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, and make
// slow runs it once, plain, from the Makefile's SLOW_TESTS, which the extract
// package's TestEverySlowTestIsInMakeSlow holds to the tests that call it.
func slowOnly(t *testing.T) {
	t.Helper()
	if testing.Short() || raceEnabled || mutationRun {
		t.Skip("builds a large input; make slow runs it")
	}
}

// The review's case: 25,000 errata that errata's file held as 24,794
// results at 9,499,809 bytes gzipped, which upload-sarif sent as 10,060,066.
// At the real budget the simulated upload is under GitHub's limit. It skips
// under -race, -short and Gremlins' mutation tag, and make slow runs it once,
// plain; the cuts it reaches are the ones TestSARIFBoundsTheUpload runs at a
// small budget. Run it with
//
//	go test -run TestSARIFBoundsTheReviewersUpload ./internal/report/
func TestSARIFBoundsTheReviewersUpload(t *testing.T) {
	// It builds a 35 MB file several times: 6 seconds plain, 90 under
	// -race, which make test's two minute timeout cannot hold, and 6 more
	// for every mutant of the report package under Gremlins.
	slowOnly(t)
	rng := rand.New(rand.NewPCG(1, 2))
	b := writeSARIF(t, manyErrata(rng, maxResults, 163))
	out := decodeSARIF(t, b)
	kept := len(out.Runs[0].Results)
	up := simulateUpload(t, b, rng)
	t.Logf("kept %d of %d results; the file is %d bytes, %d gzipped; the simulated upload is %d gzipped", kept, maxResults, len(b), gzipped(b), up)
	if up > uploadLimit {
		t.Fatalf("simulated upload is %d bytes gzipped, over %d", up, uploadLimit)
	}
	if kept == 0 || kept >= 24794 {
		t.Fatalf("kept %d results, want fewer than the 24794 that did not fit", kept)
	}
	notes := out.Runs[0].Invocations[0].Notifications
	want := fmt.Sprintf("%d errata left out of this file: the upload would pass 9800000 bytes gzipped, errata's bound under the 10 MB that code scanning accepts", maxResults-kept)
	if len(notes) != 1 || notes[0].Message.Text != want {
		t.Fatalf("notifications = %+v, want %q", notes, want)
	}
}

// At a small budget, so the file is quick to build: the writer keeps the
// most results that fit and counts the rest in a warning, and the simulated
// upload stays under the limit the budget stands for.
func TestSARIFBoundsTheUpload(t *testing.T) {
	const n = 200
	bu := budget{gzipped: 30_000, plain: 29_000}
	rng := rand.New(rand.NewPCG(3, 4))
	rep := manyErrata(rng, n, 163)
	b := writeWith(t, rep, bu)
	if up := simulateUpload(t, b, rng); up > bu.gzipped {
		t.Fatalf("simulated upload is %d bytes gzipped, over the budget of %d", up, bu.gzipped)
	}
	out := decodeSARIF(t, b)
	kept := len(out.Runs[0].Results)
	if kept == 0 || kept == n {
		t.Fatalf("kept %d of %d results", kept, n)
	}
	// One more result, with the warning for one fewer left out, does not fit.
	p := build(rep)
	p.budget = bu
	more, err := p.marshal(cuts{results: kept + 1, notes: len(p.notes)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := bu.fits(more, kept+1); err != nil || ok {
		t.Fatalf("kept %d results, but %d fit (%v)", kept, kept+1, err)
	}
	notes := out.Runs[0].Invocations[0].Notifications
	want := fmt.Sprintf("%d errata left out of this file: the upload would pass 30000 bytes gzipped, errata's bound under the 10 MB that code scanning accepts", n-kept)
	if len(notes) != 1 || notes[0].Level != "warning" || notes[0].Message.Text != want {
		t.Fatalf("notifications = %+v, want one warning %q", notes, want)
	}
	if errs := loadSchema(t).validate(b); len(errs) > 0 {
		t.Fatalf("breaks the schema: %v", errs[:min(3, len(errs))])
	}
}

// fits counts the compact form and uploadPerResult for each result, and
// gzips only past the plain budget.
func TestSARIFFitsMeasuresTheUpload(t *testing.T) {
	bu := budget{gzipped: 10_000, plain: 5_000}
	indented := []byte("{\n  \"a\": [\n    1\n  ]\n}\n")
	cases := []struct {
		name    string
		b       []byte
		results int
		want    bool
	}{
		{"compact under the plain budget", indented, 0, true},
		{"indentation is not counted", append(append([]byte("{\"a\":\""), bytes.Repeat([]byte("x"), bu.plain-8)...), []byte("\"}            ")...), 0, true},
		{"fingerprints count", indented, bu.gzipped / uploadPerResult, false},
		{"fingerprints that fit", indented, (bu.gzipped - 40) / uploadPerResult, true},
		{"zeros past the plain budget gzip small", []byte("\"" + strings.Repeat("0", 3*bu.plain) + "\""), 0, true},
		{"noise past the gzipped budget", []byte(strconv.Quote(cjk(rand.New(rand.NewPCG(5, 6)), bu.gzipped))), 0, false},
	}
	for _, c := range cases {
		if got, err := bu.fits(c.b, c.results); err != nil || got != c.want {
			t.Errorf("%s: fits = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	if _, err := bu.fits([]byte("{"), 0); err == nil {
		t.Error("fits accepted a file that is not JSON")
	}
}

// When leaving out every result is not enough, the rules' help and the
// notifications are cut short, then notifications are left out, and a
// warning always says so.
func TestSARIFBoundsTheWholeFile(t *testing.T) {
	bu := budget{gzipped: 20_000, plain: 19_000}
	rng := rand.New(rand.NewPCG(7, 8))
	// One check id for every unknown, so they share a rule and the rules
	// alone fit. The rule takes the first reason, long enough that its help
	// is cut; each unknown has its own reason, so gzip cannot fold them.
	unknowns := func(n int) check.Report {
		var rep check.Report
		for i := range n {
			reason := cjk(rng, 1000)
			if i == 0 {
				reason = cjk(rng, 3000)
			}
			rep.Results = append(rep.Results, check.Result{
				Check:   check.Check{ID: "u", Reason: reason},
				Verdict: check.VerdictUnknown, Message: cjk(rng, 300),
			})
		}
		rep.Results = append(rep.Results, check.Result{Check: check.Check{ID: "e", Reason: "r", Run: []string{"x"}}, Verdict: check.VerdictFalse})
		return rep
	}
	cases := []struct {
		name    string
		rep     check.Report
		notes   int    // notifications kept, the warning not counted
		results int    // results kept
		want    string // the warning starts with this
	}{
		{"short texts fit, and then the erratum", unknowns(10), 10, 1, "rule help and notifications cut short: "},
		{"notifications left out", unknowns(40), -1, 0, "1 erratum left out of this file, "},
	}
	for _, c := range cases {
		b := writeWith(t, c.rep, bu)
		if ok, err := bu.fits(b, 0); err != nil || !ok {
			t.Errorf("%s: the file does not fit (%v)", c.name, err)
		}
		if up := simulateUpload(t, b, rng); up > bu.gzipped {
			t.Errorf("%s: simulated upload is %d bytes gzipped, over the budget of %d", c.name, up, bu.gzipped)
		}
		out := decodeSARIF(t, b)
		notes := out.Runs[0].Invocations[0].Notifications
		last := notes[len(notes)-1]
		if got := len(out.Runs[0].Results); got != c.results {
			t.Errorf("%s: %d results kept, want %d", c.name, got, c.results)
		}
		if last.Level != "warning" || !strings.HasPrefix(last.Message.Text, c.want) || !strings.HasSuffix(last.Message.Text, "the upload would pass 20000 bytes gzipped, errata's bound under the 10 MB that code scanning accepts") {
			t.Errorf("%s: warning = %+v, want it to start with %q", c.name, last, c.want)
		}
		if c.notes >= 0 && len(notes)-1 != c.notes {
			t.Errorf("%s: %d notifications kept, want %d", c.name, len(notes)-1, c.notes)
		}
		if kept := len(notes) - 1; c.notes < 0 && (kept == 0 || kept == len(c.rep.Results)-1) {
			t.Errorf("%s: %d notifications kept, want some and not all", c.name, kept)
		}
		if !strings.Contains(notes[0].Message.Text, "(cut: the message is ") || !strings.Contains(notes[0].Message.Text, "(cut: the reason is ") {
			t.Errorf("%s: first notification is not cut short: %q", c.name, notes[0].Message.Text[:80])
		}
		if r := out.Runs[0].Tool.Driver.Rules[0]; !strings.HasSuffix(r.Help.Text, "(cut: the reason is 9000 bytes)") {
			t.Errorf("%s: help ends %q", c.name, r.Help.Text[len(r.Help.Text)-40:])
		}
		if problems := messageProblems(out); len(problems) > 0 {
			t.Errorf("%s: %s", c.name, strings.Join(problems, "\n"))
		}
	}

	// Rules alone past the budget: every cut is made, and the warning
	// still says what was left out.
	var rules check.Report
	for i := range 10 {
		rules.Results = append(rules.Results, check.Result{Check: check.Check{ID: "t" + strconv.Itoa(i), Reason: cjk(rng, 2000)}, Verdict: check.VerdictTrue})
	}
	rules.Results = append(rules.Results, check.Result{Check: check.Check{ID: "u", Reason: "r"}, Verdict: check.VerdictUnknown, Message: "m"})
	notes := decodeSARIF(t, writeWith(t, rules, bu)).Runs[0].Invocations[0].Notifications
	if want := "1 notification left out, rule help and notifications cut short: "; len(notes) != 1 || !strings.HasPrefix(notes[0].Message.Text, want) {
		t.Fatalf("notifications = %+v, want only a warning starting %q", notes, want)
	}
}

// Write holds a file to 9,800,000 bytes gzipped, 2% under GitHub's 10 MB.
func TestSARIFDefaultBudget(t *testing.T) {
	if want := (budget{gzipped: 9_800_000, plain: 9_700_000}); defaultBudget != want {
		t.Fatalf("defaultBudget = %+v, want %+v", defaultBudget, want)
	}
}

// Write uses the figures from GitHub's page, cited beside the constants.
func TestSARIFLimitsAreGitHubs(t *testing.T) {
	got := []int{descriptionLimit, maxResults, maxRelated + 1, uploadLimit, uploadPerResult, gzipBudget, plainBudget}
	want := []int{1024, 25000, 1000, 10_000_000, 40, 9_800_000, 9_700_000}
	if !slices.Equal(got, want) {
		t.Fatalf("limits = %v, want %v", got, want)
	}
}

func gzipped(b []byte) int {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return gz.Len()
}

func TestSARIFCutsLongData(t *testing.T) {
	at, over := strings.Repeat("é", valueLimit), strings.Repeat("é", valueLimit+1)
	labels := func(long string) []string {
		rep := check.Report{Results: []check.Result{agreeRule("c", nil, texts("f.yaml", "x", long, long)...)}}
		var out []string
		for _, r := range decodeSARIF(t, writeSARIF(t, rep)).Runs[0].Results[0].Related {
			out = append(out, r.Message.Text)
		}
		return out
	}
	if got := labels(at); got[0] != `"`+at+`" (e1)` {
		t.Errorf("a value at the limit = %q, want it whole", got[0])
	}
	if got, want := labels(over), `"`+at+`" (cut: the value is 402 bytes) (e1)`; got[0] != want {
		t.Errorf("a value over the limit = %q, want %q", got[0], want)
	}

	message := func(m string) string {
		rep := check.Report{Results: []check.Result{{Check: check.Check{ID: "p", Run: []string{"x"}}, Verdict: check.VerdictFalse, Message: m}}}
		return decodeSARIF(t, writeSARIF(t, rep)).Runs[0].Results[0].Message.Text
	}
	if m := strings.Repeat("m", messageLimit); message(m) != m {
		t.Errorf("a message at the limit was cut")
	}
	if got, want := message(strings.Repeat("m", messageLimit+1)), strings.Repeat("m", messageLimit)+" (cut: the message is 4097 bytes)"; got != want {
		t.Errorf("a message over the limit ends %q, want %q", got[len(got)-40:], want[len(want)-40:])
	}
}

func TestSARIFCapsRelatedLocations(t *testing.T) {
	cases := map[int]string{
		maxRelated:     "",
		maxRelated + 1: "\n1 more value is not linked: code scanning rejects a result with more than 1000 locations.",
		maxRelated + 2: "\n2 more values are not linked: code scanning rejects a result with more than 1000 locations.",
	}
	for others, tail := range cases {
		values := []check.Value{val("b", "f.yaml", 1)}
		for i := range others {
			values = append(values, val("a", "f.yaml", i+2))
		}
		rep := check.Report{Results: []check.Result{agreeRule("c", nil, check.Extracted{Extractor: "e", Values: values})}}
		out := decodeSARIF(t, writeSARIF(t, rep))
		res := out.Runs[0].Results[0]
		if len(res.Related) != min(others, maxRelated) {
			t.Errorf("%d other values: %d related locations, want %d", others, len(res.Related), min(others, maxRelated))
		}
		if !strings.HasSuffix(res.Message.Text, `[value 999](999).`+tail) {
			t.Errorf("%d other values: message ends %q, want %q", others, res.Message.Text[len(res.Message.Text)-120:], tail)
		}
		if problems := messageProblems(out); len(problems) > 0 {
			t.Errorf("%d other values: %s", others, strings.Join(problems, "\n"))
		}
	}
}

// The cap is checked on the pieces of the file, not on a 25,000 result file
// written and read back, which takes half a minute under -race; the
// warning's way into the file is TestSARIFBoundsTheUpload's.
func TestSARIFCapsResults(t *testing.T) {
	erratum := check.Result{Check: check.Check{ID: "p", Run: []string{"x"}}, Verdict: check.VerdictFalse}
	for n, want := range map[int]string{
		maxResults:     "",
		maxResults + 1: "1 erratum left out of this file: code scanning rejects a run with more than 25000 results",
	} {
		rep := check.Report{Results: make([]check.Result, n)}
		for i := range rep.Results {
			rep.Results[i] = erratum
		}
		p := build(rep)
		if len(p.results) != maxResults {
			t.Errorf("%d errata: %d results, want %d", n, len(p.results), maxResults)
		}
		if got := p.warning(cuts{results: len(p.results), notes: len(p.notes)}); got != want {
			t.Errorf("%d errata: warning %q, want %q", n, got, want)
		}
	}
}

// The vendored schema is the OASIS file byte for byte. An edit that loosened
// it would pass every document this package writes.
func TestSchemaFileIsTheOASISText(t *testing.T) {
	b, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != "c3b4bb2d6093897483348925aaa73af03b3e3f4bd4ca38cef26dcb4212a2682e" {
		t.Fatalf("%s has sha256 %s, not the OASIS errata01 text (see testdata/NOTICE)", schemaFile, got)
	}
}
