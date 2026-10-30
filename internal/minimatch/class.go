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
	var ranges, negs []units
	i := pos + 1
	sawStart, uflag, escaping, negate := false, false, false, false
	endPos := pos
	rangeStart := rune(-1)
loop:
	for i < len(glob) {
		c := glob[i]
		if (c == '!' || c == '^') && i == pos+1 {
			negate = true
			i++
			continue
		}
		if c == ']' && sawStart && !escaping {
			endPos = i + 1
			break
		}
		sawStart = true
		if c == '\\' && !escaping {
			escaping = true
			i++
			continue
		}
		if c == '[' && !escaping {
			for _, cls := range posixClasses {
				if hasPrefixAt(glob, []rune(cls.name), i) {
					if rangeStart >= 0 {
						return units(`$.`), false, len(glob) - pos, true
					}
					i += len(cls.name)
					if cls.neg {
						negs = append(negs, []rune(cls.src))
					} else {
						ranges = append(ranges, []rune(cls.src))
					}
					uflag = uflag || cls.uflag
					continue loop
				}
			}
		}
		escaping = false
		if rangeStart >= 0 {
			if c > rangeStart {
				ranges = append(ranges, concat(braceEscape(units{rangeStart}), units{'-'}, braceEscape(units{c})))
			} else if c == rangeStart {
				ranges = append(ranges, braceEscape(units{c}))
			}
			rangeStart = -1
			i++
			continue
		}
		if hasPrefixAt(glob, units{'-', ']'}, i+1) {
			ranges = append(ranges, braceEscape(units{c, '-'}))
			i += 2
			continue
		}
		if hasPrefixAt(glob, units{'-'}, i+1) {
			rangeStart = c
			i += 2
			continue
		}
		ranges = append(ranges, braceEscape(units{c}))
		i++
	}
	if endPos < i {
		return nil, false, 0, false
	}
	if len(ranges) == 0 && len(negs) == 0 {
		return units(`$.`), false, len(glob) - pos, true
	}
	if len(negs) == 0 && len(ranges) == 1 && !negate && oneCharacter(ranges[0]) {
		r := ranges[0]
		if len(r) == 2 {
			r = r[1:]
		}
		return regExpEscape(r), false, endPos - pos, false
	}
	sranges := units{'['}
	if negate {
		sranges = append(sranges, '^')
	}
	sranges = append(concat(sranges, concat(ranges...)), ']')
	snegs := units{'['}
	if !negate {
		snegs = append(snegs, '^')
	}
	snegs = append(concat(snegs, concat(negs...)), ']')
	switch {
	case len(ranges) > 0 && len(negs) > 0:
		return concat(units{'('}, sranges, units{'|'}, snegs, units{')'}), uflag, endPos - pos, true
	case len(ranges) > 0:
		return sranges, uflag, endPos - pos, true
	}
	return snegs, uflag, endPos - pos, true
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
