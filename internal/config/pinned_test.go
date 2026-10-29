package config

import (
	"slices"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract"
)

// pinnedV020 is a config in the shape a repository adopting v0.2.0 writes:
// a default run for the program checks that call one script by their id,
// program checks with a run of their own, rules, a reason shared through an
// anchor, a scope brought in by a merge key, a run written as an alias of
// defaults.run and one brought in by a merge key, and a required list in an order
// of its own. TestDefaultsAndRequiredLoadAsV020Did pins what each check runs
// and which ids are required, so a change to how defaults and required are
// decoded cannot change either.
const pinnedV020 = `
required: [suite, b-two, a-one, own-placeholder, merged, rule-agree, rule-assert, slow, aliased-run, merged-run]
defaults:
  run: &r [python3, scripts/check-invariants.py, --only, <errata.id_underscored>]
checks:
  - id: a-one
    reason: &why The reason several checks share.
    scope: repo
  - id: b-two
    reason: *why
    scope: repo
  - id: suite
    reason: r
    scope: repo
    run: [python3, scripts/tests/test_suite.py]
  - id: own-placeholder
    reason: r
    scope: repo
    run: [sh, checks/<errata.id>.sh, <errata.id_underscored>]
  - <<: {scope: repo, reason: merged in}
    id: merged
  - id: slow
    reason: r
    scope: repo
    timeout: 5m
  - id: rule-agree
    reason: r
    scope: repo
    extract:
      p: {yaml: {file: a.yaml, path: a}}
      q: {yaml: {file: b.yaml, path: b}}
    agree: [p, q]
  - id: rule-assert
    reason: r
    scope: repo
    extract:
      v: {json: {file: f.json, path: a}}
    assert: size(v) > 0
  - id: aliased-run
    reason: r
    scope: repo
    run: *r
  - <<: {run: [sh, checks/<errata.id>.sh]}
    id: merged-run
    reason: r
    scope: repo
`

// The planted break for the pin itself: apply giving every program check the
// default, own run or not.
func TestDefaultsAndRequiredLoadAsV020Did(t *testing.T) {
	cfg, err := parse([]byte(pinnedV020), extract.Build)
	if err != nil {
		t.Fatal(err)
	}
	script := func(fn string) []string {
		return []string{"python3", "scripts/check-invariants.py", "--only", fn}
	}
	want := []struct {
		id      string
		run     []string
		timeout time.Duration
		rule    bool
	}{
		{"a-one", script("a_one"), DefaultTimeout, false},
		{"b-two", script("b_two"), DefaultTimeout, false},
		{"suite", []string{"python3", "scripts/tests/test_suite.py"}, DefaultTimeout, false},
		{"own-placeholder", []string{"sh", "checks/own-placeholder.sh", "own_placeholder"}, DefaultTimeout, false},
		{"merged", script("merged"), DefaultTimeout, false},
		{"slow", script("slow"), 5 * time.Minute, false},
		{"rule-agree", nil, 0, true},
		{"rule-assert", nil, 0, true},
		{"aliased-run", script("aliased_run"), DefaultTimeout, false},
		{"merged-run", []string{"sh", "checks/merged-run.sh"}, DefaultTimeout, false},
	}
	if len(cfg.Checks) != len(want) {
		t.Fatalf("loaded %d checks, want %d", len(cfg.Checks), len(want))
	}
	for i, w := range want {
		c := cfg.Checks[i]
		if c.ID != w.id || !slices.Equal(runOf(c), w.run) || c.Timeout != w.timeout || c.IsRule() != w.rule {
			t.Errorf("checks[%d] = %s run %q timeout %s rule %v; want %s run %q timeout %s rule %v",
				i, c.ID, runOf(c), c.Timeout, c.IsRule(), w.id, w.run, w.timeout, w.rule)
		}
	}
	if wantReq := []string{"suite", "b-two", "a-one", "own-placeholder", "merged", "rule-agree", "rule-assert", "slow", "aliased-run", "merged-run"}; !slices.Equal(cfg.Required, wantReq) {
		t.Errorf("required = %q, want %q", cfg.Required, wantReq)
	}
}
