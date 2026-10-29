package protocol

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// generated is a generated check of out.md in a new root holding committed,
// whose generator is the shell script script. A nil committed leaves the
// file out.
func generated(t *testing.T, committed []byte, script string) (string, check.Check) {
	t.Helper()
	root := t.TempDir()
	if committed != nil {
		if err := os.WriteFile(filepath.Join(root, "out.md"), committed, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, check.Check{ID: "gen", Reason: "r", Scope: check.ScopeRepo, Timeout: 5 * time.Second,
		Kind: &check.Generated{File: "out.md", Run: []string{"sh", "-c", script}, MaxBytes: 1 << 16}}
}

// A file equal to what its generator prints holds; any byte that differs is
// an erratum that names the file, the line and the column where the two
// part, and quotes each side to the end of that line. Line ends count: a
// CRLF file against LF output differs at the CR, and so does a file whose
// last newline is missing. The planted breaks: the comparison trimming the
// output's last newline, which held the file without it, and the line
// counted from 0.
func TestRunGeneratedComparesByteForByte(t *testing.T) {
	cases := []struct {
		name, file, printed string
		want                check.Verdict
		line                int
		msg                 string
	}{
		{"equal", "a\nb\n", `printf 'a\nb\n'`, check.VerdictTrue, 0, "out.md is what its generator prints, 4 bytes"},
		{"both empty", "", `true`, check.VerdictTrue, 0, "out.md is what its generator prints, 0 bytes"},
		{"second line differs", "a\nb\n", `printf 'a\nc\n'`, check.VerdictFalse, 2,
			`out.md:2, column 1, differs from what its generator prints: the file has "b\n", the generator printed "c\n"`},
		{"crlf against lf", "a\r\nb\r\n", `printf 'a\nb\n'`, check.VerdictFalse, 1,
			`out.md:1, column 2, differs from what its generator prints: the file has "a\r\n", the generator printed "a\n"`},
		{"file lacks the last newline", "a\nb", `printf 'a\nb\n'`, check.VerdictFalse, 2,
			`out.md:2, column 2, differs from what its generator prints: the file has "b" and ends there, the generator printed "b\n"`},
		{"generator prints a line more", "a\n", `printf 'a\nb\n'`, check.VerdictFalse, 2,
			`out.md:2, column 1, differs from what its generator prints: the file ends there, the generator printed "b\n"`},
		{"file has a line more", "a\nb\n", `printf 'a\n'`, check.VerdictFalse, 2,
			`out.md:2, column 1, differs from what its generator prints: the file has "b\n", the generator printed nothing more`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, c := generated(t, []byte(tc.file), tc.printed)
			res := RunGenerated(context.Background(), root, c)
			if res.Verdict != tc.want || res.Message != tc.msg {
				t.Fatalf("got %s %q, want %s %q", res.Verdict, res.Message, tc.want, tc.msg)
			}
			if tc.line > 0 && res.At != (check.Location{File: "out.md", Line: tc.line}) {
				t.Errorf("at = %+v, want out.md:%d", res.At, tc.line)
			}
			if tc.line == 0 && res.At != (check.Location{}) {
				t.Errorf("a check that holds points at %+v", res.At)
			}
		})
	}
}

// A difference far into a long line shows in the quote: each side starts
// lead bytes before it, after ..., and the start moves back to the start of
// a character rather than split one. The planted breaks: from set to the
// line's start, which quoted 64 bytes of the line that are the same on both
// sides, and the loop that moves back dropped, which cut é in two.
func TestRunGeneratedQuotesAroundTheDifference(t *testing.T) {
	head := strings.Repeat("x", 100)
	root, c := generated(t, []byte(head+"A tail\n"), `printf '`+head+`B tail\n'`)
	res := RunGenerated(context.Background(), root, c)
	q := strings.Repeat("x", lead)
	want := `out.md:1, column 101, differs from what its generator prints: the file has ..."` + q + `A tail\n", the generator printed ..."` + q + `B tail\n"`
	if res.Message != want {
		t.Fatalf("message = %q\nwant      %q", res.Message, want)
	}

	// é is two bytes; with its second byte at the cut, the start moves back
	// one byte.
	head = strings.Repeat("x", 50) + "é" + strings.Repeat("y", lead-1)
	root, c = generated(t, []byte(head+"A\n"), `printf '`+head+`B\n'`)
	res = RunGenerated(context.Background(), root, c)
	if !strings.Contains(res.Message, `the file has ..."é`+strings.Repeat("y", lead-1)+`A\n"`) {
		t.Fatalf("message = %q, want the quote to start at é", res.Message)
	}
}

// A committed file that is not there is an erratum, not an unknown: the
// generator ran and printed a file the repository lacks. Nothing at a name
// below a file is no file either, as exists reads it. The result points at
// no line, so a report places it at the check. The planted break: missing
// returning false, which made each unknown.
func TestRunGeneratedMissingFileIsAnErratum(t *testing.T) {
	root, c := generated(t, nil, `printf 'abc'`)
	res := RunGenerated(context.Background(), root, c)
	want := "out.md is not in the repository, and its generator printed 3 bytes: commit what the generator prints"
	if res.Verdict != check.VerdictFalse || res.Message != want || res.At != (check.Location{}) {
		t.Fatalf("got %s %q at %+v, want false %q at no place", res.Verdict, res.Message, res.At, want)
	}

	root, c = generated(t, []byte("a file"), `printf 'abc'`)
	c.Generated().File = "out.md/below"
	if res := RunGenerated(context.Background(), root, c); res.Verdict != check.VerdictFalse {
		t.Fatalf("a name below a file: got %s %q, want false", res.Verdict, res.Message)
	}
}

// A file errata cannot read is unknown: a directory, and a file past
// max_bytes. The planted break: every read error taken as a missing file,
// which made each an erratum.
func TestRunGeneratedUnreadableFileIsUnknown(t *testing.T) {
	root, c := generated(t, nil, `printf 'abc'`)
	if err := os.Mkdir(filepath.Join(root, "out.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if res := RunGenerated(context.Background(), root, c); res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, "is a directory") {
		t.Fatalf("directory: got %s %q, want unknown naming the directory", res.Verdict, res.Message)
	}

	root, c = generated(t, []byte("abcdef"), `printf 'abc'`)
	c.Generated().MaxBytes = 4
	if res := RunGenerated(context.Background(), root, c); res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, "over the limit of 4 bytes") {
		t.Fatalf("over max_bytes: got %s %q, want unknown naming the limit", res.Verdict, res.Message)
	}
}

// A generator that does not exit 0 is unknown, whatever it printed: exit 1
// included, since only the comparison says whether the file is current, and
// a program that is not there. The message ends with its stderr. Stderr of
// a generator that exits 0 is no part of the comparison. The planted
// breaks: generatorFailed returning "" for an error, which compared the
// output of a failed generator, and stderr compared with stdout.
func TestRunGeneratedFailingGeneratorIsUnknown(t *testing.T) {
	for _, tc := range []struct{ name, script, msg string }{
		{"exit 1 with the file's output", `printf 'same'; exit 1`, "exit status 1"},
		{"exit 2 with stderr", `printf 'same'; echo cannot read the source >&2; exit 2`, "exit status 2\ncannot read the source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, c := generated(t, []byte("same"), tc.script)
			res := RunGenerated(context.Background(), root, c)
			if res.Verdict != check.VerdictUnknown || !strings.HasPrefix(res.Message, "the generator failed: ") || !strings.Contains(res.Message, tc.msg) {
				t.Fatalf("got %s %q, want unknown containing %q", res.Verdict, res.Message, tc.msg)
			}
		})
	}

	root, c := generated(t, []byte("same"), "")
	c.Generated().Run = []string{"errata-no-such-generator"}
	if res := RunGenerated(context.Background(), root, c); res.Verdict != check.VerdictUnknown || !strings.HasPrefix(res.Message, "the generator failed: ") {
		t.Fatalf("missing program: got %s %q, want unknown", res.Verdict, res.Message)
	}

	root, c = generated(t, []byte("same"), `printf 'same'; echo a warning >&2`)
	if res := RunGenerated(context.Background(), root, c); res.Verdict != check.VerdictTrue {
		t.Fatalf("stderr on exit 0: got %s %q, want true", res.Verdict, res.Message)
	}
}

