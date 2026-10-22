package ini

import (
	"bytes"
	"errors"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// The plain dialect reads INI as Python's configparser does with its
// defaults, from a file opened as UTF-8 text, which setup.cfg and most INI
// files are written for:
//
//   - a line ends at \n, \r\n or a lone \r, as Python's text mode reads it;
//   - a line whose first character other than white space is # or ; is a
//     comment; a ; or # later in a line is part of the value;
//   - [name] is a section header, the name everything between the first [
//     and the last ], white space kept;
//   - key = value or key: value at the first = or :, both sides stripped;
//   - a line indented deeper than its key continues the value, joined to it
//     with a newline, and so does an empty line between such lines; a
//     comment line inside a value is left out of it; the value ends without
//     white space at its end.
//
// A file that is not UTF-8 is refused, as Python refuses to decode it. It
// reads what configparser refuses with its default strict mode: a key
// repeated in a section gives every value, a section repeated adds to the
// first, and the keys before the first header are in the section with no
// name, which a spec selects by leaving section out. Names are compared as
// written, where configparser lowercases keys and setuptools does not.
// [DEFAULT] is a section like any other, and %(name)s is the text written:
// errata does not interpolate. In setup.cfg, mypy.ini, .mypy.ini and
// .coveragerc, a value that a tool reading the file changes, by errata's
// model of that tool's transform, is returned as written and marked not
// literal (plainNotLiteral).
var plainDialect = &iniDialect{
	name:       "plain",
	query:      configparserQuery(false),
	notLiteral: plainNotLiteral,
	read: func(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
		return readConfigparser(data, configparser{}, m, visit)
	},
}

// The tox dialect reads tox.ini as tox 4 does: configparser with strict
// mode and keys in lower case, then tox's own pass over each value
// (IniLoader.process_raw):
//
//   - a byte order mark is dropped, and a file that is not UTF-8 refused;
//   - a key set twice in a section, a section written twice, and a key
//     before the first header are refused, as configparser's strict mode
//     refuses them;
//   - keys are compared in lower case, the spec's key too; sections as
//     written;
//   - in each line of a value, a # that no \ comes before starts a comment,
//     which goes with the white space before it, a line that starts with #
//     goes, \# is #, and a \ at the end of a line joins it to the next.
//
// Factor conditions (py311: pytest) and substitutions ({posargs},
// {env:X}, {[base]deps}) are returned as written and marked not literal:
// tox computes them for an environment. [DEFAULT] is a section like any other, where tox lets every
// section inherit its keys.
var toxDialect = &iniDialect{
	name:       "tox",
	files:      func(file string) bool { _, base := core.FileParts(file); return base == "tox.ini" },
	query:      configparserQuery(true),
	notLiteral: toxNotLiteral,
	read: func(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
		return readConfigparser(bytes.TrimPrefix(data, bom), configparser{tox: true}, m, visit)
	},
}

// configparserQuery checks a plain or tox spec. lower is tox's: the key is
// compared in lower case, as configparser's optionxform makes it.
func configparserQuery(lower bool) func(Spec) (core.Query, error) {
	return func(i Spec) (core.Query, error) {
		if err := onlyDialect(i, false, false); err != nil {
			return core.Query{}, err
		}
		if i.Key == "" {
			return core.Query{}, errors.New("key is required")
		}
		key := i.Key
		if lower {
			key = strings.ToLower(key)
		}
		return core.Query{Section: []byte(i.Section), Key: []byte(key), HasKey: true}, nil
	}
}

// newToxKeys is the key map of one tox section; a variable, so a test sees
// each section get a map of its own.
var newToxKeys = func() map[string]bool { return map[string]bool{} }

// configparser is what differs between the readers built on configparser.
type configparser struct {
	tox bool // strict, keys in lower case, no keys before a header, process_raw
}

func readConfigparser(data []byte, c configparser, m *core.Meter, visit func(*core.Entry) error) error {
	tool := "configparser"
	if c.tox {
		tool = "tox"
	}
	if err := notUTF8(data, tool); err != nil {
		return err
	}
	var (
		section    []byte
		inSection  bool
		sectionSeq int
		cur        core.Entry // the key whose value is open
		open       bool       // cur has a value that an indented line may continue
		value      []byte
		indent     int    // the indent of the last header or key
		keyBuf     []byte // a tox key in lower case
		raw        []byte // a tox value after process_raw
		// tox refuses a section or a key written twice. A section is hashed
		// once, at its header, and a key only among its section's keys, a new
		// map at each header, since only DEFAULT may be written again: the
		// memory is the sections' names and one section's keys, and a long
		// section's name costs its length once.
		sections    map[string]bool
		keys, deflt map[string]bool
	)
	if c.tox {
		sections, deflt = map[string]bool{}, map[string]bool{}
	}
	finish := func() error {
		if !open {
			return nil
		}
		open = false
		cur.Value = pyTrimRight(value)
		if c.tox {
			if err := m.Charge(len(cur.Value)); err != nil {
				return err
			}
			raw, cur.Computed = toxProcess(raw[:0], cur.Value)
			cur.Value = raw
		}
		return visit(&cur)
	}
	err := eachTextLine(data, m, func(n int, line []byte) error {
		clean := pyStrip(line)
		comment := len(clean) > 0 && (clean[0] == '#' || clean[0] == ';')
		if len(clean) == 0 || comment {
			if !comment && open {
				value = append(value, '\n')
			}
			return nil
		}
		lead := pyIndent(line)
		if open && lead > indent {
			value = append(append(value, '\n'), clean...)
			return nil
		}
		if err := finish(); err != nil {
			return err
		}
		indent = lead
		if clean[0] == '[' {
			if end := bytes.LastIndexByte(clean, ']'); end >= 2 {
				section, inSection = clean[1:end], true
				sectionSeq++
				if c.tox {
					if string(section) == "DEFAULT" {
						keys = deflt
						return nil
					}
					if err := m.Charge(mapWork + len(section)); err != nil {
						return err
					}
					if sections[string(section)] {
						return core.LineError(n, "the section %s is written twice, which tox refuses", core.Quote(string(section)))
					}
					sections[string(section)] = true
					keys = newToxKeys() // a map's clear costs its size at its largest
				}
				return nil
			}
		}
		if c.tox && !inSection {
			return core.LineError(n, "a key before the first section header, which tox refuses")
		}
		at := bytes.IndexAny(clean, "=:")
		if at < 0 {
			return core.LineError(n, "%s is no section header, comment or key with = or :", core.Quote(string(clean)))
		}
		key := pyTrimRight(clean[:at])
		if len(key) == 0 {
			return core.LineError(n, "%s sets a value with no key", core.Quote(string(clean)))
		}
		if c.tox {
			keyBuf = append(keyBuf[:0], strings.ToLower(string(key))...)
			key = keyBuf
			if err := m.Charge(mapWork + len(key)); err != nil {
				return err
			}
			id := string(key)
			if keys[id] {
				return core.LineError(n, "the key %s is set twice in section %s, which tox refuses", core.Quote(string(key)), core.Quote(string(section)))
			}
			keys[id] = true
		}
		value = append(value[:0], pyStrip(clean[at+1:])...)
		cur = core.Entry{Section: section, SectionSeq: sectionSeq, Key: key, Line: n}
		open = true
		return nil
	})
	if err != nil {
		return err
	}
	return finish()
}

// toxProcess appends value as tox's IniLoader.process_raw makes it, outside
// an environment, to out: each line that starts with # goes, and in the
// others the first # that no \ comes before ends the line with the white
// space before it; then \# is #, \r goes, and a \ before a newline joins
// the two lines.
func toxProcess(out, value []byte) (processed []byte, factors bool) {
	first := true
	for line := range bytes.SplitSeq(value, newline) {
		if len(line) > 0 && line[0] == '#' {
			continue
		}
		if !first {
			out = append(out, '\n')
		}
		first = false
		for i := range line {
			if line[i] == '#' && (i == 0 || line[i-1] != '\\') {
				line = pyTrimRight(line[:i])
				break
			}
		}
		out = append(out, line...)
	}
	out = bytes.ReplaceAll(out, []byte(`\#`), []byte("#"))
	out = bytes.ReplaceAll(out, []byte("\r"), nil)
	// tox reads factor conditions here, before it joins the lines.
	for line := range bytes.SplitSeq(out, newline) {
		if toxFactorLine(line) {
			factors = true
			break
		}
	}
	return bytes.ReplaceAll(out, []byte("\\\n"), nil), factors
}
