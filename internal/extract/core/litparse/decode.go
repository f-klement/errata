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
	for p.syn.Adjacent {
		more, lit, found := p.adjacent()
		if !found {
			break
		}
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

// adjacent reads the string at tok, with a prefix before it or not, and
// moves past it: its text and whether it is literal; found is false when
// no string stands there.
func (p *parser) adjacent() (text string, lit, found bool) {
	var pre *core.Prefix
	switch n := p.peek(); {
	case p.tok.kind == tString:
	case p.prefixedAt(n):
		pre = p.prefix(p.tok)
		p.advance()
	default:
		return "", false, false
	}
	text, lit = p.str(p.tok, pre)
	p.advance()
	return text, lit, true
}

// prefixedAt reports whether tok is a word of the row's Prefixes that n, a
// string, follows glued.
func (p *parser) prefixedAt(n token) bool {
	return p.tok.kind == tIdent && n.kind == tString && n.glued && p.prefix(p.tok) != nil
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
	raw, interp := decoding(f, s, pre)
	body, ok := p.strBody(f, s)
	if !ok {
		return "", false
	}
	if f.TrimLast {
		body = strings.TrimSuffix(body, "\n")
	}
	return p.decode(body, f, pre, raw, interp)
}

// decoding is how the string s of form f, written after pre, decodes: raw
// when no escape is decoded, and the marker that opens code in it, "" for
// none. A heredoc whose tag is in single quotes is raw text.
func decoding(f core.SpanForm, s core.Span, pre *core.Prefix) (raw bool, interp string) {
	raw, interp = f.Escape == 0, f.Interp
	if pre != nil {
		raw = raw || pre.Raw
		if pre.Interp != "" {
			interp = pre.Interp
		}
	}
	if f.Heredoc && s.Here.Quote == '\'' {
		return true, ""
	}
	return raw, interp
}

// strBody is the body of s, a string of form f, with its line ends line
// feeds and the indentation f takes off taken off; false when s is no
// string.
func (p *parser) strBody(f core.SpanForm, s core.Span) (string, bool) {
	if f.Heredoc {
		return p.hereBody(f, s)
	}
	return p.spanBody(f, s)
}

// hereBody is the body of s, a heredoc of form f; false for one whose tag
// is in backquotes, a command.
func (p *parser) hereBody(f core.SpanForm, s core.Span) (string, bool) {
	if s.Here.Quote == '`' {
		return "", false // a command
	}
	tag := s.End - s.Start - len(f.Open)
	if s.Here.Quote != 0 {
		tag -= 2
	}
	indent := p.src[s.Here.End : s.Here.Close-tag]
	body := lineFeeds(p.src[s.Here.Start:s.Here.End])
	switch f.Dedent {
	case core.DedentCloser:
		body = dedent(body, indent)
	case core.DedentCommon:
		body = dedentCommon(body)
	case core.DedentNone:
	}
	return body, true
}

// spanBody is the body of s, a string of form f other than a heredoc;
// false for a sigil that is no string, such as ~r.
func (p *parser) spanBody(f core.SpanForm, s core.Span) (string, bool) {
	if f.Tag != "" {
		tag := p.src[s.Start+len(f.Open) : s.BodyStart-len(f.Inner)]
		if len(tag) != 1 || !strings.Contains(p.syn.Sigils, tag) {
			return "", false
		}
	}
	body := lineFeeds(p.src[s.BodyStart:s.BodyEnd])
	if f.TrimFirst {
		body = strings.TrimPrefix(body, "\n")
	}
	if f.Dedent == core.DedentCloser {
		last := strings.LastIndexByte(body, '\n')
		if indent := body[last+1:]; strings.Trim(indent, " \t") == "" {
			body = dedent(body[:last+1], indent)
		}
	}
	return body, true
}

// decode is body, a string of form f written after pre, decoded: as it
// stands when raw, and computed when it opens code with interp.
func (p *parser) decode(body string, f core.SpanForm, pre *core.Prefix, raw bool, interp string) (string, bool) {
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
// esc (0 for none).
func hasInterp(body, marker string, esc byte) bool {
	for i := 0; i < len(body); i++ {
		if strings.HasPrefix(body[i:], marker) && opensCode(body, i, marker) {
			return true
		}
		if esc != 0 && body[i] == esc {
			i++
		}
	}
	return false
}

// opensCode reports whether marker, at i in body, opens code: a $ does so
// only before a letter, _ or {, and any other marker always.
func opensCode(body string, i int, marker string) bool {
	j := i + 1
	return marker != "$" || j < len(body) && (identStart(body[j]) && body[j] != '$' || body[j] == '{')
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
	u := unescaper{body: body, f: f, known: known, noUnicode: noUnicode}
	for i := 0; i < len(body); i++ {
		if c := body[i]; c != esc || i+1 == len(body) {
			u.b.WriteByte(c)
			continue
		}
		k, ok := u.escape(i)
		if !ok {
			return "", false
		}
		i = k
	}
	return u.b.String(), true
}

// unescaper is what unescape reads and writes: the text decoded so far,
// the body, its form, the escapes the language knows, and whether \u and
// \U are left out.
type unescaper struct {
	b         strings.Builder
	body      string
	f         core.SpanForm
	known     string
	noUnicode bool
}

// escape decodes the escape at i, before the body's last byte, and returns
// the last byte it read; false for one the language does not decode.
func (u *unescaper) escape(i int) (int, bool) {
	n := u.body[i+1]
	if u.f.QuoteEscapes {
		return u.quoteEscape(i, n), true
	}
	i++
	if u.refuses(n) {
		return 0, false
	}
	if r, ok := simple[n]; ok {
		u.b.WriteString(r)
		return i, true
	}
	return u.special(i, n)
}

// quoteEscape decodes the escape at i, followed by n, of a QuoteEscapes
// form: an escaped escape or closer is that byte, and any other backslash
// stays. It returns the last byte it read.
func (u *unescaper) quoteEscape(i int, n byte) int {
	if n == u.f.Escape || u.f.Close != "" && n == u.f.Close[0] {
		u.b.WriteByte(n)
		return i + 1
	}
	u.b.WriteByte(u.body[i])
	return i
}

// refuses reports whether the escape \n is one the language does not
// decode: not the escape itself, not one of known, nor a \u in braces
// where known holds {; and with noUnicode any \u or \U.
func (u *unescaper) refuses(n byte) bool {
	return n != u.f.Escape && strings.IndexByte(u.known, n) < 0 && (n != 'u' || strings.IndexByte(u.known, '{') < 0) ||
		u.noUnicode && (n == 'u' || n == 'U')
}

// special decodes the escape n at i, the escape's letter, that stands for
// no fixed text: \0, Lua's \z, \x and \u or \U. It returns the last byte
// it read; false for one it does not decode.
func (u *unescaper) special(i int, n byte) (int, bool) {
	switch n {
	case '0':
		return u.nul(i)
	case 'z':
		return u.blanks(i), true
	case 'x':
		return u.hexByte(i)
	case 'u', 'U':
		return u.codePointEscape(i, n)
	}
	return i, true
}

// nul decodes \0 at i, its 0; a digit after it makes an octal escape,
// which is not decoded.
func (u *unescaper) nul(i int) (int, bool) {
	if i+1 < len(u.body) && u.body[i+1] >= '0' && u.body[i+1] <= '9' {
		return 0, false // an octal escape
	}
	u.b.WriteByte(0)
	return i, true
}

// blanks decodes Lua's \z at i, which skips the blanks after it on its
// line, and returns the last of them.
func (u *unescaper) blanks(i int) int {
	for i+1 < len(u.body) && (u.body[i+1] == ' ' || u.body[i+1] == '\t') {
		i++
	}
	return i
}

// hexByte decodes \x at i. Past ASCII, \x is a character in Python and
// JavaScript and a byte in Ruby and PHP, so it is read only below 0x80.
func (u *unescaper) hexByte(i int) (int, bool) {
	r, k, ok := hexRune(u.body[i+1:], 2, 2)
	if !ok || r >= 0x80 {
		return 0, false
	}
	u.b.WriteRune(r)
	return i + k, true
}

// codePointEscape decodes the \u or \U (n) at i.
func (u *unescaper) codePointEscape(i int, n byte) (int, bool) {
	r, k, ok := codePoint(u.body[i+1:], n, u.known)
	if !ok {
		return 0, false
	}
	u.b.WriteRune(r)
	return i + k, true
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
		return bracedPoint(s, known)
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

// bracedPoint reads the code point of a \u{...} escape at the start of s,
// one to six digits in braces, where known holds {.
func bracedPoint(s, known string) (rune, int, bool) {
	end := strings.IndexByte(s, '}')
	r, k, ok := hexRune(s[1:], 1, 6)
	if strings.IndexByte(known, '{') < 0 || end < 0 || !ok || k != end-1 {
		return 0, 0, false
	}
	return r, end + 1, true
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
