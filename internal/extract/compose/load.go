package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/compose-spec/compose-go/v2/consts"
	cg "github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/mattn/go-shellwords"
	"github.com/sirupsen/logrus"

	"github.com/f-klement/errata/internal/extract/core"
)

// compose-go writes its warnings, such as a variable that is not set or an
// obsolete version: key, to logrus's standard logger. errata reports what
// matters of them itself (a value not literal), and nothing else in the
// binary logs through logrus, so the logger writes nowhere.
func init() { logrus.SetOutput(io.Discard) }

// projectName is the name every project is loaded under. A name is
// compose-go's to check and changes no service's configuration, so the
// name: of a file, which compose-go would derive the name from, is not
// read for it.
const projectName = "errata"

// guard is the one resource loader compose-go is given: it resolves the
// path an include: or an extends: file names, in the load of the file
// compose-go is loading, to the file the walker routed it to, and refuses
// a path the walker did not route. The path it gives is the file's copy on
// the stage, which compose-go then opens itself.
type guard struct {
	stage  string           // the stage's directory
	routes map[route]string // the walker's routes, to paths below the root
	last   string           // the path Load gave last, whose directory Dir is
}

// Accept takes every path, so compose-go's own loader, which would read
// any, is never asked.
func (g *guard) Accept(string) bool { return true }

// Load is the path p names, given the file compose-go is loading, which
// ctx holds.
func (g *guard) Load(ctx context.Context, p string) (string, error) {
	from, _ := ctx.Value(consts.ComposeFileKey{}).(string)
	rel, ok := g.routes[route{from, p}]
	if !ok || from == "" {
		return "", fmt.Errorf("compose: %s, which compose-go would load, is no file errata read and routed", core.Quote(p))
	}
	g.last = filepath.Join(g.stage, filepath.FromSlash(rel))
	return g.last, nil
}

// Dir is the directory of the file p names: compose-go asks it of the path
// Load gave, or of the path it gave Load.
func (g *guard) Dir(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Dir(p)
	}
	return filepath.Dir(g.last)
}

// loader builds a project's configuration with compose-go, and each file's
// own configuration, for locate.
type loader struct {
	w     *walker
	guard *guard
	// own is the configuration of each service each file sets itself, as
	// compose-go canonicalizes it, by the file and the project that loads
	// it.
	own map[ownKey]map[string]any
}

type ownKey struct {
	f *file
	p *project
}

func newLoader(w *walker) *loader {
	return &loader{w: w, guard: &guard{stage: w.stage.dir, routes: w.routes}, own: map[ownKey]map[string]any{}}
}

// configFiles are the files compose-go loads, by their absolute paths,
// with the bytes the walker read.
func (l *loader) configFiles(files []*file) []types.ConfigFile {
	out := make([]types.ConfigFile, len(files))
	for i, f := range files {
		out[i] = types.ConfigFile{Filename: l.w.absPath(f.path), Content: f.data}
	}
	return out
}

// options are the options every load takes: the guard as its only
// resource loader, the .env files' variables as its only environment
// (ConfigDetails), and env_file: and label_file: left unread, since
// compose-go would open them itself: this package reads a service's
// env_file: (envFile) and refuses a read a label_file: or a hook's
// env_file: could reach (unreadFiles).
func (l *loader) options(o *cg.Options) {
	o.SetProjectName(projectName, true)
	o.ResourceLoaders = []cg.ResourceLoader{l.guard}
	o.SkipResolveEnvironment = true
	o.SkipResolveLabels = true
}

// load is p's configuration, under the profiles given, as compose-go
// builds it from p's files, with the options more sets last. A main
// project's paths stay as written.
func (l *loader) load(p *project, profiles []string, every bool, more ...func(*cg.Options)) (*types.Project, error) {
	details := types.ConfigDetails{WorkingDir: l.w.absPath(p.dir), ConfigFiles: l.configFiles(p.files), Environment: merged(p.env, nil)}
	opts := append([]func(*cg.Options){l.options, func(o *cg.Options) {
		o.ResolvePaths = false
		o.Profiles = profiles
		// Every reads each service, active or not, so a service that
		// needs one a profile leaves out is not refused.
		o.SkipConsistencyCheck = every
	}}, more...)
	proj, err := safely(l.w.ctx, details, opts...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.files[0].path, l.w.clip(err))
	}
	return proj, nil
}

// ownServices is the configuration of each service f sets itself, without
// what it extends or includes, as compose-go canonicalizes it in p, by the
// service's name: what locate compares the project's values with.
func (l *loader) ownServices(f *file, p *project) (map[string]any, error) {
	key := ownKey{f, p}
	if s, ok := l.own[key]; ok {
		return s, nil
	}
	dir := p.dir
	if f.resolved {
		dir = filepath.ToSlash(filepath.Dir(f.path))
	}
	// compose-go's own variables, so that the file's own values match the
	// project's where a variable from the shell is empty there.
	details := types.ConfigDetails{WorkingDir: l.w.absPath(dir), ConfigFiles: l.configFiles([]*file{f}), Environment: merged(p.goEnv(), nil)}
	proj, err := safely(l.w.ctx, details, l.options, func(o *cg.Options) {
		o.ResolvePaths = f.resolved
		o.SkipExtends, o.SkipInclude = true, true
		o.SkipValidation, o.SkipConsistencyCheck = true, true
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.path, l.w.clip(err))
	}
	out := map[string]any{}
	for name, svc := range proj.AllServices() {
		if out[name], err = serviceTree(svc); err != nil {
			return nil, err
		}
		if raw := service(f, name); raw != nil {
			bareArgs(out[name], argNames(raw, p))
		}
	}
	l.own[key] = out
	return out, nil
}

