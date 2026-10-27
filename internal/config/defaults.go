package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/f-klement/errata/internal/check"
)

// The placeholders a run may hold. errata replaces each with the check's id,
// inside one argument at a time, so the argument count never changes. They
// are spelt with angle brackets because braces start a YAML flow mapping and
// $ and % are a shell's, and with errata. because no command's own argument
// starts that way.
const (
	placeholderID          = "<errata.id>"
	placeholderUnderscored = "<errata.id_underscored>"
	placeholderStart       = "<errata."
)

// defaults is the top level defaults key: what a check gets for a key it
// does not set. Run is the run of every program check that has none.
type defaults struct {
	Run []string `json:"run"`
}

// parseDefaults decodes raw, the value of the top level defaults key, as the
// checks are decoded: strictly, with keys matched in case, and a run that
// lists no argument or holds a placeholder errata does not know is refused.
// No key, or defaults: null, sets no default.
func parseDefaults(raw json.RawMessage) (defaults, []error) {
	var d defaults
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return d, nil
	}
	if errs := exactKeys(raw, reflect.TypeFor[defaults](), "defaults"); len(errs) > 0 {
		return d, errs
	}
	if err := decodeStrict(raw, &d); err != nil {
		return d, []error{fmt.Errorf("defaults: %w", err)}
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys) // decodeStrict above read it as an object
	if _, ok := keys["run"]; ok && len(d.Run) == 0 {
		return d, []error{errors.New("defaults: run lists no argument: write the command, or remove the key")}
	}
	return d, unknownPlaceholders("defaults.run", d.Run)
}

// runOf is the run c has once d applies and the placeholders are replaced: its
// own run, or the default when it is a program check with none. A rule keeps
// what it has, so validation still refuses a rule that sets run.
func (d defaults) runOf(c check.Check) ([]string, error) {
	if c.IsRule() {
		return c.Run, nil
	}
	if len(c.Run) == 0 {
		return substitute(d.Run, c.ID), nil
	}
	if errs := unknownPlaceholders("run", c.Run); len(errs) > 0 {
		return c.Run, fmt.Errorf("check %q: %w", c.ID, errors.Join(errs...))
	}
	return substitute(c.Run, c.ID), nil
}

// substitute is run with each placeholder replaced by id, argument by
// argument, in a new slice. Only an id check.ValidID accepts is put in: one
// that is not comes back with the placeholders as written, and validation
// refuses the id, so no text the id form forbids, such as ../ or a leading
// hyphen, reaches a command.
func substitute(run []string, id string) []string {
	if len(run) == 0 {
		return nil
	}
	out := make([]string, len(run))
	if !check.ValidID(id) {
		copy(out, run)
		return out
	}
	r := strings.NewReplacer(
		placeholderID, id,
		placeholderUnderscored, strings.ReplaceAll(id, "-", "_"),
	)
	for i, arg := range run {
		out[i] = r.Replace(arg)
	}
	return out
}

// known removes the placeholders errata replaces, so what is left of an
// argument shows whether it holds one errata does not.
var known = strings.NewReplacer(placeholderID, "", placeholderUnderscored, "")

// unknownPlaceholders returns an error for each argument of run, at key, that
// holds <errata., in any case, other than as a placeholder errata knows, such
// as a misspelt one, which would otherwise reach the command as written.
func unknownPlaceholders(key string, run []string) []error {
	var errs []error
	for i, arg := range run {
		// In any case: <Errata.id> is a misspelling, not text for the program.
		if strings.Contains(strings.ToLower(known.Replace(arg)), placeholderStart) {
			errs = append(errs, fmt.Errorf("%s[%d]: %q holds a placeholder errata does not know: write %s or %s", key, i, arg, placeholderID, placeholderUnderscored))
		}
	}
	return errs
}

// plainDefaults is plainStrings for the defaults key of doc, the first
// document: an error for each string under it that YAML 1.1 loads as other
// text than was written.
func plainDefaults(doc *yaml.Node) []error {
	if doc == nil || len(doc.Content) == 0 {
		return nil
	}
	for _, p := range pairs(doc.Content[0], nil) {
		if p.key.Value == "defaults" {
			w := &stringWalk{}
			w.walk(p.value, reflect.TypeFor[defaults](), "defaults")
			return w.errs
		}
	}
	return nil
}
