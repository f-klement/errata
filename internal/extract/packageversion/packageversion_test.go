package packageversion

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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

// one is a spec that names the one file.
func one(name string) Spec { return Spec{Files: []string{name}} }

// Each manifest, read alone through files:, gives the version its tool
// reads, at its line.
func TestPackageVersionReadsEachFile(t *testing.T) {
	for _, c := range []struct {
		name, file, body, want string
	}{
		{"package.json", "package.json", "{\n  \"name\": \"web\",\n  \"version\": \"1.4.2\"\n}\n", "1.4.2@package.json:3"},
		{"package.json without a version", "package.json", `{"name": "app", "private": true}`, ""},
		{"package.json's nested version is not the package's", "web/package.json", `{"engines": {"version": "9"}, "version": "2.0.0-rc.1"}`, "2.0.0-rc.1@web/package.json:1"},
		{"PEP 621", "pyproject.toml", "[project]\nname = \"lib\"\nversion = \"0.3.1\"\n", "0.3.1@pyproject.toml:3"},
		{"PEP 621 beside a stale Poetry version", "pyproject.toml", "[project]\nversion = \"0.3.1\"\n[tool.poetry]\nversion = \"0.2.0\"\n", "0.3.1@pyproject.toml:2"},
		{"Poetry before PEP 621", "pyproject.toml", "[tool.poetry]\nname = \"lib\"\nversion = \"1.0.0\"\n", "1.0.0@pyproject.toml:3"},
		{"a dynamic version", "pyproject.toml", "[project]\nname = \"lib\"\ndynamic = [\n  \"readme\",\n  \"version\",\n]\n[tool.setuptools.dynamic]\nversion = {attr = \"lib.__version__\"}\n",
			`dynamic = ["version"]!@pyproject.toml:5`},
		{"a dynamic version poetry-core fills", "pyproject.toml", "[build-system]\nbuild-backend = \"poetry.core.masonry.api\"\n[project]\ndynamic = [\"version\"]\n[tool.poetry]\nversion = \"2.1.0\"\n",
			"2.1.0@pyproject.toml:6"},
		{"a dynamic version under another backend", "pyproject.toml", "[build-system]\nbuild-backend = \"hatchling.build\"\n[project]\ndynamic = [\"version\"]\n[tool.poetry]\nversion = \"2.1.0\"\n",
			`dynamic = ["version"]!@pyproject.toml:4`},
		{"a dynamic list without the version", "pyproject.toml", "[project]\nversion = \"1\"\ndynamic = [\"readme\"]\n", "1@pyproject.toml:2"},
		{"a pyproject.toml without a version", "pyproject.toml", "[tool.ruff]\nline-length = 100\n", ""},
		{"Cargo", "Cargo.toml", "[package]\nname = \"cli\"\nversion = \"0.9.0\"\nrust-version = \"1.75\"\n", "0.9.0@Cargo.toml:3"},
		{"Cargo inherits from a workspace elsewhere", "crates/a/Cargo.toml", "[package]\nname = \"a\"\nversion.workspace = true\n", "version.workspace = true!@crates/a/Cargo.toml:3"},
		{"Cargo inherits in an inline table", "Cargo.toml", "[package]\nname = \"a\"\nversion = { workspace = true }\n", "version.workspace = true!@Cargo.toml:3"},
		{"Cargo inherits from its own workspace", "Cargo.toml", "[workspace.package]\nversion = \"3.0.0\"\n\n[package]\nname = \"root\"\nversion.workspace = true\n", "3.0.0@Cargo.toml:2"},
		{"a virtual manifest", "Cargo.toml", "[workspace]\nmembers = [\"a\"]\n[workspace.package]\nversion = \"3.0.0\"\n", "3.0.0@Cargo.toml:4"},
		{"a package and its workspace, each a claim", "Cargo.toml", "[package]\nversion = \"1.0.0\"\n[workspace.package]\nversion = \"1.0.1\"\n", "1.0.0@Cargo.toml:2 1.0.1@Cargo.toml:4"},
		{"the workspace written first", "Cargo.toml", "[workspace.package]\nversion = \"1.0.1\"\n[package]\nversion = \"1.0.0\"\n", "1.0.1@Cargo.toml:2 1.0.0@Cargo.toml:4"},
		{"Cargo without a version", "Cargo.toml", "[package]\nname = \"cli\"\n", ""},
		{"poetry-dynamic-versioning's placeholder under poetry-core", "pyproject.toml",
			"[build-system]\nbuild-backend = \"poetry.core.masonry.api\"\n[project]\ndynamic = [\"version\"]\n[tool.poetry]\nversion = \"0.0.0\"\n[tool.poetry-dynamic-versioning]\nenable = true\n",
			"0.0.0!@pyproject.toml:6"},
		{"poetry-dynamic-versioning's placeholder before PEP 621", "pyproject.toml",
			"[tool.poetry]\nversion = \"0.0.0\"\n[tool.poetry-dynamic-versioning]\nenable = true\n", "0.0.0!@pyproject.toml:2"},
		{"poetry-dynamic-versioning turned off", "pyproject.toml",
			"[tool.poetry]\nversion = \"1.0.0\"\n[tool.poetry-dynamic-versioning]\nenable = false\n", "1.0.0@pyproject.toml:2"},
		{"package.json after a byte order mark", "package.json", "\xef\xbb\xbf{\n\"version\": \"1.0.0\"}", "1.0.0@package.json:2"},
		{"Cargo.toml after a byte order mark", "Cargo.toml", "\xef\xbb\xbf[package]\nversion = \"1.0.0\"\n", "1.0.0@Cargo.toml:2"},
		{"VERSION", "VERSION", "1.2.3\n", "1.2.3@VERSION:1"},
		{"VERSION after comments, a BOM and CRLF", "VERSION", "\xef\xbb\xbf# the release\r\n\r\n  v1.2.3 # next\r\n2.0.0\r\n", "v1.2.3@VERSION:3"},
		{"VERSION without a line end", "lib/VERSION", "\t0.1.0", "0.1.0@lib/VERSION:1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := values(t, map[string]string{c.file: c.body}, one(c.file)); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// Without files:, the extractor reads the manifests at the root, in the
// table's order, and passes over one that is not there; a manifest below the
// root is another package's. The planted break: Build without its default
// names, which read no file and found nothing.
func TestPackageVersionReadsTheRoot(t *testing.T) {
	files := map[string]string{
		"VERSION":                "1.0.0\n",
		"Cargo.toml":             "[package]\nversion = \"1.0.0\"\n",
		"package.json":           `{"version": "1.0.0"}`,
		"web/package.json":       `{"version": "9.9.9"}`,
		"crates/x/Cargo.toml":    "[package]\nversion = \"9.9.9\"\n",
		"node_modules/a/VERSION": "9.9.9\n",
	}
	if got, want := values(t, files, Spec{}), "1.0.0@package.json:1 1.0.0@Cargo.toml:2 1.0.0@VERSION:1"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := values(t, map[string]string{"README.md": "1.0.0\n"}, Spec{}); got != "" {
		t.Errorf("a root with no manifest: got %q", got)
	}
}

// Without files:, a symlink at a manifest's name that dangles inside the
// repository is passed over, as a file that is not there; one that leads
// out is refused.
func TestPackageVersionSymlinksAtTheRoot(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"package.json": `{"version": "1.0.0"}`})
	extracttest.Symlink(t, "missing", filepath.Join(root, "VERSION"))
	if got := marked(extracttest.MustRun(t, build, Spec{}, root)); got != "1.0.0@package.json:1" {
		t.Errorf("got %q", got)
	}
	outside := filepath.Join(t.TempDir(), "VERSION")
	if err := os.WriteFile(outside, []byte("1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root = extracttest.Repo(t, nil)
	extracttest.Symlink(t, outside, filepath.Join(root, "VERSION"))
	_, err := extracttest.Run(t, build, Spec{}, root)
	extracttest.WantError(t, err, "read VERSION: a symlink leads outside the repository")
}

// A file files: names must exist, unlike one the root may hold.
func TestPackageVersionNamedFileMustExist(t *testing.T) {
	got := refusal(t, map[string]string{"package.json": `{"version": "1"}`}, Spec{Files: []string{"package.json", "VERSION"}})
	if !strings.Contains(got, "VERSION") || !strings.Contains(got, "no such file") {
		t.Errorf("got %q, want the missing VERSION named", got)
	}
}

// A manifest its tool refuses makes the rule unknown, with the file and the
// line. Each case is a planted break of one refusal.
func TestPackageVersionRefuses(t *testing.T) {
	for _, c := range []struct{ name, file, body, want string }{
		{"a number version", "package.json", "{\n\"version\": 1.2\n}", "package.json: line 2: version is a number, not a string; the tool that reads the file refuses it"},
		{"a table version", "pyproject.toml", "[project.version]\na = 1\n", "pyproject.toml: line 1: project.version is a table, not a string"},
		{"an array Poetry version", "pyproject.toml", "[tool.poetry]\nversion = [\"1\"]\n", "pyproject.toml: line 2: tool.poetry.version is an array, not a string"},
		{"a boolean Cargo version", "Cargo.toml", "[package]\nversion = true\n", "Cargo.toml: line 2: package.version is a boolean, not a string"},
		{"a null version", "package.json", `{"version": null}`, "package.json: line 1: version is null, not a string"},
		{"a number workspace version", "Cargo.toml", "[workspace.package]\nversion = 3\n", "Cargo.toml: line 2: workspace.package.version is a number, not a string"},
		// A TOML date or time is no string (#168): tomllib reads a date, and
		// Cargo refuses one. Planted break: version without its
		// FormDateTime refusal, which read 1979-05-27 as the version.
		{"a TOML date as project.version", "pyproject.toml", "[project]\nversion = 1979-05-27\n", "pyproject.toml: line 2: project.version is a date or a time, not a string; the tool that reads the file refuses it"},
		{"a TOML time as Poetry's version", "pyproject.toml", "[tool.poetry]\nversion = 07:32:00\n", "pyproject.toml: line 2: tool.poetry.version is a date or a time, not a string"},
		{"a TOML date and time as package.version", "Cargo.toml", "[package]\nversion = 1979-05-27T07:32:00Z\n", "Cargo.toml: line 2: package.version is a date or a time, not a string"},
		{"a local date and time as the workspace version", "Cargo.toml", "[workspace.package]\nversion = 1979-05-27 07:32:00\n", "Cargo.toml: line 2: workspace.package.version is a date or a time, not a string"},
		{"both set and dynamic", "pyproject.toml", "[project]\nversion = \"1\"\ndynamic = [\"version\"]\n", "pyproject.toml: line 3: dynamic lists version and project.version sets it at line 2, which PEP 621 forbids"},
		{"workspace = false", "Cargo.toml", "[package]\nversion.workspace = false\n", "Cargo.toml: line 2: package.version is a table other than { workspace = true }, which Cargo refuses"},
		{"a table without workspace", "Cargo.toml", "[package]\nversion = { path = \"x\" }\n", "Cargo.toml: line 2: package.version is a table other than { workspace = true }"},
		{"workspace as a string", "Cargo.toml", "[package]\nversion = { workspace = \"true\" }\n", "Cargo.toml: line 2: package.version is a table other than { workspace = true }"},
		{"an empty version", "package.json", `{"version": ""}`, "package.json: line 1: the version is empty"},
		{"a control character", "package.json", `{"version": "1.0\u0007"}`, "package.json: line 1: the version \"1.0\\a\" holds a control character"},
		{"a direction control", "VERSION", "1.0\u202e\n", "VERSION: line 1: the version"},
		{"an empty VERSION", "VERSION", "# none yet\n\n", "VERSION: line 1: the file holds no version, only blank lines and comments"},
		{"JSON that does not parse", "package.json", `{"version": `, "package.json: the file ends before the JSON value is complete"},
		{"TOML that does not parse", "Cargo.toml", "[package\n", "Cargo.toml: toml: line 1: expected ']' to close table name"},
		{"pyproject.toml after a byte order mark, which tomllib refuses", "pyproject.toml", "\xef\xbb\xbf[project]\nversion = \"1\"\n",
			"pyproject.toml: toml: line 1: invalid character at start of key: U+00EF"},
		{"a workspace root that inherits a version it does not set", "Cargo.toml", "[workspace]\nmembers = [\"a\"]\n[package]\nversion.workspace = true\n",
			"Cargo.toml: line 4: package.version inherits the workspace's version, and this file is the workspace's root but sets no workspace.package.version, which Cargo refuses"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := refusal(t, map[string]string{c.file: c.body}, one(c.file)); !strings.HasPrefix(got, c.want) {
				t.Errorf("got %q, want it to start %q", got, c.want)
			}
		})
	}
}

// The files hold at most max_bytes in all, as one file does: two files each
// under it but over it together are refused at the second.
func TestPackageVersionBytesInAll(t *testing.T) {
	files := map[string]string{"VERSION": "1.0.0\n", "lib/VERSION": "1.0.0\n"}
	s := Spec{Files: []string{"VERSION", "lib/VERSION"}, MaxBytes: 11}
	extracttest.WantError(t, func() error { _, err := extracttest.Run(t, build, s, extracttest.Repo(t, files)); return err }(),
		"lib/VERSION: the files the extractor reads hold more than 11 bytes in all, the extractor's max_bytes")
	s.MaxBytes = 12
	if got := values(t, files, s); got != "1.0.0@VERSION:1 1.0.0@lib/VERSION:1" {
		t.Errorf("at 12 bytes: got %q", got)
	}
}

// A JSON or TOML manifest past its format's node limit is refused before it
// is parsed.
func TestPackageVersionNodeLimit(t *testing.T) {
	body := `{"version": "1", "a": [` + strings.Repeat("1,", 200) + "1]}"
	got := refusal(t, map[string]string{"package.json": body}, Spec{Files: []string{"package.json"}, MaxBytes: int64(len(body))})
	if !strings.Contains(got, "package.json") || !strings.Contains(got, "limit") {
		t.Errorf("got %q, want the node limit", got)
	}
}

// A spec that names a file errata cannot read, or names one twice, or a
// file outside the repository, or a max_bytes out of range, does not load.
func TestPackageVersionLoadErrors(t *testing.T) {
	if _, err := build(Spec{Files: manyFiles(64)}); err != nil {
		t.Errorf("64 files: %v", err)
	}
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{one("setup.py"), "package_version: errata does not know how \"setup.py\" names a package's version; it reads Cargo.toml, VERSION, package.json, pyproject.toml"},
		{one("version.txt"), "does not know how \"version.txt\""},
		{Spec{Files: []string{"VERSION", "./VERSION"}}, "package_version: files names \"./VERSION\" twice"},
		{one("../package.json"), "is outside the repository"},
		{one("/package.json"), "is outside the repository"},
		{Spec{Files: []string{"VERSION"}, MaxBytes: -1}, "max_bytes is -1"},
		{Spec{MaxBytes: core.MaxMaxBytes + 1}, "package_version: max_bytes is"},
		{Spec{Files: manyFiles(65)}, "package_version: files names 65 files, over the limit of 64; a rule over more packages names several extractors"},
	} {
		_, err := build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
}

// manyFiles names n VERSION files, each in a directory of its own.
func manyFiles(n int) []string {
	var files []string
	for i := range n {
		files = append(files, "d"+strconv.Itoa(i)+"/VERSION")
	}
	return files
}

// Without files:, a directory at a manifest's name, or a symlink there to a
// directory, is passed over as a file that is not there; files: refuses it.
func TestPackageVersionPassesOverADirectory(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"package.json": `{"version": "1.0.0"}`, "VERSION/main.go": "package version\n", "src/x": ""})
	extracttest.Symlink(t, "src", filepath.Join(root, "Cargo.toml"))
	if got := marked(extracttest.MustRun(t, build, Spec{}, root)); got != "1.0.0@package.json:1" {
		t.Errorf("got %q", got)
	}
	_, err := extracttest.Run(t, build, one("VERSION"), root)
	extracttest.WantError(t, err, "read VERSION: is a directory")
}

