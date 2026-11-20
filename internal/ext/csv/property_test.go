package csv

import (
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core/lineend"
)

// Every value lies inside its file, on the line errata counts for the
// start of its cell, whatever the table holds: quoted cells across lines,
// quotes, delimiters, lone CRs in a cell, which end a line for errata and
// not for encoding/csv, CRLF and LF row ends and blank lines between rows.
// The oracle counts the line of the offset the table was written at with
// lineend, as every built in reader counts lines. Each value's text is its
// cell's.
func TestPropertyEveryValueLiesOnItsCellsLine(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		width := rapid.IntRange(2, 4).Draw(t, "width")
		rows := rapid.IntRange(0, 6).Draw(t, "rows")
		col := rapid.IntRange(0, width-1).Draw(t, "column")
		var b strings.Builder
		var want []string
		for r := range rows + 1 {
			end := rapid.SampledFrom([]string{"\n", "\r\n"}).Draw(t, "end")
			if rapid.Bool().Draw(t, "blank") {
				b.WriteString(end)
			}
			for c := range width {
				if c > 0 {
					b.WriteByte(',')
				}
				text := "c" + strconv.Itoa(c)
				if r > 0 {
					text = drawCell(t)
				}
				line := lineend.Line([]byte(b.String()), b.Len())
				b.WriteString(written(text))
				if r > 0 && c == col {
					want = append(want, "t.csv:"+strconv.Itoa(line)+"="+text)
				}
			}
			b.WriteString(end)
		}
		data := b.String()
		got := extractReply(t, `{"column": "c`+strconv.Itoa(col)+`"}`, file("t.csv", data))
		last := len(lineend.Starts([]byte(data)))
		for _, v := range got.Values {
			if v.File != "t.csv" || v.Line < 1 || v.Line > last {
				t.Fatalf("the value %q is at %s:%d, outside the file of %d lines\n%q", *v.Text, v.File, v.Line, last, data)
			}
		}
		if g := cells(got); strings.Join(g, "|") != strings.Join(want, "|") {
			t.Fatalf("got %q\nwant %q\nfrom %q", g, want, data)
		}
	})
}

// drawCell is a cell's text: letters, a delimiter, a quote, a LF, a space
// and a lone CR, never one a LF follows (encoding/csv reads a CRLF in a
// quoted cell as LF) nor one at the end, where the row's LF would follow.
func drawCell(t *rapid.T) string {
	s := rapid.StringMatching(`[ab,"\n\r é]{0,8}`).Draw(t, "cell")
	for strings.Contains(s, "\r\n") {
		s = strings.ReplaceAll(s, "\r\n", "\r")
	}
	return strings.TrimRight(s, "\r")
}

// written is text as a cell of a CSV file: quoted when it holds a comma, a
// quote or a LF, else as it is, a lone CR included. An empty text is
// quoted too, since a row of one empty cell is a blank line.
func written(text string) string {
	if text == "" || strings.ContainsAny(text, ",\"\n") {
		return `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
	}
	return text
}
