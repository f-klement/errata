// Package marker is the marker extractor: the values of the markers that tag
// a claim in a document or a code comment with the id of the rule that
// checks it, found in the files git tracks that a rule's files: names (ADR
// 0017, docs/reference.md#marker). An opening marker is an HTML comment whose text is
// errata, a colon and the id; a closing one, an HTML comment whose text is
// a slash and errata. No comment here writes either whole, since a walk of
// errata's own files would read it as one. A Markdown file is read through
// the parse in core/mdparse, a file the lexer table lists through its
// comments (core.Language), and the value is the text between the opening
// marker and the closing one. config gives the extractor the id of its rule
// (extract.Spec.ForRule) and runs the pass that finds a marker no rule reads.
package marker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/yuin/goldmark/v2/ast"
	east "github.com/yuin/goldmark/v2/extension/ast"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/markup"
	"github.com/f-klement/errata/internal/extract/core/mdparse"
)

// Spec reads the markers that name its rule in the files Files names, each a
// file or a glob over the files git tracks: every file git tracks when it
// names none. MaxBytes bounds each file the walk parses, and what the values
// hold in all.
type Spec struct {
	Files    []string `json:"files,omitempty"`
	MaxBytes int64    `json:"max_bytes,omitempty"`
	// rule is the id of the rule the spec is an entry of, which config
	// gives it (ForRule); a rule's YAML cannot write it.
	rule string
}

// ForRule is s with id, the id of the rule it is an entry of, whose markers
// its extractor reads (extract.Spec.ForRule).
func (s Spec) ForRule(id string) Spec {
	s.rule = id
	return s
}

// Bound is the max_bytes s sets, 0 for the default (core.Bounded).
func (s Spec) Bound() int64 { return s.MaxBytes }

// Kind is the marker kind, which a rule writes under the key marker.
var Kind = core.Kind[Spec]{Key: "marker", Place: Place, Resolve: Resolve, Describe: Describe, ForRule: Spec.ForRule}

// Every is the glob of a spec that names no files: every file git tracks.
const Every = "**"

// Place is s with only what decides where it reads: its files cleaned and
// sorted, every file git tracks written out as Every, and no max_bytes.
func Place(s Spec) Spec {
	s.Files = core.Clean(files(s.Files))
	slices.Sort(s.Files)
	s.MaxBytes = 0
	return s
}

// Resolve is s with its files and its max_bytes written out, for errata
// list.
func Resolve(s Spec) Spec {
	s.Files = slices.Clone(files(s.Files))
	s.MaxBytes = core.MaxBytes(s.MaxBytes)
	return s
}

// files is what a spec's files: walks: Every for a key left out.
func files(f []string) []string {
	if f == nil {
		return []string{Every}
	}
	return f
}

// Describe is what the kind's extractor returns: flat values, each literal.
func Describe(Spec) check.Description { return check.Description{Label: "marker"} }

// The bounds of a walk (ADR 0003). A walk opens every file its files: names,
// reads each whole that holds the bytes errata and is no larger than
// max_bytes, and reads a larger one in pieces only to look for those bytes.
// The figures were measured on the builder node, a walk at a time; each
// grew in proportion to what the walk read.
const (
	// MaxFiles is the most files one walk opens. Each costs an open and a
	// stat even when it is empty: 20000 small files, a tenth of them
	// Markdown and a tenth Go with a marker each, took 0.66 s and 144 MiB
	// allocated in all, so a walk at the limit takes about 3.3 s. The Linux
	// kernel's tree holds some 80000 files.
	MaxFiles = 100_000
	// MaxWalkBytes is the most bytes one walk reads of its files in all,
	// which bounds its time with the time each file's parse is held to:
	// 256 MiB of files without errata took 0.34 s, and 128 MiB of Markdown
	// that holds errata, each file parsed, 3.0 s, so 2 GiB takes about 2.7 s
	// unparsed and 48 s parsed. The Linux kernel's tree holds some 1.3 GB.
	MaxWalkBytes = 2 << 30
	// MaxMarkers is the most markers one walk finds, the cap on the values
	// of one extractor (check.MaxValues).
	MaxMarkers = check.MaxValues
	// chunk is the piece a file over max_bytes is read in.
	chunk = 1 << 20
)

