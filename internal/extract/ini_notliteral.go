package extract

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The values each dialect's tool computes, which errata returns as written
// and marks not literal (iniDialect.notLiteral). A rule never compares such
// a value as text: it is unknown, or skip_not_literal leaves it out. What
// errata itself reads as the tool does (a php string's quotes, an ansible
// string's escapes, tox's comments and joined lines) is literal.

// computedEntry is the test of the dialects whose readers mark what their
// tool computes as they read it: php, and ansible but for its names.
func computedEntry(string) func(*iniEntry) bool {
	return func(e *iniEntry) bool { return e.computed }
}

// ansibleNotLiteral is what ansible computes: a variable whose literal
// Python writes otherwise (pyValue), or a Jinja2 template, which ansible
// renders when it uses the variable, and a host with a range or a port,
// which ansible reads as the hosts the range expands to, without the port
// (web[01:03] is web01 to web03, db1:2222 is db1). A group a children
// section names is returned as written.
func ansibleNotLiteral(string) func(*iniEntry) bool {
	return func(e *iniEntry) bool {
		if e.kind == entryName {
			return e.host != nil && (bytes.IndexByte(e.value, '[') >= 0 || hasPort(e.value))
		}
		return e.computed || hasJinja(e.value)
	}
}

// hasJinja reports whether v holds the start of a Jinja2 expression,
// statement or comment.
func hasJinja(v []byte) bool {
	return bytes.Contains(v, []byte("{{")) || bytes.Contains(v, []byte("{%")) || bytes.Contains(v, []byte("{#"))
}

// toxNotLiteral is a tox value tox computes, by the one rule tox.ini and
// setup.cfg's tox sections share (toxChanges): the tox dialect returns
// process_raw's text, so only factors (toxProcess marks them) and
// substitutions are left to differ. In a [coverage:*] section, which
// coverage.py reads from tox.ini, coverage's rule holds instead.
func toxNotLiteral(string) func(*iniEntry) bool {
	return func(e *iniEntry) bool {
		if coverageSection(e.section) {
			return coverageChanges(e.value)
		}
		return e.computed || toxSubstitutes(e.value)
	}
}

// toxChanges reports whether tox's reading of v, a value as configparser
// returns it, differs from v: process_raw drops comments and joins the
// lines a \ ends (toxProcess), keeps a line with a factor condition only
// for the environments its factors select, and replace substitutes. The
// plain dialect returns v, which setup.cfg's [tox:tox] and [testenv*]
// hold.
func toxChanges(v []byte) bool {
	processed, factors := toxProcess(nil, v)
	return factors || !bytes.Equal(processed, v) || toxSubstitutes(processed)
}

// toxSubstitutes reports whether tox's replace pass changes v, as
// tox.config.loader.replacer parses it: a { with a } after it is a
// substitution ({posargs}, {env:X}, {[base]deps}; one tox cannot resolve
// stays as written, which errata cannot tell), [] is {posargs}, and a \
// before : { } [ or ] is dropped. A \\ is kept.
func toxSubstitutes(v []byte) bool {
	if bytes.Contains(v, []byte("[]")) {
		return true
	}
	if i := bytes.IndexByte(v, '{'); i >= 0 && bytes.IndexByte(v[i+1:], '}') >= 0 {
		return true
	}
	for i := 0; i+1 < len(v); i++ {
		if v[i] != '\\' {
			continue
		}
		switch v[i+1] {
		case '\\':
			i++
		case ':', '{', '}', '[', ']':
			return true
		}
	}
	return false
}

// toxFactorLine reports whether tox reads line as a factor condition
// (expand_factors): a : followed by white space or the end of the line, and
// before it factors find_factor_groups takes, such as py311, !pypy or
// {py311,py312}-django. tox keeps such a line only for the environments
// its factors select.
func toxFactorLine(line []byte) bool {
	at := -1
	for i := range line {
		if line[i] != ':' {
			continue
		}
		if i+1 == len(line) {
			at = i
			break
		}
		if r, _ := utf8.DecodeRune(line[i+1:]); unicode.IsSpace(r) {
			at = i
			break
		}
	}
	return at >= 0 && toxFactors(strings.TrimSpace(string(line[:at])))
}

// toxFactors reports whether find_factor_groups takes s: groups split at
// commas outside braces, each of text and {a,b} brace groups, every
// alternative and every text stripped of white space at its ends, made of
// factors of word characters, ., * and ?, joined by -, each with a ! before
// it or not. An empty s holds no factor, which tox also takes.
func toxFactors(s string) bool {
	part := func(t string) bool {
		t = strings.TrimSpace(t)
		start := true // at the start of a factor, where a ! may stand
		for _, r := range t {
			switch {
			case r == '-':
				start = true
				continue
			case r == '!' && start:
			case r == '.' || r == '*' || r == '?' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			default:
				return false
			}
			start = false
		}
		return true
	}
	for len(s) > 0 {
		open := strings.IndexByte(s, '{')
		comma := strings.IndexByte(s, ',')
		switch {
		case open >= 0 && (comma < 0 || open < comma):
			if !part(s[:open]) {
				return false
			}
			end := strings.IndexByte(s[open:], '}')
			if end <= 1 {
				return false // { with no }, or {}: text tox's factor pattern refuses
			}
			for _, alt := range strings.Split(s[open+1:open+end], ",") {
				if !part(alt) {
					return false
				}
			}
			s = s[open+end+1:]
		case comma >= 0:
			if !part(s[:comma]) {
				return false
			}
			s = s[comma+1:]
		default:
			return part(s)
		}
	}
	return true
}

