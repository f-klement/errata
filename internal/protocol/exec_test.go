package protocol

import (
	"context"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
)

func sh(script string) check.Check {
	return check.Check{ID: "c", Reason: "r", Scope: check.ScopeRepo, Run: []string{"sh", "-c", script}, Timeout: 5 * time.Second}
}

func TestRunVerdicts(t *testing.T) {
	cases := []struct {
		name, script string
		want         check.Verdict
		wantMsg      string
	}{
		{"exit 0 is true", "echo fine", check.VerdictTrue, "fine"},
		{"exit 1 is false", "echo 'node atlas is gone'; exit 1", check.VerdictFalse, "node atlas is gone"},
		{"exit 2 is error", "echo boom >&2; exit 2", check.VerdictUnknown, "boom"},
		{"json message", `echo '{"message":"from json","extra":1}'; exit 1`, check.VerdictFalse, "from json"},
		{"json without message is text", `echo '{"other":1}'`, check.VerdictTrue, `{"other":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Run(context.Background(), t.TempDir(), sh(tc.script))
			if res.Verdict != tc.want {
				t.Fatalf("verdict = %s, want %s (message %q)", res.Verdict, tc.want, res.Message)
			}
			if tc.want != check.VerdictUnknown && res.Message != tc.wantMsg {
				t.Errorf("message = %q, want %q", res.Message, tc.wantMsg)
			}
			if !strings.Contains(res.Message, tc.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", res.Message, tc.wantMsg)
			}
		})
	}
}

func TestRunEnvironmentAndDir(t *testing.T) {
	root := t.TempDir()
	c := sh(`[ "$PWD" = "$ERRATA_ROOT" ] && [ "$ERRATA_CHECK_ID" = c ] && [ "$ERRATA_SCOPE" = repo ]`)
	if res := Run(context.Background(), root, c); res.Verdict != check.VerdictTrue {
		t.Fatalf("environment not as documented: %+v", res)
	}
}

func TestRunTimeout(t *testing.T) {
	c := sh("sleep 5")
	c.Timeout = 50 * time.Millisecond
	start := time.Now()
	res := Run(context.Background(), t.TempDir(), c)
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, "did not finish") {
		t.Fatalf("got %+v, want a timeout error", res)
	}
	// The timeout bounds the check, children included: the shell's `sleep`
	// must not hold the run open until it exits on its own.
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a 50ms timeout took %s to return", took)
	}
}

func TestRunZeroTimeoutMeansNoLimit(t *testing.T) {
	c := sh("sleep 0.05")
	c.Timeout = 0
	if res := Run(context.Background(), t.TempDir(), c); res.Verdict != check.VerdictTrue {
		t.Fatalf("zero timeout cut the check short: %+v", res)
	}
}

func TestRunMissingProgram(t *testing.T) {
	c := sh("")
	c.Run = []string{"errata-no-such-program"}
	if res := Run(context.Background(), t.TempDir(), c); res.Verdict != check.VerdictUnknown {
		t.Fatalf("verdict = %s, want error", res.Verdict)
	}
}

func TestRunRecordsDuration(t *testing.T) {
	res := Run(context.Background(), t.TempDir(), sh("sleep 0.01"))
	if res.Duration < 10*time.Millisecond {
		t.Fatalf("duration = %s, want at least 10ms", res.Duration)
	}
	if res.Check.ID != "c" {
		t.Fatalf("result lost its check: %+v", res.Check)
	}
}

func TestTail(t *testing.T) {
	in := "1\n2\n3\n4\n5\n6\n7\n"
	if got := tail(in); got != "3\n4\n5\n6\n7" {
		t.Fatalf("tail = %q", got)
	}
}

// Plain text that is not a JSON object comes back trimmed and otherwise intact.
func TestPropertyPlainMessageRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.StringMatching(`[a-zA-Z0-9 .:/]{0,60}`).Draw(t, "text")
		if got, want := message([]byte(s)), strings.TrimSpace(s); got != want {
			t.Fatalf("message(%q) = %q, want %q", s, got, want)
		}
	})
}

func FuzzMessage(f *testing.F) {
	f.Add([]byte(`{"message":"x"}`))
	f.Add([]byte("plain"))
	f.Add([]byte(`{"message":null}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		got := message(b)
		if got != strings.TrimSpace(got) {
			t.Fatalf("message not trimmed: %q", got)
		}
	})
}
