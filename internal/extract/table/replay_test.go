package table

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// tableReplays are the densest shapes measured for the readers, built at
// size bytes: the shortest rows of each layout under a row criterion none
// meets, since a value for each would pass the value limit, a hosts line of
// many aliases, an exports line of many clients, of many - words, whose
// defaults pile up for each client after them, and of quotes and escapes,
// fstab fields of escapes, and an exports line of many default options for
// many clients, whose cells grow as their product until the work budget
// refuses it. None is quadratic in time, so each is bounded by
// extracttest.OtherBound. make test reads them at 16 KiB and make slow
// at 1 MiB.
func tableReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string, n int) string { return strings.Repeat(unit, n/len(unit)) }
	return []extracttest.Replay[Spec]{
		{Name: "fstab rows", Spec: col("fstab", "spec", "file", "z"), Body: func() string { return fill("a b c d\n", size) }},
		{Name: "passwd rows", Spec: col("passwd", "uid", "name", "z"), Body: func() string { return fill("a:b:c:d:e:f:g\n", size) }},
		{Name: "group members", Spec: col("group", "gid", "user_list", "z"), Body: func() string { return "g:x:1:" + fill("a,", size) }},
		{Name: "hosts aliases", Spec: col("hosts", "ip_address", "names", "z"), Body: func() string { return "::1 " + fill("a ", size) }},
		{Name: "exports clients", Spec: col("exports", "options", "client", "z"), Body: func() string { return "/p " + fill("c(ro,rw) ", size) }},
		{Name: "exports default words", Spec: col("exports", "options", "client", "z"), Body: func() string { return "/p " + fill("-ro c ", size) },
			Refused: "exports: reading the table and matching its rows take more than"},
		{Name: "exports escapes and quotes", Spec: col("exports", "options", "client", "z"), Body: func() string { return "/p " + fill(`"\101"(ro) `, size) }},
		{Name: "fstab escapes", Spec: col("fstab", "spec", "file", "z"), Body: func() string { return fill(`\101 b c \101,\101`+"\n", size) }},
		{Name: "exports defaults for many clients", Spec: col("exports", "options", "client", "z"),
			Body:    func() string { return "/p -" + fill("ro,", size/2) + " " + fill("c ", size/2) },
			Refused: "exports: reading the table and matching its rows take more than"},
	}
}

// runTableReplays reads each shape at size within extracttest.OtherBound.
func runTableReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, Build, tableReplays(size), func(s Spec) string { return s.File })
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplayTableShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	runTableReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayTableShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runTableReplays(t, 1<<20)
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; the Makefile's slow target runs it.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}
