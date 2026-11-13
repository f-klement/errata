package check

import (
	"context"
	"errors"
	"testing"
)

// In a run, Shared computes each key once and hands every caller the
// result and its error; outside one, and in another run, it computes again.
// Planted breaks: Shared computing on every call; one run for every ctx.
func TestSharedComputesOncePerRun(t *testing.T) {
	calls := 0
	compute := func() (int, error) { calls++; return calls, errors.New("e") }
	ctx := WithRun(context.Background())
	for range 2 {
		if v, err := Shared(ctx, "k", compute); v != 1 || err == nil || err.Error() != "e" {
			t.Errorf("in a run: %d %v, want 1 e", v, err)
		}
	}
	if v, _ := Shared(ctx, "other", compute); v != 2 {
		t.Errorf("another key: %d, want 2", v)
	}
	if v, _ := Shared(WithRun(context.Background()), "k", compute); v != 3 {
		t.Errorf("another run: %d, want 3", v)
	}
	if v, _ := Shared(context.Background(), "k", compute); v != 4 {
		t.Errorf("outside a run: %d, want 4", v)
	}
	if v, _ := Shared(context.Background(), "k", compute); v != 5 {
		t.Errorf("outside a run, again: %d, want 5", v)
	}
}

// A compute that panics, which a caller recovers, leaves an error for the
// next caller, not a zero value, and so does a key asked for as another
// type. Planted break: the check of the value's type dropped.
func TestSharedNeverHandsOnAZeroValue(t *testing.T) {
	ctx := WithRun(context.Background())
	func() {
		defer func() { _ = recover() }()
		_, _ = Shared(ctx, "k", func() (int, error) { panic("compute") })
	}()
	if v, err := Shared(ctx, "k", func() (int, error) { return 1, nil }); err == nil || v != 0 {
		t.Errorf("after a panic: %d %v, want an error", v, err)
	}
	if _, err := Shared(ctx, "n", func() (int, error) { return 1, nil }); err != nil {
		t.Fatal(err)
	}
	if v, err := Shared(ctx, "n", func() (string, error) { return "x", nil }); err == nil || v != "" {
		t.Errorf("another type: %q %v, want an error", v, err)
	}
}

// Each Wrote moves the run's generation on by one, and only that run's;
// outside a run there is no generation and Wrote does nothing. Planted
// breaks: Wrote not counting; Generation reporting a run outside one.
func TestWroteMovesTheGenerationOn(t *testing.T) {
	if _, ok := Generation(context.Background()); ok {
		t.Error("a generation outside a run")
	}
	Wrote(context.Background())
	run, other := WithRun(context.Background()), WithRun(context.Background())
	for want := range uint64(3) {
		if gen, ok := Generation(run); !ok || gen != want {
			t.Errorf("generation %d, %v; want %d", gen, ok, want)
		}
		Wrote(run)
	}
	if gen, _ := Generation(other); gen != 0 {
		t.Errorf("another run's generation %d, want 0", gen)
	}
}

// Once a check of the run may have written to the repository, a result
// Shared kept is computed again, since it was computed from the files
// before the write; another run keeps its own. Planted break: Wrote moving
// the generation on and keeping the results.
func TestWroteDropsWhatTheRunKept(t *testing.T) {
	run, other := WithRun(context.Background()), WithRun(context.Background())
	calls := 0
	compute := func() (int, error) { calls++; return calls, nil }
	for _, ctx := range []context.Context{run, other} {
		if n, _ := Shared(ctx, "walk", compute); n != calls {
			t.Fatalf("first compute gave %d, want %d", n, calls)
		}
	}
	Wrote(run)
	if n, _ := Shared(run, "walk", compute); n != 3 {
		t.Errorf("after a write the run gave %d, want 3, computed again", n)
	}
	if n, _ := Shared(run, "walk", compute); n != 3 {
		t.Errorf("a second ask after the write gave %d, want 3, kept", n)
	}
	if n, _ := Shared(other, "walk", compute); n != 2 {
		t.Errorf("the other run gave %d, want 2, its own", n)
	}
}
