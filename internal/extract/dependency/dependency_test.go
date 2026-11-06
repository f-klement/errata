package dependency

import (
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/toml"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// parsers are the facade's: the json, toml and yaml readers' own.
var parsers = Parsers{
	JSON: json.Parse,
	TOML: toml.Parse,
	YAML: func(data []byte, expanded int) ([]*core.Node, int, error) {
		return yaml.ParseNoted(data, expanded, nil)
	},
}

// build is Build with the facade's parsers.
func build(s Spec) (check.Extractor, error) { return Build(s, parsers) }

// marked writes each value as text@file:line [origin], with a ! after the
// text of a value marked not literal.
func marked(vs []check.Value) string {
	var out []string
	for _, v := range vs {
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		out = append(out, v.Text+mark+"@"+v.At.File+":"+strconv.Itoa(v.At.Line)+" ["+v.Origin+"]")
	}
	return strings.Join(out, "; ")
}

// values is what s extracts from a git repository of files, marked.
func values(t *testing.T, files map[string]string, s Spec) string {
	t.Helper()
	return marked(extracttest.MustRun(t, build, s, extracttest.Tracked(t, files)))
}

// refusal is the error s gives on a git repository of files.
func refusal(t *testing.T, files map[string]string, s Spec) string {
	t.Helper()
	got, err := extracttest.Run(t, build, s, extracttest.Tracked(t, files))
	if err == nil {
		t.Fatalf("no error, values %s", marked(got))
	}
	return err.Error()
}

// of is a spec of name over files.
func of(name string, files ...string) Spec { return Spec{Name: name, Files: files} }

// readCase is one file read for one package, and the values it gives.
type readCase struct {
	what, pkg, file, body, want string
}

// run reads each case alone through files:.
func run(t *testing.T, cases []readCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			if got := values(t, map[string]string{c.file: c.body}, of(c.pkg, c.file)); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

// go.mod gives a require's version and a replace's, and a replace by a
// directory or by a fork as not literal, and none for a replace of a
// version the file does not require; go.work gives its replaces. Planted
// breaks: replaces taking the old path's version, replaces counting a
// replace of a version nobody requires, replaces taking a fork's version
// as literal; a value after a lone CR numbered by modfile's line.
func TestDependencyReadsGo(t *testing.T) {
	gomod := "module example.com/m\n\ngo 1.22\n\nrequire (\n\tgolang.org/x/mod v0.42.0\n\tgolang.org/x/net v0.62.0 // indirect\n)\n\nreplace golang.org/x/mod v0.42.0 => golang.org/x/mod v0.42.1\n"
	run(t, []readCase{
		{"require and replace", "golang.org/x/mod", "go.mod", gomod,
			"v0.42.0@go.mod:6 [go.mod require (resolved)]; v0.42.1@go.mod:10 [go.mod replace (resolved)]"},
		{"an indirect require", "golang.org/x/net", "tools/go.mod", gomod, "v0.62.0@tools/go.mod:7 [go.mod require (resolved)]"},
		{"another package", "golang.org/x/text", "go.mod", gomod, ""},
		{"the module's own path is no require", "example.com/m", "go.mod", gomod, ""},
		{"a replace by a directory", "example.com/lib", "go.mod", "module m\nrequire example.com/lib v1.0.0\nreplace example.com/lib => ../lib\n",
			"v1.0.0@go.mod:2 [go.mod require (resolved)]; ../lib!@go.mod:3 [go.mod replace (resolved)]"},
		{"a replace of a version the file does not require", "x", "go.mod", "module m\nrequire x v1.2.0\nreplace x v1.1.0 => x v1.0.0\n",
			"v1.2.0@go.mod:2 [go.mod require (resolved)]"},
		{"a replace by a fork", "x", "go.mod", "module m\nrequire x v1.2.0\nreplace x => github.com/fork/x v0.5.0\n",
			"v1.2.0@go.mod:2 [go.mod require (resolved)]; v0.5.0!@go.mod:3 [go.mod replace (resolved)]"},
		{"go.work", "example.com/lib", "go.work", "go 1.22\n\nuse ./a\n\nreplace example.com/lib v1.0.0 => example.com/lib v1.0.2\n",
			"v1.0.2@go.work:5 [go.work replace (resolved)]"},
		// modfile reads a lone CR as a blank, the line is errata's.
		{"a require after a lone CR", "x", "go.mod", "\rrequire x v1.0.0\n", "v1.0.0@go.mod:2 [go.mod require (resolved)]"},
		{"a require after a lone CR on line 2", "x", "go.mod", "module m\n\rrequire x v1.0.0\n", "v1.0.0@go.mod:3 [go.mod require (resolved)]"},
		{"a require a lone CR leaves in a comment", "x", "go.mod", "module m\n// c\rrequire x v1.0.0\n", ""},
	})
}

// A version a go.mod or go.work writes in a form that is not canonical is
// the text as written and not literal: the go command resolves v1 in a
// go.mod as a query, to the latest v1 release, and modfile alone would give
// v1.0.0, which the line does not hold. A replace whose version or required
// version is such a query may apply, so it is not literal either. Planted
// breaks: readGoMod and replaces without the Canonical test; replaces
// skipping a replace of a version a query may resolve to.
func TestDependencyReadsGoVersionsAsWritten(t *testing.T) {
	run(t, []readCase{
		{"a canonical version", "x", "go.mod", "module m\nrequire x v1.2.3\n", "v1.2.3@go.mod:2 [go.mod require (resolved)]"},
		{"a major alone", "x", "go.mod", "require x v1", "v1!@go.mod:1 [go.mod require (resolved)]"},
		{"a major and minor", "x", "go.mod", "module m\nrequire x v1.2\n", "v1.2!@go.mod:2 [go.mod require (resolved)]"},
		{"a pseudo-version with build metadata", "x", "go.mod", "module m\nrequire x v0.0.0-20200101000000-abcdef123456+meta\n",
			"v0.0.0-20200101000000-abcdef123456+meta!@go.mod:2 [go.mod require (resolved)]"},
		{"a replace by a short version", "x", "go.mod", "module m\nrequire x v1.2.3\nreplace x => x v1.3\n",
			"v1.2.3@go.mod:2 [go.mod require (resolved)]; v1.3!@go.mod:3 [go.mod replace (resolved)]"},
		{"a replace of the query required", "x", "go.mod", "module m\nrequire x v1\nreplace x v1 => x v1.0.1\n",
			"v1!@go.mod:2 [go.mod require (resolved)]; v1.0.1@go.mod:3 [go.mod replace (resolved)]"},
		{"a replace of a version a query may resolve to", "x", "go.mod", "module m\nrequire x v1\nreplace x v1.5.0 => x v1.0.1\n",
			"v1!@go.mod:2 [go.mod require (resolved)]; v1.0.1!@go.mod:3 [go.mod replace (resolved)]"},
		{"a replace of a query", "x", "go.mod", "module m\nrequire x v1.0.0\nreplace x v1 => x v1.0.1\n",
			"v1.0.0@go.mod:2 [go.mod require (resolved)]; v1.0.1!@go.mod:3 [go.mod replace (resolved)]"},
		{"a replace of another canonical version", "x", "go.mod", "module m\nrequire x v1.0.0\nreplace x v1.5.0 => x v1.0.1\n",
			"v1.0.0@go.mod:2 [go.mod require (resolved)]"},
		{"a replace of a query the file does not require", "x", "go.mod", "module m\nreplace x v1 => x v1.0.1\n", ""},
		{"go.work", "x", "go.work", "go 1.22\nreplace x v1 => x v1.2\n", "v1.2!@go.work:2 [go.work replace (resolved)]"},
	})
}

// module: true gives the path a go.mod's module directive declares, and
// refuses a name beside it and any file that is no go.mod. Planted break:
// readGoMod reading requires under module: true.
func TestDependencyModulePath(t *testing.T) {
	repo := map[string]string{"go.mod": "// c\nmodule github.com/f-klement/errata\n\nrequire golang.org/x/mod v0.42.0\n", "tools/go.mod": "go 1.22\n"}
	if got := values(t, repo, Spec{Module: true, Files: []string{"go.mod", "tools/go.mod"}}); got != "github.com/f-klement/errata@go.mod:2 [go.mod module]" {
		t.Errorf("got %s", got)
	}
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Module: true, Name: "x", Files: []string{"go.mod"}}, "dependency: module: true returns each go.mod's module path, so it takes no name"},
		{Spec{Module: true, Only: Range, Files: []string{"go.mod"}}, "dependency: only keeps ranges or resolved versions, and a module path is neither"},
		{Spec{Module: true, Files: []string{"go.work"}}, `dependency: module: true reads the module path of a go.mod, and "go.work" is no go.mod`},
	} {
		_, err := build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
	got := refusal(t, map[string]string{"a/go.mod": "module a\n", "a/package.json": "{}"}, Spec{Module: true, Files: []string{"a/*"}})
	if !strings.HasPrefix(got, `dependency: module: true reads the module path of a go.mod, and "a/package.json" is no go.mod`) {
		t.Errorf("a glob's file that is no go.mod: %s", got)
	}
}

