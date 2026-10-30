package protocol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/program"
)

// RunGenerated reads the committed file of c, a generated check, then runs
// its generator and compares what it prints on stdout with the file, byte
// for byte. The
// generator starts as a program check's run does (run): argv, never a
// shell, with the same timeout, environment and working directory, and
// stdin empty. errata compares its output and never runs it (ADR 0011).
//
// The check holds when the two are equal, line ends and the last newline
// included. It is an erratum when they differ, with the first line and
// column where they do, and when the file is not in the repository, since
// the generator printed a file the repository lacks. It is unknown when
// the generator exits with any status but 0, does not finish within the
// timeout or writes more than generated.max_bytes to stdout or to stderr,
// and when the file cannot be read: past max_bytes, a link that leads out
// of the repository, a directory. An exit status of 1 is unknown too: the
// generator's exit status says whether it ran, and only the comparison
// says whether the file is current.
//
// On a witness root is the fixture: the file is read from it, and the
// generator starts in the repository's root with ERRATA_ROOT at the
// fixture, as a program check's run does (ADR 0015).
func RunGenerated(ctx context.Context, root string, c check.Check) (res check.Result) {
	start := time.Now()
	res.Check = c
	defer func() { res.Duration = time.Since(start) }()
	g := c.Generated()

	// The file is read before the generator starts, so a generator that
	// writes the file and prints it is compared with the file as
	// committed, not with what it just wrote.
	src := core.Source{File: g.File, Limit: g.MaxBytes}
	data, err := src.Read(root)
	gone := err != nil && missing(src, root)

	p := run(ctx, root, c, g.Run, g.MaxBytes)
	if msg := generatorFailed(p, c); msg != "" {
		res.Verdict, res.Message = check.VerdictUnknown, msg
		return res
	}
	if gone {
		res.Verdict = check.VerdictFalse
		res.Message = fmt.Sprintf("%s is not in the repository, and its generator printed %d bytes: commit what the generator prints", g.File, len(p.Stdout))
		return res
	}
	if err != nil {
		res.Verdict, res.Message = check.VerdictUnknown, err.Error()
		return res
	}
	if bytes.Equal(data, p.Stdout) {
		res.Verdict = check.VerdictTrue
		res.Message = fmt.Sprintf("%s is what its generator prints, %d bytes", g.File, len(data))
		return res
	}
	res.Verdict = check.VerdictFalse
	res.Message, res.At = difference(g.File, data, p.Stdout)
	return res
}

// generatorFailed is the message of a generator that ended without an
// output to compare, or "" for one that exited 0 within its bounds.
func generatorFailed(p program.Ended, c check.Check) string {
	if p.OverBound != "" {
		return fmt.Sprintf("the generator wrote more than %d bytes to %s, generated.max_bytes, so errata stopped it; raise max_bytes if the file is that large", c.Generated().MaxBytes, p.OverBound)
	}
	if p.TimedOut {
		return fmt.Sprintf("the generator did not finish within %s", c.Timeout)
	}
	if p.Err != nil {
		// The tail is cut to as many bytes as a library's error, or up to
		// max_bytes of one line would reach every report. It keeps the end,
		// as tail keeps the last lines: a failing program says why last.
		said := lastBytes(tail(string(p.Stderr)), saidBytes)
		return strings.TrimSpace("the generator failed: " + p.Err.Error() + "\n" + said)
	}
	return ""
}

// saidBytes is how much of a failing generator's stderr a message keeps,
// the length core.ClipError cuts a library's error to.
const saidBytes = 256

// lastBytes is the last n bytes of s, after "..." when it cut any, starting
// at a whole rune.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return "..." + s[start:]
}

// missing reports whether nothing is at src's file, as exists reads it: no
// file at the name, or a name on the way to it that is a file and no
// directory.
func missing(src core.Source, root string) bool {
	_, err := src.Stat(root)
	return errors.Is(err, fs.ErrNotExist)
}

// lead is how many bytes before the first difference a message quotes, so
// the difference shows within the quote however long its line is.
const lead = 24

// difference is the message for a file whose bytes, file, differ from what
// its generator printed, out, and the place it names: the line, counted
// from 1, and the column, a byte counted from 1, of the first byte where
// they part. Each side is quoted from up to lead bytes before that byte to
// the end of its line, its newline included, so a CR before the newline or
// a missing last newline shows.
func difference(name string, file, out []byte) (string, check.Location) {
	i := 0
	for i < len(file) && i < len(out) && file[i] == out[i] {
		i++
	}
	lineStart := bytes.LastIndexByte(file[:i], '\n') + 1
	line := 1 + bytes.Count(file[:i], []byte("\n"))
	from := max(lineStart, i-lead)
	// The bytes before i are the same on both sides, so one start serves
	// both, moved back to the start of a character: at most UTFMax-1
	// bytes, as core.Clip cuts, so a run of bytes that start no character
	// does not move the quote away from the difference.
	for back := 0; back < utf8.UTFMax-1 && from > lineStart && !utf8.RuneStart(file[from]); back++ {
		from--
	}
	at := check.Location{File: name, Line: line}
	msg := fmt.Sprintf("%s, column %d, differs from what its generator prints: %s, %s",
		at, i-lineStart+1,
		side("the file has", "the file ends there", file, from, i, lineStart),
		side("the generator printed", "the generator printed nothing more", out, from, i, lineStart))
	return msg, at
}

// side is what one side holds from from to the end of its line, quoted
// after has, or ended when it holds nothing there. A quote that starts
// after the line does begins with ..., and one of a side that ends at i,
// the first difference, says so.
func side(has, ended string, data []byte, from, i, lineStart int) string {
	end := len(data)
	if nl := bytes.IndexByte(data[i:], '\n'); nl >= 0 {
		end = i + nl + 1
	}
	if from == end {
		return ended
	}
	q := core.Quote(string(data[from:end]))
	if from > lineStart {
		q = "..." + q
	}
	if i == len(data) {
		q += " and ends there"
	}
	return has + " " + q
}