// token is the bytes every marker holds, which a file must hold for the
// walk to parse it.
var token = []byte("errata")

// Bounds are the bounds a walk holds the files it reads to: MaxWalkBytes and
// MaxMarkers; a test lowers them to witness them with a few files.
type Bounds struct {
	Bytes int64
	Count int
}

// Walk reads the markers of the files of a files: key. Its limits are
// MaxFiles (in its Files) and its Bounds; a test lowers them to witness the
// bounds with a few files.
type Walk struct {
	files core.Files
	limit int64
	Bounds
}

// NewWalk checks files and maxBytes as a files: key and a max_bytes are
// checked (core.CheckFiles): nil walks every file git tracks, and a list of
// no entry is refused.
func NewWalk(fs []string, maxBytes int64) (Walk, error) {
	if fs != nil && len(fs) == 0 {
		return Walk{}, fmt.Errorf("%s: files lists no entry; name a file or a glob, or leave files out for every file git tracks", Kind.Key)
	}
	f, err := core.CheckFiles(Kind.Key, files(fs), maxBytes)
	if err != nil {
		return Walk{}, err
	}
	return Walk{files: f.WithFileLimit(MaxFiles), limit: core.MaxBytes(maxBytes), Bounds: Bounds{Bytes: MaxWalkBytes, Count: MaxMarkers}}, nil
}

// Limit is the most bytes the walk parses of one file.
func (w Walk) Limit() int64 { return w.limit }

// Found is one marker: the id it names, its value at the file and the line
// where the value starts, or where the marker opens for an empty value, and
// where it opens.
type Found struct {
	Rule  string
	Value check.Value
	Open  check.Location
}

// trackedKey is the key of the run's one read of git's index under a root.
type trackedKey struct{ root string }

// Files is the files the walk reads under root, in git's order, but a
// symlink git tracks that only globs matched: what it leads to is read
// under its own name when git tracks it, and is no file of the repository
// when it does not. A link files: names outright is kept, and its read is
// refused (readFile). The run's walks list against one read of git's index
// (check.Shared).
func (w Walk) Files(ctx context.Context, root string) ([]string, error) {
	t, err := check.Shared(ctx, trackedKey{root}, func() (*core.Tracked, error) { return core.NewTracked(ctx, root), nil })
	if err != nil {
		return nil, err
	}
	named, err := t.List(w.files)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range named {
		if n.Link && !w.outright(n.By) {
			continue
		}
		out = append(out, n.File)
	}
	return out, nil
}

// outright reports whether one of the entries by names a file outright,
// rather than as a glob.
func (w Walk) outright(by []int) bool {
	for _, k := range by {
		if !w.files.Entries[k].Glob {
			return true
		}
	}
	return false
}

// Markers is every marker of the files the walk reads under root (Read).
func (w Walk) Markers(ctx context.Context, root string) ([]Found, error) {
	names, err := w.Files(ctx, root)
	if err != nil {
		return nil, err
	}
	named := make([]Name, len(names))
	for i, n := range names {
		named[i] = Name{File: n, Limit: w.limit}
	}
	return Read(ctx, root, named, w.Bounds)
}

// Name is a file a walk reads, and the most bytes it parses of it.
type Name struct {
	File  string
	Limit int64
}

// fileKey is the key of one file's markers in a run: the file under its
// root, parsed up to limit bytes.
type fileKey struct {
	root, file string
	limit      int64
	bounds     Bounds
}

// fileMarkers is what one file holds: its size, and its markers or why the
// walk cannot tell them.
type fileMarkers struct {
	size  int64
	found []Found
}

