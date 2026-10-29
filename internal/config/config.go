// Package config reads errata.yaml into checks. It knows the file format and
// nothing about running a check.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	yamlv2 "go.yaml.in/yaml/v2"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/rule"
)

// DefaultFile is the config file errata looks for in the repository root.
const DefaultFile = "errata.yaml"

// DefaultTimeout applies to a check that sets no timeout of its own.
const DefaultTimeout = 30 * time.Second

// file is the top level. Each check stays raw until it is decoded on its own,
// so a check that does not decode is named in the error and the others are
// still validated. Required and Defaults stay raw too, so one decode of the
// document tells a key left out (no bytes) from one written null.
type file struct {
	Checks   []json.RawMessage `json:"checks"`
	Required json.RawMessage   `json:"required"`
	Defaults json.RawMessage   `json:"defaults"`
}

// Config is what a config file loads into: its checks, in the order the file
// holds them, and the ids under required, nil when the file has no required
// key. A file whose required list and checks do not name the same ids does
// not load, so Required, when set, is the checks' ids.
type Config struct {
	Checks   []check.Check
	Required []string
}

type checkEntry struct {
	ID      string   `json:"id"`
	Reason  string   `json:"reason"`
	Scope   string   `json:"scope"`
	Run     []string `json:"run"`
	Timeout string   `json:"timeout,omitempty"`

	Extract    map[string]extract.Spec `json:"extract,omitempty"`
	Agree      []string                `json:"agree,omitempty"`
	Compare    string                  `json:"compare,omitempty"`
	Assert     string                  `json:"assert,omitempty"`
	AllowEmpty []string                `json:"allow_empty,omitempty"`
	Normalize  []string                `json:"normalize,omitempty"`
	// SkipNotLiteral is skip_not_literal: the extractors whose not literal
	// values the rule leaves out.
	SkipNotLiteral []string `json:"skip_not_literal,omitempty"`

	// Generated makes the entry a generated check.
	Generated *generatedEntry `json:"generated,omitempty"`
}

// generatedEntry is a generated check's generated key: the committed file,
// the generator's argv, and the bound on both.
type generatedEntry struct {
	File     string   `json:"file"`
	Run      []string `json:"run"`
	MaxBytes int64    `json:"max_bytes,omitempty"`
}

// ruleKeys are the keys only a rule has. A check that sets any of them is a
// rule, whatever the value, so a program check that sets one is refused as
// two kinds rather than loading with a key that does nothing.
var ruleKeys = []string{"extract", "agree", "compare", "assert", "allow_empty", "normalize", "skip_not_literal"}

// Load reads and validates the config at path. It reports every invalid check
// at once, so a user fixes the file in one pass rather than one error a run.
// Each check's location names the file relative to root, the repository
// root, when the file is inside it, and as path, marked outside, otherwise.
func Load(root, path string) (Config, error) {
	return LoadWith(root, path, extract.Build)
}

// Builder makes the extractor one entry under a rule's extract: describes.
// extract.Build is the one errata runs with.
type Builder func(extract.Spec) (check.Extractor, error)

// LoadWith is Load with the extractors made by build. The CLI's tests pass a
// builder that wraps extract.Build in a reader of not literal values, which
// no extractor errata has yet returns.
func LoadWith(root, path string, build Builder) (Config, error) {
	raw, err := readConfig(path)
	if err != nil {
		return Config{}, err
	}
	cfg, err := parse(raw, build)
	if err != nil {
		return Config{}, err
	}
	at := place(root, path)
	for i := range cfg.Checks {
		cfg.Checks[i].At.File, cfg.Checks[i].At.Outside = at.File, at.Outside
	}
	return cfg, nil
}

// MaxConfigBytes is the largest config errata reads. The largest real one
// known, a GitOps repository's, is 37 KB for 72 checks, so this is 28 times
// that. Loading costs memory in proportion to the config: the densest shape
// measured, a rule of thousands of regex extractors, allocates about 630
// bytes for each byte, so a config at the limit allocates about 650 MB.
const MaxConfigBytes = 1 << 20

