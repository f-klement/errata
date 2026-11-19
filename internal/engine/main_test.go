package engine

import (
	"os"
	"testing"
)

// TestMain points the user's cache at a directory of the run's, so a test
// that does not set its own records no duration, and makes no copy, in
// the cache of whoever runs the tests.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "engine-cache-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CACHE_HOME", dir)
	os.Setenv("HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