// A generator that runs past its timeout is unknown, and the timeout stops
// its children too, as a program check's does. The planted break: timedOut
// left false, which read the killed generator as one that failed.
func TestRunGeneratedTimeoutIsUnknown(t *testing.T) {
	root, c := generated(t, []byte(""), `sleep 5 & wait`)
	c.Timeout = 50 * time.Millisecond
	start := time.Now()
	res := RunGenerated(context.Background(), root, c)
	if res.Verdict != check.VerdictUnknown || res.Message != "the generator did not finish within 50ms" {
		t.Fatalf("got %s %q, want unknown with the timeout", res.Verdict, res.Message)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a 50ms timeout took %s to return", took)
	}
}

// max_bytes bounds the generator's stdout: output of exactly max_bytes is
// compared, and a byte more stops the generator and its children at once
// and is unknown, not an erratum, though it cannot equal a file within the
// bound. Stderr has a bound of its own of the same size. The planted
// breaks: Write comparing with >= rather than >, which refused output of
// exactly the bound; bounded never calling stop, which left the sleep
// after the output running until it ended; and stderr made with no limit.
func TestRunGeneratedBoundsTheOutput(t *testing.T) {
	root, c := generated(t, []byte("abcd"), `printf 'abcd'`)
	c.Generated().MaxBytes = 4
	if res := RunGenerated(context.Background(), root, c); res.Verdict != check.VerdictTrue {
		t.Fatalf("output of max_bytes: got %s %q, want true", res.Verdict, res.Message)
	}

	root, c = generated(t, []byte("abcd"), `printf 'abcde'`)
	c.Generated().MaxBytes = 4
	want := "the generator wrote more than 4 bytes to stdout, generated.max_bytes, so errata stopped it; raise max_bytes if the file is that large"
	if res := RunGenerated(context.Background(), root, c); res.Verdict != check.VerdictUnknown || res.Message != want {
		t.Fatalf("a byte over: got %s %q, want unknown %q", res.Verdict, res.Message, want)
	}

	for _, stream := range []string{"stdout", "stderr"} {
		redirect := ""
		if stream == "stderr" {
			redirect = " >&2"
		}
		// The sleep, a child of the shell, holds the run unless the
		// byte past the bound kills the process group.
		root, c = generated(t, []byte("y\n"), `head -c 70000 /dev/zero`+redirect+`; sleep 10 & wait`)
		c.Timeout = 30 * time.Second
		start := time.Now()
		res := RunGenerated(context.Background(), root, c)
		if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, "more than 65536 bytes to "+stream) {
			t.Fatalf("70000 bytes to %s: got %s %q, want unknown naming %s", stream, res.Verdict, res.Message, stream)
		}
		if took := time.Since(start); took > 5*time.Second {
			t.Fatalf("the generator ran %s past its bound on %s", took, stream)
		}
	}
}

