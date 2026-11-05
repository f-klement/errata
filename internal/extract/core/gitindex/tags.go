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
	dir, err := gitDir(top, l)
	if err != nil {
		return nil, err
	}
	common, err := commonDir(dir, l)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(common, "reftable")); err == nil {
		return nil, errors.New("the repository keeps its refs in a reftable, which errata does not read")
	}
	r := refs{dir: dir, common: common, l: l}
	if r.packed, r.peeledAll, err = readPacked(filepath.Join(common, "packed-refs"), l); err != nil {
		return nil, err
	}
	loose, err := looseTags(ctx, common, l)
	if err != nil {
		return nil, err
	}
	isLoose := make(map[string]bool, len(loose))
	for _, tag := range loose {
		isLoose[tag] = true
	}
	names := slices.Clone(loose)
	for name := range r.packed {
		if tag, ok := strings.CutPrefix(name, "refs/tags/"); ok && !isLoose[tag] {
			names = append(names, tag)
		}
	}
	if len(names) == 0 {
		if _, err := os.Lstat(filepath.Join(common, "shallow")); err == nil {
			return nil, ErrShallowUntagged
		}
		return nil, nil
	}
	head, err := r.resolve("HEAD")
	if err != nil || head == "" {
		return nil, err
	}
	slices.Sort(names)
	o := objects{dir: filepath.Join(common, "objects")}
	defer o.close()
	var out []string
	for _, tag := range names {
		if !strings.HasPrefix(tag, prefix) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		on, err := r.points(&o, "refs/tags/"+tag, isLoose[tag], head)
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
	packed := map[string]packedRef{}
	data, err := readCapped(name, l.Bytes)
	if errors.Is(err, fs.ErrNotExist) {
		return packed, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read packed-refs: %w", bare(err))
	}
	peeledAll := false
	last := ""
	if len(data) == 0 {
		return packed, false, nil
	}
	for n, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		bad := func() error { return fmt.Errorf("packed-refs: line %d is no line git writes", n+1) }
		switch {
		case n == 0 && strings.HasPrefix(line, "# pack-refs with:"):
			peeledAll = slices.Contains(strings.Fields(strings.TrimPrefix(line, "# pack-refs with:")), "peeled")
		case strings.HasPrefix(line, "^"):
			ref, ok := packed[last]
			if !ok || ref.peeled != "" || !objectName(line[1:]) {
				return nil, false, bad()
			}
			ref.peeled = line[1:]
			packed[last] = ref
		default:
			id, ref, ok := strings.Cut(line, " ")
			if !ok || !objectName(id) || !refName(ref) || len(ref) > l.Name {
				return nil, false, bad()
			}
			if len(packed) == l.Entries {
				return nil, false, fmt.Errorf("packed-refs lists more than %d refs, the limit", l.Entries)
			}
			packed[ref] = packedRef{id: id}
			last = ref
		}
	}
	return packed, peeledAll, nil
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
	if strings.ContainsFunc(name, func(c rune) bool { return c <= ' ' || c == 0x7f || strings.ContainsRune("~^:?*[\\", c) }) {
		return false
	}
	for part := range strings.SplitSeq(rest, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
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
	var out []string
	entries := 0
	var walk func(rel string) error
	walk = func(rel string) error {
		list, err := os.ReadDir(filepath.Join(common, "refs", "tags", filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) && rel == "" {
			return nil
		}
		if err != nil {
			return fmt.Errorf("list refs/tags: %w", bare(err))
		}
		for _, e := range list {
			if entries++; entries > l.Entries {
				return fmt.Errorf("refs/tags holds more than %d entries, the limit", l.Entries)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			name := tagName(rel, e.Name())
			switch {
			case len("refs/tags/"+name) > l.Name:
				return fmt.Errorf("refs/tags holds a name over %d bytes, the limit", l.Name)
			case e.IsDir():
				if err := walk(name); err != nil {
					return err
				}
			case !e.Type().IsRegular():
				return fmt.Errorf("refs/tags/%s is neither a file nor a directory, which errata does not read as a ref", clip.Quote(name))
			case strings.HasSuffix(name, ".lock"):
			case !refName("refs/tags/" + name):
				return fmt.Errorf("refs/tags/%s is no ref name git takes", clip.Quote(name))
			default:
				out = append(out, name)
			}
		}
		return nil
	}
	return out, walk("")
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
	if !o.listed {
		o.listed = true
		if err := noLink(o.dir, "pack"); err != nil {
			return "", nil, fmt.Errorf("objects/%w", err)
		}
		list, err := os.ReadDir(filepath.Join(o.dir, "pack"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", nil, fmt.Errorf("list the packs: %w", bare(err))
		}
		for _, e := range list {
			if strings.HasSuffix(e.Name(), ".idx") {
				o.names = append(o.names, filepath.Join(o.dir, "pack", e.Name()))
			}
		}
		if len(o.names) > maxPacks {
			return "", nil, fmt.Errorf("the repository holds more than %d packs, the limit; git gc packs them into one", maxPacks)
		}
	}
	name, err := hex.DecodeString(id)
	if err != nil {
		return "", nil, err
	}
	for i, idx := range o.names {
		if i == len(o.packs) {
			p, err := openPack(idx)
			if err != nil {
				return "", nil, err
			}
			o.packs = append(o.packs, p)
		}
		p := o.packs[i]
		if err := o.spend(lookupWork); err != nil {
			return "", nil, err
		}
		at, found, err := p.lookup(name)
		if err != nil {
			return "", nil, fmt.Errorf("read %s: %w", filepath.Base(idx), err)
		}
		if found {
			kind, body, err := o.object(p, at, len(name))
			if err != nil {
				return "", nil, fmt.Errorf("read %s: %w", p.name, err)
			}
			return kind, body[:min(len(body), maxHead)], nil
		}
	}
	return "", nil, errors.New("it is in no loose object and no pack the repository holds, as in a clone that borrows objects through alternates, which errata does not read")
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
	lo, hi := int64(0), p.fan(int(name[0]))
	if name[0] > 0 {
		lo = p.fan(int(name[0]) - 1)
	}
	if lo > hi || hi > p.n {
		return 0, false, errors.New("its fan out is not in order")
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
	n := int64(1)
	for i := 0; c&0x80 != 0; i++ {
		if c, err = r.ReadByte(); err != nil || i == 9 {
			return entry{}, fmt.Errorf("its object at %d has no header git writes", at)
		}
		n++
	}
	switch e.kind {
	case ofsDelta:
		// Each byte after the first adds one before the shift, as git's
		// encoding does, so no distance has two forms.
		var back int64
		for i := 0; ; i++ {
			if c, err = r.ReadByte(); err != nil || i == 9 {
				return entry{}, fmt.Errorf("its delta at %d names no base git writes", at)
			}
			n++
			back = back<<7 | int64(c&0x7f)
			if c&0x80 == 0 {
				break
			}
			back++
		}
		if back <= 0 || back > at {
			return entry{}, fmt.Errorf("its delta at %d names a base outside the pack", at)
		}
		e.base = at - back
	case refDelta:
		e.ref = make([]byte, hash)
		if _, err := io.ReadFull(r, e.ref); err != nil {
			return entry{}, fmt.Errorf("its delta at %d is cut short", at)
		}
		n += int64(hash)
	default:
		if _, ok := packTypes[e.kind]; !ok {
			return entry{}, fmt.Errorf("its object at %d is of type %d, which git does not write", at, e.kind)
		}
	}
	e.stream = at + n
	return e, nil
}

// object is the type and the start, at most maxBase bytes, of the object
// at offset at in the pack p, whose names are hash bytes long. An object
// stored as a delta is rebuilt from its chain of bases, at most maxChain
// deep, from the nearest object of the chain o has kept: a delta whose
// base is in another pack is refused, as git keeps a pack whole.
func (o *objects) object(p *pack, at int64, hash int) (string, []byte, error) {
	var chain []entry // the deltas, the outermost first
	var data []byte
	var kind byte
	for {
		if got, ok := o.cache[cached{p, at}]; ok {
			data, kind = got[1:], got[0]
			break
		}
		if err := o.spend(entryWork); err != nil {
			return "", nil, err
		}
		e, err := readEntry(p.data, at, hash)
		if err != nil {
			return "", nil, err
		}
		if e.kind != ofsDelta && e.kind != refDelta {
			if data, err = o.inflate(io.NewSectionReader(p.data, e.stream, maxDeflated), maxBase); err != nil {
				return "", nil, err
			}
			kind = e.kind
			break
		}
		if len(chain) == maxChain {
			return "", nil, fmt.Errorf("its object leads through more than %d deltas", maxChain)
		}
		e.at = at
		chain = append(chain, e)
		at = e.base
		if e.kind == refDelta {
			var found bool
			if err = o.spend(lookupWork); err != nil {
				return "", nil, err
			}
			if at, found, err = p.lookup(e.ref); err == nil && !found {
				err = fmt.Errorf("its delta names the base %x, which the pack does not hold", e.ref)
			}
			if err != nil {
				return "", nil, err
			}
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		delta, err := o.inflate(io.NewSectionReader(p.data, chain[i].stream, maxDeflated), maxBase)
		if err == nil {
			data, err = patch(data, delta)
		}
		if err == nil {
			err = o.spend(int64(len(data)))
		}
		if err != nil {
			return "", nil, err
		}
		// The outermost object is the one peeled, which peel remembers;
		// the bases below it are kept for the next chain that shares them.
		if i > 0 {
			o.keep(p, chain[i].at, kind, data)
		}
	}
	return packTypes[kind], data, nil
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
	for range 2 {
		for len(delta) > 0 && delta[0]&0x80 != 0 {
			delta = delta[1:]
		}
		if len(delta) == 0 {
			return nil, errors.New("its delta is cut short")
		}
		delta = delta[1:]
	}
	var out []byte
	for len(delta) > 0 && len(out) != maxBase {
		cmd := delta[0]
		delta = delta[1:]
		switch {
		case cmd&0x80 != 0:
			var arg [7]int
			for i := range 7 {
				if cmd&(1<<i) == 0 {
					continue
				}
				if len(delta) == 0 {
					return nil, errors.New("its delta is cut short")
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
				return nil, errors.New("its delta copies past the part of its base errata reads")
			}
			out = append(out, base[off:off+min(size, maxBase-len(out))]...)
		case cmd != 0:
			if int(cmd) > len(delta) {
				return nil, errors.New("its delta is cut short")
			}
			out = append(out, delta[:min(int(cmd), maxBase-len(out))]...)
			delta = delta[cmd:]
		default:
			return nil, errors.New("its delta holds an instruction git does not write")
		}
	}
	return out, nil
}
