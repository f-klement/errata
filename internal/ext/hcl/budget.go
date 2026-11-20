package hcl

import "github.com/hashicorp/hcl/v2/hclsyntax"

// bytesPerToken is k: a file may make at most max_bytes/k tokens, as
// tokens counts them, before it is lexed. See docs/reference.md, hcl,
// for the measurement it comes from (owner, 2026-11-20).
const bytesPerToken = 16

// maxNumber is the most bytes a number may take. The parser reads a
// number into a big.Float, which builds its mantissa a word at a time, so
// its time grows with the square of the number's length: 3.2 MB of digits
// took 13.9 s, and count 6 tokens. Real numbers are short, and 1000
// digits parse in microseconds.
const maxNumber = 1000

// longAt is the byte offset of the first of tokens that is a number
// longer than maxNumber bytes, and whether there is one.
func longAt(tokens hclsyntax.Tokens) (int, bool) {
	for _, tok := range tokens {
		if tok.Type == hclsyntax.TokenNumberLit && len(tok.Bytes) > maxNumber {
			return tok.Range.Start.Byte, true
		}
	}
	return 0, false
}

// joinShift sets what a template may cost: max_bytes << joinShift, 2^32
// at the default 32 MiB, as the owner's rule derives a plugin's budgets
// from max_bytes (2026-11-20). The parser joins each literal part of a
// template into the literal before it when no ${ or %{ comes between,
// and for each join it copies the text joined so far and every part of
// the template after it, so its time has two terms: joins times parts,
// and over each run of adjacent literals, the run's parts times its
// bytes. See docs/reference.md, hcl, for the measurements.
const joinShift = 7

// bytesPerMove weighs a run's parts times bytes against joins times
// parts: one for every bytesPerMove of them. Measured, the parser copies
// about 31 bytes of joined text in the time it moves one part past an
// interpolation, and 16 gives the slowest template at the limit about the
// same time whichever term it spends (docs/reference.md, hcl).
const bytesPerMove = 16

// template is one template open at a token: the offset of its quote or
// heredoc; its parts so far, each literal and each ${ and %{ opened in
// it; its joins, each literal that follows a literal; the parts times
// bytes of each run of adjacent literals it has ended; and the literals
// of the run open now and their bytes. A ${ ends a run and a %{ does
// not: the parser drops a directive it cannot read, such as %{z}, and
// still joins the literals either side of it, so a run counts as going
// on across every %{. That overcounts a valid directive, which does end
// the run, by a join for the literal after it.
type template struct {
	start, parts, joins, runs, run, runBytes int64
}

// add counts tok into t, and reports whether tok is a part of t.
func (t *template) add(tok hclsyntax.Token) bool {
	switch tok.Type {
	case hclsyntax.TokenQuotedLit, hclsyntax.TokenStringLit:
		if t.run > 0 {
			t.joins++
		}
		t.run++
		t.runBytes += int64(len(tok.Bytes))
	case hclsyntax.TokenTemplateInterp:
		t.runs += t.run * t.runBytes
		t.run, t.runBytes = 0, 0
	case hclsyntax.TokenTemplateControl:
		// A part that leaves the run open: see template.
	default:
		return false
	}
	t.parts++
	return true
}

// cost is what joining t's parts costs so far, in parts moved.
func (t *template) cost() int64 {
	return t.joins*t.parts + (t.runs+t.run*t.runBytes)/bytesPerMove
}

// joinAt is the byte offset of the quote or heredoc that opens the first
// template among tokens whose cost passes limit, and whether there is
// one. A template inside another's interpolation is a template of its
// own, as the parser reads it. The first of open stands for the file
// outside any template, which holds no part, and is never closed.
func joinAt(tokens hclsyntax.Tokens, limit int64) (int, bool) {
	open := []template{{}}
	for _, tok := range tokens {
		switch tok.Type {
		case hclsyntax.TokenOQuote, hclsyntax.TokenOHeredoc:
			open = append(open, template{start: int64(tok.Range.Start.Byte)})
		case hclsyntax.TokenCQuote, hclsyntax.TokenCHeredoc:
			open = open[:max(len(open)-1, 1)]
		default:
			if t := &open[len(open)-1]; t.add(tok) && t.cost() > limit {
				return int(t.start), true
			}
		}
	}
	return 0, false
}

// The kinds of byte tokens tells apart.
const (
	other = iota
	letter
	digit
	blank
)

// tokens is an upper bound on the tokens hclsyntax.LexConfig makes of
// data, counted in one pass over its bytes, so a file too dense for its
// budget is refused before the lexer holds about 100 bytes for each of
// its tokens and the parser more. A run of letters, digits and _ counts
// once, and again where a letter follows a digit, since a number ends
// there; a run of spaces and tabs counts once; any other byte counts on
// its own, a carriage return too, which the lexer takes alone as an
// invalid token; and the end of the file counts once. hclsyntax makes no
// token without a byte of its own but the last, and none across two of
// these runs: an identifier or a number is a run or more, and a literal,
// a comment or a heredoc line holds at least one counted byte.
func tokens(data []byte) int {
	n := 1
	prev := other
	for _, b := range data {
		c := kind(b)
		if c == other || c != prev && (prev != letter || c != digit) {
			n++
		}
		prev = c
	}
	return n
}

// kind is the kind of b to tokens.
func kind(b byte) int {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b == '_':
		return letter
	case b >= '0' && b <= '9':
		return digit
	case b == ' ', b == '\t':
		return blank
	}
	return other
}
