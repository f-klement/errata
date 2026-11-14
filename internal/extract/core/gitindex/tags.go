package gitindex

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/extract/core/clip"
)

// ErrShallowUntagged refuses a shallow checkout that holds no tag at all:
// a CI checkout made with a depth, as actions/checkout makes one by
// default, often fetches no tag, so errata cannot tell a HEAD no tag
// names from a tag left behind.
var ErrShallowUntagged = errors.New("the checkout is shallow and holds no tag, so errata cannot tell whether a tag names HEAD; fetch the tags, as git fetch --tags or actions/checkout's fetch-tags: true does, or clone with the whole history")

// The bounds of a read of the tags that HEAD carries, beside Limits.
const (
	// maxSymrefs is how many symbolic refs a ref may lead through, as
	// git's SYMREF_MAXDEPTH.
	maxSymrefs = 5
	// maxPeel is how many tag objects a tag may lead through before it
	// names a commit, a tree or a blob.
	maxPeel = 16
	// maxHead is the most bytes of an object peel reads: its header
	// and, of a tag, the object line, which a SHA-256 name keeps under
	// 100 bytes.
	maxHead = 256
	// maxDeflated is the most bytes of a file the inflation of maxHead
	// bytes may read, so a stream of empty blocks ends.
	maxDeflated = 64 << 10
)

// HeadTags is the names, below refs/tags/ and sorted, of the tags in the
// repository whose top is top that point at the commit HEAD names once
// peeled, as git tag --points-at HEAD lists them, keeping only those that
// start with prefix. It reads git's files and runs no git (ADR 0002): HEAD
// in the git directory, and the loose refs, packed-refs and objects in the
// common directory a worktree's commondir names. An annotated tag is peeled
// by packed-refs' ^ line, or by reading its tag object, loose or in a pack,
// where a chain of deltas may store it (objects.object). A HEAD that names no
// commit yet, and a repository whose tags all point elsewhere or that
// holds none, give none, but a shallow checkout that holds no tag is
// refused (ErrShallowUntagged). A repository that keeps its refs in a
// reftable is refused, and so is any file of these that git would not
// write, past l, or that is not a regular file.
func HeadTags(ctx context.Context, top, prefix string, l Limits) ([]string, error) {
	r, err := openRefs(top, l)
	if err != nil {
		return nil, err
	}
	loose, err := looseTags(ctx, r.common, l)
	if err != nil {
		return nil, err
	}
	names, isLoose := tagNames(loose, r.packed)
	if len(names) == 0 {
		return nil, untagged(r.common)
	}
	head, err := r.resolve("HEAD")
	if err != nil || head == "" {
		return nil, err
	}
	slices.Sort(names)
	o := objects{dir: filepath.Join(r.common, "objects")}
	defer o.close()
	return r.tagsOn(ctx, &o, names, isLoose, prefix, head)
}

// tagsPrefix is where a ref that names a tag starts.
const tagsPrefix = "refs/tags/"

// openRefs finds the git directory below top and its common directory,
// refuses a repository that keeps its refs in a reftable, and reads its
// packed-refs.
func openRefs(top string, l Limits) (refs, error) {
	dir, err := gitDir(top, l)
	if err != nil {
		return refs{}, err
	}
	common, err := commonDir(dir, l)
	if err != nil {
		return refs{}, err
	}
	if _, err := os.Lstat(filepath.Join(common, "reftable")); err == nil {
		return refs{}, errors.New("the repository keeps its refs in a reftable, which errata does not read")
	}
	r := refs{dir: dir, common: common, l: l}
	if r.packed, r.peeledAll, err = readPacked(filepath.Join(common, "packed-refs"), l); err != nil {
		return refs{}, err
	}
	return r, nil
}

// tagNames is the names of the loose tags, then of the packed tags no
// loose one shadows, and which of them are loose.
func tagNames(loose []string, packed map[string]packedRef) ([]string, map[string]bool) {
	isLoose := make(map[string]bool, len(loose))
	for _, tag := range loose {
		isLoose[tag] = true
	}
	names := slices.Clone(loose)
	for name := range packed {
		if tag, ok := strings.CutPrefix(name, tagsPrefix); ok && !isLoose[tag] {
			names = append(names, tag)
		}
	}
	return names, isLoose
}

// untagged is the verdict on a repository that holds no tag: none, unless
// the checkout is shallow (ErrShallowUntagged).
func untagged(common string) error {
	if _, err := os.Lstat(filepath.Join(common, "shallow")); err == nil {
		return ErrShallowUntagged
	}
	return nil
}

// tagsOn is the tags of names, sorted, that start with prefix and point
// at head once peeled.
func (r refs) tagsOn(ctx context.Context, o *objects, names []string, isLoose map[string]bool, prefix, head string) ([]string, error) {
	var out []string
	for _, tag := range names {
		if !strings.HasPrefix(tag, prefix) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		on, err := r.points(o, tagsPrefix+tag, isLoose[tag], head)
		if err != nil {
			return nil, err
		}
		if on {
			out = append(out, tag)
		}
	}
	return out, nil
}

