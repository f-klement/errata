package dependency

import (
	"github.com/f-klement/errata/internal/extract/core"
)

// cargoTables are the dependency tables of a Cargo.toml, under the names
// Cargo reads, the forms with _ that older manifests write included.
var cargoTables = []string{"dependencies", "dev-dependencies", "dev_dependencies", "build-dependencies", "build_dependencies"}

// workspaceText is what a dependency that inherits the workspace's
// version writes in place of one.
const workspaceText = "workspace = true"

// readCargo reads the range each dependency table of a Cargo.toml gives
// the package: [dependencies], [dev-dependencies], [build-dependencies],
// each under [target.<cfg>] too, and [workspace.dependencies]. A dependency
// is the package when its key, or the package = key of a renamed one, is
// its name. Its range is a string, such as "1.0", or the version of a
// table, such as { version = "1.0", features = ["derive"] }, read as Cargo
// reads it, with the ^ a bare version implies (cargoRange); a table
// without one, from git or a path, gives nothing. One that inherits the
// workspace's, { workspace = true }, is not literal, unless the same file's
// [workspace.dependencies] gives the package, which is then its value. A
// byte order mark at the start is skipped, as Cargo skips it.
func readCargo(rd *reading, data []byte) error {
	t, err := tree(rd, data, core.TOMLFormat, rd.x.parsers.TOML, true)
	if err != nil {
		return err
	}
	c := cargo{rd: rd}
	if err := c.deps(t.Member("workspace", "dependencies"), sharedTable); err != nil {
		return err
	}
	for _, table := range cargoTables {
		if err := c.deps(t.Member(table), table); err != nil {
			return err
		}
	}
	for _, target := range groups(t.Member("target")) {
		for _, table := range cargoTables {
			if err := c.deps(target.Member(table), "target "+table); err != nil {
				return err
			}
		}
	}
	return nil
}

// sharedTable is the table of the versions a workspace's members inherit.
const sharedTable = "workspace.dependencies"

// cargo is a Cargo.toml being read, and whether its
// [workspace.dependencies] gives the package, which a member that inherits
// it then takes.
type cargo struct {
	rd     *reading
	shared bool
}

// deps adds the range each dependency of the table n gives the package.
func (c *cargo) deps(n *core.Node, section string) error {
	if n == nil || n.Shape != core.Mapping {
		return nil
	}
	for i, key := range n.Keys {
		v := n.Vals[i]
		name := key
		if renamed := v.Member("package"); renamed != nil && renamed.Shape == core.Scalar {
			name = renamed.Text
		}
		if !c.rd.is(name) {
			continue
		}
		c.shared = c.shared || section == sharedTable
		if err := c.dep(v, section); err != nil {
			return err
		}
	}
	return nil
}

// dep adds the range of one dependency of the package, v.
func (c *cargo) dep(v *core.Node, section string) error {
	if v.Shape != core.Mapping {
		s, err := text(v, section+" of the package")
		if err != nil {
			return err
		}
		return c.rd.add(found{text: cargoRange(s), section: section, role: Range, line: v.Line})
	}
	if inherit := v.Member("workspace"); inherit != nil && inherit.Shape == core.Scalar && inherit.Type == core.TypeBool && inherit.Text == "true" {
		if c.shared {
			return nil
		}
		return c.rd.add(found{text: workspaceText, section: section, role: Range, line: inherit.Line, notLiteral: true})
	}
	version := v.Member("version")
	if version == nil {
		return nil
	}
	s, err := text(version, "version")
	if err != nil {
		return err
	}
	return c.rd.add(found{text: cargoRange(s), section: section, role: Range, line: version.Line})
}

// readCargoLock reads a Cargo.lock: an array of package tables, each with
// its name and the version resolved, the workspace's own packages
// included. A byte order mark at the start is skipped, as Cargo skips it.
func readCargoLock(rd *reading, data []byte) error {
	t, err := tree(rd, data, core.TOMLFormat, rd.x.parsers.TOML, true)
	if err != nil {
		return err
	}
	return lockPackages(rd, t.Member("package"), "")
}
