// Package plugin is errata's plugin host (ADR 0020): it reads errata.lock,
// finds each plugin the lock pins in the repository's vendored plugin
// directory or in the cache, checks its build against the lock's checksum,
// and speaks the plugin protocol with it (docs/plugin-protocol.md) through
// program.Run, one process for each request. Each plugin is one more
// extractor kind, a check.ExtractorKind whose spec is the fields the
// plugin declares, so extract.Build returns a plugin's extractor as it
// returns a built in reader's.
//
// A plugin errata cannot run, whether the lock pins no build for this
// platform, the build is not installed, its checksum differs, or it did not
// describe or build its entries, still gives a kind: an entry of it loads,
// and its rule is unknown when it runs, with a message that names the
// plugin and why. Installing a plugin into the cache is for errata plugin
// install, which is still to come (ADR 0021); this package reads the cache
// it will write (CacheDir, BuildPath).
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/plugin/wire"
)

// Set is the plugins the lock file of one repository pins, each described
// at most once, when a config first names its kind.
type Set struct {
	root     string
	lock     Lock
	platform string
	plugins  map[string]*Plugin
}

// Open reads the lock file in root. A lock that pins a plugin under the key
// of a built in kind, one of builtin, is refused, since the built in kind
// would read its entries.
func Open(root string, builtin []string) (*Set, error) {
	// A build's path is started with the request's own directory as the
	// working directory, so it must not be relative.
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	l, err := ReadLock(root)
	if err != nil {
		return nil, err
	}
	for _, p := range l.Pins {
		if slices.Contains(builtin, p.Name) {
			return nil, fmt.Errorf("%s pins a plugin named %s, which is the key of a kind errata has built in; a plugin of that name is refused", LockFile, p.Name)
		}
	}
	return &Set{root: root, lock: l, platform: Platform(), plugins: map[string]*Plugin{}}, nil
}

// Has reports whether the lock pins a plugin whose kind's key is key.
func (s *Set) Has(key string) bool {
	if s == nil {
		return false
	}
	_, ok := s.lock.Pin(key)
	return ok
}

// Kind is the extractor kind of the plugin the lock pins under key, which
// Has reports, described by the plugin on the first call.
func (s *Set) Kind(ctx context.Context, key string) *check.ExtractorKind {
	p, ok := s.plugins[key]
	if !ok {
		pin, _ := s.lock.Pin(key)
		p = s.load(ctx, pin)
		s.plugins[key] = p
	}
	return p.kind()
}

// Plugin is one plugin of a Set: its pin, its build, and its description,
// or why errata cannot run it.
type Plugin struct {
	pin      Pin
	exe      exe
	desc     wire.DescribeReply
	unusable error
}

// load finds pin's build and asks it to describe itself.
func (s *Set) load(ctx context.Context, pin Pin) *Plugin {
	p := &Plugin{pin: pin}
	sum, ok := pin.Sums[s.platform]
	if !ok {
		p.unusable = fmt.Errorf("%s pins no build of plugin errata-ext-%s %s for %s; add the release's checksum for it", LockFile, pin.Name, pin.Version, s.platform)
		return p
	}
	owner, base, err := s.base()
	if err != nil {
		p.unusable = fmt.Errorf("plugin errata-ext-%s %s: %w", pin.Name, pin.Version, err)
		return p
	}
	p.exe = newExe("errata-ext-"+pin.Name+" "+pin.Version, owner, BuildPath(base, pin.Name, pin.Version, s.platform, sum), sum)
	p.exe.install = installHint(pin, owner == s.root)
	p.unusable = p.describe(ctx)
	return p
}

// installHint is what a message that pin's build is not installed ends
// with: the command that installs what the lock pins, or for a build the
// lock pins from a file, which nothing can fetch again, that it came from
// one. vendored says errata looked in the vendored directory, where only
// an install that vendors puts a build.
func installHint(pin Pin, vendored bool) string {
	switch {
	case pin.Source == SourceFile && vendored:
		return "; its entry in " + LockFile + " came from a file, which errata cannot fetch, and errata looks in " + VendorDir + " alone while the repository has it: install that file with errata plugin install --file, which vendors it"
	case pin.Source == SourceFile:
		return "; its entry in " + LockFile + " came from a file, which errata cannot fetch: install that file with errata plugin install --file"
	case vendored:
		return "; errata looks in " + VendorDir + " alone while the repository has it: vendor what " + LockFile + " pins with errata plugin install --vendor"
	}
	return "; install what " + LockFile + " pins with errata plugin install"
}

