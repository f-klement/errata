package etc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// includeKind is how a keyword's value names the files it reads in place.
type includeKind uint8

const (
	includeGlob      includeKind = iota // each word a file or a glob: OpenSSH's Include, chrony's include
	includePath                         // each word a file, or a directory whose every file is read: logrotate's include, ntp's includefile
	includeConfDir                      // each word a directory whose .conf files are read by name across them: chrony's confdir
	includeSourceDir                    // the same for .sources files, which hold only sources: chrony's sourcedir
)

// maxDepth is how deep includes nest, the limit of sshd and ssh
// (SERVCONF_MAX_DEPTH and READCONF_MAX_DEPTH), which a file that includes
// itself reaches too.
const maxDepth = 16

// extractor is a built etc extractor: the file it reads, or the drop-ins a
// files: key names.
type extractor struct {
	core.Source
	files core.Files
	limit int64
	d     *dialect
	key   string
	block *string
	match connection
	// valueLimit is the most values the extractor returns, 0 for
	// core.MaxValues: a test lowers it to witness the bound.
	valueLimit int
}

// hit is one value the spec selects, as the file wrote it.
type hit struct {
	value      string
	at         check.Location
	blocks     []block // the blocks it is under, outermost first
	condition  string
	notLiteral bool
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (x extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return x.Extract(ctx, root)
}

// Extract reads the file, or the drop-ins in the tool's order, and the
// files they include, under root, and returns the values the spec selects.
func (x extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	r, err := core.OpenReader(ctx, root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	l := &loader{x: &x, reader: r, tracked: core.NewTracked(ctx, root), left: x.limit, meter: core.NewMeter(len(x.key) + len(x.match.String()))}
	names, err := x.names(l.tracked)
	if err != nil {
		return nil, err
	}
	for _, f := range names {
		l.top = fileDir(f)
		if x.d.homes != nil {
			l.homes = x.d.homes(f)
		}
		if err := l.read(f, nil, 0, false); err != nil {
			return nil, err
		}
	}
	hits, err := x.selected(l)
	if err != nil {
		return nil, err
	}
	out := make([]check.Value, 0, len(hits))
	for _, h := range hits {
		out = append(out, check.Value{Text: h.value, At: h.at, Condition: h.condition, NotLiteral: h.notLiteral})
	}
	return out, nil
}

// names are the files the spec names, the drop-ins of files: in the tool's
// order.
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
	return x.d.order(names), nil
}

// selected are the hits l read that the spec selects: every one, or with
// match: those OpenSSH applies.
func (x extractor) selected(l *loader) ([]hit, error) {
	if x.match == nil {
		return l.hits, nil
	}
	hits, err := x.applied(l.hits, l.setters, l.meter)
	if errors.Is(err, core.ErrWork) {
		return nil, tooMuchWork(x.File)
	}
	return hits, err
}

// maxValues is the most values the extractor returns.
func (x *extractor) maxValues() int {
	if x.valueLimit == 0 {
		return core.MaxValues
	}
	return x.valueLimit
}

// loader reads the files of one extraction.
type loader struct {
	x       *extractor
	reader  *core.Reader
	tracked *core.Tracked
	top     string // the directory of the file the spec names, below which a relative include is read
	homes   []home // the tool's homes, for the file the spec names
	left    int64  // the bytes the files still to read may hold
	meter   *core.Meter
	files   int // the files read
	// seq counts the lines the loader visited, across files, in the
	// order the tool reads them, and setters are the lines that set a
	// keyword the dialect's shadows name, in that order.
	seq     int
	setters []setter
	// hits are the values selected so far. Each is a part of a file, and
	// the files hold at most max_bytes in all, so the values do too.
	hits []hit
}

// read reads file and the files it includes, under the blocks outer, at
// depth includes down. optional is true for a file an include names
// outright, which the tool passes over when it is not there.
func (l *loader) read(file string, outer []block, depth int, optional bool) error {
	if err := l.count(file, depth); err != nil {
		return err
	}
	data, found, err := l.load(file, optional)
	if !found || err != nil {
		return err
	}
	r := &reading{file: file, outer: outer, depth: depth, notLiteral: core.TemplateNotLiteral(file, nil)}
	if r.blocks, err = core.TemplateBlocks(file, data, l.meter); err != nil {
		return l.fail(file, err)
	}
	err = l.x.d.read(data, l.meter, func(e *entry) error { return l.visit(r, e) })
	return l.fail(file, err)
}

// reading is one file a loader reads: where it is, the blocks and the
// depth of the include that read it, and its template's blocks and test.
type reading struct {
	file       string
	outer      []block
	headerSeq  int // the seq of the line that opened the block the file is in
	depth      int
	blocks     *core.Blocks
	notLiteral func(*core.Entry) bool
}

// count refuses a file past the depth includes may nest, or past the files
// one extraction may read.
func (l *loader) count(file string, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("%s: includes nest more than %d deep, the limit of OpenSSH; a file that includes itself does", file, maxDepth)
	}
	if l.files++; l.files > core.MaxFiles {
		return fmt.Errorf("%s: the file and the files it includes are more than %d, the limit", l.x.File, core.MaxFiles)
	}
	return nil
}

