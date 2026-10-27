package core

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Parsers are the JSON and TOML readers a reader of version files parses a
// manifest with, such as package.json or Cargo.toml. The facade hands them
// the json and toml extractors' own, since no reader imports another.
type Parsers struct {
	JSON func([]byte) (*Node, error)
	TOML func([]byte) (*Node, error)
}

// CheckVersion refuses a version that is empty, or that holds a character
// a terminal or a viewer acts on rather than shows, which no version holds.
// line is where the version is, for the message.
func CheckVersion(line int, text string) error {
	if text == "" {
		return LineError(line, "the version is empty")
	}
	if strings.ContainsFunc(text, actedOn) {
		return LineError(line, "the version %s holds a control character, a direction control, a line separator or a byte that is not UTF-8, which no version holds", Quote(text))
	}
	return nil
}

// actedOn reports whether a terminal or a viewer acts on c rather than
// showing it, as the report package's test has it (a control character, a
// direction control, a line or paragraph separator), or c is the U+FFFD a
// byte that is not UTF-8 decodes to.
func actedOn(c rune) bool {
	return unicode.IsControl(c) || unicode.Is(unicode.Bidi_Control, c) || unicode.In(c, unicode.Zl, unicode.Zp) || c == utf8.RuneError
}
