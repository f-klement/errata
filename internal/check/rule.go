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
	// Syntax is the ecosystem whose rules compare: semver reads the values
	// in, one of Syntaxes, or "" for the reading of all five, npm's rules
	// winning where they differ.
	Syntax string
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
	// Eval evaluates the expression over what each extractor of the rule
	// returned, normalized: each value's text, or the value whole for an
	// extractor whose Description gives it whole, and the file of each,
	// which by_file groups the values by. An error means it could not
	// decide, and the rule is unknown.
	Eval(ctx context.Context, extracted []Extracted) (bool, error)
}

// NamedExtractor is one entry under a rule's extract:, with the name agree:
// and assert: refer to it by.
type NamedExtractor struct {
	Name      string
	Extractor Extractor
	// Spec is the entry as the loader resolved it, with every default it
	// fills in written out, for errata list to print. It marshals to JSON
	// as errata.yaml spells the entry, such as {"yaml": {"file": ...}}; in
	// errata it is an extract.Spec, which this package cannot name.
	Spec any
	// Description is what the extractor returns, as its kind told the
	// loader: the label a message names the kind by, whether a value may be
	// not literal, the shape assert reads each value in with a record's
	// fields, and the extractors of the rule it reads. The loader gives it
	// for an extractor that did not build too; it is the zero Description
	// when the config named no kind.
	Description Description
}

// AgreeOn is what an entry of agree names: the extractor of that name, with
// field "", or, for x.f, the extractor x and its records' field f. No
// extractor's name holds a dot (ExtractorName), so the entry splits at its
// first dot and the part before it is the only name to look up. ok is false
// when that names no extractor, or the entry ends in the dot, which
// Validate refuses; the field is then checked against the extractor's
// fields by Validate, not here.
func (r Rule) AgreeOn(entry string) (x NamedExtractor, field string, ok bool) {
	name, field, dotted := strings.Cut(entry, ".")
	if dotted && field == "" {
		return NamedExtractor{}, "", false
	}
	for _, e := range r.Extractors {
		if e.Name == name {
			return e, field, true
		}
	}
	return NamedExtractor{}, "", false
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

// Syntaxes lists every value syntax: accepts, in the order the load error
// names them: internal/semver's syntaxes, which a test in internal/rule
// holds this list to.
var Syntaxes = []string{"npm", "cargo", "pep440", "go", "rubygems"}

// SyntaxShown is how errata list names the syntax a compare: semver rule
// reads with: its syntax:, or any when it sets none.
func (r Rule) SyntaxShown() string {
	if r.Syntax == "" {
		return "any"
	}
	return r.Syntax
}

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

// ErrRuleTimeout refuses a timeout on a rule. A built in extractor reads
// files in process, bounded by ADR 0003's limits, and its context only
// carries the run's cancellation, so a timeout on a rule would be a key that
// does nothing.
var ErrRuleTimeout = errors.New("timeout applies to a program check and a generated check: a rule's extractors take no timeout")

// ErrRuleInPlace refuses in_place on a rule. A rule only reads, so it runs
// in the repository beside the others and never in a copy (ADR 0019).
var ErrRuleInPlace = errors.New("in_place applies to a program check and a generated check: a rule only reads and always runs in the repository")

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
		// A rule that compares text and names a syntax does not load; a
		// listing of one built otherwise still shows what it holds.
		if r.Comparison() == CompareSemver || r.Syntax != "" {
			relation = append(relation, Field{"syntax", r.SyntaxShown()})
		}
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
		how := "compare " + string(r.Comparison())
		if r.Comparison() == CompareSemver || r.Syntax != "" {
			how += ", syntax " + r.SyntaxShown()
		}
		lines = append(lines, Line{"agree:", quoted(q, r.Agree, ", ") + " (" + how + ")"})
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
	if c.InPlace {
		errs = append(errs, ErrRuleInPlace)
	}
	hasAgree, hasAssert := r.relations()
	errs = append(errs, r.shapeErrors(hasAgree, hasAssert)...)
	declared, byName, nameErrs := r.declaredExtractors()
	errs = append(errs, nameErrs...)
	errs = append(errs, r.dependencyErrors(declared)...)
	a := agreeing{r: r, declared: declared, byName: byName, named: map[string]bool{}, compared: map[string]bool{}}
	for _, name := range r.Agree {
		a.entry(name)
	}
	errs = append(errs, a.errs...)
	errs = append(errs, r.allowEmptyErrors(declared)...)
	errs = append(errs, r.skipErrors(declared, byName)...)
	errs = append(errs, r.coverageErrors(a.compared, hasAgree, hasAssert)...)
	errs = append(errs, r.compareErrors(hasAssert)...)
	errs = append(errs, r.syntaxErrors(hasAssert)...)
	return append(errs, r.normalizeErrors()...)
}

