package hcl

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

// extractPath builds the spec of path and sends it with files in an
// extract request, and returns the reply, read as errata reads it.
func extractPath(t tb, path string, files ...wire.File) wire.ExtractReply {
	t.Helper()
	return extractBounded(t, path, 32<<20, files...)
}

// extractBounded is extractPath with a request of max_bytes maxBytes.
func extractBounded(t tb, path string, maxBytes int64, files ...wire.File) wire.ExtractReply {
	t.Helper()
	spec, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	b := buildOne(t, string(spec))
	if b.Error != nil {
		t.Fatalf("build %s: %s", spec, *b.Error)
	}
	out := respond(t, wire.ExtractRequest{Protocol: 1, Op: wire.OpExtract, Spec: b.Spec, Files: append([]wire.File{}, files...), Values: map[string][]wire.Value{}, MaxBytes: maxBytes})
	var reply wire.ExtractReply
	if err := wire.Decode(out, &reply); err != nil {
		t.Fatalf("errata refuses the extract reply %s: %v", out, err)
	}
	return reply
}

// shown is each value of an extract reply as file:line=text, with
// " (not literal)" when it is marked so, the key as " key k" and the
// condition as " when c"; or the reply's error or no_value as error: and
// no value:.
func shown(r wire.ExtractReply) []string {
	switch {
	case r.Error != nil:
		return []string{"error: " + *r.Error}
	case r.NoValue != nil:
		return []string{"no value: " + *r.NoValue}
	}
	out := []string{}
	for _, v := range r.Values {
		s := v.File + ":" + strconv.Itoa(v.Line) + "=" + *v.Text
		if v.NotLiteral {
			s += " (not literal)"
		}
		s += " key " + *v.Key
		if v.Condition != nil {
			s += " when " + *v.Condition
		}
		out = append(out, s)
	}
	return out
}

// same fails the test unless got shows as want.
func same(t *testing.T, got wire.ExtractReply, want ...string) {
	t.Helper()
	g := shown(got)
	if strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n  %s\nwant\n  %s", strings.Join(g, "\n  "), strings.Join(want, "\n  "))
	}
}

// describe offers protocol 1 and the plugin chooses it, naming itself hcl
// at its version, with path its one field, required. Planted break: Where
// set to file, which a files entry then could not use.
func TestDescribeChoosesProtocol1(t *testing.T) {
	out := respond(t, wire.DescribeRequest{Op: wire.OpDescribe, Protocols: []int{1}})
	var d wire.DescribeReply
	if err := wire.Decode(out, &d); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	if err := d.Check(); err != nil {
		t.Fatalf("%s: errata refuses the describe: %v", out, err)
	}
	if d.Protocol != 1 || d.Name != "hcl" || d.Version != Version || d.Where != wire.WhereFiles ||
		len(d.Fields) != 1 || d.Fields[0] != (wire.Field{Name: "path", Type: wire.TypeString, Required: true}) {
		t.Fatalf("got %s", out)
	}
}

// A describe that offers no version the plugin speaks gets the versions it
// does speak, which errata names in its message. Planted break: describe
// choosing 1 whatever it was offered.
func TestDescribeOfOtherVersionsListsProtocol1(t *testing.T) {
	out := respond(t, wire.DescribeRequest{Op: wire.OpDescribe, Protocols: []int{2, 3}})
	v, err := wire.ReadVersion(out)
	if err != nil || v.Chose() || len(v.Protocols) != 1 || v.Protocols[0] != 1 || v.Name != "hcl" {
		t.Fatalf("got %s, %v", out, err)
	}
}

// A build writes the path one way, so that two entries that select one
// place are one place, and describes flat values any of which may be
// marked not literal. An index of 9 and one of nine digits are read.
// Planted breaks: written writing the path as the spec gave it; built
// describing NotLiteral false; bracket taking 9 for no digit; index
// refusing nine digits; name running to the path's end.
func TestBuildWritesThePathOneWay(t *testing.T) {
	for path, want := range map[string]string{
		`resource.aws_instance.web.ami`:             `resource.aws_instance.web.ami`,
		`resource["aws_instance"]['web'].ami`:       `resource.aws_instance.web.ami`,
		`resource.aws_s3_bucket["logs.example"].id`: `resource.aws_s3_bucket["logs.example"].id`,
		`variable["2fa"].default`:                   `variable["2fa"].default`,
		`locals.tags["a \"b\" \\"]`:                 `locals.tags["a \"b\" \\"]`,
		`ingress[*].cidr_blocks[0]`:                 `ingress[*].cidr_blocks[0]`,
		`a[9][123456789]`:                           `a[9][123456789]`,
		`["region"]`:                                `region`,
		`node-pool.size`:                            `node-pool.size`,
		`["it's"]`:                                  `["it's"]`,
	} {
		spec, _ := json.Marshal(map[string]string{"path": path})
		got := buildOne(t, string(spec))
		wantSpec, _ := json.Marshal(map[string]string{"path": want})
		if got.Error != nil || string(got.Spec) != string(wantSpec) {
			t.Errorf("%s: got %s, %v, want %s", path, got.Spec, got.Error, wantSpec)
			continue
		}
		d := got.Description
		if d.Label != "hcl" || !d.NotLiteral || d.Shape != wire.ShapeFlat || len(d.Fields) != 0 || len(d.Reads) != 0 {
			t.Errorf("%s: the description %+v", path, d)
		}
	}
}

