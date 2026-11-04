package rst

import (
	"bytes"
	"regexp"
	"strings"
)

// inlineSpans hands emit the prose of the lines ls: their text joined by a
// space, with the delimiters of inline markup left out (inline), each piece
// with the line it came from.
func inlineSpans(d *doc, ls []span, emit func(b []byte, line int)) {
	var in []byte
	at := make([]int, 0, len(ls)) // where each line starts in in
	for k, s := range ls {
		if k > 0 {
			in = append(in, ' ')
		}
		at = append(at, len(in))
		in = append(in, d.text(s)...)
	}
	k := 0
	inline(in, func(from, to int) {
		for from < to {
			for k+1 < len(at) && at[k+1] <= from {
				k++
			}
			stop := to
			if k+1 < len(at) {
				stop = min(stop, at[k+1])
			}
			emit(in[from:stop], ls[k].line)
			from = stop
		}
	})
}

// plain is the prose of the lines ls, without the blanks at either end.
func plain(d *doc, ls []span) string {
	var b []byte
	inlineSpans(d, ls, func(p []byte, _ int) { b = append(b, p...) })
	return string(bytes.TrimSpace(b))
}

// inline hands keep, in order, the ranges of in that are its prose: in
// without the delimiters of its inline markup, as docutils recognises
// them. “literal“ keeps its text as written; **strong**, *emphasis* and
// `interpreted text` keep theirs, with a role before or after and the _ or
// __ of a reference left out, and a reference written `text <target>`_
// keeps its text. A backslash escapes the character after it, and goes
// with it when that is a blank. Any other markup, a substitution, a
// footnote or a reference by name_ included, stays as written. A start of
// markup is a delimiter after a blank, the start of in, or one of '"([{<-/:,
// with no blank after it; its end the first delimiter after it with no blank
// or backslash before it, followed by a blank, the end, or punctuation. One
// pass over in: each kind of end is searched for once from each place
// (ends).
func inline(in []byte, keep func(from, to int)) {
	e := ends{in: in}
	from := 0 // the start of the text kept as written
	for p := 0; p < len(in); {
		if in[p] == '\\' {
			keep(from, p)
			from = escape(in, p, len(in))
			p += 2
			continue
		}
		if startsMarkup(in, p) {
			if a, b, next, raw, ok := e.markup(p); ok {
				keep(from, p)
				if raw {
					keep(a, b)
				} else {
					escaped(in, a, b, keep)
				}
				from, p = next, next
				continue
			}
		}
		p++
	}
	keep(from, len(in))
}

// escape is where the text after the backslash at i goes on, b being the
// end of the text: at the character it escapes, or after it when that is a
// blank.
func escape(in []byte, i, b int) int {
	if i+1 < b && isSpace(in[i+1]) {
		return i + 2
	}
	return i + 1
}

// escaped hands keep the ranges of in from a to b that its escapes leave.
func escaped(in []byte, a, b int, keep func(from, to int)) {
	from, skip := a, false
	for i, c := range in[a:b] {
		switch {
		case skip: // the character a backslash escapes
			skip = false
		case c == '\\':
			keep(from, a+i)
			from, skip = escape(in, a+i, b), true
		}
	}
	keep(from, b)
}

// startsMarkup reports whether in may start inline markup at p: a
// delimiter after a blank, the start, or an opening punctuation mark, and
// no blank after it.
func startsMarkup(in []byte, p int) bool {
	if strings.IndexByte("*`:", in[p]) < 0 || p+1 == len(in) || isSpace(in[p+1]) {
		return false
	}
	return p == 0 || isSpace(in[p-1]) || strings.IndexByte(`'"([{<-/:`, in[p-1]) >= 0
}

// ends finds the ends of inline markup in a text, each kind once from
// any place: the end of a kind after a place is the first after it, so a
// later search starts where the one before found its end, and the
// searches of a kind read the text once in all.
type ends struct {
	in                              []byte
	literal, strong, emphasis, text finder
}

// finder is the last search for one kind of end: the end it found, or
// none when it found none, so that none comes after any later place either.
type finder struct {
	at   int
	none bool
}

