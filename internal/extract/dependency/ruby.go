package dependency

import (
	"bytes"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// ruby is the lexer's row for Ruby, which a Gemfile is.
var ruby = func() core.Language {
	l, ok := core.LanguageFor("Gemfile")
	if !ok {
		panic("dependency: the lexer has no row for a Gemfile")
	}
	return l
}()

// readGemfile reads the requirements a Gemfile's gem statements give the
// package, with the lexer's row for Ruby, since a Gemfile is a Ruby
// program: a statement that starts with gem, then the package's name as a
// string, then each requirement as a string after a comma, such as
// gem "rails", "~> 7.1", ">= 7.1.2", which gives two values. A string with
// #{} interpolates, and a requirement that is no string, such as a
// constant, are not literal. The options after the requirements, such as
// require: false, give nothing, and neither does a gem with no
// requirement. errata runs no Ruby, so a gem that a method or a loop
// declares is not read.
func readGemfile(rd *reading, data []byte) error {
	// Ruby skips a byte order mark at the start of a file, which holds no
	// line end, so no line moves.
	data = core.DropBOM(data)
	g := gemfile{rd: rd, data: data, at: core.NewLineCursor(data)}
	prev := 0
	for s := range ruby.Spans(data) {
		if err := core.CheckSpan(rd.src.File, data, s); err != nil {
			return named{err}
		}
		if err := g.span(data[prev:s.Start], prev, s); err != nil {
			return err
		}
		prev = s.End
	}
	if g.in {
		// The code after the last span ends the statement, with the
		// code gathered since its last string.
		g.gather(data[prev:], prev)
		return g.other()
	}
	return nil
}

// gemfile is a Gemfile being read, a span at a time. in is set inside a gem
// statement of the package, after a string, and between holds the code
// since that string, the comments left out; each mark says where a piece of
// it starts in data, so a place in between maps back to its line.
type gemfile struct {
	rd      *reading
	data    []byte
	at      *core.LineCursor
	in      bool
	between []byte
	marks   []mark
}

// mark is where a piece of between starts: at its index in between, and
// off in data.
type mark struct{ at, off int }

// gather adds code, which starts at off in data, to between.
func (g *gemfile) gather(code []byte, off int) {
	g.marks = append(g.marks, mark{at: len(g.between), off: off})
	g.between = append(g.between, code...)
}

// offset is where the byte at i in between is in data.
func (g *gemfile) offset(i int) int {
	m := g.marks[0]
	for _, next := range g.marks[1:] {
		if next.at > i {
			break
		}
		m = next
	}
	return m.off + i - m.at
}

// reset empties between.
func (g *gemfile) reset() { g.between, g.marks = g.between[:0], g.marks[:0] }

// span reads the span s, after code, which starts at off.
func (g *gemfile) span(code []byte, off int, s core.Span) error {
	if g.in {
		g.gather(code, off)
		if s.Kind == core.SpanComment {
			return nil
		}
		if s.Kind == core.SpanString && string(bytes.TrimSpace(g.between)) == "," && !g.key(s.End) {
			g.reset()
			body := string(g.data[s.BodyStart:s.BodyEnd])
			computed := g.data[s.Start] == '`' || interpolates(g.data, s)
			if !computed {
				body = exactRange(body)
			}
			return g.rd.add(found{text: body, role: Range, line: g.at.At(s.Start), notLiteral: computed})
		}
		if err := g.other(); err != nil {
			return err
		}
	}
	if s.Kind == core.SpanString && gemStarts(code, off == 0) && !interpolates(g.data, s) && g.rd.is(string(g.data[s.BodyStart:s.BodyEnd])) {
		g.in = true
		g.reset()
	}
	return nil
}

// other ends a gem statement of the package at between, which is no comma
// and a string: a comma and then a name, such as a constant or ENV.fetch,
// is a requirement the file computes, at the line the name is on, and
// anything else, such as require: false or :require => false, ends the
// requirements.
func (g *gemfile) other() error {
	code := g.between
	defer g.reset()
	g.in = false
	rest, ok := bytes.CutPrefix(bytes.TrimLeft(code, " \t\r\n"), []byte(","))
	if !ok {
		return nil
	}
	trimmed := bytes.TrimLeft(rest, " \t\r\n")
	end := 0
	for end < len(trimmed) && (isAlnum(trimmed[end]) || trimmed[end] == '_' || trimmed[end] == '.' || trimmed[end] == ':' && end+1 < len(trimmed) && trimmed[end+1] == ':') {
		if trimmed[end] == ':' {
			end++
		}
		end++
	}
	name := trimmed[:end]
	if len(name) == 0 || isDigit(name[0]) || end < len(trimmed) && trimmed[end] == ':' {
		return nil
	}
	start := g.offset(len(code) - len(trimmed))
	return g.rd.add(found{text: string(name), role: Range, line: g.at.At(start), notLiteral: true})
}

// key reports whether the string that ends at end is a key of a hash
// argument, followed by => or :, as "require" => false is.
func (g *gemfile) key(end int) bool {
	rest := bytes.TrimLeft(g.data[end:], " \t")
	return bytes.HasPrefix(rest, []byte("=>")) || bytes.HasPrefix(rest, []byte(":"))
}

// gemStarts reports whether code, the code before a string, ends in a gem
// statement's start: gem, alone after the start of a line or a ;, then
// blanks or a (. top says code starts at the start of the file.
func gemStarts(code []byte, top bool) bool {
	i := bytes.LastIndexAny(code, "\r\n;")
	if i < 0 && !top {
		return false
	}
	tail := bytes.TrimLeft(code[i+1:], " \t")
	rest, ok := bytes.CutPrefix(tail, []byte("gem"))
	if !ok {
		return false
	}
	if p, paren := bytes.CutPrefix(rest, []byte("(")); paren {
		return len(bytes.Trim(p, " \t")) == 0
	}
	return len(rest) > 0 && len(bytes.Trim(rest, " \t")) == 0
}

// interpolates reports whether s is a double-quoted string that holds
// #{, which Ruby replaces with what the code inside computes.
func interpolates(data []byte, s core.Span) bool {
	return data[s.Start] == '"' && bytes.Contains(data[s.BodyStart:s.BodyEnd], []byte("#{"))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// readGemfileLock reads a Gemfile.lock as Bundler's parser does: in each
// source section, GEM, PATH, GIT or PLUGIN SOURCE, the specs: list gives
// each gem, indented four spaces, with the version resolved and, after a
// -, the platform it was built for, such as nokogiri (1.16.2-x86_64-linux);
// the platform is not part of the version. The lines indented six spaces
// below a gem are its own dependencies' ranges, and the DEPENDENCIES
// section repeats the Gemfile's, so neither is read.
func readGemfileLock(rd *reading, data []byte) error {
	data = core.DropBOM(data)
	source := false
	return eachLine(data, func(line []byte, n int) error {
		if len(line) > 0 && line[0] != ' ' {
			switch string(line) {
			case "GEM", "PATH", "GIT", "PLUGIN SOURCE":
				source = true
			default:
				source = false
			}
			return nil
		}
		// A gem's own dependencies are indented six spaces, so their
		// names start with blanks and match no package.
		rest, ok := bytes.CutPrefix(line, []byte("    "))
		if !source || !ok {
			return nil
		}
		i := bytes.Index(rest, []byte(" ("))
		if i <= 0 || !bytes.HasSuffix(rest, []byte(")")) {
			return nil
		}
		if !rd.is(string(rest[:i])) {
			return nil
		}
		version, _, _ := strings.Cut(string(rest[i+2:len(rest)-1]), "-")
		return rd.add(found{text: version, section: "specs", role: Resolved, line: n})
	})
}
