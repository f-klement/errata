// Package gomod parses go.mod and go.work files with golang.org/x/mod/modfile,
// the parser the go command uses, under a limit on the lines and tokens a file
// can hold. tool_version and dependency read go.mod through it. It lives below
// core, as mdparse and htmlparse do, so core itself knows no format.
package gomod

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/f-klement/errata/internal/extract/core"
)

// Parse is the go.mod data holds, the bytes of the file s names, as
// golang.org/x/mod/modfile parses it, which the go command parses it with.
// The file is refused first when it can hold more lines and tokens than
// s's limit allows, one for every BytesPerNode bytes (Nodes),
// since modfile builds the syntax of the whole file before a reader sees a
// directive. A go.mod the go command refuses, such as one with an unknown
// directive, is refused too. Every refusal names the file. Each module
// version is the text the file writes (asWritten), so a short one, such as
// v1, stays v1; Canonical tells such a version from the one it is short for.
func Parse(s core.Source, data []byte) (*modfile.File, error) {
	return parseMod(s, data, func() (*modfile.File, error) { return parseGoMod(s.File, data, asWritten) })
}

// LastTokenLine is the line of data, a go.mod or go.work file, that the
// last token of l starts on, as errata numbers every file's lines
// (core/lineend). A caller reports a value at it only when the value is
// that token, as the version or path of every directive errata reads is.
// modfile ends a line at a LF alone and reads a lone CR as a blank, as the
// go command does. A lone CR before a directive puts it on a later line
// than modfile's, and one inside it, as in require x\rv1.0.0, leaves the
// directive whole with its value on a later line than its start. The
// values stay modfile's; only the line is errata's.
func LastTokenLine(data []byte, l *modfile.Line) int {
	return core.LineOf(data, l.End.Byte-len(l.Token[len(l.Token)-1]))
}

// ParseWork is Parse for a go.work file, which modfile.ParseWork reads.
func ParseWork(s core.Source, data []byte) (*modfile.WorkFile, error) {
	return parseMod(s, data, func() (*modfile.WorkFile, error) { return parseGoWork(s.File, data, asWritten) })
}

// asWritten is the version fixer that keeps each module version as the
// file writes it. Without a fixer modfile writes the canonical form in its
// place, v1.0.0 for v1, which is neither the text of the line nor what the
// go command reads: in a go.mod it resolves a version that is not canonical
// as a query, v1 to the latest v1 release, and writes the result back, and
// in a go.work it reads it as the canonical form. A version modfile refuses
// without a fixer, one that is no semantic version, is refused with
// modfile's own message.
func asWritten(path, v string) (string, error) {
	if module.CanonicalVersion(v) == "" {
		return "", &module.ModuleError{Path: path, Err: &module.InvalidVersionError{Version: v, Err: errors.New("must be of the form v1.2.3")}}
	}
	return v, nil
}

// Canonical reports whether v, a module version Parse or ParseWork
// gives, is in canonical form, which the go command reads as it stands.
// One that is not, such as v1, v1.2 or v1.2.3+meta, it reads as another
// version: the one a query resolves to in a go.mod, the canonical form in a
// go.work. The empty version of a replace by a directory is canonical, since
// it names no version to read.
func Canonical(v string) bool { return v == "" || module.CanonicalVersion(v) == v }

// parseMod is parse's file, after the limit on the lines and tokens of
// data. modfile recovers its parser's panics but not those of the step
// that turns the syntax into directives; no input found makes one panic,
// and a panic there would end the run instead of making one rule unknown.
func parseMod[F any](s core.Source, data []byte, parse func() (*F, error)) (f *F, err error) {
	if n, limit := Nodes(data), s.Limit/BytesPerNode; int64(n) > limit {
		return nil, fmt.Errorf("%s: the file can hold up to %d lines and tokens, over the limit of %d, one for every %d bytes of max_bytes; set max_bytes: higher to read a file this dense",
			s.File, n, limit, BytesPerNode)
	}
	defer func() {
		if r := recover(); r != nil {
			f, err = nil, fmt.Errorf("%s: golang.org/x/mod/modfile stopped on the file: %v", s.File, r)
		}
	}()
	f, err = parse()
	if err != nil {
		// modfile's message starts with the file and the line.
		return nil, core.ClipError(err)
	}
	return f, nil
}

// parseGoMod and parseGoWork are modfile's; a test replaces one to witness
// the recover.
var (
	parseGoMod  = modfile.Parse
	parseGoWork = modfile.ParseWork
)

// BytesPerNode is how many bytes of max_bytes allow one line or token
// of a go.mod or go.work. modfile builds the syntax of the whole file
// before errata sees a directive, and a line or token costs 150 to 440
// bytes there: 32 MiB of lines of one short token took 2.1 GB and 4.1 s, of
// empty comments 1.6 GB, and of commas on one line 1.9 GB. A go.mod of
// requirements holds a line or token for every 7 bytes, so at the default
// the limit reads one of about 3.5 MB, some 80000 requirements. The
// densest files measured at the limit, 32 MiB of unknown directives of 129
// bytes or of lines ending in a comment, take about 0.5 to 0.75 s and up to
// about 320 MB, since modfile keeps an error for each line.
const BytesPerNode = 64

// goModPunct are the bytes modfile's lexer makes a token of whatever is
// around them: a line end, ( ) [ ] { } and the comma, and the quote and the
// backquote that start a string.
const goModPunct = "\n()[]{},\"`"

// Nodes is an upper bound on the lines and tokens modfile builds from
// data: one for each byte of goModPunct, and one for each other byte that is
// not blank and follows a blank, a byte of goModPunct or the start of the
// file, where an identifier starts. A string counts at least once, by its
// quote, and an identifier right after a ) or a closing quote counts too.
func Nodes(data []byte) int {
	n, start := 1, true
	for _, b := range data {
		switch {
		case strings.IndexByte(goModPunct, b) >= 0:
			n, start = n+1, true
		case b == ' ' || b == '\t' || b == '\r':
			start = true
		default:
			if start {
				n++
			}
			start = false
		}
	}
	return n
}
