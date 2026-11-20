package csv

import (
	"encoding/json"
	"testing"

	"github.com/f-klement/errata/internal/extract/core/lineend"
	"github.com/f-klement/errata/internal/plugin/wire"
)

// Whatever a file holds and whatever spec errata sends, the plugin replies
// with a reply errata reads, and every value lies inside its file: on a
// line from 1 to the file's last as errata counts lines, with its text, the
// spec's column as its key, and the request's path.
func FuzzCSV(f *testing.F) {
	f.Add([]byte(hosts), "ip", "name", "builder", true, ",")
	f.Add([]byte("\xef\xbb\xbfname;ip\r\na;\"1\r2\"\rb;3"), "ip", "", "", true, ";")
	f.Add([]byte("a\tb\n\"x\ny\"\tz\n"), "1", "2", "z", false, "\t")
	f.Add([]byte("a,\"b\nc,d\n"), "b", "a", "", true, ",")
	f.Add([]byte("x,y\n1,\xff\n"), "y", "", "", true, ",")
	f.Add([]byte("name,ip\nb,1"), "ip", "", "", true, ",") // a value on the last line, which no LF ends
	f.Fuzz(func(t *testing.T, data []byte, column, key, value string, header bool, delimiter string) {
		spec := map[string]any{"column": column, "header": header, "delimiter": delimiter}
		if key != "" {
			spec["where"] = []wire.KeyValue{{Key: key, Value: value}}
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			return
		}
		b := buildOne(t, string(raw))
		if b.Error != nil {
			return
		}
		out := respond(t, wire.ExtractRequest{Protocol: 1, Op: wire.OpExtract, Spec: b.Spec, Files: []wire.File{{Path: "f.csv", Data: data}}, Values: map[string][]wire.Value{}, MaxBytes: 32 << 20})
		var reply wire.ExtractReply
		if err := wire.Decode(out, &reply); err != nil {
			t.Fatalf("errata refuses the reply %s: %v", out, err)
		}
		last := len(lineend.Starts(data))
		for _, v := range reply.Values {
			if v.File != "f.csv" || v.Line < 1 || v.Line > last || v.Text == nil || v.Key == nil || *v.Key != column {
				t.Fatalf("the value %s lies outside f.csv of %d lines, or lacks its text or key\n%q", mustJSON(v), last, data)
			}
		}
	})
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