// Read is every marker of names, files under root, each parsed up to its
// limit, in the order of names and, in each, of the file. A run reads and
// parses each file once, whichever walks name it (check.Shared), and each
// walk holds what it names to b. A file the walk cannot read, or whose
// markers it cannot tell, makes the walk fail, since a marker in it would
// go unread: one past a bound, a symlink, one that holds the bytes of a
// marker and that no reader of the walk reads, an unclosed marker, a stray
// closer and a marker that does not parse.
func Read(ctx context.Context, root string, names []Name, b Bounds) ([]Found, error) {
	var out []Found
	var spent int64
	var r *core.Reader // opened at the first file
	defer func() {
		if r != nil {
			r.Close()
		}
	}()
	for _, n := range names {
		name := n.File
		if r == nil {
			var err error
			if r, err = core.OpenReader(ctx, root); err != nil {
				return nil, err
			}
		}
		// The size first, so the walk reads no file past its bound on bytes
		// (markers delta review 2, E1): the shared read below is the run's,
		// and knows nothing of what this walk has spent. A stat that fails
		// leaves the refusal to readFile, which says why.
		if info, err := r.Stat(core.Source{File: name, Limit: math.MaxInt64}); err == nil && spent+info.Size() > b.Bytes {
			return nil, errBytes(name)
		}
		m, err := check.Shared(ctx, fileKey{root, name, n.Limit, b}, func() (fileMarkers, error) {
			return readFile(r, name, n.Limit, b)
		})
		if err != nil {
			return nil, err
		}
		if spent += m.size; spent > b.Bytes {
			return nil, errBytes(name)
		}
		if len(out)+len(m.found) > b.Count {
			return nil, errCount(name)
		}
		out = append(out, m.found...)
	}
	return out, nil
}

// errBytes refuses a walk whose files, up to name, hold more than its
// bound on bytes.
func errBytes(name string) error {
	return fmt.Errorf("%s: %s: the files read hold more than %d bytes, the limit of one walk; narrow files", Kind.Key, name, MaxWalkBytes)
}

// errCount refuses a walk whose files, up to name, hold more than its
// bound on markers.
func errCount(name string) error {
	return fmt.Errorf("%s: %s: the files hold more than %d markers, the limit", Kind.Key, name, MaxMarkers)
}

// readFile is the markers of the file name, read through r: none for a file
// that holds no errata, which is not parsed. A file over limit is read in
// pieces, and refused when it holds errata; one over the walk's bound on
// bytes is refused before a byte is read, and one that alone holds more
// markers than the walk's bound is refused, so the run keeps the refusal and
// not the markers. A symlink, which only files: naming it outright brings
// here (Files), is refused: a walk reads each file under its own name, and
// git tracks the link's target, if it tracks it, under its own.
func readFile(r *core.Reader, name string, limit int64, b Bounds) (fileMarkers, error) {
	src := core.Source{File: name, Limit: math.MaxInt64}
	at, err := r.Resolved(src)
	if err != nil {
		return fileMarkers{}, fmt.Errorf("%s: %w", Kind.Key, err)
	}
	if at != name {
		// The link may be a directory on the way, which git tracks and the
		// name does not (markers delta review 2, E3).
		is := "is a symlink"
		if dir := path.Dir(name); dir != "." {
			if d, err := r.Resolved(core.Source{File: dir, Limit: math.MaxInt64}); err == nil && d != dir {
				is = "is reached through a symlink"
			}
		}
		return fileMarkers{}, fmt.Errorf("%s: %s %s git tracks, to %s, and a marker walk reads each file under its own name; list %s in files instead, or narrow files to leave the link out", Kind.Key, name, is, at, at)
	}
	f, size, err := r.Open(src)
	if err != nil {
		return fileMarkers{}, err
	}
	defer f.Close()
	if size > b.Bytes {
		return fileMarkers{}, errBytes(name)
	}
	m := fileMarkers{size: size}
	if size > limit {
		holds, err := contains(io.LimitReader(f, size), name)
		if err != nil {
			return fileMarkers{}, err
		}
		if holds {
			return fileMarkers{}, fmt.Errorf("%s: %s is %d bytes, over the limit of %d, and holds the bytes errata, so the walk cannot read its markers; set max_bytes: higher, or narrow files", Kind.Key, name, size, limit)
		}
		return m, nil
	}
	data, err := core.Source{File: name, Limit: limit}.ReadLimited(f)
	if err != nil {
		return fileMarkers{}, err
	}
	if m.found, err = Scan(name, data, limit); err != nil {
		return fileMarkers{}, err
	}
	if len(m.found) > b.Count {
		return fileMarkers{}, errCount(name)
	}
	return m, nil
}

// contains reports whether r holds token, read in pieces of chunk bytes, each
// with the bytes of the piece before it that could start the token.
func contains(r io.Reader, name string) (bool, error) {
	buf := make([]byte, chunk+len(token))
	kept := 0
	for {
		n, err := io.ReadFull(r, buf[kept:])
		if bytes.Contains(buf[:kept+n], token) {
			return true, nil
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read %s: %w", name, err)
		}
		kept = copy(buf, buf[kept+n-(len(token)-1):kept+n])
	}
}

