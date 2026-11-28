package compose

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// A fold is a place of a service's configuration where compose-go makes
// one value of several sources other than by taking the last file's: a
// list, whose items it appends across files without duplicates, and of
// which it keeps one item per index key, such as a volume's target, even
// within one file (EnforceUnicity); and a mapping it merges by a value of
// its own, as logging, whose options an override keeps only when both name
// the same driver. A source that looks up a variable no file sets may then
// match, displace or drop any value of the fold, or move its index, which
// the files alone do not show. So a value below a fold is literal only
// when no file's YAML at the fold looks up such a variable, and a key
// below one that holds nothing is refused (absentFromShell).

// replacedLists are the lists an override replaces whole, as compose-go's
// override merger does, so that the last file's list is the only source of
// their items and no list of another file folds into them.
// TestMergesAreComposeGos derives them from compose-go.
var replacedLists = [][]string{
	{"command"},
	{"entrypoint"},
	{"healthcheck", "test"},
}

// foldedMappings are the mappings compose-go merges by a value of theirs,
// not key by key. TestMergesAreComposeGos derives them from compose-go.
var foldedMappings = [][]string{
	{"logging"},
}

// beside are the keys of a service compose-go builds a key from besides
// the key itself, within one file, by that key. develop is merged with
// x-develop, its name before the specification took it in, where one
// mapping writes both (promoteAliases in loader/aliases.go); the alias of
// a key below a list item, x-initialSync, lies below develop.watch, a fold
// already. depends_on takes a service that links, volumes_from or a
// namespace such as network_mode: service:db names (Normalize in
// loader/normalize.go). A value from the shell in either may give,
// displace or drop any value below the key.
var beside = map[string][]string{
	"develop":    {"x-develop"},
	"depends_on": {"links", "volumes_from", "network_mode", "ipc", "pid", "uts", "cgroup"},
}

// besideFromShell is the key beside key that a contributor of cs writes
// with a variable no file sets; "" when none does.
func besideFromShell(cs []contributor, key string) string {
	for _, c := range cs {
		for _, k := range beside[key] {
			if n := c.raw.Field[k]; n != nil && unset(n, c.p.env) {
				return k
			}
		}
	}
	return ""
}

// isRow reports whether at is one of rows.
func isRow(rows [][]string, at []string) bool {
	return slices.ContainsFunc(rows, func(r []string) bool { return slices.Equal(r, at) })
}

// foldAt is the fold the steps from a service down to a value pass
// through first, and whether they pass through one: the keys down to the
// first list step, unless that list is replaced, or to a folded mapping.
// A fold's place is keys alone, since it ends before the first index.
func foldAt(steps []any) ([]string, bool) {
	var at []string
	for _, s := range steps {
		k, ok := s.(string)
		if !ok {
			return at, !isRow(replacedLists, at)
		}
		at = append(at, k)
		if isRow(foldedMappings, at) {
			return at, true
		}
	}
	return nil, false
}

// foldedFromShell reports whether a contributor of cs writes, at the fold
// at, a value that looks up a variable no file sets.
func foldedFromShell(cs []contributor, at []string) bool {
	for _, c := range cs {
		if n := rawFold(c.raw, at, c.p.env); n != nil && unset(n, c.p.env) {
			return true
		}
	}
	return false
}

