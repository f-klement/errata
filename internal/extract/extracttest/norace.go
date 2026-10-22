//go:build !race

package extracttest

// RaceEnabled is whether the tests run under the race detector, which slows
// them about ten times.
const RaceEnabled = false
