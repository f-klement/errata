package plugin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/plugin/wire"
)

// unusable is the extractor of an entry whose plugin errata could not run:
// it returns why, so its rule is unknown and names the plugin.
type unusable struct{ err error }

func (u unusable) Extract(context.Context, string) ([]check.Value, error) { return nil, u.err }

func (u unusable) ExtractWith(context.Context, string, map[string][]check.Value) ([]check.Value, error) {
	return nil, u.err
}

// extractor runs one entry of a plugin's kind: it reads the entry's files
// as a built in reader reads them, sends their bytes in one extract
// request, and checks the values the plugin returns.
type extractor struct {
	s     *Spec
	src   core.Source
	files *core.Files
	limit int64
}

// cannotRun is the extractor that returns why s's plugin cannot run s, and
// whether it cannot: the plugin's error, when errata could not run it at
// all, or the build request's, when it failed. Such an entry loads, and its
// rule is unknown when it runs.
func (s *Spec) cannotRun() (check.Extractor, bool) {
	why := s.plugin.noKind
	if s.res.done {
		why = s.res.failed
	}
	return unusable{why}, why != nil
}

// extractor checks where s reads and returns its extractor. A spec the
// plugin refused is a config error; an entry whose plugin errata could not
// run builds the extractor that returns why.
func (s *Spec) extractor() (check.Extractor, error) {
	switch {
	case !s.res.done && s.plugin.noKind == nil:
		return nil, fmt.Errorf("%s: the entry was not sent to its plugin in a build request", s.plugin.pin.Name)
	case s.res.refused != nil:
		return nil, s.res.refused
	}
	if x, ok := s.cannotRun(); ok {
		return x, nil
	}
	x := &extractor{s: s, limit: core.MaxBytes(s.where.MaxBytes)}
	key := s.plugin.pin.Name
	if s.where.Files == nil {
		src, err := core.CheckSource(key, s.where.File, s.where.MaxBytes)
		x.src = src
		return x, err
	}
	if s.where.File != "" {
		return nil, fmt.Errorf("%s: file and files are both set; write file for one file, or files for the files a list or a glob names", key)
	}
	if len(s.where.Files) == 0 {
		return nil, fmt.Errorf("%s: files lists no entry; name a file or a glob, or write file: for one file", key)
	}
	f, err := core.CheckFiles(key, s.where.Files, s.where.MaxBytes)
	x.files = &f
	return x, err
}

// Extract is ExtractWith with no other extractor's values.
func (x *extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	return x.ExtractWith(ctx, root, nil)
}

// ExtractWith reads the entry's files under root and returns the values the
// plugin finds in them, given values, the values of each extractor its
// description reads. A files entry that names no file returns no value
// without starting the plugin.
func (x *extractor) ExtractWith(ctx context.Context, root string, values map[string][]check.Value) ([]check.Value, error) {
	files, err := x.read(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return []check.Value{}, nil
	}
	sent, err := x.values(values)
	if err != nil {
		return nil, err
	}
	req := wire.ExtractRequest{Protocol: x.s.plugin.desc.Protocol, Op: wire.OpExtract, Spec: object(x.s.res.fields), Files: files, Values: sent, MaxBytes: x.limit}
	out, stderr, err := x.s.plugin.exe.run(ctx, req, limits{timeout: extractTimeout, stdout: x.stdoutBound(files)})
	if err != nil {
		return nil, err
	}
	var reply wire.ExtractReply
	if err := wire.Decode(out, &reply); err != nil {
		return nil, x.s.plugin.refuse("extract", err, stderr)
	}
	switch {
	case reply.Error != nil:
		return nil, fmt.Errorf("plugin %s could not read its files: %s", x.s.plugin.exe.label, strconv.Quote(*reply.Error))
	case reply.NoValue != nil:
		return nil, &check.NoValue{Reason: "plugin " + x.s.plugin.exe.label + " says " + strconv.Quote(*reply.NoValue)}
	}
	got, err := x.check(reply.Values, files)
	if err != nil {
		return nil, x.s.plugin.refuse("extract", err, stderr)
	}
	return got, nil
}

// stdoutBound is the most of an extract reply's stdout errata holds: 6
// times max_bytes, 160 bytes and 6 times the longest path of the request
// for each of MaxValues values, and 64 KiB (docs/plugin-protocol.md,
// Bounds).
func (x *extractor) stdoutBound(files []wire.File) int64 {
	longest := 0
	for _, f := range files {
		longest = max(longest, len(f.Path))
	}
	return 6*x.limit + int64(check.MaxValues)*(160+6*int64(longest)) + 64<<10
}

