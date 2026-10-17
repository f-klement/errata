// Package rule evaluates a rule: it runs the rule's extractors and tests the
// relation the rule states between their values. It fails closed, so a rule
// that could not compare anything is unknown and never true.
package rule

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// AssertNotImplemented is the message of every rule that uses assert, until
// CEL evaluation lands.
const AssertNotImplemented = "assert: CEL evaluation is not implemented yet"

// Run evaluates the rule c against the repository at root. It has the
// signature of engine.Runner, so the engine dispatches to it as it does to the
// check protocol.
func Run(ctx context.Context, root string, c check.Check) (res check.Result) {
	start := time.Now()
	res.Check = c
	// res is a named result so this assignment reaches the caller.
	defer func() { res.Duration = time.Since(start) }()

	if strings.TrimSpace(c.Rule.Assert) != "" {
		res.Verdict, res.Message = check.VerdictUnknown, AssertNotImplemented
		return res
	}
	res.Verdict, res.Message, res.Extracted = agree(ctx, root, *c.Rule)
	return res
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
	byName := make(map[string]check.Extractor, len(r.Extractors))
	for _, e := range r.Extractors {
		byName[e.Name] = e.Extractor
	}

	var (
		all        []found
		extracted  []check.Extracted
		problems   []string
		listedNone bool
	)
	for _, name := range r.Agree {
		if err := ctx.Err(); err != nil {
			return check.VerdictUnknown, fmt.Sprintf("cancelled before extractor %q ran: %v", name, err), extracted
		}
		values, err := byName[name].Extract(root)
		if err != nil {
			problems = append(problems, fmt.Sprintf("extractor %q: %v", name, err))
			continue
		}
		extracted = append(extracted, check.Extracted{Extractor: name, Values: values})
		switch {
		case len(values) == 0 && slices.Contains(r.AllowEmpty, name):
			listedNone = true
		case len(values) == 0:
			problems = append(problems, fmt.Sprintf("extractor %q found no value", name))
		default:
			for _, v := range values {
				all = append(all, found{value: v, extractor: name})
			}
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
