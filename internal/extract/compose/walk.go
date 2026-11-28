package compose

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/consts"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/f-klement/errata/internal/extract/core"
)

// maxFiles is how many files one project may read, the main file, its
// overrides, extends files, includes and .env files counted, a .env file
// that is not there too, since each costs an open. Each include: entry the
// walk visits counts too, so a file included again and again along many
// ways is bounded as well.
const maxFiles = 150

// walker reads the files of a project, each once, within max_bytes in all,
// copies each to the stage, and routes every path compose-go will load: it
// lies inside the repository, and the walker read it first. compose-go
// loads no file the walker has not routed (guard), and opens none but the
// stage's.
type walker struct {
	ctx   context.Context
	root  string // the repository's root, as Extract got it
	stage *stage // the tree compose-go is given in place of the root
	parse Parser
	limit int64 // max_bytes, which the files read hold at most in all
	left  int64 // the bytes the files read may still hold
	// expanded counts the nodes YAML aliases add, in every file read.
	expanded int
	files    map[string]*file
	// routes are the files compose-go may load, below the root, by the
	// path it asks the guard for.
	routes map[route]string
	read   int // the files read and the includes visited, against maxFiles
	// including are the files whose include: the walk is in, outermost
	// first, so a file that includes one of them is refused as a cycle.
	including []string
	// merged is whether the project is built from more than one file or
	// service: an override, an include: or an extends: of any kind. Its
	// values are then read coarsely (tainted).
	merged bool
	// uses are the YAML files the project reads, each with the project
	// that loads it, once per such project.
	uses []use
}

// use is one YAML file the project reads: the project whose variables
// interpolate it, and the directory a path one of its services extends
// is relative to.
type use struct {
	f   *file
	p   *project
	dir string
}

// use records that p loads f, which resolves its extends paths against
// dir.
func (w *walker) use(p *project, f *file, dir string) {
	if !slices.ContainsFunc(w.uses, func(u use) bool { return u.f == f && u.p == p }) {
		w.uses = append(w.uses, use{f, p, dir})
	}
}

// fromShell reports whether a YAML file the project reads looks up a
// variable no file sets, anywhere in it.
func (w *walker) fromShell() bool {
	return slices.ContainsFunc(w.uses, func(u use) bool { return u.f.doc != nil && unset(u.f.doc, u.p.env) })
}

// file is one YAML file of a project: its path below the root, with
// slashes, its bytes, and its one document, nil for an empty file.
type file struct {
	path string
	data []byte
	doc  *core.Node
	// resolved is whether compose-go resolves the file's relative paths
	// against its directory, as it does for an extends file and an
	// included project's files: such a file may name no path under ~.
	resolved bool
}

// project is a Compose project: its directory below the root, its files
// in their order, the variables its .env files set, and the projects each
// of its files includes, in their order.
type project struct {
	dir      string
	files    []*file
	env      map[string]string
	includes map[*file][]*project
	// included is whether another project includes this one, so that
	// compose-go resolves its paths against its directory.
	included bool
	// loaded is, for an included project, the variables compose-go itself
	// interpolates it with (loader.ApplyInclude): env, with each variable
	// fromFiles leaves out as compose-go sets it, which may differ from
	// unset, such as empty.
	loaded map[string]string
}

// goEnv is the variables compose-go interpolates p's files with.
func (p *project) goEnv() map[string]string {
	if p.loaded != nil {
		return p.loaded
	}
	return p.env
}

// newWalker is a walker of the repository at root, with an empty stage,
// which close removes.
func newWalker(ctx context.Context, root string, parse Parser, limit int64) (*walker, error) {
	s, err := newStage()
	if err != nil {
		return nil, err
	}
	return &walker{ctx: ctx, root: root, stage: s, parse: parse, limit: limit, left: limit,
		files: map[string]*file{}, routes: map[route]string{}}, nil
}

// route is one path compose-go asks the guard for: ref, as compose-go
// hands it, interpolated, and from, the file on the stage whose load names
// it. compose-go gives the guard no more than these two. A path a service
// of from extends names, or one from includes, is relative to the
// directory of the project that loads from; a path a file deeper in the
// extends chain names is relative to that file's directory, and still
// comes to the guard with from, the file whose load reached it.
type route struct{ from, ref string }

