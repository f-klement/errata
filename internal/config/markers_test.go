package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

const markerRules = `
checks:
  - id: api-port
    reason: The docs name the port the config sets.
    scope: repo
    extract:
      docs: {marker: {files: ['docs/**']}}
      code: {marker: {files: [cmd/main.go]}}
    agree: [docs, code]
  - id: prog
    reason: r
    scope: repo
    run: ['true']
required: [api-port, prog]
`

// texts is the values as "file:line=text".
func texts(vs []check.Value) string {
	var out []string
	for _, v := range vs {
		out = append(out, fmt.Sprintf("%s=%s", v.At, v.Text))
	}
	return strings.Join(out, " ")
}

// A config whose rule reads markers holds the built-in check after its own
// checks, a valid rule that required need not list, and its rule's marker
// extractors read the markers that name it. A config with no marker
// extractor holds no built-in check. Planted breaks: buildRule building
// without ForRule; parse not adding the check.
func TestBuiltInCheckWithMarkers(t *testing.T) {
	cfg, err := parseRaw([]byte(markerRules))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Checks) != 3 || cfg.Checks[2].ID != UnboundMarkersID {
		t.Fatalf("checks %v, want api-port, prog and %s", ids(cfg.Checks), UnboundMarkersID)
	}
	if err := cfg.Checks[2].Validate(); err != nil {
		t.Errorf("the built-in check does not validate: %v", err)
	}
	root := extracttest.Tracked(t, map[string]string{
		"docs/a.md":   "Port <!-- errata: api-port -->8080<!-- /errata -->.\n",
		"cmd/main.go": "package main\n\n// <!-- errata: api-port -->8080<!-- /errata -->\n",
	})
	got, err := cfg.Checks[0].Rule().Extractors[1].Extractor.Extract(t.Context(), root)
	if err != nil || texts(got) != "docs/a.md:1=8080" {
		t.Errorf("docs: %s %v", texts(got), err)
	}
	cfg, err = parseRaw([]byte(goodRule))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Checks) != 2 {
		t.Errorf("a config with no marker extractor holds %v", ids(cfg.Checks))
	}
}

// ids is the ids of checks.
func ids(checks []check.Check) []string {
	var out []string
	for _, c := range checks {
		out = append(out, c.ID)
	}
	return out
}

// The built-in check's id is refused for a check of a config whose rule
// reads markers, and taken by a config whose none does. Planted break: the
// taken check removed.
func TestBuiltInIDIsRefused(t *testing.T) {
	_, err := parseRaw([]byte(strings.Replace(markerRules, "id: prog", "id: "+UnboundMarkersID, 1)))
	extracttest.WantError(t, err, `id "errata-unbound-markers" is the built-in check errata adds when a rule reads markers`)
	if _, err := parseRaw([]byte("checks:\n  - {id: " + UnboundMarkersID + ", reason: r, scope: repo, run: ['true']}\n")); err != nil {
		t.Errorf("a config with no marker extractor: %v", err)
	}
}

// The built-in check walks the files the marker rules walk, together, and
// gives each marker there that no marker extractor of the rule it names
// reads at its file: one naming no rule, one naming a check that reads no
// markers, and one outside the files its rule's extractors read. A marker
// its rule reads gives nothing, and so does one outside every rule's files
// (owner decision 2026-11-03). Each is at the line its marker opens on.
// Planted breaks: Extract reading every marker as bound; read taking a
// binding of another rule.
func TestUnboundMarkers(t *testing.T) {
	notes := `  - id: notes
    reason: r
    scope: repo
    extract:
      tagged: {marker: {files: ['notes/**', 'cmd/**']}}
    assert: size(tagged) >= 0
    allow_empty: [tagged]
`
	cfg, err := parseRaw([]byte(strings.Replace(markerRules, "required: [api-port, prog]\n", notes, 1)))
	if err != nil {
		t.Fatal(err)
	}
	root := extracttest.Tracked(t, map[string]string{
		"docs/a.md":     "Port <!-- errata: api-port -->8080<!-- /errata -->.\n\n<!-- errata: api-prot -->8080<!-- /errata -->\n",
		"cmd/main.go":   "package main\n\n// <!-- errata: api-port -->8080<!-- /errata -->\n",
		"cmd/other.go":  "package main\n\n// <!-- errata: api-port -->\n// 8080<!-- /errata -->\n// <!-- errata: notes -->x<!-- /errata -->\n",
		"notes/prog.md": "<!-- errata: prog -->x<!-- /errata -->\n",
		"misc/x.md":     "<!-- errata: nothing -->x<!-- /errata -->\n",
	})
	builtIn := cfg.Checks[len(cfg.Checks)-1].Rule()
	got, err := builtIn.Extractors[0].Extractor.Extract(t.Context(), root)
	if want := "cmd/other.go:3=api-port docs/a.md:3=api-prot notes/prog.md:1=prog"; err != nil || texts(got) != want {
		t.Errorf("unbound: %s %v, want %s", texts(got), err, want)
	}
	// A marker that cannot be read makes the check unknown, as its rule.
	broken := extracttest.Tracked(t, map[string]string{"docs/a.md": "<!-- errata: api-port -->1\n", "cmd/main.go": "package main\n"})
	_, err = builtIn.Extractors[0].Extractor.Extract(t.Context(), broken)
	extracttest.WantError(t, err, "marker: docs/a.md: line 1: the marker api-port opens and no <!-- /errata -->")
}

// The run's marker extractors and the built-in check read each file once:
// in one run (check.WithRun) a file changed after the first read gives the
// first read's markers to every later walk. Planted break: Read without
// check.Shared.
func TestOneReadPerRun(t *testing.T) {
	cfg, err := parseRaw([]byte(markerRules))
	if err != nil {
		t.Fatal(err)
	}
	root := extracttest.Tracked(t, map[string]string{"docs/a.md": "<!-- errata: api-port -->1<!-- /errata -->\n", "cmd/main.go": "package main\n"})
	ctx := check.WithRun(t.Context())
	docs := cfg.Checks[0].Rule().Extractors[1].Extractor
	if got, err := docs.Extract(ctx, root); err != nil || texts(got) != "docs/a.md:1=1" {
		t.Fatalf("docs: %s %v", texts(got), err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/a.md"), []byte("<!-- errata: other -->1<!-- /errata -->\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := cfg.Checks[2].Rule().Extractors[0].Extractor.Extract(ctx, root); err != nil || len(got) != 0 {
		t.Errorf("the built-in check in the same run: %s %v, want the first read's bound marker", texts(got), err)
	}
	if got, err := cfg.Checks[2].Rule().Extractors[0].Extractor.Extract(check.WithRun(t.Context()), root); err != nil || texts(got) != "docs/a.md:1=other" {
		t.Errorf("the built-in check in a new run: %s %v, want docs/a.md:1=other", texts(got), err)
	}
}
