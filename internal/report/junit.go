package report

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/f-klement/errata/internal/check"
)

// JUnit writes JUnit XML, which GitLab, GitHub Actions and Jenkins all render
// per check without a plugin. An erratum is a <failure>, an unknown is an
// <error> (JUnit's word for a test that could not run to a result), and a
// skipped check is <skipped> with its gap, so the CI view shows the coverage.
// When the report's skips fail the run, a skipped check is an <error> whose
// message is "skipped: " and its gap, so the suite's counts fail where the
// exit code does.
// A rule that left not literal values out names each in <system-out>,
// whatever its verdict. A witness is a testcase of its own, id [witness
// name], which fails when the check held on it.
type JUnit struct{}

type junitSuite struct {
	XMLName  xml.Name    `xml:"testsuite"`
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Errors   int         `xml:"errors,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      float64       `xml:"time,attr"`
	Failure   *junitProblem `xml:"failure,omitempty"`
	Error     *junitProblem `xml:"error,omitempty"`
	Skipped   *junitProblem `xml:"skipped,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitProblem struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

func (JUnit) Write(w io.Writer, r check.Report) error {
	s := junitSuite{Name: "errata", Tests: len(r.Results)}
	for _, res := range r.Results {
		c := junitCase{
			Name:      res.Name(),
			Classname: "errata." + string(res.Check.Scope),
			Time:      res.Duration.Seconds(),
		}
		problem := &junitProblem{Message: res.Message, Body: res.Check.Reason}
		switch {
		case res.Skipped() && r.SkipsFail:
			s.Errors++
			c.Error = &junitProblem{Message: "skipped: " + res.Gap, Body: res.Check.Reason}
		case res.Skipped():
			s.Skipped++
			c.Skipped = &junitProblem{Message: res.Gap}
		case res.Verdict == check.VerdictFalse:
			s.Failures++
			c.Failure = problem
		case res.Verdict == check.VerdictUnknown:
			s.Errors++
			c.Error = problem
		}
		if left := leftOut(res); len(left) > 0 {
			lines := []string{"skip_not_literal left out these values, which are not literal:"}
			for _, f := range left {
				lines = append(lines, "  "+where(f))
			}
			c.SystemOut = strings.Join(lines, "\n")
		}
		s.Cases = append(s.Cases, c)
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(s); err != nil {
		return err
	}
	buf.WriteString("\n")
	// encoding/xml writes C1 controls and direction controls as they are,
	// and the file goes to stdout, so they are written as character
	// references, which XML 1.0 allows for each of them.
	_, err := w.Write(escapeActedOn(buf.Bytes(), func(r rune) string { return fmt.Sprintf("&#x%X;", r) }))
	return err
}
