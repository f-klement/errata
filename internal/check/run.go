package check

import (
	"context"
	"errors"
	"sync"
)

// errNoResult is the error of a result whose compute did not return, as
// when it panicked and a caller recovered, or returned no value of the type
// asked for: a later caller gets it rather than a zero value, which would
// read as an empty result.
var errNoResult = errors.New("a result shared in this run was not computed: its computation did not return")

// runKey is the context key of a run's shared results.
type runKey struct{}

// run is what the checks of one run share: each result a reader computed
// once, by its key.
type run struct {
	mu   sync.Mutex
	done map[any]*shared
}

// shared is one result of a run, computed once.
type shared struct {
	once  sync.Once
	value any
	err   error
}

// WithRun is ctx for one run of a set of checks: Shared computes a result
// once in it, however many checks ask for it. The engine starts each run
// with it, so a result is never carried from one run to the next.
func WithRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, runKey{}, &run{done: map[any]*shared{}})
}

// Shared is the result of compute under key in the run ctx holds, computed
// the first time any check of the run asks for key and handed to every
// later one, its error too. key is a comparable value that names what
// compute reads, such as a file and the root it is under. A compute that
// panics leaves an error for every later caller, never a zero value, and so
// does a key asked for under two types. Outside a run, compute runs each
// time. T must be a concrete type, as each use's is: with an interface T, a
// compute that returns a nil interface value and no error stores nil, which
// the type check below reads as no result, errNoResult (markers delta
// review 2, E2).
func Shared[T any](ctx context.Context, key any, compute func() (T, error)) (T, error) {
	r, ok := ctx.Value(runKey{}).(*run)
	if !ok {
		return compute()
	}
	r.mu.Lock()
	s, ok := r.done[key]
	if !ok {
		s = &shared{}
		r.done[key] = s
	}
	r.mu.Unlock()
	s.once.Do(func() { s.value, s.err = compute() })
	// A compute that panicked left no value and no error.
	v, ok := s.value.(T)
	if !ok && s.err == nil {
		return v, errNoResult
	}
	return v, s.err
}
