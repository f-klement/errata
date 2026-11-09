package sudoers

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// maxDepth is how deep includes nest, sudo's limit (MAX_SUDOERS_DEPTH),
// which a file that includes itself reaches too.
const maxDepth = 128

// extractor is a built sudoers extractor: the file it reads, or the
// drop-ins a files: key names, and the setting or the user it selects.
type extractor struct {
	core.Source
	files    core.Files
	limit    int64
	defaults string
	user     string
	all      bool // grants: true, every grant whatever its user list
	// valueLimit is the most values the extractor returns, 0 for
	// core.MaxValues: a test lowers it to witness the bound.
	valueLimit int
}

// maxValues is the most values the extractor returns.
func (x *extractor) maxValues() int {
	if x.valueLimit == 0 {
		return core.MaxValues
	}
	return x.valueLimit
}

// tree is what the files of one extraction set, in the order sudo reads
// them: the Defaults values of the setting the spec names, the aliases,
// and, for user:, the user specifications.
type tree struct {
	setting    string
	keepsSpecs bool
	params     []param
	aliases    [listKinds]map[string]*alias
	specs      []userSpec
	// cases are what the files set of caseSettings: whether a Defaults
	// line with no scope turned the flag off last, and whether a scoped
	// line sets it.
	cases [2]struct{ off, scoped bool }
}

// caseSettings are sudo's flags, on by default, that match user names and
// group names in any case.
var caseSettings = [2]string{"case_insensitive_user", "case_insensitive_group"}

// noteCase notes p when it sets one of caseSettings.
func (t *tree) noteCase(p param) {
	i := slices.Index(caseSettings[:], p.name)
	switch {
	case i < 0:
	case p.scope != "":
		t.cases[i].scoped = true
	default:
		t.cases[i].off = p.text == "false"
	}
}

// folds reports, for user names and for group names, whether they may
// match in another case: unless the files turn the flag off for every
// user and no scoped line sets it. A file outside the repository may set
// it too, so a name in another case may match, and never matches.
func (t *tree) folds() [2]bool {
	var out [2]bool
	for i, c := range t.cases {
		out[i] = !c.off || c.scoped
	}
	return out
}

// keepsParam reports whether p is a value of the setting the spec names.
func (t *tree) keepsParam(p param) bool { return t.setting != "" && p.name == t.setting }

