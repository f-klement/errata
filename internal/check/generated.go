package check

import (
	"errors"
	"fmt"
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

// IsGenerated reports whether c is a generated check.
func (c Check) IsGenerated() bool { return c.Generated != nil }

// validate returns every problem with g. c is the check g belongs to.
func (g Generated) validate(c Check) []error {
	var errs []error
	if len(c.Run) > 0 {
		errs = append(errs, errors.New("run is set beside generated: a generated check runs generated.run, so write the command there"))
	}
	if c.Rule != nil {
		errs = append(errs, errors.New("a check is a rule (extract) or a generated check (generated), never both"))
	}
	if c.Scope != ScopeRepo && c.Scope.known() {
		// The check compares a file of the repository with what its
		// generator prints from the repository, so it needs no provider.
		errs = append(errs, fmt.Errorf("scope %q: a generated check compares a file of the repository with what its generator prints, so its scope is repo", c.Scope))
	}
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
