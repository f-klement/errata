package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// failPrefix starts the line go test prints for a test that failed. A
// top-level test's line starts at the left margin; a subtest's is indented.
const failPrefix = "--- FAIL: "

// maxScanLine is as much of one output line as failScan keeps: a test's name
// fits many times over, and a test that prints a long line costs no memory.
const maxScanLine = 4096

// failScan is a test binary's output, read for the top-level tests it says
// failed. bounded gives one to a binary as both its stdout and its stderr,
// in place of io.Discard.
type failScan struct {
	mu    sync.Mutex
	line  []byte
	names []string
}

func (s *failScan) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range p {
		if c == '\n' {
			s.endLine()
		} else if len(s.line) < maxScanLine {
			s.line = append(s.line, c)
		}
	}
	return len(p), nil
}

func (s *failScan) endLine() {
	if rest, ok := bytes.CutPrefix(s.line, []byte(failPrefix)); ok {
		if f := bytes.Fields(rest); len(f) > 0 && testName.Match(f[0]) {
			s.names = append(s.names, string(f[0]))
		}
	}
	s.line = s.line[:0]
}

// failed is the names read so far.
func (s *failScan) failed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.names...)
}

// recordKill adds the mutant at site to the index directory's kills, with the
// tests that failed on it, or a dash when no test is named.
func recordKill(indexDir, site string, tests []string) {
	names := "-"
	if len(tests) > 0 {
		names = strings.Join(tests, " ")
	}
	appendLog(filepath.Join(indexDir, "kills"), site+" "+names+"\n")
}

// kills is what a pass's kills file says: the tests some mutant's kill names,
// and the sites of the kills that name none.
type kills struct {
	n       int
	named   map[string]bool
	unnamed []string
}

// readKills reads the kills file; none means no mutant was killed.
func readKills(path string) (kills, error) {
	k := kills{named: map[string]bool{}}
	data, err := os.ReadFile(path) //nolint:gosec // the kills file in the index directory
	if errors.Is(err, fs.ErrNotExist) {
		return k, nil
	}
	if err != nil {
		return k, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			return k, fmt.Errorf("%s: not a kills line: %q", path, sc.Text())
		}
		k.n++
		if f[1] == "-" {
			k.unnamed = append(k.unnamed, f[0])
			continue
		}
		for _, t := range f[1:] {
			k.named[t] = true
		}
	}
	return k, nil
}

// keepers is the tests that must stay for the kills: each a kill names, and
// each that reaches the site of a kill that names none, since any of them may
// be the one that killed it. ok is false when such a kill has no site, or a
// site no indexed test reaches: any test may then be the one.
func (r *reach) keepers(k kills) (map[string]bool, bool) {
	keep := map[string]bool{}
	for _, site := range k.unnamed {
		file, line, ok := splitSite(site)
		if !ok {
			return nil, false
		}
		reaching := r.reaching(file, line)
		if len(reaching) == 0 {
			return nil, false
		}
		for _, t := range reaching {
			keep[t] = true
		}
	}
	return keep, true
}

// splitSite reads "<file>:<line>".
func splitSite(site string) (string, int, bool) {
	i := strings.LastIndexByte(site, ':')
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(site[i+1:])
	return site[:i], n, err == nil
}

// blocks is the blocks each test of the index ran, by test, as
// "<file> <first line> <last line> <first column> <last column>", and how
// many tests ran each block.
func (r *reach) blocks() ([][]string, map[string]int) {
	per := make([][]string, len(r.tests))
	count := map[string]int{}
	for file, spans := range r.ranges {
		for _, s := range spans {
			b := fmt.Sprintf("%s %d %d %d %d", file, s.first, s.last, s.firstCol, s.lastCol)
			per[s.test] = append(per[s.test], b)
		}
	}
	for i := range per {
		sort.Strings(per[i])
		per[i] = compactStrings(per[i])
		for _, b := range per[i] {
			count[b]++
		}
	}
	return per, count
}

