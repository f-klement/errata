package toolversion

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/toml"
)

// parsers are the facade's: the json and toml readers' own.
var parsers = Parsers{JSON: json.Parse, TOML: toml.Parse}

// build is Build with the facade's parsers.
func build(s Spec) (check.Extractor, error) { return Build(s, parsers) }

// marked writes each value as text@file:line, with a ! after the text of a
// value marked not literal.
func marked(vs []check.Value) string {
	var out []string
	for _, v := range vs {
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		out = append(out, v.Text+mark+"@"+v.At.File+":"+strconv.Itoa(v.At.Line))
	}
	return strings.Join(out, " ")
}

// values is what s extracts from a repository of files, marked.
func values(t *testing.T, files map[string]string, s Spec) string {
	t.Helper()
	return marked(extracttest.MustRun(t, build, s, extracttest.Repo(t, files)))
}

// refusal is the error s gives on a repository of files.
func refusal(t *testing.T, files map[string]string, s Spec) string {
	t.Helper()
	got, err := extracttest.Run(t, build, s, extracttest.Repo(t, files))
	if err == nil {
		t.Fatalf("no error, values %s", marked(got))
	}
	return err.Error()
}

// loadError is the error Build gives for s.
func loadError(t *testing.T, s Spec) string {
	t.Helper()
	if _, err := build(s); err != nil {
		return err.Error()
	}
	t.Fatalf("%+v loads", s)
	return ""
}

