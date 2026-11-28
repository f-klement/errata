package compose

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	cg "github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// values are the values of key in the service name of p, under the
// profiles given, or those COMPOSE_PROFILES sets in the .env file when
// none is. Under environment, a variable the service's environment: does
// not set takes its value from its env_file:, as docker compose config
// writes it.
func (l *loader) values(p *project, name string, key, profiles []string) ([]check.Value, error) {
	proj, err := l.load(p, activeProfiles(p, profiles), false)
	if err != nil {
		return nil, err
	}
	svc, err := pick(proj, name)
	if err != nil {
		return nil, err
	}
	t, err := serviceTree(svc)
	if err != nil {
		return nil, err
	}
	if err := l.unread(name, svc, key); err != nil {
		return nil, err
	}
	if err := l.addBare(p, name, activeProfiles(p, profiles), t); err != nil {
		return nil, err
	}
	leaves, err := l.leaves(p, name, t, key)
	if err != nil {
		return nil, err
	}
	out, err := l.located(p, name, t, leaves)
	if err != nil || key[0] != "environment" {
		return out, err
	}
	fromFiles, err := l.envFileValues(p, svc, key)
	return append(out, fromFiles...), err
}

// addBare adds to t, the service name of p as compose-go builds it under
// the profiles given, each build arg a contributor names without a value
// that compose-go dropped. compose-go drops such an arg when the .env files
// do not set it, where docker compose would take it from the shell, so it
// is no value errata may report as absent. Whether the merge kept the arg
// is compose-go's to say: an override's !reset takes it away, which no
// file's own YAML shows, so p is loaded again without the step that drops
// args, and an arg is added only where that load still holds it unset.
func (l *loader) addBare(p *project, name string, profiles []string, t any) error {
	var names []string
	for _, c := range l.contributors(p, name, 0) {
		names = append(names, argNames(c.raw, c.p)...)
	}
	if len(names) == 0 {
		return nil
	}
	proj, err := l.load(p, profiles, true, func(o *cg.Options) { o.SkipNormalization = true })
	if err != nil {
		return err
	}
	svc, err := pick(proj, name)
	if err != nil || svc.Build == nil {
		return err
	}
	var kept []string
	for _, n := range names {
		if _, ok := svc.Build.Args[n]; ok {
			kept = append(kept, n)
		}
	}
	bareArgs(t, kept)
	return nil
}

// bareArgs adds to t, a service's configuration, each build arg of names
// that t does not hold, as a value compose leaves unset.
func bareArgs(t any, names []string) {
	m, _ := t.(map[string]any)
	build, _ := m["build"].(map[string]any)
	if build == nil || len(names) == 0 {
		return
	}
	args, _ := build["args"].(map[string]any)
	if args == nil {
		args = map[string]any{}
		build["args"] = args
	}
	for _, name := range names {
		if _, ok := args[name]; !ok {
			args[name] = nil
		}
	}
}

// unread refuses key when a value below it is one errata cannot read: a
// name the shell picks in a keyed list (keysFromShell), a list a file of
// svc's adds to (unreadFiles), or any value of a service whose profiles
// the shell picks (profilesFromShell).
func (l *loader) unread(name string, svc types.ServiceConfig, key []string) error {
	if err := unreadFiles(svc, key); err != nil {
		return err
	}
	if err := l.profilesFromShell(name); err != nil {
		return err
	}
	return l.keysFromShell(name, key)
}

// sources are the service mappings named name in every YAML file the
// project reads, each with the services it extends: every mapping
// compose-go may merge into the service, through an override, an extends
// or an include, and more, since a file's service of that name that no
// load reaches is among them. A refusal checks these, not contributors,
// which follow compose-go's merge order only as far as errata knows it.
func (l *loader) sources(name string) []contributor {
	var out []contributor
	for _, u := range l.w.uses {
		if svc := service(u.f, name); svc != nil {
			out = append(out, l.chain(contributor{u.f, u.p, name, svc, u.dir}, 0)...)
		}
	}
	return out
}

