package check

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// WitnessDir is where a check's witnesses live, relative to the
// repository root and written with slashes: each fixture is a directory
// below it, named as the check's witness key names it (ADR 0015). The
// globs and walks of a run pass over every directory of this name, so no
// planted break reads as a claim of the repository.
const WitnessDir = ".errata/witness"

// MaxWitnesses is the most fixtures one check may name.
const MaxWitnesses = 8

// Witnessed reports whether path, relative to a root and written with
// slashes, is in a directory of witnesses: below a directory witness whose
// parent is named .errata, wherever that is. A glob of a run leaves such a
// path out, as a walk does not enter the directory.
func Witnessed(p string) bool {
	return strings.HasPrefix(p, WitnessDir+"/") || strings.Contains(p, "/"+WitnessDir+"/")
}

// Fixture is the directory of the witness name, relative to the
// repository root and written with slashes.
func Fixture(name string) string { return path.Join(WitnessDir, name) }

// validateWitnesses returns every problem with c's witness key: at most
// MaxWitnesses names, each an id as ValidID takes it, so it is one segment
// with no dot, slash or space, none named twice, on a check of scope repo.
// An empty key is the loader's to refuse, since a check cannot tell it
// from one that names none.
func validateWitnesses(c Check) []error {
	if len(c.Witness) == 0 {
		return nil
	}
	var errs []error
	if c.Scope != ScopeRepo {
		errs = append(errs, fmt.Errorf("witness on scope %q: a witness is a tree of files, which cannot stand in for a live system, so only a check of scope repo names one", c.Scope))
	}
	if len(c.Witness) > MaxWitnesses {
		errs = append(errs, fmt.Errorf("witness names %d fixtures, over the limit of %d; a fixture may plant more than one break", len(c.Witness), MaxWitnesses))
	}
	named := map[string]bool{}
	for _, w := range c.Witness {
		if !ValidID(w) {
			errs = append(errs, fmt.Errorf("witness %q must be lower-case words joined by single hyphens, the name of a directory in %s", w, WitnessDir))
			continue
		}
		if named[w] {
			errs = append(errs, fmt.Errorf("witness names %q twice", w))
		}
		named[w] = true
	}
	return errs
}

// realRootKey is the context key of a witness's run.
type realRootKey struct{}

// OnWitness is ctx for a run of a check on a witness. The runner is given
// the fixture as its root, and real is the repository's root: a program
// starts there, and a glob reads its index, cut to the fixture's files.
func OnWitness(ctx context.Context, real string) context.Context {
	return context.WithValue(ctx, realRootKey{}, real)
}

// RealRoot is the repository's root on a run of a witness, which ctx holds,
// and false on any other run, whose root is the repository's.
func RealRoot(ctx context.Context) (string, bool) {
	real, ok := ctx.Value(realRootKey{}).(string)
	return real, ok
}

// WorkDir is the directory a program of a check starts in when the
// runner is given root: the repository's root, also on a witness's run,
// whose root is the fixture, so a run such as [python3, scripts/x.py]
// finds its script as on any run.
func WorkDir(ctx context.Context, root string) string {
	if real, ok := RealRoot(ctx); ok {
		return real
	}
	return root
}

// Name is how a report names r: the check's id, and for a result on a
// witness the fixture after it, as id [witness name].
func (r Result) Name() string {
	if r.Witness == "" {
		return r.Check.ID
	}
	return fmt.Sprintf("%s [witness %s]", r.Check.ID, r.Witness)
}

// WitnessesHeld counts the witnesses on which their check reported false,
// as it must.
func (r Report) WitnessesHeld() (held, ran int) {
	for _, res := range r.Results {
		if res.Witness == "" {
			continue
		}
		ran++
		if res.Verdict == VerdictTrue {
			held++
		}
	}
	return held, ran
}