func compactStrings(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// shared says whether every block of the test is run by at least two tests.
func shared(own []string, count map[string]int) bool {
	for _, b := range own {
		if count[b] < 2 {
			return false
		}
	}
	return true
}

// redundantMain is `testreach redundant <index dir>`: the status to exit with.
func redundantMain(stdout, stderr io.Writer, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, usageText)
		return 2
	}
	if err := redundant(stdout, args[0]); err != nil {
		fmt.Fprintf(stderr, "testreach: %v\n", err)
		return 1
	}
	return 0
}

// redundant writes, for the package of the index in indexDir, the tests that
// cover no block no other test covers and kill no mutant no other test kills.
// A block is a coverage block, by its lines and columns.
// "together" names a test that goes with every other test so named, chosen
// from the fewest blocks up, and "alone" one that could go by itself but not
// with those. A pass's kills name one killer a mutant (the first that failed),
// so a test a kill names stays even when another test kills that mutant too.
func redundant(w io.Writer, indexDir string) error {
	r, err := readIndex(filepath.Join(indexDir, "index"))
	if err != nil {
		return err
	}
	k, err := readKills(filepath.Join(indexDir, "kills"))
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "redundant: %s: %d tests, %d kills read, %d of them naming no test\n", r.pkg, len(r.tests), k.n, len(k.unnamed))
	reached, ok := r.keepers(k)
	if !ok {
		fmt.Fprintf(w, "redundant: %s: a kill names no test and no line an indexed test reaches, so no test is listed\n", r.pkg)
		return nil
	}
	per, count := r.blocks()
	alone, unmeasured, why := r.candidates(k, reached, per, count)
	fmt.Fprintf(w, "redundant: %s: kept: %d named by a kill, %d more reach a kill that names none, %d more run a block no other test runs\n", r.pkg, why[0], why[1], why[2])
	for _, i := range unmeasured {
		fmt.Fprintf(w, "redundant: %s: unmeasured %s (ran no block of the package)\n", r.pkg, r.tests[i])
	}
	together := r.together(w, alone, per, count)
	fmt.Fprintf(w, "redundant: %s: %d tests can go together, %d more only alone; %d unmeasured\n", r.pkg, together, len(alone)-together, len(unmeasured))
	return nil
}

// candidates is the tests no kill keeps and no block of their own keeps,
// fewest blocks first; apart, those that ran no block of the package, which
// the index did not measure (a test skipped under the mutation tag, a child
// process's helper, a test of test code or of another package), so mutation
// cannot judge their loss; and how many tests each of the three reasons kept.
func (r *reach) candidates(k kills, reached map[string]bool, per [][]string, count map[string]int) ([]int, []int, [3]int) {
	var alone, unmeasured []int
	why := [3]int{}
	for i, t := range r.tests {
		switch {
		case k.named[t]:
			why[0]++
		case reached[t]:
			why[1]++
		case !shared(per[i], count):
			why[2]++
		case len(per[i]) == 0:
			unmeasured = append(unmeasured, i)
		default:
			alone = append(alone, i)
		}
	}
	sort.SliceStable(alone, func(a, b int) bool { return len(per[alone[a]]) < len(per[alone[b]]) })
	return alone, unmeasured, why
}

// together writes each candidate, together while every block it runs is
// still run by another test that stays, alone otherwise, and returns how many
// go together.
func (r *reach) together(w io.Writer, alone []int, per [][]string, count map[string]int) int {
	together := 0
	for _, i := range alone {
		how := "alone"
		if shared(per[i], count) {
			how = "together"
			together++
			for _, b := range per[i] {
				count[b]--
			}
		}
		fmt.Fprintf(w, "redundant: %s: %s %s (%d blocks)\n", r.pkg, how, r.tests[i], len(per[i]))
	}
	return together
}