// Each file of the table, read alone through files:, gives the versions its
// tool reads, at their lines.
func TestToolVersionReadsEachFile(t *testing.T) {
	for _, c := range []struct {
		name, tool, body, want string
	}{
		{".tool-versions", "node", "# pins\nnodejs 20.11.1 18.19.0 # fallback\npython 3.12.1\n\nnode lts system\n",
			"20.11.1@.tool-versions:2 18.19.0@.tool-versions:2 lts!@.tool-versions:5 system!@.tool-versions:5"},
		{".tool-versions", "go", "golang 1.22.3\r\ngo   ref:abc123\tpath:~/go\n", "1.22.3@.tool-versions:1 ref:abc123!@.tool-versions:2 path:~/go!@.tool-versions:2"},
		{".tool-versions", "ruby", "rubyx 3\nruby\n#ruby 2\n", ""},
		{".sdkmanrc", "java", "# Enable auto-env\njava=21.0.2-tem\nmaven = 3.9.6 \n", "21.0.2-tem@.sdkmanrc:2"},
		{".sdkmanrc", "maven", "java=21.0.2-tem\n maven = 3.9.6 \n", "3.9.6@.sdkmanrc:2"},
		{".nvmrc", "node", "\n  v20.11.1 # the LTS\n18\n", "v20.11.1@.nvmrc:2"},
		{".nvmrc", "node", "lts/iron\n", "lts/iron!@.nvmrc:1"},
		{".node-version", "node", "20.11.1\r\n", "20.11.1@.node-version:1"},
		// pyenv reads the first word of each line: system is no version here.
		{".python-version", "python", "3.12.1\n# second\n3.11.7 system\n", "3.12.1@.python-version:1 3.11.7@.python-version:3"},
		{".python-version", "python", "system\n", "system!@.python-version:1"},
		{"runtime.txt", "python", "\npython-3.12.1\n", "3.12.1@runtime.txt:2"},
		{".ruby-version", "ruby", "ruby-3.3.0\n", "ruby-3.3.0@.ruby-version:1"},
		{".go-version", "go", "1.22.3\n", "1.22.3@.go-version:1"},
		{"go.mod", "go", "// m\nmodule example.com/m\n\ngo 1.22.0\n\ntoolchain go1.22.3\n\nrequire (\n\tgolang.org/x/mod v0.42.0\n)\n",
			"1.22.3@go.mod:6"},
		{"go.mod", "go", "module m\ntoolchain default\ngo 1.21\n", "default!@go.mod:2"},
		{"go.mod", "go", "module m\ngo 1.21rc1\n", "1.21rc1@go.mod:2"},
		{"go.mod", "go", "module m\ngo 1.27.2\ntoolchain go1.27.3-custom\n", "1.27.3-custom@go.mod:3"},
		{"go.mod", "go", "module m\n", ""},
		{".terraform-version", "terraform", "latest:^1.5\n", "latest:^1.5!@.terraform-version:1"},
		{".terraform-version", "terraform", "1.7.4\n", "1.7.4@.terraform-version:1"},
		{".java-version", "java", "17.0\n", "17.0@.java-version:1"},
		{"rust-toolchain", "rust", "\n1.75.0\n", "1.75.0@rust-toolchain:2"},
		{"rust-toolchain", "rust", "nightly-2024-01-01\n", "nightly-2024-01-01@rust-toolchain:1"},
		{"rust-toolchain", "rust", "stable", "stable!@rust-toolchain:1"},
		{"rust-toolchain", "rust", "[toolchain]\nchannel = \"1.75\"\n", "1.75@rust-toolchain:2"},
		{"rust-toolchain", "rust", "toolchain.channel = \"1.76\"\n", "1.76@rust-toolchain:1"},
		{"rust-toolchain", "rust", "\n\n  nightly-2024-01-01-x86_64-unknown-linux-gnu \n", "nightly-2024-01-01-x86_64-unknown-linux-gnu@rust-toolchain:3"},
		{"rust-toolchain.toml", "rust", "[toolchain]\ncomponents = [\"clippy\"]\nchannel = \"stable-x86_64-unknown-linux-gnu\"\n",
			"stable-x86_64-unknown-linux-gnu!@rust-toolchain.toml:3"},
		{"rust-toolchain.toml", "rust", "[toolchain]\npath = \"/opt/rust\"\n", ""},
		{".bazelversion", "bazel", "7.0.2\n", "7.0.2@.bazelversion:1"},
		{".bazelversion", "bazel", "latest-1\n", "latest-1!@.bazelversion:1"},
		{".bun-version", "bun", "1.0.25\n", "1.0.25@.bun-version:1"},
		{"VERSION", "myapp", "1.4.2\n", "1.4.2@VERSION:1"},
		{"package.json", "node", "{\n  \"engines\": {\n    \"node\": \">=20\"\n  },\n  \"packageManager\": \"pnpm@9.1.0+sha512.abc\"\n}\n",
			">=20@package.json:3"},
		// A space is no control character: a range keeps it.
		{"package.json", "node", "{\"engines\": {\"node\": \">= 20 < 22\"}}", ">= 20 < 22@package.json:1"},
		{"package.json", "pnpm", "{\n  \"engines\": {\"pnpm\": \"9\"},\n  \"packageManager\": \"pnpm@9.1.0+sha512.abc\"\n}\n",
			"9@package.json:2 9.1.0@package.json:3"},
		{"package.json", "yarn", "{\"packageManager\": \"@yarnpkg/cli@4\", \"engines\": {\"yarn\": {\"x\": 1}}}", ""},
		{"pyproject.toml", "python", "[project]\nname = \"x\"\nrequires-python = \">=3.10\"\n", ">=3.10@pyproject.toml:3"},
		{"pyproject.toml", "python", "[tool.poetry.dependencies]\npython = \"^3.11\"\n", "^3.11@pyproject.toml:2"},
		{"Cargo.toml", "rust", "[workspace.package]\nrust-version = \"1.74\"\n\n[package]\nname = \"a\"\nrust-version.workspace = true\n", "1.74@Cargo.toml:2"},
		{"Cargo.toml", "rust", "[package]\nname = \"a\"\nrust-version = \"1.70\"\n", "1.70@Cargo.toml:3"},
		// File order, not the order of the paths.
		{"Cargo.toml", "rust", "[workspace.package]\nrust-version = \"1.74\"\n[package]\nrust-version = \"1.70\"\n", "1.74@Cargo.toml:2 1.70@Cargo.toml:4"},
	} {
		got := values(t, map[string]string{c.name: c.body}, Spec{Tool: c.tool, Files: []string{c.name}})
		if got != c.want {
			t.Errorf("%s for %s: got %q, want %q", c.name, c.tool, got, c.want)
		}
	}
}

// Without files:, the extractor walks the repository for every file that
// pins the tool, a directory's in the table's order before its
// subdirectories', and passes over the manifests, which state a range and
// not a pin.
func TestToolVersionFindsEveryFileThatPinsTheTool(t *testing.T) {
	repo := map[string]string{
		".tool-versions":     "nodejs 20.11.1\npython 3.12.1\n",
		".nvmrc":             "20.11.1\n",
		".node-version":      "20.11.0\n",
		"package.json":       `{"engines": {"node": ">=18"}}`,
		"VERSION":            "1.0.0\n",
		"web/.nvmrc":         "18\n",
		".python-version":    "3.12.1\n",
		".sdkmanrc":          "java=21.0.2-tem\n",
		"go.mod":             "module m\ngo 1.22\n",
		"rust-toolchain":     "1.75\n",
		".terraform-version": "1.7.4\n",
	}
	for _, c := range []struct{ tool, want string }{
		{"node", "20.11.1@.tool-versions:1 20.11.1@.nvmrc:1 20.11.0@.node-version:1 18@web/.nvmrc:1"},
		{"python", "3.12.1@.tool-versions:2 3.12.1@.python-version:1"},
		{"java", "21.0.2-tem@.sdkmanrc:1"},
		{"go", "1.22@go.mod:2"},
		{"rust", "1.75@rust-toolchain:1"},
		{"terraform", "1.7.4@.terraform-version:1"},
		{"ruby", ""},
	} {
		if got := values(t, repo, Spec{Tool: c.tool}); got != c.want {
			t.Errorf("%s: got %q, want %q", c.tool, got, c.want)
		}
	}
	// files: narrows the search, and reads a file below the root by its name.
	if got := values(t, repo, Spec{Tool: "node", Files: []string{"web/.nvmrc", ".nvmrc"}}); got != "18@web/.nvmrc:1 20.11.1@.nvmrc:1" {
		t.Errorf("files: got %q", got)
	}
}