// profilesFromShell refuses the service name when a file writes one of its
// profiles with a variable no file sets: docker compose would take it from
// its shell, and with it whether the service is active at all.
func (l *loader) profilesFromShell(name string) error {
	for _, c := range l.sources(name) {
		profiles := c.raw.Field["profiles"]
		if profiles == nil {
			continue
		}
		for _, it := range profiles.Items {
			if err := shellRefused(c.f.path, it, "profile", c.p.env); err != nil {
				return err
			}
		}
	}
	return nil
}

// unreadFiles refuses key when it reads the labels of svc or of one of its
// pre_start hooks and that names a label_file:, or the environment of a
// hook that names an env_file:. compose-go would read those files itself,
// which errata keeps it from (options), and a label or variable such a
// file sets would read as absent. errata reads the service's own env_file:
// (envFileValues), not a hook's, and a label file of neither.
func unreadFiles(svc types.ServiceConfig, key []string) error {
	if err := fileFed(svc.LabelFiles, key, []string{"labels"}, "label_file"); err != nil {
		return err
	}
	for i, h := range svc.PreStart {
		at := []string{"pre_start", strconv.Itoa(i)}
		if err := fileFed(h.LabelFiles, key, append(slices.Clip(at), "labels"), "label_file"); err != nil {
			return err
		}
		if err := fileFed(envPaths(h.EnvFiles), key, append(slices.Clip(at), "environment"), "env_file"); err != nil {
			return err
		}
	}
	return nil
}

// fileFed refuses key when it covers field and files, which the key
// named kind writes, names one.
func fileFed(files, key, field []string, kind string) error {
	if len(files) == 0 || !covers(key, field) {
		return nil
	}
	return fmt.Errorf("compose: %s may be set by the %s %s, which errata does not read; read another key", strings.Join(field, "."), kind, core.Quote(files[0]))
}

