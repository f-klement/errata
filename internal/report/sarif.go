package report

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
)

// SARIF writes SARIF 2.1.0, which GitHub code scanning turns into inline
// annotations. Each check is a rule in tool.driver.rules, and each erratum is
// a result. An unknown and a skipped check are notifications of the
// invocation, not results: neither showed a claim false, and code scanning
// would open an alert for any result. So is a rule that holds with values
// that skip_not_literal left out, since it held on what was left. A skip is a
// note, or, when the report's skips fail the run, an error that sets
// executionSuccessful false, as an unknown does.
type SARIF struct {
	// Version is errata's version, written as the driver's version so a
	// reader knows which errata wrote the file.
	Version string
}

// sarifSchema is the schema the output follows: the OASIS errata01 text,
// which testdata/sarif-schema-2.1.0.json holds.
const sarifSchema = "https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json"

// srcRoot is the base every URI is relative to. code scanning resolves it to
// the checkout, so a value's repository relative path lands on the file.
const srcRoot = "%SRCROOT%"

// configFile is where a result with no value of its own points when its
// check carries no location, as a check built by hand rather than loaded
// does: the default config file, at line 1. A loaded program check, or a rule
// whose extractors returned nothing, points at the line its entry starts on.
const configFile = "errata.yaml"

// The limits below come from GitHub's page "SARIF support for code scanning"
// (https://docs.github.com/en/code-security/reference/code-scanning/sarif-files/sarif-support,
// read 2026-10-17). It rejects a whole upload over 10 MB gzipped, or with
// more than 25,000 results in a run or 1,000 locations in a result, and it
// limits a rule's short and full descriptions to 1,024 characters. One
// rejected file loses every alert of the run, so the writer stays inside them.
const (
	descriptionLimit = 1024
	maxResults       = 25000
	// maxRelated leaves room for the result's own location.
	maxRelated = 999
)

// What GitHub receives is not this file. upload-sarif (github/codeql-action
// at 3db7caf, src/upload-lib.ts) adds partialFingerprints to each result,
// fingerprints.ts formatting it as up to 16 hex digits, a colon and a count,
// adds run.automationDetails, then sends zlib.gzipSync(JSON.stringify(log)):
// compact JSON gzipped by Node. So the budget is measured on compact JSON,
// gzipped, plus uploadPerResult bytes for each result kept.
const (
	// uploadLimit reads GitHub's 10 MB as 10^6 bytes per MB, the smaller of
	// the two readings.
	uploadLimit = 10_000_000
	// uploadPerResult is what one fingerprint adds gzipped. 25,000
	// random fingerprints measured 21 bytes each, so 40 holds a count of
	// several digits as well.
	uploadPerResult = 40
	// gzipBudget leaves 2% of uploadLimit for Node's zlib against Go's
	// gzip, measured at 0.5% on the same bytes, and for automationDetails.
	gzipBudget = uploadLimit * 98 / 100
	// plainBudget is the compact size, fingerprints counted, below which the
	// file needs no gzip to know it fits: deflate adds 5 bytes per 64 KB
	// block, and a header, to data it cannot compress.
	plainBudget = uploadLimit * 97 / 100
)

// GitHub documents no limit on a message. These bound what one piece of
// data adds to a message, so a value or a program's output of any size
// cannot fill the file on its own.
const (
	// valueLimit is the number of characters of a value's text a message
	// quotes.
	valueLimit = 200
	// messageLimit is the number of characters of a check's message, which
	// is a program's whole stdout, that a result or notification quotes.
	messageLimit = 4096
	// shortLimit is what a notification keeps of a message, a reason or a
	// gap, in a file that does not fit with every result left out.
	shortLimit = 200
)

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool        sarifTool         `json:"tool"`
	Invocations []sarifInvocation `json:"invocations"`
	Results     []sarifResult     `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID                   string       `json:"id"`
	Name                 string       `json:"name"`
	ShortDescription     sarifMessage `json:"shortDescription"`
	FullDescription      sarifMessage `json:"fullDescription"`
	Help                 sarifMessage `json:"help"`
	DefaultConfiguration sarifConfig  `json:"defaultConfiguration"`
	Properties           sarifProps   `json:"properties"`
}

type sarifConfig struct {
	Level string `json:"level"`
}

type sarifProps struct {
	Scope string `json:"scope"`
}

// sarifMessage is a plain text message. Every piece of data in one goes
// through plain or cut, so data adds no placeholder and no embedded link.
type sarifMessage struct {
	Text string `json:"text"`
}

type sarifInvocation struct {
	ExecutionSuccessful bool                `json:"executionSuccessful"`
	Notifications       []sarifNotification `json:"toolExecutionNotifications"`
}

type sarifNotification struct {
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations,omitempty"`
	Rule      *sarifRuleRef   `json:"associatedRule,omitempty"`
}

