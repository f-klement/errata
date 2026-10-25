package jenkins

import (
	"bytes"
	"regexp"
)

// literalText is the text a string token holds, and whether errata reads
// it as literal: a string with no $ and no backslash but one of the escapes
// below, and no carriage return, which Groovy may turn into a line end. A
// $ in any form is not literal, even in '...', where Groovy keeps it,
// since errata cannot tell where Jenkins or a step expands it. A slashy or
// dollar slashy string is literal only with no $ and no backslash at all.
func literalText(t token, data []byte) ([]byte, bool) {
	body := t.body(data)
	if bytes.ContainsAny(body, "$\r") {
		return nil, false
	}
	if t.quote == slashy || t.quote == dollarSlashy {
		return body, !bytes.ContainsRune(body, '\\')
	}
	if !bytes.ContainsRune(body, '\\') {
		return body, true
	}
	out := make([]byte, 0, len(body))
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i == len(body) {
			return nil, false
		}
		r, ok := escapes[body[i]]
		if !ok {
			// Octal and \u escapes, and a backslash before a line end,
			// are left to Groovy.
			return nil, false
		}
		out = append(out, r)
	}
	return out, true
}

// escapes are the escapes of a quoted string errata decodes, and what each
// stands for. Groovy 2.4 has no \s, so 'a\sb' is not literal (groovy.g,
// ESC).
var escapes = map[byte]byte{
	'b': '\b', 't': '\t', 'n': '\n', 'f': '\f', 'r': '\r',
	'\\': '\\', '\'': '\'', '"': '"',
}

// plainNumber is a number errata reads as literal: a decimal integer, which
// Groovy writes back as it reads it. 010 is octal, 1_000 is 1000, 1.50 a
// BigDecimal and 1e3 a double, so their text is not literal.
var plainNumber = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// value is the text of a recorded assignment, and whether it is computed:
// a value of one string or number token is what errata decodes of it, and
// any other value is the file's text from its first token to its last,
// marked not literal.
func value(r record, data []byte) (text []byte, computed bool) {
	if r.toks == 1 {
		switch r.val.kind {
		case tokString:
			if text, ok := literalText(r.val, data); ok {
				return text, false
			}
		case tokNumber:
			if plainNumber.Match(r.val.text(data)) {
				return r.val.text(data), false
			}
		default: // a name, an operator: computed
		}
	}
	return data[r.val.start:r.end], true
}
