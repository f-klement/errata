package cli

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/report"
)

// output is one --output FORMAT=PATH of errata check: a format of
// report.Formats and the file it is written to. ADR 0005 gives the spelling
// and the edge cases.
type output struct {
	format string
	path   string
}

// String is the value of --output as the command line gave it, which every
// message about the output quotes.
func (o output) String() string { return o.format + "=" + o.path }

// parseOutputs reads the values of --output. It refuses a value without an
// "=", an empty format or path, a format report.Formats does not hold, and
// two values that name the same file, so errata runs nothing on a command
// line that could not write what it asks for.
func parseOutputs(values []string) ([]output, error) {
	outs := make([]output, 0, len(values))
	seen := map[string]string{}
	for _, v := range values {
		// A value without "=" cuts to an empty path.
		format, path, _ := strings.Cut(v, "=")
		if format == "" || path == "" {
			return nil, fmt.Errorf("--output %q: want FORMAT=PATH, such as junit=errata.xml", v)
		}
		if _, known := report.Formats[format]; !known {
			return nil, fmt.Errorf("--output %q: unknown format %q, want one of %s", v, format, strings.Join(report.FormatNames(), ", "))
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("--output %q: %w", v, err)
		}
		if first, dup := seen[abs]; dup {
			return nil, fmt.Errorf("--output %q and --output %q name the same file", first, v)
		}
		seen[abs] = v
		outs = append(outs, output{format: format, path: path})
	}
	return outs, nil
}

// refuseConfigOutput refuses an output that names the config file, which
// errata would otherwise replace with a report after a run that passes.
// config is the path the config was loaded from.
func refuseConfigOutput(outs []output, config string) error {
	configAbs, err := filepath.Abs(config)
	if err != nil {
		return err
	}
	for _, o := range outs {
		abs, err := filepath.Abs(o.path)
		if err != nil {
			return fmt.Errorf("--output %q: %w", o, err)
		}
		if abs == configAbs {
			return fmt.Errorf("--output %q names the config file", o)
		}
	}
	return nil
}

// pendingOutput is an output whose temporary file exists, in the directory of
// the file it replaces, so that a rename puts the whole report in place at
// once and a write that fails leaves the old file, or none, as it was.
type pendingOutput struct {
	output
	tmp *os.File
}

// openOutputs creates the temporary file of each output before any check
// runs, so a path errata cannot write to stops the run with exit code 2 before
// it spends the checks' time. On an error it removes the files it created.
func openOutputs(outs []output) ([]*pendingOutput, error) {
	pending := make([]*pendingOutput, 0, len(outs))
	for _, o := range outs {
		p, err := openOutput(o)
		if err != nil {
			discardOutputs(pending)
			return nil, err
		}
		pending = append(pending, p)
	}
	return pending, nil
}

// openOutput creates the temporary file of o. A PATH that is a directory is
// refused; a symbolic link is not followed, so the rename replaces the link.
// The file is opened with mode 0666, as a shell redirect opens a new file,
// so the umask decides who may read it; O_EXCL refuses a file someone put
// under the random name first.
func openOutput(o output) (*pendingOutput, error) {
	if info, err := os.Lstat(o.path); err == nil && info.IsDir() {
		return nil, fmt.Errorf("--output %q: is a directory", o)
	}
	var random [8]byte
	_, _ = rand.Read(random[:]) // crypto/rand.Read never returns an error
	name := filepath.Join(filepath.Dir(o.path), fmt.Sprintf(".%s.%x.tmp", filepath.Base(o.path), random))
	tmp, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666) //nolint:gosec // a report is no secret: the umask decides, as for a redirect
	if err != nil {
		return nil, fmt.Errorf("--output %q: %w", o, err)
	}
	return &pendingOutput{output: o, tmp: tmp}, nil
}

// discardOutputs removes the temporary files of outputs not yet committed.
// commitOutputs sets tmp to nil on each one it handled, so a deferred call
// after it removes nothing.
func discardOutputs(pending []*pendingOutput) {
	for _, p := range pending {
		if p.tmp != nil {
			_ = p.tmp.Close()
			_ = os.Remove(p.tmp.Name())
			p.tmp = nil
		}
	}
}

// commitOutputs writes rep to each output in its format and renames the
// temporary file over the path. It writes every output it can whatever the
// others do, and returns an error naming each one that failed.
func commitOutputs(pending []*pendingOutput, rep check.Report, version string) []error {
	var errs []error
	for _, p := range pending {
		if err := commitOutput(p, rep, version); err != nil {
			errs = append(errs, fmt.Errorf("--output %q: %w", p.output, err))
		}
	}
	return errs
}

func commitOutput(p *pendingOutput, rep check.Report, version string) error {
	tmp := p.tmp
	p.tmp = nil
	err := report.Formats[p.format](version).Write(tmp, rep)
	if err == nil {
		err = keepMode(tmp, p.path)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), p.path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// keepMode gives tmp the mode of the regular file at path, if there is one,
// as a shell redirect into an existing file keeps its mode, so a report never
// widens who may read the file it replaces. The owner is not kept.
func keepMode(tmp *os.File, path string) error {
	info, err := os.Lstat(path)
	if err == nil && info.Mode().IsRegular() {
		return tmp.Chmod(info.Mode().Perm())
	}
	return nil
}

// joinErrors puts the errors of one run on one line, since Execute prints
// a returned error once, after "errata: ". It returns nil for no error.
func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, len(errs))
	for i, err := range errs {
		msgs[i] = err.Error()
	}
	return errors.New(strings.Join(msgs, "; "))
}
