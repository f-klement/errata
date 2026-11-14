package htmlparse

import "strings"

// doctypeQuirks reports whether a page whose first doctype holds data, the
// text after <!DOCTYPE, is in a browser's quirks mode, as
// golang.org/x/net/html's parseDoctype reads it (doctype.go, v0.62.0, whose
// list quirkyIDs copies): a name other than html, a public identifier on
// the legacy list, an HTML 4.01 frameset or transitional one with no system
// identifier, IBM's XHTML system identifier, or anything after the
// identifiers. The name is compared in any case, as the standard's
// tokenizer lowercases it, where x/net reads <!DOCTYPE HTML> as quirky.
//
// Elsewhere it keeps x/net's reading where that parts from the HTML
// standard, since the tests hold the scan to x/net's Parse: the legacy
// identifier the standard writes -/W3C/DTD HTML 4.0 Transitional/EN is
// x/net's -/w3d/... here; text after a system identifier makes the page
// quirky, where the standard ignores it; and so does a public identifier
// with no system identifier after it, where the standard decides by the
// identifier alone. Here quirks mode changes only whether a table opens
// inside a p (nest.start).
func doctypeQuirks(s string) bool {
	name, rest := doctypeName(s)
	quirks := !strings.EqualFold(name, "html") // the standard's tokenizer lowercases the name; x/net's does not
	if len(rest) < 6 {
		return quirks || rest != ""
	}
	// Whole identifiers are at least one: doctypeIDs reads one before its
	// keyword can be "".
	keys, ids, whole := doctypeIDs(rest)
	if !whole {
		return true
	}
	return quirks || quirkyPublic(keys, ids) || quirkySystem(keys, ids)
}

// doctypeWhitespace is the whitespace a doctype's parts are apart by.
const doctypeWhitespace = " \t\r\n\f"

// doctypeName splits a doctype's data into its name and the rest, with
// the whitespace before the rest trimmed.
func doctypeName(s string) (string, string) {
	space := strings.IndexAny(s, doctypeWhitespace)
	if space == -1 {
		space = len(s)
	}
	return s[:space], strings.TrimLeft(s[space:], doctypeWhitespace)
}

// doctypeIDs reads the identifiers after a doctype's name, s: a public
// identifier, a system identifier after it, or a system identifier alone,
// each quoted. It is the keyword of each and the identifiers, and whether
// they are all of s: false for a keyword with no quoted identifier, and
// for anything after them.
func doctypeIDs(s string) ([]string, []string, bool) {
	key := strings.ToLower(s[:6])
	s = s[6:]
	var keys, ids []string
	for key == "public" || key == "system" {
		id, rest, ok := quotedID(s)
		if !ok {
			return nil, nil, false
		}
		s = rest
		keys, ids = append(keys, key), append(ids, id)
		key = ""
		if keys[len(keys)-1] == "public" {
			key = "system"
		}
	}
	return keys, ids, key == "" && s == ""
}

// quotedID reads the identifier s starts with after whitespace, in single
// or double quotes, which may run to the end of s unclosed, and is the
// rest of s after it. It reports false when s holds no quote there.
func quotedID(s string) (string, string, bool) {
	s = strings.TrimLeft(s, doctypeWhitespace)
	if s == "" {
		return "", "", false
	}
	quote := s[0]
	if quote != '"' && quote != '\'' {
		return "", "", false
	}
	s = s[1:]
	if q := strings.IndexByte(s, quote); q != -1 {
		return s[:q], s[q+1:], true
	}
	return s, "", true
}

// quirkyPublic reports whether the identifiers start with a public one
// that makes a page quirky: one on the legacy list, or an HTML 4.01
// frameset or transitional one with no system identifier after it.
func quirkyPublic(keys, ids []string) bool {
	if keys[0] != "public" {
		return false
	}
	public := strings.ToLower(ids[0])
	if legacyPublic(public) {
		return true
	}
	return len(ids) == 1 && (strings.HasPrefix(public, "-//w3c//dtd html 4.01 frameset//") ||
		strings.HasPrefix(public, "-//w3c//dtd html 4.01 transitional//"))
}