// base is the directory errata looks for builds in, the repository's
// vendored directory when it has one, else the cache, and owner, the
// directory above it errata does not own: the repository's root, or the
// user's cache directory.
func (s *Set) base() (owner, base string, err error) {
	vendored := filepath.Join(s.root, filepath.FromSlash(VendorDir))
	if info, err := os.Stat(vendored); err == nil && info.IsDir() {
		return s.root, vendored, nil
	}
	return cacheBase()
}

// describe asks p to describe itself, and refuses a reply that does not
// choose a version errata offered, names another plugin or release, or
// declares where or a field the protocol does not allow.
func (p *Plugin) describe(ctx context.Context) error {
	out, stderr, err := p.exe.run(ctx, wire.DescribeRequest{Op: wire.OpDescribe, Protocols: wire.Protocols}, limits{describeTimeout, describeStdout})
	if err != nil {
		return err
	}
	v, err := wire.ReadVersion(out)
	if err != nil {
		return p.refuse("describe", err, stderr)
	}
	if err := p.sameRelease(v); err != nil {
		return err
	}
	if !v.Chose() || !slices.Contains(wire.Protocols, v.Protocol) {
		return p.noCommonVersion(v)
	}
	if err := wire.Decode(out, &p.desc); err != nil {
		return p.refuse("describe", err, stderr)
	}
	if err := p.desc.Check(); err != nil {
		return p.refuse("describe", err, stderr)
	}
	return nil
}

// Describe sends the build at path the describe request, under the rules
// a run's requests have (an environment of the Go runtime's variables
// alone, an empty working directory, the 10 s bound), once it has checked
// that the build is a regular file whose sha256 is sum, below owner with
// no symbolic link between. It refuses a reply that names another plugin
// than name, gives another version than version, or speaks no protocol
// errata speaks, as a run does; version "" takes any semantic version. It
// returns the version the build gives. errata plugin install describes a
// build this way before it keeps it (ADR 0021).
func Describe(ctx context.Context, owner, path, name, version, sum string) (string, error) {
	label := "errata-ext-" + name
	if version != "" {
		label += " " + version
	}
	p := &Plugin{pin: Pin{Name: name, Version: version}, exe: newExe(label, owner, path, sum)}
	if err := p.describe(ctx); err != nil {
		return "", err
	}
	return p.desc.Version, nil
}

// refuse is the error of a reply to op that errata does not read, with
// the end of stderr, the plugin's own word on what went wrong.
func (p *Plugin) refuse(op string, err error, stderr []byte) error {
	return fmt.Errorf("plugin %s replied to %s with a reply errata refuses: %w%s", p.exe.label, op, err, stderrTail(stderr))
}

// sameRelease refuses a describe reply whose name is not the plugin's, or
// whose version is not the one the lock pins or holds a character a
// terminal acts on. A plugin with no version pinned yet, which an install
// from a file or a URL describes, may give any semantic version.
func (p *Plugin) sameRelease(v wire.Version) error {
	if v.Name != p.pin.Name {
		return fmt.Errorf("plugin %s names itself %s in its describe reply, not %s", p.exe.label, strconv.Quote(v.Name), p.pin.Name)
	}
	if err := core.VersionError(v.Version); err != nil {
		return p.refuse("describe", err, nil)
	}
	if p.pin.Version == "" {
		return p.anyVersion(v.Version)
	}
	if v.Version != p.pin.Version {
		return fmt.Errorf("plugin %s describes itself as release %s, and %s pins %s", p.exe.label, strconv.Quote(v.Version), LockFile, p.pin.Version)
	}
	return nil
}

// anyVersion refuses a version a describe reply gives that is no semantic
// version, before any path is built from it.
func (p *Plugin) anyVersion(version string) error {
	if err := CheckVersion(version); err != nil {
		return p.refuse("describe", err, nil)
	}
	return nil
}

// noCommonVersion is the error of a plugin that speaks none of the versions
// errata offered, or chose one it was not offered.
func (p *Plugin) noCommonVersion(v wire.Version) error {
	speaks := v.Protocols
	if v.Chose() {
		speaks = []int{v.Protocol}
	}
	return fmt.Errorf("plugin %s speaks protocol %s, and this errata speaks %s: install a release of errata-ext-%s for protocol %s, or upgrade errata",
		p.exe.label, ints(speaks), ints(wire.Protocols), p.pin.Name, ints(wire.Protocols))
}

// ints is ns joined with ", ".
func ints(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ", ")
}