// package.json gives the range of each section; one npm resolves from
// elsewhere is not literal. package-lock.json gives the version of each
// copy installed, and the package's own; pnpm and Yarn give theirs. An
// alias answers to the package it installs. Planted breaks: npmNotLiteral
// marking v1.2 and x.1 not literal, the lock reading node_modules/@a/x as x,
// pnpm's file: key read as literal, an alias read by its key, a Yarn link
// read as a version.
func TestDependencyReadsNpm(t *testing.T) {
	pkg := `{
  "name": "web",
  "version": "1.0.0",
  "dependencies": {"lodash": "^4.17.21", "left": "latest"},
  "devDependencies": {"lodash": "v4.17.21"},
  "peerDependencies": {"lodash": "x.4"},
  "optionalDependencies": {"lodash": "workspace:*"}
}`
	lock := `{
  "name": "web",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "web", "version": "1.0.0", "dependencies": {"lodash": "^4.17.21"}},
    "node_modules/lodash": {"version": "4.17.21"},
    "node_modules/a/node_modules/lodash": {"version": "3.10.1"},
    "node_modules/@scope/lodash": {"version": "9.9.9"},
    "packages/lodash": {"name": "lodash", "version": "0.1.0"},
    "node_modules/b": {"link": true, "resolved": "packages/b"}
  }
}`
	lockV1 := `{"name": "web", "lockfileVersion": 1, "dependencies": {
  "lodash": {"version": "4.17.21"},
  "a": {"version": "1.0.0", "dependencies": {"lodash": {"version": "git+https://example.com/l.git#abc"}}}
}}`
	run(t, []readCase{
		{"package.json", "lodash", "package.json", pkg,
			"^4.17.21@package.json:4 [package.json dependencies (range)]; v4.17.21@package.json:5 [package.json devDependencies (range)]; x.4@package.json:6 [package.json peerDependencies (range)]; workspace:*!@package.json:7 [package.json optionalDependencies (range)]"},
		{"a dist-tag", "left", "package.json", pkg, "latest!@package.json:4 [package.json dependencies (range)]"},
		{"the package's own version is package_version's", "web", "package.json", pkg, ""},
		{"an empty range", "a", "package.json", `{"dependencies": {"a": ""}}`, `""!@package.json:1 [package.json dependencies (range)]`},
		{"package-lock.json", "lodash", "package-lock.json", lock,
			"4.17.21@package-lock.json:7 [package-lock.json packages (resolved)]; 3.10.1@package-lock.json:8 [package-lock.json packages (resolved)]; 0.1.0@package-lock.json:10 [package-lock.json packages (resolved)]"},
		{"a scoped package", "@scope/lodash", "package-lock.json", lock, "9.9.9@package-lock.json:9 [package-lock.json packages (resolved)]"},
		{"the package's own entry", "web", "package-lock.json", lock,
			"1.0.0@package-lock.json:3 [package-lock.json version (resolved)]; 1.0.0@package-lock.json:6 [package-lock.json packages (resolved)]"},
		{"lockfile version 1", "lodash", "npm-shrinkwrap.json", lockV1,
			"4.17.21@npm-shrinkwrap.json:2 [npm-shrinkwrap.json dependencies (resolved)]; git+https://example.com/l.git#abc!@npm-shrinkwrap.json:3 [npm-shrinkwrap.json dependencies (resolved)]"},
		{"pnpm 9", "@types/node", "pnpm-lock.yaml", "lockfileVersion: '9.0'\nimporters:\n  .:\n    dependencies:\n      '@types/node':\n        specifier: ^20\n        version: 20.11.5\npackages:\n  '@types/node@20.11.5':\n    resolution: {integrity: x}\n  'other@1.0.0': {}\n",
			"20.11.5@pnpm-lock.yaml:9 [pnpm-lock.yaml packages (resolved)]"},
		{"pnpm 6", "react-dom", "pnpm-lock.yaml", "lockfileVersion: '6.0'\npackages:\n  /react-dom@18.2.0(react@18.2.0):\n    dev: false\n",
			"18.2.0@pnpm-lock.yaml:3 [pnpm-lock.yaml packages (resolved)]"},
		{"pnpm 5", "@a/b", "pnpm-lock.yaml", "lockfileVersion: 5.4\npackages:\n  /@a/b/1.2.3_react@18.2.0:\n    dev: true\n",
			"1.2.3@pnpm-lock.yaml:3 [pnpm-lock.yaml packages (resolved)]"},
		{"pnpm, a package from a path", "foo", "pnpm-lock.yaml", "lockfileVersion: '9.0'\npackages:\n  foo@file:../foo:\n    resolution: {directory: ../foo}\n",
			"file:../foo!@pnpm-lock.yaml:3 [pnpm-lock.yaml packages (resolved)]"},
		{"an alias in package.json", "zod", "package.json", `{"dependencies": {"zod-old": "npm:zod@^3.20.0", "zod": "npm:other@1"}}`,
			"^3.20.0@package.json:1 [package.json dependencies (range)]"},
		{"an alias in package-lock.json", "zod", "package-lock.json", "{\"packages\": {\n\"node_modules/zod-old\": {\"name\": \"zod\", \"version\": \"3.20.2\"}}}",
			"3.20.2@package-lock.json:2 [package-lock.json packages (resolved)]"},
		{"an alias in a version 1 lockfile", "zod", "package-lock.json", "{\"dependencies\": {\n\"zod-old\": {\"version\": \"npm:zod@3.20.2\"}}}",
			"3.20.2@package-lock.json:2 [package-lock.json dependencies (resolved)]"},
		{"an alias in yarn.lock", "zod", "yarn.lock", "\"zod-old@npm:zod@^3.20.0\":\n  version \"3.20.2\"\n",
			"3.20.2@yarn.lock:2 [yarn.lock (resolved)]"},
		{"yarn berry, a link", "foo", "yarn.lock", "__metadata:\n  version: 8\n\n\"foo@link:../foo::locator=app%40workspace%3A.\":\n  version: 0.0.0-use.local\n", ""},
		{"yarn 1", "@babel/core", "yarn.lock", "# yarn lockfile v1\n\n\n\"@babel/core@^7.0.0\", \"@babel/core@^7.12.3\":\n  version \"7.23.0\"\n  resolved \"https://x\"\n  dependencies:\n    version \"9\"\n\nother@^1:\n  version \"1.0.0\"\n",
			"7.23.0@yarn.lock:5 [yarn.lock (resolved)]"},
		{"yarn 1 unquoted", "lodash", "yarn.lock", "lodash@^4.17.0, lodash@^4.17.21:\r\n  version \"4.17.21\"\r\n", "4.17.21@yarn.lock:2 [yarn.lock (resolved)]"},
		{"yarn berry", "lodash", "yarn.lock", "__metadata:\n  version: 8\n\n\"lodash@npm:^4.17.0, lodash@npm:^4.17.21\":\n  version: 4.17.21\n  resolution: \"lodash@npm:4.17.21\"\n\n\"lodash@workspace:packages/lodash\":\n  version: 0.0.0-use.local\n",
			"4.17.21@yarn.lock:5 [yarn.lock (resolved)]"},
	})
}

