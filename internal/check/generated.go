package check

import (
	"errors"
	"fmt"
	"strconv"
)

// Generated is what makes a check a generated check: a file the repository
// commits and the generator that prints it. The check runs the generator
// and holds when its stdout equals the file byte for byte. errata compares
// the output and never runs it (ADR 0011).
type Generated struct {
	// File is the committed file, relative to the repository root.
	File string
	// Run is the generator: argv, never a shell string, run as a program
	// check's run is.
	Run []string
	// MaxBytes is the most bytes errata reads of File and holds of the
	// generator's stdout, and of its stderr apart. Past it on any of the
	// three the check is unknown. The loader sets it; it is never 0.
	MaxBytes int64
}

// Name is generated.
func (*Generated) Name() string { return "generated" }

// Nouns are generated check and generated checks.
func (*Generated) Nouns() (one, many string) { return "generated check", "generated checks" }

// ScopeRefusal refuses every scope but repo: the check compares a file of
// the repository with what its generator prints from the repository, so it
// needs no provider.
func (*Generated) ScopeRefusal(s Scope) string {
	if s != ScopeRepo {
		return "a generated check compares a file of the repository with what its generator prints, so its scope is repo"
	}
	return ""
}

// Validate returns every problem with g.
func (g *Generated) Validate(Check) []error {
	var errs []error
	if g.File == "" {
		errs = append(errs, errors.New("generated.file is required: name the committed file the generator prints"))
	}
	if len(g.Run) == 0 {
		errs = append(errs, errors.New("generated.run lists no argument: write the generator's command, as a list of arguments"))
	}
	if g.MaxBytes < 1 {
		// 0 would read as no bound on the generator's output.
		errs = append(errs, fmt.Errorf("generated.max_bytes is %d: the bound on the file and the generator's output must be positive", g.MaxBytes))
	}
	return errs
}

// ListFields are generated, with the file, the generator and the bound,
// and timeout.
func (g *Generated) ListFields(c Check) Fields {
	return Fields{
		{"generated", Fields{{"file", g.File}, {"run", OrEmpty(g.Run)}, {"max_bytes", g.MaxBytes}}},
		{"timeout", c.Timeout.String()},
	}
}

// ListLines are the file, the generator under run, as a program check's
// run is written since it is the program errata starts, the bound and
// timeout.
func (g *Generated) ListLines(c Check, q Quoter) []Line {
	return []Line{
		{"file:", q.Value(g.File)},
		{"run:", quoted(q, g.Run, " ")},
		{"max_bytes:", strconv.FormatInt(g.MaxBytes, 10)},
		{"timeout:", c.Timeout.String()},
	}
}
