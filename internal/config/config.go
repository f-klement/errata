// Package config reads errata.yaml into checks. It knows the file format and
// nothing about running a check.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/rule"
)

// DefaultFile is the config file errata looks for in the repository root.
const DefaultFile = "errata.yaml"

// DefaultTimeout applies to a check that sets no timeout of its own.
const DefaultTimeout = 30 * time.Second

// file is the top level. Each check stays raw until it is decoded on its own,
// so a check that does not decode is named in the error and the others are
// still validated.
type file struct {
	Checks []json.RawMessage `json:"checks"`
}

type checkEntry struct {
	ID      string   `json:"id"`
	Reason  string   `json:"reason"`
	Scope   string   `json:"scope"`
	Run     []string `json:"run"`
	Timeout string   `json:"timeout,omitempty"`

	Extract    map[string]extract.Spec `json:"extract,omitempty"`
	Agree      []string                `json:"agree,omitempty"`
	Assert     string                  `json:"assert,omitempty"`
	AllowEmpty []string                `json:"allow_empty,omitempty"`
	Normalize  []string                `json:"normalize,omitempty"`
	// SkipNotLiteral is skip_not_literal: the extractors whose not literal
	// values the rule leaves out.
	SkipNotLiteral []string `json:"skip_not_literal,omitempty"`
}

// ruleKeys are the keys only a rule has. A check that sets any of them is a
// rule, whatever the value, so a program check that sets one is refused as
// both rather than loading with a key that does nothing.
var ruleKeys = []string{"extract", "agree", "assert", "allow_empty", "normalize", "skip_not_literal"}

// Load reads and validates the config at path. It reports every invalid check
// at once, so a user fixes the file in one pass rather than one error a run.
// Each check's location names the file relative to root, the repository
// root, when the file is inside it, and as path, marked outside, otherwise.
func Load(root, path string) ([]check.Check, error) {
	return LoadWith(root, path, extract.Build)
}

// Builder makes the extractor one entry under a rule's extract: describes.
// extract.Build is the one errata runs with.
type Builder func(extract.Spec) (check.Extractor, error)

// LoadWith is Load with the extractors made by build. The CLI's tests pass a
// builder that wraps extract.Build in a reader of not literal values, which
// no extractor errata has yet returns.
func LoadWith(root, path string, build Builder) ([]check.Check, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	checks, err := ParseWith(raw, build)
	if err != nil {
		return nil, err
	}
	at := place(root, path)
	for i := range checks {
		checks[i].At.File, checks[i].At.Outside = at.File, at.Outside
	}
	return checks, nil
}

// place is where the config at path is: its path relative to root, with
// slashes, when it is inside root, and otherwise path as given, marked
// outside. The mark matters for a relative path such as ci.yaml, which names
// a file of the working directory and would read as one of the repository.
func place(root, path string) check.Location {
	absRoot, rerr := filepath.Abs(root)
	absPath, perr := filepath.Abs(path)
	if rerr != nil || perr != nil {
		return check.Location{File: path, Outside: true}
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || !filepath.IsLocal(rel) {
		return check.Location{File: path, Outside: true}
	}
	return check.Location{File: filepath.ToSlash(rel)}
}

// ErrNoChecks is the load error for a file that holds no checks. A suite of
// none would run nothing and pass, so a gate on it would go green on nothing.
var ErrNoChecks = errors.New("the file holds no checks: a config with none would run nothing and pass")

// Parse decodes raw config bytes. Decoding is strict: an unknown key is an
// error, because a misspelt key that is silently ignored turns a check off
// with no symptom, and so is a key that differs from a known one only in case.
func Parse(raw []byte) ([]check.Check, error) {
	return ParseWith(raw, extract.Build)
}

// ParseWith is Parse with the extractors made by build.
func ParseWith(raw []byte, newExtractor Builder) ([]check.Check, error) {
	doc, err := toJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	n, first, err := documents(raw)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if n > 1 {
		return nil, fmt.Errorf("the file holds %d YAML documents; errata reads one, so put every check in one document under checks", n)
	}
	if errs := exactKeys(doc, reflect.TypeFor[file](), ""); len(errs) > 0 {
		return nil, fmt.Errorf("decode: %w", errors.Join(errs...))
	}
	var f file
	if !bytes.Equal(doc, []byte("null")) {
		if err := decodeStrict(doc, &f); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
	}
	if len(f.Checks) == 0 {
		return nil, ErrNoChecks
	}

	var (
		checks []check.Check
		errs   []error
		seen   = map[string]bool{}
		at     = lines(first)
		plain  = plainStrings(first)
	)
	for i, raw := range f.Checks {
		// A string YAML 1.1 read as a bool, a number or null is refused
		// before the check is built, as a decode error is: the value the
		// check would hold is not the text its author wrote.
		if i < len(plain) && len(plain[i].errs) > 0 {
			errs = append(errs, fmt.Errorf("checks[%d]: check %q: decode: %w", i, plain[i].id, errors.Join(plain[i].errs...)))
			continue
		}
		c, decoded, err := build(raw, newExtractor)
		if i < len(at) {
			c.At.Line = at[i]
		}
		if decoded {
			// Validate even after a build error, so one pass reports every
			// problem with the check.
			err = errors.Join(err, c.Validate())
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("checks[%d]: %w", i, err))
			continue
		}
		if seen[c.ID] {
			errs = append(errs, fmt.Errorf("checks[%d]: duplicate id %q", i, c.ID))
			continue
		}
		seen[c.ID] = true
		checks = append(checks, c)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return checks, nil
}

// build decodes one check strictly and builds its extractors. decoded reports
// whether the check decoded, so it is worth validating; err names the check,
// by its id when the id itself decodes.
func build(raw json.RawMessage, newExtractor Builder) (c check.Check, decoded bool, err error) {
	var named struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &named) // best effort: the id names the check in an error
	refuse := func(err error) (check.Check, bool, error) {
		return check.Check{}, false, fmt.Errorf("check %q: decode: %w", named.ID, err)
	}

	if errs := exactKeys(raw, reflect.TypeFor[checkEntry](), ""); len(errs) > 0 {
		return refuse(errors.Join(errs...))
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys) // a check that is not an object fails decodeStrict below
	// Each of these takes names, and a bool (read as a switch) or one bare
	// name are the likely mistakes, so the error says what to write instead
	// of naming a Go type. null is a list with no name, refused below.
	for _, key := range []string{"allow_empty", "skip_not_literal"} {
		if v, ok := keys[key]; ok {
			if b := string(bytes.TrimSpace(v)); b != "null" && !strings.HasPrefix(b, "[") {
				return refuse(fmt.Errorf("%s takes a list of extractor names, such as %s: [name]; %s is not accepted", key, key, clip(b)))
			}
		}
	}
	var e checkEntry
	if err := decodeStrict(raw, &e); err != nil {
		return refuse(err)
	}

	isRule := slices.ContainsFunc(ruleKeys, func(k string) bool { _, ok := keys[k]; return ok })
	c = check.Check{
		ID:     e.ID,
		Reason: e.Reason,
		Scope:  check.Scope(e.Scope),
		Run:    e.Run,
	}
	var errs []error
	if !isRule {
		c.Timeout = DefaultTimeout
	}
	if e.Timeout != "" {
		d, perr := time.ParseDuration(e.Timeout)
		if perr != nil {
			errs = append(errs, fmt.Errorf("timeout: %w", perr))
		}
		c.Timeout = d
	}
	if _, ok := keys["assert"]; ok && strings.TrimSpace(e.Assert) == "" {
		errs = append(errs, errors.New("assert is blank: write the expression or remove the key"))
	}
	if _, ok := keys["normalize"]; ok && len(e.Normalize) == 0 {
		errs = append(errs, errors.New("normalize lists no option: name trim, lower or both, or remove the key"))
	}
	if _, ok := keys["allow_empty"]; ok && len(e.AllowEmpty) == 0 {
		errs = append(errs, errors.New("allow_empty lists no extractor: name the extractors that may find no value, or remove the key"))
	}
	if _, ok := keys["skip_not_literal"]; ok && len(e.SkipNotLiteral) == 0 {
		errs = append(errs, errors.New("skip_not_literal lists no extractor: name the extractors whose not literal values to leave out, or remove the key"))
	}
	if isRule {
		r, rerrs := buildRule(e, newExtractor)
		c.Rule = r
		errs = append(errs, rerrs...)
	}
	if len(errs) > 0 {
		return c, true, fmt.Errorf("check %q: %w", e.ID, errors.Join(errs...))
	}
	return c, true, nil
}