// The walk finds pins in every directory but those core.SkippedDirs names,
// in order, reads a symlinked file and does not enter a symlinked directory.
// The planted break: Extract reading the root's files alone.
func TestToolVersionWalksTheRepository(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"b/.nvmrc":              "18\n",
		"a/z/.tool-versions":    "nodejs 16\n",
		"a/.node-version":       "20\n",
		"a/.tool-versions":      "node 20\n",
		".nvmrc":                "22\n",
		"node_modules/x/.nvmrc": "12\n",
		"web/vendor/.nvmrc":     "10\n",
		".git/.nvmrc":           "8\n",
		"testdata/.nvmrc":       "6\n",
		"linked/.nvmrc":         "4\n",
		"c/package.json":        `{"engines": {"node": "20"}}`,
	})
	extracttest.Symlink(t, "../linked", filepath.Join(root, "d"))
	extracttest.Symlink(t, "../b/.nvmrc", filepath.Join(root, "e", ".nvmrc"))
	got := marked(extracttest.MustRun(t, build, Spec{Tool: "node"}, root))
	want := "22@.nvmrc:1 20@a/.tool-versions:1 20@a/.node-version:1 16@a/z/.tool-versions:1 18@b/.nvmrc:1 18@e/.nvmrc:1 4@linked/.nvmrc:1"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A submodule's pins are not walked, and are read when files: names them.
func TestToolVersionReadsASubmoduleByName(t *testing.T) {
	repo := map[string]string{".nvmrc": "20\n", "sub/.git": "gitdir: ../.git/modules/sub\n", "sub/.nvmrc": "18\n"}
	if got := values(t, repo, Spec{Tool: "node"}); got != "20@.nvmrc:1" {
		t.Errorf("walked: %q", got)
	}
	if got := values(t, repo, Spec{Tool: "node", Files: []string{"sub/.nvmrc"}}); got != "18@sub/.nvmrc:1" {
		t.Errorf("named: %q", got)
	}
}

// The walk's limits make the rule unknown, never a short list: a test
// lowers them to witness each with a small tree.
func TestToolVersionWalkLimits(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a/b/.nvmrc": "20\n", "c": ""})
	e, err := build(Spec{Tool: "node"})
	if err != nil {
		t.Fatal(err)
	}
	x := e.(extractor)
	// ., a, a/b, a/b/.nvmrc, c: four entries below the root; a/b is two deep.
	x.walk = core.WalkLimits{Entries: 4, Dirs: 2, Depth: 2, Names: 10}
	if got, err := x.Extract(root); err != nil || marked(got) != "20@a/b/.nvmrc:1" {
		t.Errorf("at the limits: %s, %v", marked(got), err)
	}
	x.walk = core.WalkLimits{Entries: 3, Dirs: 2, Depth: 2, Names: 10}
	_, err = x.Extract(root)
	extracttest.WantError(t, err, "walk: the repository holds more than 3 files and directories outside the directories errata passes over, the limit")
	x.walk = core.WalkLimits{Entries: 4, Dirs: 2, Depth: 1, Names: 10}
	_, err = x.Extract(root)
	extracttest.WantError(t, err, `walk: "a/b" is nested more than 1 directories deep, the limit`)
}

// A file files: names must be there; one the walk does not find is
// passed over. The planted break: Extract passing over every missing file.
func TestToolVersionNamedFileMustExist(t *testing.T) {
	got := refusal(t, map[string]string{".nvmrc": "20\n"}, Spec{Tool: "node", Files: []string{".nvmrc", ".node-version"}})
	if !strings.Contains(got, "read .node-version:") || !strings.Contains(got, "no such file") {
		t.Errorf("got %q", got)
	}
}

// A symlink out of the repository is refused in the walk too, not
// passed over as a file that is not there.
func TestToolVersionRefusesASymlinkOut(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"r/.tool-versions": "nodejs 20\n", "outside": "20\n"})
	extracttest.Symlink(t, filepath.Join(root, "outside"), filepath.Join(root, "r", ".nvmrc"))
	_, err := extracttest.Run(t, build, Spec{Tool: "node"}, filepath.Join(root, "r"))
	extracttest.WantError(t, err, "read .nvmrc: a symlink leads outside the repository")
}

