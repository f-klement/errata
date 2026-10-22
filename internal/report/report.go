// Package report renders a check.Report. Each format is one Reporter; the
// engine never knows which one runs.
package report

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
)

// Reporter writes a report in one output format.
type Reporter interface {
	Write(w io.Writer, r check.Report) error
}

// Formats lists the reporters by the name the --format flag accepts. Each
// one is made with errata's version, which a format that names the tool that
// wrote it records; the reporter does not ask the CLI for it.
var Formats = map[string]func(version string) Reporter{
	"text":  func(string) Reporter { return Text{} },
	"junit": func(string) Reporter { return JUnit{} },
	"sarif": func(v string) Reporter { return SARIF{Version: v} },
}

// FormatNames returns the accepted --format values in a stable order.
func FormatNames() []string {
	names := make([]string, 0, len(Formats))
	for n := range Formats {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Text is the human format. It prints each erratum and each unknown with its
// reason, then the counts and the coverage line, which it prints on every run.
// Under the coverage line it names each skipped check with its gap, and each
// value that skip_not_literal left out, whatever the check's verdict: a rule
// that holds held on the values that were left.
// A reason comes from the config file and a message from a repository file or
// a program's output, so the output goes to the terminal through
// QuoteControls: no file can move the cursor, rewrite a line, reorder one or
// set a link.
type Text struct{}

func (Text) Write(w io.Writer, r check.Report) error {
	var b strings.Builder
	for _, res := range r.Results {
		if !res.Failed() {
			continue
		}
		fmt.Fprintf(&b, "%s %s (%s)\n", strings.ToUpper(string(res.Verdict)), res.Check.ID, res.Check.Scope)
		writeIndented(&b, "why:  ", res.Check.Reason)
		if res.Message != "" {
			writeIndented(&b, "said: ", res.Message)
		}
		b.WriteString("\n")
	}

	ran, skipped := r.Coverage()
	fmt.Fprintf(&b, "%s, %d unknown. Coverage: ran %d, skipped %d.\n", errataCount(len(r.Errata())), len(r.Unknowns()), ran, skipped)
	for _, res := range r.Results {
		if res.Skipped() {
			fmt.Fprintf(&b, "  skipped %s (%s): %s\n", res.Check.ID, res.Check.Scope, res.Gap)
		}
	}
	for _, res := range r.Results {
		for _, f := range leftOut(res) {
			fmt.Fprintf(&b, "  left out of %s (%s), not literal: %s\n", res.Check.ID, res.Check.Scope, where(f))
		}
	}
	_, err := io.WriteString(w, QuoteControls(b.String()))
	return err
}

// QuoteControls writes every rune of s that a terminal acts on instead of
// showing, but a newline and a tab, and every byte that is not UTF-8, as the
// Go escape that names it: ESC as \x1b, CR as \r, NUL as \x00, CSI as \u009b,
// a right to left override as \u202e, a stray byte as \xff. Those runes are
// the control characters (C0, DEL and C1), the direction controls (U+061C,
// U+200E, U+200F, U+202A to U+202E and U+2066 to U+2069), and the line and
// paragraph separators U+2028 and U+2029. Raw, a file could hide a line,
// fake a verdict, open a link, or reorder a line so that it reads as
// something else. A CR before a newline is dropped, since a Windows line end
// shows nothing. A backslash is left as it is, so the escape and a file that
// spells it out read alike; the point is that neither reaches the terminal
// as a control.
func QuoteControls(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[0])
		case r == '\n' || r == '\t' || !actedOn(r):
			b.WriteString(s[:size])
		default:
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		}
		s = s[size:]
	}
	return b.String()
}

// actedOn reports whether a terminal, or a viewer such as a web log, acts on
// r rather than showing it: a control character, a direction control, or a
// line or paragraph separator.
func actedOn(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || unicode.In(r, unicode.Zl, unicode.Zp)
}

// escapeActedOn rewrites each rune of b that a terminal acts on and that
// encoding/json and encoding/xml write raw, DEL and every rune above it that
// actedOn names, with esc. Both encoders escape the C0 controls already and
// write valid UTF-8, and every byte outside a string or text is ASCII, so
// such a rune is always inside one, where an escape is allowed.
func escapeActedOn(b []byte, esc func(r rune) string) []byte {
	var out []byte
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r >= 0x7f && actedOn(r) {
			if out == nil {
				out = append(make([]byte, 0, len(b)+16), b[:i]...)
			}
			out = append(out, esc(r)...)
		} else if out != nil {
			out = append(out, b[i:i+size]...)
		}
		i += size
	}
	if out == nil {
		return b
	}
	return out
}

func writeIndented(b *strings.Builder, label, text string) {
	pad := strings.Repeat(" ", len(label))
	for i, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if i == 0 {
			fmt.Fprintf(b, "  %s%s\n", label, line)
		} else {
			fmt.Fprintf(b, "  %s%s\n", pad, line)
		}
	}
}

// errataCount says "1 erratum" and "2 errata", as GLOSSARY.md does.
func errataCount(n int) string {
	if n == 1 {
		return "1 erratum"
	}
	return fmt.Sprintf("%d errata", n)
}

// leftOut lists the values skip_not_literal left out of res's comparison,
// each with the extractor that returned it, in the order the rule ran them.
func leftOut(res check.Result) []named {
	var out []named
	for _, x := range res.Extracted {
		for _, v := range x.LeftOut {
			out = append(out, named{value: v, extractor: x.Extractor})
		}
	}
	return out
}

// quoteValue is a value's text quoted as a rule's message quotes it, cut at
// valueLimit characters with a remark giving its length.
func quoteValue(text string) string {
	if head, cut := prefix(text, valueLimit); cut {
		return fmt.Sprintf("%q (cut: the value is %d bytes)", head, len(text))
	}
	return strconv.Quote(text)
}

// prefix is the first n characters of s, and whether that leaves any out. It
// reads only those characters, so a long s costs what its prefix costs. A
// byte that is not UTF-8 counts as one character, as it does in []rune(s).
func prefix(s string, n int) (string, bool) {
	end := 0
	for range n {
		if end == len(s) {
			return s, false
		}
		_, size := utf8.DecodeRuneInString(s[end:])
		end += size
	}
	return s[:end], end < len(s)
}

// where is a value quoted, with its file and line and its extractor, as a
// rule's message names it.
func where(f named) string {
	return fmt.Sprintf("%s at %s:%d (%s)", quoteValue(f.value.Text), f.value.At.File, f.value.At.Line, f.extractor)
}
