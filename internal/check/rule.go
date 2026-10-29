package check

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Rule is what makes a check a rule: the
// extractors it names and the relation their values must satisfy. A rule
// states exactly one relation, agree or assert.
type Rule struct {
	// Extractors are sorted by name, so every message that lists them reads
	// the same on every run.
	Extractors []NamedExtractor
	// Agree names the extractors whose values must all agree, each by its
	// name, or one field of a record extractor's records as name.field
	// (AgreeOn).
	Agree []string
	// Compare is how agree compares the values: CompareText or
	// CompareSemver. The loader sets CompareText on an agree rule that names
	// none, and "" reads as CompareText.
	Compare Compare
	// Assert is a CEL expression over the extracted values.
	Assert string
	// Compiled is Assert compiled and type checked at load. A rule that uses
	// assert and has none is unknown, never true.
	Compiled Assertion
	// AllowEmpty names the extractors that may find no value without the rule
	// becoming unknown. Any other extractor that finds none makes it unknown.
	AllowEmpty []string
	// SkipNotLiteral names the extractors whose not literal values are left
	// out of the comparison. A not literal value from any other extractor
	// makes the rule unknown. An extractor whose every value was left out
	// counts as one that found no value, so AllowEmpty decides.
	SkipNotLiteral []string
	// Normalize is applied, in order, to every value before agree compares
	// it or assert sees it.
	Normalize []Normalization
}

// Assertion is a rule's assert: expression, compiled and type checked at load.
type Assertion interface {
	// Eval evaluates the expression over each extractor's values, by
	// extractor name, and the file of each value, by the same name and the
	// value's index, which by_file groups the values by. maps holds the
	// values whole of each extractor the expression reads as maps, by the
	// same name and index; values then holds their text for no use. An
	// error means it could not decide, and the rule is unknown.
	Eval(ctx context.Context, values, files map[string][]string, maps map[string][]*Data) (bool, error)
}

// NamedExtractor is one entry under a rule's extract:, with the name agree:
// and assert: refer to it by.
type NamedExtractor struct {
	Name string
	// Kind is the kind of extractor, such as json or "ini, plain dialect on
	// pytest.ini",
	// for a message; "" when the config gave none.
	Kind      string
	Extractor Extractor
	// Spec is the entry as the loader resolved it, with every default it
	// fills in written out, for errata list to print. It marshals to JSON
	// as errata.yaml spells the entry, such as {"yaml": {"file": ...}}; in
	// errata it is an extract.Spec, which this package cannot name.
	Spec any
	// Maps marks an extractor that hands assert each mapping its path
	// selects whole (as: map), so assert reads it as a list of maps and
	// agree, which compares flat values, does not take it.
	Maps bool
	// Fields are the keys of the records the extractor returns (as:
	// record), which agree can name one of as name.field; nil for an
	// extractor that returns no record.
	Fields []string
}

// AgreeOn is what an entry of agree names: the extractor of that name,
// with field "", or, when no extractor has the name and it is x.f, split at
// its last dot, the extractor x and its records' field f. Validate refuses
// an entry that is both. ok is false when it names neither
// an extractor nor a field of one, which Validate refuses; the field is
// then checked against the extractor's Fields by Validate, not here.
func (r Rule) AgreeOn(entry string) (x NamedExtractor, field string, ok bool) {
	for _, e := range r.Extractors {
		if e.Name == entry {
			return e, "", true
		}
	}
	dot := strings.LastIndexByte(entry, '.')
	if dot < 0 {
		return NamedExtractor{}, "", false
	}
	for _, e := range r.Extractors {
		if e.Name == entry[:dot] {
			return e, entry[dot+1:], true
		}
	}
	return NamedExtractor{}, "", false
}

// fieldToo reports whether name, which names an extractor, also splits at
// its last dot into a record extractor of byName and one of its fields.
// AgreeOn takes the extractor, so the field could not be named at all.
func fieldToo(byName map[string]NamedExtractor, name string) bool {
	dot := strings.LastIndexByte(name, '.')
	return dot >= 0 && slices.Contains(byName[name[:dot]].Fields, name[dot+1:])
}

// Extracted is what one extractor returned to a rule, in the order it found
// the values. A report reads the locations from here, not from the message.
type Extracted struct {
	Extractor string
	// Values are the values the rule compared, or would have compared had it
	// not been unknown.
	Values []Value
	// LeftOut are the not literal values that skip_not_literal left out of
	// the comparison. A report shows each as left out.
	LeftOut []Value
}

// Blame names the value of a result that its rule blames:
// Extracted[Extractor].Values[Value]. The rule decides it with the verdict,
// so a report reads it and never compares the values again.
type Blame struct{ Extractor, Value int }

