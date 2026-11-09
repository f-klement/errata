// Package table is the table extractor: one column of the rows of a Unix
// field table (fstab, passwd, group, hosts, exports and crypttab), read by
// the column names of the table's man page, as the tool that reads the
// file splits its lines.
package table

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns the values of Column in the rows of a field table, in file
// order, each at its file and line. File names the file, or Files the files
// of a set, each read as one table. Dialect names the table; left out, the
// file's name chooses it once a template suffix is taken off (dialectFor).
//
// Row keeps only the rows whose columns hold the values it names, each
// column one value: a field equal to it, or a list one of whose items is.
// Left out, every row is kept. A list column's value is its items, each
// one value.
type Spec struct {
	core.Where
	Dialect string            `json:"dialect,omitempty"`
	Column  string            `json:"column"`
	Row     map[string]string `json:"row,omitempty"`
}

// Kind is the table kind, which a rule writes under the key table.
var Kind = core.Kind[Spec]{Key: "table", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is s with only what decides where it reads: the file or files as
// core.PlaceFiles writes them, no max_bytes, the dialect written out, and
// the row's values in the form the dialect compares them, so two
// spellings of one host name are one place.
func Place(s Spec) Spec {
	s.Where = s.Place()
	d, err := chooseDialect(s)
	if err != nil {
		return s
	}
	s.Dialect = d.name
	if s.Row != nil {
		r := make(map[string]string, len(s.Row))
		for k, v := range s.Row {
			r[k] = d.foldValue(k, v)
		}
		s.Row = r
	}
	return s
}

// Resolve is s with its max_bytes and the dialect Build chooses written
// out, for errata list, sharing nothing with s.
func Resolve(s Spec) Spec {
	s.Where = s.Resolved()
	if d, err := chooseDialect(s); err == nil {
		s.Dialect = d.name
	}
	s.Row = maps.Clone(s.Row)
	return s
}

// Describe is what s's extractor returns: flat values under a label that
// names the dialect and the file, not literal in a template file. The
// label is "" when no dialect is chosen.
func Describe(s Spec) check.Description {
	d, err := chooseDialect(s)
	if err != nil {
		return check.Description{}
	}
	_, base := core.FileParts(firstFile(s.Where))
	return check.Description{Label: "table, " + d.name + " dialect on " + base, NotLiteral: templated(s.Where)}
}

// firstFile is the file w names, or the first entry of its files.
func firstFile(w core.Where) string {
	if w.File == "" && len(w.Files) > 0 {
		return w.Files[0]
	}
	return w.File
}

// templated reports whether a file w names is a template.
func templated(w core.Where) bool {
	for _, f := range append([]string{w.File}, w.Files...) {
		if _, base := core.FileParts(f); f != "" && core.IsTemplate(base) {
			return true
		}
	}
	return false
}

// Build checks s and returns its extractor: one file's, or a set's that
// reads each file as one table in the dialect s chooses.
func Build(s Spec) (check.Extractor, error) {
	d, err := chooseDialect(s)
	if err != nil {
		return nil, err
	}
	col, err := d.checkColumn("column", s.Column)
	if err != nil {
		return nil, err
	}
	crit, err := checkRow(d, s.Row)
	if err != nil {
		return nil, err
	}
	return core.BuildOneOrSet(Kind.Key, s.Where, func(src core.Source, _ bool) (core.FileReader, error) {
		return extractor{Source: src, d: d, col: col, row: crit}, nil
	})
}

// checkColumn is the index of the column name names, refused with what
// names it when the dialect has no such column.
func (d *dialect) checkColumn(what, name string) (int, error) {
	if name == "" {
		return 0, fmt.Errorf("table: %s is required; write one of the %s columns: %s", what, d.name, d.columnNames())
	}
	for i, c := range d.columns {
		if c.name == name {
			return i, nil
		}
	}
	return 0, fmt.Errorf("table: %s names %s, which is no column of %s; write one of %s", what, core.Quote(name), d.name, d.columnNames())
}

// checkRow is the criteria of row, ordered by column, in the form the
// dialect compares them; nil keeps every row.
func checkRow(d *dialect, row map[string]string) ([]criterion, error) {
	if row == nil {
		return nil, nil
	}
	if len(row) == 0 {
		return nil, errors.New("table: row names no column; name a column and the value it holds, or leave row out to take every row")
	}
	var out []criterion
	for _, k := range slices.Sorted(maps.Keys(row)) {
		i, err := d.checkColumn("row", k)
		if err != nil {
			return nil, err
		}
		out = append(out, criterion{col: i, want: d.foldValue(k, row[k])})
	}
	return out, nil
}

// criterion is one column a row must hold a value in, in the form the
// dialect compares it.
type criterion struct {
	col  int
	want string
}

// column is one column of a table, by its man page's name.
type column struct {
	name string
	// list is how a list column splits into items (listKind); listNone
	// for a field.
	list listKind
	// fold marks a column the tool compares in any case, ASCII only: the
	// host names of hosts, which glibc compares with strcasecmp.
	fold bool
	// def is the value of an optional trailing column a row leaves out:
	// fstab's freq and passno, crypttab's key file. A list column a row
	// leaves out has no item.
	def string
	// union names the columns whose cells this one holds, in order: hosts'
	// names, its canonical name and its aliases.
	union []int
	// blank marks a column a row may leave out that the tool then reads
	// as empty: passwd's gecos, directory and shell.
	blank bool
	// unmangle marks a column whose \ooo escapes the tool decodes:
	// fstab's first four, as libmount reads them.
	unmangle bool
	// means maps a word the tool reads as another to that word: crypttab's
	// key file - is none.
	means map[string]string
}

// listKind is how a list column's text splits into items.
type listKind uint8

const (
	listNone    listKind = iota
	listComma            // the field split at commas outside double quotes, as libmount splits options
	listPlain            // the field split at commas, as nfs-utils splits options once a word's quotes are gone
	listMembers          // the field split at commas, the white space before each item skipped, as glibc reads members
	listWords            // every word from the column on
)

// dialect is one table: its file names, its columns and how its tool
// splits a line into them.
type dialect struct {
	name string
	// names reports whether the dialect reads a file of these directories
	// and base name when the spec names no dialect.
	names   func(dirs []string, base string) bool
	columns []column
	// layout splits a line into a row's fields, for a dialect whose rows
	// are lines; own reads the rows of any other.
	layout layout
	own    readFunc
	// drops reports a row of fields fields that the tool passes over
	// although it has the fields it needs, an address or a number that
	// does not parse, and sets a number to the one the tool reads. nil
	// drops none.
	drops func(r row, fields int) bool
}

// readFunc visits the rows of data in file order, charging m, each cell
// marked by mk.
type readFunc func(data []byte, m *core.Meter, mk *marker, visit func(row) error) error

// columnNames lists d's columns in a message.
func (d *dialect) columnNames() string {
	var names []string
	for _, c := range d.columns {
		names = append(names, c.name)
	}
	return strings.Join(names, ", ")
}

// foldValue is v, a value of the column named col, in the form d compares
// it: lower case for a column compared in any case.
func (d *dialect) foldValue(col, v string) string {
	for _, c := range d.columns {
		if c.name == col && c.fold {
			return asciiLower(v)
		}
	}
	return v
}

// asciiLower is s with its ASCII letters in lower case, as strcasecmp
// compares them in the C locale.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// dialects are the dialects in the order a file name is tried against them.
var dialects = []*dialect{fstabDialect, passwdDialect, groupDialect, hostsDialect, exportsDialect, crypttabDialect}

// dialectNames lists the dialects in a message.
func dialectNames() string {
	var names []string
	for _, d := range dialects {
		names = append(names, d.name)
	}
	return strings.Join(names, ", ")
}

// dialectFor is the dialect of file, by its name once a template suffix is
// taken off, or nil.
func dialectFor(file string) *dialect {
	dirs, base := core.FileParts(core.Untemplated(file))
	for _, d := range dialects {
		if d.names(dirs, base) {
			return d
		}
	}
	return nil
}

// chooseDialect is the dialect s names, or the one the name of its file, or
// of the first entry of its files, chooses.
func chooseDialect(s Spec) (*dialect, error) {
	if s.Dialect != "" {
		for _, d := range dialects {
			if d.name == s.Dialect {
				return d, nil
			}
		}
		return nil, fmt.Errorf("table: dialect %s is not one errata reads; write one of %s, or leave it out to choose by the file's name", core.Quote(s.Dialect), dialectNames())
	}
	file := firstFile(s.Where)
	if d := dialectFor(file); d != nil {
		return d, nil
	}
	return nil, fmt.Errorf("table: no dialect reads a file named %s; write dialect: one of %s", core.Quote(path.Base(file)), dialectNames())
}

// named is the test of a base name among names.
func named(names ...string) func([]string, string) bool {
	return func(_ []string, base string) bool { return slices.Contains(names, base) }
}
