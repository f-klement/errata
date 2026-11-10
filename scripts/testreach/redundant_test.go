package main

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// redundantOf writes the index and the kills to a directory and returns what
// redundant prints for it.
func redundantOf(t *testing.T, index, kills string) string {
	t.Helper()
	d := t.TempDir()
	write(t, filepath.Join(d, "index"), "package example.com/m/p\ndir p\n"+index)
	if kills != "" {
		write(t, filepath.Join(d, "kills"), kills)
	}
	var b bytes.Buffer
	if err := redundant(&b, d); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestShared and TestAlsoShared run what TestKeeper runs, and TestKeeper is
// named by a kill, so both may go, together. TestOwn alone runs b.go 1 2.
// TestTwinA and TestTwinB run the same block and nothing else: either may
// go, not both. TestNothing runs no line of the package, so the index did
// not measure it: it is unmeasured, never together. TestSolo, which no kill
// names, stays for its own block. Planted breaks: shared with < 1 in place
// of < 2 lists TestOwn; keepers that skips the named kills lists TestKeeper;
// candidates without its case for no block lists TestNothing together.
func TestRedundantListsTestsThatAddNoBlockAndNoKill(t *testing.T) {
	got := redundantOf(t,
		"test TestKeeper\na.go 1 5\n"+
			"test TestShared\na.go 1 5\n"+
			"test TestOwn\na.go 1 5\nb.go 1 2\n"+
			"test TestTwinA\nc.go 3 4\n"+
			"test TestTwinB\nc.go 3 4\n"+
			"test TestAlsoShared\na.go 1 5\n"+
			"test TestNothing\n"+
			"test TestSolo\nd.go 1 1\n",
		"a.go:2 TestKeeper\nb.go:1 TestOwn\n")
	want := "redundant: example.com/m/p: 8 tests, 2 kills read, 0 of them naming no test\n" +
		"redundant: example.com/m/p: kept: 2 named by a kill, 0 more reach a kill that names none, 1 more run a block no other test runs\n" +
		"redundant: example.com/m/p: unmeasured TestNothing (ran no block of the package)\n" +
		"redundant: example.com/m/p: together TestShared (1 blocks)\n" +
		"redundant: example.com/m/p: together TestTwinA (1 blocks)\n" +
		"redundant: example.com/m/p: alone TestTwinB (1 blocks)\n" +
		"redundant: example.com/m/p: together TestAlsoShared (1 blocks)\n" +
		"redundant: example.com/m/p: 3 tests can go together, 1 more only alone; 1 unmeasured\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// A kill that names no test, a timeout, keeps every test that reaches its
// line, since any of them may be the one that killed it. Planted break:
// keepers that skips the unnamed kills lists both.
func TestRedundantKeepsEveryTestThatReachesAnUnnamedKill(t *testing.T) {
	got := redundantOf(t, "test TestA\na.go 1 5\ntest TestB\na.go 1 5\n", "a.go:3 -\n")
	if !strings.HasSuffix(got, ": 0 tests can go together, 0 more only alone; 0 unmeasured\n") || !strings.Contains(got, "1 kills read, 1 of them naming no test") {
		t.Fatalf("got\n%s", got)
	}
}

// An unnamed kill with no line, or at a line no indexed test reaches, keeps
// every test of the package, since any of them may have killed it. Planted
// break: keepers without its check that some test reaches the line lists
// TestB for the second.
func TestRedundantListsNothingForAKillWithNoSite(t *testing.T) {
	for _, kill := range []string{"- -\n", "z.go:9 -\n"} {
		got := redundantOf(t, "test TestA\na.go 1 5\ntest TestB\na.go 1 5\n", kill)
		if !strings.HasSuffix(got, "a kill names no test and no line an indexed test reaches, so no test is listed\n") {
			t.Fatalf("%q: got\n%s", kill, got)
		}
	}
}

// Two blocks on one line, a condition and the closure in it, are two blocks:
// TestBody alone runs the closure and stays, and TestCond, which runs only
// the condition TestBody runs too, is listed. Planted break: blocks keyed by
// their lines alone, which lists both.
func TestRedundantTellsBlocksOnOneLineApart(t *testing.T) {
	got := redundantOf(t, "test TestCond\na.go 5 5 2 59\ntest TestBody\na.go 5 5 2 59\na.go 5 5 61 83\n", "")
	if !strings.Contains(got, "together TestCond") || strings.Contains(got, "TestBody") || !strings.Contains(got, "1 more run a block no other test runs") {
		t.Fatalf("got\n%s", got)
	}
}

// ranLines keeps each block's columns after its lines, so the index tells
// apart two blocks on one line. Planted break: the columns left out.
func TestRanLinesKeepsTheColumns(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "c.out")
	write(t, profile, "mode: set\nexample.com/m/p/a.go:5.2,5.59 1 1\nexample.com/m/p/a.go:5.61,5.83 1 1\nexample.com/m/p/a.go:7.1,9.2 2 0\nexample.com/m/p/sub/b.go:1.1,1.9 1 1\n")
	var b bytes.Buffer
	if err := ranLines(&b, profile, "example.com/m/p"); err != nil {
		t.Fatal(err)
	}
	if got, want := b.String(), "a.go 5 5 2 59\na.go 5 5 61 83\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Without a kills file, no mutant was killed: coverage alone decides.
func TestRedundantWithoutKills(t *testing.T) {
	got := redundantOf(t, "test TestA\na.go 1 5\ntest TestB\na.go 1 5\n", "")
	if !strings.Contains(got, "together TestA") || !strings.Contains(got, "alone TestB") {
		t.Fatalf("got\n%s", got)
	}
}

func TestRedundantRefusesAKillsLineWithoutTests(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "index"), "package example.com/m/p\ndir p\ntest TestA\n")
	write(t, filepath.Join(d, "kills"), "a.go:3\n")
	if err := redundant(&bytes.Buffer{}, d); err == nil || !strings.Contains(err.Error(), "not a kills line") {
		t.Fatalf("got %v", err)
	}
}

