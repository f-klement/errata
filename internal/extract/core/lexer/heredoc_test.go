package lexer

import (
	"slices"
	"strconv"
	"testing"
)

// hereList is every span l finds in src as spanList writes it, with a
// heredoc's body and quote after its opener.
func hereList(t *testing.T, name, src string) []string {
	t.Helper()
	l, _ := Named(name)
	var out []string
	for s := range l.Spans([]byte(src)) {
		if s.Kind == Text && s.BodyStart == s.BodyEnd {
			continue // the <? a PHP file starts with
		}
		item := [...]string{Comment: "comment", String: "string", Name: "name", Text: "text"}[s.Kind] + " " + src[s.BodyStart:s.BodyEnd]
		if s.Here.Close > 0 {
			item = "heredoc " + src[s.Start:s.End] + " " + strconv.Quote(src[s.Here.Start:s.Here.End])
			if s.Here.Quote != 0 {
				item += " quoted"
			}
		}
		if s.RunsOff(len(src)) {
			item += " (runs off)"
		}
		out = append(out, item)
	}
	return out
}

// A heredoc's body is the lines after its opener's line up to the line that
// holds its tag, which a span on the rest of the opener's line does not
// change: a comment there is read, and a second heredoc's body follows the
// first's closer. Its tag may stand in quotes; Ruby's <<- and <<~ and PHP's
// closer may be indented, PHP's may have code after it, and Ruby's << takes
// a tag only from an upper case letter, so a shift is no heredoc. A heredoc
// with no closer, and a span on its line that runs past its end, run off.
// Planted breaks: Indent ignored; the second heredoc's body read from the
// opener's line.
func TestHeredocs(t *testing.T) {
	for _, c := range []struct {
		lang, src string
		want      []string
	}{
		{"ruby", "x = <<~EOS # c\n  # no\n  'no\n  EOS\n# yes\n", []string{"heredoc <<~EOS \"  # no\\n  'no\\n\"", "comment  c", "comment  yes"}},
		{"ruby", "x = <<-'EOS'\n#{no}\n  EOS\n", []string{"heredoc <<-'EOS' \"#{no}\\n\" quoted"}},
		{"ruby", "x = <<EOS\n  EOS\nEOS\n# yes", []string{"heredoc <<EOS \"  EOS\\n\"", "comment  yes"}},
		{"ruby", "f(<<~A, <<~B) # c\na\nA\nb\nB\n# yes", []string{"heredoc <<~A \"a\\n\"", "heredoc <<~B \"b\\n\"", "comment  c", "comment  yes"}},
		{"ruby", "a << b # c\nx <<y # d\n", []string{"comment  c", "comment  d"}},
		{"ruby", "x = <<~EOS\nno end\n", []string{"string  (runs off)"}},
		{"ruby", "x = <<~EOS", []string{"string  (runs off)"}},
		{"ruby", "x = <<~EOS + \"a\nEOS\n\"", []string{"heredoc <<~EOS \"\"", "string  (runs off)"}},
		{"php", "<?php $a = <<<EOT\n  // no\n  EOT;\n// yes\n", []string{"heredoc <<<EOT \"  // no\\n\"", "comment  yes"}},
		{"php", "<?php $a = <<<'EOT'\nx\nEOTX\nEOT\n", []string{"heredoc <<<'EOT' \"x\\nEOTX\\n\" quoted"}},
		{"php", "<?php $a = <<<\"EOT\n", []string{"string EOT\n (runs off)"}},
		{"ruby", "x = <<~1\n", nil},
	} {
		if got := hereList(t, c.lang, c.src); !slices.Equal(got, c.want) {
			t.Errorf("%s %q:\n got %q\nwant %q", c.lang, c.src, got, c.want)
		}
	}
}

// A tag past maxTag opens no heredoc, and one at it does.
func TestHeredocTagBound(t *testing.T) {
	long := string(slices.Repeat([]byte("A"), maxTag))
	if got := hereList(t, "ruby", "x = <<"+long+"\nb\n"+long+"\n"); !slices.Equal(got, []string{"heredoc <<" + long + " \"b\\n\""}) {
		t.Errorf("a tag of %d bytes: %q", maxTag, got)
	}
	if got := hereList(t, "ruby", "x = <<"+long+"A\nb\n"+long+"A\n"); len(got) != 0 {
		t.Errorf("a tag of %d bytes: %q", maxTag+1, got)
	}
}

