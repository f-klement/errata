package dependency

import (
	"bytes"
	"errors"
	"path"
	"regexp"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// reader is one row of the table: the file names it reads, how it compares
// a package's name, and how it reads a file.
type reader struct {
	// name is the reader's name, which each value's origin starts with.
	name string
	// pattern is the names it reads, as path.Match reads a pattern: a base
	// name, or, with a /, the last directory and the base name.
	pattern string
	// fold is a name as the ecosystem compares names: two names that fold
	// to one text name one package.
	fold func(string) string
	read func(rd *reading, data []byte) error
}

// readers is the table of every file the extractor reads, by its base
// name. docs/reference.md#dependency is the same table, in prose.
var readers = []*reader{
	{name: "go.mod", pattern: "go.mod", fold: exact, read: readGoMod},
	{name: "go.work", pattern: "go.work", fold: exact, read: readGoWork},
	{name: "package.json", pattern: "package.json", fold: exact, read: readPackageJSON},
	{name: "package-lock.json", pattern: "package-lock.json", fold: exact, read: readPackageLock},
	{name: "npm-shrinkwrap.json", pattern: "npm-shrinkwrap.json", fold: exact, read: readPackageLock},
	{name: "pnpm-lock.yaml", pattern: "pnpm-lock.yaml", fold: exact, read: readPnpmLock},
	{name: "yarn.lock", pattern: "yarn.lock", fold: exact, read: readYarnLock},
	{name: "pyproject.toml", pattern: "pyproject.toml", fold: normalize, read: readPyproject},
	{name: "requirements", pattern: "requirements*.txt", fold: normalize, read: readRequirements},
	{name: "requirements", pattern: "requirements*.in", fold: normalize, read: readRequirements},
	{name: "requirements", pattern: "requirements/*.txt", fold: normalize, read: readRequirements},
	{name: "requirements", pattern: "requirements/*.in", fold: normalize, read: readRequirements},
	{name: "requirements", pattern: "constraints.txt", fold: normalize, read: readRequirements},
	{name: "poetry.lock", pattern: "poetry.lock", fold: normalize, read: readPythonLock},
	{name: "uv.lock", pattern: "uv.lock", fold: normalize, read: readPythonLock},
	{name: "Cargo.toml", pattern: "Cargo.toml", fold: exact, read: readCargo},
	{name: "Cargo.lock", pattern: "Cargo.lock", fold: exact, read: readCargoLock},
	{name: "Gemfile", pattern: "Gemfile", fold: exact, read: readGemfile},
	{name: "Gemfile.lock", pattern: "Gemfile.lock", fold: exact, read: readGemfileLock},
	{name: "composer.json", pattern: "composer.json", fold: strings.ToLower, read: readComposer},
	{name: "composer.lock", pattern: "composer.lock", fold: strings.ToLower, read: readComposerLock},
}

// readerFor is the row that reads a file in the directories dirs with the
// base name base, nil when none does.
func readerFor(dirs []string, base string) *reader {
	// FileParts gives at least one directory, . for a file at the root.
	name := dirs[len(dirs)-1] + "/" + base
	for _, r := range readers {
		subject := base
		if strings.Contains(r.pattern, "/") {
			subject = name
		}
		if ok, _ := path.Match(r.pattern, subject); ok {
			return r
		}
	}
	return nil
}

// exact compares names byte for byte, as Go, npm, Cargo and RubyGems do.
func exact(name string) string { return name }

// separators are the runs PEP 503 folds to one -.
var separators = regexp.MustCompile(`[-_.]+`)

// normalize is a Python name as PEP 503 normalizes it: in lower case, with
// each run of -, _ and . read as one -, so Foo_Bar and foo-bar are one
// package. Composer's names fold to lower case.
func normalize(name string) string {
	return separators.ReplaceAllString(strings.ToLower(name), "-")
}

// tree reads data with parse, after the node limit of format, and refuses
// a tree that is not a mapping at its top.
func tree(rd *reading, data []byte, format *core.Format, parse func([]byte) (*core.Node, error), skipBOM bool) (*core.Node, error) {
	t, err := rd.src.ParseManifest(data, rd.r.name, format, parse, skipBOM)
	if err != nil {
		if errors.Is(err, core.ErrTooDense) {
			return nil, named{err}
		}
		return nil, err
	}
	if t == nil || t.Shape != core.Mapping {
		return nil, core.LineError(1, "the file is no mapping of keys, as %s is", rd.r.name)
	}
	return t, nil
}

// text is n's text when n is a string, or the refusal of a node that is
// not, which the file's tool refuses too. A TOML date or time, such as
// version = 1979-05-27, is no string: Cargo refuses it, and tomllib reads
// it as a date, which no Python tool takes for a requirement or a version.
// what names n for the message.
func text(n *core.Node, what string) (string, error) {
	if n.Shape != core.Scalar || n.Type != core.TypeString || n.Form == core.FormDateTime {
		return "", core.LineError(n.Line, "%s is %s, not a string, which the tool that reads the file refuses", what, describe(n))
	}
	return n.Text, nil
}

// describe names what n is, for a message.
func describe(n *core.Node) string {
	switch {
	case n.Shape == core.Mapping:
		return "a mapping"
	case n.Shape == core.Sequence:
		return "a list"
	case n.Type == core.TypeNumber:
		return "a number"
	case n.Type == core.TypeBool:
		return "a boolean"
	case n.Form == core.FormDateTime:
		return "a date or a time"
	case n.Type == core.TypeString:
		return "a string"
	default:
		return "null"
	}
}

// eachLine calls line with each line of data, without its line end, and its
// number from 1, the lines core's line functions end. line stops the walk
// with an error.
func eachLine(data []byte, line func(text []byte, n int) error) error {
	for n, off := 1, 0; off < len(data); n++ {
		next := core.NextLine(data, off)
		text := bytes.TrimSuffix(bytes.TrimSuffix(data[off:next], []byte("\n")), []byte("\r"))
		if err := line(text, n); err != nil {
			return err
		}
		off = next
	}
	return nil
}
