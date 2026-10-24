package shell

import (
	"strings"
	"unicode"

	"mvdan.cc/sh/v3/syntax"
)

// quoting is where a word stands, which decides how the shell reads its
// quotes and backslashes.
type quoting uint8

const (
	unquoted   quoting = iota // an assignment's value, or a default outside quotes
	double                    // a default inside "..."
	heredoc                   // a default in the body of a here-document
	arithmetic                // a default inside $(( )) or (( ))
	pattern                   // a default inside the pattern or replacement of ${Y#p} and the like
)

// literal is the text the shell gives word, standing where q says, and
// whether it is literal: made of text and quotes alone, with no expansion of
// a parameter, a command, arithmetic, a tilde or, in a declaration's
// argument in bash or mksh, braces. It reads only what the shells were seen
// to agree on (TestShellAgreesWithBash and TestPOSIXAgreesWithDash); any
// other form is not literal, so a rule never compares what errata may have
// read wrong.
func (w *walker) literal(word *syntax.Word, q quoting, inDecl bool) (string, bool) {
	if inDecl && w.lang != syntax.LangPOSIX && bracesExpand(word) {
		return "", false
	}
	var b strings.Builder
	if !w.parts(&b, word.Parts, q) {
		return "", false
	}
	return b.String(), true
}

// bracesExpand reports whether a declaration's argument holds a { in its
// text, which bash may brace-expand, so that declare X={a,b} sets b.
// syntax.SplitBraces reports a change for any such word, {a} too, and keeps
// a copy of the parts inside each pair of braces, so its memory is the
// square of the braces (8000 nested took 1 GB); errata does not call it.
func bracesExpand(word *syntax.Word) bool {
	for _, p := range word.Parts {
		if l, ok := p.(*syntax.Lit); ok && strings.Contains(l.Value, "{") {
			return true
		}
	}
	return false
}

// parts writes the text of a word's parts to b, and reports whether every
// part is literal.
func (w *walker) parts(b *strings.Builder, parts []syntax.WordPart, q quoting) bool {
	for i, part := range parts {
		if !w.part(b, part, parts[i+1:], i, q) {
			return false
		}
	}
	return true
}

// part writes the text of one part, the i-th of its word with rest after
// it, and reports whether it is literal.
func (w *walker) part(b *strings.Builder, part syntax.WordPart, rest []syntax.WordPart, i int, q quoting) bool {
	if p, ok := part.(*syntax.Lit); ok {
		return w.lit(b, p.Value, i, q) && !dollarQuote(p.Value, rest)
	}
	if p, ok := part.(*syntax.SglQuoted); ok {
		return w.single(b, p, q)
	}
	if p, ok := part.(*syntax.ParamExp); ok {
		// $Y or ${Y}, which the walk may know the value of. A value past
		// max_bytes could not be returned, so it is not resolved.
		if q != unquoted && q != double || !simpleRef(p) {
			return false
		}
		v, ok := w.resolve(p.Param.Value)
		if !ok || b.Len()+len(v) > w.limit {
			return false
		}
		b.WriteString(v)
		return true
	}
	if p, ok := part.(*syntax.DblQuoted); ok {
		return w.doubleQuoted(b, p, q)
	}
	return false
}

// single writes the text of '...' or $'...'.
func (w *walker) single(b *strings.Builder, p *syntax.SglQuoted, q quoting) bool {
	if q == unquoted && !p.Dollar {
		b.WriteString(p.Value)
		return true
	}
	if q == unquoted && w.lang == syntax.LangBash {
		s, ok := ansiC(p.Value)
		b.WriteString(s)
		return ok
	}
	if q == double && !p.Dollar && w.lang != syntax.LangMirBSDKorn && !strings.ContainsAny(p.Value, "$`\\\"") {
		// Inside "${X:-'v'}" the quotes are text, and what they hold is
		// read as in double quotes: '$x' expands.
		b.WriteString("'" + p.Value + "'")
		return true
	}
	return false
}