// What does not load, each with its message.
func TestToolVersionLoadErrors(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{}, "tool_version: tool is required"},
		{Spec{Tool: "nodejs"}, "tool_version: write tool: node; a .tool-versions line under nodejs pins it too"},
		{Spec{Tool: "golang"}, "tool_version: write tool: go;"},
		{Spec{Tool: "no de"}, `tool_version: tool "no de" holds a character other than`},
		{Spec{Tool: "a'b"}, `tool_version: tool "a'b" holds a character other than`},
		{Spec{Tool: strings.Repeat("a", 65)}, "bytes long, over the limit of 64"},
		{Spec{Tool: "node", Files: []string{"../.nvmrc"}}, "is outside the repository"},
		{Spec{Tool: "node", Files: []string{"/.nvmrc"}}, "is outside the repository"},
		{Spec{Tool: "node", Files: []string{"node.txt"}}, `tool_version: errata does not know how "node.txt" pins a tool; it reads .bazelversion,`},
		{Spec{Tool: "python", Files: []string{"web/.nvmrc"}}, `tool_version: "web/.nvmrc" pins node, not python`},
		{Spec{Tool: "node", Files: []string{".nvmrc", "./.nvmrc"}}, `tool_version: files names "./.nvmrc" twice`},
		{Spec{Tool: "node", MaxBytes: -1}, "max_bytes is -1"},
		{Spec{Tool: "node", Files: []string{".nvmrc"}, MaxBytes: core.MaxMaxBytes + 1}, "max_bytes is"},
	} {
		if got := loadError(t, c.spec); !strings.Contains(got, c.want) {
			t.Errorf("%+v: got %q, want %q", c.spec, got, c.want)
		}
	}
	// The characters on each side of the letters and digits do not load.
	for _, tool := range []string{"a`", "a{", "a[", "a\x7f"} {
		if _, err := build(Spec{Tool: tool}); err == nil {
			t.Errorf("%q loads", tool)
		}
	}
	// The tools that load: a mise backend, the name an alias stands for, and
	// each end of the letters and digits.
	for _, tool := range []string{"npm:@biomejs/biome", "node", "go", "python3.12+x", "azAZ09", "PHP"} {
		if _, err := build(Spec{Tool: tool}); err != nil {
			t.Errorf("%s: %v", tool, err)
		}
	}
}

// A file its tool would refuse makes the rule unknown, with the file and
// the line.
func TestToolVersionRefusesWhatDoesNotParse(t *testing.T) {
	for _, c := range []struct {
		name, tool, body, want string
	}{
		{"go.mod", "go", "module m\ngo 1.22\nfrob x\n", "go.mod:3: unknown directive: frob"},
		{"go.mod", "go", "module m\ngo 1.22\ngo 1.23\n", "go.mod:3: repeated go statement"},
		{".sdkmanrc", "java", "java=21\nmaven\n", `.sdkmanrc: line 2: "maven" is not candidate=version`},
		{".sdkmanrc", "java", "java= \n", ".sdkmanrc: line 1: java names no version"},
		{"runtime.txt", "python", "3.12.1\n", `runtime.txt: line 1: "3.12.1" is not python-<version>`},
		{"runtime.txt", "python", "python-\n", `runtime.txt: line 1: "python-" is not python-<version>`},
		{"rust-toolchain.toml", "rust", "[toolchain\n", "rust-toolchain.toml: "},
		{"rust-toolchain", "rust", "1.75\n1.76\n", "rust-toolchain: "},
		{"package.json", "node", "{\"engines\": ", "package.json: "},
		{".nvmrc", "node", "\n2\x010\n", `.nvmrc: line 2: the version "2\x010" holds a control character, a direction control, a line separator or a byte that is not UTF-8`},
		{".nvmrc", "node", "20\u009b31m\n", ".nvmrc: line 1: the version"},
		{".nvmrc", "node", "20\u202e1.1\n", ".nvmrc: line 1: the version"},
		{"package.json", "node", "{\"engines\": {\"node\": \"20\\u20281\"}}", "package.json: line 1: the version"},
		{"package.json", "node", "{\"engines\": {\"node\": \"\\u009b31m\"}}", "package.json: line 1: the version"},
		{".tool-versions", "node", "node 20\xff\n", ".tool-versions: line 1: the version"},
		{"package.json", "node", "{\"engines\": {\"node\": \"\\u0000\"}}", "package.json: line 1: the version"},
		{"package.json", "pnpm", "{\n\"packageManager\": \"pnpm@\"}", "package.json: line 2: the version is empty"},
		{".python-version", "python", "3.12\n3\x7f\n", ".python-version: line 2: the version"},
		{"runtime.txt", "python", "python-3\x01\n", "runtime.txt: line 1: the version"},
		{"pyproject.toml", "python", "[project]\nrequires-python = \"\"\n", "pyproject.toml: line 2: the version is empty"},
	} {
		got := refusal(t, map[string]string{c.name: c.body}, Spec{Tool: c.tool, Files: []string{c.name}})
		if !strings.Contains(got, c.want) {
			t.Errorf("%s %q: got %q, want %q", c.name, c.body, got, c.want)
		}
	}
}

