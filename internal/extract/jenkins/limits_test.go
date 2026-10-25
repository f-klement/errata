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
// core.MaxValues. The planted break: the scan keeping every assignment,
// which TestReplayJenkinsShapesAtFullSize's assignments then hold in
// memory.
func TestJenkinsValueLimit(t *testing.T) {
	x := entryExtractor(t, Spec{File: "Jenkinsfile", Variable: "X"})
	x.ValueLimit = 3
	if got, err := x.FromData([]byte(strings.Repeat("env.X = 1\n", 3))); err != nil || len(got) != 3 {
		t.Fatalf("%d values, %v", len(got), err)
	}
	_, err := x.FromData([]byte(strings.Repeat("env.X = 1\n", 4)))
	extracttest.WantError(t, err, "Jenkinsfile: the extractor selects more than 100000 values, the limit")
	defs := strings.Repeat("def X = 1\n", core.MaxValues+10)
	s := newScan([]byte(defs), []byte("X"), nil, nil)
	if err := s.run(); err != nil || len(s.records) != core.MaxValues+1 {
		t.Fatalf("the scan kept %d assignments, %v", len(s.records), err)
	}
	// The def records past the cap do not hide an env.X = after them,
	// which counts and drops them: the correctness review's witness, which
	// gave no value.
	if g := got(t, defs+"env.X = 'b'\n", Spec{Variable: "X"}); g != fmt.Sprintf("b@%d", core.MaxValues+11) {
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
