package asciidoc

import (
	"slices"
	"strings"
)

// marks reads the prose of AsciiDoc text; drop is its scratch space.
type marks struct {
	drop []bool
}

// formatting are the characters whose pairs mark text bold, italic,
// monospace and highlighted.
const formatting = "*_`#"

// appendProse appends src to dst without its formatting marks: a pair of
// *, _, ` or # around text, as Asciidoctor reads one. A constrained pair,
// *bold*, opens after a start or a character that is no word's and before
// one that is no blank, and closes after one that is no blank and before an
// end or a character that is no word's; an unconstrained pair, **b**old,
// opens and closes anywhere. A mark after a backslash is the character, and
// the backslash is left out. A mark that pairs with none is the character.
// One pass over src, holding the latest opening mark of each kind.
func (m *marks) appendProse(dst, src []byte) []byte {
	from := len(dst)
	dst = append(dst, src...)
	b := dst[from:]
	m.drop = slices.Grow(m.drop[:0], len(b))[:len(b)]
	clear(m.drop)
	var single, double [len(formatting)]int
	for k := range single {
		single[k], double[k] = -1, -1
	}
	for i := 0; i < len(b); i++ {
		if b[i] == '\\' && i+1 < len(b) && strings.IndexByte(formatting, b[i+1]) >= 0 {
			m.drop[i] = true
			i++
			continue
		}
		k := strings.IndexByte(formatting, b[i])
		if k < 0 {
			continue
		}
		if i+1 < len(b) && b[i+1] == b[i] {
			if o := double[k]; o >= 0 && i > o+2 {
				m.drop[o], m.drop[o+1], m.drop[i], m.drop[i+1] = true, true, true, true
				double[k] = -1
			} else {
				double[k] = i
			}
			i++
			continue
		}
		// a mark right after the one that opens is a doubled one, above
		if o := single[k]; o >= 0 && !isBlank(b[i-1]) && (i+1 == len(b) || !isWord(b[i+1])) {
			m.drop[o], m.drop[i] = true, true
			single[k] = -1
			continue
		}
		if (i == 0 || !isWord(b[i-1])) && i+1 < len(b) && !isBlank(b[i+1]) {
			single[k] = i
		}
	}
	j := from
	for i, c := range b {
		if !m.drop[i] {
			dst[j] = c
			j++
		}
	}
	return dst[:j]
}

// attrs is a block's attribute line: its positional attributes, the first
// one's style, its named attributes and its options.
type attrs struct {
	style string
	pos   []string
	named map[string]string
	opts  map[string]bool
}

// attributeLine reads a line that is a block's attribute list, [source,sh],
// or a block anchor, [[id]], for which it returns nil. ok is false for any
// other line.
func attributeLine(trim []byte) (a *attrs, ok bool) {
	if len(trim) < 2 || trim[0] != '[' || trim[len(trim)-1] != ']' {
		return nil, false
	}
	inner := string(trim[1 : len(trim)-1])
	switch {
	case inner == "":
		return newAttrs(), true
	case inner[0] == '[':
		return nil, true
	case !isWord(inner[0]) && strings.IndexByte(".#%,\"'{", inner[0]) < 0:
		return nil, false
	}
	return parseAttrs(inner), true
}

// newAttrs is an empty attribute list.
func newAttrs() *attrs {
	return &attrs{named: map[string]string{}, opts: map[string]bool{}}
}

// parseAttrs reads the entries of an attribute list, split at commas
// outside quotes: name=value for a named one, its value's quotes left out,
// and otherwise a positional one. A double or a single quote opens a
// quoted value only where a value starts, as in Asciidoctor, so an
// apostrophe in a word quotes nothing. The first positional one is a
// style, then #id, .role and %option shorthands; options also come from
// the named options or opts, a comma separated list.
func parseAttrs(s string) *attrs {
	a := newAttrs()
	var quote byte
	from, start := 0, true // start: a quote here opens a value
	for i := 0; i <= len(s); i++ {
		if i < len(s) {
			switch c := s[i]; {
			case quote != 0:
				if c == quote {
					quote = 0
				}
				continue
			case start && (c == '"' || c == '\''):
				quote, start = c, false
				continue
			case c == ' ' || c == '\t':
				continue
			case c == '=':
				start = true
				continue
			case c != ',':
				start = false
				continue
			}
		}
		a.entry(strings.TrimSpace(s[from:i]))
		from, start = i+1, true
	}
	return a
}

// merge adds b, the attribute line after a's, to a, as Asciidoctor merges
// the attribute lines of one block: b's named attributes and options, and
// each positional one b has, where its first holds a style.
func (a *attrs) merge(b *attrs) {
	for k, v := range b.named {
		a.named[k] = v
	}
	for k := range b.opts {
		a.opts[k] = true
	}
	for i, v := range b.pos {
		if i == 0 && b.style == "" { // [#id] or [,sh] keeps the style
			continue
		}
		for len(a.pos) <= i {
			a.pos = append(a.pos, "")
		}
		a.pos[i] = v
		if i == 0 {
			a.style = b.style
		}
	}
}

// entry reads one entry of an attribute list.
func (a *attrs) entry(e string) {
	if name, value, ok := strings.Cut(e, "="); ok && isName(strings.TrimSpace(name)) {
		name, value = strings.TrimSpace(name), unquote(strings.TrimSpace(value))
		a.named[name] = value
		if name == "options" || name == "opts" {
			for _, o := range strings.Split(value, ",") {
				a.opts[strings.TrimSpace(o)] = true
			}
		}
		return
	}
	e = unquote(e)
	if len(a.pos) == 0 {
		end := strings.IndexAny(e, "#.%")
		if end < 0 {
			end = len(e)
		}
		a.style = e[:end]
		for _, short := range strings.Split(e[end:], "%")[1:] {
			if o, _, _ := strings.Cut(short, "#"); o != "" {
				o, _, _ = strings.Cut(o, ".")
				a.opts[o] = true
			}
		}
	}
	a.pos = append(a.pos, e)
}

// isName reports whether s is an attribute's name: word characters and
// hyphens.
func isName(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if !isWord(s[i]) && s[i] != '-' {
			return false
		}
	}
	return true
}

// unquote is s without the double or single quotes around it.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