// goType is the Go type a field of typ decodes into in a config, or nil
// for a type the protocol does not have.
func goType(typ string) reflect.Type {
	switch typ {
	case wire.TypeString:
		return reflect.TypeFor[string]()
	case wire.TypeInt:
		return reflect.TypeFor[int64]()
	case wire.TypeBool:
		return reflect.TypeFor[bool]()
	case wire.TypeStrings:
		return reflect.TypeFor[[]string]()
	case wire.TypeMap:
		return reflect.TypeFor[map[string]string]()
	}
	return nil
}

// field is p's declared field name, or nil.
func (p *Plugin) field(name string) *wire.Field {
	if i := p.fieldIndex(name); i >= 0 {
		return &p.desc.Fields[i]
	}
	return nil
}

// fieldIndex is where name is among p's fields, or -1.
func (p *Plugin) fieldIndex(name string) int {
	return slices.IndexFunc(p.desc.Fields, func(f wire.Field) bool { return f.Name == name })
}

// schema is the struct an entry of p's kind is written as, which config
// reads the YAML against: errata's keys and each field, under its name, of
// the Go type of its protocol type. For a plugin errata could not describe
// it is a map of any value, so the entry is read as it is written.
func (p *Plugin) schema() reflect.Type {
	if p.unusable != nil {
		return reflect.TypeFor[map[string]any]()
	}
	fields := []reflect.StructField{
		{Name: "File", Type: reflect.TypeFor[string](), Tag: `json:"file,omitempty"`},
		{Name: "MaxBytes", Type: reflect.TypeFor[int64](), Tag: `json:"max_bytes,omitempty"`},
	}
	if p.desc.Where == wire.WhereFiles {
		fields = append(fields, reflect.StructField{Name: "Files", Type: reflect.TypeFor[[]string](), Tag: `json:"files,omitempty"`})
	}
	for i, f := range p.desc.Fields {
		fields = append(fields, reflect.StructField{Name: "F" + strconv.Itoa(i), Type: goType(f.Type), Tag: reflect.StructTag(`json:"` + f.Name + `,omitempty"`)})
	}
	return reflect.StructOf(fields)
}

// kind is p as an extractor kind.
func (p *Plugin) kind() *check.ExtractorKind {
	return &check.ExtractorKind{
		Key:    p.pin.Name,
		Spec:   p.schema(),
		Decode: p.decode,
		Build:  func(spec any) (check.Extractor, error) { return spec.(*Spec).extractor() },
		Place:  func(spec any) any { return spec.(*Spec).place() },
		Resolve: func(spec any) any {
			return spec.(*Spec).resolved()
		},
		Describe: func(spec any) check.Description { return spec.(*Spec).describe() },
		ForRule: func(spec any, id string) any {
			s := *spec.(*Spec)
			s.rule = id
			return &s
		},
	}
}

// Build sends each plugin's entries among specs, each a *Spec an extractor
// kind of this package decoded with the rule it is in, in one build request
// for each plugin, and keeps each result with its entry. A request that
// fails, or a reply errata refuses, fails every entry of that plugin, and
// their rules are unknown when they run; a spec the plugin refuses is a
// config error, which the entry's Build returns.
func Build(ctx context.Context, specs []any) {
	var order []*Plugin
	byPlugin := map[*Plugin][]*Spec{}
	for _, v := range specs {
		s := v.(*Spec)
		if _, ok := byPlugin[s.plugin]; !ok {
			order = append(order, s.plugin)
		}
		byPlugin[s.plugin] = append(byPlugin[s.plugin], s)
	}
	for _, p := range order {
		p.build(ctx, byPlugin[p])
	}
}

// build sends specs, every entry of p's kind, in one build request.
func (p *Plugin) build(ctx context.Context, specs []*Spec) {
	if p.unusable != nil {
		failAll(specs, p.unusable)
		return
	}
	req := wire.BuildRequest{Protocol: wire.Protocol, Op: wire.OpBuild, Entries: make([]wire.Entry, len(specs))}
	for i, s := range specs {
		req.Entries[i] = wire.Entry{Rule: s.rule, Spec: object(s.fields)}
	}
	out, stderr, err := p.exe.run(ctx, req, limits{buildTimeout, buildStdout})
	if err != nil {
		failAll(specs, err)
		return
	}
	results, err := p.readBuild(out, len(specs))
	if err != nil {
		failAll(specs, p.refuse("build", err, stderr))
		return
	}
	for i, s := range specs {
		*s.res = results[i]
	}
}

// failAll fails every one of specs with err.
func failAll(specs []*Spec, err error) {
	for _, s := range specs {
		*s.res = result{done: true, failed: err}
	}
}

