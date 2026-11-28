// Package hcl is the HCL extractor plugin, errata-ext-hcl. It provides one
// kind, hcl, which reads an attribute of a file in HashiCorp's configuration
// language, such as Terraform's .tf and .tfvars, Packer's and Nomad's
// files, by block type, labels and attribute name. It speaks plugin
// protocol 1 (docs/plugin-protocol.md) through package wire, one request a
// run.
//
// It parses with hashicorp/hcl/v2's hclsyntax and evaluates nothing: a
// value the file computes, such as a reference, a function call or an
// interpolation, is returned as the file wrote it and marked not literal,
// so no variable, provider or module is ever read or fetched.
//
// It reads only the bytes a request carries. It opens no file, starts no
// program and uses no network: the depguard rule plugin-is-offline in
// .golangci.yml holds the package to an allow list of imports that do none
// of these, and a test of cmd/errata-ext-hcl holds every package the
// plugin links, hcl/v2's own among them, to the same.
package hcl

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/f-klement/errata/internal/plugin/wire"
)

// Name is the plugin's name, the key of its kind.
const Name = "hcl"

// Version is the plugin's release as describe writes it, without a v. The
// release sets it with -ldflags "-X <this package>.Version=0.1.0", from the
// tag errata-ext-hcl/v0.1.0 (ADR 0021); a build from source is a
// development build.
var Version = "0.0.0-dev"

// fields are the keys an entry of the kind takes besides errata's.
var fields = []wire.Field{
	{Name: "path", Type: wire.TypeString, Required: true},
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
		fmt.Fprintln(stderr, "errata-ext-hcl:", err)
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
		return json.Marshal(wire.Version{Protocols: []int{wire.Protocol}, Name: Name, Version: Version})
	}
	return json.Marshal(wire.DescribeReply{Protocol: wire.Protocol, Name: Name, Version: Version, Where: wire.WhereFiles, Fields: fields})
}

// checkProtocol refuses a request written in a version other than 1.
func checkProtocol(version int) error {
	if version != wire.Protocol {
		return fmt.Errorf("the request is written in protocol %d, and errata-ext-hcl speaks %d", version, wire.Protocol)
	}
	return nil
}

// build replies to a build request with one result for each entry, in
// order: its spec with its path written one way and its description, or
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

// built is the result of one entry's spec. Every entry may return a value
// marked not literal, so its description says so.
func built(raw json.RawMessage) wire.Built {
	s, err := parseSpec(raw)
	if err != nil {
		msg := err.Error()
		return wire.Built{Error: &msg}
	}
	d := wire.Description{Label: Name, NotLiteral: true, Shape: wire.ShapeFlat, Fields: []string{}, Reads: []string{}}
	return wire.Built{Spec: s.written(), Description: &d}
}

// deadline is how long the plugin reads one extract request's files
// before it refuses them: below wire.ExtractTimeout, errata's 30 s for an
// extract, so the plugin still answers with an error that names the file
// before errata kills it. Tests set it shorter.
var deadline = 20 * time.Second

// extract replies to an extract request with the values the spec's path
// selects in each file, in the files' order and each file's order; with an
// error when a file does not parse, when the path selects what has no
// value, or when the files are not read within deadline; and with no
// value, saying so, when the path selects nothing.
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
	expired := time.NewTimer(deadline)
	defer expired.Stop()
	for _, f := range req.Files {
		room := wire.MaxValues - len(values)
		vs, err := within(expired.C, f.Path, func() ([]wire.Value, error) { return s.read(f, room, req.MaxBytes) })
		if err != nil {
			msg := err.Error()
			return json.Marshal(wire.ExtractReply{Error: &msg})
		}
		values = append(values, vs...)
	}
	if len(values) == 0 && len(req.Files) > 0 {
		msg := s.nothing(req.Files)
		return json.Marshal(wire.ExtractReply{NoValue: &msg})
	}
	return json.Marshal(wire.ExtractReply{Values: values})
}

// within is what read returns, or an error naming path and deadline when
// expired fires first. read runs in a goroutine that hands its result to
// a buffered channel, so it ends when read does, waited on or not, and
// never keeps the process alive: the plugin exits once Main returns.
func within(expired <-chan time.Time, path string, read func() ([]wire.Value, error)) ([]wire.Value, error) {
	type result struct {
		values []wire.Value
		err    error
	}
	done := make(chan result, 1)
	go func() {
		vs, err := read()
		done <- result{vs, err}
	}()
	select {
	case r := <-done:
		return r.values, r.err
	case <-expired:
		return nil, fmt.Errorf("%s: not read within %s, the plugin's own deadline, which it keeps below errata's 30 s so it can still answer; parsing takes longer on a slower or busier machine, so a file near this deadline can pass it on one machine and not on another", path, deadline)
	}
}
