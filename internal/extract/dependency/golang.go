package dependency

import (
	"golang.org/x/mod/modfile"

	"github.com/f-klement/errata/internal/extract/core/gomod"
)

// readGoMod reads a go.mod as the go command does, with
// golang.org/x/mod/modfile (gomod.Parse). A require of the
// package gives its version, and a replace of it the version or the
// directory it is replaced with; a directory names no version, so it is
// not literal. Go keeps no lockfile: since Go 1.17 a module's go.mod
// requires every module its build needs at the version the build selects,
// so both are resolved. Under module: true it gives the module path the
// module directive declares instead.
func readGoMod(rd *reading, data []byte) error {
	mod, err := gomod.Parse(rd.src, data)
	if err != nil {
		return named{err}
	}
	if rd.x.module {
		if mod.Module == nil {
			return nil
		}
		return rd.add(found{text: mod.Module.Mod.Path, section: "module", line: gomod.Line(data, mod.Module.Syntax.Start)})
	}
	required := map[string]bool{}
	for _, r := range mod.Require {
		if rd.is(r.Mod.Path) {
			required[r.Mod.Version] = true
			if err := rd.add(found{text: r.Mod.Version, section: "require", role: Resolved, line: gomod.Line(data, r.Syntax.Start)}); err != nil {
				return err
			}
		}
	}
	return replaces(rd, data, mod.Replace, required)
}

// readGoWork reads a go.work's replace directives, which override every
// module of the workspace: a go.work requires nothing.
func readGoWork(rd *reading, data []byte) error {
	work, err := gomod.ParseWork(rd.src, data)
	if err != nil {
		return named{err}
	}
	return replaces(rd, data, work.Replace, nil)
}

// replaces adds what each replace of the package puts in its place. A
// replace of one version, replace x v1.0.0 => ..., applies only when the
// build selects that version; in a go.mod, required holds the versions the
// file requires, and a replace of a version none of them is does nothing,
// so it gives nothing. A go.work requires nothing, so required is nil and
// every replace counts; data is the file, for the lines. A replace by another module, such as a fork, gives
// that module's version, which is not the package's, and a replace by a
// directory names none: both are not literal.
func replaces(rd *reading, data []byte, rs []*modfile.Replace, required map[string]bool) error {
	for _, r := range rs {
		if !rd.is(r.Old.Path) || required != nil && r.Old.Version != "" && !required[r.Old.Version] {
			continue
		}
		f := found{text: r.New.Version, section: "replace", role: Resolved, line: gomod.Line(data, r.Syntax.Start), notLiteral: r.New.Path != r.Old.Path}
		if f.text == "" {
			f.text, f.notLiteral = r.New.Path, true
		}
		if err := rd.add(f); err != nil {
			return err
		}
	}
	return nil
}
