package csv

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/plugin/wire"
)

// tb is what the helpers need of a test, so a property test's *rapid.T
// can call them too.
type tb interface {
	Helper()
	Fatalf(format string, args ...any)
	Fatal(args ...any)
}

// respond sends req, marshalled, and returns the reply, failing the test
// on an error.
func respond(t tb, req any) []byte {
	t.Helper()
	in, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Respond(in)
	if err != nil {
		t.Fatalf("%s: %v", in, err)
	}
	return out
}

// buildOne sends spec, a JSON object, in a build request of one entry and
// returns its result, read as errata reads it.
func buildOne(t tb, spec string) wire.Built {
	t.Helper()
	out := respond(t, wire.BuildRequest{Protocol: 1, Op: wire.OpBuild, Entries: []wire.Entry{{Rule: "r", Spec: json.RawMessage(spec)}}})
	var reply wire.BuildReply
	if err := wire.Decode(out, &reply); err != nil {
		t.Fatalf("errata refuses the build reply %s: %v", out, err)
	}
	return reply.Built[0]
}

// refusal is the error a build of spec replies with, or "" when it builds.
func refusal(t *testing.T, spec string) string {
	t.Helper()
	b := buildOne(t, spec)
	if b.Error == nil {
		return ""
	}
	return *b.Error
}

// file is a file of an extract request.
func file(path, data string) wire.File { return wire.File{Path: path, Data: []byte(data)} }

// extractReply sends spec, as a build writes it out, and files in an
// extract request, and returns the reply, read as errata reads it.
func extractReply(t tb, spec string, files ...wire.File) wire.ExtractReply {
	t.Helper()
	b := buildOne(t, spec)
	if b.Error != nil {
		t.Fatalf("build %s: %s", spec, *b.Error)
	}
	out := respond(t, wire.ExtractRequest{Protocol: 1, Op: wire.OpExtract, Spec: b.Spec, Files: files, Values: map[string][]wire.Value{}, MaxBytes: 32 << 20})
	var reply wire.ExtractReply
	if err := wire.Decode(out, &reply); err != nil {
		t.Fatalf("errata refuses the extract reply %s: %v", out, err)
	}
	return reply
}

// cells are the values of an extract reply, each as file:line=text, or the
// reply's error or no_value as error: and no value:.
func cells(r wire.ExtractReply) []string {
	switch {
	case r.Error != nil:
		return []string{"error: " + *r.Error}
	case r.NoValue != nil:
		return []string{"no value: " + *r.NoValue}
	}
	out := []string{}
	for _, v := range r.Values {
		out = append(out, v.File+":"+strconv.Itoa(v.Line)+"="+*v.Text)
	}
	return out
}

// sameCells fails the test unless got is want.
func sameCells(t *testing.T, got wire.ExtractReply, want ...string) {
	t.Helper()
	g := cells(got)
	if strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n  %s\nwant\n  %s", strings.Join(g, "\n  "), strings.Join(want, "\n  "))
	}
}

// describe offers protocol 1 and the plugin chooses it, naming itself csv
// at its version, with its fields in order and column required. Planted
// break: Where set to file, which a files entry then could not use.
func TestDescribeChoosesProtocol1(t *testing.T) {
	out := respond(t, wire.DescribeRequest{Op: wire.OpDescribe, Protocols: []int{1}})
	var d wire.DescribeReply
	if err := wire.Decode(out, &d); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	if err := d.Check(); err != nil {
		t.Fatalf("%s: errata refuses the describe: %v", out, err)
	}
	var names []string
	for _, f := range d.Fields {
		names = append(names, f.Name+":"+f.Type)
	}
	if d.Protocol != 1 || d.Name != "csv" || d.Version != Version || d.Where != wire.WhereFiles ||
		strings.Join(names, " ") != "column:string where:map header:bool dialect:string delimiter:string" || !d.Fields[0].Required {
		t.Fatalf("got %s", out)
	}
}

// A describe that offers no version the plugin speaks gets the versions it
// does speak, which errata names in its message. Planted break: describe
// choosing 1 whatever it was offered.
func TestDescribeOfOtherVersionsListsProtocol1(t *testing.T) {
	out := respond(t, wire.DescribeRequest{Op: wire.OpDescribe, Protocols: []int{2, 3}})
	v, err := wire.ReadVersion(out)
	if err != nil || v.Chose() || len(v.Protocols) != 1 || v.Protocols[0] != 1 || v.Name != "csv" {
		t.Fatalf("got %s, %v", out, err)
	}
}

