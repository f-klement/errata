package toml

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

const (
	deepRefused   = "f: line 1: nested more than 1000 levels deep, the limit; a regex extractor can still read the file"
	valuesRefused = "f: the extractor selects more than 100000 values, the limit; narrow the path or the pattern so it selects fewer"
)

// The TOML inputs that took quadratic time, that the earlier limits refused
// though they are ordinary, and that got past those limits. Times are
// linear, linear under -race, and the quadratic pass, measured with that pass
// put back. A quadratic case is full, at the size its times were measured
// at, and make slow runs it; make test runs the same shape at a tenth of the
// size, which reads within extracttest.OtherBound. One more is full, the
// list, since no smaller input reaches its limit; another test reaches it in
// make test.
var replays = []extracttest.Replay[Spec]{
	// Repeated keys, quadratic in go-toml's decode.
	// Linear 0.08 s, 0.23 s under -race;
	// quadratic 2.74 s; bound 0.9 s.
	{Name: "TOML table of 30000 keys", Spec: Spec{File: "f", Path: "k29999"},
		Body: func() string { return extracttest.KeyLines(30000, " = ") }, Values: 1, Last: "1", Bound: 900 * time.Millisecond, Full: true},
	{Name: "TOML table of 3000 keys", Spec: Spec{File: "f", Path: "k2999"},
		Body: func() string { return extracttest.KeyLines(3000, " = ") }, Values: 1, Last: "1"},
	// Linear 0.13 s, 0.39 s under -race;
	// quadratic 3.96 s; bound 1.3 s.
	{Name: "30000 TOML tables of one key", Spec: Spec{File: "f", Path: "t29999.k"},
		Body: func() string {
			return extracttest.RepeatLines(30000, func(i int) string { return "[t" + strconv.Itoa(i) + "]\nk = 1\n" })
		}, Values: 1, Last: "1", Bound: 1300 * time.Millisecond, Full: true},
	{Name: "3000 TOML tables of one key", Spec: Spec{File: "f", Path: "t2999.k"},
		Body: func() string {
			return extracttest.RepeatLines(3000, func(i int) string { return "[t" + strconv.Itoa(i) + "]\nk = 1\n" })
		}, Values: 1, Last: "1"},
	// Linear 0.09 s, 0.42 s under -race;
	// quadratic 2.79 s; bound 0.9 s.
	{Name: "nested array tables, then 30000 keys", Spec: Spec{File: "f", Path: "a[1].k29999"},
		Body: func() string {
			return "[[a]]\n" + strings.Repeat("[[a.b]]\n", 20000) + "[[a]]\n" + extracttest.KeyLines(30000, " = ")
		},
		Values: 1, Last: "1", Bound: 900 * time.Millisecond, Full: true},
	{Name: "nested array tables, then 3000 keys", Spec: Spec{File: "f", Path: "a[1].k2999"},
		Body: func() string {
			return "[[a]]\n" + strings.Repeat("[[a.b]]\n", 2000) + "[[a]]\n" + extracttest.KeyLines(3000, " = ")
		},
		Values: 1, Last: "1"},
	// Linear 0.23 s, 0.94 s under -race;
	// quadratic 3.42 s; bound 1.1 s.
	{Name: "20000 TOML sections of four keys", Spec: Spec{File: "f", Path: "s19999.d"},
		Body: func() string {
			return extracttest.RepeatLines(20000, func(i int) string { return fmt.Sprintf("[s%d]\na = 1\nb = \"x\"\nc = true\nd = 2\n", i) })
		}, Values: 1, Last: "2", Bound: 1100 * time.Millisecond, Full: true},
	{Name: "2000 TOML sections of four keys", Spec: Spec{File: "f", Path: "s1999.d"},
		Body: func() string {
			return extracttest.RepeatLines(2000, func(i int) string { return fmt.Sprintf("[s%d]\na = 1\nb = \"x\"\nc = true\nd = 2\n", i) })
		}, Values: 1, Last: "2"},
	{Name: "10000 TOML array tables", Spec: Spec{File: "f", Path: "a[-1].x"},
		Body: func() string { return strings.Repeat("[[a]]\nx = 1\n", 10000) }, Values: 1, Last: "1"},
	// Values unbounded: a TOML list gave every item, as a regex (\d*)
	// matched at every byte did, which TestExtractValueLimit replays at the
	// limit. No shorter list reaches the limit, and JSON stands for TOML in
	// TestExtractValueLimit, so the list is full.
	{Name: "TOML list of 100001 items", Spec: Spec{File: "f", Path: "a[*]"},
		Body: func() string { return "a = [" + extracttest.List(100001, "1") + "]\n" }, Refused: valuesRefused, Full: true},
	// TOML dotted keys and headers that nothing bounded the depth of.
	{Name: "TOML dotted key of 100000 parts", Spec: Spec{File: "f", Path: "a"},
		Body: func() string { return strings.Repeat("a.", 100000) + "b = 1\n" }, Refused: deepRefused},
	{Name: "TOML header of 100000 parts", Spec: Spec{File: "f", Path: "a"},
		Body: func() string { return "[" + strings.Repeat("a.", 100000) + "b]\n" }, Refused: deepRefused},
	{Name: "100 KB of TOML [", Spec: Spec{File: "f", Path: "a"},
		Body: func() string { return "a = " + strings.Repeat("[", 100000) }, Refused: "f: toml: line 1: arrays and inline tables are nested more than the maximum of 10000 levels deep"},
	// Ordinary TOML the step model refused. TestNodeLimitLeavesRoom checks
	// the node limit against the full sizes of the ordinary files.
	{Name: "500 TOML tables of 20 keys", Spec: Spec{File: "f", Path: "t499.k19"},
		Body: func() string {
			return extracttest.RepeatLines(500, func(i int) string { return "[t" + strconv.Itoa(i) + "]\n" + extracttest.KeyLines(20, " = ") })
		}, Values: 1, Last: "1"},
	{Name: "2000 TOML tables of 10 keys", Spec: Spec{File: "f", Path: "t1999.k9"},
		Body: func() string {
			return extracttest.RepeatLines(2000, func(i int) string { return "[t" + strconv.Itoa(i) + "]\n" + extracttest.KeyLines(10, " = ") })
		}, Values: 1, Last: "1"},
	{Name: "10000 translation tables", Spec: Spec{File: "f", Path: "msg9999.other"},
		Body: func() string {
			return extracttest.RepeatLines(10000, func(i int) string { return fmt.Sprintf("[msg%d]\nother = \"text %d\"\n\n", i, i) })
		}, Values: 1, Last: "text 9999"},
	{Name: "poetry.lock of 200 packages", Spec: Spec{File: "f", Path: "metadata.files.p199[-1].file"},
		Body: func() string { return poetryLock(200, 30) }, Values: 1, Last: "p199-29.whl"},
	{Name: "uv.lock of 200 packages", Spec: Spec{File: "f", Path: "package[*].wheels[-1].size"},
		Body: func() string { return extracttest.UVLock(200, 20) }, Values: 200, Last: "19"},
}

func TestReplayMeasuredInputs(t *testing.T) {
	extracttest.RunReplays(t, Build, replays, false)
}

// The full cases, at the sizes their times were measured at.
func TestReplayMeasuredInputsAtFullSize(t *testing.T) {
	slowOnly(t)
	extracttest.RunReplays(t, Build, replays, true)
}

// poetryLock is a poetry.lock of format 1.1: n packages, then one array of
// files for each.
func poetryLock(n, files int) string {
	return extracttest.RepeatLines(n, func(i int) string {
		return fmt.Sprintf("[[package]]\nname = \"p%d\"\nversion = \"1.0\"\n\n[package.dependencies]\nx = \">=1\"\n\n", i)
	}) + "[metadata]\nlock-version = \"1.1\"\n\n[metadata.files]\n" + extracttest.RepeatLines(n, func(i int) string {
		return fmt.Sprintf("p%d = [\n", i) + extracttest.RepeatLines(files, func(j int) string {
			return fmt.Sprintf("    {file = \"p%d-%d.whl\", hash = \"sha256:abcdef\"},\n", i, j)
		}) + "]\n"
	})
}
