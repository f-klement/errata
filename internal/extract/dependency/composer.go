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
		if err := composerSection(rd, section, t.Member(section)); err != nil {
			return err
		}
	}
	return nil
}

// composerSection adds the range n, a composer.json's require or
// require-dev, gives the package, nothing when it is no mapping.
func composerSection(rd *reading, section string, n *core.Node) error {
	if n == nil || n.Shape != core.Mapping {
		return nil
	}
	for i, key := range n.Keys {
		if !rd.is(key) {
			continue
		}
		if err := composerEntry(rd, section, n.Vals[i]); err != nil {
			return err
		}
	}
	return nil
}

// composerEntry adds the range v, the package's value in a section of a
// composer.json, rewritten as Composer reads it when it is literal.
func composerEntry(rd *reading, section string, v *core.Node) error {
	s, err := text(v, section+" of the package")
	if err != nil {
		return err
	}
	f := found{text: s, section: section, role: Range, line: v.Line}
	if f.notLiteral = s == "self.version" || composerBranch(s); !f.notLiteral {
		f.text = composerRange(s)
	}
	return rd.add(f)
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
			if err := composerLocked(rd, section, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// composerLocked adds the version installed of p, a package of a
// composer.lock's section, when it is the package.
func composerLocked(rd *reading, section string, p *core.Node) error {
	name := p.Member("name")
	if name == nil || name.Shape != core.Scalar || !rd.is(name.Text) {
		return nil
	}
	v := p.Member("version")
	if v == nil {
		return nil
	}
	s, err := text(v, "version")
	if err != nil {
		return err
	}
	return rd.add(found{text: s, section: section, role: Resolved, line: v.Line, notLiteral: composerBranch(s)})
}