// The generator gets no stdin: one that reads it, such as cat, reads an
// empty file at once and prints nothing. It runs from the root with the
// environment of a program check. The planted break: Stdin set to a
// reader of one byte, which cat printed.
func TestRunGeneratedGivesNoStdinAndTheProgramEnvironment(t *testing.T) {
	root, c := generated(t, []byte(""), "")
	c.Generated().Run = []string{"cat"}
	c.Timeout = 2 * time.Second
	if res := RunGenerated(context.Background(), root, c); res.Verdict != check.VerdictTrue {
		t.Fatalf("cat: got %s %q, want true on an empty stdin", res.Verdict, res.Message)
	}

	root, c = generated(t, nil, `printf '%s %s %s %s' "$PWD" "$ERRATA_ROOT" "$ERRATA_CHECK_ID" "$ERRATA_SCOPE"`)
	if err := os.WriteFile(filepath.Join(root, "out.md"), []byte(root+" "+root+" gen repo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := RunGenerated(context.Background(), root, c); res.Verdict != check.VerdictTrue {
		t.Fatalf("environment: got %s %q", res.Verdict, res.Message)
	}
}

// errata reads the committed file before the generator starts, so a
// generator that writes the file and prints it is compared with the file
// as committed: the review's probe, a stale old\n and a generator that
// writes new\n and cats it, is an erratum. One that creates a missing file
// does not make it present. The planted break: the file read again after
// the generator ran, as before the fix, which held both.
func TestRunGeneratedReadsTheFileBeforeTheGenerator(t *testing.T) {
	root, c := generated(t, []byte("old\n"), `printf 'new\n' > out.md; cat out.md`)
	res := RunGenerated(context.Background(), root, c)
	want := `out.md:1, column 1, differs from what its generator prints: the file has "old\n", the generator printed "new\n"`
	if res.Verdict != check.VerdictFalse || res.Message != want {
		t.Fatalf("a generator that rewrites its file: got %s %q, want false %q", res.Verdict, res.Message, want)
	}

	root, c = generated(t, nil, `printf 'new\n' > out.md; cat out.md`)
	if res := RunGenerated(context.Background(), root, c); res.Verdict != check.VerdictFalse || !strings.Contains(res.Message, "is not in the repository") {
		t.Fatalf("a generator that creates its file: got %s %q, want false, not in the repository", res.Verdict, res.Message)
	}
}

// The quote's start moves back at most UTFMax-1 bytes to the start of a
// character, so a long run of bytes that start none, \x80 here, still
// leaves the difference in the quote. The planted break: the cap dropped,
// which walked to the line's start and quoted 64 bytes both sides share.
func TestRunGeneratedQuotesInvalidUTF8AroundTheDifference(t *testing.T) {
	run := strings.Repeat("\x80", 200)
	root, c := generated(t, []byte(run+"A\n"), "cat printed")
	if err := os.WriteFile(filepath.Join(root, "printed"), []byte(run+"B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := RunGenerated(context.Background(), root, c)
	if res.Verdict != check.VerdictFalse || !strings.Contains(res.Message, `\x80A\n"`) || !strings.Contains(res.Message, `\x80B\n"`) {
		t.Fatalf("got %s %q, want both sides quoted up to A and B", res.Verdict, res.Message)
	}
}

// A failing generator's stderr is cut to its last 256 bytes, so one long
// line does not reach every report whole, and the reason at its end stays.
// The planted breaks: the tail added uncut, 5000 bytes of it; the tail cut
// from its start.
func TestRunGeneratedClipsTheStderrOfAFailingGenerator(t *testing.T) {
	root, c := generated(t, []byte("x"), `head -c 5000 /dev/zero | tr '\0' e >&2; echo ' cannot read the source' >&2; exit 2`)
	res := RunGenerated(context.Background(), root, c)
	if res.Verdict != check.VerdictUnknown || !strings.HasPrefix(res.Message, "the generator failed: exit status 2\n...eee") ||
		!strings.HasSuffix(res.Message, "eee cannot read the source") || len(res.Message) > 400 {
		t.Fatalf("got %s, %d bytes: %.80q", res.Verdict, len(res.Message), res.Message)
	}
}
