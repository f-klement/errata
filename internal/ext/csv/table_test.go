package csv

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/plugin/wire"
)

const hosts = "name,ip,port\nbuilder,10.0.0.5,22\nweb,10.0.0.6,22\nbuilder,10.0.0.7,2222\n"

// The value is the cell of the column in each row whose cells match where,
// each with its file and the line its row is on, its key the column and
// its condition the where. Planted breaks: value matching no where key;
// table.line returning the line of field 0, which a cell after a
// multi-line cell then misplaced.
func TestTheValueIsTheCellOfTheRowWhereMatches(t *testing.T) {
	r := extractReply(t, `{"column": "ip", "where": [{"key": "name", "value": "builder"}]}`, file("data/hosts.csv", hosts))
	sameCells(t, r, "data/hosts.csv:2=10.0.0.5", "data/hosts.csv:4=10.0.0.7")
	if *r.Values[0].Key != "ip" || *r.Values[0].Condition != "name=builder" {
		t.Fatalf("got key %q and condition %q", *r.Values[0].Key, *r.Values[0].Condition)
	}
	sameCells(t, extractReply(t, `{"column": "port", "where": [{"key": "name", "value": "builder"}, {"key": "ip", "value": "10.0.0.7"}]}`, file("h.csv", hosts)), "h.csv:4=2222")
	sameCells(t, extractReply(t, `{"column": "ip", "where": [{"key": "name", "value": "a"}]}`, file("h.csv", "name,note,ip\na,\"two\nlines\",1.2.3.4\n")), "h.csv:3=1.2.3.4")
}

// With no where, every row's cell is a value; a row that is only a header
// gives none, and the reply is an empty list, which the rule's allow_empty
// decides. Planted break: next returning the header row as data.
func TestWithNoWhereEveryRowIsAValue(t *testing.T) {
	sameCells(t, extractReply(t, `{"column": "name"}`, file("h.csv", hosts)), "h.csv:2=builder", "h.csv:3=web", "h.csv:4=builder")
	r := extractReply(t, `{"column": "name"}`, file("h.csv", "name,ip\n"))
	if r.Values == nil || len(r.Values) != 0 {
		t.Fatalf("got %+v, want an empty list of values", r)
	}
}

// A where no row matches is no value, naming the file and the where, so
// allow_empty decides and the message says why. Planted break: extract
// replying with an empty list.
func TestAWhereNoRowMatchesIsNoValue(t *testing.T) {
	sameCells(t, extractReply(t, `{"column": "ip", "where": [{"key": "name", "value": "cache"}]}`, file("h.csv", hosts)), "no value: no row of h.csv has name=cache")
	sameCells(t, extractReply(t, `{"column": "ip", "where": [{"key": "name", "value": "x"}, {"key": "port", "value": "1"}]}`, file("a.csv", hosts), file("b.csv", hosts)), "no value: no row of the 2 files has name=x, port=1")
}

// Each file of a files entry is read with its own header, in the request's
// order. Planted break: read keeping the first file's columns for the
// next.
func TestEachFileHasItsOwnHeader(t *testing.T) {
	sameCells(t, extractReply(t, `{"column": "ip", "where": [{"key": "name", "value": "b"}]}`, file("a.csv", "name,ip\nb,1\n"), file("b.csv", "ip,name\n2,b\n")), "a.csv:2=1", "b.csv:2=2")
}

// A header row is required: a file with none, and a column or a where key
// the header does not name, make the rule unknown, as does a header that
// names the column twice. Planted breaks: end passing an empty file;
// index taking the first of two columns of one name.
func TestAHeaderRowIsRequired(t *testing.T) {
	for _, c := range []struct{ spec, data, want string }{
		{`{"column": "ip"}`, "", `error: h.csv: the file has no header row to name the column "ip"; write header: false to name columns by number`},
		{`{"column": "ip"}`, "\n\n", `error: h.csv: the file has no header row to name the column "ip"; write header: false to name columns by number`},
		{`{"column": "addr"}`, hosts, `error: h.csv:1: the header row has no column "addr"`},
		{`{"column": "ip", "where": [{"key": "host", "value": "b"}]}`, hosts, `error: h.csv:1: the header row has no column "host"`},
		{`{"column": "ip"}`, "\nname,ip,ip\n", `error: h.csv:2: the header row names "ip" twice, in columns 2 and 3`},
		{`{"column": "ip"}`, "name,ip,x,y,ip\n", `error: h.csv:1: the header row names "ip" twice, in columns 2 and 5`},
	} {
		sameCells(t, extractReply(t, c.spec, file("h.csv", c.data)), c.want)
	}
}