// IsMarkdown reports whether file is read as Markdown: its extension, in
// any case, is .md or .markdown.
func IsMarkdown(file string) bool {
	ext := strings.ToLower(path.Ext(file))
	return ext == ".md" || ext == ".markdown"
}

// Scan is the markers of data, the bytes of file, which reads at most limit
// bytes: as Markdown for a Markdown file, through the comments of the
// lexer's row for a file the table lists, and refused when it holds the
// bytes of a marker and is neither.
func Scan(file string, data []byte, limit int64) ([]Found, error) {
	if !bytes.Contains(data, token) {
		return nil, nil
	}
	if IsMarkdown(file) {
		return scanMarkdown(file, data, limit)
	}
	data = core.LineFeeds(data)
	lang, ok := core.LanguageFor(file)
	if !ok {
		if at := markerish(data, 0, len(data)); at >= 0 {
			return nil, fmt.Errorf("%s: %s: line %d: a marker, in a file errata reads no markers in: it reads them in Markdown and in the comments of the languages its comment lexer lists (docs/reference.md#marker)",
				Kind.Key, file, core.NewLineCursor(data).At(at))
		}
		return nil, nil
	}
	if lang.Family == "markup" {
		return scanMarkup(file, data, lang)
	}
	return scanCode(file, data, lang)
}

// What a comment's text is to the walk.
const (
	notMarker = iota
	opener
	closer
)

// How an opener's text starts, and a closer's whole text.
const (
	openWord  = "errata:"
	closeWord = "/errata"
)

// classify reads the text of an HTML comment, between its <!-- and its -->:
// an opener, errata: and an id; a closer, /errata; or no marker. Text that
// starts with errata: or /errata, its blanks trimmed, and is neither does
// not parse.
func classify(text []byte) (kind int, id string, err error) {
	t := string(trim(text))
	if rest, ok := strings.CutPrefix(t, openWord); ok {
		id = string(trim([]byte(rest)))
		if !check.ValidID(id) {
			return 0, "", fmt.Errorf("the marker %s names no id a rule may have: lower-case words joined by single hyphens", core.Quote(t))
		}
		return opener, id, nil
	}
	if t == closeWord {
		return closer, "", nil
	}
	if strings.HasPrefix(t, closeWord) {
		return 0, "", fmt.Errorf("the marker %s does not parse: a marker closes with <!-- /errata --> and nothing more", core.Quote(t))
	}
	return notMarker, "", nil
}

// markerish is the offset of the first <!-- in data[from:to] whose text
// starts as a marker's, errata: or /errata after blanks, or -1.
func markerish(data []byte, from, to int) int {
	for i := from; i < to; {
		j := bytes.Index(data[i:to], []byte("<!--"))
		if j < 0 {
			return -1
		}
		at := i + j
		// The blanks after the <!-- only: trimming the end of the rest too
		// would read it again for each <!--.
		rest := bytes.TrimLeft(data[at+4:to], " \t\n\f\r")
		if bytes.HasPrefix(rest, []byte(openWord)) || bytes.HasPrefix(rest, []byte(closeWord)) {
			return at
		}
		i = at + 4
	}
	return -1
}

// countMarkerish is how many <!-- of data[from:to] markerish finds.
func countMarkerish(data []byte, from, to int) int {
	n := 0
	for at := markerish(data, from, to); at >= 0; at = markerish(data, at+4, to) {
		n++
	}
	return n
}

// errStray refuses a marker's <!-- that is no comment a reader of the walk
// reads as a marker: one no --> closes, or one inside another comment.
func errStray(file string, line int) error {
	return fmt.Errorf("%s: %s: line %d: a marker's <!-- that no --> closes, or that stands inside another comment, so it is no marker the walk can read", Kind.Key, file, line)
}

// mark is one opener or closer in a text: its id, and where its <!-- starts
// and its --> ends.
type mark struct {
	kind       int
	id         string
	start, end int
}

// pair is an opener and the closer after it.
type pair struct{ open, close mark }

