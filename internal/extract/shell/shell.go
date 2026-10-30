// Package shell is the shell extractor: every assignment of one variable in
// a shell script, and every default a parameter expansion gives it, read with
// mvdan.cc/sh/v3's parser through core's entry extractor. A value the shell
// computes, such as $(cmd) or "$BASE/bin", comes back as the script wrote it,
// marked not literal.
package shell

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns every value the script File gives the variable Name: X=v,
// export X=v, readonly, declare, typeset and local, and the default in
// ${X:-v}, ${X-v}, ${X:=v} and ${X=v}. Language is the dialect the script is
// parsed in, bash, posix or mksh; left out, the shebang chooses it, then a
// .bash, .mksh or .ksh name, and else bash.
type Spec struct {
	core.OneFile
	Name     string `json:"name"`
	Language string `json:"language,omitempty"`
}

// kindKey is the kind's key, which Kind and Build both use. Build names
// the constant rather than Kind.Key because an env reader's Read calls
// Build, and Build reading Kind would make an initialization cycle.
const kindKey = "shell"

// Kind is the shell kind, which a rule writes under the key shell. It is
// an env reader, second in env's list.
var Kind = core.Kind[Spec]{Key: kindKey, Place: Place, Resolve: Resolve, Describe: Describe,
	Env: &check.EnvReader{Order: 20, Chooses: chooses, Sniffs: IsScript, SniffBytes: ShebangBytes}, EnvRead: envRead}

// chooses takes, for the env kind, a name that ends in .sh, and .envrc,
// direnv's shell script.
func chooses(file string) bool {
	base := path.Base(file)
	return base == ".envrc" || strings.HasSuffix(base, ".sh")
}

// envRead is the env kind's reader of the variable name in file: Build's.
func envRead(file, name string, maxBytes int64, _ core.Parsers) (check.Extractor, error) {
	return Build(Spec{File: file, Name: name, MaxBytes: maxBytes})
}

// Resolve is s with its max_bytes written out, for errata list.
func Resolve(s Spec) Spec {
	s.OneFile = s.Resolved()
	return s
}

// Describe is what the kind's extractor returns: flat values, some not
// literal, as the reader marks a value the script computes.
func Describe(Spec) check.Description {
	return check.Description{Label: "shell", NotLiteral: true}
}

// languages are the dialects a spec may name.
var languages = map[string]syntax.LangVariant{
	"bash":  syntax.LangBash,
	"posix": syntax.LangPOSIX,
	"mksh":  syntax.LangMirBSDKorn,
}

// Build checks s and returns its extractor.
func Build(s Spec) (check.Extractor, error) {
	src, err := s.Check(kindKey)
	if err != nil {
		return nil, err
	}
	if s.Name == "" {
		return nil, errors.New("shell: name is required")
	}
	if !isName(s.Name) {
		return nil, fmt.Errorf("shell: name %s is no shell variable name: write a letter or _, then letters, digits and _", core.Quote(s.Name))
	}
	if _, ok := languages[s.Language]; s.Language != "" && !ok {
		return nil, fmt.Errorf("shell: language %s is not one errata reads; write bash, posix or mksh, or leave it out for the shebang to choose", core.Quote(s.Language))
	}
	r := reader{name: s.Name, language: s.Language, file: s.File, maxBytes: int(src.Limit), stmtLimit: int(src.Limit / 8)}
	// A template's engine renders {{ }} and the like before any shell
	// reads the file.
	return core.EntryExtractor{Source: src, Query: core.Query{Key: []byte(s.Name), HasKey: true}, Read: r.read, NotLiteral: core.TemplateNotLiteral(s.File, computed)}, nil
}

// computed is the shell's test of a not literal value: the reader marks it.
func computed(e *core.Entry) bool { return e.Computed }

// Place is s with what decides where it reads: its file cleaned and no
// max_bytes. The language stays as written, since a shebang the file holds
// chooses one only when it is read.
func Place(s Spec) Spec {
	s.OneFile = s.Place()
	return s
}

// nameStart are the bytes a shell variable name starts with, and nameBytes
// those it goes on with.
const (
	nameStart = "_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	nameBytes = nameStart + "0123456789"
)

// isName reports whether s is a shell variable name.
func isName(s string) bool {
	allowed := nameStart
	for i := range len(s) {
		if !strings.ContainsRune(allowed, rune(s[i])) {
			return false
		}
		allowed = nameBytes
	}
	return s != ""
}

// reader reads one spec's script.
type reader struct {
	name, language, file string
	maxBytes             int
	// stmtLimit is the longest statement read, an eighth of max_bytes.
	stmtLimit int
}

// read parses data in its dialect and visits every value of the spec's
// variable, in file order. A script that does not parse is an error, and so
// is one that nests past the guard's limit.
func (r reader) read(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	lang, why, err := chooseLanguage(r.language, r.file, data)
	if err != nil {
		return err
	}
	g := newGuard(data, r.stmtLimit)
	w := newWalker(r.name, lang, data, m, r.maxBytes)
	// The shebang's options apply whatever the language, and before the
	// first statement.
	w.opaque = !shebangKeeps(data)
	p := syntax.NewParser(syntax.Variant(lang))
	// The loop never breaks: StmtsSeq yields a statement with the error
	// that cut it short, and then the error again, even when the loop has
	// stopped, which Go's range over a function refuses with a panic
	// (FuzzShell found it). After the first error the guard ends the input,
	// and the loop runs out.
	var failed error
	for stmt, err := range p.StmtsSeq(g) {
		if failed == nil && err != nil {
			failed = parseError(err, data, g, lang, why)
		}
		if failed == nil {
			w.walk(stmt)
			failed = w.err
		}
		if failed != nil {
			g.stop()
		}
		g.statementEnds()
	}
	if failed != nil {
		return failed
	}
	return w.emit(visit)
}

