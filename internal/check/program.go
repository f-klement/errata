package check

import "errors"

// Program is what makes a check a program check: the program it runs
// through the check protocol, whose exit code is the verdict.
type Program struct {
	// Run is the command: argv, not a shell string, so a path with spaces
	// needs no quoting.
	Run []string
}

// Name is program.
func (*Program) Name() string { return "program" }

// Nouns are program check and program checks.
func (*Program) Nouns() (one, many string) { return "program check", "program checks" }

// ScopeRefusal is "": a program check reads what its program reads, so it
// takes every scope.
func (*Program) ScopeRefusal(Scope) string { return "" }

// errEmptyRun refuses a program check whose run lists no argument, so it
// has no command to run.
var errEmptyRun = errors.New("run lists no argument: write the command, or remove the key, which takes the default run if the file sets one")

// Validate refuses a program check with no command.
func (p *Program) Validate(Check) []error {
	if len(p.Run) == 0 {
		return []error{errEmptyRun}
	}
	return nil
}

// ListFields are run and timeout.
func (p *Program) ListFields(c Check) Fields {
	return Fields{{"run", OrEmpty(p.Run)}, {"timeout", c.Timeout.String()}}
}

// ListLines are run, each argument written by q, and timeout.
func (p *Program) ListLines(c Check, q Quoter) []Line {
	return []Line{{"run:", quoted(q, p.Run, " ")}, {"timeout:", c.Timeout.String()}}
}