// failScan reads the top-level tests go test says failed, across writes that
// split a line, and not a subtest's indented line. Planted break: endLine
// without the testName match reads "FAIL" from the summary line.
func TestFailScanReadsTheFailedTopLevelTests(t *testing.T) {
	s := &failScan{}
	for _, part := range []string{"=== RUN TestA\n--- FA", "IL: TestA (0.00s)\n    --- FAIL: TestA/sub (0.00s)\n", "--- FAIL: ExampleB (0.00s)\n--- FAIL: \nFAIL\n"} {
		if n, err := s.Write([]byte(part)); n != len(part) || err != nil {
			t.Fatalf("Write: %d, %v", n, err)
		}
	}
	if got := s.failed(); !reflect.DeepEqual(got, []string{"TestA", "ExampleB"}) {
		t.Fatalf("got %q", got)
	}
}

// A narrowed run's kill names the test its output says failed; a whole run's
// kill whose output names none, and a timeout, record a dash. Planted break:
// bounded with io.Discard in place of failScan records a dash for the first.
func TestRunRecordsWhichTestKilledAMutant(t *testing.T) {
	w := newWorld(t)
	t.Setenv("NARROW_RC", "1")
	t.Setenv("FAIL_OUT", "--- FAIL: TestLess (0.01s)")
	if rc := run(w.goBin, w.root, w.index, mutantRun); rc != 1 {
		t.Fatalf("exit %d", rc)
	}
	t.Setenv("FAIL_OUT", "")
	t.Setenv("NARROW_RC", "0")
	t.Setenv("WHOLE_RC", "1")
	if rc := run(w.goBin, w.root, w.index, mutantRun); rc != 1 {
		t.Fatalf("exit %d", rc)
	}
	t.Setenv("WHOLE_RC", "0")
	if rc := run(w.goBin, w.root, w.index, mutantRun); rc != 0 {
		t.Fatalf("a survivor: exit %d", rc)
	}
	w.took["whole"] = time.Minute
	if rc := run(w.goBin, w.root, w.index, mutantRun); rc != 1 {
		t.Fatalf("a timeout: exit %d", rc)
	}
	if got, want := read(t, filepath.Join(w.index, "kills")), "p.go:9 TestLess\np.go:9 -\np.go:9 -\n"; got != want {
		t.Fatalf("kills %q, want %q", got, want)
	}
}

// testreach redundant takes one index directory, and says why it fails.
func TestRedundantMainTakesOneDirectory(t *testing.T) {
	var out, errs bytes.Buffer
	if rc := redundantMain(&out, &errs, nil); rc != 2 || !strings.HasPrefix(errs.String(), "usage: testreach") {
		t.Fatalf("no directory: exit %d, %q", rc, errs.String())
	}
	errs.Reset()
	if rc := redundantMain(&out, &errs, []string{t.TempDir()}); rc != 1 || !strings.HasPrefix(errs.String(), "testreach: ") {
		t.Fatalf("no index: exit %d, %q", rc, errs.String())
	}
	d := t.TempDir()
	write(t, filepath.Join(d, "index"), "package example.com/m/p\ndir p\ntest TestA\n")
	if rc := redundantMain(&out, &errs, []string{d}); rc != 0 || !strings.Contains(out.String(), "1 tests, 0 kills read") {
		t.Fatalf("an index: exit %d, %q", rc, out.String())
	}
}
