package dependency

import (
	"bytes"
	"strings"
	"unicode"

	"github.com/f-klement/errata/internal/extract/core"
)

// readPyproject reads the ranges a pyproject.toml gives the package: the
// PEP 508 requirements of project.dependencies and
// project.optional-dependencies (PEP 621), of dependency-groups (PEP 735),
// of build-system.requires and of uv's and PDM's dev-dependencies, and Poetry's tables,
// tool.poetry.dependencies, tool.poetry.dev-dependencies and the
// dependencies of each tool.poetry.group. Names compare as PEP 503
// normalizes them. A byte order mark is refused, as Python's tomllib
// refuses it.
func readPyproject(rd *reading, data []byte) error {
	t, err := tree(rd, data, core.TOMLFormat, rd.x.parsers.TOML, false)
	if err != nil {
		return err
	}
	lists := []part{
		{"project.dependencies", t.Member("project", "dependencies")},
		{"build-system.requires", t.Member("build-system", "requires")},
	}
	for _, group := range groups(t.Member("project", "optional-dependencies")) {
		lists = append(lists, part{"project.optional-dependencies", group})
	}
	lists = append(lists, part{"tool.uv.dev-dependencies", t.Member("tool", "uv", "dev-dependencies")})
	for _, group := range groups(t.Member("tool", "pdm", "dev-dependencies")) {
		lists = append(lists, part{"tool.pdm.dev-dependencies", group})
	}
	for _, group := range groups(t.Member("dependency-groups")) {
		lists = append(lists, part{"dependency-groups", group})
	}
	for _, l := range lists {
		if err := requirements(rd, l.n, l.section); err != nil {
			return err
		}
	}
	poetry := []part{
		{"tool.poetry.dependencies", t.Member("tool", "poetry", "dependencies")},
		{"tool.poetry.dev-dependencies", t.Member("tool", "poetry", "dev-dependencies")},
	}
	for _, group := range groups(t.Member("tool", "poetry", "group")) {
		poetry = append(poetry, part{"tool.poetry.group", group.Member("dependencies")})
	}
	for _, p := range poetry {
		if err := poetryTable(rd, p.n, p.section); err != nil {
			return err
		}
	}
	return nil
}

// part is a part of a file, by the name its values' origin gives it.
type part struct {
	section string
	n       *core.Node
}

// groups are the values of a mapping of groups, none when n is no
// mapping.
func groups(n *core.Node) []*core.Node {
	if n == nil || n.Shape != core.Mapping {
		return nil
	}
	return n.Vals
}

// requirements adds the range of each PEP 508 requirement of the list n
// that names the package. An item that is a table, such as PEP 735's
// {include-group = "test"}, names no package.
func requirements(rd *reading, n *core.Node, section string) error {
	if n == nil {
		return nil
	}
	if n.Shape != core.Sequence {
		return core.LineError(n.Line, "%s is %s, not a list of requirements, which the build tools refuse", section, describe(n))
	}
	for _, item := range n.Items {
		if item.Shape == core.Mapping {
			continue
		}
		s, err := text(item, "a requirement of "+section)
		if err != nil {
			return err
		}
		if err := requirement(rd, s, func(int) int { return item.Line }, section, false, false); err != nil {
			return err
		}
	}
	return nil
}

// requirement adds the range of the PEP 508 requirement s when it names
// the package: the version specifiers after the name and its extras, up to
// the marker after a ;, such as >=2.31,<3. The value is at the line at
// gives for the offset in s its text starts at. A requirement of
// the package from a URL, name @ https://..., names no version in the
// file, and the URL is not literal. One without a specifier gives
// nothing. In a requirements file, pip, a ${VAR} is one pip takes from the
// environment, so it is not literal; and with pins, in a .txt file, a line
// that pins one version, name==2.31.0, installs exactly that version, as
// pip-compile writes the whole file, so it is that version, resolved.
func requirement(rd *reading, s string, at func(off int) int, section string, pip, pins bool) error {
	name, rest := pep508Name(s)
	if name == "" || !rd.is(name) {
		return nil
	}
	// rest stays a suffix of s, so len(s)-len(rest) is where it starts in
	// s, which at turns into the line that offset is on.
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	if r, ok := strings.CutPrefix(rest, "["); ok {
		_, rest, _ = strings.Cut(r, "]")
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	}
	if url, ok := strings.CutPrefix(rest, "@"); ok {
		url = strings.TrimLeftFunc(url, unicode.IsSpace)
		text, _, _ := strings.Cut(url, ";")
		return rd.add(found{text: strings.TrimSpace(text), section: section, role: Range, line: at(len(s) - len(url)), notLiteral: true})
	}
	p, paren := strings.CutPrefix(rest, "(")
	if paren {
		rest = strings.TrimLeftFunc(p, unicode.IsSpace)
	}
	// The specifier starts where rest does, the last time rest is a
	// suffix of s: what follows cuts its end.
	start := len(s) - len(rest)
	rest, _, _ = strings.Cut(rest, ";")
	spec := strings.TrimSpace(rest)
	if paren {
		spec = strings.TrimSpace(strings.TrimSuffix(spec, ")"))
	}
	if spec == "" {
		return nil
	}
	f := found{text: spec, section: section, role: Range, line: at(start), notLiteral: pip && strings.Contains(spec, "${")}
	if v, ok := strings.CutPrefix(spec, "=="); pins && ok && !f.notLiteral {
		if t := strings.TrimSpace(v); t != "" && !strings.ContainsAny(t, "=*, \t") {
			f.text, f.role, f.line = t, Resolved, at(start+2+len(v)-len(strings.TrimLeftFunc(v, unicode.IsSpace)))
		}
	}
	return rd.add(f)
}