// pyproject.toml gives the specifiers of each PEP 508 list and Poetry's
// tables, names compared as PEP 503 folds them; a requirements file gives
// its lines' specifiers, a pin of one version as resolved; poetry.lock and
// uv.lock their versions. Planted breaks: python comparing names as
// written, readRequirements reading a --hash option into the range, option
// seeing a space alone before an option, a pin read as a range, a .in
// file's pin read as resolved, readerFor
// passing over a requirements directory.
func TestDependencyReadsPython(t *testing.T) {
	pyproject := `[build-system]
requires = ["setuptools>=68", "wheel"]

[project]
dependencies = [
  "Requests[socks] >=2.31,<3 ; python_version >= '3.8'",
  "requests_oauthlib (>=1.3)",
  "flask @ https://example.com/flask.zip",
]

[project.optional-dependencies]
dev = ["requests==2.31.0"]

[dependency-groups]
test = ["requests>=2", {include-group = "dev"}]

[tool.poetry.dependencies]
python = "^3.11"
requests = "^2.31"

[tool.poetry.group.dev.dependencies]
Requests = { version = "^2.30", extras = ["socks"] }
other = { git = "https://example.com/o.git" }
`
	run(t, []readCase{
		{"pyproject.toml", "requests", "pyproject.toml", pyproject,
			">=2.31,<3@pyproject.toml:6 [pyproject.toml project.dependencies (range)]; ==2.31.0@pyproject.toml:12 [pyproject.toml project.optional-dependencies (range)]; >=2@pyproject.toml:15 [pyproject.toml dependency-groups (range)]; ^2.31@pyproject.toml:19 [pyproject.toml tool.poetry.dependencies (range)]; ^2.30@pyproject.toml:22 [pyproject.toml tool.poetry.group (range)]"},
		{"parentheses and a normalized name", "Requests-OAuthlib", "pyproject.toml", pyproject, ">=1.3@pyproject.toml:7 [pyproject.toml project.dependencies (range)]"},
		{"a URL", "flask", "pyproject.toml", pyproject, "https://example.com/flask.zip!@pyproject.toml:8 [pyproject.toml project.dependencies (range)]"},
		{"build-system", "setuptools", "pyproject.toml", pyproject, ">=68@pyproject.toml:2 [pyproject.toml build-system.requires (range)]"},
		{"no specifier and no version", "other", "pyproject.toml", pyproject, ""},
		{"requirements", "requests", "requirements-dev.txt", "# pinned\n-r base.txt\nrequests==2.31.0 \\\n    --hash=sha256:abc\nflask>=3 # web\n-e git+https://x#egg=requests\n",
			"2.31.0@requirements-dev.txt:3 [requirements (resolved)]"},
		{"a tab before the options", "requests", "requirements.txt", "requests==2.31.0\t--hash=sha256:abc\nflask>=3 \\\n\t--hash=sha256:def\n",
			"2.31.0@requirements.txt:1 [requirements (resolved)]"},
		{"a tab before a continued option", "flask", "requirements.txt", "requests==2.31.0\t--hash=sha256:abc\nflask>=3 \\\n\t--hash=sha256:def\n",
			">=3@requirements.txt:2 [requirements (range)]"},
		{"a requirements directory", "django", "requirements/base.txt", "Django>=5.0,<5.1\n", ">=5.0,<5.1@requirements/base.txt:1 [requirements (range)]"},
		{"pip-tools' input", "django", "requirements.in", "django~=5.0\n", "~=5.0@requirements.in:1 [requirements (range)]"},
		{"a pin in pip-tools' input is a request", "django", "requirements/base.in", "django==5.0.1\n", "==5.0.1@requirements/base.in:1 [requirements (range)]"},
		{"a wildcard pin is a range", "django", "requirements.txt", "django==5.0.*\n", "==5.0.*@requirements.txt:1 [requirements (range)]"},
		{"a variable pip expands", "django", "requirements.txt", "django==${DJANGO_VERSION}\n", "==${DJANGO_VERSION}!@requirements.txt:1 [requirements (range)]"},
		{"uv's dev-dependencies", "pytest", "pyproject.toml", "[tool.uv]\ndev-dependencies = [\"pytest>=8\"]\n", ">=8@pyproject.toml:2 [pyproject.toml tool.uv.dev-dependencies (range)]"},
		{"PDM's dev-dependencies", "pytest", "pyproject.toml", "[tool.pdm.dev-dependencies]\ntest = [\"pytest>=8\"]\n", ">=8@pyproject.toml:2 [pyproject.toml tool.pdm.dev-dependencies (range)]"},
		{"constraints", "flask", "constraints.txt", "Flask >= 3.0 ; python_version > '3'\n", ">= 3.0@constraints.txt:1 [requirements (range)]"},
		// A requirement a backslash joins over lines is at the line its
		// value starts on.
		{"a specifier on the line after the name", "x", "constraints.txt", "a==1\nx \\\n  >=1 ; os_name == 'nt'\n", ">=1@constraints.txt:3 [requirements (range)]"},
		{"a name after a joined blank line", "x", "constraints.txt", "\\\nx!\n", "!@constraints.txt:2 [requirements (range)]"},
		{"a pin's version on the line after the ==", "x", "requirements.txt", "x== \\\r\n1.0 \\\r\n --hash=sha256:0\r\n", "1.0@requirements.txt:2 [requirements (resolved)]"},
		{"a URL on the line after the @", "x", "requirements.txt", "x @\\\n https://h/x.zip\n", "https://h/x.zip!@requirements.txt:2 [requirements (range)]"},
		{"parentheses over lines", "x", "requirements.txt", "x (\\\n>=1)\n", ">=1@requirements.txt:2 [requirements (range)]"},
		{"a specifier pip joins with nothing between", "x", "requirements.txt", "x>=1\\\n.0,\\\n<2\n", ">=1.0,<2@requirements.txt:1 [requirements (range)]"},
		{"a marker on a later line", "x", "requirements.txt", "x[a]~=3.1 \\\nX >= 2 ; python_version > '3'", "~=3.1 X >= 2@requirements.txt:1 [requirements (range)]"},
		{"a pin and a marker on later lines", "x", "requirements.txt", "x ==\\\n 1.0 \\\n; os_name == 'nt'\n", "1.0@requirements.txt:2 [requirements (resolved)]"},
		{"parentheses with a blank inside", "x", "requirements.txt", "x ( >=1 ) ; os_name == 'nt'\n", ">=1@requirements.txt:1 [requirements (range)]"},
		// Each expected reading below is pip's own, from req_file's
		// join_lines and ignore_comments run on the input.
		{"a backslash on the last line", "x", "requirements.txt", "x>=1 \\", ">=1@requirements.txt:1 [requirements (range)]"},
		{"a backslash on the last line before its end", "x", "requirements.txt", "a==1\nx>=1 \\\n", ">=1@requirements.txt:2 [requirements (range)]"},
		{"a comment the backslash joins the next line to", "y", "requirements.txt", "x # c \\\ny>=1\n", ""},
		{"a comment line ends a join", "x", "requirements.txt", "x>=1 \\\n# c \\\nx<2\n", ">=1@requirements.txt:1 [requirements (range)]; <2@requirements.txt:3 [requirements (range)]"},
		{"a comment line right after a join", "x", "requirements.txt", "x>=1\\\n# c\n", ">=1@requirements.txt:1 [requirements (range)]"},
		{"every backslash at the end is stripped", "x", "requirements.txt", "x>=1\\\\\n.0\n", ">=1.0@requirements.txt:1 [requirements (range)]"},
		{"poetry.lock", "requests", "poetry.lock", "[[package]]\nname = \"requests\"\nversion = \"2.31.0\"\n\n[[package]]\nname = \"idna\"\nversion = \"3.6\"\n",
			"2.31.0@poetry.lock:3 [poetry.lock (resolved)]"},
		{"uv.lock", "my_app", "uv.lock", "version = 1\n\n[[package]]\nname = \"my-app\"\nversion = \"0.1.0\"\nsource = { editable = \".\" }\n",
			"0.1.0@uv.lock:5 [uv.lock (resolved)]"},
	})
}