// rawFold is the node of raw, a service's YAML, at the keys of at, through
// mappings and the items of a list that set a name; or the scalar on the
// way, which writes a short form of what lies below it. nil where raw
// writes nothing at at.
func rawFold(raw *core.Node, at []string, env map[string]string) *core.Node {
	n := raw
	for _, k := range at {
		var next *core.Node
		switch n.Shape {
		case core.Scalar:
			return n
		case core.Mapping:
			next = n.Field[k]
		case core.Sequence:
			next = itemNamed(n, k, env)
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}

// folded reports whether a source that may fold into the value at steps
// looks up a variable no file sets: a key beside the first step (beside),
// the fold the steps pass through, and, for a value a pre_start hook may
// take from its service, the fold the same steps pass through in the
// service.
func folded(cs []contributor, steps []any) bool {
	if k, _ := steps[0].(string); besideFromShell(cs, k) != "" {
		return true
	}
	if at, ok := foldAt(steps); ok && foldedFromShell(cs, at) {
		return true
	}
	if !inheritable(steps) {
		return false
	}
	at, ok := foldAt(steps[2:])
	return ok && foldedFromShell(cs, at)
}

// absentFromShell refuses key, which reads nothing in t, the service's
// configuration, when a key beside its first part (beside), or a fold on
// the way to it, in the service or, for a key a pre_start hook may take
// from it, in the service at the hook's steps, takes a source that looks
// up a variable no file sets: docker compose may hold a value there that
// errata cannot show.
func absentFromShell(cs []contributor, t any, key []string) error {
	if from := besideFromShell(cs, key[0]); from != "" {
		return fmt.Errorf("compose: %s holds nothing here, but compose-go builds %s from %s too, which looks up a variable no file errata read sets, which docker compose takes from its shell and which may give it a value", core.Quote(strings.Join(key, ".")), key[0], from)
	}
	if err := absentAt(cs, t, key, key); err != nil {
		return err
	}
	if len(key) > 2 && key[0] == "pre_start" && inheritedKey(key[2]) {
		return absentAt(cs, t, key[2:], key)
	}
	return nil
}

// absentAt refuses key when a part of t at a prefix of parts is a fold
// that takes a source from the shell.
func absentAt(cs []contributor, t any, parts, key []string) error {
	for i := 1; i <= len(parts); i++ {
		sub, steps, ok := selectKey(t, parts[:i])
		if !ok {
			return nil
		}
		at, isFold := foldOf(sub, steps)
		if isFold && foldedFromShell(cs, at) {
			return fmt.Errorf("compose: %s holds nothing here, but %s takes a value that looks up a variable no file errata read sets, which docker compose takes from its shell and which may give it one; read %s whole", core.Quote(strings.Join(key, ".")), strings.Join(at, "."), strings.Join(at, "."))
		}
	}
	return nil
}

// foldOf is the fold that sub, the part of a service's configuration at
// steps, is, and whether it is one: a list an override does not replace,
// or a folded mapping, each reached by keys alone.
func foldOf(sub any, steps []any) ([]string, bool) {
	at := make([]string, 0, len(steps))
	for _, s := range steps {
		k, ok := s.(string)
		if !ok {
			return nil, false
		}
		at = append(at, k)
	}
	if _, isList := sub.([]any); isList {
		return at, !isRow(replacedLists, at)
	}
	return at, isRow(foldedMappings, at)
}

// inheritedKeys are the keys of a service a pre_start hook takes from it,
// as compose-go has them (resolvePreStartHooks): those of the container
// specification but volumes.
var inheritedKeys = func() map[string]bool {
	out := map[string]bool{}
	for _, k := range types.ContainerSpecAttributes {
		out[k] = k != "volumes"
	}
	return out
}()

func inheritedKey(k string) bool { return inheritedKeys[k] }

// inheritable reports whether steps lead below a key a pre_start hook
// takes from its service: pre_start, the hook's index, and such a key.
func inheritable(steps []any) bool {
	if len(steps) < 3 || steps[0] != "pre_start" {
		return false
	}
	k, _ := steps[2].(string)
	return inheritedKey(k)
}

// inherited is lf, a value of a pre_start hook below a key it takes from
// its service, as the service's value when the hook's own YAML does not
// write it: at the service's line, and literal only where the service's
// value is and nothing folds into the hook's. ok is false where the hook
// writes the value itself, or lf is below no such key. Where neither the
// hook writes it nor the service holds it, the value stands where the
// hook's YAML stops, and is not literal unless that is a list of the
// hook's: an item of the hook's list is checked there, and one of the
// service's list at the service's fold (folded).
func (l *loader) inherited(cs []contributor, t any, lf leaf) (check.Value, bool, error) {
	if !inheritable(lf.steps) {
		return check.Value{}, false, nil
	}
	writes, inList, err := l.hookWrites(cs, t, lf.steps)
	if err != nil || writes {
		return check.Value{}, err != nil, err
	}
	if steps, ok := inService(t, lf); ok {
		v, err := l.value(cs, t, leaf{steps: steps, value: lf.value})
		v.Key = keyOf(lf.steps)
		v.NotLiteral = v.NotLiteral || folded(cs, lf.steps)
		return v, true, err
	}
	v, err := l.produced(cs, t, lf)
	v.NotLiteral = v.NotLiteral || !inList || folded(cs, lf.steps)
	return v, true, err
}

// hookWrites reports whether the YAML of the hook that holds the value at
// steps writes it, a short form of it included, and, where it does not,
// whether that YAML stops at a list of the hook's below the key the hook
// takes from its service.
func (l *loader) hookWrites(cs []contributor, t any, steps []any) (writes, inList bool, err error) {
	c, at, err := l.winner(cs, t, steps)
	if err != nil || c == nil {
		return false, false, err
	}
	own, _ := l.ownServices(c.f, c.p)
	n, depth := rawWalk(c.raw, own[c.name], at, c.p.env)
	below := depth > 2
	return depth == len(steps) || (below && n.Shape == core.Scalar), below && n.Shape == core.Sequence, nil
}

// inService is the steps in t, the service's configuration, to the value
// lf holds in a pre_start hook below a key the hook takes from the
// service: the same keys, and at a list the index of the service's item
// equal to the hook's. ok is false when the service holds no such value.
func inService(t any, lf leaf) ([]any, bool) {
	hook := child(child(t, "pre_start"), lf.steps[1])
	svc := t
	out := make([]any, 0, len(lf.steps)-2)
	for _, s := range lf.steps[2:] {
		hook = child(hook, s)
		var step any
		var ok bool
		if svc, step, ok = sameStep(svc, hook, s); !ok {
			return nil, false
		}
		out = append(out, step)
	}
	return out, reflect.DeepEqual(svc, lf.value)
}

// sameStep is the part of svc that step, a step of a hook's configuration
// to want, matches, and the step to it: the same key, or the index of the
// item equal to want.
func sameStep(svc, want, step any) (any, any, bool) {
	s, ok := ownStep(svc, want, step)
	if !ok {
		return nil, nil, false
	}
	return child(svc, s), s, true
}

// hostItem is the item of n, an extra_hosts list, that writes want, a host
// and an address as compose-go's JSON writes them (host=address): the
// item whose text, once env fills in its variables, compose-go reads as
// that host with that address among its own. compose-go sorts the list, so
// an item's index is not its place in the file.
func hostItem(n *core.Node, want any, env map[string]string) *core.Node {
	w, _ := want.(string)
	for _, it := range n.Items {
		if it.Shape != core.Scalar {
			continue
		}
		text, err := interpolate(it.Text, env)
		if err != nil {
			continue
		}
		hosts, err := types.NewHostsList([]string{text})
		if err == nil && slices.Contains(hosts.AsList("="), w) {
			return it
		}
	}
	return nil
}
