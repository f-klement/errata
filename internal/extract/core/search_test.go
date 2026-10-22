package core

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The regex extractor finds exactly the matches FindAllSubmatchIndex finds,
// though it runs each search itself through a clocked reader, and every value it returns is text of
// the file, with CRLF read as LF, at a line inside it.
func FuzzRegex(f *testing.F) {
	f.Add(`^FROM (\S+)`, dockerfile)
	f.Add(`(x*)$`, "a\n\nb\n")
	f.Add(`^v=(.*)$`, "v=1\r\nv=2\r\n")
	f.Add(`\b(\w)`, "ab cd\xffé e")
	f.Add(`(?-m)^(a)|(b)`, "aab\nab")
	f.Add(`(\Aa|b)`, "abab")
	f.Add(`()`, "é\xff\n")
	f.Add(`(a)\Q)(`, "a)(a)(")
	f.Fuzz(func(t *testing.T, pattern, data string) {
		r, err := CompileSearch("regex", "f", pattern, 0)
		if err != nil {
			return
		}
		lf := strings.ReplaceAll(data, "\r\n", "\n")
		got, err := r.Matches([]byte(lf))
		want := r.re.FindAllSubmatchIndex([]byte(lf), MaxValues+1)
		if err != nil {
			if len(want) <= MaxValues || !strings.Contains(err.Error(), "values, the limit") {
				t.Fatalf("error %v with %d matches", err, len(want))
			}
			return
		}
		if len(got) != len(want) {
			t.Fatalf("matches %v, FindAll %v", got, want)
		}
		for i, m := range want {
			if got[i] != Match(m[:4]) {
				t.Fatalf("match %d is %v, FindAll's %v", i, got[i], m[:4])
			}
		}
		values, err := r.FromData([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		count := strings.Count(data, "\n") + 1
		for _, v := range values {
			if v.At.Line < 1 || v.At.Line > count || !strings.Contains(lf, v.Text) {
				t.Fatalf("value %q at line %d of %d", v.Text, v.At.Line, count)
			}
		}
	})
}

const dockerfile = `FROM golang:1.27 AS build
RUN make
FROM alpine:3.22
# FROM scratch
LABEL version="0.2.0"
`

// The regex search counts lines with a cursor that only moves forward, so a
// file of newlines costs no memory for each one: the planted break is the
// index of every newline, 8 bytes each. It allocates nothing of the file's
// size, since the file has no CRLF to copy away.
// TestJSONLinesCostNoMemoryForEachNewline is the same for JSON.
func TestRegexLinesCostNoMemoryForEachNewline(t *testing.T) {
	data := []byte("1" + strings.Repeat("\n", 1<<18) + "x")
	e, err := CompileSearch("regex", "f", "(x)", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []check.Value
	n := extracttest.Allocated(func() { got, err = e.FromData(data) })
	if err != nil || extracttest.Texts(got) != "x@262145" {
		t.Errorf("regex: %s, %v", extracttest.Texts(got), err)
	}
	if bound := uint64(len(data)); n > bound {
		t.Errorf("%d bytes allocated for a file of %d, over %d", n, len(data), bound)
	}
}
