package csv

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/plugin/wire"
)

// spec is one entry of the csv kind: the column whose cell is the value,
// the cells the row must hold (where), whether the file's first row is a
// header that names the columns, and the delimiter between cells, which
// the dialect names (csv a comma, tsv a tab) or delimiter sets.
type spec struct {
	column    string
	where     []wire.KeyValue
	header    bool
	dialect   string
	delimiter rune
}

// The dialects.
const (
	dialectCSV = "csv"
	dialectTSV = "tsv"
)

// rawSpec is a spec as a request carries it. A member left out is nil.
type rawSpec struct {
	Column    *string         `json:"column"`
	Where     []wire.KeyValue `json:"where"`
	Header    *bool           `json:"header"`
	Dialect   *string         `json:"dialect"`
	Delimiter *string         `json:"delimiter"`
}

// parseSpec reads a spec as a build or an extract request carries it, and
// refuses one errata should not have sent or the kind cannot read. wire's
// strict decode refuses a member the kind does not declare, one in another
// case, and one written twice.
func parseSpec(raw json.RawMessage) (spec, error) {
	var r rawSpec
	if err := wire.DecodeSpec(raw, &r); err != nil {
		return spec{}, err
	}
	if r.Column == nil {
		return spec{}, errors.New("column is required")
	}
	s := spec{column: *r.Column, where: r.Where, header: r.Header == nil || *r.Header}
	var err error
	if s.dialect, s.delimiter, err = dialectOf(r.Dialect, r.Delimiter); err != nil {
		return spec{}, err
	}
	return s, s.checkColumns()
}

// dialectOf is the dialect and the delimiter of a spec that sets dialect,
// delimiter, both or neither. A tab is the tsv dialect and any other
// delimiter the csv one, so {dialect: tsv} and {delimiter: "\t"} are one
// spec, and a dialect that disagrees with the delimiter is refused.
func dialectOf(dialect, delimiter *string) (string, rune, error) {
	name, err := dialectName(dialect)
	if err != nil {
		return "", 0, err
	}
	if delimiter == nil {
		if name == dialectTSV {
			return dialectTSV, '\t', nil
		}
		return dialectCSV, ',', nil
	}
	d, err := delimiterOf(*delimiter)
	if err != nil {
		return "", 0, err
	}
	implied := dialectCSV
	if d == '\t' {
		implied = dialectTSV
	}
	if name != "" && name != implied {
		return "", 0, fmt.Errorf("dialect %s and delimiter %s disagree: the tsv dialect is the tab delimiter; leave one of them out", name, strconv.QuoteToASCII(*delimiter))
	}
	return implied, d, nil
}

// dialectName is the dialect a spec names, or "" when it names none.
func dialectName(dialect *string) (string, error) {
	if dialect == nil {
		return "", nil
	}
	if *dialect != dialectCSV && *dialect != dialectTSV {
		return "", fmt.Errorf("dialect is %s; write csv or tsv, or set delimiter for another delimiter", strconv.Quote(*dialect))
	}
	return *dialect, nil
}

// delimiterOf is the delimiter text sets: one character, as encoding/csv
// takes it, which is not a quote, a CR, a LF or NUL.
func delimiterOf(text string) (rune, error) {
	r, size := utf8.DecodeRuneInString(text)
	if size == 0 || size != len(text) || r == utf8.RuneError || r == 0 || strings.ContainsRune("\"\r\n", r) {
		return 0, fmt.Errorf("delimiter is %s; write one character that is not a quote, a line end or NUL", strconv.QuoteToASCII(text))
	}
	return r, nil
}

// number is a column named by its number, from 1, as a spec with header:
// false names its columns. Nine digits keep it far inside an int.
var number = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

// checkColumns refuses an empty column and, with header: false, a column
// or a where key that is not a column's number. It refuses a where key
// written twice.
func (s spec) checkColumns() error {
	names := []string{s.column}
	for i, kv := range s.where {
		if slices.ContainsFunc(s.where[:i], func(o wire.KeyValue) bool { return o.Key == kv.Key }) {
			return fmt.Errorf("where names the column %s twice", strconv.Quote(kv.Key))
		}
		names = append(names, kv.Key)
	}
	for _, n := range names {
		switch {
		case n == "":
			return errors.New("a column is named \"\"; name a column of the header")
		case !s.header && !number.MatchString(n):
			return fmt.Errorf("the column %s is no column number: with header: false, a file has no header row, and column and the keys of where name a column by its number, from 1", strconv.Quote(n))
		}
	}
	return nil
}

// written is the spec with every default written out, as a build reply
// carries it: where only when it names a column.
func (s spec) written() json.RawMessage {
	out, _ := json.Marshal(struct {
		Column    string          `json:"column"`
		Where     []wire.KeyValue `json:"where,omitempty"`
		Header    bool            `json:"header"`
		Dialect   string          `json:"dialect"`
		Delimiter string          `json:"delimiter"`
	}{s.column, s.where, s.header, s.dialect, string(s.delimiter)})
	return out
}

// label is the kind as a message names it: csv, with the dialect or the
// delimiter when it is not a comma's.
func (s spec) label() string {
	switch {
	case s.dialect == dialectTSV:
		return "csv, tsv dialect"
	case s.delimiter != ',':
		return "csv, delimiter " + strconv.QuoteRuneToASCII(s.delimiter)
	}
	return "csv"
}

// condition is what the row of each value holds, as a report names it
// beside the place: each where key and its cell, such as name=builder.
// It is empty for a spec with no where.
func (s spec) condition() string {
	parts := make([]string, len(s.where))
	for i, kv := range s.where {
		parts[i] = kv.Key + "=" + kv.Value
	}
	return strings.Join(parts, ", ")
}

// noRow is why the spec found no value in files: no row matched its where.
func (s spec) noRow(files []wire.File) string {
	in := "the " + strconv.Itoa(len(files)) + " files"
	if len(files) == 1 {
		in = files[0].Path
	}
	return "no row of " + in + " has " + s.condition()
}