// commonDir is the common directory of the git directory dir: the one its
// commondir file names, as a worktree's does, or dir itself.
func commonDir(dir string, l Limits) (string, error) {
	data, err := readCapped(filepath.Join(dir, "commondir"), l.gitFile())
	if errors.Is(err, fs.ErrNotExist) {
		return dir, nil
	}
	if err != nil {
		return "", fmt.Errorf("read its commondir: %w", bare(err))
	}
	common := strings.TrimRight(string(data), "\r\n")
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return common, nil
}

// packedRef is a ref of packed-refs: the object it names and, after a ^
// line, the object an annotated tag peels to.
type packedRef struct {
	id, peeled string
}

// readPacked reads packed-refs at name, and whether its header says that
// every tag is peeled, so a tag without a ^ line is no annotated tag. A
// file that is not there holds no ref.
func readPacked(name string, l Limits) (map[string]packedRef, bool, error) {
	data, err := readCapped(name, l.Bytes)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]packedRef{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read packed-refs: %w", bare(err))
	}
	p := packedFile{refs: map[string]packedRef{}, l: l}
	if len(data) == 0 {
		return p.refs, false, nil
	}
	for n, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if err := p.line(n, line); err != nil {
			return nil, false, err
		}
	}
	return p.refs, p.peeledAll, nil
}

// packedFile is readPacked's reading of packed-refs: the refs so far, the
// last of them, which a ^ line peels, and whether the header says every
// tag is peeled.
type packedFile struct {
	refs      map[string]packedRef
	last      string
	peeledAll bool
	l         Limits
}

// line reads the line n, counted from 0, of packed-refs: the header, a ^
// line or a ref.
func (p *packedFile) line(n int, line string) error {
	switch {
	case n == 0 && strings.HasPrefix(line, "# pack-refs with:"):
		p.peeledAll = slices.Contains(strings.Fields(strings.TrimPrefix(line, "# pack-refs with:")), "peeled")
	case strings.HasPrefix(line, "^"):
		return p.peeled(n, line[1:])
	default:
		return p.ref(n, line)
	}
	return nil
}

// peeled reads the object id of a ^ line, which peels the ref before it,
// once.
func (p *packedFile) peeled(n int, id string) error {
	ref, ok := p.refs[p.last]
	if !ok || ref.peeled != "" || !objectName(id) {
		return badPackedLine(n)
	}
	ref.peeled = id
	p.refs[p.last] = ref
	return nil
}

// ref reads a line of an object id, a space and a ref name.
func (p *packedFile) ref(n int, line string) error {
	id, ref, ok := strings.Cut(line, " ")
	if !ok || !objectName(id) || !refName(ref) || len(ref) > p.l.Name {
		return badPackedLine(n)
	}
	if len(p.refs) == p.l.Entries {
		return fmt.Errorf("packed-refs lists more than %d refs, the limit", p.l.Entries)
	}
	p.refs[ref] = packedRef{id: id}
	p.last = ref
	return nil
}

// badPackedLine refuses the line n, counted from 0, of packed-refs.
func badPackedLine(n int) error {
	return fmt.Errorf("packed-refs: line %d is no line git writes", n+1)
}

// refName reports whether name is a ref below refs/ that git's
// check_refname_format takes, so a name can only lead to a file below
// refs/ in the common directory: each component non-empty, not . or ..,
// starting with no . and ending in no .lock; no character below a space,
// no DEL, space, ~, ^, :, ?, *, [ or \; no @{, and no . at the end.
func refName(name string) bool {
	rest, ok := strings.CutPrefix(name, "refs/")
	if !ok || strings.HasSuffix(name, ".") || strings.Contains(name, "@{") {
		return false
	}
	if strings.ContainsFunc(name, badRefChar) {
		return false
	}
	for part := range strings.SplitSeq(rest, "/") {
		if !refPart(part) {
			return false
		}
	}
	return true
}

// badRefChar reports whether git refuses c in a ref name: below a space,
// DEL, ~, ^, :, ?, *, [ or \.
func badRefChar(c rune) bool {
	return c <= ' ' || c == 0x7f || strings.ContainsRune("~^:?*[\\", c)
}

// refPart reports whether git takes part as a component of a ref name:
// non-empty, starting with no . and ending in no .lock.
func refPart(part string) bool {
	return part != "" && !strings.HasPrefix(part, ".") && !strings.HasSuffix(part, ".lock")
}

// objectName reports whether s is an object's name as git writes it: 40
// lowercase hex digits for SHA-1, 64 for SHA-256.
func objectName(s string) bool {
	if len(s) != 2*sha1Size && len(s) != 2*sha256Size {
		return false
	}
	return !strings.ContainsFunc(s, func(c rune) bool { return (c < '0' || c > '9') && (c < 'a' || c > 'f') })
}