// A build writes every default out, so that two entries the defaults make
// equal are one place: {dialect: tsv} and {delimiter: "\t"} are one spec.
// Planted breaks: written leaving header out; dialectOf naming a tab
// delimiter csv.
func TestBuildWritesTheDefaultsOut(t *testing.T) {
	for spec, want := range map[string]string{
		`{"column": "ip"}`: `{"column":"ip","header":true,"dialect":"csv","delimiter":","}`,
		`{"column": "2", "where": [{"key": "1", "value": "b"}], "header": false}`: `{"column":"2","where":[{"key":"1","value":"b"}],"header":false,"dialect":"csv","delimiter":","}`,
		`{"column": "ip", "dialect": "tsv"}`:                                      `{"column":"ip","header":true,"dialect":"tsv","delimiter":"\t"}`,
		`{"column": "ip", "delimiter": "\t"}`:                                     `{"column":"ip","header":true,"dialect":"tsv","delimiter":"\t"}`,
		`{"column": "ip", "delimiter": ";", "dialect": "csv"}`:                    `{"column":"ip","header":true,"dialect":"csv","delimiter":";"}`,
		`{"column": "ip", "where": []}`:                                           `{"column":"ip","header":true,"dialect":"csv","delimiter":","}`,
	} {
		if got := buildOne(t, spec); got.Error != nil || string(got.Spec) != want {
			t.Errorf("%s: got %s, %v, want %s", spec, got.Spec, got.Error, want)
		}
	}
}

// The label names the dialect or the delimiter when it is not a comma, and
// every value is flat text that reads no other extractor. Planted break:
// label returning csv for every spec.
func TestTheLabelNamesTheDelimiter(t *testing.T) {
	for spec, want := range map[string]string{
		`{"column": "a"}`:                        "csv",
		`{"column": "a", "dialect": "tsv"}`:      "csv, tsv dialect",
		`{"column": "a", "delimiter": ";"}`:      "csv, delimiter ';'",
		`{"column": "a", "delimiter": "\u00a6"}`: "csv, delimiter '\\u00a6'",
		`{"column": "a", "dialect": "csv"}`:      "csv",
		`{"column": "a", "header": false}`:       "",
		`{"column": "1", "header": false}`:       "csv",
		`{"column": "a", "delimiter": "\t"}`:     "csv, tsv dialect",
	} {
		b := buildOne(t, spec)
		if want == "" {
			if b.Error == nil {
				t.Errorf("%s: built, want it refused", spec)
			}
			continue
		}
		if b.Error != nil || b.Description.Label != want || b.Description.Shape != wire.ShapeFlat || b.Description.NotLiteral || len(b.Description.Reads) != 0 {
			t.Errorf("%s: got %+v, %v, want the label %s", spec, b.Description, b.Error, want)
		}
	}
}