type sarifRuleRef struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	RuleIndex int             `json:"ruleIndex"`
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations"`
	Related   []sarifLocation `json:"relatedLocations,omitempty"`
}

type sarifLocation struct {
	ID       int            `json:"id,omitempty"`
	Physical *sarifPhysical `json:"physicalLocation,omitempty"`
	Message  *sarifMessage  `json:"message,omitempty"`
}

type sarifPhysical struct {
	Artifact sarifArtifact `json:"artifactLocation"`
	Region   *sarifRegion  `json:"region,omitempty"`
}

type sarifArtifact struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

// Write encodes r. encoding/json writes every string as valid UTF-8, with a
// replacement character for each invalid byte, so the output is a valid file
// whatever bytes a repository file put into a value.
func (s SARIF) Write(w io.Writer, r check.Report) error {
	return s.writeBounded(w, r, defaultBudget)
}

// budget is what the upload may take: gzipped, and plain, below which it
// needs no gzip to know it fits. Write passes defaultBudget; the tests pass
// one of tens of kilobytes, so the same cuts run in milliseconds.
type budget struct{ gzipped, plain int }

// defaultBudget is the budget Write holds a file to.
var defaultBudget = budget{gzipped: gzipBudget, plain: plainBudget}

// fits reports whether b, holding results results, fits once upload-sarif
// has added a fingerprint to each result and sent it as compact JSON,
// gzipped.
func (bu budget) fits(b []byte, results int) (bool, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, b); err != nil {
		return false, err
	}
	if compact.Len()+uploadPerResult*results <= bu.plain {
		return true, nil
	}
	return uploadSize(compact.Bytes(), results) <= bu.gzipped, nil
}

// uploadSize is what compact JSON holding results results takes gzipped,
// with uploadPerResult for each result's fingerprint.
func uploadSize(compact []byte, results int) int {
	var n countingWriter
	zw := gzip.NewWriter(&n)
	// Writes to a countingWriter never fail, so neither do these.
	_, _ = zw.Write(compact)
	_ = zw.Close()
	return int(n) + uploadPerResult*results
}

// most returns the largest k in [0, n] whose size is at most limit, given
// that size(0) is and size(n), which is shi, is not. size grows with k, nearly in
// proportion, so each step guesses k from the sizes at the two ends; a
// step that does not halve the interval is followed by a halving one, so
// it never takes more than twice the steps of a binary search.
func most(n, limit, shi int, size func(k int) (int, error)) (int, error) {
	lo, hi := 0, n
	slo, err := size(lo)
	if err != nil {
		return 0, err
	}
	halve := false
	for hi-lo > 1 {
		m := lo + (hi-lo)/2
		if !halve && shi > slo {
			m = lo + int(float64(limit-slo)/float64(shi-slo)*float64(hi-lo))
			m = min(max(m, lo+1), hi-1)
		}
		s, err := size(m)
		if err != nil {
			return 0, err
		}
		before := hi - lo
		if s <= limit {
			lo, slo = m, s
		} else {
			hi, shi = m, s
		}
		halve = !halve && 2*(hi-lo) > before
	}
	return lo, nil
}

// countingWriter counts the bytes written to it and keeps none.
type countingWriter int

func (c *countingWriter) Write(p []byte) (int, error) {
	*c += countingWriter(len(p))
	return len(p), nil
}

// pieces is what a file is made of. Each rule and notification also has a
// short form, for a file that does not fit even with every result left out.
type pieces struct {
	run               sarifRun
	rules, shortRules []sarifRule
	notes, shortNotes []sarifNotification
	results           []sarifResult
	// overCount is the errata past maxResults.
	overCount int
	budget    budget
}

