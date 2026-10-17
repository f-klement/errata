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
	"reflect"
	"slices"
	"strings"
	"time"

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
}

// ruleKeys are the keys only a rule has. A check that sets any of them is a
// rule, whatever the value, so a program check that sets one is refused as
// both rather than loading with a key that does nothing.
var ruleKeys = []string{"extract", "agree", "assert", "allow_empty", "normalize"}

// Load reads and validates the config at path. It reports every invalid check
// at once, so a user fixes the file in one pass rather than one error a run.
func Load(path string) ([]check.Check, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// ErrNoChecks is the load error for a file that holds no checks. A suite of
// none would run nothing and pass, so a gate on it would go green on nothing.
var ErrNoChecks = errors.New("the file holds no checks: a config with none would run nothing and pass")

// Parse decodes raw config bytes. Decoding is strict: an unknown key is an
// error, because a misspelt key that is silently ignored turns a check off
// with no symptom, and so is a key that differs from a known one only in case.
func Parse(raw []byte) ([]check.Check, error) {
	if n := documents(raw); n > 1 {
		return nil, fmt.Errorf("the file holds %d YAML documents; errata reads one, so put every check in one document under checks", n)
	}
	doc, err := toJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
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
	)
	for i, raw := range f.Checks {
		c, decoded, err := build(raw)
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
func build(raw json.RawMessage) (c check.Check, decoded bool, err error) {
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
	if v, ok := keys["allow_empty"]; ok {
		if b := string(bytes.TrimSpace(v)); b == "true" || b == "false" {
			return refuse(fmt.Errorf("allow_empty takes a list of extractor names, such as allow_empty: [name]; %s is not accepted", b))
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
	if isRule {
		r, rerrs := buildRule(e)
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
func buildRule(e checkEntry) (*check.Rule, []error) {
	r := &check.Rule{
		Agree:      e.Agree,
		Assert:     e.Assert,
		AllowEmpty: e.AllowEmpty,
	}
	for _, n := range e.Normalize {
		r.Normalize = append(r.Normalize, check.Normalization(n))
	}
	var errs []error
	names := slices.Sorted(maps.Keys(e.Extract))
	for i, a := range names {
		for _, b := range names[i+1:] {
			// Two names over one spec return the same values at the same
			// locations, so agree would be true without comparing two places.
			if reflect.DeepEqual(e.Extract[a], e.Extract[b]) {
				errs = append(errs, fmt.Errorf("extractors %q and %q read the same place, so comparing them compares a value with itself", a, b))
			}
		}
	}
	for _, name := range names {
		x, err := extract.Build(e.Extract[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("extractor %q: %w", name, err))
		}
		// Declared even when it did not build, so validation does not also
		// report agree naming an extractor that extract does not declare.
		r.Extractors = append(r.Extractors, check.NamedExtractor{Name: name, Extractor: x})
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