// buildRule builds a rule's extractors in the order of their names, so the
// errors and every later message list them the same way on every run, and
// compiles its assert, so a type error is a load error.
func buildRule(e checkEntry, newExtractor Builder) (*check.Rule, []error) {
	r := &check.Rule{
		Agree:          e.Agree,
		Assert:         e.Assert,
		AllowEmpty:     e.AllowEmpty,
		SkipNotLiteral: e.SkipNotLiteral,
	}
	for _, n := range e.Normalize {
		r.Normalize = append(r.Normalize, check.Normalization(n))
	}
	var errs []error
	names := slices.Sorted(maps.Keys(e.Extract))
	for i, a := range names {
		for _, b := range names[i+1:] {
			// Two names over one place return the same values at the same
			// locations, so agree would be true without comparing two places.
			// Place cleans each file, so f and ./f are one place.
			if reflect.DeepEqual(e.Extract[a].Place(), e.Extract[b].Place()) {
				errs = append(errs, fmt.Errorf("extractors %q and %q read the same place, so comparing them compares a value with itself", a, b))
			}
		}
	}
	for _, name := range names {
		x, err := newExtractor(e.Extract[name])
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("extractor %q: %w", name, err))
		case x == nil:
			// Fail closed: a nil extractor would load and be called at run
			// time, and validation reads nil as one that did not build.
			errs = append(errs, fmt.Errorf("extractor %q: built nothing, and gave no error", name))
		}
		// Declared even when it did not build, so validation does not also
		// report agree naming an extractor that extract does not declare.
		r.Extractors = append(r.Extractors, check.NamedExtractor{Name: name, Kind: e.Extract[name].Kind(), Extractor: x})
	}
	// A rule with agree beside assert is refused by validation, so its
	// expression is not compiled into a second error.
	if strings.TrimSpace(e.Assert) != "" && len(e.Agree) == 0 && len(names) > 0 {
		a, err := rule.Compile(e.Assert, names)
		if err != nil {
			errs = append(errs, err)
		}
		r.Compiled = a
	}
	return r, errs
}

// maxQuotedKey is the most bytes of a key's value a load error quotes.
const maxQuotedKey = 64

// clip is s, a value as JSON, cut at maxQuotedKey bytes with "..." when it
// was cut. A JSON string is cut as text and quoted again, so the quote closes
// and no escape is cut in two; any other value is cut on a rune boundary.
func clip(s string) string {
	if len(s) <= maxQuotedKey {
		return s
	}
	var text string
	if json.Unmarshal([]byte(s), &text) == nil {
		return strconv.Quote(cutBytes(text)) + "..."
	}
	return cutBytes(s) + "..."
}

// cutBytes is s cut at maxQuotedKey bytes, on a rune boundary.
func cutBytes(s string) string {
	if len(s) <= maxQuotedKey {
		return s
	}
	end := maxQuotedKey
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
