package udev

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// extractor is a built udev extractor: the file it reads, or the files a
// files: key names, the key and operator it selects, and the match keys
// that select rules.
type extractor struct {
	core.Source
	files core.Files
	limit int64
	key   name
	op    op
	tests []wanted
	// valueLimit is the most values the extractor returns, 0 for
	// core.MaxValues: a test lowers it to witness the bound.
	valueLimit int
}

// wanted is one match key of the spec and its value.
type wanted struct {
	name
	value string
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (x extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return x.Extract(ctx, root)
}

// Extract reads the file, or the files in udev's order, under root, and
// returns the values the spec selects.
func (x extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	r, err := core.OpenReader(ctx, root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	names, err := x.names(core.NewTracked(ctx, root), root)
	if err != nil {
		return nil, err
	}
	l := &loader{x: &x, reader: r, left: x.limit, meter: core.NewMeter(len(x.key.String()) + x.testsBytes())}
	for _, f := range names {
		if err := l.read(f); err != nil {
			return nil, err
		}
	}
	return l.out, nil
}

// testsBytes is the bytes of the spec's match keys and values.
func (x extractor) testsBytes() int {
	n := 0
	for _, w := range x.tests {
		n += len(w.String()) + len(w.value)
	}
	return n
}

// names are the files the spec names, those of files: in udev's order: in
// a directory named rules.d, only the .rules files whose name does not
// start with a ., read by name across the directories, of two files of one
// name only the one in the directory udev ranks first. A file that is a
// symlink to /dev/null masks the others of its name and is read as empty.
func (x extractor) names(tracked *core.Tracked, root string) ([]string, error) {
	if x.File != "" {
		return []string{x.File}, nil
	}
	named, err := tracked.List(x.files)
	if err != nil {
		return nil, err
	}
	var files []string
	masked := map[string]bool{}
	for _, n := range named {
		if passedOver(n.File) {
			continue
		}
		files = append(files, n.File)
		masked[n.File] = n.Link && toDevNull(root, n.File)
	}
	slices.SortStableFunc(files, func(a, b string) int { return dirRank(a) - dirRank(b) })
	return slices.DeleteFunc(core.FirstOfEachName(files), func(f string) bool { return masked[f] }), nil
}

// passedOver reports a file of a directory named rules.d that udev does
// not read: one that is no .rules file, once a template suffix is taken
// off, or whose name starts with a dot.
func passedOver(f string) bool {
	dirs, base := core.FileParts(core.Untemplated(f))
	return dirs[len(dirs)-1] == "rules.d" && (!strings.HasSuffix(base, ".rules") || strings.HasPrefix(base, "."))
}

// toDevNull reports a symlink under root that leads to /dev/null.
func toDevNull(root, f string) bool {
	to, err := os.Readlink(filepath.Join(root, filepath.FromSlash(f)))
	return err == nil && to == os.DevNull
}

// dirRanks are the directories above rules.d in the order udev ranks them:
// /etc, /run, /usr/local/lib and /usr/lib, systemd 257's CONF_PATHS, and
// lib/udev with /usr/lib, as /lib leads there on a merged /usr. The longer
// of two that end alike is tried first.
var dirRanks = []struct {
	dir  string
	rank int
}{{"usr/local/lib/udev", 2}, {"usr/lib/udev", 3}, {"etc/udev", 0}, {"run/udev", 1}, {"lib/udev", 3}}

// dirRank is the rank of the directory f is in, len(dirRanks) for one udev
// does not rank.
func dirRank(f string) int {
	dirs, _ := core.FileParts(f)
	if dirs[len(dirs)-1] != "rules.d" {
		return len(dirRanks)
	}
	above := strings.Join(dirs[:len(dirs)-1], "/")
	for _, r := range dirRanks {
		if above == r.dir || strings.HasSuffix(above, "/"+r.dir) {
			return r.rank
		}
	}
	return len(dirRanks)
}

// loader reads the files of one extraction.
type loader struct {
	x      *extractor
	reader *core.Reader
	left   int64 // the bytes the files still to read may hold
	meter  *core.Meter
	out    []check.Value
}

// read reads file and adds the values the spec selects in it.
func (l *loader) read(file string) error {
	data, err := l.reader.Read(core.Source{File: file, Limit: l.left})
	switch {
	case errors.Is(err, core.ErrTooLarge) && l.left < l.x.limit:
		return fmt.Errorf("%w; the files the extractor reads hold at most max_bytes, %d, in all", err, l.x.limit)
	case err != nil:
		return err
	}
	l.left -= int64(len(data))
	l.meter.Budget += core.WorkPerByte * int64(len(data))
	f := &fileRead{l: l, file: file, data: data, notLiteral: core.TemplateNotLiteral(file, nil)}
	return named(file, f.run())
}

// named names file in err.
func named(file string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, core.ErrWork):
		return fmt.Errorf("%s: reading the files and matching the spec take more than %d units of work for each byte of them, the limit; narrow the spec", file, core.WorkPerByte)
	}
	return fmt.Errorf("%s: %w", file, err)
}

