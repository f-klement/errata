//go:build !race

package hcl

// raceEnabled is whether the tests run under the race detector, which
// multiplies what a test holds in memory several times.
const raceEnabled = false
