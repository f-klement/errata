package config

import (
	"slices"
	"strings"
	"testing"
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
// placeholders replaced; a rule gets no run. The planted breaks: runOf
// returning c.Run for a check with none, and substitute leaving the
// arguments as written.
func TestDefaultRunAppliesToAProgramCheckWithNone(t *testing.T) {
	checks, err := Parse([]byte(withDefault))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"no-latest-tags": {"python3", "scripts/check-invariants.py", "--only", "no_latest_tags", "--id=no-latest-tags"},
		"own-run":        {"sh", "checks/own-run.sh", "own_run"},
		"a-rule":         nil,
	}
	for _, c := range checks {
		if !slices.Equal(c.Run, want[c.ID]) {
			t.Errorf("%s: run = %q, want %q", c.ID, c.Run, want[c.ID])
		}
	}
	if len(checks) != len(want) {
		t.Errorf("loaded %d checks, want %d", len(checks), len(want))
	}
}

// Two checks share the default, and the substitution for one does not
// write into the default the other gets.
func TestDefaultRunIsCopiedForEachCheck(t *testing.T) {
	checks, err := Parse([]byte("defaults: {run: [x, <errata.id>]}\nchecks:\n- {id: a, reason: r, scope: repo}\n- {id: b, reason: r, scope: repo}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := checks[0].Run[1] + " " + checks[1].Run[1]; got != "a b" {
		t.Errorf("runs end in %q, want a b", got)
	}
}

// A check with no run and no rule key is refused as before when the file
// sets no default run, whether or not it has a defaults key.
func TestNoRunAndNoDefaultIsRefused(t *testing.T) {
	for _, top := range []string{"", "defaults: {}\n", "defaults: null\n", "defaults:\n"} {
		_, err := Parse([]byte(top + "checks:\n- {id: a, reason: r, scope: repo}\n"))
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
		// A check that empties its run wants anything but the shared
		// command. The planted break: build without the empty-run check.
		"empty own run beside a default": {
			"defaults: {run: [x, <errata.id>]}\nchecks:\n- {id: a, reason: r, scope: repo, run: []}\n",
			`check "a": run lists no argument: write the command, or remove the key, which takes the default run if the file sets one`,
		},
		"null own run beside a default": {
			"defaults: {run: [x]}\nchecks:\n- {id: a, reason: r, scope: repo, run: null}\n",
			`check "a": run lists no argument`,
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
			"never both",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// A default that does not load is the one error, not one more for each
// check that has no run.
func TestABrokenDefaultIsTheOneError(t *testing.T) {
	_, err := Parse([]byte("defaults: {run: []}\nchecks:\n- {id: a, reason: r, scope: repo}\n- {id: b, reason: r, scope: repo}\n"))
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
	checks, err := Parse([]byte("defaults: {run: [sh, checks/<errata.id>.sh]}\nchecks:\n- {id: ../../etc/x, reason: r, scope: repo}\n"))
	if err == nil || checks != nil || !strings.Contains(err.Error(), "must be lower-case words") {
		t.Errorf("Parse = %v, %v; want the id refused", checks, err)
	}
	if strings.Contains(err.Error(), "run or extract") {
		t.Errorf("error = %v, also says the check has no run", err)
	}
}

// A number or a bool in defaults.run loads as the text written, as it does in
// a check's own run (README, Checks). The planted break: typedFile without
// its Defaults field, which reads 5 as a JSON number the decode refuses.
func TestDefaultRunTakesNumbersAndBoolsAsWritten(t *testing.T) {
	checks, err := Parse([]byte("defaults: {run: [head, -n, 5, 20.04, true]}\nchecks:\n- {id: a, reason: r, scope: repo}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"head", "-n", "5", "20.04", "true"}; !slices.Equal(checks[0].Run, want) {
		t.Errorf("run = %q, want %q", checks[0].Run, want)
	}
}

// A rule's run: [] loads as it did before defaults: only a program check's
// empty run is refused.
func TestARulesEmptyRunStillLoads(t *testing.T) {
	_, err := Parse([]byte("defaults: {run: [x]}\nchecks:\n- {id: a, reason: r, scope: repo, run: [], extract: {p: {yaml: {file: a, path: a}}, q: {yaml: {file: b, path: b}}}, agree: [p, q]}\n"))
	if err != nil {
		t.Errorf("a rule with run: [] does not load: %v", err)
	}
}
