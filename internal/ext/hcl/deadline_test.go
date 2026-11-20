package hcl

import (
	"bytes"
	"encoding/json"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/plugin/wire"
)

// An extract request whose files are still being read at the plugin's
// deadline is answered with an error reply naming the file and the
// deadline, and Main exits 0, so errata makes the rule unknown with the
// plugin's message and reports no crash and no timeout of its own. The
// deadline is set to a nanosecond here, which any parse passes. Planted
// breaks: extract waiting on no deadline; the deadline fixed at 20 s.
func TestAnExtractPastTheDeadlineIsAnErrorReply(t *testing.T) {
	was := deadline
	deadline = time.Nanosecond
	t.Cleanup(func() { deadline = was })
	spec := buildOne(t, `{"path": "a"}`).Spec
	req, err := json.Marshal(wire.ExtractRequest{Protocol: 1, Op: wire.OpExtract, Spec: spec, Files: []wire.File{file("f.tf", strings.Repeat("a = 1\n", 20000))}, Values: map[string][]wire.Value{}, MaxBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Main(bytes.NewReader(req), &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	var reply wire.ExtractReply
	if err := wire.Decode(stdout.Bytes(), &reply); err != nil {
		t.Fatalf("errata refuses the extract reply %s: %v", stdout.Bytes(), err)
	}
	const want = "error: f.tf: not read within 1ns, the plugin's own deadline, which it keeps below errata's 30 s so it can still answer; parsing takes longer on a slower or busier machine, so a file near this deadline can pass it on one machine and not on another"
	if got := shown(reply); len(got) != 1 || got[0] != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The plugin's deadline is 20 s by default, below wire.ExtractTimeout,
// errata's wait for an extract reply, so a slow file is refused with the
// plugin's message before errata kills the plugin. The 10 s between
// them cover the plugin's start, the request's decoding and the reply.
// internal/plugin's TestTheTimeoutsAreTheProtocols pins the host's side
// at 30 s. Planted breaks: the default raised to 40 s; to 19 s.
func TestTheDeadlineIsTwentySecondsBelowErratasTimeout(t *testing.T) {
	if deadline != 20*time.Second || deadline >= wire.ExtractTimeout {
		t.Fatalf("deadline %v, errata's extract timeout %v", deadline, wire.ExtractTimeout)
	}
}

// within returns at its deadline while the read goes on, and the read's
// goroutine ends once the read returns, though nothing waits for it, so
// a refused read never keeps the plugin running. The read here blocks
// until the test lets it go; the minute is how long the test waits for
// a step that takes microseconds. Planted breaks: the result channel
// unbuffered, so the goroutine waits forever to hand its result over;
// within waiting on the read alone.
func TestAReadPastTheDeadlineEndsWithoutAWaiter(t *testing.T) {
	expired := make(chan time.Time)
	close(expired)
	release := make(chan struct{})
	got := make(chan error, 1)
	go func() {
		_, err := within(expired, "f.tf", func() ([]wire.Value, error) { <-release; return nil, nil })
		got <- err
	}()
	select {
	case err := <-got:
		if err == nil || !strings.HasPrefix(err.Error(), "f.tf: not read within ") {
			t.Fatalf("within returned %v", err)
		}
	case <-time.After(time.Minute):
		close(release)
		t.Fatal("within still waits on the read a minute past its deadline")
	}
	close(release)
	for end := time.Now().Add(time.Minute); readsRunning(); time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the read's goroutine still runs a minute after the read returned")
		}
	}
}

// readsRunning reports whether a goroutine within started is still
// running.
func readsRunning() bool {
	var b strings.Builder
	if err := pprof.Lookup("goroutine").WriteTo(&b, 2); err != nil {
		panic(err)
	}
	return strings.Contains(b.String(), "hcl.within.func")
}