// load reads file to the bytes the files before it left, and adds its
// bytes' budget to the meter. found is false for an optional file that is
// not there.
func (l *loader) load(file string, optional bool) (data []byte, found bool, err error) {
	data, err = l.reader.Read(core.Source{File: file, Limit: l.left})
	switch {
	case optional && errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case errors.Is(err, core.ErrTooLarge) && l.left < l.x.limit:
		return nil, false, fmt.Errorf("%w; the files the extractor reads hold at most max_bytes, %d, in all", err, l.x.limit)
	case err != nil:
		return nil, false, err
	}
	l.left -= int64(len(data))
	l.meter.Budget += core.WorkPerByte * int64(len(data))
	return data, true, nil
}

// visit takes one entry of r: an include read in place, or a value the
// spec selects kept.
func (l *loader) visit(r *reading, e *entry) error {
	l.seq++
	if e.header {
		r.headerSeq = l.seq
		return nil
	}
	in := r.outer
	if e.block != nil {
		in = append(slices.Clip(r.outer), block{l.x.d.foldBlock(string(e.block)), r.headerSeq})
	}
	if err := l.noteSetter(r, e, in); err != nil {
		return err
	}
	if k, ok := l.x.d.includes[string(e.key)]; ok {
		return l.include(e, k, in, r.depth)
	}
	if !l.selects(e) {
		return nil
	}
	if len(l.hits) == l.x.maxValues() {
		return namedError{core.TooManyValues(r.file)}
	}
	h, err := hitOf(r, l.x.d, e, in)
	l.hits = append(l.hits, h)
	return err
}

// selects reports whether the spec selects e: its key, and its block when
// the spec names one.
func (l *loader) selects(e *entry) bool {
	b := l.x.block
	switch {
	case !l.x.d.wants(e, l.x.key):
		return false
	case b == nil:
		return true
	case e.block == nil:
		return *b == ""
	}
	return l.x.d.foldBlock(string(e.block)) == *b
}

// noteSetter notes a line, under the blocks in, that sets a keyword the
// dialect's shadows name, which changes what a later Match line's
// criterion matches. The lines are held to the value limit.
func (l *loader) noteSetter(r *reading, e *entry, in []block) error {
	c, ok := l.x.d.shadows[string(e.key)]
	if !ok {
		return nil
	}
	if len(l.setters) == l.x.maxValues() {
		return namedError{fmt.Errorf("%s: the files set User, Hostname and Tag on more than %d lines, the limit", r.file, l.x.maxValues())}
	}
	l.setters = append(l.setters, setter{seq: l.seq, crit: c, key: string(e.key), at: check.Location{File: r.file, Line: e.line}, blocks: slices.Clone(in)})
	return nil
}

// block is a block a value is under: its line, folded, and the seq of
// that line among the lines the loader read.
type block struct {
	text string
	seq  int
}

// setter is a line that sets a keyword the dialect's shadows name: its
// seq, the criterion it shadows, its keyword, where it is and the blocks
// it is under.
type setter struct {
	seq    int
	crit   string
	key    string
	at     check.Location
	blocks []block
}

