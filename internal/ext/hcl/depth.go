package hcl

import (
	"bytes"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// maxDepth is how deeply a file may nest, as errata's own readers allow
// (core.MaxDepth, which the plugin may not import). hclsyntax's parser
// calls itself once for each level, with no limit of its own, so 100000
// brackets, 200 KB, overflow Go's stack of 1 GB, which kills the process
// and no recover catches. A file nested deeper is refused before it is
// parsed.
const maxDepth = 1000

// opens and closes are the tokens that open and close a level: brackets,
// braces, parentheses, quotes, heredocs and template sequences.
var (
	opens = map[hclsyntax.TokenType]bool{
		hclsyntax.TokenOBrace: true, hclsyntax.TokenOBrack: true, hclsyntax.TokenOParen: true,
		hclsyntax.TokenOQuote: true, hclsyntax.TokenOHeredoc: true,
		hclsyntax.TokenTemplateInterp: true, hclsyntax.TokenTemplateControl: true,
	}
	closes = map[hclsyntax.TokenType]bool{
		hclsyntax.TokenCBrace: true, hclsyntax.TokenCBrack: true, hclsyntax.TokenCParen: true,
		hclsyntax.TokenCQuote: true, hclsyntax.TokenCHeredoc: true, hclsyntax.TokenTemplateSeqEnd: true,
	}
)

// chains are the tokens the parser also calls itself for, without a
// bracket: a minus sign, a not and a condition's question mark, each of
// which nests what follows it until the expression ends. A full splat,
// [*], is one too, counted when its level closes.
var chains = map[hclsyntax.TokenType]bool{
	hclsyntax.TokenMinus: true, hclsyntax.TokenBang: true, hclsyntax.TokenQuestion: true,
}

// depth follows how deeply the parser will nest at each token of a file.
// Each open level keeps the level outside it, and a comma, or a line end
// at the top or in braces, where a line end ends an item, ends an
// expression and its chain.
type depth struct {
	outer []level // the levels outside the current one, innermost last
	level         // the current level
	now   int     // the levels open and every chain token they hold
}

// level is one open level of a file: the chain tokens its expression has
// counted so far, whether a line end ends that expression, and whether
// it is the bracket of a full splat.
type level struct {
	chain int
	lines bool
	splat bool
}

// deepAt is the byte offset of the first of tokens at which it nests
// more than maxDepth levels, and whether there is one. Directives count
// one level from %{if} or %{for} to their %{endif} or %{endfor}.
func deepAt(tokens hclsyntax.Tokens) (int, bool) {
	d := depth{level: level{lines: true}}
	for i, tok := range tokens {
		d.take(tok.Type, seenAt(tokens, i))
		if d.now > maxDepth {
			return tok.Range.Start.Byte, true
		}
	}
	return 0, false
}

// seen is what the parser makes of one token, as far as depth counts it.
type seen struct {
	dir   string // the directive it names, if, for, endif or endfor, or ""
	splat bool   // it is the * of a full splat, [*]
	lines bool   // it opens braces in which a line end ends an item
	end   bool   // it is a line end: a newline, or a comment that holds one
}

// seenAt is what token i of tokens is to the parser. The parser skips line
// ends and comments where it reads a directive's name after %{ and where
// it looks for the for of a for expression after {, and a for expression
// is not one in which a line end ends an item. A line comment ends with
// the line end the lexer puts in it.
func seenAt(tokens hclsyntax.Tokens, i int) seen {
	tok := tokens[i]
	return seen{
		dir:   directive(tokens, i),
		splat: splat(tokens, i),
		lines: tok.Type == hclsyntax.TokenOBrace && !named(tokens, past(tokens, i, 1), "for"),
		end:   tok.Type == hclsyntax.TokenNewline || tok.Type == hclsyntax.TokenComment && bytes.HasSuffix(tok.Bytes, []byte("\n")),
	}
}

// splat is whether token i of tokens is the * of a full splat, [*]. The
// parser takes a [ and a * as a full splat and calls itself for what
// follows whatever token comes next: a ], or after an error in the file
// a ) or a } too, since it never leaves recovery. It reads the * past
// comments, and past line ends inside parentheses and brackets, so [/**/*]
// and, in parentheses, [\n*] are splats to it too. At the top of a file it
// reads [\n*] as an index, and before an error [*) is none, so counting a
// splat there only counts a level too many.
func splat(tokens hclsyntax.Tokens, i int) bool {
	if tokens[i].Type != hclsyntax.TokenStar {
		return false
	}
	open := past(tokens, i, -1)
	return open >= 0 && tokens[open].Type == hclsyntax.TokenOBrack
}

// directive is the name of the directive token i of tokens names, if, for,
// endif or endfor, after the %{ that opens it, and "" otherwise. Only an
// identifier names one, since the lexer reads a directive as an
// expression, and only an identifier looks back: a newline or a comment
// looking back over the run before it would make a run of them cost the
// square of its length.
func directive(tokens hclsyntax.Tokens, i int) string {
	if tokens[i].Type != hclsyntax.TokenIdent {
		return ""
	}
	if j := past(tokens, i, -1); j < 0 || tokens[j].Type != hclsyntax.TokenTemplateControl {
		return ""
	}
	return string(tokens[i].Bytes)
}

// past is the index of the first token from i, going by step, 1 or -1,
// that is neither a newline nor a comment, past token i itself; -1 when
// there is none before it. LexConfig ends every file with TokenEOF, so
// there is always one after it.
func past(tokens hclsyntax.Tokens, i, step int) int {
	j := i + step
	for j >= 0 && (tokens[j].Type == hclsyntax.TokenNewline || tokens[j].Type == hclsyntax.TokenComment) {
		j += step
	}
	return j
}

// named is whether token i of tokens is the identifier name.
func named(tokens hclsyntax.Tokens, i int, name string) bool {
	return tokens[i].Type == hclsyntax.TokenIdent && string(tokens[i].Bytes) == name
}

// take moves d past a token of type t, which the parser reads as s says.
func (d *depth) take(t hclsyntax.TokenType, s seen) {
	switch {
	case opens[t]:
		d.outer = append(d.outer, d.level)
		d.level = level{lines: s.lines}
		d.now++
	case closes[t]:
		d.close()
	case s.splat:
		d.splat = true
	case chains[t]:
		d.chained(true)
	case t == hclsyntax.TokenComma || s.end && d.lines:
		d.now -= d.chain
		d.chain = 0
	case s.dir != "":
		d.directive(s.dir)
	}
}

// chained counts one more chain token at the current level when it holds.
func (d *depth) chained(it bool) {
	if it {
		d.chain++
		d.now++
	}
}

// close ends the current level, and its chain with it. A level that is a
// full splat's bracket chains one at the level outside it, whatever token
// closes it. A close with no level open leaves d as it is; the parser
// refuses the file.
func (d *depth) close() {
	if len(d.outer) == 0 {
		return
	}
	d.now -= 1 + d.chain
	splat := d.splat
	d.level = d.outer[len(d.outer)-1]
	d.outer = d.outer[:len(d.outer)-1]
	d.chained(splat)
}

// directive counts a level for the body of an %{if} or %{for} at the level
// outside the directive's own, until its %{endif} or %{endfor}. The %{
// before the name has opened a level, so one is open. An end with no
// start is left to the parser, which refuses it.
func (d *depth) directive(name string) {
	outside := &d.outer[len(d.outer)-1].chain
	switch {
	case name == "if" || name == "for":
		*outside++
		d.now++
	case (name == "endif" || name == "endfor") && *outside > 0:
		*outside--
		d.now--
	}
}