// safely is compose-go's load of details, with a panic of the library
// returned as an error: a load that skips validation, as ownServices does,
// may reach a type assertion its validation would have kept it from.
//
// compose-go splits a command written as one string with go-shellwords,
// whose package switches, when a program in the binary sets them, run a
// `backtick` through the shell and read $VAR from the process's
// environment. Nothing in errata sets them; a load refuses to start if
// anything has, so neither can happen under it.
func safely(ctx context.Context, details types.ConfigDetails, opts ...func(*cg.Options)) (p *types.Project, err error) {
	if shellwords.ParseBacktick || shellwords.ParseEnv {
		return nil, errors.New("compose: go-shellwords is set to run backticks or read the environment, which errata never does; refusing to load")
	}
	defer func() {
		if r := recover(); r != nil {
			p, err = nil, fmt.Errorf("compose-go failed on the file: %v", r)
		}
	}()
	return cg.LoadWithContext(ctx, details, opts...)
}

// serviceTree is the configuration of svc, as tree gives it, with the x-
// keys compose-go keeps in the Extensions of each of its parts, which its
// JSON leaves out (withExtensions), and build.ssh as a mapping of each
// key's id to its path, which compose-go's JSON does not write as JSON.
func serviceTree(svc types.ServiceConfig) (any, error) {
	plain := svc
	if svc.Build != nil {
		b := *svc.Build
		b.SSH = nil
		plain.Build = &b
	}
	t, err := tree(plain)
	if err != nil {
		return nil, err
	}
	t, err = withExtensions(reflect.ValueOf(svc), t)
	if err != nil || svc.Build == nil || svc.Build.SSH == nil {
		return t, err
	}
	ssh := map[string]any{}
	for _, k := range svc.Build.SSH {
		ssh[k.ID] = nil
		if k.Path != "" {
			ssh[k.ID] = k.Path
		}
	}
	t.(map[string]any)["build"].(map[string]any)["ssh"] = ssh
	return t, nil
}

// withExtensions is t, the JSON tree of v, with the x- keys of each
// Extensions field of v's structs added to the mapping that writes the
// struct. A mapping that JSON left out, as it leaves out an empty one, is
// made for them; a part whose JSON is no mapping, where compose-go writes
// it its own way, is left as it is.
func withExtensions(v reflect.Value, t any) (any, error) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return t, nil
		}
		return withExtensions(v.Elem(), t)
	case reflect.Struct:
		return structExtensions(v, t)
	case reflect.Slice:
		return listExtensions(v, t)
	case reflect.Map:
		return mapExtensions(v, t)
	default:
		return t, nil
	}
}

// listExtensions is withExtensions of v, a slice, item by item, when t is
// a list of as many items.
func listExtensions(v reflect.Value, t any) (any, error) {
	list, _ := t.([]any)
	if len(list) != v.Len() {
		return t, nil
	}
	for i := range list {
		var err error
		if list[i], err = withExtensions(v.Index(i), list[i]); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// mapExtensions is withExtensions of v, a mapping by strings, key by key
// of t.
func mapExtensions(v reflect.Value, t any) (any, error) {
	m, _ := t.(map[string]any)
	if v.Type().Key().Kind() != reflect.String {
		return t, nil
	}
	for k, sub := range m {
		var err error
		if m[k], err = withExtensions(v.MapIndex(reflect.ValueOf(k).Convert(v.Type().Key())), sub); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// structExtensions is withExtensions of v, a struct.
func structExtensions(v reflect.Value, t any) (any, error) {
	m, ok := t.(map[string]any)
	if t != nil && !ok {
		return t, nil
	}
	for i := range v.NumField() {
		var err error
		if m, err = fieldExtensions(m, v.Type().Field(i), v.Field(i)); err != nil {
			return nil, err
		}
	}
	if m == nil {
		return t, nil
	}
	return m, nil
}

// fieldExtensions is m, the mapping that writes a struct, with the x- keys
// of its field f, of value v: the keys themselves when f is the struct's
// Extensions, those of an embedded struct's fields, whose JSON is m's own,
// or those below the key f writes.
func fieldExtensions(m map[string]any, f reflect.StructField, v reflect.Value) (map[string]any, error) {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	switch {
	case f.Type == reflect.TypeFor[types.Extensions]():
		return addExtensions(m, v.Interface().(types.Extensions))
	case f.Anonymous && name == "":
		sub, err := structExtensions(v, m)
		out, _ := sub.(map[string]any)
		return out, err
	case name != "" && name != "-" && m != nil:
		return m, setChild(m, name, v)
	}
	return m, nil
}

// setChild sets m's key name to withExtensions of v and what m holds
// there, unless both are nothing.
func setChild(m map[string]any, name string, v reflect.Value) error {
	sub, err := withExtensions(v, m[name])
	if sub != nil {
		m[name] = sub
	}
	return err
}

// addExtensions is m with the keys of ext, as tree gives them; a new
// mapping when m is nil and ext holds a key.
func addExtensions(m map[string]any, ext types.Extensions) (map[string]any, error) {
	if len(ext) == 0 {
		return m, nil
	}
	t, err := tree(ext)
	if err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	maps.Copy(m, t.(map[string]any))
	return m, nil
}

// tree is v as JSON decodes it, numbers kept as written: the shape
// docker compose config --format json gives a service.
func tree(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("compose: %w", err)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var out any
	if err := d.Decode(&out); err != nil {
		return nil, fmt.Errorf("compose: %w", err)
	}
	return out, nil
}
