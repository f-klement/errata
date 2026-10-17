package check

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Rule is what makes a check a rule rather than a program check: the
// extractors it names and the relation their values must satisfy. A rule
// states exactly one relation, agree or assert.
type Rule struct {
	// Extractors are sorted by name, so every message that lists them reads
	// the same on every run.
	Extractors []NamedExtractor
	// Agree names the extractors whose values must all be equal.
	Agree []string
	// Assert is a CEL expression over the extracted values.
	Assert string
	// Compiled is Assert compiled and type checked at load. A rule that uses
	// assert and has none is unknown, never true.
	Compiled Assertion
	// AllowEmpty names the extractors that may find no value without the rule
	// becoming unknown. Any other extractor that finds none makes it unknown.
	AllowEmpty []string
	// Normalize is applied, in order, to every value before agree compares
	// it or assert sees it.
	Normalize []Normalization
}

// Assertion is a rule's assert: expression, compiled and type checked at load.
type Assertion interface {
	// Eval evaluates the expression over each extractor's values, by
	// extractor name. An error means it could not decide, and the rule is
	// unknown.
	Eval(ctx context.Context, values map[string][]string) (bool, error)
}

// NamedExtractor is one entry under a rule's extract:, with the name agree:
// and assert: refer to it by.
type NamedExtractor struct {
	Name      string
	Extractor Extractor
}

// Extracted is what one extractor returned to a rule, in the order it found
// the values. A report reads the locations from here, not from the message.
type Extracted struct {
	Extractor string
	Values    []Value
}

// Normalization is one transformation applied to a value before a rule
// compares it.
type Normalization string

// The normalizations a rule's normalize: accepts.
const (
	NormalizeTrim  Normalization = "trim"
	NormalizeLower Normalization = "lower"
)

// Normalizations lists every value normalize: accepts, in the order the load
// error names them.
var Normalizations = []Normalization{NormalizeTrim, NormalizeLower}

// Normalized returns text after every normalization of r, in order. It is the
// form agree compares and assert sees, so a report that groups a rule's Extracted values by it
// gets the groups agree got.
func (r Rule) Normalized(text string) string {
	for _, n := range r.Normalize {
		switch n {
		case NormalizeTrim:
			text = strings.TrimSpace(text)
		case NormalizeLower:
			text = strings.ToLower(text)
		}
	}
	return text
}

// validate returns every problem with r. c is the check r belongs to, for the
// keys that a rule and a program check treat differently.
func (r Rule) validate(c Check) []error {
	var errs []error
	if len(c.Run) > 0 {
		errs = append(errs, errors.New("a check is a program check (run) or a rule (extract), never both"))
	}
	if c.Timeout != 0 {
		// An extractor reads files in process and takes no context, so a
		// timeout on a rule would be a key that does nothing.
		errs = append(errs, errors.New("timeout applies to a program check only: a rule's extractors take no timeout"))
	}
	if c.Scope != ScopeRepo && c.Scope.known() {
		// An unknown scope is refused once, by Check.Validate.
		// Every extractor reads a file under the root. A live rule would be
		// true about the running system without having read it.
		errs = append(errs, fmt.Errorf("scope %q: a rule reads files, so its scope is repo; a live extractor comes with a provider", c.Scope))
	}
	if len(r.Extractors) == 0 {
		errs = append(errs, errors.New("extract is required for a rule and must declare at least one extractor"))
	}

	hasAgree, hasAssert := len(r.Agree) > 0, strings.TrimSpace(r.Assert) != ""
	switch {
	case hasAgree && hasAssert:
		errs = append(errs, errors.New("agree and assert are both set: a rule states one relation, so write two checks"))
	case !hasAgree && !hasAssert:
		errs = append(errs, errors.New("a rule needs agree or assert"))
	}

	declared := make(map[string]bool, len(r.Extractors))
	for _, e := range r.Extractors {
		declared[e.Name] = true
	}
	named := make(map[string]bool, len(r.Agree))
	for _, name := range r.Agree {
		if !declared[name] {
			errs = append(errs, fmt.Errorf("agree names %q, which extract does not declare", name))
		}
		if named[name] {
			// A repeated name compares an extractor with itself, which
			// satisfies the coverage check below without comparing anything.
			errs = append(errs, fmt.Errorf("agree names %q twice", name))
		}
		named[name] = true
	}
	listed := make(map[string]bool, len(r.AllowEmpty))
	for _, name := range r.AllowEmpty {
		if !declared[name] {
			errs = append(errs, fmt.Errorf("allow_empty names %q, which extract does not declare", name))
		}
		if listed[name] {
			errs = append(errs, fmt.Errorf("allow_empty names %q twice", name))
		}
		listed[name] = true
	}
	if hasAgree && !hasAssert {
		// An extractor that agree leaves out is never compared, so the rule
		// would pass without testing the place the author added it for.
		for _, e := range r.Extractors {
			if !slices.Contains(r.Agree, e.Name) {
				errs = append(errs, fmt.Errorf("extractor %q is declared but agree does not name it", e.Name))
			}
		}
	}

	for _, n := range r.Normalize {
		if !slices.Contains(Normalizations, n) {
			errs = append(errs, fmt.Errorf("normalize %q must be one of %s", n, joinNormalizations()))
		}
	}
	return errs
}

func joinNormalizations() string {
	names := make([]string, len(Normalizations))
	for i, n := range Normalizations {
		names[i] = string(n)
	}
	return strings.Join(names, ", ")
}