// hitOf is the value of e, an entry of r under the blocks in, read in d.
func hitOf(r *reading, d *dialect, e *entry, in []block) (hit, error) {
	h := hit{value: string(e.value), at: check.Location{File: r.file, Line: e.line}, blocks: slices.Clone(in)}
	texts := make([]string, len(in))
	for i, b := range in {
		texts[i] = b.text
	}
	h.condition = strings.Join(texts, "; ")
	if e.kind != nil {
		h.condition = strings.TrimSpace(h.condition + " " + string(e.kind))
	}
	inBlock, err := r.blocks.Holds(e.line)
	h.notLiteral = d.notLiteral != nil && d.notLiteral(e) ||
		r.notLiteral != nil && r.notLiteral(&core.Entry{Value: e.value, InBlock: inBlock})
	return h, err
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
	return fmt.Errorf("%s: reading the files and matching the spec take more than %d units of work for each byte of them, the limit; narrow the spec", file, core.WorkPerByte)
}

// include reads the files e's value names, as k says, in place, under the
// blocks in. A file outside the repository is refused, since errata cannot
// read what it would set. chrony's sourcedir is passed over unless the key
// is a source, the only directives its files hold.
func (l *loader) include(e *entry, k includeKind, in []block, depth int) error {
	at := fmt.Sprintf("line %d: %s %s", e.line, e.key, core.Quote(string(e.value)))
	if core.HasJinja(e.value) {
		return fmt.Errorf("%s names files a template engine computes, which errata cannot read", at)
	}
	if k == includeSourceDir && !slices.Contains(chronySources, l.x.key) {
		return nil
	}
	inc := &including{l: l, at: at, in: in, depth: depth + 1}
	for _, arg := range includeWords(e.value) {
		if err := inc.arg(k, arg); err != nil {
			return err
		}
	}
	return inc.readDirs(k)
}

// including is one include's read: the line that names it, the blocks its
// files are read under, their depth, and the directories of a confdir or a
// sourcedir, which are read together.
type including struct {
	l     *loader
	at    string
	in    []block
	depth int
	dirs  []string
}

// arg reads the files one word of an include names, as k says, or keeps a
// directory of a confdir or sourcedir for readDirs.
func (inc *including) arg(k includeKind, arg string) error {
	p, alt, inside := inc.l.mapPath(arg)
	if err := inc.oneLayout(k, arg, p, alt); err != nil {
		return err
	}
	switch {
	case !inside:
		return fmt.Errorf("%s names %s, outside the repository, whose values errata cannot read; only a relative path and one under %s are read inside it, beside %s", inc.at, core.Quote(arg), hostDirs(inc.l.homes), inc.l.x.File)
	case k == includeGlob:
		return inc.glob(p)
	case k == includePath:
		return inc.path(p)
	}
	inc.dirs = append(inc.dirs, p)
	return nil
}

// oneLayout refuses an include that matches nothing at p, where errata
// reads it, while the same path below alt, the other directory the
// repository may keep it in, matches files: the values there would be
// missing without a word.
func (inc *including) oneLayout(k includeKind, arg, p, alt string) error {
	if alt == "" {
		return nil
	}
	here, err := inc.matches(k, p)
	if err != nil || here {
		return err
	}
	there, err := inc.matches(k, alt)
	if err != nil || !there {
		return err
	}
	return fmt.Errorf("%s names %s, which errata reads at %s, where no file is, while %s holds files; errata cannot tell which directory stands for the host's, so move the files to %s, or name them by a path relative to %s", inc.at, core.Quote(arg), core.Quote(p), core.Quote(alt), core.Quote(p), inc.l.x.File)
}

// matches reports whether the include p, read as k says, names a file the
// repository holds.
func (inc *including) matches(k includeKind, p string) (bool, error) {
	switch {
	case k == includeConfDir:
		p = path.Join(p, "*.conf")
	case k == includeSourceDir:
		p = path.Join(p, "*.sources")
	case !core.HasGlob(p):
		_, err := inc.l.reader.Stat(core.Source{File: p})
		return err == nil, nil
	}
	files, err := inc.l.list(inc.at, p)
	return len(files) > 0, err
}

