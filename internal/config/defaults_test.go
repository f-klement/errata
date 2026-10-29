package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
)

// kube's shape: many program checks that each run one script with their own
// id, written once under defaults. The placeholders are unquoted in a flow
// list and in a block list, as a user writes them.
const withDefault = `
defaults:
  run: [python3, scripts/check-invariants.py, --only, <errata.id_underscored>, --id=<errata.id>]
checks:
  - id: no-latest-tags
    reason: r
    scope: repo
  - id: own-run
    reason: r
    scope: repo
    run:
      - sh
      - checks/<errata.id>.sh
      - <errata.id_underscored>
  - id: a-rule
    reason: r
    extract:
      p: {yaml: {file: a.yaml, path: a}}
      q: {yaml: {file: b.yaml, path: b}}
    agree: [p, q]
    scope: repo
`

// A program check with no run of its own gets the default, with the id put
// into each argument that holds a placeholder; one with a run keeps its own,
// placeholders replaced; a rule gets no run. The planted breaks: apply
// leaving the run of a check with none as it is, and substitute leaving the
// arguments as written.
func TestDefaultRunAppliesToAProgramCheckWithNone(t *testing.T) {
	cfg, err := parseRaw([]byte(withDefault))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"no-latest-tags": {"python3", "scripts/check-invariants.py", "--only", "no_latest_tags", "--id=no-latest-tags"},
		"own-run":        {"sh", "checks/own-run.sh", "own_run"},
		"a-rule":         nil,
	}
	for _, c := range cfg.Checks {
		if !slices.Equal(runOf(c), want[c.ID]) {
			t.Errorf("%s: run = %q, want %q", c.ID, runOf(c), want[c.ID])
		}
	}
	if len(cfg.Checks) != len(want) {
		t.Errorf("loaded %d checks, want %d", len(cfg.Checks), len(want))
	}
}