// Cargo.toml gives each table's range, a renamed dependency by its
// package, and an inherited one as not literal unless its workspace is the
// same file; Cargo.lock gives every package's version. Planted break:
// cargo.dep adding an inherited version beside the workspace's own.
func TestDependencyReadsCargo(t *testing.T) {
	member := `[package]
name = "cli"

[dependencies]
serde = { version = "1.0.190", features = ["derive"] }
json = { package = "serde_json", version = "1" }
local = { path = "../local" }

[dev-dependencies]
serde = { workspace = true }

[target.'cfg(unix)'.build-dependencies]
serde = "1.0"
`
	root := "[workspace.dependencies]\nserde = \"1.0.200\"\n\n[dependencies]\nserde.workspace = true\n"
	run(t, []readCase{
		{"Cargo.toml", "serde", "crates/cli/Cargo.toml", member,
			"^1.0.190@crates/cli/Cargo.toml:5 [Cargo.toml dependencies (range)]; serde = { workspace = true }!@crates/cli/Cargo.toml:10 [Cargo.toml dev-dependencies (range)]; ^1.0@crates/cli/Cargo.toml:13 [Cargo.toml target build-dependencies (range)]"},
		{"inherits by a dotted key, after a tab", "serde", "Cargo.toml", "[dependencies]\nserde.workspace\t= true # shared\n",
			"serde.workspace\t= true # shared!@Cargo.toml:2 [Cargo.toml dependencies (range)]"},
		{"inherits in a table of its own", "serde", "Cargo.toml", "[dependencies.serde]\n  workspace = true\n  features = [\"derive\"]\n",
			"workspace = true!@Cargo.toml:2 [Cargo.toml dependencies (range)]"},
		{"a renamed dependency", "serde_json", "Cargo.toml", member, "^1@Cargo.toml:6 [Cargo.toml dependencies (range)]"},
		{"a path dependency", "local", "Cargo.toml", member, ""},
		{"values in the order of their lines", "serde", "Cargo.toml", "[dependencies]\nserde = \"1\"\n[workspace.dependencies]\nserde = \"2\"\n",
			"^1@Cargo.toml:2 [Cargo.toml dependencies (range)]; ^2@Cargo.toml:4 [Cargo.toml workspace.dependencies (range)]"},
		{"the workspace's own", "serde", "Cargo.toml", root, "^1.0.200@Cargo.toml:2 [Cargo.toml workspace.dependencies (range)]"},
		{"Cargo.lock", "serde", "Cargo.lock", "\xef\xbb\xbfversion = 3\n\n[[package]]\nname = \"serde\"\nversion = \"1.0.200\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\n",
			"1.0.200@Cargo.lock:5 [Cargo.lock (resolved)]"},
	})
}