// A spec the kind cannot read is refused with the reason, which errata
// makes a config error. Planted breaks, each its row's: parseSpec
// decoding with json.Unmarshal, not wire.DecodeSpec; dialectName passing
// any name; delimiterOf passing two characters, or a quote; checkColumns
// passing an empty column, a name with header: false, or a where key
// written twice.
func TestBuildRefusesASpecItCannotRead(t *testing.T) {
	for spec, want := range map[string]string{
		`{"column": "a", "Header": true}`:                                                    `the member "Header" must be written "header"`,
		`{"column": "a", "column": "b"}`:                                                     `the member "column" is written twice`,
		`{"column": "a", "rows": 1}`:                                                         `unknown field "rows"`,
		`{"header": true}`:                                                                   "column is required",
		`{"column": "a", "dialect": "excel"}`:                                                `dialect is "excel"; write csv or tsv`,
		`{"column": "a", "delimiter": ";;"}`:                                                 `delimiter is ";;"; write one character`,
		`{"column": "a", "delimiter": ""}`:                                                   `delimiter is ""`,
		`{"column": "a", "delimiter": "\""}`:                                                 `delimiter is "\""`,
		`{"column": "a", "delimiter": "\n"}`:                                                 `delimiter is "\n"`,
		`{"column": "a", "delimiter": "\u0000"}`:                                             `delimiter is "\x00"`,
		`{"column": "a", "delimiter": "\ufffd"}`:                                             `delimiter is "\ufffd"`,
		`{"column": "a", "dialect": "tsv", "delimiter": ";"}`:                                `dialect tsv and delimiter ";" disagree`,
		`{"column": "a", "dialect": "csv", "delimiter": "\t"}`:                               `dialect csv and delimiter "\t" disagree`,
		`{"column": ""}`:                                                                     `a column is named ""`,
		`{"column": "a", "where": [{"key": "", "value": "x"}]}`:                              `a column is named ""`,
		`{"column": "ip", "header": false}`:                                                  `the column "ip" is no column number`,
		`{"column": "0", "header": false}`:                                                   `the column "0" is no column number`,
		`{"column": "01", "header": false}`:                                                  `the column "01" is no column number`,
		`{"column": "1", "header": false, "where": [{"key": "x", "value": "y"}]}`:            `the column "x" is no column number`,
		`{"column": "a", "where": [{"key": "n", "value": "1"}, {"key": "n", "value": "2"}]}`: `where names the column "n" twice`,
		`[1]`:           "an object is required",
		`{"column": 1}`: "cannot unmarshal",
	} {
		if got := refusal(t, spec); !strings.Contains(got, want) {
			t.Errorf("%s: got %q, want it refused with %q", spec, got, want)
		}
	}
}

// A request that breaks the protocol gets no reply, and Main exits 2 with
// the reason on stderr; errata makes the rule unknown with it. Planted
// breaks: checkProtocol passing protocol 2; Main exiting 0 on an error.
func TestARequestThatBreaksTheProtocolIsRefused(t *testing.T) {
	for req, want := range map[string]string{
		`{"op": "list"}`: `the request's op is "list"`,
		`[]`:             "the request is no JSON object",
		`{"protocol": 2, "op": "build", "entries": []}`:                                                       "written in protocol 2, and errata-ext-csv speaks 1",
		`{"protocol": 2, "op": "extract", "spec": {}, "files": [], "values": {}, "max_bytes": 1}`:             "written in protocol 2",
		`{"protocol": 1, "op": "build", "entries": [], "extra": 1}`:                                           "unknown field",
		`{"protocol": 1, "op": "extract", "spec": {"column": "a"}, "files": []}`:                              `"values" is missing`,
		`{"protocol": 1, "op": "extract", "spec": {"column": ""}, "files": [], "values": {}, "max_bytes": 1}`: `the extract request's spec: a column is named ""`,
		`{"op": "describe", "protocols": null}`:                                                               "null",
	} {
		var stdout, stderr bytes.Buffer
		code := Main(strings.NewReader(req), &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "errata-ext-csv: ") || !strings.Contains(stderr.String(), want) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit 2 and %q", req, code, stdout.String(), stderr.String(), want)
		}
	}
}

// Main writes the reply to stdout and exits 0. Planted break: Main
// writing the request back instead of the reply.
func TestMainWritesTheReply(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Main(strings.NewReader(`{"op": "describe", "protocols": [1]}`), &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	var d wire.DescribeReply
	if err := wire.Decode(stdout.Bytes(), &d); err != nil || d.Name != "csv" {
		t.Fatalf("stdout %q: %v", stdout.String(), err)
	}
}

// Main fails on a stdin it cannot read, and on a stdout it cannot write.
// Planted break: Main ignoring the write's error.
func TestMainFailsOnABrokenStream(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Main(failing{}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "broken") {
		t.Errorf("a broken stdin: exit %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	if code := Main(strings.NewReader(`{"op": "describe", "protocols": [1]}`), failing{}, &stderr); code != 2 || !strings.Contains(stderr.String(), "broken") {
		t.Errorf("a broken stdout: exit %d, stderr %q", code, stderr.String())
	}
}

// failing is a stream that fails every read and write.
type failing struct{}

func (failing) Read([]byte) (int, error)  { return 0, errBroken }
func (failing) Write([]byte) (int, error) { return 0, errBroken }

var errBroken = errors.New("broken")
