//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/treecopy"
)

// TestMain runs errata itself when a test starts this binary as errata.
func TestMain(m *testing.M) {
	if os.Getenv("ERRATA_TEST_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// config is two checks that each run long enough to be stopped, so they
// run at once, each in a copy.
const config = `checks:
  - id: one
    reason: r
    scope: repo
    run: [sleep, "30"]
  - id: two
    reason: r
    scope: repo
    run: [sleep, "30"]
`

// A run stopped by SIGTERM or SIGHUP, as CI and a closed terminal stop
// one, removes its copies as an interrupt does. Planted break: main that
// stopped on an interrupt only, where the copies stayed in the cache.
func TestAStoppedRunRemovesItsCopies(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			cache, root := t.TempDir(), t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			if err := os.WriteFile(filepath.Join(root, "errata.yaml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "c"}} {
				git := exec.CommandContext(t.Context(), "git", args...)
				git.Dir = root
				if out, err := git.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			home, err := treecopy.Home(root)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "check")
			cmd.Dir = root
			// Two workers whatever the node's quota, which on a node of one
			// CPU would leave a pool of one, with no copy.
			cmd.Env = append(os.Environ(), "ERRATA_TEST_MAIN=1", "GOMAXPROCS=2")
			var out strings.Builder
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if !waitFor(func() bool { return len(copies(home)) == 2 }) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("errata made %v, not two copies; it printed:\n%s", copies(home), out.String())
			}
			time.Sleep(100 * time.Millisecond)
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			if left := copies(home); len(left) != 0 {
				t.Errorf("a run stopped by %s left %v", sig, left)
			}
		})
	}
}

// copies is the directories in home.
func copies(home string) []string {
	entries, _ := os.ReadDir(home)
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
}

// waitFor reports whether ok holds within 20 seconds.
func waitFor(ok func() bool) bool {
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return true
		}
	}
	return false
}
