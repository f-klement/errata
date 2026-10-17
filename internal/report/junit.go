package report

import (
	"encoding/xml"
	"io"

	"github.com/f-klement/errata/internal/check"
)

// JUnit writes JUnit XML, which GitLab, GitHub Actions and Jenkins all render
// per check without a plugin. An erratum is a <failure>, an unknown is an
// <error> (JUnit's word for a test that could not run to a result), and a
// skipped check is <skipped> with its gap, so the CI view shows the coverage.
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
}

type junitProblem struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

func (JUnit) Write(w io.Writer, r check.Report) error {
	s := junitSuite{Name: "errata", Tests: len(r.Results)}
	for _, res := range r.Results {
		c := junitCase{
			Name:      res.Check.ID,
			Classname: "errata." + string(res.Check.Scope),
			Time:      res.Duration.Seconds(),
		}
		problem := &junitProblem{Message: res.Message, Body: res.Check.Reason}
		switch {
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
		s.Cases = append(s.Cases, c)
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(s); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}
