package packageversion

import (
	"bytes"
	"fmt"
	"slices"
	"unicode"

	"github.com/f-klement/errata/internal/extract/core"
)

// fileKind is one row of the table: a file name and how its version is
// read. add takes the version, its line, and whether it is not literal.
type fileKind struct {
	name string
	read func(x *extractor, f file, data []byte, add func(text string, line int, notLiteral bool) error) error
}

// kinds is the table of every file the extractor reads, by name, in the
// order a run without files: reads the root's. The README's package_version
// section is the same table, in prose.
var kinds = []*fileKind{
	{name: "package.json", read: readPackageJSON},
	{name: "pyproject.toml", read: readPyproject},
	{name: "Cargo.toml", read: readCargo},
	{name: "VERSION", read: readVersionFile},
}

// kindOf is the row for a file's base name, nil when the table has none.
func kindOf(base string) *fileKind {
	for _, k := range kinds {
		if k.name == base {
			return k
		}
	}
	return nil
}

// The texts of a not literal version: what the file wrote in place of a
// version, so a message names the line that makes the version computed.
const (
	dynamicText   = `dynamic = ["version"]`
	workspaceText = "version.workspace = true"
)

// parse reads data with parse, after the node limit of its format. skipBOM says
// whether the file's tool skips a byte order mark at its start, as npm and
// Cargo do; Python's tomllib refuses one, so pyproject.toml keeps it, and
// the parser refuses the file.
func parse(
	f file, data []byte, format *core.Format,
	parse func([]byte) (*core.Node, error), skipBOM bool,
) (*core.Node, error) {
	if parse == nil {
		return nil, fmt.Errorf("errata was built without the reader for %s", f.kind.name)
	}
	if skipBOM {
		// The mark is on line 1 and holds no line end, so no line moves.
		data = core.DropBOM(data)
	}
	if err := f.CheckNodes(data, format); err != nil {
		return nil, named{err}
	}
	return parse(data)
}

// member is the node at keys below n, nil when a key is absent or a node on
// the way is not a mapping.
func member(n *core.Node, keys ...string) *core.Node {
	for _, k := range keys {
		if n == nil || n.Shape != core.Mapping {
			return nil
		}
		n = n.Field[k]
	}
	return n
}

// version adds n as a version, refusing one that is not a string, which
// npm, the Python packaging tools and Cargo all refuse: a TOML float such as
// version = 1.0 is no version, and neither is a table. what names n's key
// for the message.
func version(n *core.Node, what string, add func(string, int, bool) error) error {
	if n.Shape != core.Scalar || n.Type != core.TypeString {
		return core.LineError(n.Line, "%s is %s, not a string; the tool that reads the file refuses it", what, describe(n))
	}
	return add(n.Text, n.Line, false)
}

// describe names what n is, for a message.
func describe(n *core.Node) string {
	switch {
	case n.Shape == core.Mapping:
		return "a table"
	case n.Shape == core.Sequence:
		return "an array"
	case n.Type == core.TypeNumber:
		return "a number"
	case n.Type == core.TypeBool:
		return "a boolean"
	default:
		return "null"
	}
}

// readPackageJSON reads package.json's version, the one npm publishes. A
// package.json without one, such as an application's, gives nothing.
func readPackageJSON(x *extractor, f file, data []byte, add func(string, int, bool) error) error {
	tree, err := parse(f, data, core.JSONFormat, x.parsers.JSON, true)
	if err != nil {
		return err
	}
	if v := member(tree, "version"); v != nil {
		return version(v, "version", add)
	}
	return nil
}

// readPyproject reads pyproject.toml as the build reads the version:
// project.version, PEP 621's, when the file sets it. When dynamic lists the
// version instead, the build backend computes it, and it is not literal,
// except under Poetry's backend, poetry-core, which takes it from
// tool.poetry.version. A file without either is Poetry's own form, before
// PEP 621, and gives tool.poetry.version. Either way, a tool.poetry.version
// that the poetry-dynamic-versioning plugin replaces at build time, which
// tool.poetry-dynamic-versioning.enable = true turns on, is a placeholder,
// and not literal.
func readPyproject(x *extractor, f file, data []byte, add func(string, int, bool) error) error {
	tree, err := parse(f, data, core.TOMLFormat, x.parsers.TOML, false)
	if err != nil {
		return err
	}
	set := member(tree, "project", "version")
	dynamic := dynamicVersion(member(tree, "project", "dynamic"))
	poetry := member(tree, "tool", "poetry", "version")
	switch {
	case set != nil && dynamic != nil:
		return core.LineError(dynamic.Line, "dynamic lists version and project.version sets it at line %d, which PEP 621 forbids", set.Line)
	case set != nil:
		return version(set, "project.version", add)
	case dynamic != nil && poetry != nil && poetryBackend(tree):
		return poetryVersion(tree, poetry, add)
	case dynamic != nil:
		return add(dynamicText, dynamic.Line, true)
	case poetry != nil:
		return poetryVersion(tree, poetry, add)
	}
	return nil
}