// envPaths are the paths of env files.
func envPaths(files []types.EnvFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

// keyedList is a list of a service's configuration whose items' names
// decide its keys: compose-go turns it into a mapping, an item NAME=value
// or NAME alone, or it sorts the list by its items' text, as it does
// extra_hosts. compose-go interpolates an item before it splits off the
// name, and does not interpolate a key of the mapping form. field is the
// list's place, a step [] standing for each item of a list, and ends the
// characters that end an item's name, none where the whole item decides.
// An extra host's name is its host: compose-go sorts host=address, a host
// holds neither = nor :, so two hosts are ordered before their addresses
// are compared, and an address from the shell moves only the items of its
// own host, which the fold of the list marks not literal (folded).
type keyedList struct {
	field []string
	ends  string
}

// keyedLists are the keyed lists of compose-go v2.16.2, each list its
// transforms or its types' decoders turn into a mapping, or sort.
// TestKeyedListsAreComposeGos derives them from compose-go itself and
// fails on a list this table lacks. A pre_start hook inherits the
// service's own lists (namedFromShell).
var keyedLists = []keyedList{
	{[]string{"annotations"}, "="},
	{[]string{"environment"}, "="},
	{[]string{"extra_hosts"}, "=:"},
	{[]string{"labels"}, "="},
	{[]string{"sysctls"}, "="},
	{[]string{"networks"}, ""},
	{[]string{"models"}, ""},
	{[]string{"depends_on"}, ""},
	{[]string{"gpus", "[]", "options"}, "="},
	{[]string{"volumes", "[]", "volume", "labels"}, "="},
	{[]string{"build", "args"}, "="},
	{[]string{"build", "labels"}, "="},
	{[]string{"build", "additional_contexts"}, "="},
	{[]string{"build", "extra_hosts"}, "=:"},
	{[]string{"build", "ssh"}, "="},
	{[]string{"deploy", "labels"}, "="},
	{[]string{"deploy", "resources", "limits", "devices", "[]", "options"}, "="},
	{[]string{"deploy", "resources", "reservations", "devices", "[]", "options"}, "="},
	{[]string{"develop", "watch", "[]", "exec", "environment"}, "="},
	{[]string{"post_start", "[]", "environment"}, "="},
	{[]string{"pre_stop", "[]", "environment"}, "="},
	{[]string{"pre_start", "[]", "annotations"}, "="},
	{[]string{"pre_start", "[]", "environment"}, "="},
	{[]string{"pre_start", "[]", "extra_hosts"}, "=:"},
	{[]string{"pre_start", "[]", "labels"}, "="},
	{[]string{"pre_start", "[]", "sysctls"}, "="},
	{[]string{"pre_start", "[]", "networks"}, ""},
	{[]string{"pre_start", "[]", "models"}, ""},
	{[]string{"pre_start", "[]", "gpus", "[]", "options"}, "="},
	{[]string{"pre_start", "[]", "volumes", "[]", "volume", "labels"}, "="},
}

// keysFromShell refuses, when key reads a keyed list or holds one, an item
// of that list a source of the service name writes (sources) whose name
// the shell would choose (namedFromShell). docker compose would set the
// key the shell names, so errata can report no key of the list present or
// absent.
func (l *loader) keysFromShell(name string, key []string) error {
	for _, kl := range keyedLists {
		if !covers(key, kl.field) {
			continue
		}
		for _, c := range l.sources(name) {
			if err := namedFromShell(c, kl); err != nil {
				return err
			}
		}
	}
	return nil
}

// covers reports whether key reads the place field names or a place below
// it or above it: each step the two share is the same, a [] step matching
// any.
func covers(key, field []string) bool {
	for i := range min(len(key), len(field)) {
		if field[i] != "[]" && key[i] != field[i] {
			return false
		}
	}
	return true
}

// namedFromShell refuses an item of c's list kl whose name, the text
// before the first of kl.ends, holds a $, when the item's whole text looks
// up a variable no file sets. The whole text is checked, not the name
// alone: in ${X:-A=1} the first = stands inside the braces, so the name
// cannot be cut from the item before it is interpolated. An item whose
// name the files set and whose value the shell sets, such as ${A}=${X},
// is refused too, which is safe. A list of a pre_start hook is checked
// with the service's own list of that name, which compose-go copies into
// each hook (resolvePreStartHooks).
func namedFromShell(c contributor, kl keyedList) error {
	lists := listsAt(c.raw, kl.field)
	if kl.field[0] == "pre_start" {
		lists = append(lists, listsAt(c.raw, kl.field[2:])...)
	}
	what := strings.ReplaceAll(strings.Join(kl.field, "."), ".[]", "") + " item"
	for _, list := range lists {
		if err := itemsFromShell(c, list, what, kl.ends); err != nil {
			return err
		}
	}
	return nil
}

// itemsFromShell refuses an item of list, as namedFromShell says.
func itemsFromShell(c contributor, list *core.Node, what, ends string) error {
	for _, it := range list.Items {
		if it.Shape != core.Scalar || !strings.Contains(nameOf(it.Text, ends), "$") {
			continue
		}
		if err := shellRefused(c.f.path, it, what, c.p.env); err != nil {
			return err
		}
	}
	return nil
}

// listsAt are the nodes of n at field, each item of a list at a [] step.
func listsAt(n *core.Node, field []string) []*core.Node {
	switch {
	case n == nil:
		return nil
	case len(field) == 0:
		return []*core.Node{n}
	case field[0] != "[]":
		return listsAt(n.Field[field[0]], field[1:])
	}
	var out []*core.Node
	for _, it := range n.Items {
		out = append(out, listsAt(it, field[1:])...)
	}
	return out
}

// nameOf is the text before the first of ends in item, all of it when it
// holds none.
func nameOf(item, ends string) string {
	if i := strings.IndexAny(item, ends); i >= 0 {
		return item[:i]
	}
	return item
}

// argNames are the build args raw, a service of p, writes without a value:
// an item NAME of a list, as compose-go interpolates it (bareArg), or a
// key NAME with no value in a mapping, as written, since compose-go does
// not interpolate a key. addBare keeps only a name compose-go's merge
// holds.
func argNames(raw *core.Node, p *project) []string {
	args := raw.Member("build", "args")
	if args == nil {
		return nil
	}
	var out []string
	for _, it := range args.Items {
		if name, ok := bareArg(it, p); ok {
			out = append(out, name)
		}
	}
	for i, v := range args.Vals {
		if v.Shape == core.Scalar && v.Type == core.TypeNull {
			out = append(out, args.Keys[i])
		}
	}
	return out
}

// bareArg is the name of the arg it, an item of a build.args list of a
// file p loads, sets without a value: the item interpolated with the
// variables compose-go gives p, when that is not empty and holds no =. An
// empty name is no arg docker compose passes. So - ${A} with A=FOO
// in .env names FOO, which compose-go drops and docker compose takes from
// its shell. An item that looks up a variable no file sets names the arg
// its default gives, as compose-go names it, but a rule that reads the
// args never sees it: keysFromShell refuses the item first.
func bareArg(it *core.Node, p *project) (string, bool) {
	if it.Shape != core.Scalar {
		return "", false
	}
	name, _ := interpolate(it.Text, p.goEnv())
	return name, name != "" && !strings.Contains(name, "=")
}

// activeProfiles are the profiles given, or those COMPOSE_PROFILES sets in
// p's .env file when none is.
func activeProfiles(p *project, profiles []string) []string {
	if len(profiles) == 0 && p.env["COMPOSE_PROFILES"] != "" {
		return strings.Split(p.env["COMPOSE_PROFILES"], ",")
	}
	return profiles
}

// leaves are the values of t, the service name of p, below key; none
// when t holds no such key, refused where a source from the shell may hold
// one there (absentFromShell).
func (l *loader) leaves(p *project, name string, t any, key []string) ([]leaf, error) {
	leaves, err := leavesAt(t, key)
	if err != nil || len(leaves) > 0 {
		return leaves, err
	}
	return nil, absentFromShell(l.contributors(p, name, 0), t, key)
}

// leavesAt are the values of t, a service's configuration, below key;
// none when t holds no such key.
func leavesAt(t any, key []string) ([]leaf, error) {
	var leaves []leaf
	if sub, steps, ok := selectKey(t, key); ok {
		if err := collect(sub, steps, &leaves); err != nil {
			return nil, err
		}
	}
	return leaves, nil
}

// located is each leaf of the service name as a value, at its line.
func (l *loader) located(p *project, name string, t any, leaves []leaf) ([]check.Value, error) {
	cs := l.contributors(p, name, 0)
	if len(cs) == 0 && len(leaves) > 0 {
		return nil, fmt.Errorf("compose: no file errata read sets the service %s itself", core.Quote(name))
	}
	out := []check.Value{}
	for _, lf := range leaves {
		v, err := l.value(cs, t, lf)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// envFileValues are the variables the env files of svc set that its
// environment: does not, which key names: all of them for environment,
// or the one environment.NAME names.
func (l *loader) envFileValues(p *project, svc types.ServiceConfig, key []string) ([]check.Value, error) {
	vars, err := l.envFiles(p, svc)
	if err != nil {
		return nil, err
	}
	want := strings.Join(key[1:], ".")
	out := []check.Value{}
	for _, v := range vars {
		name := strings.TrimPrefix(v.Key, "environment.")
		if _, set := svc.Environment[name]; set || (want != "" && name != want) {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// every is the value of the variable name in each service of p, active or
// not, by the services' names: its environment:'s, or else its last env
// file's, each with the service as its origin.
func (l *loader) every(p *project, name string) ([]check.Value, error) {
	proj, err := l.load(p, nil, true)
	if err != nil {
		return nil, err
	}
	all := proj.AllServices()
	out := []check.Value{}
	for _, s := range slices.Sorted(maps.Keys(all)) {
		vs, err := l.variable(p, s, all[s], name)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			v.Key, v.HasKey = "", false
			v.Origin = "service " + core.Quote(s)
			out = append(out, v)
		}
	}
	return out, nil
}

// variable is the value the service svc of p gives the variable name, none
// when it sets none, refused when the shell would choose the name of a
// variable its environment: sets (keysFromShell).
func (l *loader) variable(p *project, name string, svc types.ServiceConfig, variable string) ([]check.Value, error) {
	if err := l.keysFromShell(name, []string{"environment"}); err != nil {
		return nil, err
	}
	if _, set := svc.Environment[variable]; !set {
		return l.envFileValues(p, svc, []string{"environment", variable})
	}
	t, err := serviceTree(svc)
	if err != nil {
		return nil, err
	}
	steps := []any{"environment", variable}
	return l.located(p, name, t, []leaf{{steps: steps, value: child(child(t, "environment"), variable)}})
}

// envFiles are the variables the env files of svc set, each once, at the
// line of the last file that sets it, in the order the files first set
// them. A file that is not there and not required sets none.
func (l *loader) envFiles(p *project, svc types.ServiceConfig) ([]check.Value, error) {
	if err := l.envFilesFromShell(svc.Name); err != nil {
		return nil, err
	}
	var out []check.Value
	at := map[string]int{}
	for _, ef := range svc.EnvFiles {
		vs, err := l.envFile(p, svc, ef)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			if i, ok := at[v.Key]; ok {
				out[i] = v
				continue
			}
			at[v.Key] = len(out)
			out = append(out, v)
		}
	}
	return out, nil
}

// envFilesFromShell refuses an env_file: path a source of the service name
// writes (sources) that looks up a variable no file sets: docker compose
// would take it from its shell, and with it the file it reads.
func (l *loader) envFilesFromShell(name string) error {
	for _, c := range l.sources(name) {
		for _, n := range envFileRefs(c.raw.Field["env_file"]) {
			if err := shellRefused(c.f.path, n, "env_file path", c.p.env); err != nil {
				return err
			}
		}
	}
	return nil
}

// envFileRefs are the paths an env_file: n names: a path, or a list of
// paths and of mappings with a path.
func envFileRefs(n *core.Node) []*core.Node {
	switch {
	case n == nil:
		return nil
	case n.Shape == core.Scalar:
		return []*core.Node{n}
	}
	var out []*core.Node
	for _, it := range n.Items {
		if it.Shape != core.Scalar {
			it = scalarField(it, "path")
		}
		if it != nil {
			out = append(out, it)
		}
	}
	return out
}

// envFile is the variables one env file of svc sets, each with the line
// that sets it, its ${VAR} looked up in the .env files' variables and then
// in the service's environment, as compose-go looks them up. A variable
// whose line names one neither sets is not literal.
func (l *loader) envFile(p *project, svc types.ServiceConfig, ef types.EnvFile) ([]check.Value, error) {
	rel, err := l.envFilePath(p, ef.Path)
	if err != nil {
		return nil, err
	}
	data, err := l.w.readFile(rel)
	if errors.Is(err, fs.ErrNotExist) && !bool(ef.Required) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lookup := func(k string) (string, bool) {
		if v, ok := p.env[k]; ok {
			return v, true
		}
		if v := svc.Environment[k]; v != nil {
			return *v, true
		}
		return "", false
	}
	vars := map[string]string{}
	if err := dotenv.ParseWithFormat(bytes.NewReader(data), rel, vars, lookup, ef.Format); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, core.ClipError(err))
	}
	return envFileLines(rel, data, vars, p.env), nil
}

// envFilePath is the env file path names as a path from the root: a path
// compose-go made absolute, or one relative to the project's directory.
func (l *loader) envFilePath(p *project, name string) (string, error) {
	rel := l.relative(name)
	if !filepath.IsAbs(name) {
		rel = path.Join(p.dir, filepath.ToSlash(name))
	}
	if path.IsAbs(rel) || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("compose: the env file %s lies outside the repository", core.Quote(name))
	}
	return rel, nil
}

// envFileLines is each variable of vars, which the env file rel sets, at
// the last line of data that sets it, by the order of those lines. Its text
// is what the file wrote when the line names a variable env and vars do not
// set, and the value is then not literal.
func envFileLines(rel string, data []byte, vars, env map[string]string) []check.Value {
	last := map[string]int{}
	raw := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		k, v, ok := assignment(line)
		if _, set := vars[k]; ok && set {
			last[k], raw[k] = i+1, v
		}
	}
	keys := slices.SortedFunc(maps.Keys(last), func(a, b string) int { return last[a] - last[b] })
	out := make([]check.Value, 0, len(keys))
	for _, k := range keys {
		v := check.Value{Text: vars[k], At: check.Location{File: rel, Line: last[k]}, Key: "environment." + k, HasKey: true}
		if names(raw[k], merged(env, vars)) {
			v.Text, v.NotLiteral = raw[k], true
		}
		out = append(out, v)
	}
	return out
}

// assignment is the name and the value as written of a line of an env
// file that sets a variable, NAME=value or NAME: value, after an export.
func assignment(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "export ")
	i := strings.IndexAny(line, "=:")
	if i <= 0 || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}