// define adds the alias name of kind. A second definition is refused:
// visudo refuses it, and sudo since 1.9.3 drops the line and keeps the
// first, so which one the host holds depends on its sudo.
func (t *tree) define(kind int, name string, a alias) error {
	if t.aliases[kind] == nil {
		t.aliases[kind] = map[string]*alias{}
	}
	if first, ok := t.aliases[kind][name]; ok {
		return core.LineError(a.at.Line, "the alias %s is defined at %s already; visudo refuses an alias defined twice, and sudo since 1.9.3 drops the second line, so errata cannot tell which one the host's sudo keeps", name, first.at)
	}
	t.aliases[kind][name] = &a
	return nil
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (x extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return x.Extract(ctx, root)
}

// Extract reads the file, or the drop-ins in sudo's order, and the files
// they include, under root, and returns the values the spec selects.
func (x extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	r, err := core.OpenReader(ctx, root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	l := &loader{x: &x, reader: r, tracked: core.NewTracked(ctx, root), left: x.limit, meter: core.NewMeter(len(x.defaults + x.user))}
	l.tree = tree{setting: x.defaults, keepsSpecs: x.user != "" || x.all}
	names, err := x.names(l.tracked)
	if err != nil {
		return nil, err
	}
	for _, f := range names {
		l.etc = etcOf(f)
		if err := l.read(f, 0); err != nil {
			return nil, err
		}
	}
	if x.defaults != "" {
		return x.defaultsValues(l.tree.params)
	}
	out, err := l.grants()
	if errors.Is(err, core.ErrWork) {
		return nil, tooMuchWork(names[0])
	}
	return out, err
}

// defaultsValues are the values of params, each with its scope as its
// condition.
func (x extractor) defaultsValues(params []param) ([]check.Value, error) {
	if len(params) > x.maxValues() {
		return nil, core.TooManyValues(params[x.maxValues()].at.File)
	}
	out := make([]check.Value, 0, len(params))
	for _, p := range params {
		out = append(out, check.Value{Text: p.text, At: p.at, Condition: p.scope, NotLiteral: p.notLiteral})
	}
	return out, nil
}

// names are the files the spec names: its file, or the drop-ins of files:
// that sudo reads, in its order.
func (x extractor) names(tracked *core.Tracked) ([]string, error) {
	if x.File != "" {
		return []string{x.File}, nil
	}
	named, err := tracked.List(x.files)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(named))
	for _, n := range named {
		names = append(names, n.File)
	}
	return dropIns(names), nil
}

// dropIns are the files of names sudo reads from a directory it includes,
// sorted: those whose name, without a template suffix, holds no . and
// does not end in ~.
func dropIns(names []string) []string {
	out := slices.DeleteFunc(slices.Clone(names), func(f string) bool {
		base := path.Base(core.Untemplated(f))
		return strings.Contains(base, ".") || strings.HasSuffix(base, "~")
	})
	slices.Sort(out)
	return out
}

// etcOf is the directory of the repository that stands for /etc, for the
// file the spec names: the directory above a sudoers.d it is in, or its
// own directory, which holds the file standing for /etc/sudoers.
func etcOf(file string) string {
	dir := path.Dir(file)
	if path.Base(dir) == "sudoers.d" {
		return path.Dir(dir)
	}
	return dir
}

// loader reads the files of one extraction.
type loader struct {
	x       *extractor
	reader  *core.Reader
	tracked *core.Tracked
	etc     string // the directory that stands for /etc
	left    int64  // the bytes the files still to read may hold
	meter   *core.Meter
	files   int // the files read
	tree    tree
}

// reading is one file a loader reads, with its template's blocks and test.
type reading struct {
	file   string
	blocks *core.Blocks
	test   func(*core.Entry) bool
}

// notLiteral reports whether text, at line, is a value a template engine
// renders.
func (r *reading) notLiteral(text string, line int) (bool, error) {
	if r.test == nil {
		return false, nil
	}
	in, err := r.blocks.Holds(line)
	return r.test(&core.Entry{Value: []byte(text), InBlock: in}), err
}

// read reads file and the files it includes, at depth includes down.
func (l *loader) read(file string, depth int) error {
	if err := l.count(file, depth); err != nil {
		return err
	}
	data, err := l.load(file)
	if err != nil {
		return err
	}
	r := &reading{file: file, test: core.TemplateNotLiteral(file, nil)}
	if r.blocks, err = core.TemplateBlocks(file, data, l.meter); err != nil {
		return l.fail(file, err)
	}
	err = statements(data, r.test != nil, l.meter, func(s *stmt) error {
		if s.include != nil {
			return l.include(r, s.include, depth)
		}
		return l.tree.statement(r, s)
	})
	return l.fail(file, err)
}

// count refuses a file past the depth includes may nest, or past the files
// one extraction may read.
func (l *loader) count(file string, depth int) error {
	if depth > maxDepth {
		return namedError{fmt.Errorf("%s: includes nest more than %d deep, the limit of sudo; a file that includes itself does", file, maxDepth)}
	}
	if l.files++; l.files > core.MaxFiles {
		return namedError{fmt.Errorf("%s: the file and the files it includes are more than %d, the limit", file, core.MaxFiles)}
	}
	return nil
}

// load reads file to the bytes the files before it left, and adds its
// bytes' budget to the meter.
func (l *loader) load(file string) ([]byte, error) {
	data, err := l.reader.Read(core.Source{File: file, Limit: l.left})
	switch {
	case errors.Is(err, core.ErrTooLarge) && l.left < l.x.limit:
		return nil, namedError{fmt.Errorf("%w; the files the extractor reads hold at most max_bytes, %d, in all", err, l.x.limit)}
	case err != nil:
		return nil, namedError{err}
	}
	l.left -= int64(len(data))
	l.meter.Budget += core.WorkPerByte * int64(len(data))
	return data, nil
}

// fail names file in err, which a read of it returned; a nested read's
// error, which names its own file, is returned as it is.
func (l *loader) fail(file string, err error) error {
	var named namedError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &named):
		return err
	case errors.Is(err, core.ErrWork):
		return namedError{tooMuchWork(file)}
	}
	return namedError{fmt.Errorf("%s: %w", file, err)}
}