// LeftOutCount is the part of a rule's message that counts the values
// skip_not_literal left out: a line for each extractor that left any out, in
// the order of extracted, or "" when none did. An extractor whose every value
// was left out says the rule compared nothing from it, which a rule that
// holds through allow_empty would otherwise not say. The rule ends its
// message with it, and a report that cuts a message keeps it whole.
func LeftOutCount(extracted []Extracted) string {
	var lines []string
	for _, x := range extracted {
		n := len(x.LeftOut)
		switch {
		case n == 0:
		case len(x.Values) > 0 && n == 1:
			lines = append(lines, fmt.Sprintf("skip_not_literal left out 1 not literal value of extractor %q", x.Extractor))
		case len(x.Values) > 0:
			lines = append(lines, fmt.Sprintf("skip_not_literal left out %d not literal values of extractor %q", n, x.Extractor))
		case n == 1:
			lines = append(lines, fmt.Sprintf("skip_not_literal left out the one value of extractor %q, which is not literal, so the rule compared nothing from it", x.Extractor))
		default:
			lines = append(lines, fmt.Sprintf("skip_not_literal left out all %d not literal values of extractor %q, so the rule compared nothing from it", n, x.Extractor))
		}
	}
	return strings.Join(lines, "\n")
}

// Compare is a way agree compares a rule's values.
type Compare string

// The values a rule's compare: accepts.
const (
	// CompareText: the texts after normalize must be equal.
	CompareText Compare = "text"
	// CompareSemver: the values are versions and ranges, which agree at the
	// precision each states (internal/semver).
	CompareSemver Compare = "semver"
)

// Compares lists every value compare: accepts, in the order the load error
// names them.
var Compares = []Compare{CompareText, CompareSemver}

