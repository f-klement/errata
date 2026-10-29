// Package regex is the regex extractor: capture group 1 of every match of a
// pattern in a file, or with as: record every named group of each match,
// with core's clocked search. A pattern may reference the values of another
// extractor of its rule, (?{name}), and is then searched once for each value
// (ADR 0010).
package regex

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns capture group 1 of every match of Pattern in File, or in
// each file Files names, a file or a glob (core.Files). With As record it
// returns a record for each match instead, its fields the pattern's named
// groups (core.Search.Records). A Pattern that references an extractor's
// values reads each value as ValuesAs says. With a Context, a match counts
// only where the lines around it have it.
type Spec struct {
	File     string   `json:"file,omitempty"`
	Files    []string `json:"files,omitempty"`
	Pattern  string   `json:"pattern"`
	MaxBytes int64    `json:"max_bytes,omitempty"`
	As       string   `json:"as,omitempty"`
	// ValuesAs is how a pattern that references an extractor's values
	// reads each one: Literal, the default, quoted with regexp.QuoteMeta,
	// or AsPattern, as RE2 syntax.
	ValuesAs string `json:"values_as,omitempty"`
	// Context is the lines around a match, and the patterns that must and
	// must not match in them, for the match to count (ADR 0012).
	Context *core.Context `json:"context,omitempty"`
}

// The values values_as takes.
const (
	Literal   = "literal"
	AsPattern = "pattern"
)

// Build checks r and returns its extractor: core.Search, or a
// core.FileSet of one for each file, for a pattern that references no
// values, since the search is then the whole extractor and its tests are
// core's; a Reference for one that does.
func Build(r Spec) (check.Extractor, error) {
	if r.As != "" && r.As != "record" {
		return nil, fmt.Errorf("regex: as %s is not one errata reads; write record to read each match's named groups into one record, or leave the key out for group 1", core.Quote(r.As))
	}
	ref, err := parseReference(r.Pattern)
	if err != nil {
		return nil, err
	}
	if ref != nil && r.As == "record" {
		// A search for each value returns flat values; read as records they
		// would lose the fields the rule names.
		return nil, fmt.Errorf("regex: pattern %s references values, and as: record reads only a pattern that references none; leave as out to read group 1 of each value's matches", core.Quote(r.Pattern))
	}
	var around *core.Around
	if r.Context != nil {
		if around, err = core.CompileContext("regex", *r.Context); err != nil {
			return nil, err
		}
	}
	if ref == nil {
		if r.ValuesAs != "" {
			return nil, errors.New("regex: values_as applies to a pattern that references an extractor's values, as (?{name}) does, and this pattern references none")
		}
		return core.BuildOneOrSet("regex", r.File, r.Files, r.MaxBytes, func(file string, maxBytes int64, _ bool) (core.FileReader, error) {
			e, err := core.CompileSearch("regex", file, r.Pattern, maxBytes)
			if err != nil {
				return nil, err
			}
			e = e.Around(around)
			if r.As == "record" {
				return e.Records("regex")
			}
			return e, nil
		})
	}
	return buildReference(r, *ref, around)
}

// reference is where a pattern references an extractor's values: the
// name, and the pattern before and after (?{name}).
type reference struct {
	name, before, after string
}

// with is the pattern with value, already in RE2 syntax, in place of the
// reference, as core.Each puts it there.
func (r reference) with(value string) string {
	return core.EachPattern(r.before, value, r.after)
}

// text is the reference as the pattern writes it.
func (r reference) text() string { return "(?{" + r.name + "})" }

// referenceAt finds a well formed reference: (?{, a name with no brace or
// parenthesis, and }).
var referenceAt = regexp.MustCompile(`\(\?\{([^{}()]+)\}\)`)

// parseReference returns the reference pattern holds, or nil when it holds
// no (?{, which RE2 refuses as syntax, so no pattern that loaded before
// means something else now. A pattern with two, or with a (?{ that is no
// reference, is refused.
func parseReference(pattern string) (*reference, error) {
	n := strings.Count(pattern, "(?{")
	if n == 0 {
		return nil, nil
	}
	if n > 1 {
		return nil, fmt.Errorf("regex: pattern %s references values %d times; a pattern references the values of one extractor, once", core.Quote(pattern), n)
	}
	at := referenceAt.FindStringSubmatchIndex(pattern)
	if at == nil {
		return nil, fmt.Errorf("regex: pattern %s holds (?{ but no reference; write (?{name}), with the name of an extractor of the rule", core.Quote(pattern))
	}
	return &reference{name: pattern[at[2]:at[3]], before: pattern[:at[0]], after: pattern[at[1]:]}, nil
}