// relations reports whether r writes an agree and an assert.
func (r *Rule) relations() (hasAgree, hasAssert bool) {
	return len(r.Agree) > 0, strings.TrimSpace(r.Assert) != ""
}

// shapeErrors are the problems with what r holds: no extractor, or not
// exactly one of agree and assert.
func (r *Rule) shapeErrors(hasAgree, hasAssert bool) []error {
	var errs []error
	if len(r.Extractors) == 0 {
		errs = append(errs, errors.New("extract is required for a rule and must declare at least one extractor"))
	}
	switch {
	case hasAgree && hasAssert:
		errs = append(errs, errors.New("agree and assert are both set: a rule states one relation, so write two checks"))
	case !hasAgree && !hasAssert:
		errs = append(errs, errors.New("a rule needs agree or assert"))
	}
	return errs
}

// declaredExtractors are the names r's extract declares, its extractors
// by name, and the problems with those names.
func (r *Rule) declaredExtractors() (map[string]bool, map[string]NamedExtractor, []error) {
	var errs []error
	declared := make(map[string]bool, len(r.Extractors))
	byName := make(map[string]NamedExtractor, len(r.Extractors))
	for _, e := range r.Extractors {
		if err := ExtractorName(e.Name); err != nil {
			errs = append(errs, err)
		}
		declared[e.Name] = true
		byName[e.Name] = e
	}
	return declared, byName, errs
}

// agreeing is what Validate knows as it reads the entries of r's agree:
// the extractors extract declares, by name, the entries named so far, the
// extractors agree reads, and the problems found.
type agreeing struct {
	r               *Rule
	declared        map[string]bool
	byName          map[string]NamedExtractor
	named, compared map[string]bool
	errs            []error
}

// entry reads name, an entry of agree.
func (a *agreeing) entry(name string) {
	if a.declared[name] && ExtractorName(name) != nil {
		// Refused above by its name. Split as x.f it would be refused
		// again, as an entry naming nothing and an extractor left out.
		a.compared[name] = true
		return
	}
	if prefix, ends := strings.CutSuffix(name, "."); ends && a.declared[prefix] {
		// AgreeOn names nothing for it, so it would be refused as an
		// entry extract does not declare and its extractor as left out.
		a.errs = append(a.errs, dotError(name, prefix, a.byName[prefix].Description, a.named[name]))
		a.named[name], a.compared[prefix] = true, true
		return
	}
	a.field(name)
}

// dotError is the problem with name, an entry of agree that ends in a dot
// after prefix, a declared extractor whose description is d; twice when
// agree named it before. The hint names only a form that loads for the
// extractor.
func dotError(name, prefix string, d Description, twice bool) error {
	switch {
	case twice:
		return fmt.Errorf("agree names %q twice", name)
	case d.Fields != nil:
		return fmt.Errorf("agree names %q, which ends in a dot and so names no field; name one field, such as %s.%s", name, prefix, d.Fields[0])
	case d.Whole():
		return fmt.Errorf("agree names %q, which ends in a dot and so names no field; its extractor hands assert each mapping whole (as: map), so write an assert, or leave as: map out", name)
	}
	return fmt.Errorf("agree names %q, which ends in a dot and so names no field; write %q for the extractor, or %s.<field> for a field of its records", name, prefix, prefix)
}

// field reads name, an entry of agree that names an extractor or a field
// of its records.
func (a *agreeing) field(name string) {
	x, field, ok := a.r.AgreeOn(name)
	if !ok {
		a.errs = append(a.errs, fmt.Errorf("agree names %q, which extract does not declare", name))
	}
	if a.named[name] {
		// A repeated name compares an extractor with itself, which
		// satisfies the coverage check below without comparing anything.
		a.errs = append(a.errs, fmt.Errorf("agree names %q twice", name))
	}
	if ok {
		if err := fieldError(name, x, field); err != nil {
			a.errs = append(a.errs, err)
		}
	}
	a.named[name] = true
	a.compared[x.Name] = true
}

// fieldError is the problem with name, an entry of agree that names field
// of the extractor x, or x itself for a field of "": a field x's values do
// not have, or an extractor whose values are not flat; nil for none.
func fieldError(name string, x NamedExtractor, field string) error {
	if field != "" {
		return namedFieldError(name, x, field)
	}
	return flatError(name, x)
}

// namedFieldError is the problem with name, which names field of x's
// records: x returns none, or they have no such field.
func namedFieldError(name string, x NamedExtractor, field string) error {
	switch {
	case x.Description.Fields == nil:
		return fmt.Errorf("agree names %q, but extractor %q returns no record, so it has no field %q", name, x.Name, field)
	case !slices.Contains(x.Description.Fields, field):
		return fmt.Errorf("agree names %q, but the records of extractor %q have no field %q; their fields are %s", name, x.Name, field, strings.Join(x.Description.Fields, ", "))
	}
	return nil
}

