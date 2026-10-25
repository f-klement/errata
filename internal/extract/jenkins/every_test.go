package jenkins

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// every reads TAG in body as a Jenkinsfile with Every, each value as
// text@line origin, or the error.
func every(t *testing.T, body string) string {
	t.Helper()
	x, err := Every("Jenkinsfile", "TAG", 0)
	if err != nil {
		t.Fatalf("Every: %v", err)
	}
	values, err := x.Extract(extracttest.Repo(t, map[string]string{"Jenkinsfile": body}))
	if err != nil {
		return "error: " + err.Error()
	}
	var out []string
	for _, v := range values {
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		out = append(out, fmt.Sprintf("%s@%d%s %s", v.Text, v.At.Line, mark, v.Origin))
	}
	return strings.Join(out, "; ")
}

// Every gives the value the pipeline's top sees and the value of each
// stage whose environment sets the variable, a nested one too, each named
// as its origin; a stage that sets none adds none. Planted breaks: the
// stages not read; the origin left out.
func TestEveryPlace(t *testing.T) {
	body := `pipeline {
    environment { TAG = '1.0' }
    stages {
        stage('build') {
            environment { TAG = '1.1' }
            steps { sh 'make' }
        }
        stage('test') { steps { sh 't' } }
        stage('deploy') {
            stages {
                stage('push') {
                    environment { TAG = "${BUILD_NUMBER}" }
                    steps { sh 'push' }
                }
            }
        }
    }
}
`
	if got, want := every(t, body), `1.0@2 pipeline; 1.1@5 stage "build"; "${BUILD_NUMBER}"@12! stage "push"`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A file whose top sees no value but a stage sets one gives the stage's,
// and one that sets none gives no value, not an error. Planted break: the
// top's check.NoValue returned, which refused both.
func TestEveryPlaceNone(t *testing.T) {
	body := "pipeline {\n    stages {\n        stage('a') {\n            environment { TAG = '1' }\n            steps { sh 'a' }\n        }\n    }\n}\n"
	if got, want := every(t, body), `1@4 stage "a"`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := every(t, "pipeline { stages { stage('a') { steps { sh 'a' } } } }\n"); got != "" {
		t.Errorf("got %q, want no value", got)
	}
}

// What makes one place's value unknown makes Every's: a stage named twice,
// a library, and a stage whose name is not literal, which errata cannot
// name. A variable that is no name does not build. Planted break: the
// unnamed stage passed over, which gave no value.
func TestEveryPlaceRefuses(t *testing.T) {
	twice := "pipeline { stages {\nstage('a') { environment { TAG = '1' }\nsteps { sh 'a' } }\nstage('a') { steps { sh 'a' } } } }\n"
	extracttest.WantError(t, errOf(every(t, twice)), "a second stage is named \"a\"")
	lib := "@Library('x') _\npipeline { stages { stage('a') { steps { sh 'a' } } } }\n"
	extracttest.WantError(t, errOf(every(t, lib)), "may set TAG, and errata cannot read it")
	unnamed := "pipeline { stages {\nstage(\"${X}\") {\nenvironment { TAG = '1' }\nsteps { sh 'a' } } } }\n"
	extracttest.WantError(t, errOf(every(t, unnamed)), "line 2: the environment of a stage whose name is not literal sets TAG")
	_, err := Every("Jenkinsfile", "a-b", 0)
	extracttest.WantError(t, err, "is no name an environment block accepts")
}

// errOf is the error every gave, nil for values.
func errOf(s string) error {
	if msg, ok := strings.CutPrefix(s, "error: "); ok {
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// The reads share the extractor's one work budget: a file of many stages
// that each set the variable is refused at it, rather than read once for
// each. Four stages show it. The meter readEvery is given is charged at
// least what the pipeline's read and each stage's read cost on meters of
// their own, so the stage reads are on it, and a budget one short of what
// it spends is refused. Planted break: a meter of its own for each stage's
// read, which charged the shared meter only for the scan and the
// pipeline's read.
func TestEveryPlaceSharesItsBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("pipeline { stages {\n")
	for i := range 4 {
		fmt.Fprintf(&b, "stage('s%d') { environment { TAG = '1' }\nsteps { sh 'a' } }\n", i)
	}
	b.WriteString("} }\n")
	data := []byte(b.String())
	none := func(*core.Entry) error { return nil }
	cost := func(r reader) int64 {
		m := &core.Meter{Budget: math.MaxInt64}
		// The pipeline sees no value here, as readEvery allows.
		var nothing *check.NoValue
		if err := r.read(data, m, none); err != nil && !errors.As(err, &nothing) {
			t.Fatalf("read %q: %v", r.stage, err)
		}
		return m.Spent
	}
	r := reader{file: "Jenkinsfile", variable: "TAG"}
	apart := cost(r)
	for i := range 4 {
		apart += cost(reader{file: r.file, variable: r.variable, stage: fmt.Sprintf("s%d", i), staged: true})
	}
	shared := &core.Meter{Budget: math.MaxInt64}
	if err := r.readEvery(data, shared, none); err != nil {
		t.Fatal(err)
	}
	if shared.Spent < apart {
		t.Errorf("the shared meter spent %d units, under the %d its reads cost apart", shared.Spent, apart)
	}
	if err := r.readEvery(data, &core.Meter{Budget: shared.Spent - 1}, none); !errors.Is(err, core.ErrWork) {
		t.Errorf("a budget one short of %d units: %v, want the work refusal", shared.Spent, err)
	}
}
