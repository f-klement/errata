package toolversion

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/gomod"
)

// fileKind is one row of the table: a file name, the tools a file of that
// name pins, and how its versions are read.
type fileKind struct {
	name string
	// tools are the tools the file pins. nil means each line names its
	// tool, as in .tool-versions, and so does a field, as in package.json.
	tools []string
	// anyTool is set for VERSION, which names no tool: it pins the tool the
	// spec names, and only when files: names it.
	anyTool bool
	// searched files are the ones the walk looks for when files: is left
	// out. The
	// manifests are not: they state a range a tool must satisfy, not the
	// version a repository pins.
	searched bool
	// single files hold one version a line, at least one: an empty file,
	// or one of comments, is one its tool refuses.
	single bool
	// skipsBOM says whether readTree drops a byte order mark at the start
	// of the file, as the file's tool skips it: npm in package.json, Cargo
	// and rustup in their TOML files. Python's tomllib refuses one, so
	// pyproject.toml does not set it, and the parser refuses the file. The
	// files of lines drop the mark in their own readers.
	skipsBOM bool
	read     func(x *extractor, f file, data []byte, add func(text string, line int) error) error
}

// kinds is the table of every file the extractor reads, by name.
// docs/reference.md#tool_version is the same table, in prose.
var kinds = []*fileKind{
	{name: ".tool-versions", searched: true, read: readToolVersions},
	{name: ".sdkmanrc", searched: true, read: readSdkmanrc},
	{name: ".nvmrc", tools: []string{"node"}, searched: true, single: true, read: readFirst},
	{name: ".node-version", tools: []string{"node"}, searched: true, single: true, read: readFirst},
	{name: ".python-version", tools: []string{"python"}, searched: true, single: true, read: readEvery},
	{name: "runtime.txt", tools: []string{"python"}, searched: true, single: true, read: readRuntime},
	{name: ".ruby-version", tools: []string{"ruby"}, searched: true, single: true, read: readFirst},
	{name: ".go-version", tools: []string{"go"}, searched: true, single: true, read: readFirst},
	{name: "go.mod", tools: []string{"go"}, searched: true, read: readGoMod},
	{name: ".terraform-version", tools: []string{"terraform"}, searched: true, single: true, read: readFirst},
	{name: ".java-version", tools: []string{"java"}, searched: true, single: true, read: readFirst},
	{name: "rust-toolchain", tools: []string{"rust"}, searched: true, skipsBOM: true, read: readRustToolchain},
	{name: "rust-toolchain.toml", tools: []string{"rust"}, searched: true, skipsBOM: true, read: readTOML},
	{name: ".bazelversion", tools: []string{"bazel"}, searched: true, single: true, read: readFirst},
	{name: ".bun-version", tools: []string{"bun"}, searched: true, single: true, read: readFirst},
	{name: "VERSION", anyTool: true, single: true, read: readFirst},
	{name: "package.json", skipsBOM: true, read: readPackageJSON},
	{name: "pyproject.toml", tools: []string{"python"}, read: readTOML},
	{name: "Cargo.toml", tools: []string{"rust"}, skipsBOM: true, read: readTOML},
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

// pins reports whether a file of kind k can pin tool.
func (k *fileKind) pins(tool string) bool {
	return k.anyTool || k.tools == nil || slices.Contains(k.tools, tool)
}

// pinned names the tools k pins, for a message.
func (k *fileKind) pinned() string {
	return strings.Join(k.tools, " and ")
}

// notLiteral reports whether a version names no version until a tool
// resolves it: an alias that moves, such as latest, stable or lts/*, the
// system's own install, or another source, such as ref: or path: in a
// .tool-versions file. Each word is one docs/reference.md#tool_version lists, with the tool that
// reads it. A partial version such as 20 is literal: it is what the file
// wrote, and comparing it with 20.11.1 is a comparison's work.
func notLiteral(v string) bool {
	if movingWords[v] {
		return true
	}
	for _, p := range movingPrefixes {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	// A rust channel with a host, such as stable-x86_64-unknown-linux-gnu,
	// moves as the channel does; one with a date, such as
	// nightly-2024-01-01, does not.
	for _, channel := range []string{"stable-", "beta-", "nightly-"} {
		if rest, ok := strings.CutPrefix(v, channel); ok && !isDate(rest) {
			return true
		}
	}
	return false
}

// movingWords are the versions that are names, not versions.
var movingWords = map[string]bool{
	"system": true, "latest": true, "stable": true, "unstable": true, "beta": true,
	"nightly": true, "node": true, "iojs": true, "lts": true, "default": true,
	"rolling": true, "min-required": true, "last_green": true, "last_rc": true,
	"last_downstream_green": true,
}

// movingPrefixes start the versions that resolve to another: lts/iron and
// lts/*, latest:18 and latest-1, ref:v1.2, path:~/src/node, prefix:1.2 and
// sub-1:latest.
var movingPrefixes = []string{"lts/", "latest:", "latest-", "ref:", "path:", "prefix:", "sub-"}

// isDate reports whether s starts with a date, YYYY-MM-DD.
func isDate(s string) bool {
	if len(s) < 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range []byte(s[:10]) {
		if i != 4 && i != 7 && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// eachLine calls line with the first field of each line of data that holds
// one, the line's other fields, and the first field's line; a # and what
// follows it are left out. The tools end a line at \n alone, and split
// fields at white space, a \r too, so a file with CRLF line ends reads as
// one with LF; errata numbers a field's line at a lone \r as well, as it
// numbers every file's (rest.line). A line that holds only a comment, or
// nothing, is passed over.
func eachLine(data []byte, line func(first []byte, rest *fields, n int) error) error {
	// Kept, a byte order mark would hide a .tool-versions line's tool and
	// stick to a version as a character no one sees.
	data = core.DropBOM(data)
	lines := core.NewLineCursor(data)
	for left := data; len(left) != 0; { // != leaves no boundary that loops for ever
		text, after, _ := bytes.Cut(left, []byte("\n"))
		rest := fields{rest: text, off: len(data) - len(left), lines: lines}
		left = after
		text, _, _ = bytes.Cut(text, []byte("#"))
		rest.rest = text
		if first, ok := rest.next(); ok {
			if err := line(first, &rest, rest.line()); err != nil {
				return err
			}
		}
	}
	return nil
}

// fields are the fields of a line not read yet. next reads them one at a
// time, where bytes.Fields would make a slice of them all: a line of 16
// million versions took 480 MB for that slice before the value limit
// refused it.
type fields struct {
	rest  []byte
	off   int              // where rest starts in the file
	at    int              // where the field next gave last starts in the file
	lines *core.LineCursor // the line of an offset in the file
}

// line is the line in the file of the field next gave last.
func (f *fields) line() int { return f.lines.At(f.at) }

// next is the next field, and false when the line has no more.
func (f *fields) next() ([]byte, bool) {
	start := bytes.IndexFunc(f.rest, func(c rune) bool { return !unicode.IsSpace(c) })
	if start < 0 {
		f.rest = nil
		return nil, false
	}
	f.rest, f.at = f.rest[start:], f.off+start
	end := bytes.IndexFunc(f.rest, unicode.IsSpace)
	if end < 0 {
		end = len(f.rest)
	}
	field := f.rest[:end]
	f.rest, f.off = f.rest[end:], f.at+end
	return field, true
}

// errNoVersion refuses a file of one version that holds none: pyenv's
// version-file-read and rustup refuse an empty file, and nvm, rbenv and the
// others read no version from it.
var errNoVersion = core.LineError(1, "the file holds no version, only blank lines and comments")

// errDone ends a read once it has what it reads.
var errDone = errors.New("done")

// readToolVersions reads a .tool-versions file, as asdf and mise read it:
// on each line a tool and then its versions, the first one wanted and each
// after it a fallback. Every version of the tool is a value; a line under
// the tool's alias (nodejs, golang) is the tool's too.
func readToolVersions(x *extractor, _ file, data []byte, add func(string, int) error) error {
	return eachLine(data, func(name []byte, versions *fields, _ int) error {
		if string(name) != x.tool && aliases[string(name)] != x.tool {
			return nil
		}
		for v, ok := versions.next(); ok; v, ok = versions.next() {
			if err := add(string(v), versions.line()); err != nil {
				return err
			}
		}
		return nil
	})
}

// readSdkmanrc reads an .sdkmanrc file, as sdk env reads it: a candidate,
// =, and its version on each line. A line without = is refused, and so is a
// candidate the spec names with no version. sdk env ends a line at \n
// alone; errata numbers each line where its text starts, past white space
// and a lone \r, as it numbers every file's lines.
func readSdkmanrc(x *extractor, _ file, data []byte, add func(string, int) error) error {
	data = core.DropBOM(data)
	lines := core.NewLineCursor(data)
	for left := data; len(left) != 0; { // != leaves no boundary that loops for ever
		raw, after, _ := bytes.Cut(left, []byte("\n"))
		at := len(data) - len(left) // where raw starts in the file
		left = after
		raw, _, _ = bytes.Cut(raw, []byte("#"))
		text := bytes.TrimLeftFunc(raw, unicode.IsSpace)
		at += len(raw) - len(text)
		if text = bytes.TrimRightFunc(text, unicode.IsSpace); len(text) == 0 {
			continue
		}
		n := lines.At(at)
		key, value, ok := bytes.Cut(text, []byte("="))
		if !ok {
			return core.LineError(n, "%s is not candidate=version, the form sdk env reads", core.Quote(string(text)))
		}
		if string(bytes.TrimSpace(key)) != x.tool {
			continue
		}
		trimmed := bytes.TrimLeftFunc(value, unicode.IsSpace)
		at += len(key) + 1 + len(value) - len(trimmed) // where the version starts
		if value = bytes.TrimRightFunc(trimmed, unicode.IsSpace); len(value) == 0 {
			return core.LineError(n, "%s names no version", x.tool)
		}
		if err := add(string(value), lines.At(at)); err != nil {
			return err
		}
	}
	return nil
}

// readFirst reads a file that holds one version: the first field of its
// first line that holds one. nvm, rbenv, goenv, tfenv, jenv, bazelisk and bun
// read that and nothing after it, so a later line is no claim.
func readFirst(_ *extractor, _ file, data []byte, add func(string, int) error) error {
	err := eachLine(data, func(first []byte, _ *fields, n int) error {
		if err := add(string(first), n); err != nil {
			return err
		}
		return errDone
	})
	if errors.Is(err, errDone) {
		return nil
	}
	return err
}

// readEvery reads a .python-version file as pyenv's version-file-read does:
// the first word of every line, each a version pyenv activates. A second
// word on a line is no version.
func readEvery(_ *extractor, _ file, data []byte, add func(string, int) error) error {
	return eachLine(data, func(first []byte, _ *fields, n int) error {
		return add(string(first), n)
	})
}

// readRuntime reads a runtime.txt file, Heroku's: python-, then the version,
// on its first line. The value is the version, without python-, since the
// prefix names the tool, as the first field of a .tool-versions line does.
func readRuntime(_ *extractor, _ file, data []byte, add func(string, int) error) error {
	err := eachLine(data, func(first []byte, _ *fields, n int) error {
		v, ok := bytes.CutPrefix(first, []byte("python-"))
		if !ok || len(v) == 0 {
			return core.LineError(n, "%s is not python-<version>, the form Heroku reads", core.Quote(string(first)))
		}
		if err := add(string(v), n); err != nil {
			return err
		}
		return errDone
	})
	if errors.Is(err, errDone) {
		return nil
	}
	return err
}

// readRustToolchain reads a rust-toolchain file as rustup does: a file that
// parses as TOML is read as rust-toolchain.toml is, and a file of one line
// that does not is the legacy form, that line the channel.
func readRustToolchain(x *extractor, f file, data []byte, add func(string, int) error) error {
	raw := data // readTOML drops the mark itself
	data = core.DropBOM(data)
	text := bytes.TrimSpace(data)
	if len(text) == 0 {
		return errNoVersion
	}
	if !bytes.Contains(text, []byte("\n")) && x.parsers.TOML != nil {
		if err := f.CheckNodes(data, core.TOMLFormat); err != nil {
			return named{err}
		}
		if _, err := x.parsers.TOML(data); err != nil {
			lead := len(data) - len(bytes.TrimLeftFunc(data, unicode.IsSpace))
			return add(string(text), core.LineOf(data, lead))
		}
	}
	return readTOML(x, f, raw, add)
}

// readGoMod reads the Go a go.mod pins with golang.org/x/mod/modfile, which
// the go command parses it with, and by the go command's rule: the toolchain
// directive's version when the file has one, and the go directive's when it
// has none, since the go command then runs the go directive's version. With
// both, the go directive is the oldest Go the module builds with, another
// fact, which a regex extractor reads. The toolchain's name is go and the
// version, go1.22.3, and the value leaves out the go, as runtime.txt's
// python- is left out, so it agrees with .go-version; toolchain default
// stays default. A go.mod the go command refuses, such as one with an
// unknown directive, is refused here too.
func readGoMod(_ *extractor, f file, data []byte, add func(string, int) error) error {
	mod, err := gomod.Parse(f.Source, data)
	if err != nil {
		return named{err}
	}
	if mod.Toolchain != nil {
		name := mod.Toolchain.Name
		if name != "default" {
			name = strings.TrimPrefix(name, "go")
		}
		return add(name, gomod.Line(data, mod.Toolchain.Syntax.Start))
	}
	if mod.Go != nil {
		return add(mod.Go.Version, gomod.Line(data, mod.Go.Syntax.Start))
	}
	return nil
}
