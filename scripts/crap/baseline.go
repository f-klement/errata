package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// limit is the score every function stays below, unless the baseline lists it.
const limit = 10.0

// baseline is each listed function's score, by fn.key.
type baseline map[string]float64

// baselineHeader opens the file -write-baseline prints.
const baselineHeader = `# scripts/crap-baseline.txt: each function that scores CRAP 10 or more, with its score,
# as scripts/crap prints it: <namespace> <name> <score>. make cover holds the scores to it:
# a function with no row scores below 10, a row's score does not rise, and a row whose
# function is gone or scores below 10 leaves the file in the same change. No row is ever
# added: the file only shrinks (CODING_STANDARDS.md, A function scores CRAP below 10).
`

// rounded is a score at the two decimals the baseline holds.
func rounded(s float64) float64 {
	return math.Round(s*100) / 100
}

// readBaselineFile reads a baseline file.
func readBaselineFile(name string) (baseline, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readBaseline(f, name)
}

// readBaseline parses a baseline: a comment or a blank, or a row of three
// fields whose last is a score. It refuses a row it cannot read and a key
// listed twice.
func readBaseline(r io.Reader, name string) (baseline, error) {
	b := baseline{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := b.add(line); err != nil {
			return nil, fmt.Errorf("%s does not parse at line %d: %w", name, n, err)
		}
	}
	return b, sc.Err()
}

// add reads one row into the baseline.
func (b baseline) add(line string) error {
	f := strings.Fields(line)
	if len(f) != 3 {
		return fmt.Errorf("%q is not <namespace> <name> <score>", line)
	}
	s, err := strconv.ParseFloat(f[2], 64)
	if err != nil {
		return fmt.Errorf("%q: the score %w", line, err)
	}
	k := f[0] + " " + f[1]
	if _, twice := b[k]; twice {
		return fmt.Errorf("%s has two rows", k)
	}
	b[k] = s
	return nil
}

// check holds the scores to the baseline, and names each function that breaks
// a rule, sorted.
func check(fns map[string]fn, base baseline) []string {
	var out []string
	for _, f := range fns {
		if p := checkScore(f, base); p != "" {
			out = append(out, p)
		}
	}
	for k, was := range base {
		if p := checkRow(k, was, fns); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// checkScore names a function at 10 or more with no row, or above its row.
func checkScore(f fn, base baseline) string {
	s := rounded(f.crap())
	was, listed := base[f.key]
	switch {
	case s < limit:
		return ""
	case !listed:
		return fmt.Sprintf("%s scores %.2f (%s), and a function with no row in the baseline scores below 10", f.key, s, describe(f))
	case s > was:
		return fmt.Sprintf("%s rose from %.2f to %.2f (%s), and a listed score may not rise", f.key, was, s, describe(f))
	}
	return ""
}

// checkRow names a row whose function is gone or scores below 10.
func checkRow(k string, was float64, fns map[string]fn) string {
	f, ok := fns[k]
	if !ok {
		return fmt.Sprintf("stale row: %s (%.2f) is gone from the scored tree; remove its row", k, was)
	}
	if s := rounded(f.crap()); s < limit {
		return fmt.Sprintf("stale row: %s now scores %.2f (%s), below 10; remove its row", k, s, describe(f))
	}
	return ""
}

// describe is a function's place, complexity and coverage.
func describe(f fn) string {
	return fmt.Sprintf("%s, complexity %d, coverage %.1f%%", f.where, f.comp, 100*f.cov())
}

// shrinks names each row of cur that old lacks or that scores higher than old's.
func shrinks(old, cur baseline) []string {
	var out []string
	for k, s := range cur {
		was, ok := old[k]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("new row: %s (%.2f) is not in the older baseline, and the baseline only shrinks", k, s))
		case s > was:
			out = append(out, fmt.Sprintf("raised row: %s is %.2f, above the older baseline's %.2f", k, s, was))
		}
	}
	sort.Strings(out)
	return out
}

// summary is the line a passing check prints.
func summary(fns map[string]fn, base baseline) string {
	return fmt.Sprintf("crap: %d functions scored; %d at 10 or more, each in the baseline at its score or lower", len(fns), len(base))
}

// sorted is the functions, worst first, then by key.
func sorted(fns map[string]fn) []fn {
	out := make([]fn, 0, len(fns))
	for _, f := range fns {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].crap(), out[j].crap()
		if a != b {
			return a > b
		}
		return out[i].key < out[j].key
	})
	return out
}

// writeList prints every function's score, worst first, as tab separated
// columns under a header line.
func writeList(w io.Writer, fns map[string]fn) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, "crap\tcomplexity\tcoverage\tstatements\tfunction\twhere")
	for _, f := range sorted(fns) {
		fmt.Fprintf(bw, "%.2f\t%d\t%.3f\t%d\t%s\t%s\n", f.crap(), f.comp, f.cov(), f.stmts, f.key, f.where)
	}
	return bw.Flush()
}

// writeBaseline prints the baseline the scores give: a row for each function
// at 10 or more, sorted by key.
func writeBaseline(w io.Writer, fns map[string]fn) error {
	bw := bufio.NewWriter(w)
	fmt.Fprint(bw, baselineHeader)
	keys := make([]string, 0, len(fns))
	for k, f := range fns {
		if rounded(f.crap()) >= limit {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(bw, "%s %.2f\n", k, rounded(fns[k].crap()))
	}
	return bw.Flush()
}
