package dependency

import (
	"golang.org/x/mod/modfile"

	"github.com/f-klement/errata/internal/extract/core/gomod"
)

// readGoMod reads a go.mod as the go command does, with
// golang.org/x/mod/modfile (gomod.Parse). A require of the
// package gives its version, and a replace of it the version or the
// directory it is replaced with; a directory names no version, so it is
// not literal. A version the file writes in a form that is not canonical,
// such as v1, is a query the go command resolves, through the module proxy,
// and writes back, v1 to the latest v1 release: it is the text as written
// and not literal. Go keeps no lockfile: since Go 1.17 a module's go.mod
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
			if err := rd.add(found{text: r.Mod.Version, section: "require", role: Resolved, line: gomod.Line(data, r.Syntax.Start), notLiteral: !gomod.Canonical(r.Mod.Version)}); err != nil {
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
// file requires, as it writes them, and a replace of a version none of them
// is does nothing, so it gives nothing, as does any replace of one version
// when the file requires the package at no version. When the replaced
// version or one required is a query, such as v1, which version it
// resolves to is not in the file, so the replace may apply and is not
// literal. A go.work requires nothing, so required is nil and every replace
// counts; data is the file, for the lines. A replace by another module,
// such as a fork, gives that module's version, which is not the package's,
// and a replace by a directory names none: both are not literal, and so is
// a version the replace writes in a form that is not canonical.
func replaces(rd *reading, data []byte, rs []*modfile.Replace, required map[string]bool) error {
	for _, r := range rs {
		if !rd.is(r.Old.Path) {
			continue
		}
		maybe := false
		if required != nil && r.Old.Version != "" && !required[r.Old.Version] {
			if len(required) == 0 || gomod.Canonical(r.Old.Version) && !anyQuery(required) {
				continue
			}
			maybe = true
		}
		f := found{text: r.New.Version, section: "replace", role: Resolved, line: gomod.Line(data, r.Syntax.Start),
			notLiteral: r.New.Path != r.Old.Path || maybe || !gomod.Canonical(r.New.Version)}
		if f.text == "" {
			f.text, f.notLiteral = r.New.Path, true
		}
		if err := rd.add(f); err != nil {
			return err
		}
	}
	return nil
}

// anyQuery reports whether a version of required is a query, one that is
// not canonical, which the go command resolves to a version the file does
// not hold.
func anyQuery(required map[string]bool) bool {
	for v := range required {
		if !gomod.Canonical(v) {
			return true
		}
	}
	return false
}