// looseTags is the names, below refs/tags/, of the loose tags in the
// common directory common, each a file below refs/tags. A name that ends
// in .lock is a ref git is writing, and git lists none such. Anything but
// a directory or a regular file there is refused, and so are more than
// l.Entries entries and a name over l.Name bytes.
func looseTags(ctx context.Context, common string, l Limits) ([]string, error) {
	for _, dir := range []string{"refs", "refs/tags"} {
		if err := noLink(common, dir); err != nil {
			return nil, err
		}
	}
	w := tagWalk{ctx: ctx, common: common, l: l}
	err := w.walk("")
	return w.out, err
}

// tagWalk is looseTags' walk of refs/tags: the tags found so far and the
// entries counted.
type tagWalk struct {
	ctx     context.Context
	common  string
	l       Limits
	out     []string
	entries int
}

// walk lists the directory rel below refs/tags. Only refs/tags itself may
// be missing.
func (w *tagWalk) walk(rel string) error {
	list, err := os.ReadDir(filepath.Join(w.common, "refs", "tags", filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) && rel == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list refs/tags: %w", bare(err))
	}
	for _, e := range list {
		if err := w.visit(rel, e); err != nil {
			return err
		}
	}
	return nil
}

// visit counts the entry e of the directory rel and holds its name to
// l.Name, then walks it when it is a directory and reads it as a tag
// when not.
func (w *tagWalk) visit(rel string, e fs.DirEntry) error {
	if w.entries++; w.entries > w.l.Entries {
		return fmt.Errorf("refs/tags holds more than %d entries, the limit", w.l.Entries)
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	name := tagName(rel, e.Name())
	if len(tagsPrefix+name) > w.l.Name {
		return fmt.Errorf("refs/tags holds a name over %d bytes, the limit", w.l.Name)
	}
	if e.IsDir() {
		return w.walk(name)
	}
	return w.tag(name, e)
}

// tag keeps the file name below refs/tags as a tag, but for a ref git is
// writing, which ends in .lock, and refuses anything else.
func (w *tagWalk) tag(name string, e fs.DirEntry) error {
	switch {
	case !e.Type().IsRegular():
		return fmt.Errorf("refs/tags/%s is neither a file nor a directory, which errata does not read as a ref", clip.Quote(name))
	case strings.HasSuffix(name, ".lock"):
	case !refName(tagsPrefix + name):
		return fmt.Errorf("refs/tags/%s is no ref name git takes", clip.Quote(name))
	default:
		w.out = append(w.out, name)
	}
	return nil
}

// noLink refuses the directory rel of dir when it is a symlink: git
// follows one, but the names listed there would be read as refs of a
// directory anywhere on the machine, so errata reads only the repository's
// own. One that is not there is left to the read that follows.
func noLink(dir, rel string) error {
	info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	if err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink, which errata does not follow to read refs or packs", rel)
	}
	return nil
}

