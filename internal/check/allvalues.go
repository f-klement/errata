package check

import "context"

type allValuesKey struct{}

// WithAllValues is ctx for a run whose false asserts print every value the
// rule read. Without it, an assert whose value is an all over an
// extractor's values prints only the members the all was false for, and
// counts the rest. errata check sets it with --all-values.
func WithAllValues(ctx context.Context) context.Context {
	return context.WithValue(ctx, allValuesKey{}, true)
}

// AllValues reports whether ctx is a run under WithAllValues.
func AllValues(ctx context.Context) bool {
	every, _ := ctx.Value(allValuesKey{}).(bool)
	return every
}
