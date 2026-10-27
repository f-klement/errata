package toolversion

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/mod/modfile"

	"github.com/f-klement/errata/internal/extract/core"
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

// kinds is the table of every file the extractor reads, by name. The
// README's tool_version section is the same table, in prose.
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
// .tool-versions file. Each word is one the README lists, with the tool that
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
// one, the line's other fields, and its number; a # and what follows it are
// left out. Fields are split at white space, a \r too, so a file with CRLF
// line ends reads as one with LF. A line that holds only a comment, or
// nothing, is passed over.
func eachLine(data []byte, line func(first []byte, rest *fields, n int) error) error {
	// Kept, a byte order mark would hide a .tool-versions line's tool and
	// stick to a version as a character no one sees.
	data = core.DropBOM(data)
	for n := 1; len(data) > 0; n++ {
		text, after, _ := bytes.Cut(data, []byte("\n"))
		data = after
		text, _, _ = bytes.Cut(text, []byte("#"))
		rest := fields{text}
		if first, ok := rest.next(); ok {
			if err := line(first, &rest, n); err != nil {
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
type fields struct{ rest []byte }

// next is the next field, and false when the line has no more.
func (f *fields) next() ([]byte, bool) {
	start := bytes.IndexFunc(f.rest, func(c rune) bool { return !unicode.IsSpace(c) })
	if start < 0 {
		f.rest = nil
		return nil, false
	}
	f.rest = f.rest[start:]
	end := bytes.IndexFunc(f.rest, unicode.IsSpace)
	if end < 0 {
		end = len(f.rest)
	}
	field := f.rest[:end]
	f.rest = f.rest[end:]
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
	return eachLine(data, func(name []byte, versions *fields, n int) error {
		if string(name) != x.tool && aliases[string(name)] != x.tool {
			return nil
		}
		for v, ok := versions.next(); ok; v, ok = versions.next() {
			if err := add(string(v), n); err != nil {
				return err
			}
		}
		return nil
	})
}

// readSdkmanrc reads an .sdkmanrc file, as sdk env reads it: a candidate,
// =, and its version on each line. A line without = is refused, and so is a
// candidate the spec names with no version.
func readSdkmanrc(x *extractor, _ file, data []byte, add func(string, int) error) error {
	data = core.DropBOM(data)
	for n := 1; len(data) > 0; n++ {
		text, rest, _ := bytes.Cut(data, []byte("\n"))
		data = rest
		text, _, _ = bytes.Cut(text, []byte("#"))
		if len(bytes.TrimSpace(text)) == 0 {
			continue
		}
		key, value, ok := bytes.Cut(text, []byte("="))
		if !ok {
			return core.LineError(n, "%s is not candidate=version, the form sdk env reads", core.Quote(string(bytes.TrimSpace(text))))
		}
		if string(bytes.TrimSpace(key)) != x.tool {
			continue
		}
		value = bytes.TrimSpace(value)
		if len(value) == 0 {
			return core.LineError(n, "%s names no version", x.tool)
		}
		if err := add(string(value), n); err != nil {
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
			return add(string(text), 1+bytes.Count(data[:lead], []byte("\n")))
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
func readGoMod(_ *extractor, f file, data []byte, add func(string, int) error) (err error) {
	if n, limit := goModNodes(data), f.Limit/goModBytesPerNode; int64(n) > limit {
		return named{fmt.Errorf("%s: the file can hold up to %d lines and tokens, over the limit of %d, one for every %d bytes of max_bytes; set max_bytes: higher to read a file this dense",
			f.File, n, limit, goModBytesPerNode)}
	}
	// modfile recovers its parser's panics but not those of the step that
	// turns the syntax into directives; no input found makes one panic, and
	// a panic there would end the run instead of making one rule unknown.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("golang.org/x/mod/modfile stopped on the file: %v", r)
		}
	}()
	mod, err := parseGoMod(f.File, data, nil)
	if err != nil {
		// modfile's message starts with the file and the line.
		return named{core.ClipError(err)}
	}
	if mod.Toolchain != nil {
		name := mod.Toolchain.Name
		if name != "default" {
			name = strings.TrimPrefix(name, "go")
		}
		return add(name, mod.Toolchain.Syntax.Start.Line)
	}
	if mod.Go != nil {
		return add(mod.Go.Version, mod.Go.Syntax.Start.Line)
	}
	return nil
}

// parseGoMod is modfile.Parse; a test replaces it to witness the recover.
var parseGoMod = modfile.Parse

// goModBytesPerNode is how many bytes of max_bytes allow one line or token of
// a go.mod. modfile builds the syntax of the whole file before errata sees a
// directive, and a line or token costs 150 to 440 bytes there: 32 MiB of
// lines of one short token took 2.1 GB and 4.1 s, of empty comments 1.6 GB,
// and of commas on one line 1.9 GB. A go.mod of requirements holds a line or
// token for every 7 bytes, so at the default the limit reads one of about
// 3.5 MB, some 80000 requirements. The densest files measured at the limit,
// 32 MiB of unknown directives of 129 bytes or of lines ending in a comment,
// take about 0.5 to 0.75 s and up to about 320 MB, since modfile keeps an
// error for each line.
const goModBytesPerNode = 64

// goModPunct are the bytes modfile's lexer makes a token of whatever is
// around them: a line end, ( ) [ ] { } and the comma, and the quote and the
// backquote that start a string.
const goModPunct = "\n()[]{},\"`"

// goModNodes is an upper bound on the lines and tokens modfile builds from
// data: one for each byte of goModPunct, and one for each other byte that is
// not blank and follows a blank, a byte of goModPunct or the start of the
// file, where an identifier starts. A string counts at least once, by its
// quote, and an identifier right after a ) or a closing quote counts too.
func goModNodes(data []byte) int {
	n, start := 1, true
	for _, b := range data {
		switch {
		case strings.IndexByte(goModPunct, b) >= 0:
			n, start = n+1, true
		case b == ' ' || b == '\t' || b == '\r':
			start = true
		default:
			if start {
				n++
			}
			start = false
		}
	}
	return n
}
