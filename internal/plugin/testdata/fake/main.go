// Command fake is the plugin the plugin host's tests build and run. It
// speaks protocol 1 through package wire, and misbehaves on purpose: its
// executable's name chooses how it describes itself, and an entry's mode
// how it builds and extracts. Its kind reads lines that hold the entry's
// word.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/f-klement/errata/internal/plugin/wire"
)

// fixedName and version are what the fake describes itself as. An
// install's tests set them with -ldflags -X, to build one fake for each
// release; fixedName left empty takes the name from the executable's.
var (
	fixedName string
	version   = "0.1.0"
)

func main() {
	name := fixedName
	if name == "" {
		name = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(os.Args[0]), "errata-ext-"), ".exe")
	}
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fail(err)
	}
	var op struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(in, &op); err != nil {
		fail(err)
	}
	switch op.Op {
	case wire.OpDescribe:
		describe(name)
	case wire.OpBuild:
		build(name, in)
	case wire.OpExtract:
		extract(in)
	default:
		fail(fmt.Errorf("no op %q", op.Op))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}

func reply(v any) {
	out, err := json.Marshal(v)
	if err != nil {
		fail(err)
	}
	os.Stdout.Write(out)
}

func describe(name string) {
	switch name {
	case "newer":
		reply(map[string]any{"protocols": []int{2, 3}, "name": name, "version": version})
		return
	case "liar":
		name = "other"
	case "hang":
		time.Sleep(time.Minute)
	case "broken":
		os.Stdout.WriteString(`{"protocol": 1, "name": "broken", "version": "` + version + `", "where": "files", "fields": [], "extra": 1}`)
		return
	}
	reply(wire.DescribeReply{Protocol: 1, Name: name, Version: version, Where: wire.WhereFiles, Fields: []wire.Field{
		{Name: "word", Type: wire.TypeString, Required: true},
		{Name: "mode", Type: wire.TypeString},
		{Name: "tags", Type: wire.TypeMap},
		{Name: "limit", Type: wire.TypeInt},
		{Name: "loud", Type: wire.TypeBool},
		{Name: "names", Type: wire.TypeStrings},
	}})
}

// spec is an entry of the fake kind as a request carries it.
type spec struct {
	Word  string          `json:"word"`
	Mode  string          `json:"mode,omitempty"`
	Tags  []wire.KeyValue `json:"tags,omitempty"`
	Limit int64           `json:"limit,omitempty"`
	Loud  bool            `json:"loud"`
	Names []string        `json:"names,omitempty"`
}

func build(name string, in []byte) {
	var req wire.BuildRequest
	if err := wire.Decode(in, &req); err != nil {
		fail(err)
	}
	if name == "short" {
		reply(wire.BuildReply{Built: []wire.Built{}})
		return
	}
	// The requests the fake has seen, one line each, for a test that counts
	// them.
	if log := buildLog(req); log != "" {
		f, _ := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintf(f, "%d\n", len(req.Entries))
		f.Close()
	}
	out := wire.BuildReply{Built: make([]wire.Built, len(req.Entries))}
	for i, e := range req.Entries {
		var s spec
		if err := wire.DecodeSpec(e.Spec, &s); err != nil {
			fail(err)
		}
		out.Built[i] = builtOf(s, e.Rule)
	}
	reply(out)
}

// buildLog is the file a build request's entry names in a tag "log", or "".
func buildLog(req wire.BuildRequest) string {
	for _, e := range req.Entries {
		var s spec
		_ = json.Unmarshal(e.Spec, &s)
		for _, t := range s.Tags {
			if t.Key == "log" {
				return t.Value
			}
		}
	}
	return ""
}

func builtOf(s spec, rule string) wire.Built {
	if s.Mode == "refuse" {
		msg := "the word " + s.Word + " is refused\nin rule " + rule
		return wire.Built{Error: &msg}
	}
	d := wire.Description{Label: "fake", Shape: wire.ShapeFlat, Fields: []string{}, Reads: []string{}}
	switch s.Mode {
	case "computed":
		d.NotLiteral = true
	case "reads":
		d.Reads = []string{"other"}
	case "record", "dupkey":
		d.Shape, d.Fields = wire.ShapeRecord, []string{"word"}
	case "badshape":
		d.Shape = "tree"
	case "reorder":
		return wire.Built{Spec: json.RawMessage(`{"loud": false, "tags": [{"key": "z", "value": "1"}, {"key": "a", "value": "2"}], "mode": "reorder", "word": "w"}`), Description: &d}
	case "dupspec":
		return wire.Built{Spec: json.RawMessage(`{"word": "a", "word": "b", "loud": false}`), Description: &d}
	case "strint":
		return wire.Built{Spec: json.RawMessage(`{"word": "a", "limit": "5", "loud": false}`), Description: &d}
	}
	raw, _ := json.Marshal(s) // writes loud out, false by default
	return wire.Built{Spec: raw, Description: &d}
}

