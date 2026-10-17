// Package rule evaluates a rule: it runs the rule's extractors and tests the
// relation the rule states between their values, agree or assert. It fails
// closed, so a rule that could not compare anything is unknown and never true.
package rule

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/f-klement/errata/internal/check"
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
		return res
	}
	res.Verdict, res.Message, res.Extracted = agree(ctx, root, *c.Rule)
	return res
}

// extractAll runs the extractors names lists, in that order. It returns what
// each one that did not fail returned, and a problem for each that failed or
// found no value while allow_empty does not list it. cancelled is set when the
// context ended before an extractor ran.
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
		extracted = append(extracted, check.Extracted{Extractor: name, Values: values})
		if len(values) == 0 && !slices.Contains(r.AllowEmpty, name) {
			problems = append(problems, fmt.Sprintf("extractor %q found no value", name))
		}
	}
	return extracted, problems, ""
}

// assert runs every extractor in name order and evaluates the compiled
// expression over their normalised values. An extractor that fails, or finds
// no value while allow_empty does not list it, makes the rule unknown before
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
	for _, x := range extracted {
		if len(x.Values) == 0 {
			fmt.Fprintf(&b, "  %s = [] (allow_empty)\n", x.Extractor)
		}
		for i, v := range x.Values {
			fmt.Fprintf(&b, "  %s[%d] = %q at %s:%d\n", x.Extractor, i, r.Normalized(v.Text), v.At.File, v.At.Line)
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
// the normalised string form of every value they return. It also returns
// what each extractor that did not fail returned, for the result.
func agree(ctx context.Context, root string, r check.Rule) (check.Verdict, string, []check.Extracted) {
	extracted, problems, cancelled := extractAll(ctx, root, r, r.Agree)
	if cancelled != "" {
		return check.VerdictUnknown, cancelled, extracted
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
		return check.VerdictUnknown, strings.Join(problems, "\n"), extracted
	case len(all) == 0:
		return check.VerdictUnknown, "no extractor found a value, so agree has nothing to compare", extracted
	case len(all) == 1 && !listedNone:
		// One value agrees with itself. Only an extractor that allow_empty
		// lists coming back empty makes one value a comparison the author
		// asked for.
		f := all[0]
		return check.VerdictUnknown, fmt.Sprintf("agree found one value, so it has nothing to compare: %q at %s:%d (%s)",
			f.value.Text, f.value.At.File, f.value.At.Line, f.extractor), extracted
	}

	groups := group(all, r)
	if len(groups) == 1 {
		return check.VerdictTrue, "", extracted
	}
	return check.VerdictFalse, differ(groups), extracted
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
	var b strings.Builder
	b.WriteString("agree: the values differ")
	for _, g := range groups {
		places := make([]string, len(g.where))
		for i, f := range g.where {
			places[i] = fmt.Sprintf("%s:%d (%s)", f.value.At.File, f.value.At.Line, f.extractor)
		}
		fmt.Fprintf(&b, "\n  %q at %s", g.text, strings.Join(places, ", "))
	}
	return b.String()
}
