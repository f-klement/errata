package install

import (
	"context"
	"strings"
	"testing"
	"time"
)

// lowerTimeouts sets the timeouts of every download for one test.
func lowerTimeouts(t *testing.T, header, idle time.Duration) {
	t.Helper()
	oldHeader, oldIdle := headerTimeout, idleTimeout
	headerTimeout, idleTimeout = header, idle
	t.Cleanup(func() { headerTimeout, idleTimeout = oldHeader, oldIdle })
}

// A server that sends no answer, sends its header and then no byte, or
// stops sending its body, fails the install once the timeout passes,
// rather than holding it until a CI job's own limit; one that sends its
// body slowly but steadily is waited for, since no bound covers a whole
// download. The test gives each install 10 s at most, so a break that waits
// forever fails it. The timeouts are a second, and the slow body's pause
// 20 ms, so a loaded machine running the race detector still keeps to
// them; the slow body takes longer than the idle timeout in all. Planted
// breaks: answer starting no header timer; watchBody's timer never firing;
// Read not resetting the timer.
func TestADownloadThatStallsTimesOut(t *testing.T) {
	lowerTimeouts(t, time.Second, time.Second)
	for _, stall := range []string{"header", "silent", "body"} {
		w := newWorld(t)
		w.publish("0.1.0", []string{this}, "")
		w.stall = stall
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		err := w.in.Install(ctx, Request{Plugins: []string{"csv@0.1.0"}})
		cancel()
		if err == nil || !strings.Contains(err.Error(), "the server sent nothing for 1s") || !strings.Contains(err.Error(), "checksums.txt") {
			t.Errorf("a server that stalls on the %s: %v", stall, err)
		}
		if w.lock() != "" {
			t.Error("a lock was written")
		}
	}
	w := newWorld(t)
	w.publish("0.1.0", []string{this}, "")
	w.stall, w.pause = "slow", 20*time.Millisecond
	start := time.Now()
	w.install(Request{Plugins: []string{"csv@0.1.0"}})
	if d := time.Since(start); d < idleTimeout {
		t.Errorf("the slow body took %s, no longer than the idle timeout, so it shows nothing", d)
	}
}
