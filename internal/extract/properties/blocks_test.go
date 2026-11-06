package properties

import (
	"bytes"
	"runtime"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/core"
)

// A template's block scan keeps a few words whatever the file holds: an
// ERB template at the max_bytes default, of the densest blocks, <%{%><%}%>
// on each line, allocates no more than 1 MiB beyond what the same bytes
// cost as a file that is no template. The review measured a span list,
// 16 bytes a block, at 56 MiB against 36 MiB on 4 MiB. Planted break: a
// span kept for each block closed.
func TestTemplateBlocksAtMaxBytesKeepNoListOfBlocks(t *testing.T) {
	slowOnly(t)
	line := []byte("<%{%><%}%>\n")
	data := bytes.Repeat(line, core.DefaultMaxBytes/len(line))
	read := func(file string) (uint64, time.Duration) {
		x, err := Build(Spec{File: file, Key: "a"})
		if err != nil {
			t.Fatal(err)
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		if _, err := x.(core.EntryExtractor).FromData(data); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		took := time.Since(start)
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc, took
	}
	plain, plainTook := read("a.properties")
	tpl, tplTook := read("a.properties.erb")
	t.Logf("%d bytes: a.properties allocates %d bytes in %v, a.properties.erb %d bytes in %v", len(data), plain, plainTook, tpl, tplTook)
	if tpl > plain+1<<20 {
		t.Errorf("the template allocates %d bytes, the plain file %d: the scan keeps more than the block open", tpl, plain)
	}
}
