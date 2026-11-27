package k8senv

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/workload"
)

// fromData reads the objects of a manifest (workload.Objects): every
// document of a YAML file, or the one object of a JSON file, and the items
// of each List among them. A container: that no workload of the file holds
// is an error, since the rule names a container the file does not have.
func (e extractor) fromData(data []byte) ([]check.Value, error) {
	docs, retyped, err := e.parse(data)
	if err != nil {
		return nil, err
	}
	r := reading{e: e, retyped: retyped, out: []check.Value{}}
	for _, obj := range workload.Objects(docs) {
		if err := r.object(obj); err != nil {
			return nil, err
		}
	}
	if e.container != "" && !r.held {
		return nil, fmt.Errorf("%s: no workload holds a container %s", e.File, core.Quote(e.container))
	}
	if len(r.out) == 0 {
		return nil, &check.NoValue{Reason: fmt.Sprintf("%s: no container of its workloads sets %s in env: or envFrom:; the image or the kubelet may set it, which errata cannot read", e.File, core.Quote(e.variable))}
	}
	return r.out, nil
}

// parse is the objects of the manifest, after the node limit, and the
// plain scalars of a YAML file that YAML 1.1 reads as a boolean.
func (e extractor) parse(data []byte) ([]*core.Node, map[*core.Node]bool, error) {
	if workload.JSON(e.File) {
		n, err := e.ParseManifest(data, "JSON", core.JSONFormat, e.json, false)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", e.File, err)
		}
		return []*core.Node{n}, nil, nil
	}
	if err := e.CheckNodes(data, core.YAMLFormat); err != nil {
		return nil, nil, err
	}
	retyped := map[*core.Node]bool{}
	docs, _, err := e.yaml(data, 0, func(n *core.Node, _ string, plain bool) {
		if plain && yaml11Bools[n.Text] {
			retyped[n] = true
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", e.File, err)
	}
	return docs, retyped, nil
}

// yaml11Bools are the plain scalars that YAML 1.2, and so errata's tree,
// reads as strings and YAML 1.1 as booleans. kubectl and the API server read
// a manifest with sigs.k8s.io/yaml, whose YAML 1.1 parser makes value: yes
// the boolean true, which an env value, a string, refuses.
var yaml11Bools = map[string]bool{
	"y": true, "Y": true, "yes": true, "Yes": true, "YES": true, "on": true, "On": true, "ON": true,
	"n": true, "N": true, "no": true, "No": true, "NO": true, "off": true, "Off": true, "OFF": true,
}

// reading is one read of a manifest: the values found so far, and whether
// a workload holds the container the extractor names.
type reading struct {
	e       extractor
	retyped map[*core.Node]bool
	out     []check.Value
	held    bool
}

// object reads the containers of obj, a workload.
func (r *reading) object(obj *core.Node) error {
	for _, c := range workload.Of(obj) {
		if r.e.container != "" && c.Name != r.e.container {
			continue
		}
		r.held = true
		if err := r.containerValues(obj, c); err != nil {
			return err
		}
	}
	return nil
}

// containerValues adds the value c sees of the variable. The kubelet sets
// what envFrom gives first and env: over it, each later entry over the ones
// before, so the last env: entry of the name is the value; without one, each
// envFrom source whose prefix starts the name may give it.
func (r *reading) containerValues(obj *core.Node, c workload.Container) error {
	origin := originOf(obj, c)
	v, found, err := r.lastValue(c.Node)
	if err != nil {
		return err
	}
	if found {
		return r.add(v, origin)
	}
	for _, src := range sources(c.Node.Member("envFrom"), r.e.variable) {
		if err := r.add(check.Value{Text: sourceText(src), At: check.Location{Line: src.Line}, NotLiteral: true}, origin); err != nil {
			return err
		}
	}
	return nil
}

// lastValue is the value the last item of the container's env: whose name
// is the variable gives, found false for none. Every item of that name is
// read, since the API server refuses the object for any one it refuses;
// the items of other names are not.
func (r *reading) lastValue(container *core.Node) (last check.Value, found bool, err error) {
	env := container.Member("env")
	if env == nil || env.Shape != core.Sequence {
		return check.Value{}, false, nil
	}
	for _, item := range env.Items {
		if name := item.Member("name"); name != nil && name.Shape == core.Scalar && name.Text == r.e.variable {
			if last, err = r.entryValue(item); err != nil {
				return check.Value{}, false, err
			}
			found = true
		}
	}
	return last, found, nil
}

// entryValue is the value an env: entry gives: valueFrom's source, not
// literal; value's text, not literal when the kubelet expands it; or "" for
// an entry with neither. Kubernetes refuses the object when the name or the
// value is no string, or when the entry writes a value that is not empty
// beside a valueFrom, and so does entryValue.
func (r *reading) entryValue(entry *core.Node) (check.Value, error) {
	if _, err := r.asString(entry.Member("name"), "name"); err != nil {
		return check.Value{}, err
	}
	text, line := "", entry.Line
	if value := entry.Member("value"); value != nil {
		t, err := r.asString(value, "value")
		if err != nil {
			return check.Value{}, err
		}
		text, line = t, value.Line
	}
	if from := entry.Member("valueFrom"); !isNull(from) {
		if text != "" {
			return check.Value{}, fmt.Errorf("%s: line %d: the env: entry of %s writes both value and valueFrom, and Kubernetes refuses the object: keep one of them", r.e.File, entry.Line, core.Quote(r.e.variable))
		}
		return check.Value{Text: refText(from), At: check.Location{Line: from.LineOfKey()}, NotLiteral: true}, nil
	}
	return check.Value{Text: text, At: check.Location{Line: line}, NotLiteral: workload.Expands(text)}, nil
}

// isNull reports whether n is absent or a null, which Kubernetes reads as
// a field left out.
func isNull(n *core.Node) bool {
	return n == nil || (n.Shape == core.Scalar && n.Type == core.TypeNull)
}

// asString is the string Kubernetes reads the scalar n as, "" for a null.
// A mapping, a sequence, a number, a boolean, and a plain word YAML 1.1
// reads as a boolean are refused, naming field, since the API server
// refuses the object.
func (r *reading) asString(n *core.Node, field string) (string, error) {
	what := ""
	switch {
	case n.Shape == core.Mapping:
		what = "a mapping"
	case n.Shape == core.Sequence:
		what = "a sequence"
	case n.Type == core.TypeNull:
		return "", nil
	case n.Type == core.TypeNumber:
		what = "a number"
	case n.Type == core.TypeBool || r.retyped[n]:
		what = "a boolean"
	default:
		return n.Text, nil
	}
	return "", fmt.Errorf("%s: line %d: the env: %s of %s is %s to Kubernetes, which refuses the object: an env: %s is a string, so quote it", r.e.File, n.Line, field, core.Quote(r.e.variable), what, field)
}

// refFields are the fields of a valueFrom source that name what it reads,
// in the order its text gives them.
var refFields = []string{"name", "key", "fieldPath", "resource"}

// refText is the text of a valueFrom: its source's key, such as
// secretKeyRef, and what the source names, such as db/password.
func refText(from *core.Node) string {
	if from.Shape != core.Mapping || len(from.Keys) == 0 {
		return "valueFrom"
	}
	ref := from.Field[from.Keys[0]]
	var names []string
	for _, f := range refFields {
		if n := ref.Member(f); n != nil && n.Shape == core.Scalar {
			names = append(names, n.Text)
		}
	}
	return strings.TrimSpace(from.Keys[0] + " " + strings.Join(names, "/"))
}

// sources are the items of envFrom that may set variable: each whose prefix
// starts the name and leaves a key for the ConfigMap or Secret to give.
func sources(envFrom *core.Node, variable string) []*core.Node {
	if envFrom == nil || envFrom.Shape != core.Sequence {
		return nil
	}
	var out []*core.Node
	for _, item := range envFrom.Items {
		if item.Shape != core.Mapping {
			continue
		}
		prefix := ""
		if p := item.Member("prefix"); p != nil && p.Shape == core.Scalar {
			prefix = p.Text
		}
		if len(variable) > len(prefix) && strings.HasPrefix(variable, prefix) {
			out = append(out, item)
		}
	}
	return out
}

// sourceText is the text of an envFrom source: its key, such as
// configMapRef, and the name it gives, such as configMapRef api-config.
func sourceText(src *core.Node) string {
	for _, k := range src.Keys {
		if k != "prefix" {
			name := src.Field[k].Member("name")
			if name != nil && name.Shape == core.Scalar {
				return k + " " + name.Text
			}
			return k
		}
	}
	return "envFrom"
}

// groupNames are how an origin names a container of each group.
var groupNames = [...]string{workload.Regular: "container", workload.Init: "init container", workload.Ephemeral: "ephemeral container"}

// identifier is a kind an origin writes bare: letters and digits, a
// letter first, and 63 bytes at most, the longest kind a CustomResource
// Definition takes. Every kind Kubernetes defines is one.
var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,62}$`)

// originOf names the object and the container a value holds for:
// Deployment "prod/api" container "web". A kind that is no identifier is
// quoted and clipped, as the name is, since an origin is written as it is
// and the file's text could otherwise put a line break or an escape
// sequence into the report.
func originOf(obj *core.Node, c workload.Container) string {
	kind := scalar(obj.Member("kind"))
	switch {
	case kind == "":
		kind = "object"
	case !identifier.MatchString(kind):
		kind = core.Quote(kind)
	}
	name := scalar(obj.Member("metadata", "name"))
	if ns := scalar(obj.Member("metadata", "namespace")); ns != "" && name != "" {
		name = ns + "/" + name
	}
	if name != "" {
		kind += " " + core.Quote(name)
	}
	return kind + " " + groupNames[c.Group] + " " + core.Quote(c.Name)
}

// scalar is n's text when it is a scalar, else "".
func scalar(n *core.Node) string {
	if n == nil || n.Shape != core.Scalar {
		return ""
	}
	return n.Text
}

// add keeps v, at its line of the file, with origin, within the limit on
// values.
func (r *reading) add(v check.Value, origin string) error {
	limit := r.e.valueLimit
	if limit == 0 {
		limit = core.MaxValues
	}
	if len(r.out) == limit {
		return fmt.Errorf("%s: the file holds more than %d values of %s, the limit", r.e.File, core.MaxValues, core.Quote(r.e.variable))
	}
	v.At.File, v.Origin = r.e.File, origin
	r.out = append(r.out, v)
	return nil
}