// plainNotLiteral is a value of a plain file that a tool reading the file
// changes. errata models each tool's transform, and a value is not literal
// exactly when the transform's output differs from the text errata
// returns:
//
//   - setup.cfg: setuptools reads every value with configparser's
//     interpolation, which changes every value with a % (%(name)s is
//     another key's value, %% is %, and any other % is an error), and an
//     attr: or a file: directive, read from the package or a file;
//   - mypy, in [mypy*] of setup.cfg, mypy.ini and .mypy.ini: its path
//     options, expanded with expanduser and expandvars (mypyChanges);
//   - coverage.py, in [coverage:*] of setup.cfg and in .coveragerc:
//     substitute_variables (coverageChanges);
//   - tox, in [tox:tox] and [testenv*] of setup.cfg: process_raw, factors
//     and substitutions, as in tox.ini (toxChanges).
//
// Another plain file, such as pytest.ini or .flake8, is read as written:
// errata models no transform of its tool.
func plainNotLiteral(file string) func(*iniEntry) bool {
	switch _, base := fileParts(file); base {
	case "setup.cfg":
		return func(e *iniEntry) bool {
			switch v := e.value; {
			case bytes.IndexByte(v, '%') >= 0 || bytes.HasPrefix(v, []byte("attr:")) || bytes.HasPrefix(v, []byte("file:")):
				return true
			case mypySection(e.section):
				return mypyChanges(e.key, v)
			case coverageSection(e.section):
				return coverageChanges(v)
			case toxSection(e.section):
				return toxChanges(v)
			}
			return false
		}
	case "mypy.ini", ".mypy.ini":
		return func(e *iniEntry) bool { return mypySection(e.section) && mypyChanges(e.key, e.value) }
	case ".coveragerc":
		return func(e *iniEntry) bool { return coverageChanges(e.value) }
	}
	return nil
}

// mypyPaths are the options mypy expands as paths (config_parser's
// expand_path, os.path.expandvars(os.path.expanduser(path))), and how mypy
// splits each into entries: mypy_path at , and :, files at , alone, whose
// entries it also globs.
var mypyPaths = map[string]string{
	"mypy_path": ",:", "files": ",",
	"cache_dir": "", "custom_typeshed_dir": "", "python_executable": "", "junit_xml": "", "quickstart_file": "",
}

// mypyChanges reports whether mypy's expansion changes the value of key:
// a ~ that starts the path, or an entry of a list, which expanduser
// replaces, a $name or ${name}, which expandvars replaces when the
// environment holds it (errata cannot know the environment, so a reference
// counts as a change), and in files a *, ? or [, which glob expands
// (split_and_match_files_list). mypy reads any other option as written.
// Keys are compared as configparser and mypy compare them, in lower case
// and with - as _.
func mypyChanges(key, v []byte) bool {
	name := strings.ReplaceAll(strings.ToLower(string(key)), "-", "_")
	seps, ok := mypyPaths[name]
	switch {
	case !ok:
		return false
	case hasPosixVar(v):
		return true
	}
	for _, entry := range bytes.FieldsFunc(v, func(r rune) bool { return strings.ContainsRune(seps, r) }) {
		entry = bytes.TrimSpace(entry)
		if bytes.HasPrefix(entry, []byte("~")) || name == "files" && bytes.ContainsAny(entry, "*?[") {
			return true
		}
	}
	return false
}

// hasPosixVar reports whether posixpath.expandvars finds a variable in v:
// a $ and ASCII word characters, or ${, anything and }.
func hasPosixVar(v []byte) bool {
	for i := range v {
		if v[i] != '$' || i+1 == len(v) {
			continue
		}
		switch c := v[i+1]; {
		case c == '_' || isAlpha(c) || isDigit(c):
			return true
		case c == '{' && bytes.IndexByte(v[i+2:], '}') >= 0:
			return true
		}
	}
	return false
}