// fileRead is the read of one file: its rules, and its template's blocks
// and test.
type fileRead struct {
	l          *loader
	file       string
	data       []byte
	rules      []*rule
	blocks     *core.Blocks
	notLiteral func(*core.Entry) bool
}

// run reads the file's rules, the GOTO of each to its LABEL, and the
// values of each rule udev may reach.
func (f *fileRead) run() error {
	var err error
	if f.blocks, err = core.TemplateBlocks(f.file, f.data, f.l.meter); err != nil {
		return err
	}
	err = eachRule(f.data, f.l.meter, func(lg *logical) error {
		r, err := readRule(lg, f.l.meter)
		if r != nil {
			f.rules = append(f.rules, r)
		}
		return err
	})
	if err != nil {
		return err
	}
	fl := newFlow(f.rules, resolveGotos(f.rules), f.l.meter)
	for i, r := range f.rules {
		ways, err := fl.enter(i)
		if err != nil {
			return err
		}
		if err := f.take(r, ways, fl); err != nil {
			return err
		}
		if err := fl.leave(i, r); err != nil {
			return err
		}
	}
	return nil
}

// take adds the values the spec selects in r, which udev reaches by ways
// past the GOTOs of fl. A rule no way reaches gives no value; one reached
// by several ways, which no condition states, is refused where a way
// selects it.
func (f *fileRead) take(r *rule, ways []way, fl *flow) error {
	if len(ways) == 0 || !slices.ContainsFunc(r.assigns, f.l.x.wants) {
		return nil
	}
	if err := fl.charge(ways); err != nil {
		return err
	}
	conds, selected := f.l.x.selecting(r, ways, fl)
	switch {
	case !selected:
		return nil
	case len(ways) > 1:
		t := r.assigns[slices.IndexFunc(r.assigns, f.l.x.wants)]
		return core.LineError(r.l.lineOf(t.off), "%s is in a rule udev reaches by %d ways, past or by the jumps of the GOTOs at lines %s; errata cannot state when udev applies it", t, len(ways), fl.gotoLines(ways))
	}
	for _, t := range r.assigns {
		if !f.l.x.wants(t) {
			continue
		}
		if err := f.add(r, t, conds); err != nil {
			return err
		}
	}
	return nil
}

// selecting is the conditions of a way of ways by which the spec selects
// r, and whether one does.
func (x *extractor) selecting(r *rule, ways []way, fl *flow) (conds []cond, selected bool) {
	for _, w := range ways {
		if c := fl.conds(w); x.selects(c, r) {
			conds, selected = c, true
		}
	}
	return conds, selected
}

