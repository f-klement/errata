//go:build ignore

// gen.go writes what Go's unicode tables say of every code point, for
// gen.mjs to compare with Node's (see unicode_node.go). Run from this
// directory, under the Node of the Renovate image errata follows (ADR 0014):
//
//	go run gen.go > go.txt && node gen.mjs < go.txt > ../../unicode_node.go && gofmt -w ../../unicode_node.go
package main

import (
	"bufio"
	"fmt"
	"os"
	"unicode"
	"unicode/utf16"
)

// props are the general categories minimatch's POSIX classes name, in the
// order gen.mjs reads them.
var props = []struct {
	name string
	is   func(rune) bool
}{
	{"L", func(r rune) bool { return unicode.Is(unicode.L, r) }},
	{"Nl", func(r rune) bool { return unicode.Is(unicode.Nl, r) }},
	{"Nd", func(r rune) bool { return unicode.Is(unicode.Nd, r) }},
	{"Zs", func(r rune) bool { return unicode.Is(unicode.Zs, r) }},
	{"Cc", func(r rune) bool { return unicode.Is(unicode.Cc, r) }},
	{"Z", func(r rune) bool { return unicode.Is(unicode.Z, r) }},
	{"C", func(r rune) bool { return unicode.Is(unicode.C, r) || !assigned(r) }},
	{"Ll", func(r rune) bool { return unicode.Is(unicode.Ll, r) }},
	{"P", func(r rune) bool { return unicode.Is(unicode.P, r) }},
	{"Lu", func(r rune) bool { return unicode.Is(unicode.Lu, r) }},
	{"Pc", func(r rune) bool { return unicode.Is(unicode.Pc, r) }},
}

func assigned(r rune) bool {
	for _, t := range []*unicode.RangeTable{unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.C} {
		if unicode.Is(t, r) {
			return true
		}
	}
	return false
}

func hex(r rune) string {
	s := ""
	for _, u := range utf16.Encode([]rune{r}) {
		s += fmt.Sprintf("%04x", u)
	}
	return s
}

func main() {
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		m := r
		for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
			m = min(m, x)
		}
		bits := ""
		for _, p := range props {
			if p.is(r) {
				bits += "1"
			} else {
				bits += "0"
			}
		}
		fmt.Fprintf(w, "%x %s %s %x %s\n", r, hex(unicode.ToUpper(r)), hex(unicode.ToLower(r)), m, bits)
	}
}
