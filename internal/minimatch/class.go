package minimatch

import "slices"

// posixClass is one of minimatch's POSIX classes: its regex source, whether
// that needs the u flag, and whether it is negated.
type posixClass struct {
	name  string
	src   string
	uflag bool
	neg   bool
}

// posixClasses are brace-expressions.js's, in its order.
var posixClasses = []posixClass{
	{"[:alnum:]", `\p{L}\p{Nl}\p{Nd}`, true, false},
	{"[:alpha:]", `\p{L}\p{Nl}`, true, false},
	{"[:ascii:]", `\x00-\x7f`, false, false},
	{"[:blank:]", `\p{Zs}\t`, true, false},
	{"[:cntrl:]", `\p{Cc}`, true, false},
	{"[:digit:]", `\p{Nd}`, true, false},
	{"[:graph:]", `\p{Z}\p{C}`, true, true},
	{"[:lower:]", `\p{Ll}`, true, false},
	{"[:print:]", `\p{C}`, true, false},
	{"[:punct:]", `\p{P}`, true, false},
	{"[:space:]", `\p{Z}\t\r\n\v\f`, true, false},
	{"[:upper:]", `\p{Lu}`, true, false},
	{"[:word:]", `\p{L}\p{Nl}\p{Nd}\p{Pc}`, true, false},
	{"[:xdigit:]", `A-Fa-f0-9`, false, false},
}