// poetryVersion adds tool.poetry.version, not literal when
// poetry-dynamic-versioning replaces it.
func poetryVersion(tree, poetry *core.Node, add func(string, int, bool) error) error {
	if isTrue(member(tree, "tool", "poetry-dynamic-versioning", "enable")) {
		literal := add
		add = func(text string, line int, _ bool) error { return literal(text, line, true) }
	}
	return version(poetry, "tool.poetry.version", add)
}

// isTrue reports whether n is the boolean true.
func isTrue(n *core.Node) bool {
	return n != nil && n.Shape == core.Scalar && n.Type == core.TypeBool && n.Text == "true"
}

// dynamicVersion is the "version" string in project.dynamic, nil when the
// list does not hold it or is not a list.
func dynamicVersion(dynamic *core.Node) *core.Node {
	if dynamic == nil || dynamic.Shape != core.Sequence {
		return nil
	}
	i := slices.IndexFunc(dynamic.Items, func(n *core.Node) bool {
		return n.Shape == core.Scalar && n.Type == core.TypeString && n.Text == "version"
	})
	if i < 0 {
		return nil
	}
	return dynamic.Items[i]
}

// poetryBackend reports whether build-system.build-backend is poetry-core's,
// the backend that fills a dynamic version from tool.poetry.version.
func poetryBackend(tree *core.Node) bool {
	b := member(tree, "build-system", "build-backend")
	return b != nil && b.Shape == core.Scalar && b.Text == "poetry.core.masonry.api"
}

// readCargo reads Cargo.toml as Cargo reads the package's version:
// package.version, and workspace.package.version, the version the
// workspace's members inherit. A package.version of { workspace = true }
// inherits it: from this file's workspace.package.version when it holds
// one, which is then the one value, and otherwise from a Cargo.toml further
// up, which errata does not look for, so it is not literal. A file with a
// [workspace] table is its own workspace's root, so Cargo refuses one that
// inherits a version it does not set, and so does errata. A package without
// a version, which Cargo reads as 0.0.0, gives nothing.
func readCargo(x *extractor, f file, data []byte, add func(string, int, bool) error) error {
	tree, err := parse(f, data, core.TOMLFormat, x.parsers.TOML, true)
	if err != nil {
		return err
	}
	own := member(tree, "package", "version")
	shared := member(tree, "workspace", "package", "version")
	type found struct {
		n          *core.Node
		what       string
		notLiteral bool
	}
	var all []found
	if own != nil && own.Shape == core.Mapping {
		inherit := member(own, "workspace")
		if !isTrue(inherit) {
			return core.LineError(own.Line, "package.version is a table other than { workspace = true }, which Cargo refuses")
		}
		if root := member(tree, "workspace"); shared == nil && root != nil && root.Shape == core.Mapping {
			return core.LineError(inherit.Line, "package.version inherits the workspace's version, and this file is the workspace's root but sets no workspace.package.version, which Cargo refuses")
		}
		if shared == nil {
			all = append(all, found{inherit, "", true})
		}
	} else if own != nil {
		all = append(all, found{own, "package.version", false})
	}
	if shared != nil {
		all = append(all, found{shared, "workspace.package.version", false})
	}
	slices.SortStableFunc(all, func(a, b found) int { return a.n.Line - b.n.Line })
	for _, v := range all {
		if v.notLiteral {
			err = add(workspaceText, v.n.Line, true)
		} else {
			err = version(v.n, v.what, add)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// readVersionFile reads a VERSION file: the first field of its first line
// that holds one, as tool_version reads it. A # starts a comment, a blank
// line is passed over, and fields are split at white space, so a CRLF file
// reads as an LF one. A file of blank lines and comments holds no version,
// and is refused.
func readVersionFile(_ *extractor, _ file, data []byte, add func(string, int, bool) error) error {
	data = core.DropBOM(data)
	for n := 1; len(data) > 0; n++ {
		text, after, _ := bytes.Cut(data, []byte("\n"))
		data = after
		text, _, _ = bytes.Cut(text, []byte("#"))
		// The first field is cut out by hand: bytes.Fields would make a
		// slice of every field of a line that may hold millions.
		text = bytes.TrimLeftFunc(text, unicode.IsSpace)
		if len(text) > 0 {
			end := bytes.IndexFunc(text, unicode.IsSpace)
			if end < 0 {
				end = len(text)
			}
			return add(string(text[:end]), n, false)
		}
	}
	return core.LineError(1, "the file holds no version, only blank lines and comments")
}