// follow is the file ref, a path the file at names, as a path below the
// root, relative to dir, and routes it for the load of from: refused when
// the same path in the load of from already leads elsewhere, since
// compose-go would ask for both by the same two strings, and when it looks
// up a variable no file sets, since the shell would choose the file.
func (w *walker) follow(p *project, from, at *file, dir string, ref *core.Node) (string, error) {
	if err := shellRefused(at.path, ref, "path", p.env); err != nil {
		return "", err
	}
	rel, err := inside(at.path, dir, ref.Text, p.env)
	if err != nil {
		return "", err
	}
	text, _ := interpolate(ref.Text, p.env)
	k := route{w.absPath(from.path), text}
	if old, ok := w.routes[k]; ok && old != rel {
		return "", core.LineError(ref.Line, "%s: the path %s leads to %s here and to %s elsewhere in what %s loads, and compose-go asks errata for a path by that file and the path alone, so errata cannot tell which file it loads; write one of the two differently, such as %s", at.path, core.Quote(ref.Text), rel, old, from.path, core.Quote("./"+ref.Text))
	}
	w.routes[k] = rel
	return rel, nil
}

// close removes the stage.
func (w *walker) close() { w.stage.close() }

// absPath is rel, a path below the root, as compose-go is given it: the
// path of its copy on the stage.
func (w *walker) absPath(rel string) string {
	return filepath.Join(w.stage.dir, filepath.FromSlash(rel))
}

// clip is err with the stage's path taken out of its message, so a
// message names a file as the rule does, and cut to the length of a
// message.
func (w *walker) clip(err error) error {
	msg := err.Error()
	return core.ClipError(errors.New(strings.ReplaceAll(msg, w.stage.dir+string(filepath.Separator), "")))
}