// glob reads the file p names, passed over when it is not there, or the
// files the glob p matches.
func (inc *including) glob(p string) error {
	if !core.HasGlob(p) {
		return inc.l.read(p, inc.in, inc.depth, !inc.l.x.d.mustInclude)
	}
	files, err := inc.l.list(inc.at, p)
	if err != nil {
		return err
	}
	return inc.readAll(files)
}

// path reads the file at p, passed over when it is not there, or every
// file of the directory at p but those logrotate passes over.
func (inc *including) path(p string) error {
	err := inc.l.read(p, inc.in, inc.depth, true)
	if !errors.Is(err, core.ErrDirectory) {
		return err
	}
	files, err := inc.l.listAll(inc.at, path.Join(p, "*"))
	if err != nil {
		return err
	}
	return inc.readAll(slices.DeleteFunc(files, func(f string) bool { return taboo(baseName(f)) }))
}

// readDirs reads the .conf files, or for a sourcedir the .sources files,
// of the directories kept, by base name across them.
func (inc *including) readDirs(k includeKind) error {
	suffix := ".conf"
	if k == includeSourceDir {
		suffix = ".sources"
	}
	var files []string
	for _, dir := range inc.dirs {
		found, err := inc.l.list(inc.at, path.Join(dir, "*"+suffix))
		if err != nil {
			return err
		}
		files = append(files, found...)
	}
	return inc.readAll(core.FirstOfEachName(files))
}

// readAll reads files in order.
func (inc *including) readAll(files []string) error {
	for _, f := range files {
		if err := inc.l.read(f, inc.in, inc.depth, false); err != nil {
			return err
		}
	}
	return nil
}

// list is the files git tracks that glob, a path inside the repository,
// matches as glob(3) matches it, sorted as glob(3) sorts them: a ** is a
// *, and a * or a ? matches no . at the start of a name, so a file whose
// name starts with . is listed only where the glob's segment starts with
// one too.
func (l *loader) list(at, glob string) ([]string, error) {
	for strings.Contains(glob, "**") {
		glob = strings.ReplaceAll(glob, "**", "*")
	}
	files, err := l.listAll(at, glob)
	return slices.DeleteFunc(files, func(f string) bool { return hidden(glob, f) }), err
}