// coverageChanges reports whether coverage.py's substitute_variables
// changes v: it replaces $$, a $ and a word, and ${...}, with -default or
// not (the pattern \$(\$|\w|\{)). A $ before anything else, as in raise
// NotImplementedError$ or $ 5, stays as written.
func coverageChanges(v []byte) bool {
	for i := range v {
		if v[i] != '$' || i+1 == len(v) {
			continue
		}
		// Python's \w is str.isalnum and _: a letter, or a number of any
		// kind, ² too.
		if r, _ := utf8.DecodeRune(v[i+1:]); r == '$' || r == '{' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

// mypySection is a section mypy reads: [mypy], and [mypy-pattern] for
// modules.
func mypySection(s []byte) bool {
	return string(s) == "mypy" || bytes.HasPrefix(s, []byte("mypy-"))
}

// coverageSection is a section coverage.py reads from setup.cfg or tox.ini.
func coverageSection(s []byte) bool { return bytes.HasPrefix(s, []byte("coverage:")) }

// toxSection is a section tox reads from setup.cfg: [tox:tox], and
// [testenv] and [testenv:name].
func toxSection(s []byte) bool {
	return string(s) == "tox:tox" || string(s) == "testenv" || bytes.HasPrefix(s, []byte("testenv:"))
}

// templateSuffixes end the names of template files, such as php.ini.j2,
// which an engine renders before any tool reads them. .tpl and .tftpl are
// also Terraform's templatefile.
var templateSuffixes = []string{".j2", ".jinja", ".jinja2", ".tmpl", ".tpl", ".tftpl", ".erb"}

// isTemplate reports whether a file's name ends in a template suffix.
func isTemplate(base string) bool {
	for _, s := range templateSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	return false
}

// templateNotLiteral is, for a template file, the test of a value the
// engine renders, or that test, the dialect's own, which may be nil,
// marks: one that holds {{, {% or {# (hasJinja), <% in ERB, and ${ or %{
// in Terraform's .tpl and .tftpl. For another file it is test.
func templateNotLiteral(file string, test func(*iniEntry) bool) func(*iniEntry) bool {
	_, base := fileParts(file)
	if !isTemplate(base) {
		return test
	}
	erb := strings.HasSuffix(base, ".erb")
	terraform := strings.HasSuffix(base, ".tpl") || strings.HasSuffix(base, ".tftpl")
	return func(e *iniEntry) bool {
		v := e.value
		return hasJinja(v) || erb && bytes.Contains(v, []byte("<%")) ||
			terraform && (bytes.Contains(v, []byte("${")) || bytes.Contains(v, []byte("%{"))) ||
			test != nil && test(e)
	}
}

// pyComputed reports whether ansible's value for lit, a Python literal
// other than a string, written alone, differs from its text as Python
// writes it (str of the value): a number written otherwise (0x10, 1_000,
// +5, 1.50, 1e3, 2.0j, and 1+2j, which is (1+2j)), or a container. True,
// False, None, a decimal integer, a float and an imaginary number written
// as Python writes them are literal, and so is ..., which ansible turns
// back into the text "...", as it turns a complex number into its str.
func pyComputed(lit []byte) bool {
	t := string(lit)
	switch {
	case t == "True" || t == "False" || t == "None" || t == "...":
		return false
	case t == "" || strings.ContainsAny(t[:1], "[({") || strings.HasPrefix(t, "set"):
		return true
	case t[0] == '+':
		return true
	}
	neg := t[0] == '-'
	if neg {
		rest := t[1:]
		if len(rest) == 0 || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\f' {
			return true // blanks after the sign go
		}
		t = rest
	}
	switch {
	case strings.IndexByte(t, '_') >= 0:
		return true // underscores go
	case t[len(t)-1] == 'j' || t[len(t)-1] == 'J':
		// str of a complex number with a real part of +0 is its imaginary
		// part as repr writes it, without a .0, and j; -1j is (-0-1j).
		f, err := strconv.ParseFloat(t[:len(t)-1], 64)
		if neg || err != nil && !math.IsInf(f, 0) {
			return true
		}
		return t != pyImagRepr(f)
	case len(t) >= 2 && t[0] == '0' && strings.IndexByte("xXoObB", t[1]) >= 0:
		return true
	case strings.ContainsAny(t, ".eE"):
		f, err := strconv.ParseFloat(t, 64)
		if err != nil && !math.IsInf(f, 0) {
			return true
		}
		if neg {
			return "-"+t != pyFloatRepr(-f)
		}
		return t != pyFloatRepr(f)
	}
	return t[0] == '0' && (len(t) > 1 || neg) // 00 is 0, and -0 is 0
}

// pyImagRepr is str(complex(0, f)) as Python writes it: repr's digits and
// form, without the .0 a float gets, then j.
func pyImagRepr(f float64) string {
	return strings.TrimSuffix(pyFloatRepr(f), ".0") + "j"
}

// pyFloatRepr is repr(f) as Python writes a float: the shortest digits that
// read back as f, fixed when its exponent is -4 to 15 and with a .0 when it
// has no fraction, else in e notation with at least two exponent digits.
func pyFloatRepr(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	exp, _ := strconv.Atoi(e[strings.IndexByte(e, 'e')+1:])
	if exp < -4 || exp >= 16 {
		return e
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}
