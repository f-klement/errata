package extracttest

import (
	"testing"
	"time"
)

// A case measured past OtherBound carries its own Bound, and both runners
// read it. RunShapes once took OtherBound for every case, whatever its
// Bound.
func TestBoundIsTheCasesOwnOrOtherBound(t *testing.T) {
	if got := boundOf(Replay[struct{}]{}); got != OtherBound {
		t.Errorf("a case with no Bound: %v, want OtherBound, %v", got, OtherBound)
	}
	if got := boundOf(Replay[struct{}]{Bound: 3500 * time.Millisecond}); got != 3500*time.Millisecond {
		t.Errorf("a case with a Bound of 3.5 s: %v", got)
	}
}

// A read over its bound is read again, up to three times, and judged by
// its fastest: a linear shape a loaded node slowed failed CI at 1.07 s
// against 1 s, and three runs at once on builder took one to 1.6 s. A read within its bound is read once, and one over it every
// time stays over it.
func TestFastestReadsAgainOnlyWhileOverTheBound(t *testing.T) {
	for _, c := range []struct {
		name  string
		times []time.Duration
		reads int
		best  time.Duration
	}{
		{"within the bound", []time.Duration{900 * time.Millisecond, 0, 0}, 1, 900 * time.Millisecond},
		{"slowed once", []time.Duration{1500 * time.Millisecond, 600 * time.Millisecond, 0}, 2, 600 * time.Millisecond},
		{"over the bound every time", []time.Duration{3 * time.Second, 2 * time.Second, 4 * time.Second}, 3, 2 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			n := 0
			got, best, err := fastest(time.Second, func() (int, time.Duration, error) {
				n++
				return n, c.times[n-1], nil
			})
			if got != 1 || err != nil {
				t.Errorf("values of read %d, error %v; want the first read's", got, err)
			}
			if n != c.reads || best != c.best {
				t.Errorf("%d reads, the fastest %v; want %d, %v", n, best, c.reads, c.best)
			}
		})
	}
}
