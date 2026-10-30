package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// sign is the source of the profile below. TestSign called Sign(-1), Sign(0)
// and Sign(7), so the first clause's body ran, the second's never did, and the
// third's, which is empty, ran.
const sign = `package p

// Sign names n's range.
func Sign(n int) string {
	switch {
	case n < 0:
		return "negative"
	case n > 100,
		n == 50:
		return "large"
	case n == 7:
	}
	return "other"
}
`

// signProfile is what go test -cover -coverprofile printed for sign with Go
// 1.27.2: no block holds a case expression.
const signProfile = `mode: set
example.com/p/p.go:5.2,5.9 1 1
example.com/p/p.go:7.3,7.20 1 1
example.com/p/p.go:10.3,10.17 1 0
example.com/p/p.go:11.14,11.14 0 1
example.com/p/p.go:13.2,13.16 1 1
`

// The mutants gremlins finds in sign's case expressions, by the position of
// their operator token.
var (
	runArm     = pos{6, 9}  // n < 0, whose body ran
	unrunFirst = pos{8, 9}  // n > 100, whose body never ran
	unrunNext  = pos{9, 5}  // n == 50, on the clause's second line
	emptyArm   = pos{11, 9} // n == 7, whose empty body ran
)

func readSign(rel string) ([]byte, error) {
	if rel != "p.go" {
		return nil, os.ErrNotExist
	}
	return []byte(sign), nil
}

// gremlinsCovers is how gremlins v0.6.0 decides that a mutant is covered
// (internal/coverage/profile.go): the position lies in a block a test ran,
// read line by line.
func gremlinsCovers(t *testing.T, profile string, p pos) bool {
	t.Helper()
	for _, line := range strings.Split(profile, "\n") {
		m := blockLine.FindStringSubmatch(line)
		if m == nil || m[1] != "example.com/p/p.go" || m[7] == "0" {
			continue
		}
		n := make([]int, 4)
		for i := range n {
			n[i], _ = strconv.Atoi(m[i+2])
		}
		sl, sc, el, ec := n[0], n[1], n[2], n[3]
		switch {
		case p.line > sl && p.line < el,
			sl == el && p.line == sl && p.col >= sc && p.col <= ec,
			sl < el && p.line == sl && p.col >= sc,
			sl < el && p.line == el && p.col <= ec:
			return true
		}
	}
	return false
}

func withCaseBlocks(t *testing.T) string {
	t.Helper()
	lines, err := caseBlocks([]byte(signProfile), "example.com/p", readSign)
	if err != nil {
		t.Fatal(err)
	}
	return signProfile + strings.Join(lines, "\n") + "\n"
}

// Before the helper, every case expression reads as not covered, the run arm's
// too: the blind spot this command closes.
func TestTheProfileAloneLeavesEveryCaseExpressionOut(t *testing.T) {
	for _, p := range []pos{runArm, unrunFirst, unrunNext, emptyArm} {
		if gremlinsCovers(t, signProfile, p) {
			t.Errorf("the profile alone covers %v; the fixture no longer shows the blind spot", p)
		}
	}
}

// A clause whose body a test ran makes its expression's mutant covered, an
// empty body as well.
func TestARunClauseCoversItsCaseExpression(t *testing.T) {
	got := withCaseBlocks(t)
	for _, p := range []pos{runArm, emptyArm} {
		if !gremlinsCovers(t, got, p) {
			t.Errorf("the mutant at %v, in a clause whose body ran, is not covered:\n%s", p, got)
		}
	}
}

// A clause whose body no test ran leaves its expressions' mutants not covered,
// on every line of its list. The planted break, every case expression treated
// as covered (ranAny returning true), fails here.
func TestAClauseNoTestReachesStaysNotCovered(t *testing.T) {
	got := withCaseBlocks(t)
	for _, p := range []pos{unrunFirst, unrunNext} {
		if gremlinsCovers(t, got, p) {
			t.Errorf("the mutant at %v, in a clause whose body never ran, is covered:\n%s", p, got)
		}
	}
}

// The block runs from the case keyword to the colon, so it holds the
// expressions and nothing of the body.
func TestTheBlockSpansCaseToColon(t *testing.T) {
	lines, err := caseBlocks([]byte(signProfile), "example.com/p", readSign)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com/p/p.go:6.2,6.12 0 1", "example.com/p/p.go:11.2,11.13 0 1"}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("caseBlocks gave %q, want %q", lines, want)
	}
}

// pick has a select whose first arm a test ran and whose second it did not,
// and a //line directive above Sign that renumbers its lines from 200.
const pick = `package p

// Pick reads one of two channels.
func Pick(a, b chan int, n int) int {
	select {
	case a <- n + 1:
		return 1
	case v := <-b:
		return v * 2
	}
}

//line p.go:200
func Sign(n int) string {
	switch {
	case n < 0:
		return "negative"
	}
	return "other"
}
`

// pickProfile is what go test -cover -coverprofile printed for pick with Go
// 1.27.2, after Pick(a, b, 1) with a buffered and Sign(-1). It counts lines
// as the file has them, the //line directive notwithstanding.
const pickProfile = `mode: set
example.com/p/p.go:5.2,5.9 1 1
example.com/p/p.go:7.3,7.11 1 1
example.com/p/p.go:9.3,9.15 1 0
example.com/p/p.go:15.2,15.9 1 1
example.com/p/p.go:17.3,17.20 1 1
example.com/p/p.go:19.2,19.16 1 0
`

