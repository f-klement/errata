package hcl

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	hclv2 "github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// A file may make max_bytes/16 tokens, as tokens counts them, and is
// refused past that before it is lexed, naming the count and the limit
// (owner, 2026-11-20). `a = [1,2]` counts 10. Planted breaks: the
// comparison taken with >=; the limit max_bytes itself; read never
// counting.
func TestAFileTooDenseForMaxBytesIsAnError(t *testing.T) {
	f := file("f.tf", "a = [1,2]")
	if got := shown(extractBounded(t, "a[0]", 160, f)); !slices.Equal(got, []string{"f.tf:1=1 key a"}) {
		t.Errorf("10 tokens at max_bytes 160: got %q", got)
	}
	want := []string{"error: f.tf: may make 10 tokens, past the limit of 9, one for every 16 bytes of max_bytes; raise max_bytes to read a file this dense"}
	if got := shown(extractBounded(t, "a[0]", 159, f)); !slices.Equal(got, want) {
		t.Errorf("10 tokens at max_bytes 159: got %q, want %q", got, want)
	}
}

// A number may take 1000 bytes and is refused past that before it is
// parsed, in an attribute or an interpolation, naming the file, its line
// and the limit, since the parser's time grows with the square of a
// number's length: 3.2 MB of digits took 13.9 s and counts 6 tokens. 64
// KiB of digits is refused too. Planted breaks: the comparison taken
// with >=; read never checking; the offset of the file's first token
// given; the check on identifiers instead of numbers.
func TestANumberLongerThanTheLimitIsAnError(t *testing.T) {
	digits := strings.Repeat("7", 1000)
	same(t, extractPath(t, "a", file("f.tf", "b = 1\na = "+digits+"\n")), "f.tf:2="+digits+" key a")
	const want = "error: f.tf:2: a number longer than 1000 bytes, the limit, since parsing one takes time that grows with the square of its length"
	same(t, extractPath(t, "a", file("f.tf", "b = 1\na = 1"+digits+"\n")), want)
	same(t, extractPath(t, "a", file("f.tf", "b = 1\na = \"${1"+digits+"}\"\n")), want)
	same(t, extractPath(t, "a", file("f.tf", "b = 1\na = "+strings.Repeat("7", 64<<10)+"\n")), want)
}

// A template may cost max_bytes << 7 to join, 2^32 at the default, and
// is refused past that before it is parsed, naming the file, the line of
// its quote or heredoc and the limit (owner, 2026-11-20). Its cost is its
// joins, each literal part that follows a literal part, times its parts,
// each literal and each ${ and %{; plus, over each run of adjacent
// literals, the run's parts times its bytes, divided by 16. Each heredoc
// line is a literal part, and so is the text up to a $${ or %%{. A
// template inside another's interpolation counts on its own, and each
// template from its own quote or heredoc. A %{ is a part that leaves the
// run open, since the parser drops an invalid directive such as %{z} and
// joins the literals either side of it. The rows that call joinAt
// take the limit of a max_bytes of 1 MiB, 2^27, so that their files lex
// within a mutation run's heap cap. Planted breaks: the comparison taken
// with >=; read never checking; the limit fixed at 2^32; the
// interpolations or the directives left out of the parts; a literal
// after an interpolation counted as a join; a %{ ending the run; a run's
// bytes left out; runs
// set in place of added; the division by 16 dropped; a close not ending
// its template; an open not starting one; the offset of the part past
// the limit given.
func TestATemplateThatCostsTooMuchToJoinIsAnError(t *testing.T) {
	rep := strings.Repeat
	lines := func(n int) string { return "x = <<E\n" + rep("\n", n) + "E\n" }
	interps := func(m int) string { return "x = \"" + rep("$${", 2000) + rep("${a}", m) + "\"\n" }
	directives := func(k int) string { return "x = \"" + rep("$${", 2000) + rep("%{if a}%{endif}", k) + "\"\n" }
	long := func(n int) string { return "x = <<E\n" + rep(rep("b", n-1)+"\n", 2) + "E\n" }
	runs := "x = \"" + rep(rep("$${", 100)+"${a}", 2) + rep("$${", 100) + "\"\n"
	invalid := func(n int) string { return "x = \"" + rep("a%{z}", n) + "\"\n" }
	nested := "x = \"${\"" + rep("$${", 2000) + rep("${a}", 64767) + "\"}" + rep("$${", 2000) + "\"\n"
	for name, row := range map[string]struct {
		data    string
		limit   int64
		refused bool
	}{
		"11239 lines of a byte":                {lines(11239), 1 << 27, false},
		"11240 lines of a byte":                {lines(11240), 1 << 27, true},
		"64767 interpolations":                 {interps(64767), 1 << 27, false},
		"64768 interpolations":                 {interps(64768), 1 << 27, true},
		"32383 pairs of directives":            {directives(32383), 1 << 27, false},
		"32384 pairs of directives":            {directives(32384), 1 << 27, true},
		"8067 invalid directives":              {invalid(8067), 1 << 27, false},
		"8068 invalid directives":              {invalid(8068), 1 << 27, true},
		"two lines of 3995 bytes":              {long(3995), 1000, false},
		"two lines of 3996 bytes":              {long(3996), 1000, true},
		"three runs, at the limit":             {runs, 95319, false},
		"three runs, past the limit":           {runs, 95318, true},
		"two heredocs, each at the limit":      {lines(11239) + lines(11239), 1 << 27, false},
		"a string at the limit in another one": {nested, 1 << 27, false},
	} {
		if _, refused := joinAt(lex([]byte(row.data)), row.limit); refused != row.refused {
			t.Errorf("%s at %d: refused is %v", name, row.limit, refused)
		}
	}
	for name, past := range map[string]struct {
		data     string
		maxBytes int64
		line     int
	}{
		"11240 lines, opened on line 2":          {"b = 1\n" + lines(11240), 1 << 20, 2},
		"a string past it in another, on line 2": {"x = \"${\n\"" + rep("$${", 10632) + "\"}\"\n", 1 << 20, 2},
		"12000 lines at a max_bytes of 1 MiB":    {lines(12000), 1 << 20, 1},
		"8068 invalid directives at 1 MiB":       {invalid(8068), 1 << 20, 1},
		"65535 lines, then 1000 interpolations":  {"x = <<E\n" + rep("\n", 65535) + rep("${a}", 1000) + "\nE\n", 32 << 20, 1},
	} {
		if got := shown(extractBounded(t, "x", past.maxBytes, file("f.tf", past.data))); !slices.Equal(got, []string{templateError(past.line, past.maxBytes)}) {
			t.Errorf("%s: got %q, want %q", name, got, templateError(past.line, past.maxBytes))
		}
	}
	same(t, extractBounded(t, "x", 2<<20, file("f.tf", lines(12000))), "f.tf:1="+rep("\n", 12000)+" key x")
	same(t, extractPath(t, "x", file("f.tf", "x = <<E\na$${b}\nE\n")), "f.tf:1=a${b}\n key x")
}