// sentinel is the name of the group referenceGroup puts in the
// reference's place, with an underscore more for each time the pattern
// already holds it, since RE2 lets two groups share a name.
const sentinel = "errata_reference"

// buildReference checks a spec whose pattern holds ref: its file or files
// and max_bytes as any regex's, values_as, the pattern with an empty value
// in place of the reference as any pattern, and where the reference stands.
func buildReference(r Spec, ref reference, around *core.Around) (check.Extractor, error) {
	if r.ValuesAs != "" && r.ValuesAs != Literal && r.ValuesAs != AsPattern {
		return nil, fmt.Errorf("regex: values_as %s must be %s or %s", core.Quote(r.ValuesAs), Literal, AsPattern)
	}
	asPattern := r.ValuesAs == AsPattern
	_, err := core.BuildOneOrSet("regex", r.File, r.Files, r.MaxBytes, func(file string, maxBytes int64, _ bool) (core.FileReader, error) {
		if _, err := core.CheckSource("regex", file, maxBytes); err != nil {
			return nil, err
		}
		e, err := core.CompileSearch("regex", file, ref.with(""), maxBytes)
		if err != nil {
			return nil, fmt.Errorf("%w (read with %s as an empty value)", err, ref.text())
		}
		return e, nil
	})
	if err != nil {
		return nil, err
	}
	group := referenceGroup(ref)
	if group == 0 {
		return nil, fmt.Errorf("regex: pattern %s: the reference %s stands inside a character class or \\Q...\\E, where it is text; move it out of it",
			core.Quote(r.Pattern), ref.text())
	}
	if asPattern && group == 1 {
		return nil, fmt.Errorf("regex: pattern %s: with values_as: %s the reference %s stands before capture group 1, so a value's own groups would come first; put it inside group 1 or after it",
			core.Quote(r.Pattern), AsPattern, ref.text())
	}
	return Reference{spec: r, ref: ref, asPattern: asPattern, around: around}, nil
}

// referenceGroup is the number of the capture group that stands in the
// reference's place in the pattern parsed with one: one more than the groups
// that open before the reference, or 0 when the group is not there, because
// the reference stands where RE2 reads text. buildReference calls it once
// the pattern with (?:) in the group's place compiled, and a named group
// parses wherever (?:) does, so a parse that fails has no group either.
func referenceGroup(ref reference) int {
	name := sentinel
	for strings.Contains(ref.before+ref.after, name) {
		name += "_"
	}
	parsed, err := syntax.Parse("(?m)"+ref.before+"(?P<"+name+">)"+ref.after, syntax.Perl)
	if err != nil {
		return 0
	}
	return groupNamed(parsed, name)
}

// groupNamed is the number of the capture group named name in re, 0 when
// there is none.
func groupNamed(re *syntax.Regexp, name string) int {
	if re.Op == syntax.OpCapture && re.Name == name {
		return re.Cap
	}
	for _, sub := range re.Sub {
		if n := groupNamed(sub, name); n > 0 {
			return n
		}
	}
	return 0
}

// Reference is the regex extractor of a pattern that references the values
// of another extractor of its rule. It is a check.Dependent: its rule runs
// that extractor first, and it searches the file once for each value, with
// the value in the reference's place.
type Reference struct {
	spec      Spec
	ref       reference
	asPattern bool
	around    *core.Around
}

// Needs is the extractor the pattern references.
func (e Reference) Needs() []string { return []string{e.ref.name} }

// Extract refuses: the values the pattern needs come from its rule.
func (e Reference) Extract(string) ([]check.Value, error) {
	return nil, fmt.Errorf("regex: the pattern references the values of extractor %q, which only its rule gives it", e.ref.name)
}

