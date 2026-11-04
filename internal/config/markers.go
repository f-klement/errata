package config

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/marker"
	"github.com/f-klement/errata/internal/rule"
)

// UnboundMarkersID is the id of the built-in check that a config holds
// whenever a rule reads markers: it is false for each marker that no rule's
// marker extractor reads, since a marker nothing reads tags a claim nothing
// checks (ADR 0017).
const UnboundMarkersID = "errata-unbound-markers"

// unboundReason is the built-in check's reason.
const unboundReason = "Each marker names the rule that reads its value. A marker in the files the marker rules walk that no marker extractor of the rule it names reads, at the file it is in, tags a claim nothing checks, so the watch it stands for is off without a word."

// unboundName is the name of the built-in check's one extractor, whose
// values are the markers no rule reads.
const unboundName = "unbound"

// binding is a rule that reads markers, and the walk of one of its marker
// extractors.
type binding struct {
	rule string
	walk marker.Walk
	spec extract.Spec
}

// unboundMarkers is the built-in check of checks, and whether they need
// one: whether a rule among them reads markers. It walks the files the
// rules' marker extractors walk, together (owner decision 2026-11-03), so a
// marker outside all of them is not its to report. A check of checks with
// its id is refused.
func unboundMarkers(checks []check.Check) (check.Check, bool, error) {
	var bindings []binding
	var limit int64
	taken := false
	for _, c := range checks {
		taken = taken || c.ID == UnboundMarkersID
		r := c.Rule()
		if r == nil {
			continue
		}
		for _, x := range r.Extractors {
			spec, _ := x.Spec.(extract.Spec)
			m, ok := extract.SpecOf(spec, marker.Kind)
			if !ok {
				continue
			}
			w, err := marker.NewWalk(m.Files, m.MaxBytes)
			if err != nil {
				// The extractor did not build either, and its rule says so.
				continue
			}
			bindings = append(bindings, binding{rule: c.ID, walk: w, spec: spec})
			limit = max(limit, w.Limit())
		}
	}
	if len(bindings) == 0 {
		return check.Check{}, false, nil
	}
	if taken {
		return check.Check{}, false, fmt.Errorf("id %q is the built-in check errata adds when a rule reads markers; give the check another id", UnboundMarkersID)
	}
	var entries []string // what the rules' files: name, for errata list
	for _, b := range bindings {
		m, _ := extract.SpecOf(b.spec, marker.Kind)
		for _, f := range marker.Resolve(m).Files {
			if !slices.Contains(entries, f) {
				entries = append(entries, f)
			}
		}
	}
	shown := marker.Spec{Files: entries, MaxBytes: limit}
	x := check.NamedExtractor{
		Name:        unboundName,
		Extractor:   unbound{bindings: bindings},
		Spec:        extract.Of(marker.Kind, shown).Resolved(),
		Description: marker.Describe(shown),
	}
	r := &check.Rule{
		Extractors: []check.NamedExtractor{x},
		Assert:     "size(" + unboundName + ") == 0",
		AllowEmpty: []string{unboundName},
	}
	var err error
	if r.Compiled, err = rule.Compile(r.Assert, r.Extractors); err != nil {
		return check.Check{}, false, err
	}
	return check.Check{ID: UnboundMarkersID, Reason: unboundReason, Scope: check.ScopeRepo, Kind: r}, true, nil
}

// unbound is the built-in check's extractor: a value for each marker in the
// files its bindings walk that no binding of the rule it names reads, the
// id it names at the place it opens.
type unbound struct {
	bindings []binding
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (u unbound) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return u.Extract(ctx, root)
}

// Extract lists the files of every binding, reads the files of them all
// once each, in git's order, each parsed up to the largest max_bytes of the
// bindings that list it, and gives each marker whose file no binding of the
// rule it names lists. The run's marker extractors read the same files from
// the same reads (marker.Read).
func (u unbound) Extract(ctx context.Context, root string) ([]check.Value, error) {
	limit := map[string]int64{}                       // each file's, the most of the bindings that list it
	reads := make([]map[string]bool, len(u.bindings)) // each binding's files
	for i, b := range u.bindings {
		names, err := b.walk.Files(ctx, root)
		if err != nil {
			return nil, fmt.Errorf("listing the files the markers of %q are read in: %w", b.rule, err)
		}
		reads[i] = map[string]bool{}
		for _, n := range names {
			reads[i][n] = true
			limit[n] = max(limit[n], b.walk.Limit())
		}
	}
	if len(limit) > marker.MaxFiles {
		return nil, fmt.Errorf("%s: the rules that read markers name %d files together, more than %d, the limit of one walk; narrow their files", marker.Kind.Key, len(limit), marker.MaxFiles)
	}
	names := make([]marker.Name, 0, len(limit))
	for _, f := range slices.Sorted(maps.Keys(limit)) { // git's order
		names = append(names, marker.Name{File: f, Limit: limit[f]})
	}
	found, err := marker.Read(ctx, root, names, marker.Bounds{Bytes: marker.MaxWalkBytes, Count: marker.MaxMarkers})
	if err != nil {
		return nil, err
	}
	out := []check.Value{}
	for _, f := range found {
		if !u.read(reads, f) {
			out = append(out, check.Value{Text: f.Rule, At: f.Open})
		}
	}
	return out, nil
}

// read reports whether a binding of the rule f names lists f's file.
func (u unbound) read(reads []map[string]bool, f marker.Found) bool {
	for i, b := range u.bindings {
		if b.rule == f.Rule && reads[i][f.Value.At.File] {
			return true
		}
	}
	return false
}