// A byte order mark at the start of a file is dropped: the values and their
// lines are those of the file without it. npm skips it in package.json, and
// Cargo and rustup in their TOML files. The planted breaks: eachLine without
// its core.DropBOM, which keeps the mark in the version and hides the
// .tool-versions line's tool, and readTree without its core.DropBOM, which
// refuses the JSON and TOML files.
func TestToolVersionDropsAByteOrderMark(t *testing.T) {
	for _, c := range []struct{ name, tool, body, want string }{
		{".nvmrc", "node", "\ufeff20.11.1\n", "20.11.1@.nvmrc:1"},
		{".tool-versions", "node", "\ufeffnodejs 20.11.1\n", "20.11.1@.tool-versions:1"},
		{".python-version", "python", "\ufeff\n3.12\n", "3.12@.python-version:2"},
		{".sdkmanrc", "java", "\ufeffjava=21\n", "21@.sdkmanrc:1"},
		{"runtime.txt", "python", "\ufeffpython-3.12.1\n", "3.12.1@runtime.txt:1"},
		{"rust-toolchain", "rust", "\ufeffstable\n", "stable!@rust-toolchain:1"},
		{"rust-toolchain", "rust", "\ufeff[toolchain]\nchannel = \"1.75.0\"\n", "1.75.0@rust-toolchain:2"},
		{"rust-toolchain.toml", "rust", "\ufeff[toolchain]\nchannel = \"1.75.0\"\n", "1.75.0@rust-toolchain.toml:2"},
		{"package.json", "node", "\ufeff{\"engines\": {\"node\": \">=20\"}}\n", ">=20@package.json:1"},
		{"Cargo.toml", "rust", "\ufeff[package]\nrust-version = \"1.70\"\n", "1.70@Cargo.toml:2"},
	} {
		if got := values(t, map[string]string{c.name: c.body}, Spec{Tool: c.tool, Files: []string{c.name}}); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// pyproject.toml keeps a byte order mark, so the file does not parse, as
// Python's tomllib refuses it. The planted break: readTree dropping the
// mark for every kind.
func TestToolVersionPyprojectKeepsAByteOrderMark(t *testing.T) {
	got := refusal(t, map[string]string{"pyproject.toml": "\ufeff[project]\nrequires-python = \">=3.10\"\n"}, Spec{Tool: "python", Files: []string{"pyproject.toml"}})
	if !strings.HasPrefix(got, "pyproject.toml: ") {
		t.Errorf("got %q, want the TOML parser's refusal of pyproject.toml", got)
	}
	if !strings.Contains(got, "U+00EF") {
		t.Errorf("got %q, want the refusal to name the mark's first byte, U+00EF", got)
	}
}

// A file of one version that holds none, empty or only comments, is one its
// tool refuses, so the rule is unknown, also among other files in the
// search; a file of many tools that pins none of this one gives nothing.
// The planted break: FromData without the single check.
func TestToolVersionRefusesAFileOfNoVersion(t *testing.T) {
	for _, c := range []struct{ name, tool, body string }{
		{".nvmrc", "node", ""}, {".nvmrc", "node", "# lts\n\n"}, {".python-version", "python", "\n"},
		{"runtime.txt", "python", "# none\n"}, {"VERSION", "x", " \n"}, {"rust-toolchain", "rust", "  \n"},
		{".bun-version", "bun", "\ufeff"},
	} {
		got := refusal(t, map[string]string{c.name: c.body, ".tool-versions": c.tool + " 1\n"}, Spec{Tool: c.tool, Files: []string{".tool-versions", c.name}})
		if want := c.name + ": line 1: the file holds no version, only blank lines and comments"; got != want {
			t.Errorf("%s %q: got %q", c.name, c.body, got)
		}
	}
	got := refusal(t, map[string]string{".nvmrc": "\n", ".tool-versions": "nodejs 20\n"}, Spec{Tool: "node"})
	if !strings.HasPrefix(got, ".nvmrc: line 1: the file holds no version") {
		t.Errorf("in the search: %q", got)
	}
	if got := values(t, map[string]string{".tool-versions": "python 3\n", ".sdkmanrc": "", "go.mod": "module m\n"}, Spec{Tool: "node", Files: []string{".tool-versions", ".sdkmanrc"}}); got != "" {
		t.Errorf("files of many tools: %q", got)
	}
}

// Each word the README lists as not literal is marked, and the versions
// around them are not.
func TestToolVersionNotLiteral(t *testing.T) {
	for v, want := range map[string]bool{
		"system": true, "latest": true, "stable": true, "unstable": true, "beta": true, "nightly": true,
		"node": true, "iojs": true, "lts": true, "default": true, "rolling": true, "min-required": true,
		"last_green": true, "last_rc": true, "last_downstream_green": true,
		"lts/*": true, "lts/iron": true, "latest:18": true, "latest-1": true, "latest-allowed": true,
		"ref:v1.2": true, "path:~/x": true, "prefix:1.2": true, "sub-1:latest": true,
		"stable-x86_64-unknown-linux-gnu": true, "nightly-aarch64-apple-darwin": true, "beta-": true,
		"nightly-2024-01-01": false, "beta-2024-02-29-x86_64-unknown-linux-gnu": false, "nightly-2024-0x-01": true,
		"20": false, "20.11.1": false, "v20.11.1": false, ">=20": false, "ruby-3.3.0": false, "go1.22.3": false,
		"1.75.0": false, "21.0.2-tem": false, "pypy3.10-7.3.15": false, "systemd": false, "lts2": false,
	} {
		if got := notLiteral(v); got != want {
			t.Errorf("%q: not literal %v, want %v", v, got, want)
		}
	}
}

// Every version is one value: a .tool-versions line of more than
// core.MaxValues versions is refused at the limit, and the versions of all
// files hold at most max_bytes in all, as the files read do. The planted
// breaks: the value limit left out of add, the sum of values left out, and
// the sum of bytes read left out.
func TestToolVersionLimits(t *testing.T) {
	many := "node" + strings.Repeat(" 1", core.MaxValues+1) + "\n"
	got := refusal(t, map[string]string{".tool-versions": many}, Spec{Tool: "node"})
	if !strings.Contains(got, ".tool-versions: the extractor selects more than 100000 values, the limit") {
		t.Errorf("value limit: %q", got)
	}
	// The read stops at the limit: a control character on the line after
	// it is never read.
	got = refusal(t, map[string]string{".tool-versions": many + "node \x01\n"}, Spec{Tool: "node"})
	if !strings.Contains(got, "more than 100000 values, the limit") {
		t.Errorf("the read went past the value limit: %q", got)
	}
	got = refusal(t, map[string]string{".tool-versions": "node 1234567\n", ".nvmrc": "1234567\n"}, Spec{Tool: "node", MaxBytes: 13})
	if !strings.Contains(got, ".nvmrc: the files the extractor reads hold more than 13 bytes in all, the extractor's max_bytes") {
		t.Errorf("bytes read in all: %q", got)
	}
	// Links to one file read it once, so only the versions they repeat can
	// pass max_bytes: 8 bytes read, 21 bytes of versions.
	root := extracttest.Repo(t, map[string]string{".nvmrc": "1234567\n"})
	extracttest.Symlink(t, "../.nvmrc", filepath.Join(root, "a", ".nvmrc"))
	extracttest.Symlink(t, "../.nvmrc", filepath.Join(root, "b", ".nvmrc"))
	_, err := extracttest.Run(t, build, Spec{Tool: "node", MaxBytes: 13}, root)
	extracttest.WantError(t, err, "a/.nvmrc: the versions the extractor selects hold more than 13 bytes in all, the extractor's max_bytes")
	if got, err := extracttest.Run(t, build, Spec{Tool: "node", MaxBytes: 21}, root); err != nil || len(got) != 3 {
		t.Errorf("at 21 bytes of versions, max_bytes: %s, %v", marked(got), err)
	}
	// At each limit exactly, the versions are read.
	if got := extracttest.MustRun(t, build, Spec{Tool: "node"}, extracttest.Repo(t, map[string]string{".tool-versions": "node" + strings.Repeat(" 1", core.MaxValues) + "\n"})); len(got) != core.MaxValues {
		t.Errorf("%d values at the value limit", len(got))
	}
	if got := values(t, map[string]string{".tool-versions": "node 1\n", ".nvmrc": "12345\n"}, Spec{Tool: "node", MaxBytes: 13}); got != "1@.tool-versions:1 12345@.nvmrc:1" {
		t.Errorf("at max_bytes in all: %q", got)
	}
	got = refusal(t, map[string]string{".nvmrc": "123456789\n"}, Spec{Tool: "node", MaxBytes: 8})
	if !strings.Contains(got, "read .nvmrc: the file is 10 bytes, over the limit of 8 bytes") {
		t.Errorf("file size: %q", got)
	}
	got = refusal(t, map[string]string{"package.json": "[[[[[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]]]]]]"}, Spec{Tool: "node", Files: []string{"package.json"}, MaxBytes: 64})
	if !strings.Contains(got, "package.json: the file can hold up to") {
		t.Errorf("node limit: %q", got)
	}
}

// A go.mod is refused before modfile parses it when it can hold more lines
// and tokens than max_bytes over 64, and read at the limit: "module m",
// "go 1.22" and their two line ends are seven with the start. The planted
// break: readGoMod without the check.
func TestToolVersionGoModNodeLimit(t *testing.T) {
	repo := map[string]string{"go.mod": "module m\ngo 1.22\n"}
	if got := values(t, repo, Spec{Tool: "go", MaxBytes: 7 * 64}); got != "1.22@go.mod:2" {
		t.Errorf("at the limit: %q", got)
	}
	got := refusal(t, repo, Spec{Tool: "go", MaxBytes: 7*64 - 1})
	if got != "go.mod: the file can hold up to 7 lines and tokens, over the limit of 6, one for every 64 bytes of max_bytes; set max_bytes: higher to read a file this dense" {
		t.Errorf("over the limit: %q", got)
	}
	for _, c := range []struct {
		body string
		want int
	}{
		{"", 1}, {"a", 2}, {" a  b\t\r\n", 4}, {"a(b)\n(", 7}, {"\u00a0x", 2},
		// Each byte modfile makes a token of, with no blank around it, and
		// a token right after one.
		{"x ,,,", 5}, {"[]{}", 5}, {`"a""b"`, 7}, {"`a`b", 5}, {"retract [v1.0.0, v1.1.0]", 7},
	} {
		if got := goModNodes([]byte(c.body)); got != c.want {
			t.Errorf("%q: %d nodes, want %d", c.body, got, c.want)
		}
	}
}

// A go.mod and a .go-version that pin one Go give one text, so a rule over
// them agrees: the toolchain's go is left out, and the go directive below a
// toolchain is no pin. The planted break: readGoMod keeping the go prefix.
func TestToolVersionGoModAgreesWithGoVersion(t *testing.T) {
	repo := map[string]string{
		"go.mod":         "module m\n\ngo 1.22.0\n\ntoolchain go1.22.3\n",
		".go-version":    "1.22.3\n",
		".tool-versions": "golang 1.22.3\n",
	}
	got := extracttest.MustRun(t, build, Spec{Tool: "go"}, extracttest.Repo(t, repo))
	if len(got) != 3 {
		t.Fatalf("got %s", marked(got))
	}
	for _, v := range got {
		if v.Text != "1.22.3" {
			t.Errorf("%s:%d gives %q, not 1.22.3", v.At.File, v.At.Line, v.Text)
		}
	}
}

// A panic in modfile makes the rule unknown with the file named, not the
// run end. No input found makes it panic, so the parser is replaced.
func TestToolVersionGoModPanicIsARefusal(t *testing.T) {
	defer func(p func(string, []byte, modfile.VersionFixer) (*modfile.File, error)) { parseGoMod = p }(parseGoMod)
	parseGoMod = func(string, []byte, modfile.VersionFixer) (*modfile.File, error) { panic("index out of range") }
	got := refusal(t, map[string]string{"go.mod": "module m\n"}, Spec{Tool: "go"})
	if got != "go.mod: golang.org/x/mod/modfile stopped on the file: index out of range" {
		t.Errorf("got %q", got)
	}
}

// A line of another tool's versions is passed over without a slice of its
// fields: 1 MiB of them allocates less than the file. The planted break:
// eachLine splitting each line with bytes.Fields, which allocated 12 MB.
func TestToolVersionPassesOverALineWithoutItsFields(t *testing.T) {
	e, err := build(Spec{Tool: "node", Files: []string{".tool-versions"}})
	if err != nil {
		t.Fatal(err)
	}
	x := e.(extractor)
	data := []byte("python" + strings.Repeat(" 1", 1<<19) + "\nnode 20\n")
	var got []check.Value
	if n := extracttest.Allocated(func() { got, err = x.FromData(x.files[0], data) }); n > 1<<20 {
		t.Errorf("allocated %d bytes", n)
	}
	if err != nil || marked(got) != "20@.tool-versions:2" {
		t.Errorf("got %s, %v", marked(got), err)
	}
}

// A walk that finds 100 links to one file reads it once: the replay of the
// security review's shape, a .tool-versions of another tool's lines linked
// from 100 directories, at 64 KiB under a max_bytes of 1 MiB, which 100
// reads would pass. The planted break: Extract reading each link.
func TestToolVersionReadsALinkedFileOnce(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{".tool-versions": strings.Repeat("python 3\n", 64<<10/9), ".nvmrc": "20\n"})
	for i := range 100 {
		extracttest.Symlink(t, "../.tool-versions", filepath.Join(root, fmt.Sprintf("d%03d", i), ".tool-versions"))
	}
	extracttest.Symlink(t, "../.nvmrc", filepath.Join(root, "d050", ".nvmrc"))
	got, err := extracttest.Run(t, build, Spec{Tool: "node", MaxBytes: 1 << 20}, root)
	if err != nil || marked(got) != "20@.nvmrc:1 20@d050/.nvmrc:1" {
		t.Errorf("got %s, %v", marked(got), err)
	}
}

// The values limit holds across files: the extractor stops at the file that
// passes it.
func TestToolVersionValueLimitAcrossFiles(t *testing.T) {
	half := strings.Repeat(" 1", core.MaxValues/2+1)
	two := map[string]string{"a/.tool-versions": "node" + half + "\n", "b/.tool-versions": "node" + half + "\n"}
	got := refusal(t, two, Spec{Tool: "node", Files: []string{"a/.tool-versions", "b/.tool-versions"}})
	if !strings.Contains(got, "b/.tool-versions: the extractor selects more than 100000 values") {
		t.Errorf("got %q", got)
	}
	// Exactly the limit across the two is read.
	half = strings.Repeat(" 1", core.MaxValues/2)
	two = map[string]string{"a/.tool-versions": "node" + half + "\n", "b/.tool-versions": "node" + half + "\n"}
	if n := len(extracttest.MustRun(t, build, Spec{Tool: "node", Files: []string{"a/.tool-versions", "b/.tool-versions"}}, extracttest.Repo(t, two))); n != core.MaxValues {
		t.Errorf("%d values at the limit across files", n)
	}
}

// A one-line rust-toolchain over the TOML node limit is refused before the
// trial TOML parse, which would build every node of it first: the read
// allocates less than the file. The planted break: readRustToolchain without
// its CheckNodes, which took 3.7 GB for a line of 32 MiB.
func TestToolVersionRustToolchainNodeLimitComesFirst(t *testing.T) {
	e, err := build(Spec{Tool: "rust", Files: []string{"rust-toolchain"}, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	x := e.(extractor)
	data := []byte("a=[" + strings.Repeat("1,", 1<<19-4) + "1]\n")
	if n := extracttest.Allocated(func() { _, err = x.FromData(x.files[0], data) }); n > uint64(len(data)) {
		t.Errorf("allocated %d bytes for a file of %d", n, len(data))
	}
	extracttest.WantError(t, err, "rust-toolchain: the file can hold up to")
}

// A Parsers without a reader refuses the file that needs it rather than
// crash: a test building the extractor alone can leave them out.
func TestToolVersionWithoutParsers(t *testing.T) {
	e, err := Build(Spec{Tool: "rust", Files: []string{"rust-toolchain.toml"}}, Parsers{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Extract(extracttest.Repo(t, map[string]string{"rust-toolchain.toml": "[toolchain]\n"}))
	extracttest.WantError(t, err, "rust-toolchain.toml: errata was built without the reader for rust-toolchain.toml")
}

// Place keeps the tool and the cleaned files, and drops max_bytes.
func TestToolVersionPlace(t *testing.T) {
	p := Place(Spec{Tool: "node", Files: []string{"./a/../.nvmrc"}, MaxBytes: 3})
	if p.Tool != "node" || len(p.Files) != 1 || p.Files[0] != ".nvmrc" || p.MaxBytes != 0 {
		t.Errorf("got %+v", p)
	}
	a := Place(Spec{Tool: "node", Files: []string{".nvmrc", "./.tool-versions"}})
	b := Place(Spec{Tool: "node", Files: []string{".tool-versions", ".nvmrc"}})
	if !slices.Equal(a.Files, b.Files) {
		t.Errorf("one set of files in two orders: %v and %v", a.Files, b.Files)
	}
	if Place(Spec{Tool: "node"}).Files != nil {
		t.Error("the walk has files in its place")
	}
}

// The extractor declares that it can return a not literal value, so
// skip_not_literal loads over it.
func TestToolVersionMayReturnNotLiteral(t *testing.T) {
	e, err := build(Spec{Tool: "node"})
	if err != nil {
		t.Fatal(err)
	}
	if !check.MayReturnNotLiteral(e) {
		t.Error("MayReturnNotLiteral is false")
	}
}

// writeFile writes body to name under root, for the tests that write over
// one repository.
func writeFile(t interface{ Fatal(...any) }, root, name, body string) {
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
