package hcl

import (
	"math"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	hclv2 "github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// tooDeep is the error of a file nested too deeply, after its file and line.
const tooDeep = "nested more than 1000 levels deep, the limit, counting each bracket, quote, template sequence, directive, minus sign, not, condition and full splat"

// A file nested more than 1000 levels deep is an error naming the line of
// the level past the limit, before it is parsed, since the parser would
// overflow Go's stack at 100000 levels and kill the plugin. Each way the
// parser nests counts: brackets, parentheses, calls, quotes and
// interpolations, blocks, for expressions, directives, minus signs, nots,
// conditions and full splats, and so do a directive named after a line end
// or a comment and a for expression in braces across line ends. An
// %{endif} with no %{if} makes no room. 1000 levels are read. Planted
// breaks: deepAt comparing with >=, which refused 1000 levels; chains
// without the question mark; depth ignoring directives; directive
// counting an %{endif} with no %{if}; read parsing without the check;
// splat never set, or not counted, or seen only with no comment or line
// end between its tokens, or counted only at a ]; directive skipping no line end, or no
// comment; a for in braces ended at a line end; every level ended at one;
// a block comment taken for a line end.
func TestAFileNestedTooDeeplyIsAnError(t *testing.T) {
	rep := strings.Repeat
	type nest struct {
		file func(n int) string
		line int // the line of the level past the limit
	}
	for name, n := range map[string]nest{
		"brackets":     {func(n int) string { return "x = " + rep("[", n) + rep("]", n) }, 1},
		"parentheses":  {func(n int) string { return "x = " + rep("(", n) + "1" + rep(")", n) }, 1},
		"calls":        {func(n int) string { return "x = " + rep("f(", n) + rep(")", n) }, 1},
		"interpolated": {func(n int) string { return "x = " + rep(`"${`, n) + "1" + rep(`}"`, n) }, 1},
		"for":          {func(n int) string { return "x = " + rep("[for a in b: ", n) + "1" + rep("]", n) }, 1},
		"directives":   {func(n int) string { return "x = \"" + rep("%{if a}", n) + "y" + rep("%{endif}", n) + "\"" }, 1},
		"minus signs":  {func(n int) string { return "x = " + rep("-", n) + "1" }, 1},
		"nots":         {func(n int) string { return "x = " + rep("!", n) + "true" }, 1},
		"conditions":   {func(n int) string { return "x = " + rep("a ? b : ", n) + "c" }, 1},
		"full splats":  {func(n int) string { return "x = a" + rep("[*]", n) }, 1},
		// The parser reads a splat's * and ] past comments, and past line
		// ends in parentheses.
		"splats with a comment before the star": {func(n int) string { return "x = a" + rep("[/**/*]", n) }, 1},
		"splats with a comment after the star":  {func(n int) string { return "x = a" + rep("[*/**/]", n) }, 1},
		"splats across lines":                   {func(n int) string { return "x = (a" + rep("[\n*]", n) + ")" }, 1000},
		"splats across lines on both sides":     {func(n int) string { return "x = (a" + rep("[\n*\n]", n) + ")" }, 1999},
		// After an error in the file the parser takes any token after
		// [* as the splat's close.
		"splats closed by a parenthesis after an error": {func(n int) string { return "x = a[*y]\nz = (b" + rep("[*)", n) }, 2},
		"splats closed by a brace after an error":       {func(n int) string { return "x = a[*y]\nz = (b" + rep("[*}", n) }, 2},
		// A line end ends an expression only at the top and in braces, and
		// a comment only when it holds one.
		"minus signs across lines": {func(n int) string { return "x = (" + rep("-\n", n) + "1)" }, 1000},
		"minus signs and comments": {func(n int) string { return "x = " + rep("-/**/", n) + "1" }, 1},
		// The template parser skips line ends and comments before a
		// directive's name, and a for expression in braces reads across
		// line ends, as it does after comments before its for.
		"directives after a line end":     {func(n int) string { return "x = \"" + rep("%{\nif a}", n) + "\"" }, 1000},
		"directives after a comment":      {func(n int) string { return "x = \"" + rep("%{/**/if a}", n) + "\"" }, 1},
		"for directives after a line end": {func(n int) string { return "x = \"" + rep("%{\nfor a in b}", n) + "\"" }, 1000},
		"a for in braces":                 {func(n int) string { return "x = {for k, v in m : k => " + rep("-\n", n) + "1}" }, 1000},
		"a for after a comment":           {func(n int) string { return "x = {# c\nfor k, v in m : k => " + rep("!\n", n) + "a}" }, 1001},
	} {
		got := shown(extractPath(t, "x", file("f.tf", n.file(5000)+"\n")))
		if want := "error: f.tf:" + strconv.Itoa(n.line) + ": " + tooDeep; len(got) != 1 || got[0] != want {
			t.Errorf("%s 5000 deep: got %q, want %q", name, got, want)
		}
	}
	same(t, extractPath(t, "b", file("f.tf", strings.Repeat("b {\n", 1001)+strings.Repeat("}\n", 1001))),
		"error: f.tf:1001: "+tooDeep)
	same(t, extractPath(t, "x", file("f.tf", "x = \""+strings.Repeat("%{endif}", 5)+"${"+strings.Repeat("[", 999)+strings.Repeat("]", 999)+"}\"\n")),
		"error: f.tf:1: "+tooDeep)
	deep := "x = " + strings.Repeat("[", 999) + "1" + strings.Repeat("]", 999) + "\n"
	same(t, extractPath(t, "x"+strings.Repeat("[0]", 999), file("f.tf", deep)), "f.tf:1=1 key x")
}

// A chain ends with its expression, at a comma or at a line end at the
// top or in braces, a line comment's among them, and a level's chain ends
// with the level: more than 1000 negative numbers, conditions, splats and
// directives one after another, in blocks and objects too, nest no deeper
// than one, and after them a line nested 1000 levels is read and one
// nested 1001 is refused. Planted breaks: take ending no chain at a comma;
// take ending no chain at a line end; close leaving the closed level's
// chain counted, or its line ends; directive counting no %{endif};
// directive counting %{endif} up; directive ending a level at %{else} and
// not at %{endif}, or not at %{endfor}; braces never ended at a line end;
// any identifier after { taken for a for; a line comment not ending a
// line.
func TestDepthCountsOnlyWhatNests(t *testing.T) {
	var b strings.Builder
	b.WriteString("x = [" + strings.Repeat("-1, ", 1001) + "]\n")
	b.WriteString("y = [" + strings.Repeat("(-1), ", 1001) + "]\n")
	b.WriteString("z = \"" + strings.Repeat("%{if a}y%{endif}", 1001) + strings.Repeat("%{if a}y%{else}n%{endif}", 1001) + strings.Repeat("%{for v in l}${v}%{endfor}", 1001) + strings.Repeat("%{\nif a}y%{/**/endif}", 1001) + "\"\n")
	for i := range 1001 {
		b.WriteString("a" + strconv.Itoa(i) + " = c ? -1 : !d[*].e # note\n")
	}
	b.WriteString("o = {\n" + strings.Repeat("k = c ? -1 : !d[*].e\n", 1001) + "}\n")
	b.WriteString("blk {\n")
	for i := range 1001 {
		b.WriteString("k" + strconv.Itoa(i) + " = -1 // note\n")
	}
	b.WriteString("}\n")
	nest := func(n int) string { return "w = " + strings.Repeat("[", n) + strings.Repeat("]", n) + "\n" }
	r := extractPath(t, "x[*]", file("f.tf", b.String()+nest(1000)))
	if r.Error != nil || len(r.Values) != 1001 {
		t.Fatalf("got %d values and the error %v, want 1001 values", len(r.Values), shown(r))
	}
	same(t, extractPath(t, "x[*]", file("f.tf", b.String()+nest(1001))),
		"error: f.tf:"+strconv.Itoa(strings.Count(b.String(), "\n")+1)+": "+tooDeep)
}

// Whatever a file repeats 8000 times after a prefix, deepAt refuses it or
// the parser reads it on a stack of 512 KiB. A unit that nests deeper each
// time passes 1000 levels and is refused, so a file deepAt passes nests no
// deeper than its prefix and one unit, a few dozen levels, while 512 KiB
// holds about 75 levels of brackets and some thousands of minus signs:
// a parse that overflows it is a way of nesting deepAt does not count, or
// counts too little. A small stack keeps the file small, so the seeds run
// in seconds under -race. Planted
// breaks: splat always false; splat skipping no comment or line end;
// a splat counted only at a ]; directive not skipping line ends; a for
// in braces ended at a line end; every level ended at one; a block
// comment taken for a line end.
func FuzzDepthBoundsTheParser(f *testing.F) {
	for _, s := range [][2]string{
		{"x = ", "["}, {"x = \"", "${"}, {"x = \"", "%{if a}"}, {"x = <<E\n", "%{for a in b}"},
		{"x = ", "-"}, {"x = (", "a ?"}, {"x = a", ".b"}, {"x = f(", "g("}, {"x = {", "a = {"},
		{"b {\n", "c {\n"}, {"x = [", "for a in b: "}, {"x = a", "[0]"}, {"x = \"", "%{\nendif}"},
		{"x = a", "[*]"}, {"x = \"", "%{\nif a}"}, {"x = \"", "%{/**/if a}"}, {"x = \"", "%{\nfor a in b}"},
		{"x = a", "[/**/*]"}, {"x = a", "[*/**/]"}, {"x = (a", "[\n*]"}, {"x = (a", "[\n*\n]"},
		{"x = a[*y]\nz = (b", "[*)"}, {"x = a[*y]\nz = (b", "[*}"},
		{"x = (", "-\n"}, {"x = [", "!\n"}, {"x = ", "-/**/"}, {"x = {for k, v in m : k => ", "-\n"}, {"x = {#\nfor k, v in m : k => ", "!\n"}, {"x = {\n a = ", "-\n"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(_ *testing.T, prefix, unit string) {
		if len(unit) == 0 || len(unit) > 12 || len(prefix) > 32 {
			return
		}
		defer debug.SetMaxStack(debug.SetMaxStack(512 << 10))
		data := []byte(prefix + strings.Repeat(unit, 8000))
		if _, deep := deepAt(lex(data)); !deep {
			_, _ = hclsyntax.ParseConfig(data, "f.tf", hclv2.InitialPos) // only an overflow, which kills the run, fails
		}
	})
}

// lex is the tokens of data, as LexConfig makes them.
func lex(data []byte) hclsyntax.Tokens {
	tokens, _ := hclsyntax.LexConfig(data, "f.tf", hclv2.InitialPos)
	return tokens
}

// A full splat is seen at its *, the second token of a file at the
// earliest, when a [ comes before it past comments and line ends. A *
// after anything else is no splat, nor is a token after a [ that is not
// a *. Planted breaks: splat asking for a token before the [; splat not
// checking that the [ is there, or the *.
func TestASplatIsSeenAtItsStar(t *testing.T) {
	for data, i := range map[string]int{"[*]": 1, "[/**/*]": 2, "[\n*)": 2} {
		if !seenAt(lex([]byte(data)), i).splat {
			t.Errorf("the * of %q is not seen as a splat", data)
		}
	}
	for data, i := range map[string]int{"*]": 0, "/**/*]": 1, "[a]": 1, "[/**/a]": 2, "a*]": 1, "a/**/*]": 2} {
		if seenAt(lex([]byte(data)), i).splat {
			t.Errorf("token %d of %q is seen as a splat", i, data)
		}
	}
}

// deepAt takes time linear in a run of newlines or comments: over 200 KB
// of each it takes less than 4 times as long for each byte as over 12.8
// KB, the fastest of five, where a pass that looks back over the run at
// each of its tokens takes 16 times as long. The large run gets a
// deadline from the small one's time, so a quadratic pass fails at the
// deadline, not after the minutes it would take, in each of three tries,
// so one try a loaded machine descheduled does not fail it. Planted break:
// directive looking back from every token.
func TestDepthIsLinearInARunOfLineEnds(t *testing.T) {
	const large = 200 << 10
	for _, unit := range []string{"\n", "#\n", "/**/"} {
		run := func(size int) hclsyntax.Tokens {
			return lex([]byte("x = 1\n" + strings.Repeat(unit, size/len(unit)) + "y = 2\n"))
		}
		small, big := run(large/16), run(large)
		base := time.Duration(math.MaxInt64)
		for range 5 {
			start := time.Now()
			deepAt(small)
			base = min(base, time.Since(start))
		}
		if !withinThreeTries(func() { deepAt(big) }, 64*base) {
			t.Fatalf("deepAt took more than %s over 200 KB of %q in each of three tries, 64 times the %s it took over 12.8 KB", 64*base, unit, base)
		}
	}
}

// withinThreeTries is whether f returns within limit in one of three tries.
// One try on a loaded machine can lose its CPU for longer than the limit; a
// pass 16 times slower than the limit allows overruns all three.
func withinThreeTries(f func(), limit time.Duration) bool {
	for range 3 {
		done := make(chan struct{})
		go func() {
			f()
			close(done)
		}()
		select {
		case <-done:
			return true
		case <-time.After(limit):
		}
	}
	return false
}