// doubleQuoted writes the text of "...".
func (w *walker) doubleQuoted(b *strings.Builder, p *syntax.DblQuoted, q quoting) bool {
	if p.Dollar || q != unquoted && q != double {
		return false
	}
	if q == unquoted {
		return w.parts(b, p.Parts, double)
	}
	// bash reads "${X:-"\a"}" as a, as if the inner quotes ended the outer
	// ones, so inside a default in double quotes only plain text is
	// literal.
	for _, inner := range p.Parts {
		lit, ok := inner.(*syntax.Lit)
		if !ok || strings.Contains(lit.Value, `\`) {
			return false
		}
		b.WriteString(lit.Value)
	}
	return true
}

// simpleRef reports whether a parameter expansion is $Y or ${Y} of a name.
func simpleRef(p *syntax.ParamExp) bool {
	return p.Param != nil && isName(p.Param.Value) && p.Flags == nil && !p.Excl && !p.Length && !p.Width &&
		!p.IsSet && p.NestedParam == nil && p.Index == nil && len(p.Modifiers) == 0 && p.Slice == nil &&
		p.Repl == nil && p.Names == 0 && p.Exp == nil
}

// dollarQuote reports whether a literal part ends in a $ no backslash
// quotes and a quoted part follows it: $'...' or $"...", which posix parses
// as a $ and a string, though dash and POSIX 2024 read them otherwise.
func dollarQuote(v string, rest []syntax.WordPart) bool {
	if len(rest) == 0 || !strings.HasSuffix(v, "$") {
		return false
	}
	_, sgl := rest[0].(*syntax.SglQuoted)
	_, dbl := rest[0].(*syntax.DblQuoted)
	if !sgl && !dbl {
		return false
	}
	before := strings.TrimSuffix(v, "$")
	backslashes := len(before) - len(strings.TrimRight(before, `\`))
	return backslashes%2 == 0
}

// lit writes the text of a literal part, the i-th of its word, to b.
// Unquoted, a backslash quotes the character after it, and a tilde at the
// start of the word or after a colon expands to a home directory, so it is
// not literal. In double quotes a backslash quotes only $ ` " and \. In a
// here-document or arithmetic, a part with a backslash is not literal. A
// backslash that ends the part quotes nothing errata can see, so the part
// is not literal.
func (w *walker) lit(b *strings.Builder, v string, i int, q quoting) bool {
	if q != unquoted && q != double {
		b.WriteString(v)
		return !strings.Contains(v, `\`)
	}
	if q == unquoted && (strings.HasPrefix(v, "~") && (i == 0 || strings.HasSuffix(b.String(), ":")) || strings.Contains(v, ":~")) {
		return false
	}
	for j := 0; j != len(v); j++ {
		if v[j] == '\\' {
			if j+1 == len(v) {
				return false
			}
			if q == unquoted || strings.ContainsAny(v[j+1:j+2], "$`\"\\") {
				j++
			}
		}
		b.WriteByte(v[j])
	}
	return true
}

// ansiC is the text bash gives the inside of $'...': its escapes \a \b \e
// \E \f \n \r \t \v \\ \' \" \?, an octal byte of one to three digits and a
// hex byte of one or two. A byte of zero, which ends the text in bash, and
// every other escape (\c, \u, \U, and the unknown ones, which bash keeps)
// are not literal.
func ansiC(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i != len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i == len(s) {
			return "", false
		}
		c, width, ok := escape(s[i:])
		if !ok {
			return "", false
		}
		i += width - 1
		b.WriteByte(c)
	}
	return b.String(), true
}

// escapes are the one-letter escapes of $'...' and the bytes they stand for.
var escapes = map[byte]byte{
	'a': '\a', 'b': '\b', 'e': 0x1b, 'E': 0x1b, 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v',
	'\\': '\\', '\'': '\'', '"': '"', '?': '?',
}

// escape reads the escape at the start of s, after its backslash: the byte
// it stands for, the bytes it takes, and whether errata reads it.
func escape(s string) (byte, int, bool) {
	if c, ok := escapes[s[0]]; ok {
		return c, 1, true
	}
	n, width := number(s, 3, 8)
	if s[0] == 'x' {
		n, width = number(s[1:], 2, 16)
		width++
	}
	// No digit is a value of 0, as is \0 itself.
	if n < 1 || n > 0xff {
		return 0, 0, false
	}
	return byte(n), width, true
}

// number reads up to most digits of base 8 or 16 at the start of s: their
// value and how many there are.
func number(s string, most, base int) (n, width int) {
	for _, c := range []byte(s[:min(len(s), most)]) {
		d := strings.IndexRune("0123456789abcdef"[:base], unicode.ToLower(rune(c)))
		if d == -1 {
			break
		}
		n = n*base + d
		width++
	}
	return n, width
}
