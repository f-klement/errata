package core

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The ordinary files the node limit was measured on can hold, at their full
// size, at most a quarter of the nodes the default allows: an OpenAPI
// document of 4.66 MB, a Helm index of 6.3 MB and a uv.lock of 7.9 MB. Each
// generator repeats one unit whose count of node starts does not depend on
// its index, so the count is linear in the units, and two small files give
// it at the full size.
func TestNodeLimitLeavesRoomForOrdinaryFiles(t *testing.T) {
	for _, c := range []struct {
		name  string
		f     *Format
		gen   func(n int) string
		units int
	}{
		{"OpenAPI of 3000 paths", YAMLFormat, extracttest.OpenAPI, 3000},
		{"Helm index of 220 charts of 40 versions", YAMLFormat, func(n int) string { return extracttest.HelmIndex(n, 40) }, 220},
		{"uv.lock of 5000 packages", TOMLFormat, func(n int) string { return extracttest.UVLock(n, 20) }, 5000},
	} {
		one, two := c.f.CountNodes([]byte(c.gen(1))), c.f.CountNodes([]byte(c.gen(2)))
		if three := c.f.CountNodes([]byte(c.gen(3))); three-two != two-one {
			t.Fatalf("%s: %d, %d, %d nodes for 1, 2, 3 units, not linear", c.name, one, two, three)
		}
		size1, size2 := len(c.gen(1)), len(c.gen(2))
		nodes, size := one+(c.units-1)*(two-one), size1+(c.units-1)*(size2-size1)
		if limit := int(DefaultMaxBytes / c.f.BytesPerNode); 4*nodes > limit {
			t.Errorf("%s, %d bytes: %d nodes, more than a quarter of %d", c.name, size, nodes, limit)
		}
	}
}

// yaml.v3 breaks lines on NEL, LS and PS as on \n, and reads UTF-16 after a
// byte order mark, so the YAML count bounds the nodes of a file that uses
// them. Each body is a planted break: before, a thousand of each counted
// four nodes. é ends in the byte PS ends in, and does not count.
func TestNodeCountBoundsUnicodeLineBreaks(t *testing.T) {
	for _, body := range []string{
		strings.Repeat("-\u0085", 1000),
		strings.Repeat("- ", 1000),
		strings.Repeat("- ", 1000),
		strings.Repeat("x --- ", 1000),
		extracttest.UTF16LE(strings.Repeat("- ", 1000) + "x\n"),
		extracttest.UTF16LE(strings.Repeat("-\u0085", 1000)),
	} {
		n := extracttest.YAMLNodes([]byte(body))
		if bound := YAMLFormat.CountNodes([]byte(body)); n < 1000 || n > bound {
			t.Errorf("%.12q: yaml.v3 builds %d nodes, the bound is %d", body, n, bound)
		}
	}
	if n := YAMLFormat.CountNodes([]byte(strings.Repeat("é", 1000))); n != 4 {
		t.Errorf("1000 é count %d nodes, want 4", n)
	}
}

// countNodes is four for the start of the file and two for each byte after
// which a node can start. Each row pins one way a byte counts or does not:
// a start byte, a byte that starts a node only before white space or the
// end, a NEL, LS or PS ending at a byte, a break cut short at the end of the
// file, and UTF-16 decoded as yaml.v3 decodes it, odd bytes and lone
// surrogates included. The values follow from the rule, worked by hand.
func TestCountNodesExactly(t *testing.T) {
	le := func(units ...uint16) string {
		b := []byte{0xff, 0xfe}
		for _, u := range units {
			b = append(b, byte(u), byte(u>>8))
		}
		return string(b)
	}
	for _, c := range []struct {
		name string
		f    *Format
		body string
		want int
	}{
		{"an empty file", JSONFormat, "", 4},
		{"JSON's starts", JSONFormat, `{"a":[1,2]}`, 12},
		{"TOML's starts", TOMLFormat, "a.b = [1,{c=2}]\n", 18},
		{"YAML's starts", YAMLFormat, "a: b\r\n", 10},
		{"- before a space", YAMLFormat, "- a", 6},
		{"- before a letter", YAMLFormat, "-a", 4},
		{"- at the end", YAMLFormat, "-", 6},
		{"? before a tab", YAMLFormat, "?\tx", 6},
		{"- before a NEL", YAMLFormat, "-\u0085", 8},
		{"- before a NEL cut short", YAMLFormat, "-\xc2", 4},
		{"- before an LS", YAMLFormat, "-\u2028", 8},
		{"- before a PS", YAMLFormat, "-\u2029", 8},
		{"- before an LS cut short", YAMLFormat, "-\xe2\x80", 4},
		{"- before a rune that is not a break", YAMLFormat, "-\xe2\x80\xa7", 4},
		{"an LS after a letter", YAMLFormat, "x\u2028", 6},
		{"an LS first in the file", YAMLFormat, "\u2028", 6},
		{"the last byte of an LS alone", YAMLFormat, "\xa8", 4},
		{"the last two bytes of an LS", YAMLFormat, "\x80\xa8", 4},
		{"the last byte of a NEL alone", YAMLFormat, "\x85", 4},
		{"a NEL", YAMLFormat, "\u0085", 6},
		{"0x85 after a letter", YAMLFormat, "a\x85", 4},
		{"é, which ends in PS's last byte", YAMLFormat, "é", 4},
		{"- before a NEL, in JSON", JSONFormat, "-\u0085", 4},
		{"UTF-16LE", YAMLFormat, le('a', ':'), 6},
		{"UTF-16BE", YAMLFormat, "\xfe\xff\x00a\x00:", 6},
		{"UTF-16 with an odd last byte", YAMLFormat, le('a') + ":", 4},
		{"a surrogate pair", YAMLFormat, le(0xd83d, 0xde00, ':'), 6},
		{"a lone high surrogate before :", YAMLFormat, le(0xd83d, ':'), 6},
		{"a lone high surrogate and an odd byte", YAMLFormat, le(0xd83d) + ":", 4},
		// A high surrogate joins the unit after it, not the one before: read
		// from i-2, the pair would take the low surrogate first and skip the
		// colon, counting 6. yaml.v3 refuses this file, so only the limit's
		// message could differ.
		{"a lone low surrogate before a high one", YAMLFormat, le(0xdc00, 0xd800, ':', '\n'), 8},
		{"a byte order mark in JSON", JSONFormat, le('a', ':'), 6},
		{"0xff alone first", YAMLFormat, "\xff:", 6},
	} {
		if got := c.f.CountNodes([]byte(c.body)); got != c.want {
			t.Errorf("%s: %q counts %d nodes, want %d", c.name, c.body, got, c.want)
		}
	}
}