// pep508Name cuts the name a PEP 508 requirement starts with, after any
// blanks: a letter or digit, then letters, digits, -, _ and ., ending in a
// letter or digit. It is "" for a line that starts with no name.
func pep508Name(s string) (name, rest string) {
	s = strings.TrimLeft(s, " \t")
	end := 0
	for end < len(s) && (isAlnum(s[end]) || s[end] == '-' || s[end] == '_' || s[end] == '.') {
		end++
	}
	for end > 0 && !isAlnum(s[end-1]) {
		end--
	}
	return s[:end], s[end:]
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// poetryTable adds the range a Poetry dependency table gives the package:
// a string, such as "^2.31", the version of a table, such as
// { version = "^2.31", extras = ["socks"] }, or each version of a list of
// such tables, one for each marker, read as Poetry reads it: a bare version,
// "1.2", is that version alone (exactRange). A table without a version,
// such as one from git or a path, gives nothing.
func poetryTable(rd *reading, n *core.Node, section string) error {
	if n == nil || n.Shape != core.Mapping {
		return nil
	}
	for i, key := range n.Keys {
		if !rd.is(key) {
			continue
		}
		v := n.Vals[i]
		items := []*core.Node{v}
		if v.Shape == core.Sequence {
			items = v.Items
		}
		for _, item := range items {
			if item.Shape == core.Mapping {
				if item = item.Member("version"); item == nil {
					continue
				}
			}
			s, err := text(item, section+" of the package")
			if err != nil {
				return err
			}
			if err := rd.add(found{text: exactRange(s), section: section, role: Range, line: item.Line}); err != nil {
				return err
			}
		}
	}
	return nil
}

// readRequirements reads a pip requirements or constraints file as pip's
// req_file reads it: each line a PEP 508 requirement, a \ at the end of a
// line that is not a comment joining the next line to it with nothing
// between, a # after a blank or at the start of the joined text starting a
// comment, and a line that starts with - an option, such as -r or -e,
// which names no version. The options after a requirement, such as
// --hash=, are not part of its range. A value is at the line its text
// starts on. A line that pins one version is resolved (requirement).
func readRequirements(rd *reading, data []byte) error {
	data = core.DropBOM(data)
	// A pin in pip-tools' input, a .in file, is what a person asks for,
	// which pip-compile resolves into the .txt; only a .txt file's pin is
	// resolved.
	pins := !strings.HasSuffix(rd.src.File, ".in")
	// joined is the requirement so far, the lines a backslash joins, and
	// starts holds where in it each of those lines starts, the first at 0,
	// so a value is at the line its text starts on. pending is whether a
	// backslash left joined open; first is its first line.
	var joined []byte
	var starts []int
	first, pending := 0, false
	end := func() error {
		whole := string(joined)
		joined, pending = joined[:0], false
		if i := comment([]byte(whole)); i >= 0 {
			whole = whole[:i]
		}
		lead := len(whole) - len(strings.TrimLeftFunc(whole, unicode.IsSpace))
		whole = strings.TrimSpace(whole)
		if whole == "" || whole[0] == '-' {
			return nil
		}
		if i := option(whole); i >= 0 {
			whole = whole[:i]
		}
		at := func(off int) int {
			i := 0
			for i+1 < len(starts) && starts[i+1] <= lead+off {
				i++
			}
			return first + i
		}
		return requirement(rd, whole, at, "", true, pins)
	}
	err := eachLine(data, func(line []byte, n int) error {
		if !pending {
			first, starts = n, starts[:0]
		}
		starts = append(starts, len(joined))
		isComment := bytes.HasPrefix(bytes.TrimLeftFunc(line, unicode.IsSpace), []byte("#"))
		// pip strips every backslash at both ends of a line it continues.
		if bytes.HasSuffix(line, []byte(`\`)) && !isComment {
			joined, pending = append(joined, bytes.Trim(line, `\`)...), true
			return nil
		}
		if isComment {
			// pip puts a blank before a comment line, so the comment
			// starts after a blank in the joined text too.
			joined = append(joined, ' ')
		}
		joined = append(joined, line...)
		return end()
	})
	if err != nil || !pending {
		return err
	}
	// The last line ends in a backslash: pip reads what it joined.
	return end()
}

// option is where the options after a requirement start, such as
// --hash=, -1 for none: a - after a blank, a space or a tab, as pip splits
// options at any white space.
func option(s string) int {
	for i := 1; i < len(s); i++ {
		if s[i] == '-' && (s[i-1] == ' ' || s[i-1] == '\t') {
			return i
		}
	}
	return -1
}

// comment is where a comment starts in line, -1 for none: a # at the
// start or after a blank, as pip reads one.
func comment(line []byte) int {
	for i, c := range line {
		if c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return i
		}
	}
	return -1
}

// readPythonLock reads a poetry.lock or a uv.lock: an array of package
// tables, each with its name and the version resolved. uv lists the project
// itself too.
func readPythonLock(rd *reading, data []byte) error {
	t, err := tree(rd, data, core.TOMLFormat, rd.x.parsers.TOML, false)
	if err != nil {
		return err
	}
	return lockPackages(rd, t.Member("package"), "")
}

// lockPackages adds the version of each table of the array n whose name is
// the package's.
func lockPackages(rd *reading, n *core.Node, section string) error {
	if n == nil || n.Shape != core.Sequence {
		return nil
	}
	for _, p := range n.Items {
		name := p.Member("name")
		if name == nil || name.Shape != core.Scalar || !rd.is(name.Text) {
			continue
		}
		if err := lockVersion(rd, p, section); err != nil {
			return err
		}
	}
	return nil
}