// flatError is the problem with name, which names x itself: x returns
// records or whole mappings, which agree does not compare.
func flatError(name string, x NamedExtractor) error {
	switch {
	case x.Description.Fields != nil:
		return fmt.Errorf("agree names %q, whose extractor returns records (as: record); agree compares flat values, so name one field, such as %s.%s, or write an assert", name, name, x.Description.Fields[0])
	case x.Description.Whole():
		return fmt.Errorf("agree names %q, whose extractor hands assert each mapping whole (as: map); agree compares flat values, so write an assert, or leave as: map out", name)
	}
	return nil
}

// listErrors are the problems with name, an extractor the key key lists:
// one extract does not declare, or one seen before.
func listErrors(key, name string, declared, seen map[string]bool) []error {
	var errs []error
	if !declared[name] {
		errs = append(errs, fmt.Errorf("%s names %q, which extract does not declare", key, name))
	}
	if seen[name] {
		errs = append(errs, fmt.Errorf("%s names %q twice", key, name))
	}
	return errs
}

// allowEmptyErrors are the problems with r's allow_empty.
func (r *Rule) allowEmptyErrors(declared map[string]bool) []error {
	var errs []error
	listed := make(map[string]bool, len(r.AllowEmpty))
	for _, name := range r.AllowEmpty {
		errs = append(errs, listErrors("allow_empty", name, declared, listed)...)
		listed[name] = true
	}
	return errs
}

// skipErrors are the problems with r's skip_not_literal.
func (r *Rule) skipErrors(declared map[string]bool, byName map[string]NamedExtractor) []error {
	var errs []error
	skipped := make(map[string]bool, len(r.SkipNotLiteral))
	for _, name := range r.SkipNotLiteral {
		errs = append(errs, listErrors("skip_not_literal", name, declared, skipped)...)
		if !skipped[name] {
			if err := literalOnly(name, byName[name]); err != nil {
				errs = append(errs, err)
			}
		}
		skipped[name] = true
	}
	return errs
}

// literalOnly is the problem with name, listed in skip_not_literal, when
// its extractor x returns literal values only: it has nothing to leave
// out, so the key would load and do nothing. An extractor that did not
// build is reported where it failed.
func literalOnly(name string, x NamedExtractor) error {
	if x.Extractor == nil || x.Description.NotLiteral {
		return nil
	}
	whose := "whose extractor"
	if x.Description.Label != "" {
		whose = "whose kind of extractor, " + x.Description.Label + ","
	}
	return fmt.Errorf("skip_not_literal names %q, %s returns only literal values, so it has nothing to leave out", name, whose)
}

// coverageErrors are the extractors an agree rule declares and does not
// compare: such a rule would pass without testing the place the author
// added it for.
func (r *Rule) coverageErrors(compared map[string]bool, hasAgree, hasAssert bool) []error {
	if !hasAgree || hasAssert {
		return nil
	}
	var errs []error
	for _, e := range r.Extractors {
		if !compared[e.Name] {
			errs = append(errs, fmt.Errorf("extractor %q is declared but agree does not name it", e.Name))
		}
	}
	return errs
}

// compareErrors are the problems with r's compare: one beside an assert,
// which compares in its expression, so compare would load and do nothing,
// or one errata does not know.
func (r *Rule) compareErrors(hasAssert bool) []error {
	switch {
	case r.Compare != "" && hasAssert:
		return []error{fmt.Errorf("compare %q applies to agree; assert compares in its expression, with semver() and semver_satisfies() for versions", r.Compare)}
	case r.Compare != "" && !slices.Contains(Compares, r.Compare):
		return []error{fmt.Errorf("compare %q must be one of %s", r.Compare, joinCompares())}
	}
	return nil
}

// syntaxErrors are the problems with r's syntax: one beside an assert, one
// in a rule that compares no versions, or one errata does not know.
func (r *Rule) syntaxErrors(hasAssert bool) []error {
	switch {
	case r.Syntax == "":
	case hasAssert:
		return []error{fmt.Errorf("syntax %q applies to agree with compare: semver; an assert names the syntax as the third argument of semver_satisfies", r.Syntax)}
	case r.Comparison() != CompareSemver:
		return []error{fmt.Errorf("syntax %q applies to compare: semver, and this rule compares %s", r.Syntax, r.Comparison())}
	case !slices.Contains(Syntaxes, r.Syntax):
		return []error{fmt.Errorf("syntax %q must be one of %s", r.Syntax, strings.Join(Syntaxes, ", "))}
	}
	return nil
}

// normalizeErrors are the normalizations r names that errata does not know.
func (r *Rule) normalizeErrors() []error {
	var errs []error
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
