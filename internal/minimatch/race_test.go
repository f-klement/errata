//go:build race

package minimatch

// raceEnabled is whether the tests run under the race detector, which
// slows them about ten times.
const raceEnabled = true