// parseError says why the parser stopped, at the line where it did.
func parseError(err error, data []byte, g *guard, lang syntax.LangVariant, why string) error {
	var pe syntax.ParseError
	var le syntax.LangError
	switch {
	case errors.Is(err, errTooDeep):
		return core.LineError(lineAt(data, g.off), "the script nests more deeply than errata reads: the parser's stack passed %d calls, the limit, which about %d levels of parentheses in (( )) reach and more of any other nesting; a regex extractor can still read the file", maxFrames, maxFrames/costliestPerLevel)
	case errors.Is(err, errTooLong):
		return core.LineError(lineAt(data, g.mark), "a statement, with the comments and blank lines before it, is more than %d bytes long, the limit, an eighth of max_bytes: errata holds a statement's syntax whole while it reads it, which can take 200 bytes of memory for each byte; set max_bytes: higher to read it, or a regex extractor can still read the file", g.stmtLimit)
	case errors.As(err, &pe):
		return core.LineError(lineAt(data, int(pe.Pos.Offset())), "the script does not parse as %s, %s: %s", lang, why, core.Clip(pe.Text))
	case errors.As(err, &le):
		langs := make([]string, len(le.Langs))
		for i, l := range le.Langs {
			langs[i] = l.String()
		}
		return core.LineError(lineAt(data, int(le.Pos.Offset())), "%s parses only in %s, and the script is read as %s, %s; set language: to read it as another", core.Clip(le.Feature), strings.Join(langs, " and "), lang, why)
	}
	return err
}

// lineAt is the line, counted from 1, of offset off in data.
func lineAt(data []byte, off int) int {
	return 1 + bytes.Count(data[:min(max(off, 0), len(data))], []byte("\n"))
}

// chooseLanguage is the dialect a script is parsed in, and why, for a
// message: the spec's language, else the shell its shebang names, else its
// name's extension, else bash. A shebang that names another program is an
// error: the file is not a script of any dialect errata reads.
func chooseLanguage(spec, file string, data []byte) (syntax.LangVariant, string, error) {
	if spec != "" {
		return languages[spec], "which language: names", nil
	}
	if shell, ok := shebang(data); ok {
		lang, known := shebangShells[shell]
		if !known {
			return 0, "", core.LineError(1, "the shebang names %s, which is no shell errata reads; set language: to bash, posix or mksh to read the file as one", core.Quote(shell))
		}
		return lang, "which its shebang names", nil
	}
	switch filepath.Ext(file) {
	case ".bash":
		return syntax.LangBash, "by its name", nil
	case ".mksh", ".ksh":
		return syntax.LangMirBSDKorn, "by its name", nil
	}
	return syntax.LangBash, "since no shebang names a shell", nil
}

// ShebangBytes is the most bytes of a file's start IsScript needs: Linux
// reads at most 256 bytes of a #! line (BINPRM_BUF_SIZE), so a shell
// named past them is no shebang the kernel runs.
const ShebangBytes = 256

// IsScript reports whether data starts with a #! line that names a shell
// errata reads, directly or through env: the test the env extractor
// chooses this reader by for a file whose name says nothing, such as
// bin/deploy. Only the first ShebangBytes bytes of data count.
func IsScript(data []byte) bool {
	program, ok := shebang(data[:min(len(data), ShebangBytes)])
	_, known := shebangShells[program]
	return ok && known
}

// shebangShells are the programs a shebang may name, and the dialect each
// is read in. ksh is read as mksh, the Korn shell mvdan.cc/sh parses.
var shebangShells = map[string]syntax.LangVariant{
	"bash": syntax.LangBash,
	"sh":   syntax.LangPOSIX, "dash": syntax.LangPOSIX, "ash": syntax.LangPOSIX, "posh": syntax.LangPOSIX,
	"mksh": syntax.LangMirBSDKorn, "ksh": syntax.LangMirBSDKorn, "lksh": syntax.LangMirBSDKorn, "pdksh": syntax.LangMirBSDKorn,
}

// shebangFields are the words of a #! first line, after the #!. ok is false
// when the file starts with no #!.
func shebangFields(data []byte) (fields []string, ok bool) {
	rest, ok := bytes.CutPrefix(data, []byte("#!"))
	if !ok {
		return nil, false
	}
	line, _, _ := bytes.Cut(rest, []byte("\n"))
	return strings.Fields(string(line)), true
}

// shebang is the program a #! first line runs, by its base name: through
// env, the first word after env's options and assignments. ok is false when
// the file starts with no #!.
func shebang(data []byte) (program string, ok bool) {
	fields, ok := shebangFields(data)
	if !ok {
		return "", false
	}
	if len(fields) == 0 {
		return "", true
	}
	program = filepath.Base(fields[0])
	switch program {
	case "env":
		program = ""
		for i := 1; i < len(fields); i++ {
			f := fields[i]
			if f == "-u" || f == "-C" || f == "-P" {
				i++ // an option that takes the next word
				continue
			}
			if !strings.HasPrefix(f, "-") && !strings.Contains(f, "=") {
				return filepath.Base(f), true
			}
		}
	case "busybox":
		// busybox runs the applet its first argument names.
		program = ""
		if len(fields) != 1 {
			program = filepath.Base(fields[1])
		}
	}
	return program, true
}
