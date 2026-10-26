package extracttest

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// Replay is one input measured against the extractors' earlier limits,
// rebuilt by its shape: what reading it must give. S is the spec of the
// reader it is read with.
type Replay[S any] struct {
	Name string
	Spec S // its file is f
	Body func() string
	// Either Values is the count of values and Last the text of the last
	// one, or Refused is the whole message.
	Values  int
	Last    string
	Refused string
	// TimeLimit replaces a regex search's time limit, so a refusal on time
	// comes fast.
	TimeLimit time.Duration
	// Bound is the most time reading the input may take, without -race, at
	// the fastest of up to three reads (fastest).
	// A case that replays a quadratic pass sets it at no more than a third
	// of the time that pass takes on the input, and at three times or more
	// the slowest of three linear runs on a loaded machine, under -race too;
	// its comment gives the three times.
	Bound time.Duration
	// Full marks a case at the size measured against its quadratic pass,
	// which only make slow runs: -race slows it past make test's time. A
	// case of the same shape at a tenth of the size stays in make test.
	Full bool
}

// OtherBound is the bound of a case that sets none: none of those replays a
// quadratic pass, so it stops a read gone badly wrong and was measured
// against no slow pass. It was 1 s until shell's deep pipelines took 1.06 s
// in CI beside two other pipelines. On 2026-10-26, with three runs of every
// package's replays at once on builder, the slowest of them, shell's
// assignments at 1 MiB, took 1.44 s at the fastest of three reads, and the
// slowest under -race at make test's size 0.39 s.
const OtherBound = 2 * time.Second

// boundOf is c's Bound, or OtherBound when it sets none.
func boundOf[S any](c Replay[S]) time.Duration {
	if c.Bound == 0 {
		return OtherBound
	}
	return c.Bound
}

// reads is how many times fastest reads an input that keeps going over its
// bound.
const reads = 3

// fastest reads with read, and again while the read takes more than bound,
// up to reads times. A linear read that a loaded node slows passes on a
// later read, and a quadratic one is slow every time: on 2026-10-26 three
// concurrent runs on builder took shell's and jenkins' linear shapes to
// 1.6 s against their bound of 1 s. It returns the first read's values and
// error, and the fastest read's time.
func fastest[V any](bound time.Duration, read func() (V, time.Duration, error)) (V, time.Duration, error) {
	got, best, err := read()
	for i := 1; i < reads && best > bound; i++ {
		_, took, _ := read()
		best = min(best, took)
	}
	return got, best, err
}

// timed is a read of root with e that gives its time.
func timed(e check.Extractor, root string) func() ([]check.Value, time.Duration, error) {
	return func() ([]check.Value, time.Duration, error) {
		start := time.Now()
		got, err := e.Extract(root)
		return got, time.Since(start), err
	}
}

// RunReplays reads the cases whose Full is full, each with the extractor
// build makes of its spec, from a file named f.
func RunReplays[S any](t *testing.T, build func(S) (check.Extractor, error), cases []Replay[S], full bool) {
	t.Helper()
	for _, c := range cases {
		if c.Full != full {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			if MutationRun && c.TimeLimit == 0 {
				// Under Gremlins only the cases of the regex clock run: the
				// others bound the time of a whole parse, which no mutant of
				// an operator was seen to change, and cost a second a mutant.
				t.Skip("make test runs it; Gremlins runs the cases that stop on the clock")
			}
			root := Repo(t, map[string]string{"f": c.Body()})
			e, err := build(c.Spec)
			if err != nil {
				t.Fatal(err)
			}
			if c.TimeLimit > 0 {
				e = withTimeLimit(t, e, c.TimeLimit)
			}
			bound := boundOf(c)
			got, took, err := fastest(bound*RaceSlowdown, timed(e, root))
			if took > bound*RaceSlowdown {
				t.Errorf("took %v, over %v", took, bound*RaceSlowdown)
			}
			switch {
			case c.Refused != "":
				if err == nil || !strings.HasPrefix(err.Error(), c.Refused) {
					t.Errorf("error %v, want %q", err, c.Refused)
				}
			case err != nil:
				t.Errorf("error %v, want %d values", err, c.Values)
			case len(got) != c.Values || got[len(got)-1].Text != c.Last:
				t.Errorf("%d values, %s; want %d, the last %q", len(got), Texts(got[max(0, len(got)-3):]), c.Values, c.Last)
			}
		})
	}
}

// withTimeLimit is e with its TimeLimit field set to d: the regex search's,
// which this package cannot name, since core's own tests import it.
func withTimeLimit(t *testing.T, e check.Extractor, d time.Duration) check.Extractor {
	t.Helper()
	v := reflect.New(reflect.TypeOf(e)).Elem()
	v.Set(reflect.ValueOf(e))
	if v.Kind() != reflect.Struct || !v.FieldByName("TimeLimit").CanSet() {
		t.Fatalf("a case with a time limit builds %T, which has no TimeLimit to set", e)
	}
	v.FieldByName("TimeLimit").Set(reflect.ValueOf(d))
	ex, ok := v.Interface().(check.Extractor)
	if !ok {
		t.Fatalf("%T is no longer an extractor", e)
	}
	return ex
}

// RunShapes reads each case, a dense shape of a file, from the file fileOf
// names, within its Bound or OtherBound. Last is a prefix of the last
// value's text: a shape's last value may be as long as the file.
func RunShapes[S any](t *testing.T, build func(S) (check.Extractor, error), cases []Replay[S], fileOf func(S) string) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			root := Repo(t, map[string]string{fileOf(c.Spec): c.Body()})
			e, err := build(c.Spec)
			if err != nil {
				t.Fatal(err)
			}
			bound := boundOf(c)
			got, took, err := fastest(bound*RaceSlowdown, timed(e, root))
			if took > bound*RaceSlowdown {
				t.Errorf("took %v, over %v", took, bound*RaceSlowdown)
			}
			switch {
			case c.Refused != "":
				if err == nil || !strings.HasPrefix(err.Error(), c.Refused) {
					t.Errorf("error %v, want %q", err, c.Refused)
				}
			case err != nil:
				t.Errorf("error %v", err)
			case len(got) != c.Values || (c.Values > 0 && !strings.HasPrefix(got[len(got)-1].Text, c.Last)):
				t.Errorf("%d values, want %d", len(got), c.Values)
			}
		})
	}
}
