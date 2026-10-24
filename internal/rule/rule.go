// Package rule evaluates a rule: it runs the rule's extractors and tests the
// relation the rule states between their values, agree or assert. It fails
// closed, so a rule that could not compare anything is unknown and never true.
package rule

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/semver"
)

// Run evaluates the rule c against the repository at root. It has the
// signature of engine.Runner, so the engine dispatches to it as it does to the
// check protocol.
func Run(ctx context.Context, root string, c check.Check) (res check.Result) {
	start := time.Now()
	res.Check = c
	// res is a named result so this assignment reaches the caller.
	defer func() { res.Duration = time.Since(start) }()

	if strings.TrimSpace(c.Rule.Assert) != "" {
		res.Verdict, res.Message, res.Extracted = assert(ctx, root, *c.Rule)
	} else {
		res.Verdict, res.Message, res.Note, res.Extracted = agree(ctx, root, *c.Rule)
	}
	switch counted := check.LeftOutCount(res.Extracted); {
	case counted == "":
	case res.Message == "":
		res.Message = counted
	default:
		res.Message += "\n" + counted
	}
	return res
}

// count is n and noun, with an s for any n but one.
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// extractAll runs the extractors names lists, in that order. It returns what
// each one that did not fail returned, and a problem for each that failed,
// returned a not literal value while skip_not_literal does not list it, or
// found no value while allow_empty does not list it. An extractor that
// skip_not_literal lists has its not literal values left out, and one whose
// every value was left out found no value. cancelled is set when the context
// ended before an extractor ran.
func extractAll(ctx context.Context, root string, r check.Rule, names []string) (extracted []check.Extracted, problems []string, cancelled string) {
	byName := make(map[string]check.Extractor, len(r.Extractors))
	for _, e := range r.Extractors {
		byName[e.Name] = e.Extractor
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return extracted, problems, fmt.Sprintf("cancelled before extractor %q ran: %v", name, err)
		}
		values, err := byName[name].Extract(root)
		if err != nil {
			problems = append(problems, fmt.Sprintf("extractor %q: %v", name, err))
			continue
		}
		x := check.Extracted{Extractor: name, Values: values}
		computed := slices.ContainsFunc(values, func(v check.Value) bool { return v.NotLiteral })
		skip := slices.Contains(r.SkipNotLiteral, name)
		switch {
		case computed && skip:
			x.Values = nil
			for _, v := range values {
				if v.NotLiteral {
					x.LeftOut = append(x.LeftOut, v)
				} else {
					x.Values = append(x.Values, v)
				}
			}
		case computed:
			problems = append(problems, notLiteral(name, values))
		}
		extracted = append(extracted, x)
		if len(x.Values) == 0 && !slices.Contains(r.AllowEmpty, name) {
			if len(x.LeftOut) > 0 {
				// The count line at the end of the message says how many.
				problems = append(problems, fmt.Sprintf("extractor %q found no literal value", name))
			} else {
				problems = append(problems, fmt.Sprintf("extractor %q found no value", name))
			}
		}
	}
	return extracted, problems, ""
}

// notLiteral is the problem of an extractor that returned not literal values
// while skip_not_literal does not list it: each such value, with the place
// it was found. The file only computes such a value, so comparing its text
// would test what the file wrote rather than what it means.
func notLiteral(name string, values []check.Value) string {
	var computed []check.Value
	var texts []string
	for _, v := range values {
		if v.NotLiteral {
			computed = append(computed, v)
			texts = append(texts, v.Text)
		}
	}
	q := newQuoter(texts)
	var b strings.Builder
	fmt.Fprintf(&b, "extractor %q returned %s, which a rule does not compare as text, since the file only computes such a value; list %q in skip_not_literal to leave such values out",
		name, count(len(computed), "not literal value"), name)
	for _, v := range computed {
		fmt.Fprintf(&b, "\n  %s at %s:%d", q.quote(v.Text), v.At.File, v.At.Line)
	}
	return b.String()
}