// Without files:, only the table's own spelling is read: a file system that
// folds case, as macOS and Windows ship theirs, would open a version file or
// a Package.json under the table's name, and the root's listing does not.
func TestPackageVersionReadsExactNames(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"version": "9.9.9\n", "Package.json": `{"version": "9.9.9"}`, "Cargo.toml": "[package]\nversion = \"1.0.0\"\n"})
	got, err := rootNames(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got["Cargo.toml"] {
		t.Errorf("rootNames gives %v, want Cargo.toml alone", got)
	}
	if got := marked(extracttest.MustRun(t, build, Spec{}, root)); got != "1.0.0@Cargo.toml:2" {
		t.Errorf("got %q", got)
	}
	_, err = rootNames(t.Context(), filepath.Join(root, "missing"))
	extracttest.WantError(t, err, "open the repository root: no such file or directory")
}

// Place cleans and sorts the files and leaves max_bytes out, so the same
// files in another order or spelling are one place.
func TestPackageVersionPlace(t *testing.T) {
	a := Place(Spec{Files: []string{"./VERSION", "web/../package.json"}, MaxBytes: 5})
	b := Place(Spec{Files: []string{"package.json", "VERSION"}})
	if strings.Join(a.Files, ",") != "VERSION,package.json" || a.MaxBytes != 0 || strings.Join(b.Files, ",") != strings.Join(a.Files, ",") {
		t.Errorf("places %+v and %+v", a, b)
	}
	if p := Place(Spec{MaxBytes: 5}); p.Files != nil || p.MaxBytes != 0 || p.Tag != nil {
		t.Errorf("the root's place is %+v", p)
	}
	// The tag's prefix decides what is read, so two prefixes are two
	// places; the place holds a copy. Planted break: Place dropping Tag.
	tag := "v"
	p := Place(Spec{Tag: &tag})
	if p.Tag == nil || *p.Tag != "v" || p.Tag == &tag {
		t.Errorf("the tag's place is %+v", p)
	}
}