// legacyPublic reports whether the lowercased public identifier public is
// one of x/net's legacy identifiers, whole or as a prefix (quirkyIDs).
func legacyPublic(public string) bool {
	switch public {
	case "-//w3o//dtd w3 html strict 3.0//en//", "-/w3d/dtd html 4.0 transitional/en", "html":
		return true
	}
	for _, q := range quirkyIDs {
		if strings.HasPrefix(public, q) {
			return true
		}
	}
	return false
}

// quirkySystem reports whether the identifiers end with IBM's XHTML
// system identifier.
func quirkySystem(keys, ids []string) bool {
	return keys[len(keys)-1] == "system" && strings.EqualFold(ids[len(ids)-1], "http://www.ibm.com/data/dtd/v11/ibmxhtml1-transitional.dtd")
}

// quirkyIDs are the public identifiers that put a page in quirks mode,
// copied from golang.org/x/net/html v0.62.0 (doctype.go).
var quirkyIDs = []string{
	"+//silmaril//dtd html pro v0r11 19970101//",
	"-//advasoft ltd//dtd html 3.0 aswedit + extensions//",
	"-//as//dtd html 3.0 aswedit + extensions//",
	"-//ietf//dtd html 2.0 level 1//",
	"-//ietf//dtd html 2.0 level 2//",
	"-//ietf//dtd html 2.0 strict level 1//",
	"-//ietf//dtd html 2.0 strict level 2//",
	"-//ietf//dtd html 2.0 strict//",
	"-//ietf//dtd html 2.0//",
	"-//ietf//dtd html 2.1e//",
	"-//ietf//dtd html 3.0//",
	"-//ietf//dtd html 3.2 final//",
	"-//ietf//dtd html 3.2//",
	"-//ietf//dtd html 3//",
	"-//ietf//dtd html level 0//",
	"-//ietf//dtd html level 1//",
	"-//ietf//dtd html level 2//",
	"-//ietf//dtd html level 3//",
	"-//ietf//dtd html strict level 0//",
	"-//ietf//dtd html strict level 1//",
	"-//ietf//dtd html strict level 2//",
	"-//ietf//dtd html strict level 3//",
	"-//ietf//dtd html strict//",
	"-//ietf//dtd html//",
	"-//metrius//dtd metrius presentational//",
	"-//microsoft//dtd internet explorer 2.0 html strict//",
	"-//microsoft//dtd internet explorer 2.0 html//",
	"-//microsoft//dtd internet explorer 2.0 tables//",
	"-//microsoft//dtd internet explorer 3.0 html strict//",
	"-//microsoft//dtd internet explorer 3.0 html//",
	"-//microsoft//dtd internet explorer 3.0 tables//",
	"-//netscape comm. corp.//dtd html//",
	"-//netscape comm. corp.//dtd strict html//",
	"-//o'reilly and associates//dtd html 2.0//",
	"-//o'reilly and associates//dtd html extended 1.0//",
	"-//o'reilly and associates//dtd html extended relaxed 1.0//",
	"-//softquad software//dtd hotmetal pro 6.0::19990601::extensions to html 4.0//",
	"-//softquad//dtd hotmetal pro 4.0::19971010::extensions to html 4.0//",
	"-//spyglass//dtd html 2.0 extended//",
	"-//sq//dtd html 2.0 hotmetal + extensions//",
	"-//sun microsystems corp.//dtd hotjava html//",
	"-//sun microsystems corp.//dtd hotjava strict html//",
	"-//w3c//dtd html 3 1995-03-24//",
	"-//w3c//dtd html 3.2 draft//",
	"-//w3c//dtd html 3.2 final//",
	"-//w3c//dtd html 3.2//",
	"-//w3c//dtd html 3.2s draft//",
	"-//w3c//dtd html 4.0 frameset//",
	"-//w3c//dtd html 4.0 transitional//",
	"-//w3c//dtd html experimental 19960712//",
	"-//w3c//dtd html experimental 970421//",
	"-//w3c//dtd w3 html//",
	"-//w3o//dtd w3 html 3.0//",
	"-//webtechs//dtd mozilla html 2.0//",
	"-//webtechs//dtd mozilla html//",
}