func extract(in []byte) {
	var req wire.ExtractRequest
	if err := wire.Decode(in, &req); err != nil {
		fail(err)
	}
	var s spec
	if err := wire.DecodeSpec(req.Spec, &s); err != nil {
		fail(err)
	}
	first := req.Files[0].Path
	switch s.Mode {
	case "env":
		reply(wire.ExtractReply{Values: texts(first, os.Environ())})
	case "cwd":
		entries, _ := os.ReadDir(".")
		wd, _ := os.Getwd()
		reply(wire.ExtractReply{Values: texts(first, []string{strconv.Itoa(len(entries)), wd})})
	case "files":
		var paths []string
		for _, f := range req.Files {
			paths = append(paths, f.Path+"="+string(f.Data))
		}
		reply(wire.ExtractReply{Values: texts(first, paths)})
	case "sleep":
		time.Sleep(time.Minute)
	case "stderr":
		os.Stderr.Write(bytes.Repeat([]byte("e"), 2<<20))
		time.Sleep(time.Minute)
	case "stdout":
		os.Stdout.Write(bytes.Repeat([]byte(" "), int(s.Limit)))
		time.Sleep(time.Minute)
	case "exit":
		os.Stderr.WriteString("first line\nboom\n")
		os.Exit(3)
	case "error":
		msg := "cannot read\nthis"
		reply(wire.ExtractReply{Error: &msg})
	case "novalue":
		msg := "nothing here"
		reply(wire.ExtractReply{NoValue: &msg})
	case "dupkey":
		a, b := "a", "b"
		reply(wire.ExtractReply{Values: []wire.Value{{File: first, Line: 1, Data: &wire.Node{Line: 1, Map: []wire.Pair{
			{Key: "word", Value: wire.Node{Line: 1, Scalar: &a}},
			{Key: "word", Value: wire.Node{Line: 1, Scalar: &b}},
		}}}}})
	case "raw":
		// The reply as names[0] writes it, and names[1], if any, on stderr.
		os.Stdout.WriteString(s.Names[0])
		if len(s.Names) > 1 {
			os.Stderr.WriteString(s.Names[1])
		}
	case "reads":
		var out []wire.Value
		for _, v := range req.Values["other"] {
			out = append(out, wire.Value{Text: v.Text, File: v.File, Line: v.Line})
		}
		reply(wire.ExtractReply{Values: out})
	default:
		reply(wire.ExtractReply{Values: lines(s, req.Files)})
	}
}

// texts are values of each of ts in file, on no line.
func texts(file string, ts []string) []wire.Value {
	out := []wire.Value{}
	for _, t := range ts {
		out = append(out, wire.Value{Text: &t, File: file})
	}
	return out
}

// lines are the lines of files that hold the word, each the text after
// it. The mode bends one value the way a test asks.
func lines(s spec, files []wire.File) []wire.Value {
	out := []wire.Value{}
	for _, f := range files {
		for i, l := range strings.Split(string(f.Data), "\n") {
			_, after, ok := strings.Cut(l, s.Word)
			if !ok {
				continue
			}
			text := strings.TrimSpace(after)
			v := wire.Value{Text: &text, File: f.Path, Line: i + 1}
			for _, t := range s.Tags {
				switch t.Key {
				case "key", "condition":
					val := strings.Repeat(t.Value, int(max(s.Limit, 1)))
					if t.Key == "key" {
						v.Key = &val
					} else {
						v.Condition = &val
					}
				}
			}
			out = append(out, bend(s.Mode, v))
		}
	}
	return out
}

func bend(mode string, v wire.Value) wire.Value {
	switch mode {
	case "computed", "notliteral":
		v.NotLiteral = true
	case "badfile":
		v.File = "elsewhere"
	case "badline":
		v.Line = 9999
	case "record":
		v.Data = &wire.Node{Line: v.Line, Map: []wire.Pair{{Key: "word", Value: wire.Node{Line: v.Line, Scalar: v.Text}}}}
		v.Text = nil
	}
	return v
}
