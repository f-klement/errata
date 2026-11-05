package dependency

import "github.com/f-klement/errata/internal/extract/core"

// readComposer reads the range require and require-dev give the package in
// a composer.json, as Composer reads it (composerRange). Composer compares
// names in any case. self.version, the version of the root package, and a
// branch, such as dev-main or 2.x-dev, name no version in the file, and are
// not literal. PHP's JSON reader refuses a byte order mark, and so does
// errata.
func readComposer(rd *reading, data []byte) error {
	t, err := tree(rd, data, core.JSONFormat, rd.x.parsers.JSON, false)
	if err != nil {
		return err
	}
	for _, section := range []string{"require", "require-dev"} {
		n := t.Member(section)
		if n == nil || n.Shape != core.Mapping {
			continue
		}
		for i, key := range n.Keys {
			if !rd.is(key) {
				continue
			}
			s, err := text(n.Vals[i], section+" of the package")
			if err != nil {
				return err
			}
			f := found{text: s, section: section, role: Range, line: n.Vals[i].Line}
			if f.notLiteral = s == "self.version" || composerBranch(s); !f.notLiteral {
				f.text = composerRange(s)
			}
			if err := rd.add(f); err != nil {
				return err
			}
		}
	}
	return nil
}

// readComposerLock reads a composer.lock: the arrays packages and
// packages-dev, each package with its name and the version installed. A
// branch installed, such as dev-main, names no version, and is not literal.
func readComposerLock(rd *reading, data []byte) error {
	t, err := tree(rd, data, core.JSONFormat, rd.x.parsers.JSON, false)
	if err != nil {
		return err
	}
	for _, section := range []string{"packages", "packages-dev"} {
		n := t.Member(section)
		if n == nil || n.Shape != core.Sequence {
			continue
		}
		for _, p := range n.Items {
			name := p.Member("name")
			if name == nil || name.Shape != core.Scalar || !rd.is(name.Text) {
				continue
			}
			v := p.Member("version")
			if v == nil {
				continue
			}
			s, err := text(v, "version")
			if err != nil {
				return err
			}
			if err := rd.add(found{text: s, section: section, role: Resolved, line: v.Line, notLiteral: composerBranch(s)}); err != nil {
				return err
			}
		}
	}
	return nil
}
