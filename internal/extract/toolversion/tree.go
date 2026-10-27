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
// version is the workspace's. last, when set, turns the scalar of the last
// path into a version, or drops it.
func readTree(x *extractor, f file, data []byte, format *core.Format, parse func([]byte) (*core.Node, error), add func(string, int) error, last func(*core.Node) (string, bool)) error {
	if parse == nil {
		return fmt.Errorf("errata was built without the reader for %s", f.kind.name)
	}
	if f.kind.skipsBOM {
		data = core.DropBOM(data)
	}
	if err := f.CheckNodes(data, format); err != nil {
		return named{err}
	}
	tree, err := parse(data)
	if err != nil {
		return err
	}
	_, base := core.FileParts(f.File)
	paths := x.paths[base]
	type found struct {
		text string
		line int
	}
	var all []found
	for i, p := range paths {
		nodes, err := p.Eval(tree)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			if n.Shape != core.Scalar {
				continue
			}
			text := n.Text
			if last != nil && i == len(paths)-1 {
				var ok bool
				if text, ok = last(n); !ok {
					continue
				}
			}
			all = append(all, found{text, n.Line})
		}
	}
	slices.SortStableFunc(all, func(a, b found) int { return a.line - b.line })
	for _, v := range all {
		if err := add(v.text, v.line); err != nil {
			return err
		}
	}
	return nil
}