// assert runs every extractor in name order and evaluates the compiled
// expression over their normalised values. An extractor that fails, returns
// a not literal value while skip_not_literal does not list it, or finds no
// value while allow_empty does not list it, makes the rule unknown before
// CEL runs; so does an evaluation that cannot decide.
func assert(ctx context.Context, root string, r check.Rule) (check.Verdict, string, []check.Extracted) {
	if r.Compiled == nil {
		return check.VerdictUnknown, "assert: the expression was not compiled at load, so it cannot run", nil
	}
	names := make([]string, len(r.Extractors))
	for i, e := range r.Extractors {
		names[i] = e.Name
	}
	extracted, problems, cancelled := extractAll(ctx, root, r, names)
	switch {
	case cancelled != "":
		return check.VerdictUnknown, cancelled, extracted
	case len(problems) > 0:
		return check.VerdictUnknown, strings.Join(problems, "\n"), extracted
	}

	values := make(map[string][]string, len(extracted))
	for _, x := range extracted {
		texts := make([]string, len(x.Values))
		for i, v := range x.Values {
			texts[i] = r.Normalized(v.Text)
		}
		values[x.Extractor] = texts
	}
	holds, err := r.Compiled.Eval(ctx, values)
	switch {
	case err != nil:
		return check.VerdictUnknown, "assert: " + err.Error() + "\n" + saw(r, extracted), extracted
	case holds:
		return check.VerdictTrue, "", extracted
	default:
		return check.VerdictFalse, "assert: the expression is false\n" + saw(r, extracted), extracted
	}
}

// maxQuoted is the most bytes of a value a message quotes. A longer value
// is quoted maxQuoted bytes at a time, from where it differs from the values
// it is compared with (see quoter), with the offset and its length. It holds
// an image reference with a sha256 digest, about 150 bytes, and most URLs.
// The limit counts the value's own bytes, and quoting can write up to three
// bytes for one that does not print, so a rule over 100000 long values of
// non printing bytes writes a message of about 80 MB, measured; ordinary
// text stays near a third of that. The values themselves are not cut: the
// comparison reads them whole, and the result's extracted values carry them.
const maxQuoted = 256

// quoteContext is how many bytes before the first one that differs a
// clipped value's window starts, so the difference is seen in place.
const quoteContext = 32

// quoter quotes the values of one message. A value of at most maxQuoted
// bytes is quoted whole. A longer one is quoted from quoteContext bytes
// before the first byte where it differs from every other value of the
// message, the longest prefix it shares with any of them, so two values
// that agree in their first maxQuoted bytes still print differently:
//
//	..."/a/a/a/a/a/a/a/a/a/a/ax?tag=1.2.3" (from byte 266 of 299)
//
// For two values the windows always differ. Among four or more long values
// that share prefixes pairwise, two windows can still match; those two also
// print an FNV-64 fingerprint of the whole value. Finding the prefixes sorts the values once, so it is
// linear in their bytes times the logarithm of their number.
type quoter struct {
	start map[string]int  // where a long value's window starts
	print map[string]bool // a long value whose window another one shares
}