// listAll is the files git tracks that glob matches as core's matcher
// matches it, sorted: for a directory a tool reads with readdir, as
// logrotate reads an included one, which a name that starts with . is in.
func (l *loader) listAll(at, glob string) ([]string, error) {
	f, err := core.CheckFiles(Kind.Key, []string{glob}, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	named, err := l.tracked.List(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	var out []string
	for _, n := range named {
		out = append(out, n.File)
	}
	slices.Sort(out)
	return out, nil
}

// hidden reports a file that glob matches only as core's matcher reads it:
// one of its names starts with a . where the glob's segment does not.
func hidden(glob, file string) bool {
	globs, names := strings.Split(glob, "/"), strings.Split(file, "/")
	for i, name := range names {
		if strings.HasPrefix(name, ".") && i < len(globs) && !strings.HasPrefix(globs[i], ".") {
			return true
		}
	}
	return false
}

// mapPath is where in the repository the tool's path arg is: a relative
// path below the directory of the file the spec names, and a path from /
// or ~ under one of the dialect's homes below the directory that stands
// for it, with the other directory a repository may keep it in (home.alt).
// A path under no home and one that climbs out of the repository are
// outside it.
func (l *loader) mapPath(arg string) (p, alt string, inside bool) {
	p, inside = path.Join(l.top, arg), true
	if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~") {
		p, alt, inside = underHome(arg, l.homes)
	}
	if _, err := core.CheckSource(Kind.Key, p, 0); err != nil || !inside {
		return "", "", false
	}
	return p, alt, true
}

// home is a directory of the host an include may name, the directory of
// the repository that stands for it, and alt, another directory a
// repository may keep its files in, "" when there is none.
type home struct{ host, repo, alt string }

// underHome is where in the repository the path arg is, under the first of
// homes it is under, the same path below the home's alt, and whether it is
// under one.
func underHome(arg string, homes []home) (p, alt string, inside bool) {
	for _, h := range homes {
		r, ok := strings.CutPrefix(arg, h.host+"/")
		if arg == h.host {
			r, ok = ".", true
		}
		if ok {
			return path.Join(h.repo, r), altOf(h.alt, r), true
		}
	}
	return "", "", false
}

// altOf is rel below alt, or "" when alt is "".
func altOf(alt, rel string) string {
	if alt == "" {
		return ""
	}
	return path.Join(alt, rel)
}

// homeOf is the homes of a tool whose main file lives in host: the
// directory of the file the spec names stands for it.
func homeOf(host string) func(string) []home {
	return func(file string) []home { return []home{{host: host, repo: fileDir(file)}} }
}

// sshHomes are ssh's: ~/.ssh for a user's config, a file named config in
// a directory named .ssh, whose relative includes ssh reads below ~/.ssh;
// /etc/ssh for any other file.
func sshHomes(file string) []home {
	if dirs, base := core.FileParts(core.Untemplated(file)); base == "config" && inDir(dirs, ".ssh") {
		return []home{{host: "~/.ssh", repo: fileDir(file)}}
	}
	return []home{{host: "/etc/ssh", repo: fileDir(file)}}
}

// chronyHomes are chrony's: a file in a directory named chrony stands for
// /etc/chrony/chrony.conf, as Debian keeps it, and the directory above for
// /etc; any other file stands for /etc/chrony.conf, as Fedora keeps it,
// and its directory's chrony for /etc/chrony. A role that keeps Debian's
// chrony.conf in files/ may keep its conf.d beside it, so the directory of
// the file is each home's alt where it is not the home (oneLayout), unless
// the directory that stands for /etc is named etc (rootAlt).
func chronyHomes(file string) []home {
	dir := fileDir(file)
	if path.Base(dir) == "chrony" {
		return []home{{host: "/etc/chrony", repo: dir}, {host: "/etc", repo: path.Dir(dir), alt: rootAlt(path.Dir(dir), dir)}}
	}
	return []home{{host: "/etc/chrony", repo: path.Join(dir, "chrony"), alt: rootAlt(dir, dir)}, {host: "/etc", repo: dir}}
}

// rootAlt is alt, or "" when etc, the directory that stands for /etc, is
// named etc: a tree of a host's root states where each file lives, so its
// other directory is another real directory, not a second place for the
// same one.
func rootAlt(etc, alt string) string {
	if path.Base(etc) == "etc" {
		return ""
	}
	return alt
}

// fileDir is the directory of file, with slashes.
func fileDir(file string) string { return path.Dir(filepath.ToSlash(file)) }

// hostDirs names the homes in a message.
func hostDirs(homes []home) string {
	var names []string
	for _, h := range homes {
		names = append(names, h.host)
	}
	return strings.Join(names, " or ")
}

// includeWords are the paths an include's value names: its words, a word
// in double or single quotes taken whole, without them.
func includeWords(v []byte) []string {
	var out []string
	for len(v) > 0 {
		v = trim(v)
		if len(v) == 0 {
			break
		}
		if v[0] == '"' || v[0] == '\'' {
			if end := bytes.IndexByte(v[1:], v[0]); end >= 0 {
				out = append(out, string(v[1:end+1]))
				v = v[end+2:]
				continue
			}
		}
		w, rest := word(v)
		out = append(out, string(w))
		v = rest
	}
	return out
}

// taboo reports a file name logrotate passes over in an included
// directory: the backups and leftovers of editors and package managers
// its default tabooext and taboopat list. logrotate matches each as a
// pattern *ext with FNM_PERIOD, whose * matches no . at the start, so a
// name that starts with . is never taboo.
func taboo(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	for _, ext := range []string{",v", ".cfsaved", ".disabled", ".dpkg-bak", ".dpkg-del", ".dpkg-dist", ".dpkg-new", ".dpkg-old", ".dpkg-tmp", ".rpmnew", ".rpmorig", ".rpmsave", ".swp", ".ucf-dist", ".ucf-new", ".ucf-old", "~"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return strings.Contains(name, ".rhn-cfg-tmp-")
}