// Two checks share the default, and the substitution for one does not
// write into the default the other gets.
func TestDefaultRunIsCopiedForEachCheck(t *testing.T) {
	cfg, err := parseRaw([]byte("defaults: {run: [x, <errata.id>]}\nchecks:\n- {id: a, reason: r, scope: repo}\n- {id: b, reason: r, scope: repo}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := runOf(cfg.Checks[0])[1] + " " + runOf(cfg.Checks[1])[1]; got != "a b" {
		t.Errorf("runs end in %q, want a b", got)
	}
}

// A check with no run and no rule key is refused as before when the file
// sets no default run, whether or not it has a defaults key.
func TestNoRunAndNoDefaultIsRefused(t *testing.T) {
	for _, top := range []string{"", "defaults: {}\n", "defaults: null\n", "defaults:\n"} {
		_, err := parseRaw([]byte(top + "checks:\n- {id: a, reason: r, scope: repo}\n"))
		if err == nil || !strings.Contains(err.Error(), `check "a": run or extract is required`) {
			t.Errorf("%q: error = %v, want run or extract is required", top, err)
		}
	}
}

// Each refusal names its reason. The planted breaks: parseDefaults without
// its check of an empty run, and unknownPlaceholders finding nothing.
func TestDefaultsRefusals(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"empty default run": {
			"defaults: {run: []}\nchecks:\n- {id: a, reason: r, scope: repo}\n",
			"defaults: run lists no argument: write the command, or remove the key",
		},
		"null default run": {
			"defaults: {run: null}\nchecks:\n- {id: a, reason: r, scope: repo}\n",
			"defaults: run lists no argument",
		},
		// parseDefaults reads whether run is written from the document
		// tree, so a run a merge key brings in counts as written too.
		"empty default run through a merge key": {
			"defaults: {<<: {run: []}}\nchecks:\n- {id: a, reason: r, scope: repo}\n",
			"defaults: run lists no argument",
		},
		"misspelt placeholder in the default": {
			"defaults: {run: [x, <errata.ID>]}\nchecks:\n- {id: a, reason: r, scope: repo}\n",
			`defaults.run[1]: "<errata.ID>" holds a placeholder errata does not know: write <errata.id> or <errata.id_underscored>`,
		},
		"misspelt placeholder in a check's own run": {
			"checks:\n- {id: a, reason: r, scope: repo, run: [x, --only=<errata.id-underscored>]}\n",
			`checks[0]: check "a": run[1]: "--only=<errata.id-underscored>" holds a placeholder errata does not know`,
		},
		"placeholder around a placeholder": {
			"checks:\n- {id: a, reason: r, scope: repo, run: [x, <errata.<errata.id>>]}\n",
			"run[1]: \"<errata.<errata.id>>\" holds a placeholder errata does not know",
		},
		"unknown key under defaults": {
			"defaults: {timeout: 5s}\nchecks:\n- {id: a, reason: r, scope: repo}\n",
			`defaults: json: unknown field "timeout"`,
		},
		"key under defaults in another case": {
			"defaults: {Run: [x]}\nchecks:\n- {id: a, reason: r, scope: repo}\n",
			`defaults: key "Run" must be spelled "run"`,
		},
		"defaults in another case": {
			"Defaults: {run: [x]}\nchecks:\n- {id: a, reason: r, scope: repo}\n",
			`key "Defaults" must be spelled "defaults"`,
		},
		"default run that is not a list": {
			"defaults: {run: x}\nchecks:\n- {id: a, reason: r, scope: repo}\n",
			"defaults: json: cannot unmarshal",
		},
		"default argument YAML 1.1 reads as a bool": {
			"defaults: {run: [x, yes]}\nchecks:\n- {id: a, reason: r, scope: repo}\n",
			`defaults.run[1]: YAML 1.1 reads yes, unquoted, as the bool true`,
		},
		// The planted break: unknownPlaceholders without strings.ToLower.
		"placeholder in another case": {
			"defaults: {run: [x, <Errata.id>]}\nchecks:\n- {id: a, reason: r, scope: repo}\n",
			`defaults.run[1]: "<Errata.id>" holds a placeholder errata does not know`,
		},
		// With defaults in typedFile, .inf comes back as the text +Inf and
		// plainDefaults names it. Without it, the conversion fails on a float
		// JSON cannot hold, naming no key.
		"default argument YAML 1.1 reads as infinity": {
			"defaults: {run: [ls, .inf]}\nchecks:\n- {id: a, reason: r, scope: repo}\n",
			`defaults.run[1]: YAML 1.1 reads .inf, unquoted,`,
		},
		"a rule with a run beside a default": {
			"defaults: {run: [x]}\nchecks:\n- {id: a, reason: r, scope: repo, run: [z], extract: {p: {yaml: {file: a, path: a}}, q: {yaml: {file: b, path: b}}}, agree: [p, q]}\n",
			"keys of two kinds of check: run makes a program check, and extract and agree make a rule",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseRaw([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// A program check that empties its run wants anything but the shared
// command, and is refused with the one error that says so, with or without
// a default and however the empty run is written. The whole error is
// compared, so a second message after it, such as "run or extract is
// required", fails the test. The planted breaks: build without the
// empty-run check, and build validating the refused run as missing.
func TestAnEmptyOwnRunIsTheOneError(t *testing.T) {
	const want = `checks[0]: check "a": run lists no argument: write the command, or remove the key, which takes the default run if the file sets one`
	for _, raw := range []string{
		"defaults: {run: [x, <errata.id>]}\nchecks:\n- {id: a, reason: r, scope: repo, run: []}\n",
		"defaults: {run: [x]}\nchecks:\n- {id: a, reason: r, scope: repo, run: null}\n",
		"defaults: {run: [x]}\nchecks:\n- {<<: {run: []}, id: a, reason: r, scope: repo}\n",
		"checks:\n- {id: a, reason: r, scope: repo, run: []}\n",
	} {
		_, err := parseRaw([]byte(raw))
		if err == nil || err.Error() != want {
			t.Errorf("%q: error = %v, want %q alone", raw, err, want)
		}
	}
}

// A default that does not load is the one error, not one more for each
// check that has no run.
func TestABrokenDefaultIsTheOneError(t *testing.T) {
	_, err := parseRaw([]byte("defaults: {run: []}\nchecks:\n- {id: a, reason: r, scope: repo}\n- {id: b, reason: r, scope: repo}\n"))
	if err == nil || strings.Contains(err.Error(), "run or extract is required") {
		t.Errorf("error = %v, want the defaults error alone", err)
	}
}

// substitute puts in only an id check.ValidID accepts, so the id adds no
// argument, no leading hyphen and no path step: the argument count stays,
// and an id that validation refuses comes back as written. The planted
// break: substitute without its check.ValidID test, which puts ../x in.
func TestSubstituteNeverPutsInAnInvalidID(t *testing.T) {
	run := []string{"sh", "checks/<errata.id>.sh", "<errata.id_underscored>"}
	for _, id := range []string{"../x", "-x", "a b", "a/b", "a--b", "A", "", "a\nb", "a-"} {
		got := substitute(run, id)
		if !slices.Equal(got, run) {
			t.Errorf("substitute(%q) = %q, want the run as written", id, got)
		}
	}
	got := substitute(run, "a-b-1")
	if want := []string{"sh", "checks/a-b-1.sh", "a_b_1"}; !slices.Equal(got, want) {
		t.Errorf("substitute(a-b-1) = %q, want %q", got, want)
	}
	if got := substitute([]string{"<errata.id><errata.id>"}, "a"); !slices.Equal(got, []string{"aa"}) {
		t.Errorf("two placeholders in one argument = %q, want [aa]", got)
	}
}

// A check with an id validation refuses never loads, whatever the default:
// the whole file is refused.
func TestAnInvalidIDWithADefaultDoesNotLoad(t *testing.T) {
	cfg, err := parseRaw([]byte("defaults: {run: [sh, checks/<errata.id>.sh]}\nchecks:\n- {id: ../../etc/x, reason: r, scope: repo}\n"))
	if err == nil || cfg.Checks != nil || !strings.Contains(err.Error(), "must be lower-case words") {
		t.Errorf("parse = %v, %v; want the id refused", cfg.Checks, err)
	}
	if strings.Contains(err.Error(), "run or extract") {
		t.Errorf("error = %v, also says the check has no run", err)
	}
}

// A number or a bool in defaults.run loads as the text written, as it does in
// a check's own run (README, Checks). The planted break: typedFile without
// its Defaults field, which reads 5 as a JSON number the decode refuses.
func TestDefaultRunTakesNumbersAndBoolsAsWritten(t *testing.T) {
	cfg, err := parseRaw([]byte("defaults: {run: [head, -n, 5, 20.04, true]}\nchecks:\n- {id: a, reason: r, scope: repo}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"head", "-n", "5", "20.04", "true"}; !slices.Equal(runOf(cfg.Checks[0]), want) {
		t.Errorf("run = %q, want %q", runOf(cfg.Checks[0]), want)
	}
}

// A rule's run is refused whatever its value, as a rule's timeout is: an
// empty or a null run would load as a key that does nothing, and a run with
// arguments is refused once, not also as a check of both kinds. A program
// check, with no rule key, still takes its run from the default. The planted
// breaks: build without the refusal of a rule's run, and the refusal
// reading extract alone as the mark of a rule.
func TestARulesRunIsRefusedWhateverItsValue(t *testing.T) {
	const rule = "extract: {p: {yaml: {file: a, path: a}}, q: {yaml: {file: b, path: b}}}, agree: [p, q]"
	for _, run := range []string{"[]", "null", "[z]"} {
		_, err := parseRaw([]byte("defaults: {run: [x]}\nchecks:\n- {id: a, reason: r, scope: repo, run: " + run + ", " + rule + "}\n"))
		want := `checks[0]: check "a": keys of two kinds of check: run makes a program check, and extract and agree make a rule; a check is one kind: remove the keys of the kinds it is not, or write a check for each`
		if err == nil || err.Error() != want {
			t.Errorf("a rule with run: %s: error = %v, want %q alone", run, err, want)
		}
	}
	// An entry is a rule through any rule key, not extract alone.
	_, err := parseRaw([]byte("defaults: {run: [x]}\nchecks:\n- {id: a, reason: r, scope: repo, run: [], allow_empty: [p]}\n"))
	want := `checks[0]: check "a": keys of two kinds of check: run makes a program check, and allow_empty makes a rule; a check is one kind: remove the keys of the kinds it is not, or write a check for each`
	if err == nil || err.Error() != want {
		t.Errorf("a rule through allow_empty alone with run: []: error = %v, want %q alone", err, want)
	}
	cfg, err := parseRaw([]byte("defaults: {run: [x, <errata.id>]}\nchecks:\n- {id: a, reason: r, scope: repo}\n- {id: b, reason: r, scope: repo, " + rule + "}\n"))
	if err != nil || !slices.Equal(runOf(cfg.Checks[0]), []string{"x", "a"}) || runOf(cfg.Checks[1]) != nil {
		t.Errorf("a program check and a rule beside a default: %v; want the program check to run [x a] and the rule no run", err)
	}
}

// apply decides on the keys the entry wrote, not on the built check, so it
// tells a run left out, which takes the default, from a run written empty,
// which does not: both are an empty Run once built. A later default, such
// as a timeout that build fills in when the key is left out, needs the same
// distinction. The planted break: apply reading an empty Run as no key.
// Only the program check's row calls apply, so a rule and a generated check
// take no default (TestDefaultRunAppliesToAProgramCheckWithNone and
// TestGeneratedTakesNoDefaultRun).
func TestDefaultsApplyByTheKeysTheEntryWrites(t *testing.T) {
	d := defaults{Run: []string{"x", "<errata.id>"}}
	var unset check.Program
	if err := d.apply(&unset, map[string]json.RawMessage{"id": json.RawMessage(`"a"`)}, "a"); err != nil || !slices.Equal(unset.Run, []string{"x", "a"}) {
		t.Errorf("run left out: run %q, %v; want [x a]", unset.Run, err)
	}
	for _, written := range []string{"[]", "null"} {
		var empty check.Program
		if err := d.apply(&empty, map[string]json.RawMessage{"run": json.RawMessage(written)}, "a"); err != nil || empty.Run != nil {
			t.Errorf("run: %s: run %q, %v; want no run, for build to refuse", written, empty.Run, err)
		}
	}
}

// runOf is the run of c when it is a program check, and nil for a check of
// another kind.
func runOf(c check.Check) []string {
	if p := c.Program(); p != nil {
		return p.Run
	}
	return nil
}
