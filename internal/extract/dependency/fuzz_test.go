package dependency

import (
	"strings"
	"testing"
)

// fileOf is a file name each reader reads, by its index in readers.
func fileOf(i int) string {
	return strings.Replace(readers[i].pattern, "*", "", 1)
}

// Every reader reads any bytes, for any name, without a crash, into
// non-empty values at lines inside the file, and a reader whose value is
// text of its line gives text its line holds.
func FuzzDependency(f *testing.F) {
	seeds := map[string][]string{
		"go.mod":              {"module m\nrequire x v1.0.0\nreplace x => ../x\n", "require x v1", "module m\nrequire x v1.2\nreplace x v1.2 => x v1.3\n"},
		"go.work":             {"go 1.22\nreplace x v1 => x v1.0.1\n"},
		"package.json":        {`{"dependencies": {"x": "^1"}, "devDependencies": {"x": "latest"}}`},
		"package-lock.json":   {`{"name": "x", "version": "1", "packages": {"": {"name": "x", "version": "1"}, "node_modules/x": {"version": "2"}}}`, `{"dependencies": {"x": {"version": "1", "dependencies": {"x": {"version": "2"}}}}}`},
		"npm-shrinkwrap.json": {`{"packages": {"node_modules/a/node_modules/x": {"version": "3"}}}`},
		"pnpm-lock.yaml":      {"lockfileVersion: '9.0'\npackages:\n  x@1.0.0: {}\n", "lockfileVersion: 5.4\npackages:\n  /x/1.0.0_y@2:\n    dev: true\n"},
		"yarn.lock":           {"\"x@^1\", x@^2:\n  version \"1.2.0\"\n", "__metadata:\n  version: 8\n\"x@npm:^1\":\n  version: 1.2.0\n"},
		"pyproject.toml":      {"[project]\ndependencies = [\"x>=1\"]\n[tool.poetry.dependencies]\nx = {version = \"^1\"}\n"},
		"requirements.txt":    {"x==1 \\\n  --hash=sha256:0\n# c\n-r y\n"},
		"constraints.txt":     {"x>=1 ; python_version > '3'\r\n", "\\\nx!", "x>=1\\\n.0, \\\r\n<2\n", "x>=1\\\\\n.0 \\"},
		"poetry.lock":         {"[[package]]\nname = \"x\"\nversion = \"1\"\n"},
		"uv.lock":             {"[[package]]\nname = \"X\"\nversion = \"1\"\n"},
		"Cargo.toml":          {"[workspace.dependencies]\nx = \"1\"\n[dependencies]\nx.workspace = true\n[target.'cfg(unix)'.dependencies]\nx = {version = \"2\"}\n"},
		"Cargo.lock":          {"[[package]]\nname = \"x\"\nversion = \"1\"\n"},
		"Gemfile":             {"gem \"x\", \"~> 1\", V, require: false\ngem('x', # c\n '2')\n"},
		"Gemfile.lock":        {"GEM\n  specs:\n    x (1.0-java)\n      x (~> 1)\n"},
		"composer.json":       {`{"require": {"X": "^1"}, "require-dev": {"x": "self.version"}}`},
		"composer.lock":       {`{"packages": [{"name": "x", "version": "1"}]}`},
	}
	for i := range readers {
		for _, body := range seeds[fileOf(i)] {
			f.Add(uint8(i), body, "x")
		}
	}
	f.Fuzz(func(t *testing.T, kind uint8, data, name string) {
		file := fileOf(int(kind) % len(readers))
		got, err := fromData(file, name, data)
		if err != nil {
			return
		}
		checkLines(t, file, data, got)
	})
}