// project reads the main file and the overrides of srcs, the .env file
// beside the main file, and every file they include or extend. A read of
// the main file alone also takes what docker compose loads beside it
// (alone): found says the read names the file docker compose finds by its
// name, as Every does, rather than as -f names it. fileOnly, a rule's
// overrides: [], reads the main file alone, as -f does, and asks nothing
// of what docker compose run without -f would load beside it.
func (w *walker) project(srcs []core.Source, found, fileOnly bool) (*project, error) {
	rels := make([]string, len(srcs))
	for i, s := range srcs {
		rels[i] = path.Clean(filepath.ToSlash(s.File))
	}
	dir := path.Dir(rels[0])
	env, picks, err := w.dotEnv(path.Join(dir, ".env"))
	if err != nil {
		return nil, err
	}
	if len(rels) == 1 && !fileOnly {
		if rels, err = w.alone(rels[0], picks, found); err != nil {
			return nil, err
		}
	}
	p := &project{dir: dir, env: env, includes: map[*file][]*project{}}
	w.merged = len(rels) > 1
	for _, rel := range rels {
		if err := w.addFile(p, rel, false); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// defaultNames are the names docker compose run without -f finds a
// project's file by, in its order of preference, and overrideNames those
// of the override it merges over that file from the same directory, the
// first one there (compose-go's cli.DefaultFileNames and
// DefaultOverrideFileNames).
var (
	defaultNames  = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}
	overrideNames = []string{"compose.override.yml", "compose.override.yaml", "docker-compose.override.yml", "docker-compose.override.yaml"}
)

// alone is the files a read of rel with no override loads. docker compose
// run without -f in rel's directory loads the files COMPOSE_FILE names
// when the .env file sets it (picks), and otherwise the first file of a
// default name there and the first override beside it (compose-go's
// cli.WithConfigFileEnv and WithDefaultConfigPath). With -f it loads rel
// alone. A read that names rel as docker compose finds it (found) loads
// the override with rel, so the project is merged, and is refused where
// docker compose loads another file in rel's place. A read that names rel
// as -f would is refused where docker compose run without -f would load
// more than rel, since the two give other values and the rule does not
// say which it means. COMPOSE_FILE is refused rather than followed: its
// separator is the platform's, and its files may leave rel out.
func (w *walker) alone(rel string, picks, found bool) ([]string, error) {
	dir, base := path.Split(rel)
	if picks {
		return nil, fmt.Errorf("compose: %s sets COMPOSE_FILE, so docker compose run without -f loads the files it names in place of %s alone; name the files in file: and overrides: of a compose rule, or write overrides: [] to read %s alone as -f does", path.Join(dir, ".env"), rel, rel)
	}
	i := slices.Index(defaultNames, base)
	if i < 0 {
		return []string{rel}, nil
	}
	first, err := w.firstThere(dir, defaultNames[:i])
	if err != nil || first != "" {
		return []string{rel}, w.notFound(err, first, rel, found)
	}
	over, err := w.firstThere(dir, overrideNames)
	switch {
	case err != nil:
		return nil, err
	case over == "":
		return []string{rel}, nil
	case !found:
		return nil, fmt.Errorf("compose: docker compose run without -f merges %s over %s; list it in overrides: to read the two merged, or write overrides: [] to read %s alone as -f does", over, rel, rel)
	}
	return []string{rel, over}, nil
}

// notFound is the error of a read of rel where docker compose run without
// -f loads first in its place: none for a read that names rel as -f does.
func (w *walker) notFound(err error, first, rel string, found bool) error {
	if err != nil || !found {
		return err
	}
	return fmt.Errorf("compose: docker compose run without -f loads %s, not %s beside it", first, rel)
}

// firstThere is the first of names that is in dir below the root, as
// compose-go looks for it with os.Stat, or "" when none is.
func (w *walker) firstThere(dir string, names []string) (string, error) {
	for _, n := range names {
		rel := path.Join(dir, n)
		_, err := core.Source{File: rel}.Stat(w.ctx, w.root)
		if err == nil {
			return rel, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return "", nil
}

// addFile reads the file rel into p, and walks what it extends and
// includes.
func (w *walker) addFile(p *project, rel string, resolved bool) error {
	f, err := w.yamlFile(rel, resolved)
	if err != nil {
		return err
	}
	p.files = append(p.files, f)
	w.use(p, f, p.dir)
	if err := w.extends(p, f); err != nil {
		return err
	}
	w.including = append(w.including, rel)
	err = w.includes(p, f)
	w.including = w.including[:len(w.including)-1]
	return err
}

// yamlFile reads and parses rel, once.
func (w *walker) yamlFile(rel string, resolved bool) (*file, error) {
	if f, ok := w.files[rel]; ok {
		f.resolved = f.resolved || resolved
		return f, nil
	}
	data, err := w.readFile(rel)
	if err != nil {
		return nil, err
	}
	docs, expanded, err := w.parse(data, w.expanded)
	w.expanded = expanded
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	f := &file{path: rel, data: data, resolved: resolved}
	if f.doc, err = configDoc(rel, docs); err != nil {
		return nil, err
	}
	w.files[rel] = f
	return f, nil
}

// configDoc is the one document of a Compose file, nil for an empty file.
func configDoc(rel string, docs []*core.Node) (*core.Node, error) {
	switch {
	case len(docs) == 0:
		return nil, nil
	case len(docs) > 1:
		return nil, fmt.Errorf("%s: holds %d YAML documents; errata reads a Compose file of one", rel, len(docs))
	case docs[0].Shape != core.Mapping:
		return nil, fmt.Errorf("%s: the top level of a Compose file is a mapping", rel)
	}
	return docs[0], nil
}

// count counts one more file read or include visited, refused past
// maxFiles.
func (w *walker) count() error {
	if w.read++; w.read > maxFiles {
		return fmt.Errorf("compose: the project reads and includes more than %d files, the limit", maxFiles)
	}
	return nil
}

// readFile reads rel within the bytes max_bytes leaves, and copies it to
// the stage.
func (w *walker) readFile(rel string) ([]byte, error) {
	if err := w.count(); err != nil {
		return nil, err
	}
	data, err := core.Source{File: rel, Limit: w.left}.Read(w.ctx, w.root)
	if err != nil && w.left < w.limit && errors.Is(err, core.ErrTooLarge) {
		return nil, fmt.Errorf("%w; the files of a Compose project hold at most max_bytes, %d, in all", err, w.limit)
	}
	if err != nil {
		return nil, err
	}
	w.left -= int64(len(data))
	return data, w.stage.put(rel, data)
}

// inside is ref, a path the file from names, as a path below the root:
// interpolated from env and relative to dir, the directory compose-go
// reads it against (follow). A path that is absolute, leaves
// the root or names a remote resource is refused.
func inside(from, dir, ref string, env map[string]string) (string, error) {
	ref, err := interpolate(ref, env)
	if err != nil {
		return "", fmt.Errorf("%s: %w", from, err)
	}
	switch {
	case remote(ref):
		return "", fmt.Errorf("%s: names %s, a remote resource, which errata cannot read offline", from, core.Quote(ref))
	case path.IsAbs(ref) || filepath.IsAbs(ref) || strings.HasPrefix(ref, "~"):
		return "", fmt.Errorf("%s: names %s, an absolute path, outside the repository", from, core.Quote(ref))
	}
	rel := path.Join(dir, filepath.ToSlash(ref))
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s: names %s, which leaves the repository", from, core.Quote(ref))
	}
	return rel, nil
}

// scpLike is the scp-like form of a git URL, user@host:path, as in
// git@github.com:org/repo.git, which docker compose fetches as a remote.
var scpLike = regexp.MustCompile(`^[A-Za-z0-9._~-]+@[A-Za-z0-9.-]+:`)

// remote reports whether ref names a resource docker compose fetches
// rather than reads from the disk: a URL, an OCI artifact or a git
// repository in its scp-like form.
func remote(ref string) bool {
	return strings.Contains(ref, "://") || strings.HasPrefix(ref, "oci:") || scpLike.MatchString(ref)
}

// interpolate is s with each ${VAR} replaced from env, as compose-go
// replaces it: a variable env does not set is empty.
func interpolate(s string, env map[string]string) (string, error) {
	return interpolateWith(s, func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
}

// interpolateWith is s with each ${VAR} replaced by what lookup gives, as
// compose-go replaces it.
func interpolateWith(s string, lookup template.Mapping) (string, error) {
	return template.SubstituteWithOptions(s, lookup, template.WithoutLogging)
}

// extends reads the files the extends chain of each service of f names,
// f being a file p loads, as compose-go follows the chain: a service's
// extends file relative to p's directory, and one a service of that file
// extends relative to that file's directory, and so on. Only the services
// a chain reaches are followed, so a service of an extends file that no
// chain reaches names nothing errata reads. done holds each service
// followed, with the directory its paths are relative to, so a chain that
// meets another, or itself, stops there.
func (w *walker) extends(p *project, f *file) error {
	done := map[link]bool{}
	for _, svc := range services(f) {
		if err := w.chain(p, f, link{f, p.dir, svc}, done); err != nil {
			return err
		}
	}
	return checkHome(f, p.env)
}

// link is one service of an extends chain: the file it stands in, the
// directory the paths it names are relative to, and its mapping.
type link struct {
	at  *file
	dir string
	svc *core.Node
}

// chain follows the extends chain from l, a service from loads, to its
// end, and reads each file it names. A service name that looks up a
// variable no file sets is refused, as a path is (follow).
func (w *walker) chain(p *project, from *file, l link, done map[link]bool) error {
	for l.svc != nil && !done[l] {
		done[l] = true
		name, ref := extendsOf(l.svc.Field["extends"])
		if name == nil {
			return nil
		}
		w.merged = true
		at := l.at // the file that writes the name, before l moves on
		if ref != nil {
			rel, err := w.follow(p, from, l.at, l.dir, ref)
			if err != nil {
				return err
			}
			if l.at, err = w.yamlFile(rel, true); err != nil {
				return err
			}
			if err := checkHome(l.at, p.env); err != nil {
				return err
			}
			l.dir = path.Dir(rel)
			w.use(p, l.at, l.dir)
		}
		if err := shellRefused(at.path, name, "extends service", p.env); err != nil {
			return err
		}
		text, _ := interpolate(name.Text, p.env)
		l.svc = service(l.at, text)
	}
	return nil
}

// extendsOf is the service and the file an extends: names, each nil when
// it names none: extends: NAME names a service of the same file.
func extendsOf(ext *core.Node) (name, ref *core.Node) {
	switch {
	case ext == nil:
		return nil, nil
	case ext.Shape == core.Scalar:
		return ext, nil
	}
	return scalarField(ext, "service"), scalarField(ext, "file")
}

// services are the service mappings of f, by name in its order.
func services(f *file) []*core.Node {
	if f.doc == nil {
		return nil
	}
	s := f.doc.Field["services"]
	if s == nil || s.Shape != core.Mapping {
		return nil
	}
	var out []*core.Node
	for _, v := range s.Vals {
		if v.Shape == core.Mapping {
			out = append(out, v)
		}
	}
	return out
}

// scalarField is the scalar under key of the mapping n, nil when n is no
// mapping or holds no scalar there.
func scalarField(n *core.Node, key string) *core.Node {
	if n == nil || n.Shape != core.Mapping {
		return nil
	}
	v := n.Field[key]
	if v == nil || v.Shape != core.Scalar {
		return nil
	}
	return v
}

// includes reads each project the include: of f names, in its order.
func (w *walker) includes(p *project, f *file) error {
	if f.doc == nil || f.doc.Field["include"] == nil {
		return nil
	}
	inc := f.doc.Field["include"]
	if inc.Shape != core.Sequence {
		return fmt.Errorf("%s: include is a list", f.path)
	}
	for _, item := range inc.Items {
		w.merged = true
		q, err := w.include(p, f, item)
		if err != nil {
			return err
		}
		p.includes[f] = append(p.includes[f], q)
	}
	return nil
}

// include reads the project one entry of an include: names: its files and
// its .env files, those an env_file: lists, each relative to the including
// project's directory, or with none listed the .env beside its first file.
// A project_directory is refused, and so is a file that includes itself,
// or a file that includes it, which compose-go would refuse only after
// the walk had recursed without end.
func (w *walker) include(p *project, f *file, item *core.Node) (*project, error) {
	paths, envFiles, err := includeEntry(f.path, item)
	if err != nil {
		return nil, err
	}
	if err := w.count(); err != nil {
		return nil, err
	}
	var rels []string
	for _, ref := range paths {
		rel, err := w.follow(p, f, f, p.dir, ref)
		if err != nil {
			return nil, err
		}
		if err := w.cycle(f.path, rel); err != nil {
			return nil, err
		}
		rels = append(rels, rel)
	}
	q := &project{dir: path.Dir(rels[0]), includes: map[*file][]*project{}, included: true}
	if q.env, err = w.includeEnv(p, q, f.path, envFiles); err != nil {
		return nil, err
	}
	for _, rel := range rels {
		if err := w.addFile(q, rel, true); err != nil {
			return nil, err
		}
	}
	return q, nil
}

// cycle refuses rel, which from includes, when the walk is in its
// include: already, naming the files of the cycle.
func (w *walker) cycle(from, rel string) error {
	i := slices.Index(w.including, rel)
	if i < 0 {
		return nil
	}
	return fmt.Errorf("%s: include cycle: %s", from, strings.Join(append(slices.Clone(w.including[i:]), rel), " includes "))
}

// includeEntry is the paths and the env files of one include: entry, a
// path or a mapping of path, env_file and project_directory.
func includeEntry(from string, item *core.Node) (paths, envFiles []*core.Node, err error) {
	if item.Shape == core.Scalar {
		return []*core.Node{item}, nil, nil
	}
	if item.Shape != core.Mapping {
		return nil, nil, core.LineError(item.Line, "%s: an include entry is a path or a mapping", from)
	}
	if item.Field["project_directory"] != nil {
		return nil, nil, core.LineError(item.Line, "%s: errata does not read an include's project_directory; include the project from its own directory", from)
	}
	if paths, err = stringList(from, item.Field["path"]); err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		return nil, nil, core.LineError(item.Line, "%s: an include entry names no path", from)
	}
	envFiles, err = stringList(from, item.Field["env_file"])
	return paths, envFiles, err
}

// stringList is n as a list of scalars: a scalar is a list of one, and nil
// none.
func stringList(from string, n *core.Node) ([]*core.Node, error) {
	switch {
	case n == nil:
		return nil, nil
	case n.Shape == core.Scalar:
		return []*core.Node{n}, nil
	case n.Shape != core.Sequence:
		return nil, core.LineError(n.Line, "%s: write a path or a list of paths", from)
	}
	for _, it := range n.Items {
		if it.Shape != core.Scalar {
			return nil, core.LineError(it.Line, "%s: write a path or a list of paths", from)
		}
	}
	return n.Items, nil
}

// includeEnv is the variables an included project q interpolates with,
// built as compose-go builds them (loader.ApplyInclude), with its own
// functions: the variables of the including project p, and under them
// those q's env files set, which compose-go's dotenv reads from the stage.
// A variable p sets wins. One whose value takes a variable neither sets is
// left out (fromFiles), and kept in q.loaded as compose-go sets it.
func (w *walker) includeEnv(p, q *project, from string, envFiles []*core.Node) (map[string]string, error) {
	files, data, err := w.includeEnvFiles(p, q, from, envFiles)
	if err != nil {
		return nil, err
	}
	parse := func(env map[string]string) (map[string]string, error) {
		return dotenv.GetEnvFromFile(env, files)
	}
	own, err := fromFiles(parse, p.env, data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", from, w.clip(err))
	}
	all, _ := parse(p.env) // fromFiles parsed the same without an error
	q.loaded = types.Mapping(merged(p.env, nil)).Merge(all)
	return types.Mapping(merged(p.env, nil)).Merge(own), nil
}

// includeEnvFiles are the env files of the included project q, as paths on
// the stage: those its include: entry in from lists, each relative to the
// including project p's directory, or with none listed the .env beside
// q's first file when it is there. compose-go opens these itself, through
// no resource loader, so a path that names a variable is refused: errata
// reads it as written, and compose-go opens the copy errata read. data are
// the files' bytes.
func (w *walker) includeEnvFiles(p, q *project, from string, envFiles []*core.Node) (out []string, data [][]byte, err error) {
	if len(envFiles) == 0 {
		rel := path.Join(q.dir, ".env")
		d, err := w.readFile(rel)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		return []string{w.absPath(rel)}, [][]byte{d}, nil
	}
	for _, n := range envFiles {
		ef := n.Text
		if ef == "/dev/null" {
			continue
		}
		if strings.Contains(ef, "$") {
			return nil, nil, fmt.Errorf("%s: the include env_file %s holds a $; compose-go opens an include's env file itself, so errata takes its path only as written", from, core.Quote(ef))
		}
		rel, err := inside(from, p.dir, ef, nil)
		if err != nil {
			return nil, nil, err
		}
		d, err := w.readFile(rel)
		if err != nil {
			return nil, nil, err
		}
		out, data = append(out, w.absPath(rel)), append(data, d)
	}
	return out, data, nil
}

// merged is a copy of base with over's variables set over it.
func merged(base, over map[string]string) map[string]string {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, over)
	return out
}

// dotEnv is the variables the main project's .env file rel sets, its
// ${VAR} looked up in the variables above it; none when it is not there.
// It reads nothing else, so no variable of the process's environment
// reaches the project. compose-go is handed these variables, and never
// opens the file. It also says whether the file sets COMPOSE_FILE, from
// the shell or not, which chooses the project's files (alone).
func (w *walker) dotEnv(rel string) (map[string]string, bool, error) {
	data, err := w.readFile(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	picks := false // whether the file sets COMPOSE_FILE, from the shell or not
	vars, err := fromFiles(func(env map[string]string) (map[string]string, error) {
		vs, err := dotenv.UnmarshalBytesWithLookup(data, func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		})
		_, set := vs[consts.ComposeFilePath]
		picks = picks || set
		return vs, err
	}, nil, [][]byte{data})
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", rel, err)
	}
	return vars, picks, nil
}