// With header: false the file has no header row, and columns are named by
// number from 1: the first row is data. A number past the first row's
// cells makes the rule unknown. Planted breaks: next passing over the
// first row with header: false; index taking n, not n-1.
func TestColumnsByNumberNeedNoHeader(t *testing.T) {
	sameCells(t, extractReply(t, `{"column": "2", "header": false, "where": [{"key": "1", "value": "builder"}]}`, file("h.csv", "builder,10.0.0.5\nweb,10.0.0.6\n")), "h.csv:1=10.0.0.5")
	sameCells(t, extractReply(t, `{"column": "3", "header": false}`, file("h.csv", "a,b,c\n")), "h.csv:1=c")
	sameCells(t, extractReply(t, `{"column": "3", "header": false}`, file("h.csv", "\na,b\n")), "error: h.csv:2: column 3 is past the 2 columns of the first row")
	sameCells(t, extractReply(t, `{"column": "1", "header": false}`, file("h.csv", "")))
}

// A file that does not parse makes the rule unknown, with the file, the
// line errata counts and what is wrong. A quote still open at the end of
// the file is on the line its row starts on, and one closed too early on
// its own line. Planted breaks: parseError dropping the line; the reader
// set to LazyQuotes, which read a bare quote; parseError placing an open
// quote at pe.Line, or every quote error at its row's start.
func TestAFileThatDoesNotParseIsAnError(t *testing.T) {
	for data, want := range map[string]string{
		"name,ip\na,1\nb,2,3\n":        "error: h.csv:3: a row of 3 cells, and the first row has 2",
		"name,ip\na,1\nb\n":            "error: h.csv:3: a row of 1 cells, and the first row has 2",
		"name,ip\na,1\nb,1\"2\n":       `error: h.csv:3: bare " in non-quoted-field`,
		"name,ip\na,\"1\"x\n":          `error: h.csv:2: extraneous or missing " in quoted-field`,
		"name,ip\na,\"1\nb,2\n":        `error: h.csv:2: extraneous or missing " in quoted-field`,
		"name,ip\r\na,\"1\r\n\r\nb,2":  `error: h.csv:2: extraneous or missing " in quoted-field`,
		"name,ip\nb,1\nc,\"2\n\n\n":    `error: h.csv:3: extraneous or missing " in quoted-field`,
		"name,ip\nb,1\nc,\"2\nx\"y,\n": `error: h.csv:4: extraneous or missing " in quoted-field`,
		"na\"me,ip\na,1\n":             `error: h.csv:1: bare " in non-quoted-field`,
	} {
		sameCells(t, extractReply(t, `{"column": "ip"}`, file("h.csv", data)), want)
	}
}

// A value's cell that is not UTF-8 makes the rule unknown: a value is text, and
// JSON would carry it with each bad byte replaced. Planted break: value
// skipping the check, which errata then read as U+FFFD.
func TestACellThatIsNotUTF8IsAnError(t *testing.T) {
	sameCells(t, extractReply(t, `{"column": "ip"}`, file("h.csv", "name,ip\na,\xff\n")), `error: h.csv:2: the cell of column "ip" is not UTF-8, and a value is text`)
	sameCells(t, extractReply(t, `{"column": "ip", "where": [{"key": "name", "value": "b"}]}`, file("h.csv", "name,ip\n\xff,\xff\nb,1\n")), "h.csv:3=1")
}