// pairs pairs marks, in order, each opener with the closer after it, at the
// lines line gives. Markers do not nest: an opener while one is open, a
// closer with none open and an opener left open at the end are refused.
func pairs(file string, marks []mark, line func(int) int) ([]pair, error) {
	var out []pair
	var open *mark
	for i := range marks {
		m := marks[i]
		switch {
		case m.kind == opener && open != nil:
			return nil, fmt.Errorf("%s: %s: line %d: the marker %s opens, and nothing closes it before the marker %s opens at line %d; close each marker with <!-- /errata --> before the next opens",
				Kind.Key, file, line(open.start), open.id, m.id, line(m.start))
		case m.kind == opener:
			open = &marks[i]
		case open == nil:
			return nil, fmt.Errorf("%s: %s: line %d: <!-- /errata --> closes no marker", Kind.Key, file, line(m.start))
		default:
			out = append(out, pair{*open, m})
			open = nil
		}
	}
	if open != nil {
		return nil, fmt.Errorf("%s: %s: line %d: the marker %s opens and no <!-- /errata --> after it closes it, so errata cannot tell where its value ends",
			Kind.Key, file, line(open.start), open.id)
	}
	return out, nil
}

// isBlank reports whether b is a blank: a space, a tab, a line feed, a form
// feed or a carriage return.
func isBlank(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\f' || b == '\r' }

// trim is b without the blanks at either end.
func trim(b []byte) []byte {
	start, end := markup.TrimBlanks(b, 0, len(b))
	return b[start:end]
}

// clean is a value's text: b with the blanks at either end left out and
// each run of blanks inside it read as one space, so a value a line wrap
// splits reads as one line. With stars, a * that starts a line after its
// blanks, the decoration of a block comment, is left out too. first is the
// offset in b of the value's first byte, or -1 for an empty value.
func clean(b []byte, stars bool) (text string, first int) {
	var out []byte
	first = -1
	space, lineStart := false, false
	for i, c := range b {
		switch {
		case c == '\n':
			space, lineStart = true, true
			continue
		case isBlank(c):
			space = true
			continue
		case lineStart && stars && c == '*' && (i+1 == len(b) || isBlank(b[i+1])):
			lineStart = false
			continue
		}
		lineStart = false
		if first < 0 {
			first = i
		} else if space {
			out = append(out, ' ')
		}
		space = false
		out = append(out, c)
	}
	return string(out), first
}

// found is the marker of p, whose value is text with its first byte at
// offset first of the file, or -1 for an empty value, at the lines lines
// gives.
func found(file string, p pair, text string, first int, lines *core.LineCursor) Found {
	open := lines.At(p.open.start)
	at := open
	if first >= 0 {
		at = lines.At(first)
	}
	return Found{
		Rule:  p.open.id,
		Value: check.Value{Text: text, At: check.Location{File: file, Line: at}},
		Open:  check.Location{File: file, Line: open},
	}
}

// scanMarkup is the markers of a file of HTML or XML: each comment the lexer
// finds is one, and a value is the text of the file between an opener and
// its closer, tags and all.
func scanMarkup(file string, data []byte, lang core.Language) ([]Found, error) {
	var marks []mark
	lines := core.NewLineCursor(data)
	for s := range lang.Spans(data) {
		if err := core.CheckSpan(file, data, s); err != nil {
			return nil, fmt.Errorf("%s: %w", Kind.Key, err)
		}
		if s.Kind != core.SpanComment {
			continue
		}
		kind, id, err := classify(data[s.BodyStart:s.BodyEnd])
		if err != nil {
			return nil, fmt.Errorf("%s: %s: line %d: %w", Kind.Key, file, lines.At(s.Start), err)
		}
		if n := countMarkerish(data, s.Start, s.End); n > 1 || n == 1 && kind == notMarker {
			return nil, errStray(file, lines.At(s.Start))
		}
		if kind != notMarker {
			marks = append(marks, mark{kind: kind, id: id, start: s.Start, end: s.End})
		}
	}
	ps, err := pairs(file, marks, lines.At)
	if err != nil {
		return nil, err
	}
	var out []Found
	for _, p := range ps {
		text, first := clean(data[p.open.end:p.close.start], false)
		if first >= 0 {
			first += p.open.end
		}
		out = append(out, found(file, p, text, first, lines))
	}
	return out, nil
}

// comment is one comment of a code file: its span, and where its body
// starts in the text of the comments.
type comment struct {
	span core.Span
	at   int
}

// scanCode is the markers of a code file: the opening and the closing HTML
// comment written inside a comment of its language, each whole in one. The
// comments' bodies are read one after another with a line feed between
// two, so a value may run from one comment into the next when only blanks
// stand between them in the file, as in a run of line comments.
func scanCode(file string, data []byte, lang core.Language) ([]Found, error) {
	var (
		text     []byte
		comments []comment
		marks    []mark
		of       []int // the comment of each mark
	)
	lines := core.NewLineCursor(data)
	for s := range lang.Spans(data) {
		if err := core.CheckSpan(file, data, s); err != nil {
			return nil, fmt.Errorf("%s: %w", Kind.Key, err)
		}
		if s.Kind != core.SpanComment {
			continue
		}
		if len(comments) > 0 {
			text = append(text, '\n')
		}
		c := comment{span: s, at: len(text)}
		text = append(text, data[s.BodyStart:s.BodyEnd]...)
		found, err := bodyMarks(file, text, c.at, len(text), lines, s.BodyStart)
		if err != nil {
			return nil, err
		}
		for range found {
			of = append(of, len(comments))
		}
		marks = append(marks, found...)
		comments = append(comments, c)
	}
	// offset is the offset in the file of off, an offset in the text inside
	// the body of comment k or at its end.
	offset := func(k, off int) int { return comments[k].span.BodyStart + off - comments[k].at }
	ps, err := pairs(file, marks, func(off int) int {
		k := 0
		for k+1 < len(comments) && comments[k+1].at <= off {
			k++
		}
		return lines.At(offset(k, off))
	})
	if err != nil {
		return nil, err
	}
	// Markers do not nest, so the marks pair in order: pair i is marks 2i
	// and 2i+1.
	var out []Found
	for i, p := range ps {
		a, b := of[2*i], of[2*i+1]
		for k := a; k < b; k++ {
			if gap := data[comments[k].span.End:comments[k+1].span.Start]; len(trim(gap)) > 0 {
				return nil, fmt.Errorf("%s: %s: line %d: the value of the marker %s runs out of its comment into code at line %d; close the marker in the comment it opens in, or in a comment after it with only blanks between",
					Kind.Key, file, lines.At(offset(a, p.open.start)), p.open.id, lines.At(comments[k].span.End))
			}
		}
		text, first := clean(text[p.open.end:p.close.start], true)
		if first >= 0 {
			off := p.open.end + first
			k := a
			for k < b && comments[k+1].at <= off {
				k++
			}
			first = offset(k, off)
		}
		out = append(out, foundAt(file, p, text, first, offset(a, p.open.start), lines))
	}
	return out, nil
}

// foundAt is found with the opener at offset open of the file.
func foundAt(file string, p pair, text string, first, open int, lines *core.LineCursor) Found {
	p.open.start = open
	return found(file, p, text, first, lines)
}

// bodyMarks is the markers of text[from:to], one comment's body: each <!--
// whose text is a marker's, ended by a --> in the same body. A marker's
// <!-- whose comment ends before its --> is refused. base is the offset in
// the file of text[from], for a message's line.
func bodyMarks(file string, text []byte, from, to int, lines *core.LineCursor, base int) ([]mark, error) {
	var out []mark
	for i := from; ; {
		at := markerish(text, i, to)
		if at < 0 {
			return out, nil
		}
		end := bytes.Index(text[at+4:to], []byte("-->"))
		if end < 0 {
			return nil, fmt.Errorf("%s: %s: line %d: a marker's <!-- opens here, and its comment ends before a --> closes it", Kind.Key, file, lines.At(base+at-from))
		}
		kind, id, err := classify(text[at+4 : at+4+end])
		if err != nil {
			return nil, fmt.Errorf("%s: %s: line %d: %w", Kind.Key, file, lines.At(base+at-from), err)
		}
		out = append(out, mark{kind: kind, id: id, start: at, end: at + 4 + end + 3})
		i = at + 4 + end + 3
	}
}

// scanMarkdown is the markers of a Markdown file: the HTML comments
// goldmark reads as HTML, an inline one or a comment block, so a marker in a
// code block or a code span is none. A value is the prose between an opener
// and its closer, as the text extractor reads it, and stays in one block.
func scanMarkdown(file string, data []byte, limit int64) ([]Found, error) {
	doc, err := mdparse.Parse(file, data, limit, mdparse.Time(limit))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Kind.Key, err)
	}
	src := doc.Src
	lines := core.NewLineCursor(src)
	var marks []mark
	walkErr := ast.Walk(doc.Root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var found []mark
		var err error
		switch h := n.(type) {
		case *ast.RawHTML:
			found, err = rawMarks(file, src, h, lines)
		case *ast.HTMLBlock:
			found, err = blockMarks(file, doc, h, lines)
		case *ast.CodeSpan, *ast.CodeBlock:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			// A <!-- that Markdown reads as text: no --> closes it.
			if s := h.Value.Index(); markerish(src, s.Start, s.Stop) >= 0 {
				err = errStray(file, lines.At(markerish(src, s.Start, s.Stop)))
			}
		default:
			return ast.WalkContinue, nil
		}
		if err != nil {
			return ast.WalkStop, err
		}
		marks = append(marks, found...)
		return ast.WalkSkipChildren, nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if len(marks) == 0 {
		return nil, nil
	}
	ps, err := pairs(file, marks, lines.At)
	if err != nil {
		return nil, err
	}
	prose, err := doc.Prose(false)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Kind.Key, err)
	}
	var out []Found
	var cells map[int]ast.Node // read when a value holds a |
	k := 0                     // the offset of the prose the walk is at; the pairs are in file order
	for _, p := range ps {
		for k < len(prose.Text) && prose.Offset(k) < p.open.end {
			k++
		}
		start := k
		for k < len(prose.Text) && prose.Offset(k) < p.close.start {
			k++
		}
		from, to := markup.TrimBlanks(prose.Text, start, k)
		if bytes.IndexByte(prose.Text[from:to], '\n') >= 0 {
			return nil, fmt.Errorf("%s: %s: line %d: the value of the marker %s runs from one block into the next; close the marker in the paragraph, heading or list item it opens in",
				Kind.Key, file, lines.At(p.open.start), p.open.id)
		}
		if bytes.IndexByte(prose.Text[from:to], '|') >= 0 {
			if cells == nil {
				if cells, err = tableCells(file, doc.Src, limit); err != nil {
					return nil, err
				}
			}
			if cells[p.open.start] != cells[p.close.start] {
				return nil, fmt.Errorf("%s: %s: line %d: the value of the marker %s runs from one table cell into the next; close the marker in the cell it opens in",
					Kind.Key, file, lines.At(p.open.start), p.open.id)
			}
		}
		// The prose from from is trimmed, so the value starts at from.
		text, first := clean(prose.Text[from:to], false)
		if first >= 0 {
			first = prose.Offset(from)
		}
		out = append(out, found(file, p, text, first, lines))
	}
	return out, nil
}

