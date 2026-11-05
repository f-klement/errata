package marker

import (
	"math"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
)

// fillers are lines a generated file holds around its marker, by the file
// they are written in: none opens a block that would hold the line after it
// as code.
var fillers = map[string][]string{
	"a.md": {"", "# Heading", "Some prose.", "- an item", "> a quote", "<!-- a note -->", "errata names `<!-- errata: x -->` in a span"},
	"a.go": {"", "x := 1", "// a note", "/* a\n * block */", `s := "<!-- errata: x -->"`},
}

// markerLine is the lines that hold a marker of r whose value is words, in
// a file of the name file: with wrap a line break between two words, and
// with next one between the opener and the value.
func markerLine(file string, words []string, wrap, next bool) string {
	sep, lead := " ", ""
	br := "\n"
	if file == "a.go" {
		br = "\n// "
	}
	if wrap {
		sep = br
	}
	if next {
		lead = br
	}
	value := lead + strings.Join(words, sep)
	if file == "a.go" {
		return "// on <!-- errata: r -->" + value + "<!-- /errata --> here"
	}
	return "on <!-- errata: r -->" + value + "<!-- /errata --> here"
}

// The value of a marker is its words on one line, at the line where it
// starts, wherever in the file the marker stands, whether or not a line
// wrap splits it, and whether it starts on the opener's line or the next. Planted breaks: found taking the opener's line for the
// value's; scanCode mapping a text offset to the first comment's.
func TestValueLine(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		file := rapid.SampledFrom([]string{"a.md", "a.go"}).Draw(t, "file")
		before := rapid.SliceOfN(rapid.SampledFrom(fillers[file]), 0, 8).Draw(t, "before")
		after := rapid.SliceOfN(rapid.SampledFrom(fillers[file]), 0, 8).Draw(t, "after")
		words := rapid.SliceOfN(rapid.StringMatching(`[a-z0-9.]{1,6}`), 1, 3).Draw(t, "words")
		wrap := len(words) > 1 && rapid.Bool().Draw(t, "wrap")
		next := rapid.Bool().Draw(t, "next")
		// A blank line before the marker keeps it out of a quote or a
		// list item, whose block would otherwise hold it.
		parts := append(append(append([]string{}, before...), "", markerLine(file, words, wrap, next)), append([]string{""}, after...)...)
		src := strings.Join(parts, "\n") + "\n"
		line := strings.Count(strings.Join(append(append([]string{}, before...), ""), "\n"), "\n") + 2
		found, err := Scan(file, []byte(src), 1<<20)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if len(found) != 1 {
			t.Fatalf("%q: found %+v, want one marker", src, found)
		}
		f := found[0]
		at := line
		if next {
			at++
		}
		if f.Rule != "r" || f.Value.Text != strings.Join(words, " ") || f.Value.At.Line != at || f.Open.Line != line {
			t.Fatalf("%q: found %s=%q at %d, opening at %d; want r=%q at %d, opening at %d", src, f.Rule, f.Value.Text, f.Value.At.Line, f.Open.Line, strings.Join(words, " "), at, line)
		}
	})
}

// Over any bytes, as any file the walk reads, Scan either refuses with the
// kind's name or finds markers whose values are one line, each starting
// inside the file at or after the line its marker opens on.
func FuzzScan(f *testing.F) {
	files := []string{"a.md", "a.go", "a.html", "a.py", "a.txt"}
	for _, s := range []string{
		"", "<!-- errata: a -->1<!-- /errata -->\n", "// <!-- errata: a -->1\n// 2<!-- /errata -->\n",
		"<!-- errata: a -->\n8080\n<!-- /errata -->\n", "x <!-- errata: a -->1\n\n2<!-- /errata -->",
		"/* <!-- errata: a -->\n * 1 <!-- /errata --> */", "<p><!-- errata: a -->1<!-- /errata --></p>",
		"a\r\r<!-- errata: a -->1<!-- /errata -->\r", "// a\r\r// <!-- errata: a -->1<!-- /errata -->\r", // lone CRs end lines
	} {
		for i := range files {
			f.Add(uint8(i), s)
		}
	}
	f.Fuzz(func(t *testing.T, which uint8, src string) {
		file := files[int(which)%len(files)]
		found, err := Scan(file, []byte(src), 1<<20)
		if err != nil {
			if !strings.HasPrefix(err.Error(), "marker: ") {
				t.Fatalf("%s %q: refused without the kind's name: %v", file, src, err)
			}
			return
		}
		lines := core.LastLine([]byte(src))
		for _, m := range found {
			v := m.Value
			if v.At.Line < m.Open.Line || v.At.Line > lines || m.Open.Line < 1 || strings.ContainsAny(v.Text, "\n\r\t") {
				t.Fatalf("%s %q: %s=%q at %d, opening at %d, of %d lines", file, src, m.Rule, v.Text, v.At.Line, m.Open.Line, lines)
			}
		}
	})
}

// scanTime is the least time of five runs of reps scans of file holding
// data.
func scanTime(file string, data []byte, reps int) time.Duration {
	best := time.Duration(math.MaxInt64)
	for range 5 {
		start := time.Now()
		for range reps {
			_, _ = Scan(file, data, 1<<30)
		}
		best = min(best, time.Since(start))
	}
	return best
}

// A scan is linear in its file: n opening <!-- that are no marker's, then
// sixteen blanks for each, scan eight times as long at 8n, in a file no
// reader reads and in a block comment. A pass that reads the blanks again
// for each <!-- takes sixty-four times as long. The limit, 20, sits far from
// both, since a run under coverage on a shared node read 8.4 for 4n once
// (pipeline 3029471786), and each size is timed for at least 20 ms. Planted
// break: markerish trimming the blanks at the end of the rest, as it did,
// which took 2.17 s for 16384 of them on the builder node.
func TestScanIsLinear(t *testing.T) {
	shape := func(file string, n int) []byte {
		body := strings.Repeat("errata <!--", n) + strings.Repeat(" ", 16*n)
		if file == "a.go" {
			body = "/* " + body + " */\n"
		}
		return []byte(body)
	}
	for _, file := range []string{"a.txt", "a.go"} {
		const n = 2000
		reps := 1
		for scanTime(file, shape(file, n), reps) < 20*time.Millisecond && reps < 1<<16 {
			reps *= 2
		}
		small, large := scanTime(file, shape(file, n), reps), scanTime(file, shape(file, 8*n), reps)
		if ratio := float64(large) / float64(small); ratio > 20 {
			t.Errorf("%s: %d and %d openers took %v and %v, %.1f times; a linear scan takes about 8", file, n, 8*n, small, large, ratio)
		}
	}
}
