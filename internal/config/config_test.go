package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const good = `
checks:
  - id: one
    reason: why one
    scope: repo
    run: [sh, one.sh]
  - id: two
    reason: why two
    scope: live
    run: [two]
    timeout: 5s
`

func TestParse(t *testing.T) {
	checks, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(checks))
	}
	if checks[0].Timeout != DefaultTimeout {
		t.Errorf("default timeout = %s, want %s", checks[0].Timeout, DefaultTimeout)
	}
	if checks[1].Timeout != 5*time.Second {
		t.Errorf("timeout = %s, want 5s", checks[1].Timeout)
	}
	if got := strings.Join(checks[0].Run, " "); got != "sh one.sh" {
		t.Errorf("run = %q", got)
	}
}

func TestParseEmpty(t *testing.T) {
	checks, err := Parse(nil)
	if err != nil || len(checks) != 0 {
		t.Fatalf("Parse(nil) = %v, %v; want no checks and no error", checks, err)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"missing reason": {
			"checks:\n- id: a\n  scope: repo\n  run: [x]\n", "reason is required",
		},
		"unknown key": {
			"checks:\n- id: a\n  why: typo for reason\n  reason: r\n  scope: repo\n  run: [x]\n", "unknown field",
		},
		"unknown top-level key": {
			"check:\n- id: a\n", "unknown field",
		},
		"duplicate id": {
			"checks:\n- {id: a, reason: r, scope: repo, run: [x]}\n- {id: a, reason: r, scope: repo, run: [x]}\n", "duplicate id",
		},
		"bad timeout": {
			"checks:\n- {id: a, reason: r, scope: repo, run: [x], timeout: soon}\n", "timeout",
		},
		"not yaml": {"checks: [", "decode"},
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

func TestParseReportsAllInvalidChecks(t *testing.T) {
	_, err := Parse([]byte("checks:\n- {id: a, scope: repo, run: [x]}\n- {id: b, scope: repo, run: [x]}\n"))
	if err == nil || !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("error = %v, want both checks named", err)
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFile)
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load of a missing file succeeded")
	}
}

// FuzzParse: arbitrary bytes never panic the parser, and anything it accepts
// is a set of valid checks with unique ids.
func FuzzParse(f *testing.F) {
	f.Add([]byte(good))
	f.Add([]byte("checks: []"))
	f.Add([]byte("checks:\n- {id: a, reason: r, scope: applied, run: [x], timeout: 1ms}\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		checks, err := Parse(raw)
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, c := range checks {
			if verr := c.Validate(); verr != nil {
				t.Fatalf("Parse accepted an invalid check: %v", verr)
			}
			if seen[c.ID] {
				t.Fatalf("Parse accepted duplicate id %q", c.ID)
			}
			seen[c.ID] = true
		}
	})
}