func newQuoter(values []string) quoter {
	q := quoter{start: map[string]int{}, print: map[string]bool{}}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	for i, v := range sorted {
		if len(v) <= maxQuoted {
			continue
		}
		shared := 0
		if i > 0 {
			shared = commonPrefix(v, sorted[i-1])
		}
		if i+1 < len(sorted) {
			shared = max(shared, commonPrefix(v, sorted[i+1]))
		}
		start := max(0, shared-quoteContext)
		for start > 0 && !utf8.RuneStart(v[start]) {
			start--
		}
		q.start[v] = start
	}
	seen := map[string]string{}
	for v := range q.start {
		w := q.window(v)
		if other, ok := seen[w]; ok {
			q.print[v], q.print[other] = true, true
		}
		seen[w] = v
	}
	return q
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// window is a long value's quoted window with its marks.
func (q quoter) window(v string) string {
	start := q.start[v]
	end := min(len(v), start+maxQuoted)
	for end < len(v) && !utf8.RuneStart(v[end]) {
		end--
	}
	var b strings.Builder
	if start > 0 {
		b.WriteString("...")
	}
	b.WriteString(strconv.Quote(v[start:end]))
	if end < len(v) {
		b.WriteString("...")
	}
	if start > 0 {
		fmt.Fprintf(&b, " (from byte %d of %d)", start, len(v))
	} else {
		fmt.Fprintf(&b, " (%d bytes)", len(v))
	}
	return b.String()
}

// quote is v for the message, as %q writes it, clipped as quoter says.
func (q quoter) quote(v string) string {
	if _, long := q.start[v]; !long {
		if len(v) > maxQuoted { // a value newQuoter was not given
			return newQuoter([]string{v}).quote(v)
		}
		return strconv.Quote(v)
	}
	w := q.window(v)
	if q.print[v] {
		h := fnv.New64a()
		_, _ = h.Write([]byte(v)) // a hash.Hash never returns an error
		w = strings.TrimSuffix(w, ")") + fmt.Sprintf(", fingerprint %016x)", h.Sum64())
	}
	return w
}

// saw is the part of an assert's message that shows what it evaluated: the
// expression, then each value by the index the expression reads it at, in the
// form it saw, with the place it was found.
func saw(r check.Rule, extracted []check.Extracted) string {
	var b strings.Builder
	for i, line := range strings.Split(strings.TrimSpace(r.Assert), "\n") {
		label := "            "
		if i == 0 {
			label = "expression: "
		}
		fmt.Fprintf(&b, "  %s%s\n", label, strings.TrimRight(line, " \t\r"))
	}
	var all []string
	for _, x := range extracted {
		for _, v := range x.Values {
			all = append(all, r.Normalized(v.Text))
		}
	}
	q := newQuoter(all)
	for _, x := range extracted {
		if len(x.Values) == 0 {
			fmt.Fprintf(&b, "  %s = [] (allow_empty)\n", x.Extractor)
		}
		for i, v := range x.Values {
			fmt.Fprintf(&b, "  %s[%d] = %s at %s:%d\n", x.Extractor, i, q.quote(r.Normalized(v.Text)), v.At.File, v.At.Line)
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// found is one value with the extractor that returned it.
type found struct {
	value     check.Value
	extractor string
}

// agree runs every extractor that r.Agree names, in that order, and compares
// the normalised string form of every value they return, as text or, with
// compare: semver, as versions and ranges. It also returns
// what each extractor that did not fail returned, for the result, and a
// note for a rule that held, which the text output prints.
func agree(ctx context.Context, root string, r check.Rule) (check.Verdict, string, string, []check.Extracted) {
	extracted, problems, cancelled := extractAll(ctx, root, r, r.Agree)
	if cancelled != "" {
		return check.VerdictUnknown, cancelled, "", extracted
	}
	var (
		all        []found
		listedNone bool
	)
	for _, x := range extracted {
		if len(x.Values) == 0 && slices.Contains(r.AllowEmpty, x.Extractor) {
			listedNone = true
		}
		for _, v := range x.Values {
			all = append(all, found{value: v, extractor: x.Extractor})
		}
	}
	switch {
	case len(problems) > 0:
		return check.VerdictUnknown, strings.Join(problems, "\n"), "", extracted
	case len(all) == 0:
		return check.VerdictUnknown, "no extractor found a value, so agree has nothing to compare", "", extracted
	case len(all) == 1 && !listedNone:
		// One value agrees with itself. Only an extractor that allow_empty
		// lists coming back empty makes one value a comparison the author
		// asked for.
		f := all[0]
		return check.VerdictUnknown, fmt.Sprintf("agree found one value, so it has nothing to compare: %s at %s:%d (%s)",
			newQuoter(nil).quote(f.value.Text), f.value.At.File, f.value.At.Line, f.extractor), "", extracted
	}

	groups := group(all, r)
	if r.Comparison() == check.CompareSemver {
		verdict, message, note := agreeSemver(groups)
		return verdict, message, note, extracted
	}
	if len(groups) == 1 {
		return check.VerdictTrue, "", "", extracted
	}
	return check.VerdictFalse, differ(groups), "", extracted
}

// distinct is one normalised value and every place it was found.
type distinct struct {
	text  string
	where []found
}

// group collects the values by their normalised text. Groups keep the order
// of the first value in each, and values keep the order they were found in, so
// the message is stable for a given set of extractor results.
func group(all []found, r check.Rule) []distinct {
	var groups []distinct
	index := map[string]int{}
	for _, f := range all {
		text := r.Normalized(f.value.Text)
		i, ok := index[text]
		if !ok {
			i = len(groups)
			index[text] = i
			groups = append(groups, distinct{text: text})
		}
		groups[i].where = append(groups[i].where, f)
	}
	return groups
}

// differ is the message of an agree rule whose values differ: each distinct
// value on its own line, with every location it was found at.
func differ(groups []distinct) string {
	texts := make([]string, len(groups))
	for i, g := range groups {
		texts[i] = g.text
	}
	q := newQuoter(texts)
	var b strings.Builder
	b.WriteString("agree: the values differ")
	for _, g := range groups {
		fmt.Fprintf(&b, "\n  %s at %s", q.quote(g.text), places(g))
	}
	return b.String()
}

// semverReader reads the values of one comparison and refuses, with a
// *semver.BoundError, the one that takes their ranges past the bound.
type semverReader interface {
	Parse(s string) (semver.Value, error)
}

// newSemverReader gives agreeSemver its reader. A test replaces it with one
// that refuses within a few values, since semver's own bound is a million
// intervals, too slow to reach once per mutant.
var newSemverReader = func() semverReader { return new(semver.Reader) }

// agreeSemver is agree with compare: semver over the distinct values. Each
// must read as a version or a range, or the rule is unknown, naming each
// one that does not; a value is never compared as text. A semver.Reader
// reads them, and refuses the value that takes their ranges past
// semver.MaxIntervals before reading any more. The values agree as
// semver.Agree decides. Any message lists each value with its places and
// what it was read as, each clipped as the quoter clips a value. A rule that
// held on ranges alone says so in its note.
func agreeSemver(groups []distinct) (verdict check.Verdict, message, note string) {
	texts := make([]string, len(groups))
	for i, g := range groups {
		texts[i] = g.text
	}
	q := newQuoter(texts)
	values := make([]semver.Value, len(groups))
	var (
		unread []string
		reader = newSemverReader()
		bound  *semver.BoundError
	)
	for i, g := range groups {
		v, err := reader.Parse(g.text)
		switch {
		case errors.As(err, &bound):
			return check.VerdictUnknown, "agree: compare semver stopped at " + q.quote(g.text) + " at " + places(g) + ": " + err.Error(), ""
		case err != nil:
			unread = append(unread, fmt.Sprintf("%s at %s: %v", q.quote(g.text), places(g), err))
		}
		values[i] = v
	}
	if len(unread) > 0 {
		return check.VerdictUnknown, "agree: compare semver reads every value as a version or a range, and cannot read " +
			count(len(unread), "value") + "\n  " + strings.Join(unread, "\n  "), ""
	}
	out := semver.Agree(values)
	var lead string
	switch {
	case out.Holds && out.OnlyRanges:
		return check.VerdictTrue, "", "agree: only ranges were compared, since no value is a version, and they overlap"
	case out.Holds:
		return check.VerdictTrue, "", ""
	case out.OnlyRanges:
		lead = "agree: only ranges were compared, since no value is a version, and no release satisfies them all"
	case values[out.First].IsRange() || values[out.Second].IsRange():
		version, rng := out.First, out.Second
		if values[version].IsRange() {
			version, rng = rng, version
		}
		lead = fmt.Sprintf("agree: the version %s does not satisfy the range %s", q.quote(texts[version]), q.quote(texts[rng]))
	default:
		lead = fmt.Sprintf("agree: the versions %s and %s differ at the precision both state", q.quote(texts[out.First]), q.quote(texts[out.Second]))
	}
	var b strings.Builder
	b.WriteString(lead)
	for i, g := range groups {
		fmt.Fprintf(&b, "\n  %s (%s) at %s", q.quote(g.text), clip(values[i].String()), places(g))
	}
	return check.VerdictFalse, b.String(), ""
}

// clip is what a value was read as, cut to maxQuoted bytes with its length
// named: a range's intervals can take several times the bytes of its text.
func clip(s string) string {
	if len(s) <= maxQuoted {
		return s
	}
	return fmt.Sprintf("%s... (%d bytes)", s[:maxQuoted], len(s))
}

// places is every place a distinct value was found, with its extractor.
func places(g distinct) string {
	out := make([]string, len(g.where))
	for i, f := range g.where {
		out[i] = fmt.Sprintf("%s:%d (%s)", f.value.At.File, f.value.At.Line, f.extractor)
	}
	return strings.Join(out, ", ")
}
