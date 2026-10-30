package minimatch

import (
	"slices"
	"sync"
	"unicode"
	"unicode/utf16"
)

// JavaScript's strings are UTF-16, and minimatch reads them a code unit at a
// time: its length, its charAt and a regex without the u flag all count code
// units. The port keeps every string as a slice of its code units, one rune
// each, so that a surrogate is a unit of its own as in JavaScript.
type units = []rune

// toUnits is s as JavaScript holds it, its UTF-16 code units.
func toUnits(s string) units {
	enc := utf16.Encode([]rune(s))
	out := make(units, len(enc))
	for i, u := range enc {
		out[i] = rune(u)
	}
	return out
}

// fromUnits is the Go string of code units, a lone surrogate as U+FFFD.
func fromUnits(u units) string {
	enc := make([]uint16, len(u))
	for i, r := range u {
		enc[i] = uint16(r) //nolint:gosec // a code unit, at most 0xFFFF
	}
	return string(utf16.Decode(enc))
}

// codePoints decodes u into code points, a lone surrogate as itself.
func codePoints(u units) []rune {
	var out []rune
	for i := 0; i < len(u); i++ {
		r := u[i]
		if utf16.IsSurrogate(r) && r < 0xDC00 && i+1 < len(u) && u[i+1] >= 0xDC00 && u[i+1] <= 0xDFFF {
			r = utf16.DecodeRune(r, u[i+1])
			i++
		}
		out = append(out, r)
	}
	return out
}

// encodeRune appends the UTF-16 units of r, a surrogate as itself.
func encodeRune(dst units, r rune) units {
	if r >= 0x10000 {
		a, b := utf16.EncodeRune(r)
		return append(dst, a, b)
	}
	return append(dst, r)
}

// jsUpperOf is String.prototype.toUpperCase of the one code point r, as Node
// has it (unicode_node.go holds where Go's tables differ).
func jsUpperOf(r rune) units {
	if e, ok := upperExceptions[r]; ok {
		out := make(units, len(e))
		for i, u := range e {
			out[i] = rune(u)
		}
		return out
	}
	if utf16.IsSurrogate(r) {
		return units{r}
	}
	return encodeRune(nil, unicode.ToUpper(r))
}

// jsLowerOf is String.prototype.toLowerCase of the one code point r, apart
// from the final sigma, which jsLower decides by the letters around it.
func jsLowerOf(r rune) units {
	if e, ok := lowerExceptions[r]; ok {
		out := make(units, len(e))
		for i, u := range e {
			out[i] = rune(u)
		}
		return out
	}
	if utf16.IsSurrogate(r) {
		return units{r}
	}
	return encodeRune(nil, unicode.ToLower(r))
}

// jsUpper is String.prototype.toUpperCase.
func jsUpper(u units) units {
	var out units
	for _, r := range codePoints(u) {
		out = append(out, jsUpperOf(r)...)
	}
	return out
}

// jsLower is String.prototype.toLowerCase, with Unicode's final sigma: a
// capital sigma after a cased letter and not before one is a final sigma.
func jsLower(u units) units {
	cps := codePoints(u)
	var out units
	for i, r := range cps {
		if r == 'Σ' && finalSigma(cps, i) {
			out = append(out, 'ς')
			continue
		}
		out = append(out, jsLowerOf(r)...)
	}
	return out
}

// finalSigma is Unicode's Final_Sigma condition at cps[i]: a cased letter
// before it and none after it, skipping case ignorable characters.
func finalSigma(cps []rune, i int) bool {
	before := false
	for j := i - 1; j >= 0; j-- {
		if caseIgnorable(cps[j]) {
			continue
		}
		before = cased(cps[j])
		break
	}
	if !before {
		return false
	}
	for j := i + 1; j < len(cps); j++ {
		if caseIgnorable(cps[j]) {
			continue
		}
		return !cased(cps[j])
	}
	return true
}

// cased is Unicode's Cased property.
func cased(r rune) bool {
	return unicode.In(r, unicode.Lu, unicode.Ll, unicode.Lt, unicode.Other_Lowercase, unicode.Other_Uppercase)
}

// caseIgnorable is Unicode's Case_Ignorable property: the marks, format
// characters, modifiers, and the word break MidLetter, MidNumLet and
// Single_Quote characters.
func caseIgnorable(r rune) bool {
	switch r {
	case '\'', '.', ':', '·', 0x0387, 0x055F, 0x05F4, 0x2018, 0x2019, 0x2024, 0x2027, 0xFE13, 0xFE52, 0xFE55, 0xFF07, 0xFF0E, 0xFF1A:
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}

// canonical is the ECMAScript Canonicalize of a code unit for a regex with
// the i flag and without u: its upper case when that is one code unit, but
// never a character past ASCII folded into ASCII.
func canonical(u rune) rune {
	up := jsUpperOf(u)
	if len(up) != 1 {
		return u
	}
	if u >= 128 && up[0] < 128 {
		return u
	}
	return up[0]
}

// sameCanonical holds, for each canonical code unit, the code units that
// canonicalize to it, built once.
var sameCanonical = sync.OnceValue(func() map[rune]units {
	m := map[rune]units{}
	for u := rune(0); u <= 0xFFFF; u++ {
		if c := canonical(u); c != u {
			m[c] = append(m[c], u)
		}
	}
	return m
})

// equivalentUnits are the code units a regex with the i flag and without u
// matches where its pattern holds u.
func equivalentUnits(u rune) units {
	c := canonical(u)
	out := units{c}
	for _, x := range sameCanonical()[c] {
		if x != c {
			out = append(out, x)
		}
	}
	return out
}

// equivalentPoints are the code points a regex with the i and u flags
// matches where its pattern holds r: r's simple case folding class, as Go's
// SimpleFold orbits hold it, but none for a code point Node folds with none.
func equivalentPoints(r rune) []rune {
	out := []rune{r}
	if caseless[r] {
		return out
	}
	for x := unicode.SimpleFold(r); x != r; x = unicode.SimpleFold(x) {
		if !caseless[x] {
			out = append(out, x)
		}
	}
	return out
}

// hasProperty is whether r has the general category name, as Node's \p{..}
// reads it. C, Other, holds the unassigned code points too, which Go's
// unicode.C leaves out.
func hasProperty(name string, r rune) bool {
	var in bool
	switch name {
	case "C":
		in = unicode.Is(unicode.C, r) || !assigned(r)
	default:
		in = unicode.Is(unicode.Categories[name], r)
	}
	if slices.ContainsFunc(propertyFlips[name], func(span [2]rune) bool { return r >= span[0] && r <= span[1] }) {
		in = !in
	}
	return in
}

// assigned is whether r has a general category other than Cn.
func assigned(r rune) bool {
	return unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.C)
}