// A Gemfile gives each requirement string of the gem's statement, and a
// computed one as not literal; Gemfile.lock gives the version of each
// spec without its platform. Planted breaks: gemStarts taking gem at the
// end of another word, readGemfileLock passing over PATH, other taking a
// constant's line from the start of between, readGemfile losing between at
// the end of the file.
func TestDependencyReadsRuby(t *testing.T) {
	gemfile := `source "https://rubygems.org"
ruby "3.3.0"

gem "rails", "~> 7.1", ">= 7.1.2"
gem 'pg', require: false
gem "puma", # the server
  "~> 6.4"
group :test do
  gem("rspec", RSPEC_VERSION)
end
gem "sidekiq", "#{SIDEKIQ}", "require" => false
x = "gem"; gem "redis", '5.0' # gem "redis", "4"
puts "gem", "rails", "1"
xgem "rails", "9"
`
	lock := `GEM
  remote: https://rubygems.org/
  specs:
    nokogiri (1.16.2-x86_64-linux)
      racc (~> 1.4)
    racc (1.7.3)
    rails (7.1.3)

PLATFORMS
  x86_64-linux

PATH
  remote: .
  specs:
    larder (0.1.0)

DEPENDENCIES
  rails (~> 7.1)
`
	run(t, []readCase{
		{"two requirements", "rails", "Gemfile", gemfile, "~> 7.1@Gemfile:4 [Gemfile (range)]; >= 7.1.2@Gemfile:4 [Gemfile (range)]"},
		{"options only", "pg", "Gemfile", gemfile, ""},
		{"a comment between", "puma", "Gemfile", gemfile, "~> 6.4@Gemfile:7 [Gemfile (range)]"},
		{"a constant", "rspec", "Gemfile", gemfile, "RSPEC_VERSION!@Gemfile:9 [Gemfile (range)]"},
		{"interpolation and a hash key", "sidekiq", "Gemfile", gemfile, "#{SIDEKIQ}!@Gemfile:11 [Gemfile (range)]"},
		{"after a ;", "redis", "Gemfile", gemfile, "==5.0@Gemfile:12 [Gemfile (range)]"},
		{"a constant after a comment", "rails", "Gemfile", "gem \"rails\", # pinned\n  RAILS_VERSION\n", "RAILS_VERSION!@Gemfile:2 [Gemfile (range)]"},
		{"a constant after a comment at the end", "x", "Gemfile", "gem \"x\", # c\n# d\nV", "V!@Gemfile:3 [Gemfile (range)]"},
		{"a lone CR ends the line before a requirement", "rails", "Gemfile", "gem \"rails\",\r  \"~> 7.1\"", "~> 7.1@Gemfile:2 [Gemfile (range)]"},
		{"an exact version", "rails", "Gemfile", "gem \"rails\", \"= 7.1\", \"7.1\"\n", "==7.1@Gemfile:1 [Gemfile (range)]; ==7.1@Gemfile:1 [Gemfile (range)]"},
		{"Gemfile.lock", "nokogiri", "Gemfile.lock", lock, "1.16.2@Gemfile.lock:4 [Gemfile.lock specs (resolved)]"},
		{"a dependency's range is not read", "racc", "Gemfile.lock", lock, "1.7.3@Gemfile.lock:6 [Gemfile.lock specs (resolved)]"},
		{"PATH", "larder", "Gemfile.lock", lock, "0.1.0@Gemfile.lock:15 [Gemfile.lock specs (resolved)]"},
		{"DEPENDENCIES is not read", "rails", "Gemfile.lock", lock, "7.1.3@Gemfile.lock:7 [Gemfile.lock specs (resolved)]"},
	})
	got := refusal(t, map[string]string{"Gemfile": "gem \"a\", \"1\n"}, of("a", "Gemfile"))
	if !strings.HasPrefix(got, "Gemfile: line 1: a string opens here and runs to the end of the file") {
		t.Errorf("a string that runs off: %s", got)
	}
}