// namedError is an error that names its file already.
type namedError struct{ error }

func (e namedError) Unwrap() error { return e.error }

// tooMuchWork refuses a read that spent its work budget.
func tooMuchWork(file string) error {
	return fmt.Errorf("%s: reading the files and resolving their aliases take more than %d units of work for each byte of them, the limit; narrow the spec", file, core.WorkPerByte)
}

// include reads, in place, the file or the directory inc names, in the
// file r.
func (l *loader) include(r *reading, inc *include, depth int) error {
	at := fmt.Sprintf("line %d: %s", inc.line, core.Quote(string(inc.rest)))
	arg, err := includePath(inc.rest)
	if err != nil {
		return fmt.Errorf("%s: %w", at, err)
	}
	if core.HasJinja([]byte(arg)) {
		return fmt.Errorf("%s names files a template engine computes, which errata cannot read", at)
	}
	p, inside := l.mapPath(r.file, arg)
	if !inside {
		return fmt.Errorf("%s names %s, outside the repository, whose values errata cannot read; only a relative path and one under /etc are read inside it, below %s", at, core.Quote(arg), core.Quote(l.etc))
	}
	if !inc.dir {
		return l.includeFile(at, p, depth)
	}
	files, err := l.listDir(at, p)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := l.read(f, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// includeFile reads the file p an include line names. One the repository
// does not hold is refused: the host may hold it, and sudo, which since
// 1.9.3 logs a missing include and reads on, would read what it grants.
func (l *loader) includeFile(at, p string, depth int) error {
	if _, err := l.reader.Stat(core.Source{File: p}); err != nil {
		return fmt.Errorf("%s names %s, which the repository does not hold; the host may hold it, and what it sets is not in the repository", at, core.Quote(p))
	}
	return l.read(p, depth+1)
}

// listDir is the files of the directory dir that git tracks and sudo
// reads, in its order: not those below a directory in it, and not those
// dropIns passes over.
func (l *loader) listDir(at, dir string) ([]string, error) {
	f, err := core.CheckFiles(Kind.Key, []string{path.Join(dir, "*")}, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	named, err := l.tracked.List(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	var out []string
	for _, n := range named {
		if path.Dir(n.File) == path.Clean(dir) {
			out = append(out, n.File)
		}
	}
	return dropIns(out), nil
}

// mapPath is where in the repository the path arg of an include in file
// is: a relative path below file's directory, as sudo 1.9.1 and later
// read it, and one under /etc below the directory that stands for it. A
// path elsewhere, one that climbs out of the directory that stands for
// /etc, and one that climbs out of the repository are outside.
func (l *loader) mapPath(file, arg string) (string, bool) {
	p := path.Join(path.Dir(file), arg)
	if strings.HasPrefix(arg, "/") {
		rest, ok := strings.CutPrefix(arg, "/etc/")
		if !ok {
			return "", false
		}
		p = path.Join(l.etc, rest)
	}
	if _, err := core.CheckSource(Kind.Key, p, 0); err != nil || !below(p, l.etc) {
		return "", false
	}
	return p, true
}

// below reports whether p is dir or a path under it; every path inside
// the repository is under its top, ".".
func below(p, dir string) bool {
	return dir == "." || p == dir || strings.HasPrefix(p, dir+"/")
}

// includePath is the path an include line's text names: a word, or a part
// in double quotes, a \ keeping the byte after it, with %% read as %, and
// a comment after it. A %h, the host's short name, is refused: sudo
// computes it, which errata cannot.
func includePath(rest []byte) (string, error) {
	c := &cursor{s: &stmt{text: rest}}
	p := c.word("", nil)
	c.blank()
	comment := c.peek() == '#' && !idAt(rest, c.i)
	if !c.eof() && !comment || p == "" {
		return "", errors.New("an include names one path, in double quotes when it holds white space")
	}
	return expandPercent(p)
}

// expandPercent is the path p with %% read as %; a %h, the host's name,
// is refused.
func expandPercent(p string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '%' && i+1 < len(p) {
			switch p[i+1] {
			case 'h':
				return "", errors.New("the path holds %h, the host's name, which sudo computes and errata cannot read")
			case '%':
				i++
			}
		}
		b.WriteByte(p[i])
	}
	return b.String(), nil
}