// tagName is name below the directory rel, written with slashes.
func tagName(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

// refs reads the refs of one repository: HEAD in the git directory, the
// rest in the common directory, loose or packed.
type refs struct {
	dir, common string
	packed      map[string]packedRef
	peeledAll   bool
	l           Limits
}

// resolve is the object the ref name names, through symbolic refs, ""
// when it names none yet, as an unborn branch's HEAD.
func (r refs) resolve(name string) (string, error) {
	for range maxSymrefs + 1 {
		text, found, err := r.read(name)
		if err != nil || !found {
			return "", err
		}
		target, symbolic := strings.CutPrefix(text, "ref: ")
		if !symbolic {
			if !objectName(text) {
				return "", fmt.Errorf("%s holds no object name and no ref: line, which git writes in a ref", clip.Quote(name))
			}
			return text, nil
		}
		if !refName(target) {
			return "", fmt.Errorf("%s names %s, which is no ref name git takes below refs/", clip.Quote(name), clip.Quote(target))
		}
		name = target
	}
	return "", fmt.Errorf("%s leads through more than %d symbolic refs, which git refuses", clip.Quote(name), maxSymrefs)
}

// read is the text of the ref name, without its line end, and whether the
// repository holds it: HEAD's file in the git directory, which may be a
// symlink into refs/, or a loose ref's file in the common directory, and
// failing that its object in packed-refs.
func (r refs) read(name string) (string, bool, error) {
	at := filepath.Join(r.common, filepath.FromSlash(name))
	if name == "HEAD" {
		at = filepath.Join(r.dir, "HEAD")
		if target, err := os.Readlink(at); err == nil {
			return "ref: " + target, true, nil
		}
	}
	data, err := readCapped(at, r.l.gitFile())
	if errors.Is(err, fs.ErrNotExist) {
		ref, ok := r.packed[name]
		return ref.id, ok, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", clip.Quote(name), bare(err))
	}
	return strings.TrimRight(string(data), "\r\n"), true, nil
}

// points reports whether the tag ref name points at head once peeled. A
// packed tag is peeled by its ^ line, or is no annotated tag when the file
// says it peels every tag; any other is peeled by reading its objects.
func (r refs) points(o *objects, name string, loose bool, head string) (bool, error) {
	ref := r.packed[name]
	id := ref.id
	if loose {
		var err error
		if id, err = r.resolve(name); err != nil {
			return false, err
		}
		if id == "" {
			return false, fmt.Errorf("%s leads to a ref the repository does not hold, which git ignores as broken", clip.Quote(name))
		}
	} else if ref.peeled != "" || r.peeledAll {
		return id == head || ref.peeled == head, nil
	}
	return peelsTo(o, name, id, head)
}

// peelsTo reports whether the object id, which the tag ref name names,
// is head or a tag object that leads to head, through at most maxPeel
// tag objects.
func peelsTo(o *objects, name, id, head string) (bool, error) {
	for range maxPeel + 1 {
		if id == head {
			return true, nil
		}
		kind, target, err := o.peel(id)
		if err != nil || kind != "tag" {
			return false, err
		}
		id = target
	}
	return false, fmt.Errorf("the tag %s leads through more than %d tag objects", clip.Quote(name), maxPeel)
}

// objects reads git's object store at dir: the loose objects, and the
// packs in dir/pack through their version 2 indexes. Each pack is opened
// once, when an object is first looked for in it, and kept open until
// close; each object a pack rebuilds from a chain of deltas is kept, up to
// maxCached bytes in all, since the tags a fetch packs are deltas of one
// another, and a chain shares its bases with the next.
type objects struct {
	dir    string
	names  []string // the index files of the packs, once listed
	packs  []*pack  // the packs opened, in the order of names
	listed bool
	cache  map[cached][]byte
	held   int           // the bytes cache holds
	z      io.ReadCloser // the zlib reader, once made (inflate)
	buf    *bufio.Reader // the buffer inflate reads a file through, once made
	work   int64         // the work done so far (spend)
	peeled map[string]peeledObject
}

// peeledObject is what peel found of an object: its type and, of a tag,
// the object it names. Many tags may name one object, and each later one
// costs a map lookup.
type peeledObject struct{ kind, target string }

// maxWork is the most work one read of the tags on HEAD does in the
// objects, shared by every tag: each byte a zlib stream gives or takes, each
// byte patch writes, entryWork for each object or delta read and
// lookupWork for each search of a pack index, which stand for their
// opens, seeks and reads. Each
// object's bounds hold it to one chain of 4095 deltas of 64 KiB, but tags
// that each name the top of such a chain would repeat it, 64 KiB written
// 4095 times a tag. A var, so a test lowers it.
var maxWork int64 = 256 << 20

// The fixed work of one read: entryWork for one loose object, or one
// object or delta of a pack, an open or a seek and a zlib reset, and
// lookupWork for one search of a pack index, some 20 reads of a name,
// priced at four reads: at one, a chain of deltas whose bases are found by
// name bought the most time a byte of any shape. A read costs some 10 to 25 us, so the
// budget's time is its count of them, at most some 260000.
const (
	lookupWork = 4 << 10
	entryWork  = 1 << 10
)

// errWork refuses a read of the tags past maxWork.
func errWork() error {
	return fmt.Errorf("the tags on HEAD took more than %d KiB of objects inflated, rebuilt and looked up, the limit; git gc repacks the tags into short chains", maxWork>>10)
}

// spend counts n bytes of work, and refuses a read past maxWork.
func (o *objects) spend(n int64) error {
	if o.work += n; o.work > maxWork {
		return errWork()
	}
	return nil
}

// counted counts the bytes taken from it. It is a flate.Reader, so the
// decompressor reads it byte by byte without a buffer of its own, and the
// count is what the stream used, not what a buffer read ahead.
type counted struct {
	r *bufio.Reader
	n int64
}

func (c *counted) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

func (c *counted) ReadByte() (byte, error) {
	b, err := c.r.ReadByte()
	if err == nil {
		c.n++
	}
	return b, err
}

// cached names an object a pack rebuilt: the pack, and the object's offset.
type cached struct {
	p  *pack
	at int64
}

// The bounds of the packs of one read.
const (
	// maxPacks is the most packs a repository may hold; git's gc --auto
	// repacks at 50.
	maxPacks = 1000
	// maxCached is the most bytes of rebuilt objects kept.
	maxCached = 32 << 20
)

// close closes every pack o opened.
func (o *objects) close() {
	for _, p := range o.packs {
		p.close()
	}
}

// The types of an object in a pack, and the two that store it as a delta
// of another: by the distance back to it, or by its name.
var packTypes = map[byte]string{1: "commit", 2: "tree", 3: "blob", 4: "tag"}

const (
	ofsDelta = 6
	refDelta = 7
)

// peel is the type of the object id and, of a tag, the object its first
// line names. It inflates at most maxHead bytes of a loose object, and of
// a packed one what its chain of deltas needs (objects.object).
func (o *objects) peel(id string) (string, string, error) {
	if got, ok := o.peeled[id]; ok {
		return got.kind, got.target, nil
	}
	kind, target, err := o.read(id)
	if err == nil {
		if o.peeled == nil {
			o.peeled = map[string]peeledObject{}
		}
		o.peeled[id] = peeledObject{kind, target}
	}
	return kind, target, err
}

// read is peel without its memory of what it peeled.
func (o *objects) read(id string) (string, string, error) {
	if err := o.spend(entryWork); err != nil {
		return "", "", err
	}
	kind, body, err := o.loose(id)
	if errors.Is(err, fs.ErrNotExist) {
		kind, body, err = o.packed(id)
	}
	if err != nil {
		return "", "", fmt.Errorf("read the object %s: %w", id, err)
	}
	if kind != "tag" {
		return kind, "", nil
	}
	line, _, _ := bytes.Cut(body, []byte("\n"))
	target, ok := bytes.CutPrefix(line, []byte("object "))
	if !ok || len(target) != len(id) || !objectName(string(target)) {
		return "", "", fmt.Errorf("the tag object %s does not start with the object line git writes", id)
	}
	return kind, string(target), nil
}

// loose is the type and the start of the loose object id, or an error
// that errors.Is reads as fs.ErrNotExist when there is none.
func (o *objects) loose(id string) (string, []byte, error) {
	f, err := openRegular(filepath.Join(o.dir, id[:2], id[2:]))
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	head, err := o.inflate(f, maxHead)
	if err != nil {
		return "", nil, err
	}
	header, body, ok := bytes.Cut(head, []byte{0})
	kind, _, spaced := bytes.Cut(header, []byte(" "))
	if !ok || !spaced {
		return "", nil, errors.New("its loose object has no header git writes")
	}
	return string(kind), body, nil
}

// inflate is at most limit bytes of the zlib stream r starts with, read
// from at most maxDeflated bytes of it.
func inflate(r io.Reader, limit int64) ([]byte, error) {
	var o objects
	return o.inflate(r, limit)
}

// inflate is the package's inflate through o's zlib reader, which it makes
// on its first call and resets on each later one: a reader holds some
// 40 KiB of tables, and a read of many tags inflates thousands of objects.
func (o *objects) inflate(r io.Reader, limit int64) ([]byte, error) {
	if o.buf == nil {
		o.buf = bufio.NewReader(nil)
	}
	o.buf.Reset(io.LimitReader(r, maxDeflated))
	in := &counted{r: o.buf}
	var err error
	if o.z == nil {
		o.z, err = zlib.NewReader(in)
	} else {
		err = o.z.(zlib.Resetter).Reset(in, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("it is no zlib stream: %w", err)
	}
	head, err := io.ReadAll(io.LimitReader(o.z, limit))
	if err != nil {
		return nil, fmt.Errorf("it does not inflate: %w", err)
	}
	return head, o.spend(in.n + int64(len(head)))
}

// packed is the type and the start of the object id in one of the packs.
func (o *objects) packed(id string) (string, []byte, error) {
	if err := o.list(); err != nil {
		return "", nil, err
	}
	name, err := hex.DecodeString(id)
	if err != nil {
		return "", nil, err
	}
	for i, idx := range o.names {
		kind, body, found, err := o.inPack(i, idx, name)
		if err != nil || found {
			return kind, body, err
		}
	}
	return "", nil, errors.New("it is in no loose object and no pack the repository holds, as in a clone that borrows objects through alternates, which errata does not read")
}

// list lists the index files of the packs, on its first call only.
func (o *objects) list() error {
	if o.listed {
		return nil
	}
	o.listed = true
	if err := noLink(o.dir, "pack"); err != nil {
		return fmt.Errorf("objects/%w", err)
	}
	list, err := os.ReadDir(filepath.Join(o.dir, "pack"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("list the packs: %w", bare(err))
	}
	for _, e := range list {
		if strings.HasSuffix(e.Name(), ".idx") {
			o.names = append(o.names, filepath.Join(o.dir, "pack", e.Name()))
		}
	}
	if len(o.names) > maxPacks {
		return fmt.Errorf("the repository holds more than %d packs, the limit; git gc packs them into one", maxPacks)
	}
	return nil
}

// inPack is the type and the start of the object name in the pack i,
// whose index is idx, and whether the pack holds it.
func (o *objects) inPack(i int, idx string, name []byte) (string, []byte, bool, error) {
	p, err := o.opened(i, idx)
	if err != nil {
		return "", nil, false, err
	}
	if err := o.spend(lookupWork); err != nil {
		return "", nil, false, err
	}
	at, found, err := p.lookup(name)
	if err != nil {
		return "", nil, false, fmt.Errorf("read %s: %w", filepath.Base(idx), err)
	}
	if !found {
		return "", nil, false, nil
	}
	kind, body, err := o.object(p, at, len(name))
	if err != nil {
		return "", nil, false, fmt.Errorf("read %s: %w", p.name, err)
	}
	return kind, body[:min(len(body), maxHead)], true, nil
}

// opened is the pack i, whose index is idx, opened on the first look in
// it. The packs open in the order of o.names, so i is at most len(o.packs).
func (o *objects) opened(i int, idx string) (*pack, error) {
	if i == len(o.packs) {
		p, err := openPack(idx)
		if err != nil {
			return nil, err
		}
		o.packs = append(o.packs, p)
	}
	return o.packs[i], nil
}

// pack is a pack open for reads: its index, its fan out, and its objects.
type pack struct {
	name      string // the pack file's base name, for a message
	idx, data *os.File
	head      []byte // the index's signature, version and fan out
	n         int64  // the objects the index lists
	size      int64  // the index's size
}

// openPack opens the version 2 pack index idx and the pack beside it, and
// refuses either when it is not what git writes. The index is 8 bytes of
// signature and version, a fan out of 256 counts, then of each object its
// name, a CRC and a 4-byte offset, whose high bit makes it the index of an
// 8-byte offset after them.
func openPack(idx string) (*pack, error) {
	p := &pack{name: strings.TrimSuffix(filepath.Base(idx), ".idx") + ".pack", head: make([]byte, 8+256*4)}
	var err error
	if p.idx, err = openRegular(idx); err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(idx), bare(err))
	}
	if err := p.check(); err != nil {
		p.close()
		return nil, fmt.Errorf("read %s: %w", filepath.Base(idx), err)
	}
	if p.data, err = openRegular(strings.TrimSuffix(idx, ".idx") + ".pack"); err != nil {
		p.close()
		return nil, fmt.Errorf("read %s: %w", p.name, bare(err))
	}
	sig := make([]byte, 4)
	if _, err := p.data.ReadAt(sig, 0); err != nil || string(sig) != "PACK" {
		p.close()
		return nil, fmt.Errorf("read %s: it does not start with PACK, git's signature", p.name)
	}
	return p, nil
}

// check reads the index's head and refuses one that is not version 2, or
// whose count of objects the file cannot hold.
func (p *pack) check() error {
	info, err := p.idx.Stat()
	if err != nil {
		return bare(err)
	}
	p.size = info.Size()
	if _, err := p.idx.ReadAt(p.head, 0); err != nil || string(p.head[:8]) != "\xfftOc\x00\x00\x00\x02" {
		return errors.New("it is no version 2 pack index, the one git writes")
	}
	p.n = p.fan(255)
	// The names' size is not in the file: it is the size of the names
	// looked up, 20 or 32 bytes, so the file is held to the smaller.
	if p.n > p.size/(sha1Size+8) {
		return errors.New("its fan out lists more objects than the file holds")
	}
	return nil
}

// fan is the count the fan out gives for the byte b: the objects whose
// names start with b or a smaller byte.
func (p *pack) fan(b int) int64 { return int64(binary.BigEndian.Uint32(p.head[8+4*b:])) }

// close closes what p holds open.
func (p *pack) close() {
	// A nil file's Close returns os.ErrInvalid, as for the pack openPack
	// had not opened yet; each was opened for reading, so a close loses
	// nothing.
	for _, f := range []*os.File{p.idx, p.data} {
		_ = f.Close()
	}
}

// lookup is the offset in the pack of the object name, and whether the
// index lists it. The index is searched where it lies, read by ReadAt, so
// an index of millions of objects costs some 20 reads.
func (p *pack) lookup(name []byte) (int64, bool, error) {
	hash := int64(len(name))
	names := int64(len(p.head))
	offsets := names + p.n*hash + p.n*4
	if offsets+p.n*4 > p.size {
		return 0, false, errors.New("its fan out lists more objects than the file holds")
	}
	lo, hi, err := p.fanSpan(name[0])
	if err != nil {
		return 0, false, err
	}
	got := make([]byte, hash)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if _, err := p.idx.ReadAt(got, names+mid*hash); err != nil {
			return 0, false, bare(err)
		}
		switch c := bytes.Compare(got, name); {
		case c < 0:
			lo = mid + 1
		case c > 0:
			hi = mid
		default:
			return offset(p.idx, offsets, p.n, mid)
		}
	}
	return 0, false, nil
}