// fromShell is the value fromFiles gives a variable no file sets, a text
// no file can hold.
const fromShell = "\x00errata: the shell's\x00"

// fromFiles is the variables parse reads from env files of the bytes data,
// with env to look a ${VAR} up in, less each whose value takes a variable
// that neither env nor the files set, even through a default: docker
// compose takes that one from its shell, which errata does not read, so
// the variable has no value errata knows, and a ${VAR} of it is not
// literal. parse runs again with each such variable set to fromShell, and
// a variable whose value changes is the one left out.
func fromFiles(parse func(env map[string]string) (map[string]string, error), env map[string]string, data [][]byte) (map[string]string, error) {
	vars, err := parse(env)
	if err != nil {
		return nil, err
	}
	shell := merged(env, nil)
	for _, k := range looksUp(data) {
		_, inEnv := env[k]
		if _, inVars := vars[k]; !inEnv && !inVars {
			shell[k] = fromShell
		}
	}
	if len(shell) == len(env) {
		return vars, nil
	}
	marked, err := parse(shell)
	if err != nil {
		return nil, err
	}
	for k, v := range vars {
		if marked[k] != v {
			delete(vars, k)
		}
	}
	return vars, nil
}

// looksUp are the variables env files of the bytes data look up, found or
// not: each file is parsed with every lookup failing and again with every
// lookup found, since a default such as ${A:-${B}} asks for B only in the
// one case and ${A:+${B}} only in the other.
func looksUp(data [][]byte) []string {
	var out []string
	record := func(found bool) dotenv.LookupFn {
		return func(k string) (string, bool) {
			out = append(out, k)
			return fromShell, found
		}
	}
	for _, d := range data {
		_, _ = dotenv.UnmarshalBytesWithLookup(d, record(false))
		_, _ = dotenv.UnmarshalBytesWithLookup(d, record(true))
	}
	return out
}