// ExtractWith searches the file, or each file of the set, for each distinct
// value of the extractor the pattern references, in the order that
// extractor found them. That extractor having found none, the pattern has
// nothing to search for, and allow_empty decides (check.NoValue).
func (e Reference) ExtractWith(root string, values map[string][]check.Value) ([]check.Value, error) {
	each := distinct(values[e.ref.name])
	if len(each) == 0 {
		return nil, &check.NoValue{Reason: fmt.Sprintf("extractor %q gave no value to search for", e.ref.name)}
	}
	// The time limit starts before the values are checked, since checking
	// one under values_as: pattern costs two parses of it.
	searches := core.NewEach("regex", e.ref.before, e.ref.after, e.spec.MaxBytes)
	for _, v := range each {
		if err := searches.Add(func() (string, error) { return e.insert(v) }); err != nil {
			return nil, err
		}
	}
	searches = searches.Around(e.around)
	x, err := core.BuildOneOrSet("regex", e.spec.File, e.spec.Files, e.spec.MaxBytes, func(file string, maxBytes int64, _ bool) (core.FileReader, error) {
		return searches.At("regex", file, maxBytes)
	})
	if err != nil {
		return nil, err
	}
	vs, err := x.Extract(root)
	var bad *core.PatternError
	if errors.As(err, &bad) {
		v := each[bad.Index]
		return nil, fmt.Errorf("regex: the pattern with the value %s of extractor %q, at %s: %w", core.Quote(v.Text), e.ref.name, v.At, bad.Err)
	}
	return vs, err
}

// insert is v as the pattern takes it: quoted as a literal, or for
// values_as: pattern its text, which must be an RE2 pattern by itself and
// must not end inside \Q, which would run on into the pattern around it,
// in (?-m:...), so that ^ and $ match at the start and end of the text, as
// in a JavaScript regex without the m flag, such as a Renovate
// matchString. Either way the value stays inside the group that holds it.
// An empty value would leave the pattern around it to match alone, and
// one past core.MaxReference would cost gigabytes to compile, so both
// are refused.
func (e Reference) insert(v check.Value) (string, error) {
	if v.Text == "" {
		return "", fmt.Errorf("regex: the value of extractor %q, at %s, is empty, and the pattern would match with nothing in the reference's place",
			e.ref.name, v.At)
	}
	if len(v.Text) > core.MaxReference {
		return "", fmt.Errorf("regex: the value of extractor %q, at %s, holds %d bytes, over the limit of %d for a value a pattern references",
			e.ref.name, v.At, len(v.Text), core.MaxReference)
	}
	if !e.asPattern {
		return regexp.QuoteMeta(v.Text), nil
	}
	if _, err := syntax.Parse(v.Text, syntax.Perl); err != nil {
		return "", fmt.Errorf("regex: the value %s of extractor %q, at %s, is no RE2 pattern, and values_as: %s reads each value as one: %w",
			core.Quote(v.Text), e.ref.name, v.At, AsPattern, core.ClipError(err))
	}
	if _, err := syntax.Parse(v.Text+")", syntax.Perl); err == nil {
		return "", fmt.Errorf("regex: the value %s of extractor %q, at %s, ends inside \\Q, which would run on into the pattern around it; end it with \\E",
			core.Quote(v.Text), e.ref.name, v.At)
	}
	return "(?-m:" + v.Text + ")", nil
}

// distinct is the first value of each text among values, in order.
func distinct(values []check.Value) []check.Value {
	seen := map[string]bool{}
	var out []check.Value
	for _, v := range values {
		if !seen[v.Text] {
			seen[v.Text] = true
			out = append(out, v)
		}
	}
	return out
}

// Kind is the regex kind, which a rule writes under the key regex.
var Kind = core.Kind[Spec]{Key: "regex", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is r with only what decides what it reads: its file or files as
// core.PlaceFiles writes them, no max_bytes, and values_as written out as
// literal, the default, read as left out.
func Place(r Spec) Spec {
	if r.ValuesAs == Literal {
		r.ValuesAs = ""
	}
	r.File, r.Files = core.PlaceFiles(r.File, r.Files)
	r.MaxBytes = 0
	return r
}

// Resolve is r with its max_bytes written out, its files and context
// copied, and values_as written out for a pattern that references values
// and leaves it out, for errata list.
func Resolve(r Spec) Spec {
	if r.ValuesAs == "" && strings.Contains(r.Pattern, "(?{") {
		r.ValuesAs = Literal
	}
	r.Files = slices.Clone(r.Files)
	if r.Context != nil {
		c := *r.Context
		r.Context = &c
	}
	r.MaxBytes = core.MaxBytes(r.MaxBytes)
	return r
}

// Describe is what r's extractor returns: literal values, flat, or with
// as: record a record of the pattern's named groups, and for a pattern
// that references an extractor's values, that extractor's name in Reads.
// A pattern Build refuses reads no extractor.
func Describe(r Spec) check.Description {
	d := check.Description{Label: "regex"}
	if r.As == "record" {
		d.Shape, d.Fields = check.ShapeRecord, core.RegexFields(r.Pattern)
	}
	if ref, err := parseReference(r.Pattern); err == nil && ref != nil {
		d.Reads = []string{ref.name}
	}
	return d
}
