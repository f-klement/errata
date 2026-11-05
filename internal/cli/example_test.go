package cli

import (
	"bytes"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/config"
)

// unreachable are the scopes no run in CI can reach, since errata has no
// provider for a running system yet (docs/reference.md#scopes). The example's
// checks of these scopes are skipped, and every other check runs. When a
// provider lands, this list shrinks and the example's checks of that scope
// must hold in CI.
var unreachable = []check.Scope{check.ScopeLive, check.ScopeApplied}

// examples is the example directory, from this package's directory.
const examples = "../../examples"

// The example config runs green on its fixtures, in place, with the command
// examples/README.md gives, from the top of this repository: examples/ is a
// root below the top, so errata reads the index of the repository above it
// (docs/adr/0018-a-root-below-the-top-reads-the-index-of-the-repository-above.md).
// Every check of a scope in unreachable is skipped by name, and no other is;
// every witness holds. A check that does not hold is named with its verdict.
// Planted breaks, each read off the line it fails on: the marker's port in
// docs/operations.md changed to 8081 ("api-port-agrees is FALSE"), applied
// dropped from unreachable (the skipped list differs), the gradle.properties
// of the witness guava-drifts set to the version Maven pins
// ("guava-pinned-once [witness guava-drifts] is FALSE"), and core's
// rootIndex taking the root for the top of its repository
// ("api-port-agrees is UNKNOWN").
//
// It needs a git checkout of errata, since errata refuses a root that no
// repository holds (ADR 0018). Outside one, as in a source archive, it fails
// on that refusal. It skips only in a mutation build, whose copy of the
// module holds no .git: the skip follows the build tag, not the tree the
// test finds, so CI's test job and every checkout still run it.
func TestExampleRunsGreen(t *testing.T) {
	if mutationRun {
		t.Skip("a mutation build's copy of the module holds no .git, and the example needs the repository above examples/")
	}
	cfg, err := config.Load(examples, filepath.Join(examples, "errata.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	t.Chdir(filepath.Join(examples, ".."))
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"check", "--root", "examples", "--config", "examples/errata.example.yaml"}, &stdout, &stderr)
	for _, m := range regexp.MustCompile(`(?m)^(FALSE|UNKNOWN) (.+) \(\w+\)$`).FindAllStringSubmatch(stdout.String(), -1) {
		t.Errorf("%s is %s, want every check of the example to hold", m[2], m[1])
	}
	if code != 0 {
		t.Fatalf("the example exits %d, want 0\nstdout:\n%s\nstderr:\n%s", code, &stdout, &stderr)
	}

	var wantSkipped []string
	witnesses := 0
	for _, c := range cfg.Checks {
		if slices.Contains(unreachable, c.Scope) {
			wantSkipped = append(wantSkipped, c.ID+" ("+string(c.Scope)+")")
		}
		witnesses += len(c.Witness)
	}
	var skipped []string
	for _, m := range regexp.MustCompile(`(?m)^  skipped (\S+ \(\w+\)): `).FindAllStringSubmatch(stdout.String(), -1) {
		skipped = append(skipped, m[1])
	}
	slices.Sort(wantSkipped)
	slices.Sort(skipped)
	if !slices.Equal(skipped, wantSkipped) {
		t.Errorf("the example skips %v, want the checks of the scopes CI cannot reach, %v\n%s", skipped, wantSkipped, &stdout)
	}
	if len(wantSkipped) == 0 {
		t.Error("the example has no check of a scope CI cannot reach, so the test shows no skip")
	}
	ran := len(cfg.Checks) - len(wantSkipped)
	coverage := regexp.MustCompile(`(?m)^0 errata, 0 unknown\. Coverage: ran ` + strconv.Itoa(ran) +
		`, skipped ` + strconv.Itoa(len(wantSkipped)) + `\. ` + strconv.Itoa(witnesses) + ` witnesses held\.$`)
	if !coverage.Match(stdout.Bytes()) {
		t.Errorf("the coverage line does not say ran %d, skipped %d, %d witnesses held:\n%s", ran, len(wantSkipped), witnesses, &stdout)
	}
}