// composer.json gives require's and require-dev's ranges, names in any
// case; composer.lock the versions installed.
func TestDependencyReadsComposer(t *testing.T) {
	run(t, []readCase{
		{"composer.json", "monolog/monolog", "composer.json", "{\n  \"require\": {\"php\": \">=8.1\", \"Monolog/Monolog\": \"^3.5\"},\n  \"require-dev\": {\"monolog/monolog\": \"self.version\"}\n}\n",
			"^3.5@composer.json:2 [composer.json require (range)]; self.version!@composer.json:3 [composer.json require-dev (range)]"},
		{"Composer's ranges", "a/b", "composer.json", "{\"require\": {\"a/b\": \"~1.2 | 7.1 || ^3.0@dev | =2.0\"}, \"require-dev\": {\"a/b\": \">= 1.0, <2.0\"}}",
			"~>1.2 || ==7.1 || ^3.0 || ==2.0@composer.json:1 [composer.json require (range)]; >=1.0 <2.0@composer.json:1 [composer.json require-dev (range)]"},
		{"a branch", "a/b", "composer.json", "{\"require\": {\"a/b\": \"dev-main\"}, \"require-dev\": {\"a/b\": \"2.x-dev\"}}",
			"dev-main!@composer.json:1 [composer.json require (range)]; 2.x-dev!@composer.json:1 [composer.json require-dev (range)]"},
		{"a branch installed", "a/b", "composer.lock", "{\"packages\": [{\"name\": \"a/b\", \"version\": \"dev-main\"}]}",
			"dev-main!@composer.lock:1 [composer.lock packages (resolved)]"},
		{"composer.lock", "monolog/monolog", "composer.lock", "{\n  \"packages\": [\n    {\"name\": \"monolog/monolog\", \"version\": \"3.5.0\"}\n  ],\n  \"packages-dev\": [\n    {\"name\": \"monolog/monolog\",\n     \"version\": \"v3.5.0\"}\n  ]\n}\n",
			"3.5.0@composer.lock:3 [composer.lock packages (resolved)]; v3.5.0@composer.lock:7 [composer.lock packages-dev (resolved)]"},
	})
}