// fanSpan is the span of the index's names that start with the byte b,
// as the fan out gives it, which it refuses when out of order.
func (p *pack) fanSpan(b byte) (int64, int64, error) {
	lo, hi := int64(0), p.fan(int(b))
	if b > 0 {
		lo = p.fan(int(b) - 1)
	}
	if lo > hi || hi > p.n {
		return 0, 0, errors.New("its fan out is not in order")
	}
	return lo, hi, nil
}

// offset is the pack offset of the object at index i of an index of n
// objects whose 4-byte offsets start at offsets.
func offset(f *os.File, offsets, n, i int64) (int64, bool, error) {
	word := make([]byte, 8)
	if _, err := f.ReadAt(word[:4], offsets+i*4); err != nil {
		return 0, false, bare(err)
	}
	at := int64(binary.BigEndian.Uint32(word))
	if at&(1<<31) == 0 {
		return at, true, nil
	}
	if _, err := f.ReadAt(word, offsets+n*4+(at&(1<<31-1))*8); err != nil {
		return 0, false, bare(err)
	}
	if big := binary.BigEndian.Uint64(word); big < 1<<63 {
		return int64(big), true, nil //nolint:gosec // G115: big is under 1<<63, so it fits
	}
	return 0, false, errors.New("it gives an offset past any file")
}

