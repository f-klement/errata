package litparse

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
)

// strings reads the string at tok, written after pre (nil for none), and
// the strings written straight after it in a language that joins them, as
// one string; nil when one of them is computed, which moves past them all.
func (p *parser) strings(pre *core.Prefix) *core.Node {
	t := p.tok
	first, ok := p.str(t, pre)
	p.advance()
	var b strings.Builder // the strings after the first, joined once
	joined := false
run:
	for p.syn.Adjacent {
		var pre *core.Prefix
		switch n := p.peek(); {
		case p.tok.kind == tString:
		case p.tok.kind == tIdent && n.kind == tString && n.glued && p.prefix(p.tok) != nil:
			pre = p.prefix(p.tok)
			p.advance()
		default:
			break run
		}
		more, lit := p.str(p.tok, pre)
		p.advance()
		if !joined {
			b.WriteString(first)
			joined = true
		}
		b.WriteString(more)
		ok = ok && lit
	}
	if !ok {
		return nil
	}
	if joined {
		first = b.String()
	}
	return p.scalar(core.TypeString, first, t.start)
}

// str is the text of the string t, written after pre (nil for none), and
// whether it is literal: a form that computes, a sigil that is no string,
// code inside the body, or an escape errata does not decode make it
// computed. Every line end in the body reads as a line feed.
func (p *parser) str(t token, pre *core.Prefix) (string, bool) {
	s := t.span
	f := p.lang.Forms[s.Form]
	if f.Computed || !s.Closed {
		return "", false
	}
	raw, interp := f.Escape == 0, f.Interp
	if pre != nil {
		raw = raw || pre.Raw
		if pre.Interp != "" {
			interp = pre.Interp
		}
	}
	var body string
	switch {
	case f.Heredoc:
		switch s.Here.Quote {
		case '\'':
			raw, interp = true, ""
		case '`':
			return "", false // a command
		}
		tag := s.End - s.Start - len(f.Open)
		if s.Here.Quote != 0 {
			tag -= 2
		}
		indent := p.src[s.Here.End : s.Here.Close-tag]
		body = lineFeeds(p.src[s.Here.Start:s.Here.End])
		switch f.Dedent {
		case core.DedentCloser:
			body = dedent(body, indent)
		case core.DedentCommon:
			body = dedentCommon(body)
		case core.DedentNone:
		}
	default:
		if f.Tag != "" {
			tag := p.src[s.Start+len(f.Open) : s.BodyStart-len(f.Inner)]
			if len(tag) != 1 || !strings.Contains(p.syn.Sigils, tag) {
				return "", false // a sigil that is no string, such as ~r
			}
		}
		body = lineFeeds(p.src[s.BodyStart:s.BodyEnd])
		if f.TrimFirst {
			body = strings.TrimPrefix(body, "\n")
		}
		if f.Dedent == core.DedentCloser {
			last := strings.LastIndexByte(body, '\n')
			if indent := body[last+1:]; strings.Trim(indent, " \t") == "" {
				body = dedent(body[:last+1], indent)
			}
		}
	}
	if f.TrimLast {
		body = strings.TrimSuffix(body, "\n")
	}
	esc := f.Escape
	if raw {
		esc = 0
	}
	if interp != "" && hasInterp(body, interp, esc) {
		return "", false
	}
	if raw {
		return body, true
	}
	return unescape(body, f, p.syn.Escapes, pre != nil && pre.Bytes)
}

// lineFeeds is s with each CRLF and each lone CR a line feed.
func lineFeeds(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// dedent is body with indent taken off the start of each line that starts
// with it, and a line of blanks alone made empty.
func dedent(body, indent string) string {
	if indent == "" {
		return body
	}
	lines := strings.SplitAfter(body, "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, indent):
			lines[i] = l[len(indent):]
		case strings.Trim(l, " \t\n") == "":
			lines[i] = strings.TrimLeft(l, " \t")
		}
	}
	return strings.Join(lines, "")
}

// dedentCommon is body with the blanks that every line that is not blank
// starts with taken off, as Ruby's <<~ does.
func dedentCommon(body string) string {
	lines := strings.SplitAfter(body, "\n")
	least := -1
	for _, l := range lines {
		if strings.Trim(l, " \t\n") == "" {
			continue
		}
		if n := len(l) - len(strings.TrimLeft(l, " \t")); least < 0 || n < least {
			least = n
		}
	}
	if least <= 0 {
		return body
	}
	for i, l := range lines {
		lines[i] = l[min(least, len(l)-len(strings.TrimLeft(l, " \t"))):]
	}
	return strings.Join(lines, "")
}