// Resolve writes max_bytes out and copies the files and the tag, so errata
// list's spec shares nothing with the config's. Planted break: Resolve
// keeping the config's tag pointer.
func TestPackageVersionResolve(t *testing.T) {
	tag := "v"
	s := Spec{Files: []string{"VERSION"}, Tag: &tag}
	r := Resolve(s)
	if r.MaxBytes != core.MaxBytes(0) || r.Tag == &tag || *r.Tag != "v" || &r.Files[0] == &s.Files[0] {
		t.Errorf("resolved %+v", r)
	}
	if Resolve(Spec{}).Tag != nil {
		t.Error("Resolve gave a tag to a spec of none")
	}
}

// gitRun runs git in dir with a fixed identity and no user or system
// config, and fails the test on an error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// tagged is a git repository of files, committed, with tags on HEAD and
// before ones on the commit before it.
func tagged(t *testing.T, files map[string]string, tags, before []string) string {
	t.Helper()
	root := extracttest.Repo(t, files)
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "commit", "-q", "--allow-empty", "-m", "old")
	for _, tag := range before {
		gitRun(t, root, "tag", tag)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "--allow-empty", "-m", "c")
	for _, tag := range tags {
		gitRun(t, root, "tag", "-a", "-m", "release", tag)
	}
	return root
}