// A spec the kind cannot read is refused with the reason, which errata
// makes a config error; a path that does not parse names the column it
// stops at. Planted breaks, each its row's: parseSpec decoding with
// json.Unmarshal, not wire.DecodeSpec; parsePath passing a control
// character, at the path's start too; name passing an empty name;
// identRune taking a digit first; index passing a leading zero; index and
// escapes reading past the path's end; quoted passing an unknown escape.
func TestBuildRefusesASpecItCannotRead(t *testing.T) {
	for spec, want := range map[string]string{
		`{"Path": "a"}`:                `the member "Path" must be written "path"`,
		`{"path": "a", "path": "b"}`:   `the member "path" is written twice`,
		`{"path": "a", "file": "x"}`:   `unknown field "file"`,
		`{}`:                           "path is required",
		`[1]`:                          "an object is required",
		`{"path": 1}`:                  "cannot unmarshal",
		`{"path": ""}`:                 `path is ""`,
		`{"path": "a\tb"}`:             `path "a\tb": column 2: a path holds no control character`,
		`{"path": "\ta"}`:              `path "\ta": column 1: a path holds no control character`,
		`{"path": "a[1"}`:              `path "a[1": column 4: ] is missing`,
		`{"path": "a[\"b\\"}`:          `path "a[\"b\\": column 5: in a quoted name a backslash is written`,
		`{"path": "a..b"}`:             `path "a..b": column 3: a name starts with a letter or _`,
		`{"path": "a."}`:               `path "a.": column 3: a name starts with a letter or _`,
		`{"path": ".a"}`:               `path ".a": column 1: a name starts with a letter or _`,
		`{"path": "2fa"}`:              `path "2fa": column 1: a name starts with a letter or _; write ["..."]`,
		`{"path": "a b"}`:              `path "a b": column 2: a step starts with . or [`,
		`{"path": "a["}`:               `path "a[": column 3: [ is not closed`,
		`{"path": "a[x]"}`:             `path "a[x]": column 3: write [*], an index`,
		`{"path": "a[*"}`:              `path "a[*": column 4: ] is missing`,
		`{"path": "a[*}"}`:             `path "a[*}": column 4: ] is missing`,
		`{"path": "a[01]"}`:            `path "a[01]": column 3: an index is 0, or up to nine digits`,
		`{"path": "a[1234567890]"}`:    `path "a[1234567890]": column 3: an index is 0`,
		`{"path": "a[\"b"}`:            `path "a[\"b": column 5: the quoted name is not closed`,
		`{"path": "a[\"b\\n\"]"}`:      `path "a[\"b\\n\"]": column 5: in a quoted name a backslash is written \\ and the quote \"`,
		`{"path": "a['b\\\"']"}`:       `column 5: in a quoted name a backslash is written \\ and the quote \'`,
		`{"path": "é.ü[\"x\"]q"}`:      `path "é.ü[\"x\"]q": column 9: a step starts with . or [`,
		`{"path": "a[\"b\"].[\"c\"]"}`: `column 8: a name starts with a letter or _`,
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
		`{"protocol": 2, "op": "build", "entries": []}`:                                                     "written in protocol 2, and errata-ext-hcl speaks 1",
		`{"protocol": 2, "op": "extract", "spec": {}, "files": [], "values": {}, "max_bytes": 1}`:           "written in protocol 2",
		`{"protocol": 1, "op": "build", "entries": [], "extra": 1}`:                                         "unknown field",
		`{"protocol": 1, "op": "extract", "spec": {"path": "a"}, "files": []}`:                              `"values" is missing`,
		`{"protocol": 1, "op": "extract", "spec": {"path": ""}, "files": [], "values": {}, "max_bytes": 1}`: `the extract request's spec: path is ""`,
		`{"op": "describe", "protocols": null}`:                                                             "null",
	} {
		var stdout, stderr bytes.Buffer
		code := Main(strings.NewReader(req), &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "errata-ext-hcl: ") || !strings.Contains(stderr.String(), want) {
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
	if err := wire.Decode(stdout.Bytes(), &d); err != nil || d.Name != "hcl" {
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