// hasInterp reports whether body opens code with marker outside an escape
// esc (0 for none). A $ opens code only before a letter, _ or {.
func hasInterp(body, marker string, esc byte) bool {
	for i := 0; i < len(body); i++ {
		if strings.HasPrefix(body[i:], marker) {
			if marker != "$" {
				return true
			}
			if j := i + 1; j < len(body) && (identStart(body[j]) && body[j] != '$' || body[j] == '{') {
				return true
			}
		}
		if esc != 0 && body[i] == esc {
			i++
		}
	}
	return false
}

// unescape decodes the escapes of body, a string of form f, by known, the
// escapes the language decodes (Syntax.Escapes), and reports whether it
// knows each. Any other escape makes the string computed: a language may
// keep it as written (Python's \d), drop the backslash (JavaScript's \a), or
// refuse it. With QuoteEscapes only \\ and the escaped closer are escapes,
// and any other backslash stays. noUnicode leaves out \u and \U, which a Python
// bytes literal does not decode.
func unescape(body string, f core.SpanForm, known string, noUnicode bool) (string, bool) {
	esc := f.Escape
	if strings.IndexByte(body, esc) < 0 {
		return body, true
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != esc || i+1 == len(body) {
			b.WriteByte(c)
			continue
		}
		n := body[i+1]
		if f.QuoteEscapes {
			if n == esc || f.Close != "" && n == f.Close[0] {
				b.WriteByte(n)
				i++
			} else {
				b.WriteByte(c)
			}
			continue
		}
		i++
		if n != esc && strings.IndexByte(known, n) < 0 && (n != 'u' || strings.IndexByte(known, '{') < 0) ||
			noUnicode && (n == 'u' || n == 'U') {
			return "", false
		}
		if r, ok := simple[n]; ok {
			b.WriteString(r)
			continue
		}
		switch n {
		case '0':
			if i+1 < len(body) && body[i+1] >= '0' && body[i+1] <= '9' {
				return "", false // an octal escape
			}
			b.WriteByte(0)
		case 'z':
			// Lua's \z skips the blanks after it, on its line.
			for i+1 < len(body) && (body[i+1] == ' ' || body[i+1] == '\t') {
				i++
			}
		case 'x':
			// Past ASCII, \x is a character in Python and JavaScript and a
			// byte in Ruby and PHP, so it is read only below 0x80.
			r, k, ok := hexRune(body[i+1:], 2, 2)
			if !ok || r >= 0x80 {
				return "", false
			}
			b.WriteRune(r)
			i += k
		case 'u', 'U':
			r, k, ok := codePoint(body[i+1:], n, known)
			if !ok {
				return "", false
			}
			b.WriteRune(r)
			i += k
		}
	}
	return b.String(), true
}

// simple are the escapes that stand for one fixed text: the control
// characters, the escape byte and the quotes, a space for \s, and nothing
// for a backslash before a line feed, which joins the lines.
var simple = map[byte]string{
	'n': "\n", 't': "\t", 'r': "\r", 'a': "\a", 'b': "\b", 'f': "\f", 'v': "\v", 'e': "\x1b", 's': " ",
	'\\': "\\", '\'': "'", '"': `"`, '`': "`", '$': "$", '#': "#", '/': "/", '\n': "",
}

// codePoint reads the code point of a \u or \U escape (n) at the start of s:
// four digits after \u where known holds u, one to six in braces after \u
// where it holds {, and eight after \U. It returns the bytes it read.
func codePoint(s string, n byte, known string) (rune, int, bool) {
	if n == 'u' && strings.HasPrefix(s, "{") {
		end := strings.IndexByte(s, '}')
		r, k, ok := hexRune(s[1:], 1, 6)
		if strings.IndexByte(known, '{') < 0 || end < 0 || !ok || k != end-1 {
			return 0, 0, false
		}
		return r, end + 1, true
	}
	if n == 'u' && strings.IndexByte(known, 'u') < 0 {
		return 0, 0, false // Swift writes \u only with braces
	}
	digits := 4
	if n == 'U' {
		digits = 8
	}
	r, k, ok := hexRune(s, digits, digits)
	if !ok || !utf8.ValidRune(r) {
		return 0, 0, false
	}
	return r, k, true
}

// hexRune reads least to most hex digits at the start of s.
func hexRune(s string, least, most int) (rune, int, bool) {
	k := 0
	for k < len(s) && k < most && strings.IndexByte("0123456789abcdefABCDEF", s[k]) >= 0 {
		k++
	}
	if k < least {
		return 0, 0, false
	}
	n, err := strconv.ParseUint(s[:k], 16, 32)
	if err != nil || n > utf8.MaxRune {
		return 0, 0, false
	}
	return rune(n), k, true
}