// cuts says how much of pieces a file holds: the first results results and
// notes notifications, and the short rules and notifications when short.
type cuts struct {
	results, notes int
	short          bool
}

func (s SARIF) writeBounded(w io.Writer, r check.Report, bu budget) error {
	p := build(r)
	p.run.Tool.Driver.Version = s.Version
	p.budget = bu
	all := cuts{results: len(p.results), notes: len(p.notes)}
	compact, err := p.marshal(all, false)
	if err != nil {
		return err
	}
	ok, err := bu.fits(compact, all.results)
	if err != nil {
		return err
	}
	if !ok {
		if all, err = p.shrink(uploadSize(compact, all.results)); err != nil {
			return err
		}
	}
	b, err := p.marshal(all, true)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// shrink returns the file with the fewest cuts that fits: results left out
// first, then the rules' help and the notifications cut short, then
// notifications left out. full is the size of the file with nothing cut.
// Each step's size grows with what it keeps, so most finds the most that
// fits. When even the rules alone do not fit, it returns every cut, and the
// warning says so.
func (p pieces) shrink(full int) (cuts, error) {
	size := func(c cuts) (int, error) {
		b, err := p.marshal(c, false)
		return uploadSize(b, c.results), err
	}
	limit := p.budget.gzipped
	for _, short := range []bool{false, true} {
		none := cuts{notes: len(p.notes), short: short}
		if s, err := size(none); err != nil {
			return cuts{}, err
		} else if s > limit {
			continue
		}
		shi := full
		if short {
			var err error
			if shi, err = size(cuts{results: len(p.results), notes: len(p.notes), short: true}); err != nil {
				return cuts{}, err
			}
		}
		if shi <= limit {
			return cuts{results: len(p.results), notes: len(p.notes), short: short}, nil
		}
		keep, err := most(len(p.results), limit, shi, func(k int) (int, error) {
			return size(cuts{results: k, notes: len(p.notes), short: short})
		})
		return cuts{results: keep, notes: len(p.notes), short: short}, err
	}
	shi, err := size(cuts{notes: len(p.notes), short: true})
	if err != nil {
		return cuts{}, err
	}
	if s, err := size(cuts{short: true}); err != nil || s > limit {
		return cuts{short: true}, err
	}
	keep, err := most(len(p.notes), limit, shi, func(k int) (int, error) {
		return size(cuts{notes: k, short: true})
	})
	return cuts{notes: keep, short: true}, err
}

// build turns r into the pieces of a file, every result and notification
// whole.
func build(r check.Report) pieces {
	p := pieces{run: sarifRun{
		Tool:        sarifTool{Driver: sarifDriver{Name: "errata", InformationURI: "https://github.com/f-klement/errata"}},
		Invocations: []sarifInvocation{{ExecutionSuccessful: len(r.Unknowns())+r.FailingSkips() == 0}},
	}}
	// A skip that fails the run is an error, as an unknown is.
	skipLevel := "note"
	if r.SkipsFail {
		skipLevel = "error"
	}
	index := map[string]int{}
	for _, res := range r.Results {
		// The key is the id as encoding/json writes it, each invalid byte
		// a U+FFFD, so two ids that read alike in the file are one rule.
		key := string([]rune(res.Check.ID))
		i, ok := index[key]
		if !ok {
			// Two checks with one id would be two rules with one id. The
			// loader refuses that, so the first one stands for both here.
			i = len(p.rules)
			index[key] = i
			whole, short := sarifRuleOf(res.Check)
			p.rules, p.shortRules = append(p.rules, whole), append(p.shortRules, short)
		}
		ref := &sarifRuleRef{ID: res.Check.ID, Index: i}
		switch {
		case res.Skipped():
			note := func(limit int) sarifNotification {
				return sarifNotification{
					Level:   skipLevel,
					Message: sarifMessage{fmt.Sprintf("check %s skipped (%s): %s", plain(res.Check.ID), plain(string(res.Check.Scope)), cut(res.Gap, "gap", limit))},
					Rule:    ref,
				}
			}
			p.notes, p.shortNotes = append(p.notes, note(-1)), append(p.shortNotes, note(shortLimit))
		case res.Verdict == check.VerdictUnknown:
			// The short form, for a file that does not fit, keeps the count
			// in the message and leaves the locations out.
			locs, said := leftOutLocations(res)
			whole := sarifNotification{Level: "error", Message: sarifMessage{unknownText(res, messageLimit, -1, said)}, Locations: locs, Rule: ref}
			short := sarifNotification{Level: "error", Message: sarifMessage{unknownText(res, shortLimit, shortLimit, "")}, Rule: ref}
			p.notes, p.shortNotes = append(p.notes, whole), append(p.shortNotes, short)
		case res.Verdict == check.VerdictTrue && len(leftOut(res)) > 0:
			locs, said := leftOutLocations(res)
			note := func(limit int) sarifNotification {
				text := "check " + plain(res.Check.ID) + " holds on the values left: " + cut(strings.TrimSpace(res.Message), "message", limit)
				return sarifNotification{Level: "note", Message: sarifMessage{text}, Rule: ref}
			}
			whole := note(messageLimit)
			whole.Message.Text += said
			whole.Locations = locs
			p.notes, p.shortNotes = append(p.notes, whole), append(p.shortNotes, note(shortLimit))
		case res.Verdict == check.VerdictFalse && len(p.results) == maxResults:
			p.overCount++
		case res.Verdict == check.VerdictFalse:
			p.results = append(p.results, sarifResultOf(res, i))
		}
	}
	return p
}

// marshal writes the file c describes, with a warning when it leaves out or
// cuts anything: indented for the reader, or compact, as upload-sarif sends
// it, to measure.
func (p pieces) marshal(c cuts, indent bool) ([]byte, error) {
	run := p.run
	run.Tool.Driver.Rules = p.rules
	notes := p.notes
	if c.short {
		run.Tool.Driver.Rules, notes = p.shortRules, p.shortNotes
	}
	run.Tool.Driver.Rules = append([]sarifRule{}, run.Tool.Driver.Rules...)
	run.Results = append([]sarifResult{}, p.results[:c.results]...)
	inv := run.Invocations[0]
	inv.Notifications = append([]sarifNotification{}, notes[:c.notes]...)
	if text := p.warning(c); text != "" {
		inv.Notifications = append(inv.Notifications, sarifNotification{Level: "warning", Message: sarifMessage{text}})
	}
	run.Invocations = []sarifInvocation{inv}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	err := enc.Encode(sarifLog{Schema: sarifSchema, Version: "2.1.0", Runs: []sarifRun{run}})
	// encoding/json writes C1 controls and direction controls as they are,
	// and the file can go to stdout, so they are written as JSON escapes.
	// The escapes are made here, so the size measured is the size written.
	return escapeActedOn(buf.Bytes(), func(r rune) string { return fmt.Sprintf(`\u%04x`, r) }), err
}

// warning says what the file c describes leaves out or cuts, and why, or
// is empty when it holds everything.
func (p pieces) warning(c cuts) string {
	var said, why []string
	dropped := len(p.results) - c.results
	if n := p.overCount + dropped; n > 0 {
		said = append(said, errataCount(n)+" left out of this file")
	}
	if n := len(p.notes) - c.notes; n == 1 {
		said = append(said, "1 notification left out")
	} else if n > 0 {
		said = append(said, fmt.Sprintf("%d notifications left out", n))
	}
	if c.short {
		said = append(said, "rule help and notifications cut short")
	}
	if len(said) == 0 {
		return ""
	}
	if p.overCount > 0 {
		why = append(why, fmt.Sprintf("code scanning rejects a run with more than %d results", maxResults))
	}
	if dropped > 0 || c.notes < len(p.notes) || c.short {
		why = append(why, fmt.Sprintf("the upload would pass %d bytes gzipped, errata's bound under the 10 MB that code scanning accepts", p.budget.gzipped))
	}
	return strings.Join(said, ", ") + ": " + strings.Join(why, "; ")
}

// sarifRuleOf returns the rule for c, whole and with its help cut at
// descriptionLimit.
func sarifRuleOf(c check.Check) (whole, short sarifRule) {
	// code scanning refuses an empty string where it reads a field, and a
	// check that loaded always has a reason, so the id is only a fallback.
	reason := strings.TrimSpace(c.Reason)
	if reason == "" {
		reason = c.ID
	}
	first, _, _ := strings.Cut(reason, "\n")
	first = strings.TrimSpace(first)
	whole = sarifRule{
		ID:                   c.ID,
		Name:                 c.ID,
		ShortDescription:     sarifMessage{plainCut(first, descriptionLimit)},
		FullDescription:      sarifMessage{plainCut(reason, descriptionLimit)},
		Help:                 sarifMessage{plain(reason)},
		DefaultConfiguration: sarifConfig{Level: "error"},
		Properties:           sarifProps{Scope: string(c.Scope)},
	}
	short = whole
	short.Help = sarifMessage{cut(reason, "reason", descriptionLimit)}
	return whole, short
}

// wordJoiner is U+2060, a character of no width that only forbids a line
// break.
const wordJoiner = "\u2060"

// plain puts data into a SARIF plain text message so it reads as itself.
// SARIF 2.1.0 section 3.11.5 requires a literal brace to be written as two,
// since one brace starts a placeholder. Section 3.11.6 makes
// [text](destination) an embedded link and defines a backslash escape only
// inside link text, so a backslash anywhere else would show. Data is never
// link text here, and a link needs "](", so a word joiner between "]" and
// "(" stops data from forming one: under the section's grammar, and under a
// viewer that finds links with a regular expression.
func plain(s string) string { return plainCut(s, -1) }

// plainCut is plain that stops before the text exceeds n characters, never
// between a character and what plain adds to it. A negative n means no
// limit.
func plainCut(s string, n int) string {
	var b strings.Builder
	size := 0
	rs := []rune(s)
	for i, r := range rs {
		e := string(r)
		switch {
		case r == '{':
			e = "{{"
		case r == '}':
			e = "}}"
		case r == ']' && i+1 < len(rs) && rs[i+1] == '(':
			e = "]" + wordJoiner
		}
		width := utf8.RuneCountInString(e)
		if n >= 0 && size+width > n {
			break
		}
		size += width
		b.WriteString(e)
	}
	return b.String()
}

// cut is plain over the first limit characters of s, with a remark saying
// what was cut and how long s was. what names s in that remark. A negative
// limit means no limit.
func cut(s, what string, limit int) string {
	return cutKeeping(s, what, limit, "")
}

// cutKeeping is cut that keeps tail, the end of s, whole: s without it is cut
// to leave room for it. It is the count of values skip_not_literal left out,
// which ends a rule's message, so a long message does not cut the count off.
// A tail that is not the end of s, or that does not fit in limit, is cut with
// the rest.
func cutKeeping(s, what string, limit int, tail string) string {
	if limit < 0 {
		return plain(s)
	}
	body, kept := s, ""
	if tail != "" && strings.HasSuffix(s, "\n"+tail) && utf8.RuneCountInString(tail) < limit {
		body, kept = strings.TrimSuffix(s, "\n"+tail), "\n"+plain(tail)
		limit -= utf8.RuneCountInString(tail) + 1
	}
	head, over := prefix(body, limit)
	if !over {
		return plain(body) + kept
	}
	return plain(head) + fmt.Sprintf(" (cut: the %s is %d bytes)", what, len(s)) + kept
}

// unknownText is the notification of an unknown, its message and reason
// cut at msgLimit and reasonLimit, and said, which follows the message.
func unknownText(res check.Result, msgLimit, reasonLimit int, said string) string {
	text := "check " + plain(res.Check.ID) + " is unknown"
	if msg := strings.TrimSpace(res.Message); msg != "" {
		text += ": " + cutKeeping(msg, "message", msgLimit, check.LeftOutCount(res.Extracted))
	}
	text += said
	if reason := strings.TrimSpace(res.Check.Reason); reason != "" {
		text += "\nwhy: " + cut(reason, "reason", reasonLimit)
	}
	return text
}

func sarifResultOf(res check.Result, ruleIndex int) sarifResult {
	msg := "check " + plain(res.Check.ID) + " found its claim false"
	if m := strings.TrimSpace(res.Message); m != "" {
		msg = cutKeeping(m, "message", messageLimit, check.LeftOutCount(res.Extracted))
	}
	out := sarifResult{RuleID: res.Check.ID, RuleIndex: ruleIndex, Level: "error"}

	values := flatten(res.Extracted)
	primary := blamed(res)
	// A result that names its own place, such as the line where a
	// generated file first differs, points there. Its file is one the
	// loader held inside the repository.
	placed, inside := physical(res.At)
	if res.At.File != "" && inside {
		out.Locations = []sarifLocation{{Physical: placed}}
	}
	if out.Locations == nil {
		if primary < 0 {
			at := res.Check.At
			if at.File == "" {
				at = check.Location{File: configFile, Line: 1}
			}
			if p, inside := physical(at); inside {
				out.Locations = []sarifLocation{{Physical: p}}
			} else {
				where := cut(at.File, "path", valueLimit)
				out.Locations = []sarifLocation{{Message: &sarifMessage{"the check is in " + where + ", outside the repository"}}}
				msg += "\nThe check is in " + where + ", outside the repository, so this result has no place in a file."
			}
		} else {
			first := values[primary]
			if p, inside := physical(first.value.At); inside {
				out.Locations = []sarifLocation{{Physical: p}}
			} else {
				out.Locations = []sarifLocation{{Message: &sarifMessage{outside(first)}}}
				msg += "\nThe value is at " + cut(first.value.At.File, "path", valueLimit) + ", outside the repository, so this result has no place in a file."
			}
		}
	}

	// Compared values take the related location slots first, but for
	// leftOutReserve kept for the values left out, so neither side pushes the
	// other out; a side that needs fewer slots leaves them to the other.
	// Compared values come first among the locations.
	var compared []named
	for i, f := range values {
		if i != primary {
			compared = append(compared, f)
		}
	}
	left := leftOut(res)
	keepCompared := min(len(compared), maxRelated-min(len(left), leftOutReserve))
	keepLeft := min(len(left), maxRelated-keepCompared)
	var links, leftLinks []string
	for _, f := range compared[:keepCompared] {
		loc := relatedLocation(f, len(out.Related)+1, false)
		out.Related = append(out.Related, loc)
		links = append(links, fmt.Sprintf("[value %d](%d)", loc.ID, loc.ID))
	}
	for _, f := range left[:keepLeft] {
		loc := relatedLocation(f, len(out.Related)+1, true)
		out.Related = append(out.Related, loc)
		leftLinks = append(leftLinks, fmt.Sprintf("[value %d](%d)", loc.ID, loc.ID))
	}
	// code scanning links a related location only from the result message.
	// The link text is errata's own words; the value is in the related
	// location's message. The "]" of "[related locations]" ends any "[" that
	// data before it left open, so a viewer that reads a link as
	// [^\]]*\]\( cannot join that "[" to the first link here.
	if len(links) > 0 {
		msg += "\nThe other values, as [related locations]: " + strings.Join(links, ", ") + "."
	}
	if len(leftLinks) > 0 {
		msg += "\nThe values left out as not literal, as [related locations]: " + strings.Join(leftLinks, ", ") + "."
	}
	msg += notLinked(len(compared)-keepCompared, len(left)-keepLeft)
	out.Message = sarifMessage{msg}
	return out
}

// leftOutReserve is how many related location slots a result keeps for
// values skip_not_literal left out when the compared values would fill the
// cap. Compared values come first, since they show the disagreement, and
// code scanning shows only the first 100 locations of a result, so 899 slots
// still hold more compared values than it shows. 100 left out values are
// enough to show which places the rule did not compare, and the message
// counts the rest of each side.
const leftOutReserve = 100

// notLiteralRemark ends the message of a related location that holds a
// value skip_not_literal left out.
const notLiteralRemark = "not literal, so skip_not_literal left it out of the comparison"

// relatedLocation is the location of value f, numbered id, with a message
// that names it, and says it was left out when it was. Ids from 1 keep each
// location distinct, which the schema requires, even when two values share a
// place.
func relatedLocation(f named, id int, leftOut bool) sarifLocation {
	loc := sarifLocation{ID: id}
	text := label(f)
	if p, inside := physical(f.value.At); inside {
		loc.Physical = p
	} else {
		text = outside(f)
	}
	if leftOut {
		text += ", " + notLiteralRemark
	}
	loc.Message = &sarifMessage{text}
	return loc
}

// notLinked is the sentence that counts the values a result could not link,
// compared and left out, or "" when it linked every one.
func notLinked(compared, leftOut int) string {
	n := compared + leftOut
	if n == 0 {
		return ""
	}
	noun := "values are"
	if n == 1 {
		noun = "value is"
	}
	of := ""
	if leftOut > 0 {
		of = fmt.Sprintf(", %d compared and %d left out as not literal", compared, leftOut)
	}
	return fmt.Sprintf("\n%d more %s not linked%s: code scanning rejects a result with more than %d locations.", n, noun, of, maxRelated+1)
}

// leftOutLocations names the values skip_not_literal left out of res as the
// locations of a notification, as an erratum names them as related
// locations, and returns the sentence the notification's message ends with.
// Notifications have no documented limit on locations; the result's cap
// holds here too.
func leftOutLocations(res check.Result) ([]sarifLocation, string) {
	left := leftOut(res)
	if len(left) == 0 {
		return nil, ""
	}
	keep := min(len(left), maxRelated)
	locs := make([]sarifLocation, 0, keep)
	for _, f := range left[:keep] {
		locs = append(locs, relatedLocation(f, len(locs)+1, true))
	}
	text := "\nThe values left out as not literal are the locations of this notification."
	if rest := len(left) - keep; rest > 0 {
		text += fmt.Sprintf(" %d more are not listed: the notification lists at most %d.", rest, maxRelated)
	}
	return locs, text
}

// label names a value in a message: its text quoted as the agree message
// quotes it, cut at valueLimit, and its extractor.
func label(f named) string {
	text := quoteValue(f.value.Text)
	if f.value.Shown != "" {
		text = f.value.Shown
	}
	return plain(text + f.value.Notes() + " (" + f.extractor + ")")
}

// outside is the message of a value whose path leaves the repository. It has
// no physical location, so the message names the path.
func outside(f named) string {
	return label(f) + " at " + cut(f.value.At.File, "path", valueLimit) + ", outside the repository"
}

// named is one value with the extractor that returned it.
type named struct {
	value     check.Value
	extractor string
}

func flatten(extracted []check.Extracted) []named {
	var out []named
	for _, e := range extracted {
		for _, v := range e.Values {
			out = append(out, named{value: v, extractor: e.Extractor})
		}
	}
	return out
}

// blamed is the index in flatten(res.Extracted) of the value res's rule
// blames, the one its erratum points at, or -1 when it blames none. The
// rule decided it with its verdict; a report does not compare the values
// again.
func blamed(res check.Result) int {
	b := res.Blamed
	if b == nil || b.Extractor < 0 || b.Extractor >= len(res.Extracted) || b.Value < 0 || b.Value >= len(res.Extracted[b.Extractor].Values) {
		return -1
	}
	i := b.Value
	for _, x := range res.Extracted[:b.Extractor] {
		i += len(x.Values)
	}
	return i
}

// physical places at under %SRCROOT%. It reports false for a location
// marked outside, whose path is not from the root, and for one that
// check.Location.Inside refuses: absolute, naming a host or a drive, or
// climbing out with "..", with a backslash read as part of a name and read
// as a separator. relativeURI encodes a backslash as %5C inside one
// segment, and a consumer on Windows decodes it and takes it as a
// separator; upload-sarif opens the file to fingerprint it, so a name that
// climbs out under either reading would hash a line of a file outside the
// checkout. The rule refuses such a value's location already; this keeps
// the reporter from writing one if a value ever carries it.
func physical(at check.Location) (*sarifPhysical, bool) {
	file := at.File
	if file == "" {
		file = configFile
	}
	if !(check.Location{File: file, Outside: at.Outside}).Inside() {
		return nil, false
	}
	p := &sarifPhysical{Artifact: sarifArtifact{URI: relativeURI(file), URIBaseID: srcRoot}}
	if at.Line >= 1 {
		p.Region = &sarifRegion{StartLine: at.Line}
	}
	return p, true
}

// relativeURI turns a repository relative path into a relative URI reference.
// url.URL escapes every byte a path may not hold, an invalid UTF-8 byte
// included, and puts "./" before a first segment with a colon so it does not
// read as a scheme.
func relativeURI(path string) string {
	return (&url.URL{Path: path}).String()
}
