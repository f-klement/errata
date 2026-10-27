package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract"
)

// program is one program check of id, as a config writes it under checks.
func program(id string) string {
	return "  - id: " + id + "\n    reason: r\n    scope: repo\n    run: [x]\n"
}

// A config whose required list holds exactly its checks' ids loads, in any
// order, and returns the list as written. The planted break: parse
// returning a Config without Required.
func TestRequiredMatchingTheChecksLoads(t *testing.T) {
	cfg, err := parse([]byte("required: [b, a]\nchecks:\n"+program("a")+program("b")), extract.Build)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Required, []string{"b", "a"}) || len(cfg.Checks) != 2 {
		t.Errorf("loaded required %q and %d checks, want [b a] and 2", cfg.Required, len(cfg.Checks))
	}
	cfg, err = parse([]byte("checks:\n"+program("a")), extract.Build)
	if err != nil || cfg.Required != nil {
		t.Errorf("a config without required: %v, required %q, want it to load with none", err, cfg.Required)
	}
}

// A required id no check has, and a check whose id required does not list,
// each stop the load, and the error names every such id, not the first. The
// planted breaks: requiredErrors returning nil when the ids differ, and
// naming only the first missing id.
func TestRequiredRefusesAMissingOrAnExtraCheck(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		want         []string
	}{
		{
			name:   "checks deleted",
			config: "required: [a, gone, b, lost]\nchecks:\n" + program("a") + program("b"),
			want:   []string{`required lists ids no check has: "gone", "lost"; restore each check, or remove its id from required`},
		},
		{
			name:   "checks added",
			config: "required: [a]\nchecks:\n" + program("a") + program("new") + program("newer"),
			want:   []string{`these checks are not under required: "new", "newer"; add each id to required, or remove the check`},
		},
		{
			name:   "both",
			config: "required: [a, gone]\nchecks:\n" + program("a") + program("new"),
			want: []string{
				`required lists ids no check has: "gone"`,
				`these checks are not under required: "new"`,
			},
		},
		{
			name:   "listed twice",
			config: "required: [a, a, gone, gone]\nchecks:\n" + program("a"),
			want: []string{
				`required lists these ids more than once: "a", "gone"`,
				`required lists ids no check has: "gone";`,
			},
		},
		{
			name:   "empty",
			config: "required: []\nchecks:\n" + program("a"),
			want:   []string{"required lists no check id: name the id of every check that must run, or remove the key"},
		},
		{
			name:   "null",
			config: "required:\nchecks:\n" + program("a"),
			want:   []string{"required lists no check id"},
		},
		{
			name:   "one bare id",
			config: "required: a\nchecks:\n" + program("a"),
			want:   []string{`required takes a list of check ids, one id a line; "a" is not accepted`},
		},
		{
			name:   "an id YAML 1.1 reads as a bool",
			config: "required: [y]\nchecks:\n" + program(`"y"`),
			want:   []string{`required[0]: YAML 1.1 reads y, unquoted, as the bool true, so it would load as "true"; write it quoted: "y"`},
		},
		{
			name:   "spelt in another case",
			config: "Required: [a]\nchecks:\n" + program("a"),
			want:   []string{`key "Required" must be spelled "required"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parse([]byte(tc.config), extract.Build)
			if err == nil {
				t.Fatalf("loaded %d checks, want a refusal", len(cfg.Checks))
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not say %q", err, w)
				}
			}
		})
	}
}

// A check with an error of its own is refused for that error, and is not
// also named as missing from the checks, since its id is under required.
// The required list is compared even then, so one pass names a check added
// without its id too. The planted break: comparing required with the ids of
// the checks that loaded.
func TestRequiredComparesEveryEntry(t *testing.T) {
	bad := "  - id: a\n    reason: r\n    scope: nowhere\n    run: [x]\n"
	_, err := parse([]byte("required: [a]\nchecks:\n"+bad+program("new")), extract.Build)
	if err == nil {
		t.Fatal("loaded a check of an unknown scope")
	}
	if strings.Contains(err.Error(), "no check has") {
		t.Errorf("error %q names the check with an error of its own as missing", err)
	}
	if !strings.Contains(err.Error(), `checks[0]: check "a"`) || !strings.Contains(err.Error(), `these checks are not under required: "new"`) {
		t.Errorf("error %q does not name both the check's own error and the check added without its id", err)
	}
}

// Load returns the required list too, so the CLI can print it.
func TestLoadReturnsRequired(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, DefaultFile)
	if err := os.WriteFile(path, []byte("required: [a]\nchecks:\n"+program("a")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(root, path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Required, []string{"a"}) || cfg.Checks[0].At.File != DefaultFile {
		t.Errorf("Load returned required %q, check at %+v", cfg.Required, cfg.Checks[0].At)
	}
}