// readConfig reads the config at path, refusing one over MaxConfigBytes
// before it holds more than that. It reads rather than asks the size, so a
// pipe or a device given with --config is held to the limit too.
func readConfig(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxConfigBytes {
		return nil, fmt.Errorf("the file is over %d bytes (%d MiB), the largest config errata reads", MaxConfigBytes, MaxConfigBytes>>20)
	}
	return raw, nil
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

// firstDocumentError is the error for a file whose first document the
// document tree, yaml.v3, cannot read. yaml.v3 names the line before a
// syntax error's (the checks: line for an unclosed { on the line after it),
// so the error is yaml.v2's when yaml.v2 refuses the file too, worded as
// toJSON words it: yaml.v2 parses the whole document before it decodes any
// of it, so a syntax error comes before any alias is expanded. Should
// yaml.v2 read what yaml.v3 refused, the file is refused with yaml.v3's
// error, as the alias bound counted nothing. yaml.v2 then decoded it into
// plain values, not JSON: a scalar an alias names is shared, not copied, and
// yaml.v2's own guard bounds the nodes aliases add, so the probe costs about
// the file's own size (TestFirstDocumentProbeIsBounded).
func firstDocumentError(raw []byte, v3err error) error {
	var probe any
	if err := yamlv2.UnmarshalStrict(raw, &probe); err != nil {
		return fmt.Errorf("decode: error converting YAML to JSON: %w", err)
	}
	return fmt.Errorf("decode: %w", v3err)
}

// ErrNoChecks is the load error for a file that holds no checks. A suite of
// none would run nothing and pass, so a gate on it would go green on nothing.
var ErrNoChecks = errors.New("the file holds no checks: a config with none would run nothing and pass")

// parse decodes raw config bytes into the checks and the required list.
// Decoding is strict: an unknown key is an error, because a misspelt key
// that is silently ignored turns a check off with no symptom, and so is a
// key that differs from a known one only in case. newExtractor makes each
// rule's extractors.
func parse(raw []byte, newExtractor Builder) (Config, error) {
	// The document tree keeps each alias as a reference, so its expanded
	// size is counted before toJSON expands it, whenever the first document
	// decoded, even if a later one did not: toJSON reads only the first. A
	// first document the tree cannot read is refused here, so toJSON never
	// expands a tree nobody counted.
	n, first, derr := documents(raw)
	if first == nil && derr != nil {
		return Config{}, firstDocumentError(raw, derr)
	}
	if first != nil {
		if err := checkExpansion(first); err != nil {
			return Config{}, fmt.Errorf("decode: %w", err)
		}
	}
	doc, err := toJSON(raw)
	if err != nil {
		return Config{}, fmt.Errorf("decode: %w", err)
	}
	if derr != nil {
		return Config{}, fmt.Errorf("decode: %w", derr)
	}
	if n > 1 {
		return Config{}, fmt.Errorf("the file holds %d YAML documents; errata reads one, so put every check in one document under checks", n)
	}
	if errs := exactKeys(doc, reflect.TypeFor[file](), ""); len(errs) > 0 {
		return Config{}, fmt.Errorf("decode: %w", errors.Join(errs...))
	}
	var f file
	if !bytes.Equal(doc, []byte("null")) {
		if err := decodeStrict(doc, &f); err != nil {
			return Config{}, fmt.Errorf("decode: %w", err)
		}
	}
	required, hasRequired, err := parseRequired(f.Required)
	if err != nil {
		return Config{}, fmt.Errorf("decode: %w", err)
	}
	if len(f.Checks) == 0 {
		return Config{}, ErrNoChecks
	}
	// A default that does not load is the one error: every check that relies
	// on it would otherwise add one of its own for having no run.
	def, errs := parseDefaults(f.Defaults, topLevel(first, "defaults"))
	if errs = append(errs, plainKey(first, "defaults", reflect.TypeFor[defaults]())...); len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}

	var (
		checks []check.Check
		seen   = map[string]bool{}
		at     = lines(first)
		plain  = plainStrings(first)
		ids    = make([]string, 0, len(f.Checks))
	)
	for i, raw := range f.Checks {
		id := entryID(raw)
		if i < len(plain) && plain[i].id != "" {
			id = plain[i].id // as written, as required must write it
		}
		ids = append(ids, id)
		// A string YAML 1.1 read as a bool, a number or null is refused
		// before the check is built, as a decode error is: the value the
		// check would hold is not the text its author wrote.
		if i < len(plain) && len(plain[i].errs) > 0 {
			errs = append(errs, fmt.Errorf("checks[%d]: check %q: decode: %w", i, plain[i].id, errors.Join(plain[i].errs...)))
			continue
		}
		c, err := build(raw, def, newExtractor)
		if i < len(at) {
			c.At.Line = at[i]
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
	if hasRequired {
		if perrs := plainKey(first, "required", reflect.TypeFor[[]string]()); len(perrs) > 0 {
			errs = append(errs, fmt.Errorf("decode: %w", errors.Join(perrs...)))
		} else {
			errs = append(errs, requiredErrors(required, ids)...)
		}
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return Config{Checks: checks, Required: required}, nil
}

// parseRequired decodes raw, the value of the top-level required key, and
// reports whether the file has the key at all: a null list (required: with
// nothing after it) is the key written, and requiredErrors refuses it as an
// empty one. A value that is not a list, such as one bare id, is refused
// with what to write instead of a Go type's name.
func parseRequired(raw json.RawMessage) (ids []string, present bool, err error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if b := string(bytes.TrimSpace(raw)); b != "null" && !strings.HasPrefix(b, "[") {
		return nil, true, fmt.Errorf("required takes a list of check ids, one id a line; %s is not accepted", clip(b))
	}
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, true, fmt.Errorf("required: %w", err)
	}
	return ids, true, nil
}

// requiredErrors compares required, the ids the file lists under required,
// with ids, those of its entries under checks in file order, and returns an
// error that names every id one has and the other lacks, so a deleted check,
// or a check added without its id, stops the load before anything runs. An
// empty or null list is refused, since it would turn the list off with the
// key still in the file, and so is an id listed twice. An entry with no id
// is refused on its own and is not named again here.
func requiredErrors(required, ids []string) []error {
	if len(required) == 0 {
		return []error{errors.New("required lists no check id: name the id of every check that must run, or remove the key")}
	}
	listed := make(map[string]bool, len(required))
	var twice []string
	for _, id := range required {
		if listed[id] {
			twice = append(twice, strconv.Quote(id))
		}
		listed[id] = true
	}
	has := make(map[string]bool, len(ids))
	var extra []string
	for _, id := range ids {
		has[id] = true
		if id != "" && !listed[id] {
			extra = append(extra, strconv.Quote(id))
		}
	}
	var missing []string
	for _, id := range required {
		if !has[id] {
			missing = append(missing, strconv.Quote(id))
			has[id] = true // named once, however often required lists it
		}
	}
	var errs []error
	if len(twice) > 0 {
		errs = append(errs, fmt.Errorf("required lists these ids more than once: %s", strings.Join(twice, ", ")))
	}
	if len(missing) > 0 {
		errs = append(errs, fmt.Errorf("required lists ids no check has: %s; restore each check, or remove its id from required", strings.Join(missing, ", ")))
	}
	if len(extra) > 0 {
		errs = append(errs, fmt.Errorf("these checks are not under required: %s; add each id to required, or remove the check", strings.Join(extra, ", ")))
	}
	return errs
}

// entryID is the id one entry under checks writes, or "" when it writes
// none that decodes, so the required list is compared with every entry,
// those that do not load included, and a check with an error of its own is
// not also named as missing.
func entryID(raw json.RawMessage) string {
	var named struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &named)
	return named.ID
}

// build decodes one check strictly, applies the defaults def while the
// entry's own keys are known, builds its extractors and validates it. err
// names the check, by its id when the id itself decodes.
func build(raw json.RawMessage, def defaults, newExtractor Builder) (c check.Check, err error) {
	var named struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &named) // best effort: the id names the check in an error
	refuse := func(err error) (check.Check, error) {
		return check.Check{}, fmt.Errorf("check %q: decode: %w", named.ID, err)
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

	c = check.Check{
		ID:     e.ID,
		Reason: e.Reason,
		Scope:  check.Scope(e.Scope),
	}
	row, errs := kindOf(keys)
	var kerrs []error
	if row != nil {
		c.Timeout, errs = timeout(row, e.Timeout, errs)
		c.Kind, kerrs = row.build(e, keys, def, newExtractor)
		errs = append(errs, kerrs...)
	}
	var berr error
	if len(errs) > 0 {
		berr = fmt.Errorf("check %q: %w", e.ID, errors.Join(errs...))
	}
	// Validate even after a build error, so one pass reports every problem
	// with the check, and after the defaults apply, so a program check that
	// has neither its own run nor the default is refused as one with no run.
	// On a conflict the check holds no kind and does not load. It is
	// validated as a program check with a stand-in run, which refuses
	// nothing, so its id, reason and scope are still checked in the same
	// pass and it is not also refused as one with no kind.
	// A run refused for listing no argument is validated as present in
	// the same way: the specific error stands alone, without "run or
	// extract is required" after it, which would tell an author who wrote
	// run to write run.
	v := c
	if row == nil || errors.Is(errors.Join(kerrs...), errEmptyRun) {
		v.Kind = &check.Program{Run: []string{"the stand-in run"}}
	}
	return c, errors.Join(berr, v.Validate())
}

// kindRow is one kind of check in config's kind decision: what an entry is
// when it writes one of keys, and how that kind is built from the entry.
type kindRow struct {
	// is names the kind after "makes", as the error of a conflict says it.
	is string
	// kind is a zero value of the kind the row builds, for Kinds.
	kind check.Kind
	keys []string
	// isDefault marks the row of an entry that writes the keys of no kind.
	isDefault bool
	// noTimeout refuses the timeout key on a kind that takes none, whatever
	// its value, or is nil when the kind takes one.
	noTimeout error
	// build builds the kind from the entry e, whose keys as written are
	// keys, with the defaults def and the extractors of newExtractor.
	build func(e checkEntry, keys map[string]json.RawMessage, def defaults, newExtractor Builder) (check.Kind, []error)
}

// kindRows is config's kind decision, a row for each check kind. An entry
// is the kind whose keys it writes, whatever their values, so a key of
// another kind is refused rather than loaded as one that does nothing. An
// entry that writes the keys of no kind is a program check, which takes
// the default run.
var kindRows = []kindRow{
	{is: "a program check", kind: &check.Program{}, keys: []string{"run"}, isDefault: true, build: buildProgram},
	{is: "a rule", kind: &check.Rule{}, keys: ruleKeys, noTimeout: check.ErrRuleTimeout, build: buildRuleKind},
	{is: "a generated check", kind: &check.Generated{}, keys: []string{"generated"}, build: buildGeneratedKind},
}

// Kinds returns a zero value of each check kind a config can load, in the
// order of config's kind decision. It is the one list of the kinds: engine's
// tests hold its dispatch table to it, and report's tests hold errata list
// to it.
func Kinds() []check.Kind {
	out := make([]check.Kind, len(kindRows))
	for i, row := range kindRows {
		out[i] = reflect.New(reflect.TypeOf(row.kind).Elem()).Interface().(check.Kind)
	}
	return out
}

// kindOf decides the kind of an entry from its keys: the row of the one
// kind whose keys it writes, or the program check's when it writes none.
// An entry that writes the keys of more than one kind has no row and one
// error, which names each kind's keys.
func kindOf(keys map[string]json.RawMessage) (*kindRow, []error) {
	var rows []*kindRow
	var says []string
	for i := range kindRows {
		row := &kindRows[i]
		var wrote []string
		for _, k := range row.keys {
			if _, ok := keys[k]; ok {
				wrote = append(wrote, k)
			}
		}
		if len(wrote) == 0 {
			continue
		}
		rows = append(rows, row)
		verb := " makes "
		if len(wrote) > 1 {
			verb = " make "
		}
		says = append(says, andJoin(wrote)+verb+row.is)
	}
	switch len(rows) {
	case 0:
		return defaultRow(), nil
	case 1:
		return rows[0], nil
	}
	return nil, []error{fmt.Errorf("keys of %s kinds of check: %s; a check is one kind: remove the keys of the kinds it is not, or write a check for each", countWord(len(rows)), commaAndJoin(says))}
}

// defaultRow is the row marked isDefault, the program check's.
func defaultRow() *kindRow {
	for i := range kindRows {
		if kindRows[i].isDefault {
			return &kindRows[i]
		}
	}
	panic("config: no kind row is marked isDefault")
}

// andJoin is words joined with commas and a last "and", such as "extract,
// agree and compare".
func andJoin(words []string) string {
	if len(words) == 1 {
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// commaAndJoin is clauses joined with commas and a last ", and", since a
// clause may hold an and of its own.
func commaAndJoin(clauses []string) string {
	return strings.Join(clauses[:len(clauses)-1], ", ") + ", and " + clauses[len(clauses)-1]
}

// countWord is n as a word, for the two or three kinds a conflict names.
func countWord(n int) string {
	if words := []string{2: "two", 3: "three"}; n < len(words) {
		return words[n]
	}
	return strconv.Itoa(n)
}

// timeout is the timeout of an entry of the kind row, from written, the
// timeout key's value or "" for none: DefaultTimeout when it writes none,
// and 0 for a kind that takes no timeout. A refused value appends its
// error to errs.
func timeout(row *kindRow, written string, errs []error) (time.Duration, []error) {
	var d time.Duration
	if row.noTimeout == nil {
		d = DefaultTimeout
	}
	if written == "" {
		return d, errs
	}
	parsed, err := time.ParseDuration(written)
	switch {
	case err != nil:
		return d, append(errs, fmt.Errorf("timeout: %w", err))
	case row.noTimeout != nil:
		// Refused whatever its value: a rule's timeout of 0s would load
		// as a key that does nothing, which validation, reading the
		// field, could not tell from no key.
		return d, append(errs, row.noTimeout)
	case parsed <= 0:
		// The protocol reads a timeout of zero as no limit, so 0s would
		// let a hung program hold the run for ever while reading as zero
		// time. The check keeps the default, so validation does not
		// refuse a negative one a second time.
		return d, append(errs, fmt.Errorf("timeout is %s: write a positive duration such as 10s, or leave the key out for the default of %s", written, DefaultTimeout))
	}
	return parsed, errs
}

// errEmptyRun refuses a program check's run that lists no argument. build
// reads it to validate the check as one with a run, so it is refused once.
var errEmptyRun = errors.New("run lists no argument: write the command, or remove the key, which takes the default run if the file sets one")

// buildProgram builds a program check: its own run, or the default run
// when the entry writes none. An empty run would take the default run, a
// program the entry says it does not have, so it is refused.
func buildProgram(e checkEntry, keys map[string]json.RawMessage, def defaults, _ Builder) (check.Kind, []error) {
	p := &check.Program{Run: e.Run}
	var errs []error
	if _, wrote := keys["run"]; wrote && len(e.Run) == 0 {
		errs = append(errs, errEmptyRun)
	}
	if err := def.apply(p, keys, e.ID); err != nil {
		errs = append(errs, err)
	}
	return p, errs
}

// buildGeneratedKind builds a generated check. One written null holds no
// kind, rather than a nil *check.Generated that validation would read.
func buildGeneratedKind(e checkEntry, _ map[string]json.RawMessage, _ defaults, _ Builder) (check.Kind, []error) {
	g, errs := buildGenerated(e.Generated, e.ID)
	if g == nil {
		return nil, errs
	}
	return g, errs
}

// buildRuleKind builds a rule: its keys that are written but blank, which
// would load as keys that do nothing, are refused, and buildRule builds
// the rest.
func buildRuleKind(e checkEntry, keys map[string]json.RawMessage, _ defaults, newExtractor Builder) (check.Kind, []error) {
	var errs []error
	if _, ok := keys["assert"]; ok && strings.TrimSpace(e.Assert) == "" {
		errs = append(errs, errors.New("assert is blank: write the expression or remove the key"))
	}
	if _, ok := keys["compare"]; ok && e.Compare == "" {
		errs = append(errs, errors.New("compare is blank: write text or semver, or remove the key for text"))
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
	r, rerrs := buildRule(e, newExtractor)
	return r, append(errs, rerrs...)
}

// buildGenerated builds a generated check from its generated key, g, for the
// check id. The file is held as an extractor's is: inside the repository and
// at most 4096 bytes long, with max_bytes from 1 to 64 MiB and 32 MiB when
// left out, which bounds the file and the generator's stdout alike. The
// generator's argv takes the placeholders a run takes. A key written null or
// as no mapping, a file left out and an argv of no argument do not load.
func buildGenerated(g *generatedEntry, id string) (*check.Generated, []error) {
	if g == nil {
		return nil, []error{errors.New("generated takes file, the committed file, and run, the generator's command; null is not accepted")}
	}
	var errs []error
	// An empty file is refused once, by validation; CheckSource would
	// refuse it again and stop before it read max_bytes.
	limit := core.MaxBytes(g.MaxBytes)
	if g.File != "" {
		if _, err := core.CheckSource("generated", g.File, g.MaxBytes); err != nil {
			errs = append(errs, err)
			// Refused here, so validation does not refuse a max_bytes
			// below 1 a second time.
			limit = core.DefaultMaxBytes
		}
	}
	errs = append(errs, unknownPlaceholders("generated.run", g.Run)...)
	return &check.Generated{File: g.File, Run: substitute(g.Run, id), MaxBytes: limit}, errs
}

// buildRule builds a rule's extractors in the order of their names, so the
// errors and every later message list them the same way on every run, and
// compiles its assert, so a type error is a load error. An extractor whose
// values another reads need not be named in the assert.
func buildRule(e checkEntry, newExtractor Builder) (*check.Rule, []error) {
	r := &check.Rule{
		Agree:          e.Agree,
		Compare:        check.Compare(e.Compare),
		Assert:         e.Assert,
		AllowEmpty:     e.AllowEmpty,
		SkipNotLiteral: e.SkipNotLiteral,
	}
	if r.Compare == "" && len(e.Agree) > 0 {
		r.Compare = check.CompareText
	}
	for _, n := range e.Normalize {
		r.Normalize = append(r.Normalize, check.Normalization(n))
	}
	var errs []error
	var read []string // the extractors another extractor reads the values of
	names := slices.Sorted(maps.Keys(e.Extract))
	var whole []string // the extractors assert reads as lists of maps
	errs = append(errs, samePlaces(e.Extract, names)...)
	for _, name := range names {
		x, err := newExtractor(e.Extract[name])
		if d, ok := x.(check.Dependent); ok {
			read = append(read, d.Needs()...)
		}
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
		r.Extractors = append(r.Extractors, check.NamedExtractor{
			Name: name, Kind: e.Extract[name].Kind(), Extractor: x, Spec: e.Extract[name].Resolved(),
			Maps: e.Extract[name].Maps(), Fields: e.Extract[name].Fields(),
		})
		if e.Extract[name].Maps() {
			whole = append(whole, name)
		}
	}
	// A rule with agree beside assert is refused by validation, so its
	// expression is not compiled into a second error.
	if strings.TrimSpace(e.Assert) != "" && len(e.Agree) == 0 && len(names) > 0 {
		a, err := rule.Compile(e.Assert, names, rule.Lists{Maps: whole, Read: read})
		if err != nil {
			errs = append(errs, err)
		}
		r.Compiled = a
	}
	return r, errs
}

// maxSamePlace is the most extractors samePlaces names. A rule of thousands
// of extractors over a few places would otherwise make an error for each.
const maxSamePlace = 8

// samePlaces refuses each extractor, in the order of names, that reads the
// place an earlier one reads, naming the first. Two names over one place
// return the same values at the same locations, so agree would be true
// without comparing two places. Place cleans each file, so f and ./f are one
// place. Each place is looked up once by its JSON, so a rule of n extractors
// costs n lookups, not a comparison of every pair.
func samePlaces(specs map[string]extract.Spec, names []string) []error {
	var errs []error
	first := make(map[string]string, len(names))
	more := 0
	for _, name := range names {
		key, err := json.Marshal(specs[name].Place())
		if err != nil {
			// A spec is strings, numbers and maps of strings, which always
			// marshal; one that did not would name no place to compare.
			continue
		}
		a, seen := first[string(key)]
		switch {
		case !seen:
			first[string(key)] = name
		case len(errs) < maxSamePlace:
			errs = append(errs, fmt.Errorf("extractors %q and %q read the same place, so comparing them compares a value with itself", a, name))
		default:
			more++
		}
	}
	if more > 0 {
		errs = append(errs, fmt.Errorf("and %d more extractors read a place an extractor above reads", more))
	}
	return errs
}

// maxQuotedKey is the most bytes of a key's value a load error quotes.
const maxQuotedKey = 64

// clip is s, a value as JSON, cut at maxQuotedKey bytes with "..." when it
// was cut. A JSON string is cut as text and quoted again, so the quote closes
// and no escape is cut in two; any other value is cut on a rune boundary. A
// string whose text fits is quoted whole, with no "...", although its quotes
// take its JSON over the limit.
func clip(s string) string {
	if len(s) <= maxQuotedKey {
		return s
	}
	var text string
	if json.Unmarshal([]byte(s), &text) == nil {
		cut, more := cutBytes(text)
		if !more {
			return strconv.Quote(text)
		}
		return strconv.Quote(cut) + "..."
	}
	cut, _ := cutBytes(s)
	return cut + "..."
}

// cutBytes is s cut at maxQuotedKey bytes, on a rune boundary, and whether
// that cut anything.
func cutBytes(s string) (string, bool) {
	if len(s) <= maxQuotedKey {
		return s, false
	}
	end := maxQuotedKey
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end], true
}