// read reads the entry's files under root with every check a built in
// reader's files get, the files of a set at most max_bytes in all, each
// file once however many names lead to it.
func (x *extractor) read(ctx context.Context, root string) ([]wire.File, error) {
	if x.files == nil {
		data, err := x.src.Read(ctx, root)
		if err != nil {
			return nil, err
		}
		return []wire.File{{Path: x.src.File, Data: data}}, nil
	}
	named, err := x.files.List(ctx, root)
	if err != nil || len(named) == 0 {
		return nil, err
	}
	r, err := core.OpenReader(ctx, root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return x.readSet(r, named)
}

// readSet reads each of named with r, to what the files before it left of
// max_bytes.
func (x *extractor) readSet(r *core.Reader, named []core.Named) ([]wire.File, error) {
	var out []wire.File
	left := x.limit
	seen := map[string]bool{}
	for _, n := range named {
		if at, err := r.Resolved(core.Source{File: n.File}); err == nil {
			if seen[at] {
				continue
			}
			seen[at] = true
		}
		data, err := r.Read(core.Source{File: n.File, Limit: left})
		if errors.Is(err, core.ErrTooLarge) {
			return nil, fmt.Errorf("%s: %w; the files the extractor reads hold at most max_bytes, %d, in all", x.s.plugin.pin.Name, err, x.limit)
		}
		if err != nil {
			return nil, err
		}
		left -= int64(len(data))
		out = append(out, wire.File{Path: n.File, Data: data})
	}
	return out, nil
}

// values are the values of each extractor the description reads, as a
// request carries them. A value that is not valid UTF-8 is refused before
// the plugin starts, since JSON would carry it only with each bad byte
// replaced.
func (x *extractor) values(values map[string][]check.Value) (map[string][]wire.Value, error) {
	out := map[string][]wire.Value{}
	for _, name := range x.s.res.desc.Reads {
		vs := make([]wire.Value, 0, len(values[name]))
		for _, v := range values[name] {
			w, ok := toWire(v)
			if !ok {
				return nil, fmt.Errorf("the value of extractor %q at %s holds a byte that is not UTF-8, which plugin %s cannot be sent", name, v.At, x.s.plugin.exe.label)
			}
			vs = append(vs, w)
		}
		out[name] = vs
	}
	return out, nil
}

// toWire is v as a request carries it, and whether its text is all valid
// UTF-8.
func toWire(v check.Value) (wire.Value, bool) {
	w := wire.Value{File: v.At.File, Line: v.At.Line, NotLiteral: v.NotLiteral}
	ok := utf8.ValidString(v.At.File)
	text := func(s string) *string {
		ok = ok && utf8.ValidString(s)
		return &s
	}
	if v.HasKey || v.Key != "" {
		w.Key = text(v.Key)
	}
	if v.Condition != "" {
		w.Condition = text(v.Condition)
	}
	if v.Data == nil {
		w.Text = text(v.Text)
		return w, ok
	}
	n, nodeOK := toNode(v.Data)
	w.Data = n
	return w, ok && nodeOK
}

// toNode is d as a request carries it, and whether its keys and scalars are
// all valid UTF-8.
func toNode(d *check.Data) (*wire.Node, bool) {
	n := &wire.Node{Line: d.At.Line}
	switch d.Kind {
	case check.DataScalar:
		s := d.Text
		n.Scalar = &s
		return n, utf8.ValidString(s)
	case check.DataMap:
		var ok bool
		n.Map, ok = toPairs(d)
		return n, ok
	default:
		var ok bool
		n.List, ok = toNodes(d.Items)
		return n, ok
	}
}

// toPairs are the keys and items of d, a mapping, as a request carries
// them, and whether they are all valid UTF-8.
func toPairs(d *check.Data) ([]wire.Pair, bool) {
	items, ok := toNodes(d.Items)
	pairs := make([]wire.Pair, len(items))
	for i, it := range items {
		pairs[i] = wire.Pair{Key: d.Keys[i], Value: it}
		ok = ok && utf8.ValidString(d.Keys[i])
	}
	return pairs, ok
}

// toNodes are items as a request carries them, and whether they are all
// valid UTF-8.
func toNodes(items []*check.Data) ([]wire.Node, bool) {
	out := make([]wire.Node, len(items))
	ok := true
	for i, it := range items {
		c, itOK := toNode(it)
		out[i], ok = *c, ok && itOK
	}
	return out, ok
}

// check holds the values of a reply to what the protocol asks of them and
// returns them as check holds them: each in one of the request's files, on
// a line the file has, of the shape the description says, marked not
// literal only when the description allows it, and all of them within
// MaxValues and max_bytes, counting each value's key and condition.
func (x *extractor) check(values []wire.Value, files []wire.File) ([]check.Value, error) {
	lines := make(map[string]int, len(files))
	for _, f := range files {
		lines[f.Path] = core.LineOf(f.Data, len(f.Data))
	}
	b := budget{limit: x.limit}
	out := make([]check.Value, 0, len(values))
	for i, w := range values {
		v, err := x.value(w, lines, &b)
		if err != nil {
			return nil, fmt.Errorf("value %d: %w", i, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// budget is what the values so far hold: nodes against MaxValues and bytes
// against max_bytes.
type budget struct {
	nodes int
	bytes int64
	limit int64
}

// add counts n nodes and b bytes, and refuses the sum past either bound.
func (b *budget) add(n int, bytes int64) error {
	if b.nodes += n; b.nodes > check.MaxValues {
		return fmt.Errorf("the values pass %d, the limit on an extractor's values, each node of a mapping one", check.MaxValues)
	}
	if b.bytes += bytes; b.bytes > b.limit {
		return fmt.Errorf("the values hold more than %d bytes in all, counting each key and condition, the extractor's max_bytes; set max_bytes: higher", b.limit)
	}
	return nil
}

// value checks one value of a reply and returns it as check holds it.
func (x *extractor) value(w wire.Value, lines map[string]int, b *budget) (check.Value, error) {
	last, ok := lines[w.File]
	if !ok {
		return check.Value{}, fmt.Errorf("its file %s is none of the request's files", strconv.Quote(w.File))
	}
	if w.Line < 0 || w.Line > last {
		return check.Value{}, fmt.Errorf("its line %d is not in %s, which has %d lines", w.Line, w.File, last)
	}
	d := x.s.res.desc
	if w.NotLiteral && !d.NotLiteral {
		return check.Value{}, errors.New("it is marked not literal, and the description says no value is")
	}
	v := check.Value{At: check.Location{File: w.File, Line: w.Line}, NotLiteral: w.NotLiteral}
	extra := int64(0)
	if w.Key != nil {
		v.Key, v.HasKey, extra = *w.Key, true, int64(len(*w.Key))
	}
	if w.Condition != nil {
		v.Condition, extra = *w.Condition, extra+int64(len(*w.Condition))
	}
	if err := x.shaped(&v, w, d, last); err != nil {
		return check.Value{}, err
	}
	n, size := v.Size()
	return v, b.add(n, size+extra)
}

// shaped sets v's text or data from w, refusing a value whose shape is not
// the description's.
func (x *extractor) shaped(v *check.Value, w wire.Value, d check.Description, last int) error {
	if d.Shape == check.ShapeFlat {
		if w.Text == nil || w.Data != nil {
			return errors.New("it is no flat value: it needs text and no data")
		}
		v.Text = *w.Text
		return nil
	}
	return whole(v, w, d, last)
}

// whole sets v's data from w, a value handed over whole, refusing one that
// is no mapping or, for a record, whose keys are not the record's fields.
func whole(v *check.Value, w wire.Value, d check.Description, last int) error {
	if w.Text != nil || w.Data == nil || w.Data.Map == nil {
		return errors.New("it is no value handed over whole: it needs data, a mapping, and no text")
	}
	data, err := fromNode(*w.Data, w.File, last)
	if err != nil {
		return err
	}
	if d.Shape == check.ShapeRecord && !equalKeys(data.Keys, d.Fields) {
		return fmt.Errorf("its keys %q are not the record's fields %q", data.Keys, d.Fields)
	}
	v.Data, v.Text = data, data.Show(check.ShownBytes)
	return nil
}

// equalKeys reports whether a and b hold the same keys in the same order.
func equalKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fromNode is n, a node of a value in file, whose last line is last, as
// check holds it, refusing a node whose line is not in the file.
func fromNode(n wire.Node, file string, last int) (*check.Data, error) {
	if n.Line < 0 || n.Line > last {
		return nil, fmt.Errorf("a node's line %d is not in %s, which has %d lines", n.Line, file, last)
	}
	d := &check.Data{At: check.Location{File: file, Line: n.Line}}
	var err error
	switch {
	case n.Scalar != nil:
		d.Kind, d.Text = check.DataScalar, *n.Scalar
	case n.Map != nil:
		d.Kind = check.DataMap
		if d.Keys, err = keysOf(n.Map); err == nil {
			d.Items, err = fromNodes(n.Map, func(p wire.Pair) wire.Node { return p.Value }, file, last)
		}
	default:
		d.Kind = check.DataList
		d.Items, err = fromNodes(n.List, func(n wire.Node) wire.Node { return n }, file, last)
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// keysOf are the keys of a mapping node's pairs, refusing a key written
// twice, which no file a built in reader reads yields.
func keysOf(pairs []wire.Pair) ([]string, error) {
	var keys []string
	for _, p := range pairs {
		if slices.Contains(keys, p.Key) {
			return nil, fmt.Errorf("a mapping holds the key %s twice", strconv.Quote(p.Key))
		}
		keys = append(keys, p.Key)
	}
	return keys, nil
}

// fromNodes is the node of each of items, by node, as check holds it.
func fromNodes[T any](items []T, node func(T) wire.Node, file string, last int) ([]*check.Data, error) {
	var out []*check.Data
	for _, it := range items {
		c, err := fromNode(node(it), file, last)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
