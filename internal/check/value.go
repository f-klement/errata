package check

import (
	"context"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Location is a place in a file of the repository: a path relative to the
// repository root, and a line counted from 1. It is where an extractor found
// a value, or where a check is written in the config file.
type Location struct {
	File string
	Line int
	// Outside marks a config file outside the repository, such as one given
	// with -c from another directory. File then names it as the user gave
	// it, which is no path from the root, so a report names the file and
	// places nothing in the repository.
	Outside bool
}

// Inside reports whether l is a path inside the repository, as every
// value's location must be: not empty, not absolute, and with no .. that
// leaves the root, as Linux reads it and as Windows does (local). It reads
// no file, so a link is not followed.
func (l Location) Inside() bool {
	return !l.Outside && local(l.File, false) && local(l.File, true)
}

// OutsideOnWindows reports whether l is outside the repository only as
// Windows reads it: where a slash alone separates names and a drive means
// nothing, as on Linux, it is inside, but a backslash that climbs out or a
// drive such as C: puts it outside. Inside refuses it on every OS, and a
// message says why, since on Linux the file is in the repository.
func (l Location) OutsideOnWindows() bool {
	return !l.Outside && local(l.File, false) && !local(l.File, true)
}

// local reports whether p is a path that stays inside the root, read as
// text and the same on every OS. Without windows it reads p as Linux does: a
// slash separates two names, and a path that starts with one is absolute.
// With windows it reads p as Windows does: a backslash separates two names
// too, and a path is absolute when it starts with either, or when its first
// name, once . and .. are resolved, is a drive such as C: (C:x, ./C:x and
// a/../C:x alike). Both readings refuse a .. that leaves the root. Inside
// asks both, so a path that is absolute or climbs out under either is
// refused on every OS: a\b/../.. climbs out on Linux, and a\..\..\x on
// Windows. Nothing else is refused.
//
// It judges the ASCII separators, the ASCII dot and the ASCII drive
// letters alone. A name such as 1:x, which is no drive, passes, and so
// does a Unicode lookalike of . or /, such as U+2024 ONE DOT LEADER,
// U+2215 DIVISION SLASH or the fullwidth U+FF0F, which local reads as part
// of a name. A value's location is never opened, and a file: entry is
// opened only after filepath.IsLocal passes it on the OS that opens it, so
// neither escapes the repository.
//
// It is not filepath.IsLocal, which on Windows also refuses the names
// Windows keeps for devices (NUL, CON, PRN, AUX, COM1 and the like, with
// any extension), so a repository's aux.yaml would be outside it on Windows
// alone. A location is a name a report prints, and the rule never opens it;
// a reader that opens a file checks that file when it opens it.
func local(p string, windows bool) bool {
	sep := func(r rune) bool { return r == '/' || windows && r == '\\' }
	if p == "" || sep(rune(p[0])) {
		return false
	}
	depth := 0
	for _, n := range strings.FieldsFunc(p, sep) {
		switch {
		case n == ".":
		case n == "..":
			if depth--; depth < 0 {
				return false
			}
		case windows && depth == 0 && isDrive(n):
			return false
		default:
			depth++
		}
	}
	return true
}

// isDrive reports whether the name n starts with a drive, an ASCII letter
// and a colon, as C:x does.
func isDrive(n string) bool {
	return len(n) > 1 && n[1] == ':' && ('a' <= n[0] && n[0] <= 'z' || 'A' <= n[0] && n[0] <= 'Z')
}

// ActedOn reports whether a terminal or a viewer acts on r rather than
// showing it: a control character (C0, DEL and C1), such as a newline; a
// direction control (U+061C, U+200E, U+200F, U+202A to U+202E and U+2066
// to U+2069); or the line or paragraph separator, U+2028 and U+2029. It is
// the one set errata refuses in a path (HoldsControl) and in a version
// (core.CheckVersion), and that the reports escape (report.QuoteControls),
// so the guard and the escape cannot drift apart.
func ActedOn(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || unicode.In(r, unicode.Zl, unicode.Zp)
}

// HoldsControl reports whether s holds a rune ActedOn names. A path that
// holds one is refused wherever errata meets it, in a config's file:, a
// glob's match and a value's location. A message or the text report would
// print a control character as it is, and a newline in a file's name would
// print a line that reads as an erratum or a verdict of its own. The text
// report escapes the other two, but a SARIF viewer decodes the JSON escape,
// so there a direction control could reorder the line a reviewer reads and
// a separator could break it.
func HoldsControl(s string) bool {
	return strings.ContainsFunc(s, ActedOn)
}

// String is l as a message names it: the file and the line, as f.yaml:3, or
// the file alone for a line below 1, which a value has when it holds for the
// file as a whole, such as whether the file exists.
func (l Location) String() string {
	if l.Line < 1 {
		return l.File
	}
	return l.File + ":" + strconv.Itoa(l.Line)
}

// Value is one value an extractor returned, in its string form, with the
// place it was found. A rule compares values; the text and SARIF reports
// point at their locations.
type Value struct {
	Text string
	At   Location
	// NotLiteral marks a value the file only computes, such as $(cmd) in a
	// shell script, ${{ expr }} in a CI file, a reference or a function
	// call: Text is what the file wrote, not what it evaluates to. A rule
	// never compares such a value as text. It is unknown, or, for an
	// extractor that skip_not_literal lists, it leaves the value out. The
	// zero value is literal, so a reader whose every value is literal never
	// sets it.
	NotLiteral bool
	// Key is the name the file sets the value under, such as API_PORT for
	// the line API_PORT=8080 of an env file, for a reader whose values
	// carry one; "" for any other. A report names it beside the location
	// (KeyNote).
	Key string
	// HasKey marks a value whose reader names its key, so the empty key,
	// which an env file line =x sets, prints as key "" and not as no key.
	HasKey bool
	// Condition is what must hold for a job to see the value, for a reader
	// that returns one value for each case a file chooses between, such as
	// a GitLab CI rule's "if $CI_COMMIT_TAG" or a parallel:matrix instance
	// "build: [a]"; "" for a value that always holds. A rule compares it as
	// any other value; a report names it beside the location
	// (ConditionNote).
	Condition string
	// Origin is the reader that found the value and the part of its file
	// the value holds for, for a value an env extractor returned, such as
	// `gitlab-ci job "deploy"` or `shell`; "" for any other. Its reader
	// quotes the file's text in it, so a report writes it as it is
	// (OriginNote).
	Origin string
	// Data is the value whole, for an extractor that hands assert each
	// mapping its path selects (as: map); nil for a flat value. Text is
	// then Data's Show, cut at ShownBytes, for a message and a report.
	Data *Data
	// Shown is how a report names a value handed over whole, as the rule's
	// message shows it: Data after the rule's normalize, cut at ShownBytes.
	// The rule sets it in its result; an extractor leaves it "", and so
	// does the rule for a flat value, whose text a report quotes.
	Shown string
}

// Size is what v counts against the bounds on an extractor's values: one
// node and the bytes of its text, or for a value handed over whole the
// nodes and bytes of its Data.
func (v Value) Size() (nodes int, bytes int64) {
	if v.Data != nil {
		return v.Data.Size()
	}
	return 1, int64(len(v.Text))
}

// maxNote is the most bytes of a key or a condition a note quotes.
const maxNote = 64

// KeyNote is what a report writes after a value's location to name its
// key, such as ` key "API_PORT"`, cut as note says. It is "" for a value
// with no key.
func (v Value) KeyNote() string {
	if !v.HasKey && v.Key == "" {
		return ""
	}
	return note(" key ", v.Key)
}

// Notes are what a report writes after a value's location: its key, its
// origin and its condition, each "" when the value has none.
func (v Value) Notes() string { return v.KeyNote() + v.OriginNote() + v.ConditionNote() }

// OriginNote is what a report writes after a value's location to name the
// reader and the part of the file it came from, such as
// ` from gitlab-ci job "deploy"`. It is "" for a value with no origin.
func (v Value) OriginNote() string {
	if v.Origin == "" {
		return ""
	}
	return " from " + v.Origin
}

// ConditionNote is what a report writes after a value's location to name
// its condition, such as ` when "if $CI_COMMIT_TAG"`, cut as note says. It
// is "" for a value with none.
func (v Value) ConditionNote() string {
	if v.Condition == "" {
		return ""
	}
	return note(" when ", v.Condition)
}

// note is label and s quoted, and for an s longer than maxNote its first
// maxNote bytes, cut back to the start of a character, and ....
func note(label, s string) string {
	if len(s) <= maxNote {
		return label + strconv.Quote(s)
	}
	n := maxNote
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return label + strconv.Quote(s[:n]) + "..."
}

// NoValue is the error an extractor returns when it found no value and
// can say where the value may be instead, such as a variable that only a
// GitLab project's settings could set. The rule treats it as an empty list,
// so allow_empty still decides, and names Reason when the extractor is
// unknown for it.
type NoValue struct {
	Reason string
}

func (e *NoValue) Error() string { return e.Reason }

// Extractor gets values from one place in the repository. An empty list is
// not an error: the rule decides whether finding nothing is unknown. An error
// means the extractor could not read its source, and the rule is unknown.
// What it returns, and which extractors of its rule it reads, its kind
// tells beforehand (Description). ctx is the run's: an extractor that waits
// on something outside errata, as a plugin's process will, stops when it
// ends. A built in reader reads files in process under ADR 0003's bounds
// and does not read it.
type Extractor interface {
	Extract(ctx context.Context, root string) ([]Value, error)
	// ExtractWith is Extract given the values of each extractor its
	// Description's Reads names, which is how a rule runs it: their literal
	// values, after skip_not_literal, in the order they were found, with
	// the text the file holds, before normalize. A lowered value would miss
	// the text it came from, so a search that should ignore case says so
	// in its own pattern, with (?i). An extractor that reads no other
	// extractor's values ignores values and returns what Extract does.
	ExtractWith(ctx context.Context, root string, values map[string][]Value) ([]Value, error)
}
