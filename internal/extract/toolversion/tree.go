package toolversion

import (
	"fmt"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// treePaths are the paths each JSON or TOML file is read at for tool, by the
// file's name. They are the json and toml extractors' paths, so a rule that
// needs another member of the file reads it with one of those.
func treePaths(tool string) (map[string][]core.Path, error) {
	text := map[string][]string{
		"rust-toolchain":      {"toolchain.channel"},
		"rust-toolchain.toml": {"toolchain.channel"},
		"package.json":        {"engines['" + tool + "']", "packageManager"},
		"pyproject.toml":      {"project.requires-python", "tool.poetry.dependencies.python"},
		"Cargo.toml":          {"package.rust-version", "workspace.package.rust-version"},
	}
	out := map[string][]core.Path{}
	for name, paths := range text {
		for _, p := range paths {
			parsed, err := core.CheckPath("tool_version", p)
			if err != nil {
				return nil, err
			}
			out[name] = append(out[name], parsed)
		}
	}
	return out, nil
}

// readTOML reads a TOML file at its paths: rust-toolchain.toml's channel,
// pyproject.toml's requires-python and Poetry's python dependency, and
// Cargo.toml's rust-version, a package's or a workspace's.
func readTOML(x *extractor, f file, data []byte, add func(string, int) error) error {
	return readTree(x, f, data, core.TOMLFormat, x.parsers.TOML, add, nil)
}

// readPackageJSON reads package.json: engines.<tool>, the range of the tool
// the package runs on, and packageManager, Corepack's name@version, when its
// name is the tool. The version leaves out a +hash after it, which is the
// download's checksum, not its version.
func readPackageJSON(x *extractor, f file, data []byte, add func(string, int) error) error {
	return readTree(x, f, data, core.JSONFormat, x.parsers.JSON, add, func(n *core.Node) (string, bool) {
		name, version, ok := strings.Cut(n.Text, "@")
		// A scoped name, such as @yarnpkg/cli, is no tool a file pins.
		if !ok || name != x.tool {
			return "", false
		}
		version, _, _ = strings.Cut(version, "+")
		return version, true
	})
}

// readTree reads data with parse, after the node limit of its format, and
// adds every scalar its paths select, in file order. A byte order mark at the
// start of data is dropped first when the file's kind skips it; the mark
// holds no line end, so no line moves. A path that selects a
// mapping, such as Cargo's rust-version.workspace = true, adds nothing: the
// version is the workspace's. A TOML date or time, such as
// channel = 2024-01-01, is refused: rustup, Cargo and the Python tools each
// read the key as a string and refuse a date. last, when set, turns the
// scalar of the last path into a version, or drops it.
func readTree(x *extractor, f file, data []byte, format *core.Format, parse func([]byte) (*core.Node, error), add func(string, int) error, last func(*core.Node) (string, bool)) error {
	tree, err := parseTree(f, data, format, parse)
	if err != nil {
		return err
	}
	_, base := core.FileParts(f.File)
	all, err := selected(x.paths[base], tree, last)
	if err != nil {
		return err
	}
	slices.SortStableFunc(all, func(a, b treeValue) int { return a.line - b.line })
	for _, v := range all {
		if err := add(v.text, v.line); err != nil {
			return err
		}
	}
	return nil
}

// parseTree reads data with parse, after the byte order mark when the
// file's kind skips it and the node limit of its format.
func parseTree(f file, data []byte, format *core.Format, parse func([]byte) (*core.Node, error)) (*core.Node, error) {
	if parse == nil {
		return nil, fmt.Errorf("errata was built without the reader for %s", f.kind.name)
	}
	if f.kind.skipsBOM {
		data = core.DropBOM(data)
	}
	if err := f.CheckNodes(data, format); err != nil {
		return nil, named{err}
	}
	return parse(data)
}

// treeValue is a version a path selected, and its line.
type treeValue struct {
	text string
	line int
}

// selected is the scalars paths select in tree, path by path, the last
// path's turned into a version by last when it is set.
func selected(paths []core.Path, tree *core.Node, last func(*core.Node) (string, bool)) ([]treeValue, error) {
	var all []treeValue
	for i, p := range paths {
		nodes, err := p.Eval(tree)
		if err != nil {
			return nil, err
		}
		convert := last
		if i != len(paths)-1 {
			convert = nil
		}
		for _, n := range nodes {
			text, ok, err := scalarText(n, convert)
			if err != nil {
				return nil, err
			}
			if ok {
				all = append(all, treeValue{text, n.Line})
			}
		}
	}
	return all, nil
}

// scalarText is the version node n holds: none for a mapping or a list, an
// error for a date or a time, and when last is set, what last turns it into.
func scalarText(n *core.Node, last func(*core.Node) (string, bool)) (string, bool, error) {
	if n.Shape != core.Scalar {
		return "", false, nil
	}
	if n.Form == core.FormDateTime {
		return "", false, core.LineError(n.Line, "the version is a date or a time, not a string; the tool that reads the file refuses it")
	}
	if last == nil {
		return n.Text, true, nil
	}
	text, ok := last(n)
	return text, ok, nil
}