// tableCells is, for the offset of each inline HTML comment of src, a
// Markdown file, that stands in a cell of a GitHub table, the cell: src
// parsed again with tables (mdparse.ParseTables), which the walk's own
// parse reads as paragraphs, so a value that joins two cells is told from
// one that holds a |.
func tableCells(file string, src []byte, limit int64) (map[int]ast.Node, error) {
	doc, err := mdparse.ParseTables(file, bytes.Clone(src), limit, mdparse.Time(limit))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Kind.Key, err)
	}
	cells := map[int]ast.Node{}
	var cell ast.Node
	err = ast.Walk(doc.Root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if n.Kind() == east.KindTableCell {
			if cell = nil; entering {
				cell = n
			}
		}
		if h, ok := n.(*ast.RawHTML); ok && entering && cell != nil {
			if ix := h.Value.Indices(); len(ix) > 0 {
				cells[ix[0].Start] = cell
			}
		}
		return ast.WalkContinue, nil
	})
	return cells, err
}

// rawMarks is the marker an inline HTML comment h is, if any.
func rawMarks(file string, src []byte, h *ast.RawHTML, lines *core.LineCursor) ([]mark, error) {
	ix := h.Value.Indices()
	if len(ix) == 0 {
		return nil, nil
	}
	var raw []byte
	for _, s := range ix {
		raw = append(raw, src[s.Start:s.Stop]...)
	}
	if !bytes.HasPrefix(raw, []byte("<!--")) || !bytes.HasSuffix(raw, []byte("-->")) || len(raw) < 7 {
		return nil, nil // a tag, or <!--> or <!--->, which hold no text
	}
	start := ix[0].Start
	kind, id, err := classify(raw[4 : len(raw)-3])
	if err != nil {
		return nil, fmt.Errorf("%s: %s: line %d: %w", Kind.Key, file, lines.At(start), err)
	}
	if n := countMarkerish(raw, 0, len(raw)); n > 1 || n == 1 && kind == notMarker {
		return nil, errStray(file, lines.At(start))
	}
	if kind == notMarker {
		return nil, nil
	}
	return []mark{{kind: kind, id: id, start: start, end: ix[len(ix)-1].Stop}}, nil
}

