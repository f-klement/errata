// Package report renders a check.Report. Each format is one Reporter; the
// engine never knows which one runs.
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/f-klement/errata/internal/check"
)

// Reporter writes a report in one output format.
type Reporter interface {
	Write(w io.Writer, r check.Report) error
}

// Formats lists the reporters by the name the --format flag accepts.
var Formats = map[string]Reporter{
	"text":  Text{},
	"junit": JUnit{},
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
	_, err := io.WriteString(w, b.String())
	return err
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
