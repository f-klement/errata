package core

import (
	"fmt"

	"github.com/f-klement/errata/internal/extract/core/lexer"
)

// Language is one row of the lexer table (internal/extract/core/lexer): a
// language, the file names it is chosen for and the forms its comments and
// literals take. Its Spans are the comments, string literals and quoted names
// of a file's bytes. A reader reads the table through core, as it reads
// every package below core.
type Language = lexer.Language

// Span is one comment, string literal or quoted name Language.Spans finds.
type Span = lexer.Span

// SpanKind is what a Span is.
type SpanKind = lexer.Kind

// SpanForm is one form of a row's comments and literals, which the literal
// reader decodes a string by. It is SpanForm, as Kind is SpanKind, since a
// string scalar's Form is core's own.
type SpanForm = lexer.Form

// Here is a heredoc's body.
type Here = lexer.Here

// Syntax is how a row writes literal data, which the literal reader reads.
type Syntax = lexer.Syntax

// Bracket is a pair of brackets of a Syntax that holds a list or a mapping.
type Bracket = lexer.Bracket

// Prefix is a word a Syntax's strings may be written straight after.
type Prefix = lexer.Prefix

// How a string form takes indentation off its lines (SpanForm.Dedent).
const (
	DedentNone   = lexer.DedentNone
	DedentCloser = lexer.DedentCloser
	DedentCommon = lexer.DedentCommon
)

// The kinds of Span.
const (
	SpanComment = lexer.Comment
	SpanString  = lexer.String
	SpanName    = lexer.Name
	SpanText    = lexer.Text
)

// LanguageFor is the row of the file named file, chosen by its base name,
// then by its longest extension a row lists, in any case; false when no row
// lists it.
func LanguageFor(file string) (Language, bool) { return lexer.For(file) }

// LanguageNamed is the row a spec's language: names, by its Name.
func LanguageNamed(name string) (Language, bool) { return lexer.Named(name) }

// Languages are copies of the rows of the table, in its order.
func Languages() []Language { return lexer.Languages() }

// LanguageNames are the names of every row, comma separated, for a message.
func LanguageNames() string { return lexer.Names() }

// CheckSpan refuses s, a span of the file named file whose bytes are data,
// when it runs off the end of the file (Span.RunsOff): the lexer lost its
// place there, so no reader of the table can tell comment from code after
// the line s opened on. Each reader of the table checks every span it walks,
// of every kind, so the file's rule is unknown rather than wrong.
func CheckSpan(file string, data []byte, s Span) error {
	if !s.RunsOff(len(data)) {
		return nil
	}
	what := map[SpanKind]string{SpanComment: "comment", SpanString: "string", SpanName: "quoted name", SpanText: "text"}[s.Kind]
	return fmt.Errorf("%s: line %d: a %s opens here and runs to the end of the file, so the lexer lost its place and cannot tell comments from code after it; a construct it does not understand, such as a heredoc or a regular expression, holds a quote or a comment's opener (docs/reference.md#comment)",
		file, LineOf(data, s.Start), what)
}