// The bounds of an object a pack stores as a chain of deltas.
const (
	// maxChain is how many deltas a chain may hold, git's limit on
	// pack.depth.
	maxChain = 4095
	// maxBase is the most bytes of each object of a chain inflated, a base
	// or the result of a delta: tag objects, which git deltas only against
	// other tags, are a few hundred bytes, or a few thousand signed.
	maxBase = 64 << 10
)

// entry is the header of an object in a pack: its type, and of a delta
// the base it applies to, by offset in the pack or by name; and where its
// zlib stream starts.
type entry struct {
	at     int64 // where the object starts, once read in a chain
	kind   byte
	base   int64  // an offset delta's base
	ref    []byte // a reference delta's base
	stream int64
}

// readEntry reads the header of the object at offset at in the pack f:
// a first byte that holds the type in bits 4 to 6, with the size running
// on while the top bit is set; then, of an offset delta, the distance back
// to its base, and of a reference delta, its base's name of hash bytes.
func readEntry(f *os.File, at int64, hash int) (entry, error) {
	r := bufio.NewReaderSize(io.NewSectionReader(f, at, 64), 64)
	c, err := r.ReadByte()
	if err != nil {
		return entry{}, fmt.Errorf("its object at %d is cut short", at)
	}
	e := entry{kind: c >> 4 & 7}
	n, err := sizeBytes(r, c, at)
	if err != nil {
		return entry{}, err
	}
	based, err := e.readBase(r, at, hash)
	if err != nil {
		return entry{}, err
	}
	e.stream = at + n + based
	return e, nil
}

