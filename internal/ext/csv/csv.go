// Package csv is the CSV extractor plugin, errata-ext-csv. It provides one
// kind, csv, which reads a cell of a CSV or TSV file by its column and by
// the other cells of its row, and speaks plugin protocol 1
// (docs/plugin-protocol.md) through package wire, one request a run.
//
// It reads only the bytes a request carries. It opens no file, starts no
// program and uses no network: the depguard rule plugin-is-offline in
// .golangci.yml holds the package to an allow list of imports that do none
// of these.
package csv

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/f-klement/errata/internal/plugin/wire"
)

// Name is the plugin's name, the key of its kind.
const Name = "csv"

// Version is the plugin's release as describe writes it, without a v. The
// release sets it with -ldflags "-X <this package>.Version=0.1.0", from the
// tag errata-ext-csv/v0.1.0 (ADR 0021); a build from source is a
// development build.
var Version = "0.0.0-dev"

// fields are the keys an entry of the kind takes besides errata's, in the
// order errata list prints them.
var fields = []wire.Field{
	{Name: "column", Type: wire.TypeString, Required: true},
	{Name: "where", Type: wire.TypeMap},
	{Name: "header", Type: wire.TypeBool},
	{Name: "dialect", Type: wire.TypeString},
	{Name: "delimiter", Type: wire.TypeString},
}

// Main answers the one request in stdin on stdout, as the plugin's main
// does, and returns the status to exit with: 0 with a reply written, 2 with
// a request it cannot answer, whose reason goes to stderr.
func Main(stdin io.Reader, stdout, stderr io.Writer) int {
	request, err := io.ReadAll(stdin)
	if err == nil {
		var reply []byte
		if reply, err = Respond(request); err == nil {
			_, err = stdout.Write(reply)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "errata-ext-csv:", err)
		return 2
	}
	return 0
}

// Respond reads one request and returns the reply to write. An error is a
// request the plugin cannot answer, such as one that breaks the protocol;
// a file the plugin cannot parse is a reply, which makes the rule unknown.
func Respond(request []byte) ([]byte, error) {
	var head struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(request, &head); err != nil {
		return nil, fmt.Errorf("the request is no JSON object: %w", err)
	}
	switch head.Op {
	case wire.OpDescribe:
		return describe(request)
	case wire.OpBuild:
		return build(request)
	case wire.OpExtract:
		return extract(request)
	}
	return nil, fmt.Errorf("the request's op is %s, none of describe, build and extract", strconv.Quote(head.Op))
}

// describe replies to a describe request: protocol 1 when errata offers it,
// else the versions the plugin speaks.
func describe(request []byte) ([]byte, error) {
	var req wire.DescribeRequest
	if err := wire.Decode(request, &req); err != nil {
		return nil, err
	}
	if !slices.Contains(req.Protocols, wire.Protocol) {
		return json.Marshal(wire.Version{Protocols: wire.Protocols, Name: Name, Version: Version})
	}
	return json.Marshal(wire.DescribeReply{Protocol: wire.Protocol, Name: Name, Version: Version, Where: wire.WhereFiles, Fields: fields})
}

// checkProtocol refuses a request written in a version other than 1.
func checkProtocol(version int) error {
	if version != wire.Protocol {
		return fmt.Errorf("the request is written in protocol %d, and errata-ext-csv speaks %d", version, wire.Protocol)
	}
	return nil
}

// build replies to a build request with one result for each entry, in
// order: its spec with every default written out and its description, or
// why the spec is refused.
func build(request []byte) ([]byte, error) {
	var req wire.BuildRequest
	if err := wire.Decode(request, &req); err != nil {
		return nil, err
	}
	if err := checkProtocol(req.Protocol); err != nil {
		return nil, err
	}
	out := wire.BuildReply{Built: make([]wire.Built, len(req.Entries))}
	for i, e := range req.Entries {
		out.Built[i] = built(e.Spec)
	}
	return json.Marshal(out)
}

// built is the result of one entry's spec.
func built(raw json.RawMessage) wire.Built {
	s, err := parseSpec(raw)
	if err != nil {
		msg := err.Error()
		return wire.Built{Error: &msg}
	}
	d := wire.Description{Label: s.label(), Shape: wire.ShapeFlat, Fields: []string{}, Reads: []string{}}
	return wire.Built{Spec: s.written(), Description: &d}
}

// extract replies to an extract request with the cells the spec names in
// each file, in the files' order; with an error when a file does not
// parse; and with no value, saying why, when the spec's where matches no
// row.
func extract(request []byte) ([]byte, error) {
	var req wire.ExtractRequest
	if err := wire.Decode(request, &req); err != nil {
		return nil, err
	}
	if err := checkProtocol(req.Protocol); err != nil {
		return nil, err
	}
	s, err := parseSpec(req.Spec)
	if err != nil {
		return nil, fmt.Errorf("the extract request's spec: %w", err)
	}
	values := []wire.Value{}
	for _, f := range req.Files {
		vs, err := s.read(f, wire.MaxValues-len(values))
		if err != nil {
			msg := err.Error()
			return json.Marshal(wire.ExtractReply{Error: &msg})
		}
		values = append(values, vs...)
	}
	if len(values) == 0 && len(s.where) > 0 {
		msg := s.noRow(req.Files)
		return json.Marshal(wire.ExtractReply{NoValue: &msg})
	}
	return json.Marshal(wire.ExtractReply{Values: values})
}