// braceEscape escapes [, ], \ and -, for inside a class.
func braceEscape(s units) units {
	var out units
	for _, c := range s {
		if c == '[' || c == ']' || c == '\\' || c == '-' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return out
}

// jsWhitespace is \s of a JavaScript regex.
func jsWhitespace(c rune) bool {
	switch c {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return c >= 0x2000 && c <= 0x200A
}

// regExpEscape escapes every regex magic character, as minimatch's
// regExpEscape does: s.replace(/[-[\]{}()*+?.,\\^$|#\s]/g, '\\$&').
func regExpEscape(s units) units {
	var out units
	for _, c := range s {
		switch {
		case slices.Contains([]rune(`-[]{}()*+?.,\^$|#`), c), jsWhitespace(c):
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return out
}

// parseClass is brace-expressions.js's parseClass: the regex source of the
// class at glob[pos], whether it needs the u flag, the code units it takes
// (0 when it is no class), and whether it is magic.
func parseClass(glob units, pos int) (units, bool, int, bool) {
	r := classReader{glob: glob, pos: pos, i: pos + 1, endPos: pos, rangeStart: -1}
	if r.read() {
		return units(`$.`), false, len(glob) - pos, true
	}
	return r.regex()
}

// classReader is parseClass's state: the class's ranges and negated
// POSIX classes so far, at glob[i], with the end once a ] closes it, and
// a range's start while one is open.
type classReader struct {
	glob                              units
	pos, i, endPos                    int
	ranges, negs                      []units
	sawStart, uflag, escaping, negate bool
	rangeStart                        rune
}

// read reads the class up to the ] that closes it, or the end of the
// glob. It reports true for a POSIX class that ends a range, which
// matches nothing.
func (r *classReader) read() bool {
	for r.i < len(r.glob) {
		done, bad := r.unit(r.glob[r.i])
		if bad {
			return true
		}
		if done {
			break
		}
	}
	return false
}

// unit reads c, at i: a negation first, the ] that closes the class, or
// a member. done is set at the ].
func (r *classReader) unit(c rune) (done, bad bool) {
	if (c == '!' || c == '^') && r.i == r.pos+1 {
		r.negate = true
		r.i++
		return false, false
	}
	if c == ']' && r.sawStart && !r.escaping {
		r.endPos = r.i + 1
		return true, false
	}
	r.sawStart = true
	return false, r.member(c)
}

// member reads c, at i, as part of the class: a backslash that escapes
// the next, a POSIX class, or a character.
func (r *classReader) member(c rune) bool {
	if c == '\\' && !r.escaping {
		r.escaping = true
		r.i++
		return false
	}
	if c == '[' && !r.escaping {
		if took, bad := r.posix(); took || bad {
			return bad
		}
	}
	r.escaping = false
	r.plain(c)
	return false
}

// posix reads the POSIX class that opens at i, if one does. bad is set
// when it ends a range.
func (r *classReader) posix() (took, bad bool) {
	for _, cls := range posixClasses {
		if !hasPrefixAt(r.glob, []rune(cls.name), r.i) {
			continue
		}
		if r.rangeStart >= 0 {
			return true, true
		}
		r.i += len(cls.name)
		if cls.neg {
			r.negs = append(r.negs, []rune(cls.src))
		} else {
			r.ranges = append(r.ranges, []rune(cls.src))
		}
		r.uflag = r.uflag || cls.uflag
		return true, false
	}
	return false, false
}

// plain reads the character c, at i: the end of an open range, the start
// of one, or a member alone. A - before the ] is a member.
func (r *classReader) plain(c rune) {
	if r.rangeStart >= 0 {
		r.endRange(c)
		return
	}
	switch {
	case hasPrefixAt(r.glob, units{'-', ']'}, r.i+1):
		r.ranges = append(r.ranges, braceEscape(units{c, '-'}))
		r.i += 2
	case hasPrefixAt(r.glob, units{'-'}, r.i+1):
		r.rangeStart = c
		r.i += 2
	default:
		r.ranges = append(r.ranges, braceEscape(units{c}))
		r.i++
	}
}

// endRange ends the open range at c. A range backwards holds nothing.
func (r *classReader) endRange(c rune) {
	if c > r.rangeStart {
		r.ranges = append(r.ranges, concat(braceEscape(units{r.rangeStart}), units{'-'}, braceEscape(units{c})))
	} else if c == r.rangeStart {
		r.ranges = append(r.ranges, braceEscape(units{c}))
	}
	r.rangeStart = -1
	r.i++
}

// regex is parseClass's answer once the class is read: none when no ]
// closed it, nothing to match for an empty one, the character for one of
// one character, and else the class.
func (r *classReader) regex() (units, bool, int, bool) {
	if r.endPos < r.i {
		return nil, false, 0, false
	}
	if len(r.ranges) == 0 && len(r.negs) == 0 {
		return units(`$.`), false, len(r.glob) - r.pos, true
	}
	if len(r.negs) == 0 && len(r.ranges) == 1 && !r.negate && oneCharacter(r.ranges[0]) {
		return regExpEscape(unescapedOne(r.ranges[0])), false, r.endPos - r.pos, false
	}
	return r.classSource()
}

// unescapedOne is the one character of r, without the \ before it.
func unescapedOne(r units) units {
	if len(r) == 2 {
		return r[1:]
	}
	return r
}

// classSource is the regex of the ranges, the negated POSIX classes, or
// both as alternatives.
func (r *classReader) classSource() (units, bool, int, bool) {
	sranges := units{'['}
	if r.negate {
		sranges = append(sranges, '^')
	}
	sranges = append(concat(sranges, concat(r.ranges...)), ']')
	snegs := units{'['}
	if !r.negate {
		snegs = append(snegs, '^')
	}
	snegs = append(concat(snegs, concat(r.negs...)), ']')
	switch {
	case len(r.ranges) > 0 && len(r.negs) > 0:
		return concat(units{'('}, sranges, units{'|'}, snegs, units{')'}), r.uflag, r.endPos - r.pos, true
	case len(r.ranges) > 0:
		return sranges, r.uflag, r.endPos - r.pos, true
	}
	return snegs, r.uflag, r.endPos - r.pos, true
}

// oneCharacter is /^\\?.$/.test(r): one character, perhaps after a \,
// that is no line terminator.
func oneCharacter(r units) bool {
	switch len(r) {
	case 1:
		return !lineTerminator(r[0])
	case 2:
		return r[0] == '\\' && !lineTerminator(r[1])
	}
	return false
}