// templateError is the error a template refused at line under a max_bytes
// of maxBytes shows as.
func templateError(line int, maxBytes int64) string {
	return "error: f.tf:" + strconv.Itoa(line) + ": a template that costs more than " + strconv.FormatInt(maxBytes<<7, 10) + " to join, 128 for every byte of max_bytes, counting its joins times its parts and each run of literals' parts times bytes over 16; each heredoc line, each ${ and %{, and the text up to each $${ or %%{ is a part; raise max_bytes to read it"
}

// A heredoc of 8000 lines with two interpolations on each, 424 KB, is
// read: its runs of adjacent literals are two parts long, so its cost
// is about 3.2e8, though its parts times all its literal bytes pass 2^32.
// Planted break: the cost taken as parts times every literal's bytes,
// the product of the round before.
func TestAHeredocWithInterpolationsOnEachLineIsRead(t *testing.T) {
	data := "x = <<E\n" + strings.Repeat("  name = \"${var.prefix}-svc\" # owner ${var.team} pad\n", 8000) + "E\n"
	got := extractPath(t, "x", file("f.tf", data))
	if got.Error != nil || len(got.Values) != 1 || !got.Values[0].NotLiteral {
		t.Errorf("got %q", shown(got))
	}
}

// tokens counts a run of letters, digits and _ once, and again where a
// letter follows a digit; a run of blanks once; any other byte once; and
// the end once. Planted breaks: each clause of the count dropped in turn,
// and kind putting _, a digit, a tab or the first or last letter or digit
// elsewhere.
func TestTokensCountsRunsAndBytes(t *testing.T) {
	for data, want := range map[string]int{
		"":           1,
		"abc_9":      2,
		"_a":         2,
		"aazzAAZZ":   2,
		"0099":       2,
		"9a":         3,
		"1e5x":       4,
		"a1b2":       3,
		"  \t \t":    2,
		"\r\r":       3,
		"a b":        4,
		",,":         3,
		"\n\n":       3,
		"\xc3\xa9":   3,
		"a.b = c[*]": 11,
	} {
		if got := tokens([]byte(data)); got != want {
			t.Errorf("tokens(%q) = %d, want %d", data, got, want)
		}
	}
}

// tokens is an upper bound on the tokens hclsyntax.LexConfig makes, the
// lexer's end of file among them, whatever the bytes. Planted break: a
// letter after a digit counted with it, though a number ends there.
func FuzzTokensBoundTheLexer(f *testing.F) {
	for _, s := range []string{
		mainTF, "", "1x", "1e5x", "a = \"  \"", "a = \"\"", "a = \"${x}${y}\"", "a = \"%{if x}%{endif}\"",
		"a = <<EOT\n\n\n  \nEOT\n", "a = <<-EOT\n x\n EOT", "a = <<EOT\n", "# \n// \n/* */", "/*", "a = \"",
		"\xff\xfe\x00", "😀😀😀 = 1", "é = 1", "a = \"é😀\"", "a = 1.5e+3", "a = 0x1f", "a\r\nb\r\n",
		"a = \"\\n\\\"$${x}%%{y}\"", "a = [ for x in y : x if z ]", "a = {for k, v in m : k => v...}",
		"a = x ? y : z", "a = !b == -c", "a = x.*.y[*][0]", "a = \"${\"${\"\"}\"}\"", "a = ~} ${~ x ~}",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		lexed, _ := hclsyntax.LexConfig(data, "f.tf", hclv2.InitialPos)
		if got := tokens(data); got < len(lexed) {
			t.Fatalf("tokens(%q) = %d, below the %d tokens the lexer makes: %v", data, got, len(lexed), lexed)
		}
	})
}