// readBuild reads a build reply for n entries.
func (p *Plugin) readBuild(out []byte, n int) ([]result, error) {
	var reply wire.BuildReply
	if err := wire.Decode(out, &reply); err != nil {
		return nil, err
	}
	if len(reply.Built) != n {
		return nil, fmt.Errorf("it holds %d results for %d entries", len(reply.Built), n)
	}
	results := make([]result, n)
	for i, b := range reply.Built {
		r, err := p.builtResult(b)
		if err != nil {
			return nil, fmt.Errorf("result %d: %w", i, err)
		}
		results[i] = r
	}
	return results, nil
}

// builtResult is one result of a build reply, checked.
func (p *Plugin) builtResult(b wire.Built) (result, error) {
	if b.Error != nil {
		return result{done: true, refused: fmt.Errorf("%s: plugin %s refuses the entry: %s", p.pin.Name, p.exe.label, strconv.Quote(*b.Error))}, nil
	}
	fields, err := p.builtFields(b.Spec)
	if err != nil {
		return result{}, err
	}
	desc, err := description(*b.Description)
	if err != nil {
		return result{}, err
	}
	return result{done: true, fields: fields, desc: desc}, nil
}

// builtFields reads spec, a spec as a build reply writes it out: each
// member a declared field, as a request carries it, in the order the
// plugin declares them.
func (p *Plugin) builtFields(spec json.RawMessage) ([]fieldValue, error) {
	ms, err := members(spec)
	if err != nil {
		return nil, fmt.Errorf("its spec: %w", err)
	}
	var out []fieldValue
	for _, m := range ms {
		f := p.field(m.name)
		if f == nil {
			return nil, fmt.Errorf("its spec holds %s, which the plugin does not declare", strconv.Quote(m.name))
		}
		v, err := requestValue(f.Type, m.value)
		if err != nil {
			return nil, fmt.Errorf("its spec's %s: %w", m.name, err)
		}
		out = append(out, fieldValue{m.name, v})
	}
	slices.SortStableFunc(out, func(a, b fieldValue) int { return p.fieldIndex(a.name) - p.fieldIndex(b.name) })
	return out, nil
}

// description is d as check holds it, refusing a label that holds a
// character a terminal acts on and a shape the protocol does not have.
func description(d wire.Description) (check.Description, error) {
	if check.HoldsControl(d.Label) || d.Label == "" {
		return check.Description{}, fmt.Errorf("the label %s is empty or holds a control character, a direction control or a line or paragraph separator", strconv.Quote(d.Label))
	}
	shapes := map[string]check.Shape{wire.ShapeFlat: check.ShapeFlat, wire.ShapeMap: check.ShapeMap, wire.ShapeRecord: check.ShapeRecord}
	shape, ok := shapes[d.Shape]
	if !ok {
		return check.Description{}, fmt.Errorf("the shape %s is none of flat, map and record", strconv.Quote(d.Shape))
	}
	out := check.Description{Label: d.Label, NotLiteral: d.NotLiteral, Shape: shape, Reads: slices.Clone(d.Reads)}
	if shape == check.ShapeRecord {
		out.Fields = slices.Clone(d.Fields)
	} else if len(d.Fields) > 0 {
		return check.Description{}, errors.New("its fields are set for a shape that is not record")
	}
	return out, nil
}

// place is s with only what decides where it reads: its files as a place
// writes them, no max_bytes, and the spec the build wrote out, so two
// entries the plugin fills to one spec are one place.
func (s *Spec) place() *Spec {
	out := s.resolved()
	out.where = s.where.Place()
	return out
}

// resolved is s with every default written out: max_bytes, and the spec
// the build reply wrote out. An entry the build did not take keeps its
// fields as written.
func (s *Spec) resolved() *Spec {
	out := *s
	out.where = s.where.Resolved()
	if s.res.done && s.res.failed == nil && s.res.refused == nil {
		out.fields = s.res.fields
	}
	out.fields = slices.Clone(out.fields)
	return &out
}

// describe is what s's extractor returns, from the build reply. An entry
// whose plugin errata could not run is described as one that may return
// not literal values and may read any extractor of its rule, so its rule
// loads with skip_not_literal, refuses no extractor only the plugin would
// have read, and is unknown when it runs.
func (s *Spec) describe() check.Description {
	if !s.res.done || s.res.failed != nil || s.res.refused != nil {
		return check.Description{Label: s.plugin.pin.Name, NotLiteral: true, ReadsAny: true, MaxBytes: s.where.MaxBytes}
	}
	d := s.res.desc
	d.Fields, d.Reads = slices.Clone(d.Fields), slices.Clone(d.Reads)
	d.MaxBytes = s.where.MaxBytes
	return d
}
