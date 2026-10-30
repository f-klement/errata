package githubactions

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// every is what Every reads of PORT in a workflow, each value as values
// writes it and its origin after it.
func every(t *testing.T, workflow string, maxBytes int64) (string, error) {
	t.Helper()
	x, err := Every(wf, "PORT", maxBytes, yamlParse)
	if err != nil {
		t.Fatalf("Every: %v", err)
	}
	vs, err := x.Extract(t.Context(), extracttest.Repo(t, map[string]string{wf: workflow}))
	var out []string
	for _, v := range vs {
		out = append(out, values([]check.Value{v})+" "+v.Origin)
	}
	return strings.Join(out, "; "), err
}

// Every gives the value each job sees, its env: over the workflow's, and
// the value of each step whose env: sets the variable, each named as its
// origin, a step by its id, its name or its line; a matrix job's value for
// each combination. A job that calls a reusable workflow, and a job that
// sees none, give none; a step that sets none adds none. Planted breaks:
// the reusable workflow job refused, as Build refuses it; the steps not
// read; a job with no value refused; the origin left out.
func TestEveryJobAndStep(t *testing.T) {
	got, err := every(t, `on: push
env: {PORT: "80"}
jobs:
  build:
    runs-on: x
    env: {PORT: "81"}
    steps:
      - run: make
      - id: up
        env: {PORT: "82"}
        run: up
      - name: down
        env: {PORT: "83"}
        run: down
      - env: {PORT: "84"}
        run: other
  test:
    runs-on: x
    strategy: {matrix: {port: ["1", "2"]}}
    env: {PORT: "${{ matrix.port }}"}
    steps: [{run: t}]
  call:
    uses: ./.github/workflows/other.yml
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := `81@6 job "build"; 82@10 job "build" step "up"; 83@13 job "build" step "down"; 84@15 job "build" step at line 15; ` +
		`1@19[test (1)] job "test"; 2@19[test (2)] job "test"`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	got, err = every(t, "on: push\njobs:\n  a: {runs-on: x, steps: [{run: a}]}\n", 0)
	if err != nil || got != "" {
		t.Errorf("no value: %q, %v", got, err)
	}
}

// A step's own env: wins over a GITHUB_ENV write before it, as the
// runner merges it over the job's environment, so its value is read; a
// step without one, after a step that may write GITHUB_ENV, sees what the
// write left, which is out of reach, so the job's value is unknown, and so
// it is when the job sets none, since the write may set it. A write in the
// last step reaches no step, and a quiet action writes none. Planted
// breaks: the steps before not read, which gave 8080 for the server that
// runs on 9090; a step with its own env: refused after a write, as before
// this fix; the steps checked once a step with its own env: had refused,
// which let the inheriting step through; each step checked against
// itself.
func TestEveryStepAfterGitHubEnv(t *testing.T) {
	for _, c := range []struct{ name, workflow, want string }{
		{"inherits after a write", `on: push
env: {PORT: "8080"}
jobs:
  a:
    runs-on: x
    steps:
      - run: echo "PORT=9090" >> "$GITHUB_ENV"
      - run: ./server
`, "names GITHUB_ENV in its run:, which can set PORT for the steps after it"},
		{"no value, inherits after a write", `on: push
jobs:
  a:
    runs-on: x
    steps:
      - uses: some/action@v1
      - run: ./server
`, `runs the action "some/action@v1", which can set PORT for the steps after it`},
		{"own env: then an inheriting step", `on: push
jobs:
  a:
    runs-on: x
    steps:
      - run: echo "PORT=9" >> "$GITHUB_ENV"
      - env: {PORT: "1"}
        run: a
      - run: b
`, "names GITHUB_ENV in its run:"},
	} {
		_, err := every(t, c.workflow, 0)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	for _, c := range []struct{ name, workflow, want string }{
		{"own env: after a write", `on: push
jobs:
  a:
    runs-on: x
    steps:
      - run: echo "PORT=9" >> "$GITHUB_ENV"
      - env: {PORT: "1"}
        run: a
`, `1@7 job "a" step at line 7`},
		{"a quiet action, then the step's own write", `on: push
jobs:
  a:
    runs-on: x
    steps:
      - uses: actions/checkout@v5
      - env: {PORT: "1"}
        run: echo "PORT=9" >> "$GITHUB_ENV"
`, `1@7 job "a" step at line 7`},
		{"a write in the last step", `on: push
env: {PORT: "8"}
jobs:
  a:
    runs-on: x
    steps:
      - run: make
      - run: echo "PORT=9" >> "$GITHUB_ENV"
`, `8@2 job "a"`},
	} {
		got, err := every(t, c.workflow, 0)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

// Two steps of one name are each named by their line, so a rule can tell
// them apart; a step's id names it whatever its name. Planted break: the
// name used whatever it repeats.
func TestEveryStepNamesTwoStepsOfOneName(t *testing.T) {
	got, err := every(t, `on: push
jobs:
  a:
    runs-on: x
    steps:
      - name: up
        env: {PORT: "1"}
        run: a
      - name: up
        id: second
        env: {PORT: "2"}
        run: b
      - name: up
        env: {PORT: "3"}
        run: c
`, 0)
	if want := `1@7 job "a" step at line 6; 2@11 job "a" step "second"; 3@14 job "a" step at line 13`; err != nil || got != want {
		t.Errorf("got %q, %v\nwant %q", got, err, want)
	}
}

// What Build refuses for a job Every refuses too, and a variable that is
// no name, or one the runner sets, does not build.
func TestEveryRefuses(t *testing.T) {
	_, err := every(t, "on: push\njobs:\n  a: [x]\n", 0)
	extracttest.WantError(t, err, `job "a" is a sequence, not a mapping`)
	_, err = every(t, "on: push\njobs:\n  a: {runs-on: x, steps: x}\n", 0)
	extracttest.WantError(t, err, "steps: is a scalar, not a sequence")
	_, err = every(t, "on: push\njobs:\n  a: {runs-on: x, steps: [{env: [x], run: a}]}\n", 0)
	extracttest.WantError(t, err, "the env: of step at line 3 is a sequence, not a mapping")
	_, err = every(t, "on: push\njobs:\n  a: {runs-on: x, env: \"${{ x }}\"}\n", 0)
	extracttest.WantError(t, err, `the env: of job "a" is an expression`)
	_, err = every(t, "on: push\njobs:\n  a: {runs-on: x, steps: [{env: {PORT: \"${{ x }}\"}, run: a}], container: {image: i, env: {PORT: \"2\"}}}\n", 0)
	extracttest.WantError(t, err, "both set PORT")
	_, err = every(t, "on: push\njobs:\n  a: {runs-on: x, strategy: {matrix: \"${{ fromJSON(x) }}\"}, env: {PORT: \"${{ matrix.p }}\"}}\n", 0)
	extracttest.WantError(t, err, "matrix")
	_, err = Every(wf, "GITHUB_TOKEN", 0, yamlParse)
	extracttest.WantError(t, err, "is one the runner sets")
	_, err = Every(wf, "1X", 0, yamlParse)
	extracttest.WantError(t, err, "is no variable name")
	_, err = Every("/x", "PORT", 0, yamlParse)
	extracttest.WantError(t, err, "is outside the repository")
}

// The values in all keep to core.MaxValues and max_bytes, and values of
// exactly either bound are kept; the value bound is witnessed at 100, a
// limit the test sets. Planted breaks: either bound dropped; the byte
// bound taken as >=; the value bound taken as one more.
func TestEveryBounds(t *testing.T) {
	// The value bound at 100 values, as the extractor holds it at
	// core.MaxValues: 100000 jobs would pass the mutation run's heap cap.
	var b strings.Builder
	b.WriteString("on: push\nenv: {PORT: \"8\"}\njobs:\n")
	for i := range 100 {
		fmt.Fprintf(&b, "  j%d: {}\n", i)
	}
	x, err := Every(wf, "PORT", 0, yamlParse)
	if err != nil {
		t.Fatal(err)
	}
	limited := x.(extractor)
	limited.valueLimit = 100
	if vs, err := limited.Extract(t.Context(), extracttest.Repo(t, map[string]string{wf: b.String()})); err != nil || len(vs) != 100 {
		t.Errorf("100 values under a limit of 100: %d, %v", len(vs), err)
	}
	b.WriteString("  j100: {}\n")
	_, err = limited.Extract(t.Context(), extracttest.Repo(t, map[string]string{wf: b.String()}))
	extracttest.WantError(t, err, fmt.Sprintf("more than %d values", core.MaxValues))

	long := "on: push\nenv: {PORT: \"" + strings.Repeat("8", 1000) + "\"}\njobs:\n  a: {}\n  b: {}\n"
	if _, err := every(t, long, 2000); err != nil {
		t.Errorf("values of 2000 bytes in all under max_bytes 2000: %v", err)
	}
	_, err = every(t, long+"  c: {}\n", 2000)
	extracttest.WantError(t, err, "the values of every job and step hold more than 2000 bytes in all")
}

// Every returns at most core.MaxValues values, unless a test sets its
// limit. Planted break: another figure for no limit.
func TestEveryValueLimitIsMaxValues(t *testing.T) {
	if got := (extractor{}).maxValues(); got != core.MaxValues {
		t.Errorf("no limit set: %d, want %d", got, core.MaxValues)
	}
	if got := (extractor{valueLimit: 5}).maxValues(); got != 5 {
		t.Errorf("a limit of 5: %d", got)
	}
}