// Comparison is how agree compares r's values: r.Compare, or CompareText
// when it is not set.
func (r Rule) Comparison() Compare {
	if r.Compare == "" {
		return CompareText
	}
	return r.Compare
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

// ErrRuleTimeout refuses a timeout on a rule. An extractor reads files in
// process and takes no context, so a timeout on a rule would be a key that
// does nothing.
var ErrRuleTimeout = errors.New("timeout applies to a program check and a generated check: a rule's extractors take no timeout")

// Name is rule.
func (*Rule) Name() string { return "rule" }

// Nouns are rule and rules.
func (*Rule) Nouns() (one, many string) { return "rule", "rules" }

// ScopeRefusal refuses every scope but repo. Every extractor reads a file
// under the root, so a live rule would be true about the running system
// without having read it.
func (*Rule) ScopeRefusal(s Scope) string {
	if s != ScopeRepo {
		return "a rule reads files, so its scope is repo; a live extractor comes with a provider"
	}
	return ""
}

// ListFields are extract, each entry by its name, the relation, and the
// lists of names a rule takes, as [] when it sets none.
func (r *Rule) ListFields(Check) Fields {
	extract := make(map[string]any, len(r.Extractors))
	for _, x := range r.Extractors {
		extract[x.Name] = x.Spec
	}
	relation := Fields{}
	if len(r.Agree) > 0 {
		relation = Fields{{"agree", r.Agree}, {"compare", string(r.Comparison())}}
	} else if r.Assert != "" {
		relation = Fields{{"assert", r.Assert}}
	}
	normalize := []string{}
	for _, n := range r.Normalize {
		normalize = append(normalize, string(n))
	}
	return Fields{
		{"extract", extract},
		{"relation", relation},
		{"normalize", normalize},
		{"allow_empty", OrEmpty(r.AllowEmpty)},
		{"skip_not_literal", OrEmpty(r.SkipNotLiteral)},
	}
}

// ListLines are extract, a line for each extractor, agree with its compare
// or assert, and each list of names the rule sets.
func (r *Rule) ListLines(_ Check, q Quoter) []Line {
	var extract []string
	for _, x := range r.Extractors {
		extract = append(extract, q.Value(x.Name)+": "+q.Spec(x.Spec))
	}
	lines := []Line{{"extract:", strings.Join(extract, "\n")}}
	if len(r.Agree) > 0 {
		lines = append(lines, Line{"agree:", quoted(q, r.Agree, ", ") + " (compare " + string(r.Comparison()) + ")"})
	} else {
		lines = append(lines, Line{"assert:", r.Assert})
	}
	normalize := make([]string, len(r.Normalize))
	for i, n := range r.Normalize {
		normalize[i] = string(n)
	}
	for _, f := range []struct {
		label string
		names []string
	}{{"normalize:", normalize}, {"allow_empty:", r.AllowEmpty}, {"skip_not_literal:", r.SkipNotLiteral}} {
		if len(f.names) > 0 {
			lines = append(lines, Line{f.label, quoted(q, f.names, ", ")})
		}
	}
	return lines
}

// Validate returns every problem with r. c is the check r is the kind of,
// for its timeout, which a rule does not take.
func (r *Rule) Validate(c Check) []error {
	var errs []error
	if c.Timeout != 0 {
		errs = append(errs, ErrRuleTimeout)
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
	byName := make(map[string]NamedExtractor, len(r.Extractors))
	for _, e := range r.Extractors {
		declared[e.Name] = true
		byName[e.Name] = e
	}
	errs = append(errs, r.dependencyErrors(declared)...)
	named := make(map[string]bool, len(r.Agree))
	compared := make(map[string]bool, len(r.Agree)) // the extractors agree reads
	for _, name := range r.Agree {
		x, field, ok := r.AgreeOn(name)
		if !ok {
			errs = append(errs, fmt.Errorf("agree names %q, which extract does not declare", name))
		}
		if named[name] {
			// A repeated name compares an extractor with itself, which
			// satisfies the coverage check below without comparing anything.
			errs = append(errs, fmt.Errorf("agree names %q twice", name))
		}
		switch {
		case !ok:
		case field == "" && fieldToo(byName, name):
			dot := strings.LastIndexByte(name, '.')
			errs = append(errs, fmt.Errorf("agree names %q, which is both an extractor and field %q of the records of extractor %q; rename the extractor, so each entry names one", name, name[dot+1:], name[:dot]))
		case field != "" && x.Fields == nil:
			errs = append(errs, fmt.Errorf("agree names %q, but extractor %q returns no record, so it has no field %q", name, x.Name, field))
		case field != "" && !slices.Contains(x.Fields, field):
			errs = append(errs, fmt.Errorf("agree names %q, but the records of extractor %q have no field %q; their fields are %s", name, x.Name, field, strings.Join(x.Fields, ", ")))
		case field == "" && x.Fields != nil:
			errs = append(errs, fmt.Errorf("agree names %q, whose extractor returns records (as: record); agree compares flat values, so name one field, such as %s.%s, or write an assert", name, name, x.Fields[0]))
		case field == "" && x.Maps:
			errs = append(errs, fmt.Errorf("agree names %q, whose extractor hands assert each mapping whole (as: map); agree compares flat values, so write an assert, or leave as: map out", name))
		}
		named[name] = true
		compared[x.Name] = true
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
	skipped := make(map[string]bool, len(r.SkipNotLiteral))
	for _, name := range r.SkipNotLiteral {
		if !declared[name] {
			errs = append(errs, fmt.Errorf("skip_not_literal names %q, which extract does not declare", name))
		}
		if skipped[name] {
			errs = append(errs, fmt.Errorf("skip_not_literal names %q twice", name))
		}
		// An extractor that did not build is reported where it failed. One
		// whose kind returns literal values only has nothing to leave out,
		// so the key would load and do nothing.
		if x := byName[name]; x.Extractor != nil && !skipped[name] && !MayReturnNotLiteral(x.Extractor) {
			whose := "whose extractor"
			if x.Kind != "" {
				whose = "whose kind of extractor, " + x.Kind + ","
			}
			errs = append(errs, fmt.Errorf("skip_not_literal names %q, %s returns only literal values, so it has nothing to leave out", name, whose))
		}
		skipped[name] = true
	}
	if hasAgree && !hasAssert {
		// An extractor that agree leaves out is never compared, so the rule
		// would pass without testing the place the author added it for.
		for _, e := range r.Extractors {
			if !compared[e.Name] {
				errs = append(errs, fmt.Errorf("extractor %q is declared but agree does not name it", e.Name))
			}
		}
	}

	// Ifs rather than a switch: Go's coverage profile leaves case
	// expressions outside every block, so their mutants would read as not
	// covered and pull the package under its mutant coverage floor.
	if r.Compare != "" && hasAssert {
		// assert compares in its expression, so compare would load and do
		// nothing.
		errs = append(errs, fmt.Errorf("compare %q applies to agree; assert compares in its expression, with semver() and semver_satisfies() for versions", r.Compare))
	} else if r.Compare != "" && !slices.Contains(Compares, r.Compare) {
		errs = append(errs, fmt.Errorf("compare %q must be one of %s", r.Compare, joinCompares()))
	}

	for _, n := range r.Normalize {
		if !slices.Contains(Normalizations, n) {
			errs = append(errs, fmt.Errorf("normalize %q must be one of %s", n, joinNormalizations()))
		}
	}
	return errs
}

func joinCompares() string {
	names := make([]string, len(Compares))
	for i, c := range Compares {
		names[i] = string(c)
	}
	return strings.Join(names, ", ")
}

func joinNormalizations() string {
	names := make([]string, len(Normalizations))
	for i, n := range Normalizations {
		names[i] = string(n)
	}
	return strings.Join(names, ", ")
}