func readPick(rel string) ([]byte, error) {
	if rel != "p.go" {
		return nil, os.ErrNotExist
	}
	return []byte(pick), nil
}

func pickBlocks(t *testing.T) []string {
	t.Helper()
	lines, err := caseBlocks([]byte(pickProfile), "example.com/p", readPick)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// A select arm whose body ran covers its statement, n + 1's mutant among it;
// the arm whose body never ran gets no block. The planted break, clause
// refusing a CommClause, fails here.
func TestARunSelectArmCoversItsStatement(t *testing.T) {
	lines := pickBlocks(t)
	got := pickProfile + strings.Join(lines, "\n") + "\n"
	if !gremlinsCovers(t, got, pos{6, 14}) {
		t.Errorf("the mutant of n + 1, in an arm whose body ran, is not covered:\n%s", got)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "example.com/p/p.go:8.") {
			t.Errorf("the arm whose body never ran got %q", l)
		}
	}
	if len(lines) == 0 || lines[0] != "example.com/p/p.go:6.2,6.17 0 1" {
		t.Errorf("caseBlocks gave %q, want the first arm's block 6.2,6.17 first", lines)
	}
}

// Under a //line directive the block is at the line the file has, where the
// profile counts it, not at the line the directive gives. The planted break,
// PositionFor following the directive, puts it at line 202 and fails here.
func TestALineDirectiveLeavesPositionsAsWritten(t *testing.T) {
	lines := pickBlocks(t)
	want := "example.com/p/p.go:16.2,16.12 0 1"
	if len(lines) != 2 || lines[1] != want {
		t.Errorf("caseBlocks gave %q, want Sign's block %q", lines, want)
	}
}

// A profile name that no file has, as a //line directive naming another file
// gives, is skipped, not an error that fails gremlins' coverage pass. The
// planted break, the skip removed, fails here.
func TestAProfileNameNoFileHasIsSkipped(t *testing.T) {
	profile := "mode: set\nexample.com/p/other.go:15.2,15.9 1 1\n"
	lines, err := caseBlocks([]byte(profile), "example.com/p", readPick)
	if err != nil || len(lines) != 0 {
		t.Errorf("caseBlocks gave %q, %v; want nothing and no error", lines, err)
	}
}

// A file outside the module, and one no block of which ran, are not read.
func TestFilesOutsideTheModuleOrNeverRunAreNotRead(t *testing.T) {
	profile := "mode: set\nother.org/q/q.go:1.1,2.2 1 1\nexample.com/p/z.go:1.1,2.2 1 0\n"
	lines, err := caseBlocks([]byte(profile), "example.com/p", func(rel string) ([]byte, error) {
		t.Errorf("read %s", rel)
		return nil, os.ErrNotExist
	})
	if err != nil || len(lines) != 0 {
		t.Errorf("caseBlocks gave %q, %v; want nothing", lines, err)
	}
}

// A profile it cannot read fails, so gremlins' coverage pass fails with it
// rather than run on a profile half rewritten.
func TestABadProfileFails(t *testing.T) {
	for name, profile := range map[string]string{
		"no mode line":    "example.com/p/p.go:5.2,5.9 1 1\n",
		"a line not read": "mode: set\nexample.com/p/p.go 5.2,5.9 1 1\n",
	} {
		if _, err := caseBlocks([]byte(profile), "example.com/p", readSign); err == nil {
			t.Errorf("%s: caseBlocks gave no error", name)
		}
	}
	if _, err := caseBlocks([]byte(signProfile), "example.com/p", func(string) ([]byte, error) {
		return []byte("package p\nfunc {"), nil
	}); err == nil {
		t.Error("a source that does not parse gave no error")
	}
}

// A profile that names a file outside the module root fails, and the file is
// not read, though it exists and parses. The planted break, the IsLocal check
// in run removed, reads it and passes.
func TestAFileOutsideTheRootFails(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"root/go.mod":      "module example.com/p\n\ngo 1.21\n",
		"outside.go":       sign,
		"root/profile.out": "mode: set\nexample.com/p/../outside.go:7.3,7.20 1 1\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := run(root, filepath.Join(root, "profile.out"))
	if err == nil || !strings.Contains(err.Error(), "outside the module root") {
		t.Errorf("run gave %v, want a refusal of a file outside the module root", err)
	}
}

// run appends the blocks to the profile file and keeps what was there.
func TestRunAppendsToTheProfile(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"go.mod":      "module example.com/p\n\ngo 1.21\n",
		"p.go":        sign,
		"profile.out": signProfile,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(dir, filepath.Join(dir, "profile.out")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "profile.out"))
	if err != nil {
		t.Fatal(err)
	}
	want := signProfile + "example.com/p/p.go:6.2,6.12 0 1\nexample.com/p/p.go:11.2,11.13 0 1\n"
	if string(got) != want {
		t.Errorf("the profile reads\n%s\nwant\n%s", got, want)
	}
	if err := run(t.TempDir(), filepath.Join(dir, "profile.out")); err == nil {
		t.Error("a root without go.mod gave no error")
	}
}
