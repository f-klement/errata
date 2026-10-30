package check

import (
	"fmt"
	"slices"
	"strings"
)

// dependencyErrors refuses each extractor that reads the values of one
// extract does not declare, and the first cycle of extractors that read
// each other's values, itself included, since none of them could run first.
// declared holds the name of every extractor of r.
func (r Rule) dependencyErrors(declared map[string]bool) []error {
	var errs []error
	reads := map[string][]string{}
	for _, e := range r.Extractors {
		if e.Extractor == nil {
			// It did not build, which is reported where it failed, so what
			// its description says it reads is not reported again.
			continue
		}
		for _, n := range e.Description.Reads {
			if !declared[n] {
				errs = append(errs, fmt.Errorf("extractor %q reads the values of %q, which extract does not declare", e.Name, n))
				continue
			}
			reads[e.Name] = append(reads[e.Name], n)
		}
	}
	if cycle := findCycle(r.Extractors, reads); cycle != nil {
		errs = append(errs, fmt.Errorf("extractors read each other's values in a cycle, so none of them can run first: %s",
			strings.Join(quoteAll(cycle), " reads ")))
	}
	return errs
}

// findCycle returns the first cycle in reads, a search from each extractor
// in the order of extractors, as the names along it with the first again at
// the end, such as a, b, a; or nil when there is none.
func findCycle(extractors []NamedExtractor, reads map[string][]string) []string {
	const (
		unseen = iota
		open   // on the path the search is following
		closed // searched, and on no cycle
	)
	state := map[string]int{}
	var path, cycle []string
	var visit func(n string) bool
	visit = func(n string) bool {
		state[n] = open
		path = append(path, n)
		for _, m := range reads[n] {
			if state[m] == open {
				cycle = append(slices.Clone(path[slices.Index(path, m):]), m)
				return true
			}
			if state[m] == unseen && visit(m) {
				return true
			}
		}
		path = path[:len(path)-1]
		state[n] = closed
		return false
	}
	for _, e := range extractors {
		if state[e.Name] == unseen && visit(e.Name) {
			return cycle
		}
	}
	return nil
}

func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return out
}

// Order is names in the order a rule runs their extractors: each one after
// every extractor of names that it reads the values of, and otherwise in
// the order names lists them, so a rule whose extractors read no other's
// values runs them as names lists them. A name that names no extractor of r keeps its place.
func (r Rule) Order(names []string) []string {
	reads := make(map[string][]string, len(r.Extractors))
	for _, e := range r.Extractors {
		reads[e.Name] = e.Description.Reads
	}
	listed := make(map[string]bool, len(names))
	for _, n := range names {
		listed[n] = true
	}
	placed := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	var place func(n string)
	place = func(n string) {
		if placed[n] || !listed[n] {
			return
		}
		// Placed before what it reads, so a cycle, which validation
		// refuses, ends here rather than recursing for ever.
		placed[n] = true
		for _, m := range reads[n] {
			place(m)
		}
		out = append(out, n)
	}
	for _, n := range names {
		place(n)
	}
	return out
}
