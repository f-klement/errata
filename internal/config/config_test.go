package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	yamlv2 "go.yaml.in/yaml/v2"

	"go.yaml.in/yaml/v3"

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
	cfg, err := parseRaw([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(cfg.Checks))
	}
	if cfg.Checks[0].Timeout != DefaultTimeout {
		t.Errorf("default timeout = %s, want %s", cfg.Checks[0].Timeout, DefaultTimeout)
	}
	if cfg.Checks[1].Timeout != 5*time.Second {
		t.Errorf("timeout = %s, want 5s", cfg.Checks[1].Timeout)
	}
	if got := strings.Join(runOf(cfg.Checks[0]), " "); got != "sh one.sh" {
		t.Errorf("run = %q", got)
	}
}

// A file with no checks does not load: it would run nothing and exit 0, so a
// gate on it would go green on nothing.
func TestParseRefusesNoChecks(t *testing.T) {
	for _, raw := range []string{"", "# only a comment\n", "checks:\n", "checks: []\n", "checks: ~\n"} {
		cfg, err := parseRaw([]byte(raw))
		if !errors.Is(err, ErrNoChecks) || !strings.Contains(err.Error(), "the file holds no checks") {
			t.Errorf("parse(%q) = %v, %v; want the no-checks refusal", raw, cfg.Checks, err)
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
		// The protocol reads zero as no limit, so 0s would run a hung program
		// for ever while it reads as zero time. The planted break: assigning
		// the parsed duration without the check, which loads all three.
		"zero timeout": {
			"checks:\n- {id: a, reason: r, scope: repo, run: [x], timeout: 0s}\n", "timeout is 0s: write a positive duration such as 10s, or leave the key out for the default of 30s",
		},
		"bare zero timeout": {
			"checks:\n- {id: a, reason: r, scope: repo, run: [x], timeout: \"0\"}\n", "timeout is 0: write a positive duration",
		},
		"negative timeout, refused once": {
			"checks:\n- {id: a, reason: r, scope: repo, run: [x], timeout: -1s}\n", `check "a": timeout is -1s: write a positive duration such as 10s, or leave the key out for the default of 30s`,
		},
		"not yaml": {"checks: [", "decode"},
		"unknown key names the check": {
			"checks:\n- {id: a, why: r, reason: r, scope: repo, run: [x]}\n", `checks[0]: check "a": decode: json: unknown field "why"`,
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

// A negative timeout is refused by the loader's message alone, not a second
// time by validation.
func TestNegativeTimeoutIsRefusedOnce(t *testing.T) {
	_, err := parseRaw([]byte("checks:\n- {id: a, reason: r, scope: repo, run: [x], timeout: -1s}\n"))
	if err == nil || strings.Count(err.Error(), "timeout") != 1 {
		t.Fatalf("error = %v, want one message about the timeout", err)
	}
}

func TestParseReportsAllInvalidChecks(t *testing.T) {
	_, err := parseRaw([]byte("checks:\n- {id: a, scope: repo, run: [x]}\n- {id: b, scope: repo, run: [x]}\n"))
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
		cfg, err := Load(root, path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range []int{3, 7} {
			want.Line = line
			if got := cfg.Checks[i].At; got != want {
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
		cfg, err := Load("repo", path)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := cfg.Checks[0].At, (check.Location{File: path, Line: 3, Outside: true}); got != want {
			t.Errorf("Load(repo, %s): check is at %+v, want %+v", path, got, want)
		}
	}
	// Inside the root, a path relative to the working directory is made
	// relative to the root.
	if err := os.WriteFile(filepath.Join(root, "checks.yaml"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("repo", "repo/checks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Checks[0].At, (check.Location{File: "checks.yaml", Line: 3}); got != want {
		t.Errorf("Load(repo, repo/checks.yaml): check is at %+v, want %+v", got, want)
	}
}

// parse sets the line each check's entry starts on, as the decoder counts
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
			cfg, err := parseRaw([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			var got []int
			for _, c := range cfg.Checks {
				got = append(got, c.At.Line)
				if c.At.File != "" {
					t.Errorf("parse set the file %q; only Load knows it", c.At.File)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("lines = %v, want %v", got, tc.want)
			}
		})
	}
}

// lines follows no alias. One entry written as an alias gets the alias's
// own line, and checks written whole as an alias give no line at all. No
// config that loads holds either, since an anchor before checks sits under
// a key that refuses a check, so lines is read here on the tree. The
// planted break: lines taking the line of the node an alias names.
func TestLinesFollowsNoAlias(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want []int
	}{
		"an aliased entry": {"checks:\n- &e {id: a}\n- *e\n", []int{2, 3}},
		"aliased checks":   {"x: &c [{id: a}]\nchecks: *c\n", nil},
	} {
		_, doc, err := documents([]byte(tc.raw))
		if err != nil {
			t.Fatal(err)
		}
		if got := lines(doc); !slices.Equal(got, tc.want) {
			t.Errorf("%s: lines = %v, want %v", name, got, tc.want)
		}
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
	f.Add([]byte("checks:\n- {id: a, reason: r, scope: repo, generated: {file: a.md, run: [gen, <errata.id>], max_bytes: 9}}\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		cfg, err := parseRaw(raw)
		if err != nil {
			return
		}
		// sigs.k8s.io/yaml decodes the first document with yaml/v2, and Parse
		// counts with yaml/v3. A file parse accepts must be one document to
		// v2 as well, or a check v2 reads in a second document is dropped.
		if n, err := v2Documents(raw); err != nil || n > 1 {
			t.Fatalf("parse accepted a file that yaml/v2 reads as %d documents (%v)", n, err)
		}
		seen := map[string]bool{}
		for _, c := range cfg.Checks {
			if verr := c.Validate(); verr != nil {
				t.Fatalf("parse accepted an invalid check: %v", verr)
			}
			if seen[c.ID] {
				t.Fatalf("parse accepted duplicate id %q", c.ID)
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

// A config of MaxConfigBytes loads, and one byte more is refused before it is
// parsed, naming the limit: the loader's memory grows with the config, and
// errata list is run on configs nobody has read. The planted break: reading
// the file whole, which loads the larger config.
func TestLoadRefusesAConfigOverTheLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, DefaultFile)
	for _, size := range []int{MaxConfigBytes, MaxConfigBytes + 1} {
		pad := size - len(good) - len("#\n")
		raw := good + "#" + strings.Repeat("x", pad) + "\n"
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(root, path)
		switch {
		case size == MaxConfigBytes && err != nil:
			t.Errorf("a config of %d bytes: %v", size, err)
		case size > MaxConfigBytes && (err == nil || err.Error() != "the file is over 1048576 bytes (1 MiB), the largest config errata reads"):
			t.Errorf("a config of %d bytes: %v", size, err)
		}
	}
}

// aliasedReason is a config of n checks whose reasons are one scalar of 64
// KiB, anchored by the first and aliased by the rest, the shape of the
// security review of #90.
func aliasedReason(n int) []byte {
	var b strings.Builder
	b.WriteString("checks:\n- {id: c0, scope: repo, run: [x], reason: &r " + strings.Repeat("a", 64<<10) + "}\n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "- {id: c%d, scope: repo, run: [x], reason: *r}\n", i)
	}
	return []byte(b.String())
}

// Aliases may not expand a config past MaxExpandedBytes: the YAML library
// counts aliased nodes, not their bytes, so one long scalar aliased by every
// check multiplied the loaded config (security review of #90: 528 KB of
// config ran the loader out of memory). A config just under the limit
// loads, one just over it is refused, and so is a chain of aliases of
// aliases, counted in one pass over the tree. The planted break: Parse
// without checkExpansion, which loads the config over the limit.
func TestAliasesMayNotExpandAConfigPastTheLimit(t *testing.T) {
	if _, err := parseRaw(aliasedReason(60)); err != nil {
		t.Errorf("60 aliases of 64 KiB, 3.8 MiB expanded: %v", err)
	}
	want := "decode: aliases expand the file to more than 4194304 bytes (4 MiB), the most a config may hold; write the repeated part out"
	if _, err := parseRaw(aliasedReason(70)); err == nil || err.Error() != want {
		t.Errorf("70 aliases of 64 KiB, 4.4 MiB expanded: %v", err)
	}
	// A later document that the tree cannot read used to skip the count,
	// while the YAML library that converts the first expanded it anyway
	// (security review of #90, delta 1: 456 KB of config ran out of
	// memory). The planted break: counting only when every document reads.
	for _, bad := range []string{"---\n[unclosed\n", "...\ntext after the end\n"} {
		raw := append(aliasedReason(70), bad...)
		alloc, err := parseAllocs(raw)
		if err == nil || err.Error() != want {
			t.Errorf("70 aliases of 64 KiB and then %q: %v", bad, err)
		}
		if alloc > 64<<20 {
			t.Errorf("70 aliases of 64 KiB and then %q: allocated %d MiB before the refusal", bad, alloc>>20)
		}
	}
	var laughs strings.Builder
	laughs.WriteString("l0: &l0 [" + strings.Repeat("a", 64) + "]\n")
	for i := 1; i < 12; i++ {
		fmt.Fprintf(&laughs, "l%d: &l%d [%s]\n", i, i, strings.TrimSuffix(strings.Repeat(fmt.Sprintf("*l%d, ", i-1), 10), ", "))
	}
	laughs.WriteString("checks: []\n")
	if _, err := parseRaw([]byte(laughs.String())); err == nil || err.Error() != want {
		t.Errorf("aliases of aliases, 10^11 nodes expanded: %v", err)
	}
}

// checkExpansion counts one byte for each node and the bytes of each scalar,
// and refuses a count over MaxExpandedBytes, not one at it: a document of
// exactly the limit passes and one byte more is refused, including when the
// byte comes after the count has reached the limit within a mapping.
func TestCheckExpansionBoundary(t *testing.T) {
	scalar := func(n int) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: strings.Repeat("a", n)} }
	doc := func(children ...*yaml.Node) *yaml.Node {
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Content: children}}}
	}
	// The document, the mapping and a scalar's own node are 3 bytes.
	if err := checkExpansion(doc(scalar(MaxExpandedBytes - 3))); err != nil {
		t.Errorf("a document of exactly the limit: %v", err)
	}
	if err := checkExpansion(doc(scalar(MaxExpandedBytes - 2))); err == nil {
		t.Error("a document one byte over the limit passes")
	}
	if err := checkExpansion(doc(scalar(MaxExpandedBytes-3), scalar(0))); err == nil {
		t.Error("a document that reaches the limit and then holds one more node passes")
	}
}

// A syntax error in the first document names the line it is on, as yaml.v2
// names it: yaml.v3, which reads the documents first, names the line before
// (correctness delta review of #90). The planted break: returning yaml.v3's
// error, which names lines 2, 2 and 1.
func TestFirstDocumentSyntaxErrorNamesItsLine(t *testing.T) {
	for raw, want := range map[string]string{
		"checks:\n  - id: a\n    reason: [unclosed\n": "decode: error converting YAML to JSON: yaml: line 3: did not find expected ',' or ']'",
		"checks:\n  - id: a\n\treason: r\n":           "decode: error converting YAML to JSON: yaml: line 3: found a tab character that violates indentation",
		"checks:\n  - {id: a, reason: r\n":            "decode: error converting YAML to JSON: yaml: line 2: did not find expected ',' or '}'",
	} {
		if _, err := parseRaw([]byte(raw)); err == nil || err.Error() != want {
			t.Errorf("%q: error %v, want %s", raw, err, want)
		}
	}
}

// The probe firstDocumentError runs with yaml.v2 costs about the file's own
// size, aliases or not: yaml.v2 decodes into plain values, where a scalar an
// alias names is shared rather than copied, and its own guard bounds the
// nodes aliases add. So should yaml.v2 read a first document yaml.v3 refused,
// which no input tried so far does, the alias bound is not needed to keep it
// small. The shapes are 64 KiB aliased by 8000 checks, as a scalar and in a
// mapping, which converted to JSON take 512 MB. The planted break: probing
// with toJSON, which converts to JSON, and fails the bound.
func TestFirstDocumentProbeIsBounded(t *testing.T) {
	var mapping strings.Builder
	mapping.WriteString("base: &b {reason: " + strings.Repeat("a", 64<<10) + ", scope: repo}\nchecks:\n")
	for i := range 8000 {
		fmt.Fprintf(&mapping, "- {<<: *b, id: c%d, run: [x]}\n", i)
	}
	v3err := errors.New("planted yaml.v3 error")
	for name, raw := range map[string][]byte{
		"scalar":  aliasedReason(8000),
		"mapping": []byte(mapping.String()),
	} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		err := firstDocumentError(raw, v3err)
		runtime.ReadMemStats(&after)
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
			t.Errorf("%s: the probe allocated %d MiB", name, alloc>>20)
		}
		if !errors.Is(err, v3err) {
			t.Errorf("%s: a file yaml.v2 reads gives %v, want yaml.v3's error", name, err)
		}
	}
}
