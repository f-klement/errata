package hcl

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core/lineend"
	"github.com/f-klement/errata/internal/plugin/wire"
)

// Whatever a file holds and whatever path errata sends, the plugin replies
// with a reply errata reads, and every value lies inside its file: on a
// line from 1 to the file's last as errata counts lines, with its text, a
// key and the request's path. An error names the file.
func FuzzHCL(f *testing.F) {
	f.Add([]byte(mainTF), "resource.aws_instance[*].instance_type")
	f.Add([]byte(mainTF), "resource[*][*][*]")
	f.Add([]byte("# a\rb\nx = [\r\n  1,\r\n  {k = var.v}]\r\n"), "x[*][*]")
	f.Add([]byte("/* a\rb */ r = <<-EOT\n  x\n  EOT\n"), "r")
	f.Add([]byte("t = {(var.k) = 1, \"a.b\" = \"${x}\"}\n"), "t[*]")
	f.Add([]byte("b {\n  c \"l\" { d = -1 }\n}\nb { e = 2 }\n"), "b[*]")
	f.Add([]byte("a = 1\na = 2\n"), "a")
	f.Add([]byte("a = \"x"), "a")        // a string never closed, on the last line, which no LF ends
	f.Add([]byte("a = 1\r\nb = 2"), "b") // a value on the last line, which no line end closes
	f.Fuzz(func(t *testing.T, data []byte, path string) {
		raw, err := json.Marshal(map[string]string{"path": path})
		if err != nil {
			return
		}
		b := buildOne(t, string(raw))
		if b.Error != nil {
			return
		}
		out := respond(t, wire.ExtractRequest{Protocol: 1, Op: wire.OpExtract, Spec: b.Spec, Files: []wire.File{{Path: "f.tf", Data: data}}, Values: map[string][]wire.Value{}, MaxBytes: 32 << 20})
		var reply wire.ExtractReply
		if err := wire.Decode(out, &reply); err != nil {
			t.Fatalf("errata refuses the reply %s: %v", out, err)
		}
		if reply.Error != nil && !strings.HasPrefix(*reply.Error, "f.tf:") {
			t.Fatalf("the error %q names no file\n%q", *reply.Error, data)
		}
		last := len(lineend.Starts(data))
		for _, v := range reply.Values {
			if v.File != "f.tf" || v.Line < 1 || v.Line > last || v.Text == nil || v.Key == nil {
				t.Fatalf("the value %s lies outside f.tf of %d lines, or lacks its text or key\n%q", mustJSON(v), last, data)
			}
		}
	})
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
