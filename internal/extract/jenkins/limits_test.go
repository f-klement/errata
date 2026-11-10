package jenkins

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

func entryExtractor(t *testing.T, s Spec) core.EntryExtractor {
	t.Helper()
	e, err := Build(s)
	if err != nil {
		t.Fatal(err)
	}
	return e.(core.EntryExtractor)
}

// The extractor returns at most its value limit, and refuses past it, as
// every extractor does; the scan keeps at most one assignment more than
// its cap. The planted break: the scan keeping every assignment, which
// TestReplayJenkinsShapesAtFullSize's assignments then hold in memory.
// The cap is lowered to 5 here: at core.MaxValues the checks read 100,010
// lines three times, 82 of the package's 104 s under make test's race and
// coverage on a worker node, and the package passed go test's 2m on a busy runner
// (MR 42's pipeline 3043917907). TestJenkinsValueLimitAtFullSize runs them
// at the real cap.
func TestJenkinsValueLimit(t *testing.T) {
	keep := keepRecords
	keepRecords = 5
	t.Cleanup(func() { keepRecords = keep })
	valueLimit(t)
}

// The same at core.MaxValues.
func TestJenkinsValueLimitAtFullSize(t *testing.T) {
	slowOnly(t)
	if keepRecords != core.MaxValues {
		t.Fatalf("the scan keeps %d records, not core.MaxValues", keepRecords)
	}
	valueLimit(t)
}

// valueLimit checks the value limit and the scan's cap, keepRecords.
func valueLimit(t *testing.T) {
	t.Helper()
	x := entryExtractor(t, Spec{File: "Jenkinsfile", Variable: "X"})
	x.ValueLimit = 3
	if got, err := x.FromData([]byte(strings.Repeat("env.X = 1\n", 3))); err != nil || len(got) != 3 {
		t.Fatalf("%d values, %v", len(got), err)
	}
	_, err := x.FromData([]byte(strings.Repeat("env.X = 1\n", 4)))
	extracttest.WantError(t, err, "Jenkinsfile: the extractor selects more than 100000 values, the limit")
	defs := strings.Repeat("def X = 1\n", keepRecords+10)
	s := newScan([]byte(defs), []byte("X"), nil, nil)
	if err := s.run(); err != nil || len(s.records) != keepRecords+1 {
		t.Fatalf("the scan kept %d assignments, %v", len(s.records), err)
	}
	// The def records past the cap do not hide an env.X = after them,
	// which counts and drops them: the correctness review's witness, which
	// gave no value.
	if g := got(t, defs+"env.X = 'b'\n", Spec{Variable: "X"}); g != fmt.Sprintf("b@%d", keepRecords+11) {
		t.Errorf("got %q", g)
	}
	s = newScan([]byte("env.X = 1\n"+defs), []byte("X"), nil, nil)
	if err := s.run(); err != nil || len(s.records) != 1 {
		t.Fatalf("the scan kept %d assignments after env.X =, %v", len(s.records), err)
	}
}

// A value holds at most max_bytes in all, as every extractor's values.
func TestJenkinsValueBytesLimit(t *testing.T) {
	body := "env.X = 'aaaa'\nenv.X = 'bbbb'\n"
	x := entryExtractor(t, Spec{File: "Jenkinsfile", Variable: "X", MaxBytes: 7})
	_, err := x.FromData([]byte(body))
	extracttest.WantError(t, err, "Jenkinsfile: the values the extractor selects hold more than 7 bytes in all")
}

// Each read has a budget of work, which every token and every byte of a
// string is charged: below its cost a read is refused, at it the read
// returns what it reads with no meter.
func TestJenkinsWorkMeterRefusesAtEveryBudget(t *testing.T) {
	x := entryExtractor(t, Spec{File: "Jenkinsfile", Variable: "X", Stage: "s"})
	coretest.RefusesAtEveryBudget(t, x, "pipeline {\n  environment { X = \"${a}\" }\n  stages { stage('s') { environment { X = '1' } } }\n}\n")
}

// Ordinary Jenkinsfiles spend under a quarter of the budget a byte.
func TestJenkinsWorkMeterLeavesRoomForOrdinaryFiles(t *testing.T) {
	coretest.LeavesRoom(t, entryExtractor(t, Spec{File: "Jenkinsfile", Variable: "TAG", Stage: "build"}), pipeline)
}
