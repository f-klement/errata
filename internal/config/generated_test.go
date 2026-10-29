package config

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// generatedEntryYAML is one generated check under checks, with rest added
// to the check's keys.
func generatedEntryYAML(rest string) string {
	return "checks:\n- id: arch\n  reason: r\n  scope: repo\n  generated: {file: architecture.md, run: [python3, scripts/build.py, --stdout]}\n" + rest
}

// A generated check loads with its file, its generator and the defaults
// written out: max_bytes at core's default, the timeout at a program
// check's, and no run of its own. The planted breaks: build not reading the
// generated key, which loaded the check as a program check with no run, and
// the default timeout left to program checks, which gave the generator
// none.
func TestGeneratedLoads(t *testing.T) {
	cfg, err := parseRaw([]byte(generatedEntryYAML("")))
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Checks[0]
	if c.Generated == nil || c.Generated.File != "architecture.md" || !slices.Equal(c.Generated.Run, []string{"python3", "scripts/build.py", "--stdout"}) {
		t.Fatalf("generated = %+v", c.Generated)
	}
	if c.Generated.MaxBytes != core.DefaultMaxBytes || c.Timeout != DefaultTimeout || c.Run != nil || c.Rule != nil {
		t.Fatalf("max_bytes %d, timeout %s, run %q, rule %v", c.Generated.MaxBytes, c.Timeout, c.Run, c.Rule)
	}

	cfg, err = parseRaw([]byte("checks:\n- {id: arch, reason: r, scope: repo, timeout: 2m, generated: {file: a.md, run: [gen], max_bytes: 4096}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.Checks[0]; c.Generated.MaxBytes != 4096 || c.Timeout != 2*time.Minute {
		t.Fatalf("max_bytes %d, timeout %s, want 4096 and 2m", c.Generated.MaxBytes, c.Timeout)
	}
}

// defaults.run is a program check's, so a generated check does not take it,
// and its generator takes the placeholders a run takes. The planted breaks:
// apply given isRule alone, which gave the generated check the default run
// and refused it as both, and substitute left out of buildGenerated.
func TestGeneratedTakesNoDefaultRun(t *testing.T) {
	cfg, err := parseRaw([]byte("defaults: {run: [check.py, <errata.id>]}\nchecks:\n" +
		"- {id: arch-doc, reason: r, scope: repo, generated: {file: a.md, run: [gen, --for=<errata.id_underscored>]}}\n" +
		"- {id: prog, reason: r, scope: repo}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if g := cfg.Checks[0]; g.Run != nil || !slices.Equal(g.Generated.Run, []string{"gen", "--for=arch_doc"}) {
		t.Errorf("generated check: run %q, generated.run %q", g.Run, g.Generated.Run)
	}
	if p := cfg.Checks[1]; !slices.Equal(p.Run, []string{"check.py", "prog"}) {
		t.Errorf("program check beside it: run %q", p.Run)
	}
}

// required lists a generated check as any other: its id under required
// loads, and the check left off required stops the load.
func TestGeneratedIsHeldByRequired(t *testing.T) {
	if _, err := parseRaw([]byte(generatedEntryYAML("required:\n  - arch\n"))); err != nil {
		t.Fatalf("listed: %v", err)
	}
	_, err := parseRaw([]byte(generatedEntryYAML("- {id: other, reason: r, scope: repo, run: [x]}\nrequired:\n  - other\n")))
	if err == nil || !strings.Contains(err.Error(), `these checks are not under required: "arch"`) {
		t.Fatalf("not listed: error = %v", err)
	}
}

// Each refusal of a generated check, asserted by its message, and each
// refused once. The planted breaks: each refusal's if dropped in turn.
func TestGeneratedRefusals(t *testing.T) {
	cases := []struct {
		name, check, want string
	}{
		{"run beside it", `{id: a, reason: r, scope: repo, run: [x], generated: {file: f, run: [g]}}`,
			"run is set beside generated, whatever its value"},
		{"empty run beside it", `{id: a, reason: r, scope: repo, run: [], generated: {file: f, run: [g]}}`,
			"run is set beside generated, whatever its value"},
		{"extract beside it", `{id: a, reason: r, scope: repo, extract: {x: {yaml: {file: f.yaml, path: a}}}, assert: "size(x) > 0", generated: {file: f, run: [g]}}`,
			"a rule (extract) or a generated check (generated), never both"},
		{"null", `{id: a, reason: r, scope: repo, generated: null}`, "generated takes file, the committed file, and run"},
		{"no file", `{id: a, reason: r, scope: repo, generated: {run: [g]}}`, "generated.file is required"},
		{"outside the repository", `{id: a, reason: r, scope: repo, generated: {file: ../f, run: [g]}}`, "generated: file \"../f\" is outside the repository"},
		{"max_bytes over the ceiling", `{id: a, reason: r, scope: repo, generated: {file: f, run: [g], max_bytes: 67108865}}`, "generated: max_bytes is 67108865"},
		{"negative max_bytes", `{id: a, reason: r, scope: repo, generated: {file: f, run: [g], max_bytes: -1}}`, "generated: max_bytes is -1"},
		{"no generator", `{id: a, reason: r, scope: repo, generated: {file: f, run: []}}`, "generated.run lists no argument"},
		{"unknown placeholder", `{id: a, reason: r, scope: repo, generated: {file: f, run: [g, <errata.name>]}}`, "generated.run[1]: \"<errata.name>\" holds a placeholder errata does not know"},
		{"live", `{id: a, reason: r, scope: live, generated: {file: f, run: [g]}}`, `scope "live": a generated check compares`},
		{"zero timeout", `{id: a, reason: r, scope: repo, timeout: 0s, generated: {file: f, run: [g]}}`, "timeout is 0s: write a positive duration"},
		{"unknown key", `{id: a, reason: r, scope: repo, generated: {file: f, run: [g], shell: true}}`, `unknown field "shell"`},
		{"key in another case", `{id: a, reason: r, scope: repo, generated: {File: f, run: [g]}}`, `generated: key "File" must be spelled "file"`},
		{"an argument YAML reads as a bool", `{id: a, reason: r, scope: repo, generated: {file: f, run: [g, yes]}}`, "generated.run[1]: YAML 1.1 reads yes, unquoted, as the bool true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseRaw([]byte("checks:\n- " + tc.check + "\n"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
			if n := strings.Count(err.Error(), tc.want); n != 1 {
				t.Errorf("%q appears %d times in %v", tc.want, n, err)
			}
		})
	}
}

// A file left out and a max_bytes below 0 are each refused once, by their
// own message: CheckSource is not asked about an empty file, and a refused
// max_bytes is not refused again by validation, and an empty run beside
// generated is refused once. The planted breaks: the guard on an empty file
// dropped, which added "generated: file is required"; the limit kept at -1,
// which added validation's message; and emptyRun read for a generated
// check, which added "run lists no argument" and validation's refusal.
func TestGeneratedRefusesEachProblemOnce(t *testing.T) {
	_, err := parseRaw([]byte("checks:\n- {id: a, reason: r, scope: repo, generated: {run: [g]}}\n"))
	if err == nil || strings.Contains(err.Error(), "generated: file is required") {
		t.Errorf("no file: error = %v, want validation's message alone", err)
	}
	// An empty run beside generated is refused as run beside generated,
	// not as a program check's run of no argument.
	_, err = parseRaw([]byte("checks:\n- {id: a, reason: r, scope: repo, run: [], generated: {file: f, run: [g]}}\n"))
	if err == nil || strings.Contains(err.Error(), "run lists no argument") || strings.Count(err.Error(), "run is set beside generated") != 1 {
		t.Errorf("empty run beside generated: error = %v, want the one refusal", err)
	}
	_, err = parseRaw([]byte("checks:\n- {id: a, reason: r, scope: repo, generated: {file: f, run: [g], max_bytes: -1}}\n"))
	if err == nil || strings.Contains(err.Error(), "generated.max_bytes is") {
		t.Errorf("negative max_bytes: error = %v, want CheckSource's message alone", err)
	}
}

// The model the loader builds is the one the engine dispatches on.
func TestGeneratedIsAGeneratedCheck(t *testing.T) {
	cfg, err := parseRaw([]byte(generatedEntryYAML("")))
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.Checks[0]; !c.IsGenerated() || c.IsRule() || c.Scope != check.ScopeRepo {
		t.Fatalf("loaded %+v", c)
	}
}