// sizeBytes reads the rest of the size an object's first byte c starts,
// which runs on while the top bit is set, and is how many bytes the size
// took in all.
func sizeBytes(r *bufio.Reader, c byte, at int64) (int64, error) {
	n := int64(1)
	var err error
	for i := 0; c&0x80 != 0; i++ {
		if c, err = r.ReadByte(); err != nil || i == 9 {
			return 0, fmt.Errorf("its object at %d has no header git writes", at)
		}
		n++
	}
	return n, nil
}

// readBase reads the base of the delta e at offset at, by distance back
// or by name, and refuses a type git does not write. It is how many
// bytes it read.
func (e *entry) readBase(r *bufio.Reader, at int64, hash int) (int64, error) {
	switch e.kind {
	case ofsDelta:
		base, n, err := ofsBase(r, at)
		e.base = base
		return n, err
	case refDelta:
		e.ref = make([]byte, hash)
		if _, err := io.ReadFull(r, e.ref); err != nil {
			return 0, fmt.Errorf("its delta at %d is cut short", at)
		}
		return int64(hash), nil
	default:
		if _, ok := packTypes[e.kind]; !ok {
			return 0, fmt.Errorf("its object at %d is of type %d, which git does not write", at, e.kind)
		}
		return 0, nil
	}
}

// ofsBase reads the distance back from at to an offset delta's base, and
// is the base's offset and how many bytes the distance took. Each byte
// after the first adds one before the shift, as git's encoding does, so
// no distance has two forms.
func ofsBase(r *bufio.Reader, at int64) (int64, int64, error) {
	var back, n int64
	for i := 0; ; i++ {
		c, err := r.ReadByte()
		if err != nil || i == 9 {
			return 0, 0, fmt.Errorf("its delta at %d names no base git writes", at)
		}
		n++
		back = back<<7 | int64(c&0x7f)
		if c&0x80 == 0 {
			break
		}
		back++
	}
	if back <= 0 || back > at {
		return 0, 0, fmt.Errorf("its delta at %d names a base outside the pack", at)
	}
	return at - back, n, nil
}

// object is the type and the start, at most maxBase bytes, of the object
// at offset at in the pack p, whose names are hash bytes long. An object
// stored as a delta is rebuilt from its chain of bases, at most maxChain
// deep, from the nearest object of the chain o has kept: a delta whose
// base is in another pack is refused, as git keeps a pack whole.
func (o *objects) object(p *pack, at int64, hash int) (string, []byte, error) {
	chain, kind, data, err := o.base(p, at, hash)
	if err != nil {
		return "", nil, err
	}
	if data, err = o.rebuild(p, chain, kind, data); err != nil {
		return "", nil, err
	}
	return packTypes[kind], data, nil
}

// base follows the chain of deltas from the object at offset at of the
// pack p down to the nearest object that is no delta or that o has kept.
// It is the deltas, the outermost first, and that object's type and data.
func (o *objects) base(p *pack, at int64, hash int) ([]entry, byte, []byte, error) {
	var chain []entry
	for {
		if got, ok := o.cache[cached{p, at}]; ok {
			return chain, got[0], got[1:], nil
		}
		e, err := o.header(p, at, hash)
		if err != nil {
			return nil, 0, nil, err
		}
		if e.kind != ofsDelta && e.kind != refDelta {
			data, err := o.inflate(io.NewSectionReader(p.data, e.stream, maxDeflated), maxBase)
			return chain, e.kind, data, err
		}
		if len(chain) == maxChain {
			return nil, 0, nil, fmt.Errorf("its object leads through more than %d deltas", maxChain)
		}
		e.at = at
		chain = append(chain, e)
		if at, err = o.deltaBase(p, e); err != nil {
			return nil, 0, nil, err
		}
	}
}

