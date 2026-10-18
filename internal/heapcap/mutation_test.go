//go:build mutation

package heapcap

import (
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

// Under the tag, init arms the watchdog at Cap: the limit is Cap, the stack
// bound is maxStack, and the watchdog's goroutine runs. Gremlins' coverage
// run runs this test, so a binary left without the watchdog fails it before
// any mutant runs.
func TestInitArmsTheWatchdogAtCap(t *testing.T) {
	if armed != Cap {
		t.Errorf("init armed the watchdog at %d bytes, want Cap, %d", armed, uint64(Cap))
	}
	if prev := debug.SetMaxStack(maxStack); prev != maxStack {
		debug.SetMaxStack(prev)
		t.Errorf("the stack bound was %d bytes, want maxStack, %d", prev, maxStack)
	}
	buf := make([]byte, 1<<20)
	if stacks := string(buf[:runtime.Stack(buf, true)]); !strings.Contains(stacks, "heapcap.arm.func1") {
		t.Errorf("no goroutine runs the watchdog:\n%s", stacks)
	}
}
