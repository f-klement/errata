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
		return readConfigparser(core.DropBOM(data), configparser{tox: true}, m, visit)
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
	r := &cpReader{c: c, m: m, visit: visit}
	if c.tox {
		r.sections, r.deflt = map[string]bool{}, map[string]bool{}
	}
	if err := eachTextLine(data, m, r.line); err != nil {
		return err
	}
	return r.finish()
}

// cpReader is readConfigparser's state between lines.
type cpReader struct {
	c          configparser
	m          *core.Meter
	visit      func(*core.Entry) error
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
}

// finish visits the open key, if any, with its value.
func (r *cpReader) finish() error {
	if !r.open {
		return nil
	}
	r.open = false
	r.cur.Value = pyTrimRight(r.value)
	if r.c.tox {
		if err := r.m.Charge(len(r.cur.Value)); err != nil {
			return err
		}
		r.raw, r.cur.Computed = toxProcess(r.raw[:0], r.cur.Value)
		r.cur.Value = r.raw
	}
	return r.visit(&r.cur)
}

// line reads line n of the file: a blank line or a comment, a line that
// continues a value, a section header, or a key.
func (r *cpReader) line(n int, line []byte) error {
	clean := pyStrip(line)
	if r.skipBlankOrComment(clean) {
		return nil
	}
	lead := pyIndent(line)
	if r.open && lead > r.indent {
		r.value = append(append(r.value, '\n'), clean...)
		return nil
	}
	if err := r.finish(); err != nil {
		return err
	}
	r.indent = lead
	if done, err := r.header(n, clean); done || err != nil {
		return err
	}
	return r.key(n, clean)
}

// skipBlankOrComment skips clean, a stripped line, when it is blank or a
// comment, and reports whether it did. A blank line continues an open value
// with a line break.
func (r *cpReader) skipBlankOrComment(clean []byte) bool {
	comment := len(clean) > 0 && (clean[0] == '#' || clean[0] == ';')
	if len(clean) == 0 || comment {
		if !comment && r.open {
			r.value = append(r.value, '\n')
		}
		return true
	}
	return false
}

// header reads clean as a section header, and reports false where it is
// none.
func (r *cpReader) header(n int, clean []byte) (bool, error) {
	if clean[0] != '[' {
		return false, nil
	}
	end := bytes.LastIndexByte(clean, ']')
	if end < 2 {
		return false, nil
	}
	r.section, r.inSection = clean[1:end], true
	r.sectionSeq++
	if !r.c.tox {
		return true, nil
	}
	return true, r.toxSection(n)
}

// toxSection starts tox's record of the keys of the section just read, and
// refuses a section written twice other than DEFAULT.
func (r *cpReader) toxSection(n int) error {
	if string(r.section) == "DEFAULT" {
		r.keys = r.deflt
		return nil
	}
	if err := r.m.Charge(mapWork + len(r.section)); err != nil {
		return err
	}
	if r.sections[string(r.section)] {
		return core.LineError(n, "the section %s is written twice, which tox refuses", core.Quote(string(r.section)))
	}
	r.sections[string(r.section)] = true
	r.keys = newToxKeys() // a map's clear costs its size at its largest
	return nil
}

// key reads clean as a key, = or :, and the start of its value, which it
// opens.
func (r *cpReader) key(n int, clean []byte) error {
	if r.c.tox && !r.inSection {
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
	if r.c.tox {
		var err error
		if key, err = r.toxKey(n, key); err != nil {
			return err
		}
	}
	r.value = append(r.value[:0], pyStrip(clean[at+1:])...)
	r.cur = core.Entry{Section: r.section, SectionSeq: r.sectionSeq, Key: key, Line: n}
	r.open = true
	return nil
}

// toxKey is key in lower case, as tox reads it, and refuses a key its
// section already set.
func (r *cpReader) toxKey(n int, key []byte) ([]byte, error) {
	r.keyBuf = append(r.keyBuf[:0], strings.ToLower(string(key))...)
	key = r.keyBuf
	if err := r.m.Charge(mapWork + len(key)); err != nil {
		return nil, err
	}
	id := string(key)
	if r.keys[id] {
		return nil, core.LineError(n, "the key %s is set twice in section %s, which tox refuses", core.Quote(string(key)), core.Quote(string(r.section)))
	}
	r.keys[id] = true
	return key, nil
}

// toxUncommented appends value to out without its comments: each line
// that starts with # goes, and in the others the first # that no \ comes
// before ends the line with the white space before it.
func toxUncommented(out, value []byte) []byte {
	first := true
	for line := range bytes.SplitSeq(value, newline) {
		if len(line) > 0 && line[0] == '#' {
			continue
		}
		if !first {
			out = append(out, '\n')
		}
		first = false
		out = append(out, toxLineText(line)...)
	}
	return out
}

// toxLineText is line up to its first # that no \ comes before, without
// the white space before that #.
func toxLineText(line []byte) []byte {
	for i := range line {
		if line[i] == '#' && (i == 0 || line[i-1] != '\\') {
			return pyTrimRight(line[:i])
		}
	}
	return line
}

// toxProcess appends value as tox's IniLoader.process_raw makes it, outside
// an environment, to out: each line that starts with # goes, and in the
// others the first # that no \ comes before ends the line with the white
// space before it; then \# is #, \r goes, and a \ before a newline joins
// the two lines.
func toxProcess(out, value []byte) (processed []byte, factors bool) {
	out = toxUncommented(out, value)
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