// header reads the header of the object at offset at of the pack p, which
// costs entryWork.
func (o *objects) header(p *pack, at int64, hash int) (entry, error) {
	if err := o.spend(entryWork); err != nil {
		return entry{}, err
	}
	return readEntry(p.data, at, hash)
}

// deltaBase is the offset in the pack p of the base of the delta e: the
// one it names by offset, or the one the index lists by its name, which
// costs lookupWork.
func (o *objects) deltaBase(p *pack, e entry) (int64, error) {
	if e.kind != refDelta {
		return e.base, nil
	}
	if err := o.spend(lookupWork); err != nil {
		return 0, err
	}
	at, found, err := p.lookup(e.ref)
	if err == nil && !found {
		err = fmt.Errorf("its delta names the base %x, which the pack does not hold", e.ref)
	}
	return at, err
}

// rebuild applies the deltas of chain, the innermost first, to data, the
// base of type kind, and keeps each object rebuilt below the outermost.
func (o *objects) rebuild(p *pack, chain []entry, kind byte, data []byte) ([]byte, error) {
	for i := len(chain) - 1; i >= 0; i-- {
		delta, err := o.inflate(io.NewSectionReader(p.data, chain[i].stream, maxDeflated), maxBase)
		if err == nil {
			data, err = patch(data, delta)
		}
		if err == nil {
			err = o.spend(int64(len(data)))
		}
		if err != nil {
			return nil, err
		}
		// The outermost object is the one peeled, which peel remembers;
		// the bases below it are kept for the next chain that shares them.
		if i > 0 {
			o.keep(p, chain[i].at, kind, data)
		}
	}
	return data, nil
}

// keep keeps the object at offset at of the pack p, of type kind, while
// the cache holds less than maxCached bytes.
func (o *objects) keep(p *pack, at int64, kind byte, data []byte) {
	if o.held+len(data) > maxCached {
		return
	}
	if o.cache == nil {
		o.cache = map[cached][]byte{}
	}
	o.cache[cached{p, at}] = append([]byte{kind}, data...)
	o.held += len(data)
}

// patch is the start, at most maxBase bytes, of the object the delta
// makes of base, as git's patch-delta.c applies one: the base's size and
// the result's, each in 7-bit groups, then instructions that copy a range
// of the base, when the top bit is set, or insert the bytes after them.
// Each is cut to the room left under maxBase, and the result stops there.
// A delta that copies past what errata read of its base is refused.
func patch(base, delta []byte) ([]byte, error) {
	delta, err := skipSizes(delta)
	if err != nil {
		return nil, err
	}
	var out []byte
	for len(delta) > 0 && len(out) != maxBase {
		if out, delta, err = patchStep(base, delta, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// skipSizes is the delta past its two sizes, the base's and the
// result's, each in 7-bit groups.
func skipSizes(delta []byte) ([]byte, error) {
	for range 2 {
		for len(delta) > 0 && delta[0]&0x80 != 0 {
			delta = delta[1:]
		}
		if len(delta) == 0 {
			return nil, errors.New("its delta is cut short")
		}
		delta = delta[1:]
	}
	return delta, nil
}

// patchStep applies the instruction delta starts with to out, and is out
// and the delta after it.
func patchStep(base, delta, out []byte) ([]byte, []byte, error) {
	cmd := delta[0]
	delta = delta[1:]
	switch {
	case cmd&0x80 != 0:
		return patchCopy(cmd, base, delta, out)
	case cmd != 0:
		if int(cmd) > len(delta) {
			return nil, nil, errors.New("its delta is cut short")
		}
		return append(out, delta[:min(int(cmd), maxBase-len(out))]...), delta[cmd:], nil
	default:
		return nil, nil, errors.New("its delta holds an instruction git does not write")
	}
}

// patchCopy appends to out the range of base that the copy instruction
// cmd gives: the bytes after it hold the offset and the size, each byte
// there only when its bit of cmd is set, and a size of 0 is 0x10000.
func patchCopy(cmd byte, base, delta, out []byte) ([]byte, []byte, error) {
	var arg [7]int
	for i := range 7 {
		if cmd&(1<<i) == 0 {
			continue
		}
		if len(delta) == 0 {
			return nil, nil, errors.New("its delta is cut short")
		}
		arg[i] = int(delta[0])
		delta = delta[1:]
	}
	off := arg[0] | arg[1]<<8 | arg[2]<<16 | arg[3]<<24
	size := arg[4] | arg[5]<<8 | arg[6]<<16
	if size == 0 {
		size = 0x10000
	}
	if off+size > len(base) {
		return nil, nil, errors.New("its delta copies past the part of its base errata reads")
	}
	return append(out, base[off:off+min(size, maxBase-len(out))]...), delta, nil
}