// One extractor reads a manifest and its lockfile, a glob's files in git's
// order, and only: keeps one role. Planted break: add ignoring only.
func TestDependencyAcrossFiles(t *testing.T) {
	repo := map[string]string{
		"package.json":        `{"dependencies": {"lodash": "^4.17.0"}}`,
		"package-lock.json":   `{"packages": {"node_modules/lodash": {"version": "4.17.21"}}}`,
		"web/package.json":    `{"devDependencies": {"lodash": "^4.16.0"}}`,
		"docs/README.md":      "lodash 4",
		"api/requirements.in": "x",
	}
	all := of("lodash", "package.json", "package-lock.json", "web/*.json")
	want := "^4.17.0@package.json:1 [package.json dependencies (range)]; 4.17.21@package-lock.json:1 [package-lock.json packages (resolved)]; ^4.16.0@web/package.json:1 [package.json devDependencies (range)]"
	if got := values(t, repo, all); got != want {
		t.Errorf("all:\ngot  %s\nwant %s", got, want)
	}
	all.Only = Resolved
	if got := values(t, repo, all); got != "4.17.21@package-lock.json:1 [package-lock.json packages (resolved)]" {
		t.Errorf("only resolved: %s", got)
	}
	all.Only = Range
	if got := values(t, repo, all); strings.Contains(got, "resolved") || !strings.Contains(got, "^4.16.0") {
		t.Errorf("only range: %s", got)
	}
	got := refusal(t, repo, of("lodash", "docs/*"))
	if !strings.HasPrefix(got, `dependency: errata does not know how "docs/README.md" names a package's version; it reads go.mod, go.work, package.json`) {
		t.Errorf("a glob's file no reader reads: %s", got)
	}
}

// A spec that cannot read loads with a message naming why. Each refusal is
// a planted break of its check.
func TestDependencyRefusesSpecs(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Files: []string{"go.mod"}}, "dependency: name is required"},
		{Spec{Name: "x"}, "dependency: files is required"},
		{Spec{Name: "a b", Files: []string{"go.mod"}}, `dependency: name "a b" holds a blank`},
		{Spec{Name: "a\x1b", Files: []string{"go.mod"}}, `dependency: name "a\x1b" holds a blank or a character a terminal acts on`},
		{Spec{Name: strings.Repeat("a", maxName+1), Files: []string{"go.mod"}}, "dependency: name is 1025 bytes long, over the limit of 1024"},
		{Spec{Name: strings.Repeat("a", maxName), Files: []string{"go.mod"}}, ""},
		{Spec{Name: "x", Only: "locked", Files: []string{"go.mod"}}, `dependency: only is "locked"; write range`},
		{Spec{Name: "x", Files: []string{"setup.py"}}, `dependency: errata does not know how "setup.py" names a package's version`},
		{Spec{Name: "x", Files: []string{"../go.mod"}}, "dependency: file \"../go.mod\" is outside the repository"},
		{Spec{Name: "x", Files: []string{"go.mod", "./go.mod"}}, "dependency: files names \"./go.mod\" twice"},
	} {
		_, err := build(c.spec)
		if c.want == "" {
			if err != nil {
				t.Errorf("%q: %v, want it to load", c.spec.Name, err)
			}
			continue
		}
		extracttest.WantError(t, err, c.want)
	}
}