// blockMarks is the markers of an HTML block h: those of its comments for a
// comment block, CommonMark's type 2, whose text after its end on its last
// line the prose reads. A marker in any other HTML block, such as a <div>,
// whose text the prose does not read, is refused; a script or a style
// element, CommonMark's type 1 that opens no <pre>, holds no comment.
func blockMarks(file string, doc *mdparse.Doc, h *ast.HTMLBlock, lines *core.LineCursor) ([]mark, error) {
	src := doc.Src
	p := doc.NewProse()
	for _, s := range h.Value.Segments() {
		p.CopyFrom(src[s.Start:s.Stop], s.Start)
	}
	if h.HTMLBlockKind != ast.HTMLBlockKind2 {
		if scriptLike(h, src) {
			return nil, nil
		}
		if at := markerish(p.Text, 0, len(p.Text)); at >= 0 {
			return nil, fmt.Errorf("%s: %s: line %d: a marker inside an HTML block other than a comment, whose text errata does not read; put the marker in a paragraph",
				Kind.Key, file, lines.At(p.Offset(at)))
		}
		return nil, nil
	}
	var out []mark
	for _, c := range markup.HTMLComments(p.Text, 0) {
		end := bytes.Index(p.Text[c.End:], []byte("-->"))
		if end < 0 {
			continue // <!--> and <!--->, which hold no text
		}
		kind, id, err := classify(p.Text[c.Start:c.End])
		if err != nil {
			return nil, fmt.Errorf("%s: %s: line %d: %w", Kind.Key, file, lines.At(p.Offset(c.Open)), err)
		}
		if kind != notMarker {
			out = append(out, mark{kind: kind, id: id, start: p.Offset(c.Open), end: p.Offset(c.End+end+2) + 1})
		}
	}
	if countMarkerish(p.Text, 0, len(p.Text)) != len(out) {
		return nil, errStray(file, lines.At(p.Offset(markerish(p.Text, 0, len(p.Text)))))
	}
	return out, nil
}