// Elixir's sigils: a lower case letter reads escapes, an upper case one or
// more does not, each between any of the delimiters, three quotes over
// lines included. A ~ with no letter, or with a letter and no delimiter,
// opens nothing. Planted break: Tag ignored.
func TestSigils(t *testing.T) {
	check(t, []pinned{
		{"elixir", `a = ~s(x \) # no) # yes`, []string{`string x \) # no`, "comment  yes"}},
		{"elixir", `a = ~S|x \| # yes`, []string{`string x \`, "comment  yes"}},
		{"elixir", "a = ~HTML\"\"\"\n# no\n\"\"\" # yes", []string{"string \n# no\n", "comment  yes"}},
		{"elixir", "a = ~r/#no/ ~w[a #b] # yes", []string{"string #no", "string a #b", "comment  yes"}},
		{"elixir", "a = ~ (1) # yes\nb = ~sx # c", []string{"comment  yes", "comment  c"}},
		{"elixir", "a = ~ss(x) # c", []string{"comment  c"}},
		{"elixir", "a = ~s'''\n# no\n''' # yes", []string{"string \n# no\n", "comment  yes"}},
	})
}

// A span says which form of its row it is.
func TestSpansNameTheirForm(t *testing.T) {
	l, _ := Named("python")
	var got []string
	for s := range l.Spans([]byte(`'a' "b" """c""" # d`)) {
		got = append(got, l.Forms[s.Form].Open)
	}
	if want := []string{"'", `"`, `"""`, "#"}; !slices.Equal(got, want) {
		t.Errorf("forms %q, want %q", got, want)
	}
}

// A heredoc's edges: a tag of digits, underscores and characters past ASCII
// after a letter or _, never a digit first; Ruby's << only before an upper
// case letter or _; a tag in any of three quotes, and an unclosed quote;
// a closer indented with a tab, one at the end of the file with no line
// end, one on a line of the file's last; an opener on the last line; a
// second heredoc whose first closes on the last line; a line comment after
// the opener that ends at its line's end. It kills the mutants of tag, here
// and the walk's skip that the cases above leave.
func TestHeredocEdges(t *testing.T) {
	for _, c := range []struct {
		lang, src string
		want      []string
	}{
		{"ruby", "x = <<~A_1é\na\n\tA_1é\n", []string{"heredoc <<~A_1é \"a\\n\""}},
		{"ruby", "x = <<~1A\n", nil},
		{"ruby", "x = <<_A\na\n_A", []string{"heredoc <<_A \"a\\n\""}},
		{"ruby", "x = <<a\na\na\n", nil},
		{"ruby", "x = <<~`A`\nls\nA\n", []string{"heredoc <<~`A` \"ls\\n\" quoted"}},
		{"ruby", "x = <<~\"A\"\nb\n  A", []string{"heredoc <<~\"A\" \"b\\n\" quoted"}},
		{"ruby", "x = <<~'A", []string{"string A (runs off)"}},
		{"ruby", "x = <<~'A\"\n", []string{"string A\"\n (runs off)"}},
		{"ruby", "x = <<~A", []string{"string  (runs off)"}},
		{"ruby", "f(<<~A, <<~B)\nA", []string{"heredoc <<~A \"\"", "string  (runs off)"}},
		{"ruby", "f(<<~A, <<~B)\nA\nb", []string{"heredoc <<~A \"\"", "string  (runs off)"}},
		{"ruby", "x = <<~A # c\nA\n# d", []string{"heredoc <<~A \"\"", "comment  c", "comment  d"}},
		{"ruby", "x = <<A\n  A\nA\n", []string{"heredoc <<A \"  A\\n\""}},
		{"php", "<?php $a = <<<EOT\nx\nEOT_2\nEOT;", []string{"heredoc <<<EOT \"x\\nEOT_2\\n\""}},
		// The bytes at each edge of a tag's: a, z, A, Z, 0, 9, _ and the
		// first byte past ASCII (À ends in 0x80); a tag after the bytes
		// beside them, @, [, `, { and /, opens nothing.
		{"ruby", "x = <<~azAZ09_À\nb\nazAZ09_À\n", []string{"heredoc <<~azAZ09_À \"b\\n\""}},
		{"ruby", "x = <<~a\x7f\na\n", []string{"heredoc <<~a \"\""}},
		{"ruby", "x = <<~0A\n", nil},
		{"ruby", "x = <<~9A\n", nil},
		{"ruby", "x = <<~@A [A {A /A\n", nil},
		{"ruby", "x = <<~", nil},
		{"ruby", "x = <<~A\n\nA\n", []string{"heredoc <<~A \"\\n\""}},
		{"ruby", "x = <<~A\na\n  ", []string{"string  (runs off)"}},
		{"ruby", "x = <<A\nAz\nA", []string{"heredoc <<A \"Az\\n\""}},
	} {
		if got := hereList(t, c.lang, c.src); !slices.Equal(got, c.want) {
			t.Errorf("%s %q:\n got %q\nwant %q", c.lang, c.src, got, c.want)
		}
	}
}

// A sigil's tag runs to TagMax letters, and a ~ at the end of the file, or
// with its letters at the end, opens nothing.
func TestSigilTags(t *testing.T) {
	check(t, []pinned{
		{"elixir", "a = ~ABCDEFGH(x) # c", []string{"string x", "comment  c"}},
		{"elixir", "a = ~ABCDEFGHI(x) # c", []string{"comment  c"}},
		{"elixir", "a = ~", nil},
		{"elixir", "a = ~s", nil},
	})
}

// Code inside an interpolation is lexed as the row's code, at any depth: a
// string in it is a string, and its closer closes neither the code nor the
// body around it. Python's {{ is a brace. Code that never closes runs off.
// Planted break: the walk skipped, so the closer search reads the code.
func TestInterpolationIsCode(t *testing.T) {
	check(t, []pinned{
		{"javascript", "a = `${ `\nPLANTED // no\n` }` // yes", []string{"string ${ `\nPLANTED // no\n` }", "comment  yes"}},
		{"javascript", "a = `${f(`${g(\"}\")}`)}` // yes", []string{"string ${f(`${g(\"}\")}`)}", "comment  yes"}},
		{"ruby", "a = \"#{ \"\n# no\n\" }\" # yes", []string{"string #{ \"\n# no\n\" }", "comment  yes"}},
		{"elixir", "a = \"#{ \"\n# no\n\" }\" # yes", []string{"string #{ \"\n# no\n\" }", "comment  yes"}},
		{"python", "a = f\"\"\"{ \"\"\"\n# no\n\"\"\" }\"\"\" # yes", []string{"string { \"\"\"\n# no\n\"\"\" }", "comment  yes"}},
		{"python", "a = f'{{ # no' # yes", []string{"string {{ # no", "comment  yes"}},
		{"python", "a = rf'{x:{w}}' # yes", []string{"string {x:{w}}", "comment  yes"}},
		{"kotlin", "val a = \"\"\"${ \"\"\"\n// no\n\"\"\" }\"\"\" // yes", []string{"string ${ \"\"\"\n// no\n\"\"\" }", "comment  yes"}},
		{"groovy", "def a = \"\"\"${ \"\"\"\n// no\n\"\"\" }\"\"\" // yes", []string{"string ${ \"\"\"\n// no\n\"\"\" }", "comment  yes"}},
		{"scala", "val a = s\"\"\"${ \"\"\"\n// no\n\"\"\" }\"\"\" // yes", []string{"string ${ \"\"\"\n// no\n\"\"\" }", "comment  yes"}},
		{"swift", "let a = \"\"\"\n\\( \"\"\"\n// no\n\"\"\" )\n\"\"\" // yes", []string{"string \n\\( \"\"\"\n// no\n\"\"\" )\n", "comment  yes"}},
		{"swift", "let a = #\"\\#( \"// no\" )\"# // yes", []string{"string \\#( \"// no\" )", "comment  yes"}},
		{"php", "<?php $a = \"{$b[\"// no\"]} ${c}\"; // yes", []string{"string {$b[\"// no\"]} ${c}", "comment  yes"}},
		{"kotlin", "val a = \"${ /* no } */ 1 }\" // yes", []string{"string ${ /* no } */ 1 }", "comment  yes"}},
		{"javascript", "a = `${ `", []string{"string ${ ` (runs off)"}},
		{"javascript", "a = `${ x", []string{"string ${ x (runs off)"}},
		{"ruby", "a = \"#{ <<~A }\"\nA\n", []string{"string #{ <<~A }\"\nA\n (runs off)"}},
	})
}

// A regular expression opens only where a value starts, holds [/] and
// escapes, and ends at its line; elsewhere / divides. PHP's text outside
// its code, after a ?> that a line comment ends at, and Ruby's __END__, are
// spans of their own. Planted breaks: Before ignored; Classes ignored; Stop
// ignored; ToEnd ignored.
func TestRegexTextAndEnd(t *testing.T) {
	check(t, []pinned{
		{"javascript", "x = /[/\"]\\//g // c", []string{"string [/\"]\\/", "comment  c"}},
		{"javascript", "x = a / b / c // d", []string{"comment  d"}},
		{"javascript", "x = {test: /\\.css$/, y: f(/'/)} // c", []string{"string \\.css$", "string '", "comment  c"}},
		{"ruby", "if x =~ /#\"/ then end # c", []string{"string #\"", "comment  c"}},
		{"groovy", "def p = ~/a\"b/ // c", []string{"string a\"b", "comment  c"}},
		{"php", "<p>don't</p>\n<?php $a = 1; // x ?> <p>it's // no</p>\n<?= $b ?>\n", []string{"text <p>don't</p>\n", "comment  x ", "text  <p>it's // no</p>\n", "text \n"}},
		{"php", "<?php $a = 1; # x ?>tail", []string{"comment  x ", "text tail"}},
		{"php", "no code at all, it's text", []string{"text no code at all, it's text"}},
		{"ruby", "x = 1\n__END__\n\"data # no\n", []string{"text \n\"data # no\n"}},
		{"ruby", "x = __END__ # c\n__END__x\n", []string{"comment  c"}},
	})
}

// A heredoc's body whose code closes past the closer's line, or never,
// runs off: the lexer cannot tell which line closes it. Code that closes
// inside the body, and a body in single quotes, read as before. Planted
// break: codeCloses not asked.
func TestHeredocCode(t *testing.T) {
	for _, c := range []struct {
		lang, src string
		want      []string
	}{
		{"ruby", "x = <<~A\n#{ \"\nA\n\" }\nA\n# c", []string{"string  (runs off)"}},
		{"ruby", "x = <<~A\n#{ y\nA\n", []string{"string  (runs off)"}},
		{"ruby", "x = <<~A\n#{ \"}\" } \\#{\nA\n# c", []string{"heredoc <<~A \"#{ \\\"}\\\" } \\\\#{\\n\"", "comment  c"}},
		{"ruby", "x = <<~'A'\n#{\nA\n# c", []string{"heredoc <<~'A' \"#{\\n\" quoted", "comment  c"}},
		{"php", "<?php $x = <<<A\n{$b[\"\nA\n\"]}\nA;\n", []string{"string  (runs off)"}},
	} {
		if got := hereList(t, c.lang, c.src); !slices.Equal(got, c.want) {
			t.Errorf("%s %q:\n got %q\nwant %q", c.lang, c.src, got, c.want)
		}
	}
}

// Delta review 1's cases. A Python f-string's format spec, after a : or !
// with no other bracket open and no = after it, is no code: a # or a quote
// in it opens nothing, and a {...} in it is a field. A / after a line end
// that a statement goes on over divides. PHP's text ends only at <?php,
// <?= or <? and a blank, so <?xml stays text. Scala walks ${...} only in
// an s, f or raw string. Planted breaks: Spec ignored; valueStarts not
// looking past line ends; Then ignored; the Scala plain forms walking ${.
func TestDelta1(t *testing.T) {
	check(t, []pinned{
		{"python", "a = f\"{n:#x}\" # yes", []string{"string {n:#x}", "comment  yes"}},
		{"python", "a = f\"{x:'^10}\" # yes", []string{"string {x:'^10}", "comment  yes"}},
		{"python", "a = f\"{x:{w}.{p}f} {x!r:>{w}}\" # yes", []string{"string {x:{w}.{p}f} {x!r:>{w}}", "comment  yes"}},
		{"python", "a = f\"{(y:=1)} {x != '#'} {d['k':'v']}\" # yes", []string{"string {(y:=1)} {x != '#'} {d['k':'v']}", "comment  yes"}},
		{"python", "a = f\"{x:{'#'}}\" # yes", []string{"string {x:{'#'}}", "comment  yes"}},
		{"python", "a = f\"{x:#x\n", []string{"string {x:#x\n (runs off)"}},
		{"javascript", "const total = a\n  / b // half", []string{"comment  half"}},
		{"javascript", "const a = (1\n  / 2)\nx = {\n  /re'/ // c", []string{"string re'", "comment  c"}},
		{"php", "<?xml version=\"1.0\"?>\n<?php $a = 1; // c\n", []string{"text <?xml version=\"1.0\"?>\n", "comment  c"}},
		{"php", "<?php $a = 1; ?><?xml x='y'?><? $b; // c", []string{"text <?xml x='y'?>", "comment  c"}},
		{"scala", "val a = \"${\" // c\nval b = s\"${ \"}\" }\" // d\nval c = sql\"${ x }\" // e", []string{"string ${", "comment  c", "string ${ \"}\" }", "comment  d", "string ${ x }", "comment  e"}},
	})
}

// Delta review 2's cases. In a form whose code opens with a lone {, an
// escape takes no brace, so Python's rf'\\{{' is a backslash and a brace:
// the two pygments lines the corpus found. Ruby's percent literals are
// read where a value starts, their bracket delimiters nest, and a % after
// a value is the operator. A Ruby regular expression may run over lines.
// Planted breaks: braceCode's escape skipping the brace; the percent forms
// left out; Pair ignored; the Ruby regex ending at its line; Words
// ignored; Glued ignored; LineStarts ignored.
func TestDelta2(t *testing.T) {
	check(t, []pinned{
		{"python", "    (rf'(\\{{[{_dash}])(![^}}]*)(\\}}?)', # c\n", []string{"string (\\{{[{_dash}])(![^}}]*)(\\}}?)", "comment  c"}},
		{"python", "    (rf'(\\{{%)(-?\\s*)(filter)(\\s+)({_ident_inner})', # c\n", []string{"string (\\{{%)(-?\\s*)(filter)(\\s+)({_ident_inner})", "comment  c"}},
		{"python", "a = f'\\{{a}}' # c", []string{"string \\{{a}}", "comment  c"}},
		{"ruby", "x = %r{^/([a-z]):/}i # c", []string{"string ^/([a-z]):/", "comment  c"}},
		{"ruby", "x = \"#{n ? %[-#{n}] : ''}\" # c", []string{"string #{n ? %[-#{n}] : ''}", "comment  c"}},
		{"ruby", "x = %w(a (b) #c) # d", []string{"string a (b) #c", "comment  d"}},
		{"ruby", "x = %q<a # b> + %Q|#{y}| # c", []string{"string a # b", "string #{y}", "comment  c"}},
		{"ruby", "x = a % (b) # c\ny = 10 % 3 # d", []string{"comment  c", "comment  d"}},
		{"ruby", "re = /\n  a # x\n  \"b\n/x # c", []string{"string \n  a # x\n  \"b\n", "comment  c"}},
		{"ruby", "x = 10 / 2 # a\ny = 3 / 4 # b", []string{"comment  a", "comment  b"}},
		// After a word a value follows, and never after a symbol's : (the
		// corpus's rubygems and rake lines).
		{"ruby", "if /https?:/.match?(uri) # a\n  x\nend # b", []string{"string https?:", "comment  a", "comment  b"}},
		{"ruby", "case x\nwhen /^%/ # a\nend", []string{"string ^%", "comment  a"}},
		{"ruby", "ops = [:%, :/, :==] # a", []string{"comment  a"}},
		{"ruby", "x = a.if / 2 # a", []string{"comment  a"}},
		{"ruby", "line = src.shift\n  /^\\s*/ =~ line # a", []string{"string ^\\s*", "comment  a"}},
		{"javascript", "return /'/.test(s) // a\ny = typeof /x/ // b", []string{"string '", "comment  a", "string x", "comment  b"}},
	})
}

// Delta review 3's cases. After a word that may be a method's name, a /
// opens a regex only with a blank before it and none after it; a line
// continued with a backslash does not end the statement; a ? or : glued
// before / is a character's or a symbol's (the webrick line); JavaScript's
// of is a name. Planted breaks: Calls ignored; the backslash not read; ?
// left out of Glued; of a value word again; a line end after the opener
// not counted as a blank.
func TestDelta3(t *testing.T) {
	check(t, []pinned{
		{"ruby", "p = 4\nq = p / 2 # a\nr = 'it' # b\ns = 'x/'", []string{"comment  a", "string it", "comment  b", "string x/"}},
		{"ruby", "p /x'/ # a", []string{"string x'", "comment  a"}},
		{"ruby", "puts/x/ # a", []string{"comment  a"}},
		{"ruby", "X = a \\\n  / b # a\nY = 'it' # b", []string{"comment  a", "string it", "comment  b"}},
		{"ruby", "X = a \\\r\n  / b # a", []string{"comment  a"}},
		{"ruby", "if req.path[-1] != ?/ # a\n  x = \"/\" # b\nend", []string{"comment  a", "string /", "comment  b"}},
		{"ruby", "c ? /re'/ : d # a", []string{"string re'", "comment  a"}},
		{"ruby", "X = :/\nY = '/' # a", []string{"string /", "comment  a"}},
		{"javascript", "const x = of / 2 // a\nconst y = 'it' // b", []string{"comment  a", "string it", "comment  b"}},
		// Delta 4's: a / before a line end after print divides.
		{"ruby", "print /\n  2\nY = 'it' # a\nZ = 'a/'", []string{"string it", "comment  a", "string a/"}},
		{"ruby", "X = p /\r\n  2 # a", []string{"comment  a"}},
	})
}
