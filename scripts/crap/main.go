// Command crap scores each function of errata's internal/ and scripts/ for CRAP, from
// the coverage profile that `make test` writes, and holds the scores to
// scripts/crap-baseline.txt. It runs no test.
//
// Usage:
//
//	crap [-root dir] [-require dir]... [-baseline file] <profile>
//	crap [-root dir] [-require dir]... -list <profile>
//	crap [-root dir] [-require dir]... -write-baseline <profile>
//	crap -baseline file -shrinks <older baseline>
//
// The CRAP of a function is comp^2 * (1 - cov)^3 + comp, where comp is its
// cyclomatic complexity and cov the share of its statements the tests ran.
//
// The counting is a port of the Go rules of unclebob/crapper,
// src/crapper/languages/golang.py, by Robert C. Martin, under the 0BSD
// licence ("Permission to use, copy, modify, and/or distribute this software
// for any purpose with or without fee is hereby granted"). It follows
// crap4go, whose code this command does not use. A function's complexity is
// 1, plus one for each if, for and range, each case clause of a switch, a
// type switch or a select (default included), and each && and ||. A closure
// counts toward the function that holds it and has no row of its own. A
// function is keyed by its package's import path and its name; a method by
// the import path, a dot and its receiver's type name, then its name, as
// crapper keys Go: `github.com/f-klement/errata/internal/rule.Rule Validate`.
//
// Coverage is Go's statement profile. A block counts toward the function
// whose body holds it, by line and column, and a block that a -coverpkg run
// lists once per test binary counts once, covered when any copy ran. Functions
// are read from each directory the profile names and each package under a
// -require directory. Left out, as the total of `make cover` leaves them out:
// test files, the packages whose directory name ends in "test" (extracttest,
// coretest and the like, which only tests import), files a build constraint
// leaves out of a plain build, and generated files (`// Code generated ...
// DO NOT EDIT.`).
//
// It refuses, with exit 2 and a line that starts "crap: REFUSED:", rather
// than score anything 0% by accident: a profile that does not parse or holds
// no block, a profile that names a file the tree lacks or one outside the
// root's module, a -require directory with no package, and a function with
// statements and no block in the profile. Each of these means the profile is
// not of this tree, or the run that wrote it did not instrument a package.
//
// The baseline holds one row per function that scores 10 or more, as
// `<namespace> <name> <score>`. Against it, a function with no row must score
// below 10, a row's score may not rise, and a row whose function is gone or
// now scores below 10 must leave the file (a stale row fails), so the file
// only shrinks. -shrinks holds the baseline to an older one: no row the older
// lacks, and no score above the older's. A check that fails exits 1, naming
// each function. Scores are compared at two decimals, as the file holds them.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// options are the command line.
type options struct {
	root, baseline, shrinks string
	require                 []string
	list, write             bool
	profile                 string
}

// dirs is a flag that may be given more than once.
type dirs []string

func (d *dirs) String() string     { return fmt.Sprint(*d) }
func (d *dirs) Set(v string) error { *d = append(*d, v); return nil }

// parseArgs reads the command line, or says what is wrong with it.
func parseArgs(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("crap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.root, "root", ".", "the module root the profile's tests ran in")
	fs.StringVar(&o.baseline, "baseline", "", "the baseline to hold the scores to")
	fs.StringVar(&o.shrinks, "shrinks", "", "an older baseline that -baseline may only shrink from")
	fs.Var((*dirs)(&o.require), "require", "a directory, relative to the root, each package under which the profile must cover")
	fs.BoolVar(&o.list, "list", false, "print every function's score, worst first")
	fs.BoolVar(&o.write, "write-baseline", false, "print the baseline the scores give")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	return o, o.validate(fs.Args())
}

// validate checks that the flags make one mode, and takes the profile.
func (o *options) validate(rest []string) error {
	if o.shrinks != "" {
		return o.validateShrinks(rest)
	}
	if len(rest) != 1 {
		return fmt.Errorf("want one profile, got %d arguments", len(rest))
	}
	if o.list && o.write {
		return errors.New("-list and -write-baseline are two modes; name one")
	}
	o.profile = rest[0]
	return nil
}

// validateShrinks checks the flags of the -shrinks mode, which reads no profile.
func (o *options) validateShrinks(rest []string) error {
	if o.baseline == "" || len(rest) != 0 || o.list || o.write {
		return errors.New("-shrinks takes -baseline and nothing else")
	}
	return nil
}

// run is the command, with its exit status: 0 when the check passes, 1 when it
// fails, 2 on a refusal or a wrong command line.
func run(args []string, stdout, stderr io.Writer) int {
	o, err := parseArgs(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "crap: %v\n", err)
		return 2
	}
	if o.shrinks != "" {
		return report(stdout, stderr, shrinkMode(o))
	}
	fns, err := scoreTree(o.root, o.profile, o.require)
	if err != nil {
		return refuse(stderr, err)
	}
	return report(stdout, stderr, scoreMode(o, fns, stdout))
}

// outcome is what a mode ends with: the failures it names, or a refusal.
type outcome struct {
	problems []string
	summary  string
	err      error
}

// shrinkMode holds -baseline to the older -shrinks baseline.
func shrinkMode(o options) outcome {
	cur, err := readBaselineFile(o.baseline)
	if err != nil {
		return outcome{err: err}
	}
	old, err := readBaselineFile(o.shrinks)
	if err != nil {
		return outcome{err: err}
	}
	return outcome{problems: shrinks(old, cur), summary: fmt.Sprintf("crap: %s holds %d rows, none new or higher than in %s", o.baseline, len(cur), o.shrinks)}
}

// scoreMode lists the scores, writes the baseline, or checks the scores
// against the baseline (an empty one when none is named).
func scoreMode(o options, fns map[string]fn, stdout io.Writer) outcome {
	if o.list {
		return outcome{err: writeList(stdout, fns)}
	}
	if o.write {
		return outcome{err: writeBaseline(stdout, fns)}
	}
	base := baseline{}
	if o.baseline != "" {
		var err error
		if base, err = readBaselineFile(o.baseline); err != nil {
			return outcome{err: err}
		}
	}
	return outcome{problems: check(fns, base), summary: summary(fns, base)}
}

// report prints an outcome and gives its exit status.
func report(stdout, stderr io.Writer, out outcome) int {
	if out.err != nil {
		return refuse(stderr, out.err)
	}
	for _, p := range out.problems {
		fmt.Fprintln(stdout, p)
	}
	if len(out.problems) > 0 {
		fmt.Fprintf(stdout, "crap: FAILED: %d problems\n", len(out.problems))
		return 1
	}
	if out.summary != "" {
		fmt.Fprintln(stdout, out.summary)
	}
	return 0
}

// refuse prints a refusal and gives its exit status.
func refuse(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "crap: REFUSED: %v\n", err)
	return 2
}