// The dialects: tsv reads a tab and delimiter any other character; a UTF-8
// byte order mark is not part of the first column's name. Planted breaks:
// newTable leaving Comma a comma; newTable keeping the mark, which a quoted
// header after it, as PowerShell's Export-Csv writes, then failed to parse.
func TestTheDialectsAndTheByteOrderMark(t *testing.T) {
	sameCells(t, extractReply(t, `{"column": "ip", "dialect": "tsv"}`, file("h.tsv", "name\tip\na,b\t1,2\n")), "h.tsv:2=1,2")
	sameCells(t, extractReply(t, `{"column": "ip", "delimiter": ";"}`, file("h.csv", "name;ip\na,b;1,2\n")), "h.csv:2=1,2")
	sameCells(t, extractReply(t, `{"column": "ip", "delimiter": "\u00a6"}`, file("h.csv", "name\u00a6ip\na\u00a61\n")), "h.csv:2=1")
	sameCells(t, extractReply(t, `{"column": "ip", "where": [{"key": "name", "value": "a"}]}`, file("h.csv", "\ufeffname,ip\na,1\n")), "h.csv:2=1")
	sameCells(t, extractReply(t, `{"column": "1", "header": false}`, file("h.csv", "\ufeffa,1\n")), "h.csv:1=a")
	powershell := "\ufeff\"name\",\"ip\"\r\n\"a\",\"1\"\r\n\"b\",\"2\"\r\n" // Export-Csv -Encoding UTF8
	sameCells(t, extractReply(t, `{"column": "ip", "where": [{"key": "name", "value": "b"}]}`, file("h.csv", powershell)), "h.csv:3=2")
}

// The values stop at wire.MaxValues, counted across the request's files:
// one more is an error naming the file and the line of the value past the
// limit, as a built in reader refuses it. Planted breaks: read without the
// check; extract handing each file the whole limit.
func TestTheValuesStopAtTheLimit(t *testing.T) {
	rows := func(n int) string { return "ip\n" + strings.Repeat("1\n", n) }
	if got := extractReply(t, `{"column": "ip"}`, file("h.csv", rows(wire.MaxValues))); len(got.Values) != wire.MaxValues || got.Error != nil {
		t.Fatalf("%d rows: got %d values and %v, want every row", wire.MaxValues, len(got.Values), got.Error)
	}
	over := fmt.Sprintf("error: h.csv:%d: the files hold more than %d values, the limit; narrow where or the files", wire.MaxValues+2, wire.MaxValues)
	sameCells(t, extractReply(t, `{"column": "ip"}`, file("h.csv", rows(wire.MaxValues+1))), over)
	sameCells(t, extractReply(t, `{"column": "ip"}`, file("a.csv", rows(wire.MaxValues)), file("b.csv", rows(1))),
		fmt.Sprintf("error: b.csv:2: the files hold more than %d values, the limit; narrow where or the files", wire.MaxValues))
}

// A file with no lone CR, most of them, keeps one slice of line starts for
// both counts. Planted break: newPositions always building lineend.Starts.
func TestPositionsKeepOneSliceWithoutALoneCR(t *testing.T) {
	if p := newPositions([]byte("a\r\nb\nc")); &p.ends[0] != &p.lf[0] {
		t.Fatal("a file with no lone CR keeps two slices of line starts")
	}
	if p := newPositions([]byte("a\rb\nc")); len(p.ends) != 3 || len(p.lf) != 2 {
		t.Fatalf("a file with a lone CR: %v and %v", p.ends, p.lf)
	}
}

// A line is counted as errata counts it, at a LF, a CRLF or a lone CR,
// where encoding/csv counts LFs alone. A lone CR inside a quoted cell
// moves every later row a line down. Planted break: positions.line
// returning encoding/csv's line.
func TestLinesAreErrataLines(t *testing.T) {
	data := "name,ip\r\nother,\"a\rb\"\r\nbuilder,\"10.0.0.\n1\"\r\nbuilder,3"
	sameCells(t, extractReply(t, `{"column": "ip", "where": [{"key": "name", "value": "builder"}]}`, file("h.csv", data)), "h.csv:4=10.0.0.\n1", "h.csv:6=3")
}

// positions.line places a byte past the data, which a parse error at the
// end of a file gives, on the last line, and line 0 on the first. Planted
// break: line without its clamp, which panicked.
func TestPositionsClampToTheData(t *testing.T) {
	p := newPositions([]byte("a\rb\nc"))
	for _, c := range []struct{ line, col, want int }{{1, 1, 1}, {1, 3, 2}, {2, 1, 3}, {2, 9, 3}, {9, 1, 3}, {0, 0, 1}} {
		if got := p.line(c.line, c.col); got != c.want {
			t.Errorf("line(%d, %d) = %d, want %d", c.line, c.col, got, c.want)
		}
	}
}