// tag is a pointer to the prefix p.
func tag(p string) *string { return &p }

// With tag:, each tag on HEAD that starts with the prefix gives the rest
// of its name, at its ref, after the files' versions; a tag on another
// commit gives nothing, and so does a tag that is the prefix alone.
// Without tag:, no tag is read. Planted breaks: the prefix kept in the
// version; the prefix-alone tag given as an empty version, which the
// version check refused.
func TestPackageVersionReadsTheTagsOnHEAD(t *testing.T) {
	files := map[string]string{"package.json": "{\n\"version\": \"1.2.0\"\n}\n"}
	root := tagged(t, files, []string{"v1.2.0", "latest", "v", "api/v3"}, []string{"v1.1.0"})
	for _, c := range []struct {
		name string
		spec Spec
		want string
	}{
		{"prefix v", Spec{Tag: tag("v")}, "1.2.0@package.json:2 1.2.0@refs/tags/v1.2.0:0"},
		{"every tag", Spec{Tag: tag("")}, "1.2.0@package.json:2 api/v3@refs/tags/api/v3:0 latest@refs/tags/latest:0 v@refs/tags/v:0 v1.2.0@refs/tags/v1.2.0:0"},
		{"a prefix with a slash", Spec{Files: []string{"package.json"}, Tag: tag("api/v")}, "1.2.0@package.json:2 3@refs/tags/api/v3:0"},
		{"no tag matches", Spec{Tag: tag("release-")}, "1.2.0@package.json:2"},
		{"no tag:", Spec{}, "1.2.0@package.json:2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := marked(extracttest.MustRun(t, build, c.spec, root)); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

// A tag on HEAD whose rest is no version, and a repository errata cannot
// read the tags of, make the rule unknown, each with its reason. Planted
// breaks: the version check dropped from tags; the error of HeadTags
// dropped, which gave the files' versions alone.
func TestPackageVersionTagRefusals(t *testing.T) {
	root := tagged(t, nil, []string{"v1\u202e2"}, nil)
	_, err := extracttest.Run(t, build, Spec{Tag: tag("v")}, root)
	extracttest.WantError(t, err, `"refs/tags/v1\u202e2": the version "1\u202e2" holds a control character, a direction control`)

	plain := extracttest.Repo(t, map[string]string{"VERSION": "1\n"})
	_, err = extracttest.Run(t, build, Spec{Tag: tag("v")}, plain)
	extracttest.WantError(t, err, "read the tags on HEAD: no git repository holds the root")
}

// A build without a parser names the reader it lacks rather than crashing.
func TestPackageVersionWithoutParsers(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"Cargo.toml": "", "package.json": "{}"})
	for _, name := range []string{"Cargo.toml", "package.json"} {
		e, err := Build(one(name), Parsers{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.Extract(t.Context(), root)
		extracttest.WantError(t, err, name+": errata was built without the reader for "+name)
	}
}

// The kind describes its values as possibly not literal, so a rule may
// list it in skip_not_literal.
func TestPackageVersionDescribedNotLiteral(t *testing.T) {
	if _, err := build(Spec{}); err != nil {
		t.Fatal(err)
	}
	if !Describe(Spec{}).NotLiteral {
		t.Error("described as literal only")
	}
}

// onWitness is the context of a run on the witness w of a repository
// whose index lists files, as the engine holds the fixture, and the
// fixture's path, the root such a run is given.
func onWitness(t *testing.T, files map[string]string) (context.Context, string) {
	t.Helper()
	committed := map[string]string{}
	for f, body := range files {
		committed[".errata/witness/w/"+f] = body
	}
	root := extracttest.Tracked(t, committed)
	fixture := filepath.Join(root, ".errata", "witness", "w")
	held, err := os.OpenRoot(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	ctx, err := core.OnFixture(check.OnWitness(t.Context(), root), fixture, held)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, fixture
}

// On a witness the manifests at the root are those git's index lists at
// the top of the fixture, so an untracked VERSION is never listed, and not
// passed over as a file a read finds is not there. Failed on 6f2f58a,
// whose listing named VERSION. Planted break: core's listing of a
// witness's top read from the disk.
func TestPackageVersionListsOnlyWhatAWitnessCommits(t *testing.T) {
	ctx, fixture := onWitness(t, map[string]string{"package.json": `{"version": "1.0.0"}`})
	if err := os.WriteFile(filepath.Join(fixture, "VERSION"), []byte("9.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	present, err := rootNames(ctx, fixture)
	if err != nil || len(present) != 1 || !present["package.json"] {
		t.Errorf("the names on the witness: %v, %v; want package.json alone", present, err)
	}
	e, err := build(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.Extract(ctx, fixture); err != nil || marked(got) != "1.0.0@package.json:1" {
		t.Errorf("the versions on the witness: %q, %v", marked(got), err)
	}
}

// On a witness a manifest the fixture commits and the disk does not hold
// makes the extractor unknown, not passed over: a clone holds it. Failed
// on dfc8360, which passed over VERSION. Planted break: core's refusal of
// a committed file missing from the disk read as not there.
func TestPackageVersionRefusesAWitnessFileMissingFromTheDisk(t *testing.T) {
	ctx, fixture := onWitness(t, map[string]string{"package.json": `{"version": "1.0.0"}`, "VERSION": "1.0.0\n"})
	if err := os.Remove(filepath.Join(fixture, "VERSION")); err != nil {
		t.Fatal(err)
	}
	e, err := build(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Extract(ctx, fixture)
	if err == nil {
		t.Fatalf("no error, values %s", marked(got))
	}
	extracttest.WantError(t, err, "read VERSION: VERSION is a file in git's index below the fixture, and the disk does not hold it as one")
}