// A file that does not parse, or holds a value of the wrong type where its
// tool wants a string, makes the rule unknown with the file and line
// named. Planted break: text taking a number.
func TestDependencyRefusesFiles(t *testing.T) {
	for _, c := range []struct {
		file, body, want string
	}{
		{"package.json", `{"dependencies": {"x": 1}}`, "package.json: line 1: dependencies of the package is a number, not a string"},
		{"package.json", `{"dependencies": `, "package.json: "},
		{"package.json", `[]`, "package.json: line 1: the file is no mapping of keys, as package.json is"},
		{"Cargo.toml", "[dependencies]\nx = { version = 1 }\n", "Cargo.toml: line 2: version is a number, not a string"},
		// A TOML date or time is no string (#168); planted break: text
		// without its FormDateTime refusal, which read the date's text.
		{"Cargo.toml", "[dependencies]\nx = 1979-05-27\n", "Cargo.toml: line 2: dependencies of the package is a date or a time, not a string, which the tool that reads the file refuses"},
		{"Cargo.toml", "[workspace.dependencies]\nx = { version = 1979-05-27 }\n", "Cargo.toml: line 2: version is a date or a time, not a string"},
		{"Cargo.lock", "[[package]]\nname = \"x\"\nversion = 1979-05-27\n", "Cargo.lock: line 3: version is a date or a time, not a string"},
		{"pyproject.toml", "[tool.poetry.dependencies]\nx = 07:32:00\n", "pyproject.toml: line 2: tool.poetry.dependencies of the package is a date or a time, not a string"},
		{"pyproject.toml", "[project]\ndependencies = [1979-05-27]\n", "pyproject.toml: line 2: a requirement of project.dependencies is a date or a time, not a string"},
		{"poetry.lock", "[[package]]\nname = \"x\"\nversion = 1979-05-27 07:32:00\n", "poetry.lock: line 3: version is a date or a time, not a string"},
		{"pyproject.toml", "[project]\ndependencies = \"x\"\n", "pyproject.toml: line 2: project.dependencies is a string, not a list of requirements"},
		{"pyproject.toml", "\xef\xbb\xbf[project]\n", "pyproject.toml: "},
		{"go.mod", "module m\nfrob x\n", "go.mod:2: unknown directive: frob"},
		{"composer.lock", `{"packages": [{"name": "x", "version": ["1"]}]}`, "composer.lock: line 1: version is a list, not a string"},
		{"pnpm-lock.yaml", "- a\n", "pnpm-lock.yaml: line 1: the file is no mapping of keys, as pnpm-lock.yaml is"},
		{"package-lock.json", `{"dependencies": {"a": {"version": "npm:x@1\u001b"}}}`, `package-lock.json: line 1: the version "1\x1b" holds a control character`},
		{"pyproject.toml", "[tool.poetry.dependencies]\nx = \"1\\u001b\"\n", `pyproject.toml: line 2: the version "==1\x1b" holds a control character`},
		{"poetry.lock", "[[package]]\nname = \"x\"\nversion = \"1\\u001b\"\n", `poetry.lock: line 3: the version "1\x1b" holds a control character`},
		{"Gemfile.lock", "GEM\n  specs:\n    x (1\x1b)\n", `Gemfile.lock: line 3: the version "1\x1b" holds a control character`},
	} {
		got := refusal(t, map[string]string{c.file: c.body}, of("x", c.file))
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("%s %q:\ngot  %s\nwant %s", c.file, c.body, got, c.want)
		}
	}
}

// The files hold at most max_bytes in all, the node limit holds for each,
// and a go.mod's lines and tokens are held to it. Planted break: Extract
// reading each file to max_bytes alone.
func TestDependencyBounds(t *testing.T) {
	repo := map[string]string{"a/requirements.txt": "x==1\n", "b/requirements.txt": "x==2\n"}
	if got := values(t, repo, Spec{Name: "x", Files: []string{"*/requirements.txt"}, MaxBytes: 10}); !strings.Contains(got, "2@b/requirements.txt") {
		t.Errorf("at the limit: %s", got)
	}
	got := refusal(t, repo, Spec{Name: "x", Files: []string{"*/requirements.txt"}, MaxBytes: 9})
	if !strings.Contains(got, "the files the extractor reads hold at most max_bytes, 9, in all") {
		t.Errorf("over the limit: %s", got)
	}
	got = refusal(t, map[string]string{"package.json": "[[[[[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]]]]]]"}, Spec{Name: "x", Files: []string{"package.json"}, MaxBytes: 64})
	if !strings.HasPrefix(got, "package.json: the file can hold up to") {
		t.Errorf("node limit: %s", got)
	}
	got = refusal(t, map[string]string{"go.mod": "module m\ngo 1.22\n"}, Spec{Name: "x", Files: []string{"go.mod"}, MaxBytes: 7*64 - 1})
	if !strings.HasPrefix(got, "go.mod: the file can hold up to 7 lines and tokens") {
		t.Errorf("go.mod limit: %s", got)
	}
}

// Place keeps what decides where the extractor reads and sorts the files;
// Resolve writes max_bytes out.
func TestDependencyPlaceAndResolve(t *testing.T) {
	p := Place(Spec{Name: "x", Files: []string{"b/go.mod", "./a/go.mod"}, Only: Range, MaxBytes: 9})
	if strings.Join(p.Files, " ") != "a/go.mod b/go.mod" || p.MaxBytes != 0 || p.Name != "x" || p.Only != Range {
		t.Errorf("Place: %+v", p)
	}
	s := Spec{Name: "x", Files: []string{"go.mod"}}
	r := Resolve(s)
	r.Files[0] = "y"
	if r.MaxBytes != core.DefaultMaxBytes || s.Files[0] != "go.mod" {
		t.Errorf("Resolve: %+v, spec %+v", r, s)
	}
}
