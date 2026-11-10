package table

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Every dialect's reader reads any file, as a template or not, into rows
// of one cell list for each column, the rows in file order, each cell at a
// line inside the file. A cell holds no line end, and its text is a part
// of its line as the tool decodes it, or its column's default. A read
// stops on no file but one past its work budget, or an exports file
// nfs-utils stops reading, at the line it names.
func FuzzTable(f *testing.F) {
	f.Add("UUID=a / ext4 rw,\"x,y\" 0\n# c\n/dev/b /srv\r\n", uint8(0), false)
	f.Add("root:x:0:0::/root:/bin/sh\n # c\n+\n", uint8(1), false)
	f.Add("wheel:x:10:a,,b\n", uint8(2), true)
	f.Add("10.0.0.1 a B # c\r1.2.3.4\n", uint8(3), false)
	f.Add("/srv -ro a(rw) \\\n @n (x)\n\"/a b\"\n/c \\", uint8(4), false)
	f.Add("/s\\040\"x y\"#z -a -b h(\\054) #c \\\n/t\\377 \\ \n", uint8(4), false)
	f.Add("/a /b\\040c t o\\054p 0 x\n/d /e f\n", uint8(0), false)
	f.Add("x::0:0\ng:x:1: a,\tb\n", uint8(2), false)
	f.Add("a:x:+00:-0\nb:x:1x:0\n", uint8(1), false)
	f.Add("/a /b c d 0 99999999999999999999\n/e /f g h 1x 2\n", uint8(0), false)
	f.Add("\"/a \\\n  b\" h(ro) -rw\n/c -ro -sync d\n", uint8(4), false)
	f.Add("/a b(rw\n", uint8(4), false)
	f.Add("c d - -\ne f none none\n", uint8(5), false)
	f.Add("c UUID=x\n# d\nx y none a,b\n", uint8(5), false)
	f.Add("{{ a }} {% b %} x y\n{% if c %}\n/d /e f g\n{% endif %}\n", uint8(0), true)
	f.Fuzz(func(t *testing.T, data string, which uint8, tmpl bool) {
		d := dialects[int(which)%len(dialects)]
		file := d.name
		if tmpl {
			file += ".j2"
		}
		raw := []byte(data)
		last := core.LastLine(raw)
		m := core.NewMeter(len(raw))
		mk, err := newMarker(file, raw, m)
		if err != nil {
			if errors.Is(err, core.ErrWork) {
				return
			}
			t.Fatal(err)
		}
		prev := 0
		err = d.read(raw, m, mk, func(r row) error {
			prev = checkFuzzRow(t, d, raw, last, prev, r)
			return nil
		})
		if err != nil && !errors.Is(err, core.ErrWork) && !refused(d, err) {
			t.Fatalf("%s: %v", d.name, err)
		}
	})
}

// checkFuzzRow holds r, a row of raw read in d after a row whose first
// cell is at line prev, to FuzzTable's rules, and is the line of its first
// cell.
func checkFuzzRow(t *testing.T, d *dialect, raw []byte, last, prev int, r row) int {
	t.Helper()
	if len(r) != len(d.columns) || len(r[0]) != 1 {
		t.Fatalf("%s: a row of %d columns, its first of %d cells", d.name, len(r), len(r[0]))
	}
	if r[0][0].line < prev {
		t.Fatalf("%s: a row at line %d after one at %d", d.name, r[0][0].line, prev)
	}
	for i, cells := range r {
		for _, c := range cells {
			checkFuzzCell(t, d, d.columns[i], raw, last, c)
		}
	}
	return r[0][0].line
}

// checkFuzzCell holds c, a cell of column col, to FuzzTable's rules.
func checkFuzzCell(t *testing.T, d *dialect, col column, raw []byte, last int, c cell) {
	t.Helper()
	if c.line < 1 || c.line > last {
		t.Fatalf("%s: a cell at line %d in a file of %d lines", d.name, c.line, last)
	}
	if bytes.ContainsAny(c.text, "\r\n") && d != fstabDialect && d != exportsDialect {
		t.Fatalf("%s: the cell %q at line %d holds a line end", d.name, c.text, c.line)
	}
	if line := extracttest.RawLine(raw, c.line); string(c.text) != col.def && !holds(d, col, raw, c) {
		t.Fatalf("%s: line %d is %q, which does not hold the %s cell %q", d.name, c.line, line, col.name, c.text)
	}
}

// refused reports an error of exports' that names the line nfs-utils
// stops reading the file at, the only error but ErrWork a read returns.
func refused(d *dialect, err error) bool {
	return d == exportsDialect && strings.HasPrefix(err.Error(), "line ")
}

// numbers are the columns whose value is the number the tool reads, not
// the text the file writes.
var numbers = []string{"freq", "passno", "uid", "gid"}

// holds reports whether raw holds c, a cell of col, as d reads it: a
// number is one, unless a template's engine writes it, when it stays as
// written; fstab decodes its escapes, which hold no white space, so
// a field's decoded text is a part of its decoded line; exports drops
// quotes and decodes escapes inside a word, so each part of a cell between
// white space is a part of the decoded rest of the file from its line, and
// the first a part of its decoded line unless a \ joins the next line to
// it: a quote a join leaves open runs on into the next line, as one space,
// so a word of only a quote, "\ then bd", starts with the space and has
// its first part on the line after.
func holds(d *dialect, col column, raw []byte, c cell) bool {
	line := extracttest.RawLine(raw, c.line)
	switch {
	case slices.Contains(numbers, col.name) && !c.varies:
		_, err := strconv.Atoi(string(c.text))
		return err == nil
	case d == fstabDialect:
		return bytes.Contains(unmangle([]byte(line)), c.text)
	case d == exportsDialect:
		return inExports(raw, c, line)
	}
	return strings.Contains(line, string(c.text))
}

// inExports reports whether the parts of c, an exports cell, are in raw
// from c's line, line, on, as holds says.
func inExports(raw []byte, c cell, line string) bool {
	rest := xdecode(raw[lineStart(raw, c.line):])
	parts := bytes.Fields(c.text)
	for i, p := range parts {
		if i == 0 && !joins(line) && !bytes.Contains(xdecode([]byte(line)), p) || !bytes.Contains(rest, p) {
			return false
		}
	}
	return true
}

// joins reports a line a \ right before its end joins to the next.
func joins(line string) bool { return strings.HasSuffix(line, `\`) }

// lineStart is the offset of line n of data.
func lineStart(data []byte, n int) int {
	off := 0
	for i := 1; i < n; i++ {
		off = core.NextLine(data, off)
	}
	return off
}