// add adds the value of t, an assignment of r under the conditions conds
// the GOTOs before r add.
func (f *fileRead) add(r *rule, t *token, conds []cond) error {
	l := f.l
	if len(l.out) == l.x.maxValues() {
		return core.TooManyValues(f.file)
	}
	line := r.l.lineOf(t.off)
	inBlock, err := f.blocks.Holds(line)
	if err != nil {
		return err
	}
	var all []string
	for _, c := range conds {
		all = append(all, c.text)
	}
	for _, m := range r.matches {
		all = append(all, m.String())
	}
	v := check.Value{Text: applied(t), At: check.Location{File: f.file, Line: line}, Condition: strings.Join(all, ", "), Key: name{t.key, t.attr, t.hasAttr}.String() + t.op.String(), HasKey: true}
	v.NotLiteral = formats(t) && substitutes(t.value) || f.notLiteral != nil && f.notLiteral(&core.Entry{Value: []byte(t.value), InBlock: inBlock})
	l.out = append(l.out, v)
	return nil
}

// maxValues is the most values the extractor returns.
func (x *extractor) maxValues() int {
	if x.valueLimit == 0 {
		return core.MaxValues
	}
	return x.valueLimit
}

// wants reports whether the assignment t is of the spec's key, and its
// operator when the spec names one.
func (x *extractor) wants(t *token) bool {
	return name{t.key, t.attr, t.hasAttr}.canonical() == x.key && (x.op == 0 || t.op == x.op)
}

// selects reports whether the spec selects r, whose GOTOs before it test
// the keys in conds: every match key of the spec is one r or a GOTO before
// it tests with == and the value as written, in any case for a value
// written i"...".
func (x *extractor) selects(conds []cond, r *rule) bool {
	for _, w := range x.tests {
		if !slices.ContainsFunc(r.matches, w.is) && !slices.ContainsFunc(conds, func(c cond) bool { return c.tok != nil && w.is(c.tok) }) {
			return false
		}
	}
	return true
}

// is reports whether t tests w's key with == and w's value.
func (w wanted) is(t *token) bool {
	if t.op != opMatch || (name{t.key, t.attr, t.hasAttr}) != w.name {
		return false
	}
	return t.value == w.value || t.fold && asciiFold(t.value) == asciiFold(w.value)
}

// asciiFold is s with its ASCII capitals in lower case, as udev folds a
// value written i"...", and every other byte as it is.
func asciiFold(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// formatChars are the letters after % and formatNames the names after $
// that udev substitutes in a value (udev-format.c).
const formatChars = "NsEknbdpMmcPDLrS"

var formatNames = []string{"devnode", "tempnode", "attr", "sysfs", "env", "kernel", "number", "driver", "devpath", "id", "major", "minor", "result", "parent", "name", "links", "root", "sys"}

// formats reports an assignment whose value udev formats before it
// applies it: any but OPTIONS, which udev parses when it loads the rules
// and never formats.
func formats(t *token) bool { return t.key != "OPTIONS" }

// applied is the value of t as udev applies it, where it substitutes
// nothing in it: each %% a % and each $$ a $, as udev_event_apply_format
// copies them. A value it substitutes in is not literal and keeps the text
// as written, and so does a value udev does not format.
func applied(t *token) string {
	if !formats(t) || substitutes(t.value) {
		return t.value
	}
	return undouble(t.value)
}

// undouble is v with each %% read as % and each $$ as $.
func undouble(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		b.WriteByte(v[i])
		if i+1 < len(v) && v[i+1] == v[i] && (v[i] == '%' || v[i] == '$') {
			i++
		}
	}
	return b.String()
}

// substitutes reports a value udev substitutes in before it applies it: a
// % before one of formatChars, or a $ before one of formatNames. %% and $$
// are a % and a $ (applied); a pair of any other byte starts no
// substitution either, so every pair is passed over.
func substitutes(v string) bool {
	for i := 0; i < len(v)-1; i++ {
		if v[i+1] == v[i] {
			i++
			continue
		}
		if formatAt(v, i) {
			return true
		}
	}
	return false
}

// formatAt reports a substitution at i of v: a % before a format letter,
// or a $ before a format name.
func formatAt(v string, i int) bool {
	if v[i] == '%' {
		return strings.IndexByte(formatChars, v[i+1]) >= 0
	}
	return v[i] == '$' && slices.ContainsFunc(formatNames, func(n string) bool { return strings.HasPrefix(v[i+1:], n) })
}
