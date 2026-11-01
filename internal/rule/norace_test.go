//go:build !race

package rule

// raceEnabled is whether the tests run under the race detector, which
// slows them about ten times.
const raceEnabled = false
