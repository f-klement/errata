package sudoers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The edges of a line: a # at its end, an ID after white space and after
// a digit's neighbour, a template expression empty or holding a #, an
// escaped quote in a quoted part, a digest's name with no :, and include
// keywords alone. Each reads as sudo reads it, without a panic.
func TestLineEdges(t *testing.T) {
	runReads(t, []readCase{
		{"# at the end", map[string]string{"sudoers": "Defaults x=a #\n#\n#include\n"}, defaults("sudoers", "x"), "sudoers:1=a"},
		{"IDs after a space", map[string]string{"sudoers": "User_Alias U = #9, #1000\nU ALL = /a\n"}, user("sudoers", "#9"),
			"sudoers:2={command=/a host=ALL user=#9,#1000}"},
		{"# before a colon is a comment", map[string]string{"sudoers": "Defaults x=a #:b\n"}, defaults("sudoers", "x"), "sudoers:1=a"},
		{"empty expression", map[string]string{"sudoers.j2": "Defaults x={{}}, y\n"}, defaults("sudoers.j2", "x"), "sudoers.j2:1={{}} !lit"},
		{"expression holding a #", map[string]string{"sudoers.j2": "Defaults x={{ a #b }}c\n"}, defaults("sudoers.j2", "x"), "sudoers.j2:1={{ a #b }}c !lit"},
		{"escaped quote", map[string]string{"sudoers": "Defaults x=\"a\\\"b\"\n"}, defaults("sudoers", "x"), "sudoers:1=a\"b"},
		{"digest name alone", map[string]string{"sudoers": "alice ALL = sha256 x\n"}, user("sudoers", "alice"), "sudoers:1={command=sha256 x host=ALL user=alice}"},
		{"order of tags and options", map[string]string{"sudoers": "alice ALL = TIMEOUT=1 CWD=/x NOEXEC: NOPASSWD: /a\n"}, user("sudoers", "alice"),
			"sudoers:1={command=/a host=ALL options=CWD=/x,TIMEOUT=1 tags=NOPASSWD,NOEXEC user=alice}"},
		{"%% in an include", map[string]string{"sudoers": "@include a%%b\n", "a%b": "Defaults x\n"}, defaults("sudoers", "x"), "a%b:1=true"},
	})
	runRefusals(t, []refuseCase{
		{"a host with a colon", map[string]string{"sudoers": "alice ab:g = /a\n"}, user("sudoers", "alice"), "expected = after the host list"},
		{"a colon ends a host alias", map[string]string{"sudoers": "Host_Alias H = ab:\n"}, defaults("sudoers", "x"), "is no alias name"},
		{"no option", map[string]string{"sudoers": "alice ALL = FOO=1 /x\n"}, user("sudoers", "alice"), "expected , or : after a command"},
		{"a \\ ends an include", map[string]string{"sudoers": "@include a\\\n"}, defaults("sudoers", "x"), "which the repository does not hold"},
		{"a % ends an include", map[string]string{"sudoers": "@include a%\n"}, defaults("sudoers", "x"), "which the repository does not hold"},
	})
}

// Every list of a record is at the line of its first entry, and an empty
// one at the command's. Planted break: orAt keeping an empty list's
// location.
func TestRecordLines(t *testing.T) {
	x, err := Build(user("sudoers", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	root := extracttest.Tracked(t, map[string]string{"sudoers": "alice ALL = \\\n (root) \\\n NOPASSWD: \\\n /a, \\\n /b\n"})
	vs, err := x.Extract(t.Context(), root)
	if err != nil || len(vs) != 2 {
		t.Fatalf("%v, %v", vs, err)
	}
	var got []string
	for _, k := range []string{"command", "host", "options", "runas", "runas_group", "tags", "user"} {
		got = append(got, fmt.Sprintf("%s:%d", k, vs[1].Data.Field(k).At.Line))
	}
	want := "command:5 host:1 options:5 runas:2 runas_group:5 tags:3 user:1"
	if strings.Join(got, " ") != want {
		t.Errorf("got %s, want %s", strings.Join(got, " "), want)
	}
}

// The bounds hold at their edges: as many values or bytes as the limit
// pass, one more is refused, and the files one spec reads hold max_bytes
// in all. A record of alice ALL = /a has ten nodes and 52 bytes. Planted
// break: count's > read as >=.
func TestBoundEdges(t *testing.T) {
	files := map[string]string{"sudoers": "Defaults a\nDefaults a\nalice ALL = /a\n"}
	for _, c := range []struct {
		spec          Spec
		values, bytes int
		refused       string
	}{
		{defaults("sudoers", "a"), 2, 0, ""},
		{defaults("sudoers", "a"), 1, 0, "more than"},
		{user("sudoers", "alice"), 10, 0, ""},
		{user("sudoers", "alice"), 9, 0, "more than"},
		{user("sudoers", "alice"), 0, 52, ""},
		{user("sudoers", "alice"), 0, 51, "hold more than 51 bytes"},
	} {
		b, err := Build(c.spec)
		if err != nil {
			t.Fatal(err)
		}
		x := b.(extractor)
		x.valueLimit = c.values
		if c.bytes > 0 {
			x.limit = int64(c.bytes)
		}
		_, err = x.Extract(t.Context(), extracttest.Tracked(t, files))
		switch {
		case c.refused == "" && err != nil:
			t.Errorf("%+v: %v", c, err)
		case c.refused != "" && (err == nil || !strings.Contains(err.Error(), c.refused)):
			t.Errorf("%+v: %v, want %q", c, err, c.refused)
		}
	}
}

// wide is a sudoers that includes 100 files, each of which includes the
// same 101: more than core.MaxFiles reads, two deep.
func wide() map[string]string {
	files := map[string]string{"sudoers": "#includedir d\n"}
	for i := range 101 {
		files[fmt.Sprintf("d/f%d", i)] = "#includedir ../e\n"
		files[fmt.Sprintf("e/f%d", i)] = "Defaults x\n"
	}
	delete(files, "d/f100")
	return files
}

// Includes nest to 128 deep and no deeper, a directory that includes
// itself reaches that depth too, the files one spec reads are at most
// core.MaxFiles, and the files an include reads hold max_bytes with the
// file that includes them. Planted breaks: count's > maxDepth read as >=;
// l.files counted down.
func TestIncludeBounds(t *testing.T) {
	chain := func(n int) map[string]string {
		files := map[string]string{}
		for i := range n {
			files[fmt.Sprintf("f%d", i)] = fmt.Sprintf("@include f%d\n", i+1)
		}
		files[fmt.Sprintf("f%d", n)] = "Defaults x\n"
		return files
	}
	runReads(t, []readCase{{"128 deep", chain(128), defaults("f0", "x"), "f128:1=true"}})
	runRefusals(t, []refuseCase{
		{"129 deep", chain(129), defaults("f0", "x"), "nest more than 128 deep"},
		{"a directory that includes itself", map[string]string{"d/a": "#includedir .\n"}, defaults("d/a", "x"), "nest more than 128 deep"},
		{"too many files", wide(), defaults("sudoers", "x"), fmt.Sprintf("are more than %d", core.MaxFiles)},
		{"max_bytes over the includes", map[string]string{"sudoers": "@include x\n", "x": "Defaults x=aaaaaaaaaaaaa\n"},
			Spec{Where: core.Where{File: "sudoers", MaxBytes: 20}, Defaults: "x"}, "hold at most max_bytes, 20, in all"},
	})
}