// checkHome refuses, in a file whose paths compose-go resolves (an extends
// file or an included project's), a value that starts with ~, which
// compose-go expands from the HOME of the process's environment, and a
// develop.watch path that is absolute or climbs with .., whose links
// compose-go follows on the disk. A file whose paths it leaves as written
// is not checked: it expands neither.
func checkHome(f *file, env map[string]string) error {
	if !f.resolved || f.doc == nil {
		return nil
	}
	if err := homeScalars(f, f.doc, env); err != nil {
		return err
	}
	for _, svc := range services(f) {
		if err := watchPaths(f, svc.Field["develop"], env); err != nil {
			return err
		}
	}
	return nil
}

// homeScalars refuses a scalar of n whose value starts with ~. The parser
// bounds the depth of n before this, at core.MaxDepth.
func homeScalars(f *file, n *core.Node, env map[string]string) error {
	if n.Shape == core.Scalar {
		return homeScalar(f, n, env)
	}
	for _, c := range append(slices.Clone(n.Vals), n.Items...) {
		if err := homeScalars(f, c, env); err != nil {
			return err
		}
	}
	return nil
}

func homeScalar(f *file, n *core.Node, env map[string]string) error {
	text := n.Text
	if strings.Contains(text, "$") {
		text, _ = interpolate(text, env)
	}
	if strings.HasPrefix(text, "~") {
		return core.LineError(n.Line, "%s: %s starts with ~, which compose resolves from the HOME of the environment errata does not read", f.path, core.Quote(n.Text))
	}
	return nil
}

// watchPaths refuses a develop.watch path of develop that is absolute or
// holds a .. part.
func watchPaths(f *file, develop *core.Node, env map[string]string) error {
	if develop == nil || develop.Shape != core.Mapping || develop.Field["watch"] == nil {
		return nil
	}
	for _, w := range develop.Field["watch"].Items {
		p := scalarField(w, "path")
		if p == nil {
			continue
		}
		text, _ := interpolate(p.Text, env)
		if path.IsAbs(text) || slices.Contains(strings.Split(filepath.ToSlash(text), "/"), "..") {
			return core.LineError(p.Line, "%s: the develop.watch path %s is absolute or climbs with .., and compose follows its links on the disk", f.path, core.Quote(p.Text))
		}
	}
	return nil
}