// next is the first q at or after from where ok holds, and whether there is
// one, searching again only when from is past the end found before. A
// search is only ever asked from a place at or after the one before it.
func (f *finder) next(n, from int, ok func(q int) bool) (int, bool) {
	if !f.none && f.at < from {
		f.at = from
		for f.at < n && !ok(f.at) {
			f.at++
		}
		f.none = f.at >= n // from may be past the end of the text
	}
	return f.at, !f.none
}

// markup reads the inline markup that starts at p, which startsMarkup
// allows: its text runs from a to b, raw when its escapes stay as written,
// and next is the offset after it. ok is false when no markup starts at p.
func (e *ends) markup(p int) (a, b, next int, raw, ok bool) {
	in := e.in
	switch {
	case bytes.HasPrefix(in[p:], []byte("``")):
		if q, ok := e.literal.next(len(in), p+3, func(q int) bool { return e.endsAt(q, "``", 0, false) }); ok && !isSpace(in[p+2]) {
			return p + 2, q, q + 2, true, true
		}
		return 0, 0, 0, false, false
	case bytes.HasPrefix(in[p:], []byte("**")):
		if q, ok := e.strong.next(len(in), p+3, func(q int) bool { return e.endsAt(q, "**", 0, true) }); ok && !isSpace(in[p+2]) {
			return p + 2, q, q + 2, false, true
		}
		fallthrough
	case in[p] == '*':
		if q, ok := e.emphasis.next(len(in), p+2, func(q int) bool { return e.endsAt(q, "*", 0, true) }); ok {
			return p + 1, q, q + 1, false, true
		}
		return 0, 0, 0, false, false
	case in[p] == ':':
		m := rolePrefix.Find(in[p:])
		if m == nil {
			return 0, 0, 0, false, false
		}
		return e.interpreted(p+len(m)-2, true)
	default: // a backquote
		return e.interpreted(p, false)
	}
}

// interpreted reads interpreted text whose ` is at p, with its role or
// reference after it: its text, or, after a role or as a reference, its
// text without an embedded target, <...> at its end.
func (e *ends) interpreted(p int, hasRole bool) (a, b, next int, raw, ok bool) {
	in := e.in
	q, found := e.text.next(len(in), p+2, func(q int) bool { return in[q] == '`' && e.endsAt(q, "`", suffix(in, q+1), true) })
	if !found {
		return 0, 0, 0, false, false
	}
	a, b = p+1, q
	n := suffix(in, q+1)
	if in[b-1] == '>' && (hasRole || n > 0) {
		switch lt := bytes.LastIndexByte(in[a:b], '<'); {
		case lt == 0:
			a, b = a+1, b-1 // `<target>`_ reads as its target
		case lt != -1 && isSpace(in[a+lt-1]):
			b = a + len(bytes.TrimRight(in[a:a+lt], " \t"))
		}
	}
	return a, b, q + 1 + n, false, true
}

// suffix is the length of what may follow the end of interpreted text at
// at: the __ or _ of a reference, or a role, or 0 for none.
func suffix(in []byte, at int) int {
	switch {
	case bytes.HasPrefix(in[at:], []byte("__")):
		return 2
	case bytes.HasPrefix(in[at:], []byte("_")):
		return 1
	}
	return len(role.Find(in[at:]))
}

// role matches a role, :name:, its name letters and digits joined singly
// by - _ . + and :, and rolePrefix one before interpreted text, whose `
// it matches with the character after it, which is no blank and no `.
var (
	role       = regexp.MustCompile(`^:[A-Za-z0-9]+(?:[-_.+:][A-Za-z0-9]+)*:`)
	rolePrefix = regexp.MustCompile("^:[A-Za-z0-9]+(?:[-_.+:][A-Za-z0-9]+)*:`[^`\\s]")
)

// endsAt reports whether the end string end is at q of the text, with n
// bytes after it that belong to the markup, and before it no blank, nor,
// when escapes is set, a backslash; after them the end of the text, a
// blank or punctuation.
func (e *ends) endsAt(q int, end string, n int, escapes bool) bool {
	in := e.in
	if !bytes.HasPrefix(in[q:], []byte(end)) || isSpace(in[q-1]) || (escapes && in[q-1] == '\\') {
		return false
	}
	after := q + len(end) + n
	return after >= len(in) || isSpace(in[after]) || strings.IndexByte(`'")]}>-/:.,;!?\`, in[after]) >= 0
}
