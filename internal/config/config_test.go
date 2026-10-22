package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	yamlv2 "go.yaml.in/yaml/v2"

	"github.com/f-klement/errata/internal/check"
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

// A file with no checks does not load: it would run nothing and exit 0, so a
// gate on it would go green on nothing.
func TestParseRefusesNoChecks(t *testing.T) {
	for _, raw := range []string{"", "# only a comment\n", "checks:\n", "checks: []\n", "checks: ~\n"} {
		checks, err := Parse([]byte(raw))
		if !errors.Is(err, ErrNoChecks) || !strings.Contains(err.Error(), "the file holds no checks") {
			t.Errorf("Parse(%q) = %v, %v; want the no-checks refusal", raw, checks, err)
		}
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
		"unknown key names the check": {
			"checks:\n- {id: a, why: r, reason: r, scope: repo, run: [x]}\n", `checks[0]: check "a": decode: json: unknown field "why"`,
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

func TestParseReportsAllInvalidChecks(t *testing.T) {
	_, err := Parse([]byte("checks:\n- {id: a, scope: repo, run: [x]}\n- {id: b, scope: repo, run: [x]}\n"))
	if err == nil || !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("error = %v, want both checks named", err)
	}
}

// Load sets each check's location: the file relative to the root when it is
// inside, as given when it is not, and the line its entry starts on.
func TestLoad(t *testing.T) {
	root := t.TempDir()
	write := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	outside := write(filepath.Join(t.TempDir(), DefaultFile))
	for path, want := range map[string]check.Location{
		write(filepath.Join(root, DefaultFile)):         {File: "errata.yaml"},
		write(filepath.Join(root, "ci", "checks.yaml")): {File: "ci/checks.yaml"},
		outside: {File: outside, Outside: true},
	} {
		checks, err := Load(root, path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range []int{3, 7} {
			want.Line = line
			if got := checks[i].At; got != want {
				t.Errorf("Load(%s): check %d is at %+v, want %+v", path, i, got, want)
			}
		}
	}
	if _, err := Load(root, filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load of a missing file succeeded")
	}
}

// A config outside the root given by a path relative to the working
// directory is named as given and marked outside. Named as given and not
// marked, ci.yaml would read as the file of that name in the repository.
func TestLoadOutsideTheRootFromTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yaml"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for _, path := range []string{"ci.yaml", "repo/../ci.yaml"} {
		checks, err := Load("repo", path)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := checks[0].At, (check.Location{File: path, Line: 3, Outside: true}); got != want {
			t.Errorf("Load(repo, %s): check is at %+v, want %+v", path, got, want)
		}
	}
	// Inside the root, a path relative to the working directory is made
	// relative to the root.
	if err := os.WriteFile(filepath.Join(root, "checks.yaml"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	checks, err := Load("repo", "repo/checks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := checks[0].At, (check.Location{File: "checks.yaml", Line: 3}); got != want {
		t.Errorf("Load(repo, repo/checks.yaml): check is at %+v, want %+v", got, want)
	}
}

// Parse sets the line each check's entry starts on, as the decoder counts
// lines, so a file with CR line ends or an alias entry places its checks too.
func TestParseSetsLines(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want []int
	}{
		"block":         {good, []int{3, 7}},
		"flow":          {"checks: [{id: a, reason: r, scope: repo, run: [x]},\n  {id: b, reason: r, scope: repo, run: [x]}]\n", []int{1, 2}},
		"CR line ends":  {strings.ReplaceAll(good, "\n", "\r"), []int{3, 7}},
		"after a ---":   {"# head\n---\n" + good, []int{5, 9}},
		"merged entry":  {"checks:\n- {id: a, <<: &b {reason: r, scope: repo, run: [x]}}\n- <<: *b\n  id: c\n", []int{2, 3}},
		"merged checks": {"<<: {checks: [{id: a, reason: r, scope: repo, run: [x]}]}\n", []int{0}},
		"utf-16 file":   {utf16Text(good, false), []int{3, 7}},
	} {
		t.Run(name, func(t *testing.T) {
			checks, err := Parse([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			var got []int
			for _, c := range checks {
				got = append(got, c.At.Line)
				if c.At.File != "" {
					t.Errorf("Parse set the file %q; only Load knows it", c.At.File)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("lines = %v, want %v", got, tc.want)
			}
		})
	}
}

// exactKeys looks inside a list of objects too. No field of errata.yaml is
// such a list yet, so the type here is the test's own.
func TestExactKeysReadsListsOfObjects(t *testing.T) {
	type item struct {
		Name string `json:"name"`
	}
	errs := exactKeys(json.RawMessage(`{"items": [{"name": "a"}, {"Name": "b"}]}`), reflect.TypeFor[struct {
		Items []item `json:"items"`
	}](), "")
	if len(errs) != 1 || errs[0].Error() != `items[1]: key "Name" must be spelled "name"` {
		t.Fatalf("errors = %v, want the key of the second item", errs)
	}
}

// FuzzParse: arbitrary bytes never panic the parser, and anything it accepts
// is a set of valid checks with unique ids.
func FuzzParse(f *testing.F) {
	f.Add([]byte(good))
	f.Add([]byte("checks: []"))
	f.Add([]byte("checks:\n- {id: a, reason: r, scope: applied, run: [x], timeout: 1ms}\n"))
	f.Add([]byte(goodRule))
	f.Add([]byte("checks:\n- {id: a, reason: r, scope: repo, extract: {v: {toml: {file: f, path: a}}}, assert: v}\n"))
	// The review's shapes of a second document that a scan of LF lines missed.
	f.Add([]byte("checks:\n- {id: a, reason: r, scope: repo, run: [x]}\r---\rchecks:\n- {id: b, reason: r, scope: repo, run: [x]}\n"))
	f.Add([]byte("checks:\n- {id: a, reason: r, scope: repo, run: [x]}\n\u2028---\u2028checks:\n- {id: b, reason: r, scope: repo, run: [x]}\n"))
	f.Add([]byte("checks:\n- {id: a, reason: r, scope: repo, run: [x]}\n... {checks: [{id: b, reason: r, scope: repo, run: [x]}]}\n"))
	f.Add([]byte("checks:\n- {id: a, reason: r, scope: repo, run: [x]}\n---\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		checks, err := Parse(raw)
		if err != nil {
			return
		}
		// sigs.k8s.io/yaml decodes the first document with yaml/v2, and Parse
		// counts with yaml/v3. A file Parse accepts must be one document to
		// v2 as well, or a check v2 reads in a second document is dropped.
		if n, err := v2Documents(raw); err != nil || n > 1 {
			t.Fatalf("Parse accepted a file that yaml/v2 reads as %d documents (%v)", n, err)
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

// v2Documents counts the documents yaml/v2's decoder reads in raw, leaving
// out the empty ones at the end, as documents does with yaml/v3.
func v2Documents(raw []byte) (int, error) {
	dec := yamlv2.NewDecoder(bytes.NewReader(raw))
	n, kept := 0, 0
	for {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return kept, nil
		}
		if err != nil {
			return n + 1, err
		}
		n++
		if v != nil {
			kept = n
		}
	}
}