// scriptLike reports whether h is a script, style or textarea element, whose
// <!-- is no comment in HTML: an HTML block of CommonMark's type 1 that does
// not open a pre element, as the markdown extractor reads it.
func scriptLike(h *ast.HTMLBlock, src []byte) bool {
	segs := h.Value.Segments()
	if h.HTMLBlockKind != ast.HTMLBlockKind1 || len(segs) == 0 {
		return false
	}
	first := bytes.TrimLeft(src[segs[0].Start:segs[0].Stop], " \t")
	return len(first) < 4 || !strings.EqualFold(string(first[:4]), "<pre")
}

// extractor is the extractor of a spec: the values of the markers its walk
// finds that name its rule.
type extractor struct {
	walk Walk
	rule string
}

// Build refuses a spec built outside a rule, which names no rule, and what
// NewWalk refuses.
func Build(s Spec) (check.Extractor, error) {
	if s.rule == "" {
		return nil, fmt.Errorf("%s: the extractor reads the markers that name its rule, and was built outside one", Kind.Key)
	}
	w, err := NewWalk(s.Files, s.MaxBytes)
	if err != nil {
		return nil, err
	}
	return extractor{walk: w, rule: s.rule}, nil
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(ctx, root)
}

// Extract is the value of every marker under root that names the rule.
func (e extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	found, err := e.walk.Markers(ctx, root)
	if err != nil {
		return nil, err
	}
	out := []check.Value{}
	for _, f := range found {
		if f.Rule == e.rule {
			out = append(out, f.Value)
		}
	}
	return out, nil
}
