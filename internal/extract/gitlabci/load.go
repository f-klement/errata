package gitlabci

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// maxIncludes is how many files one configuration may include, nested
// includes counted: "The maximum number of nested included files for a
// pipeline is 150" (Use CI/CD configuration from other files). That is
// GitLab's default; an instance's ci_max_includes setting can raise it,
// and errata reads to the default. A file that includes itself reaches it
// too.
const maxIncludes = 150

// nodeInfo is what the tree leaves out of a node that the extractor needs:
// the file it came from, the tag the file wrote on it, and whether it is a
// plain scalar with no tag.
type nodeInfo struct {
	file  string
	tag   string
	plain bool
}

// isReference reports whether n is a !reference tag.
func (l *loader) isReference(n *core.Node) bool { return l.info[n].tag == "!reference" }

// loader reads a GitLab CI file and the local files it includes, and merges
// them as GitLab does.
type loader struct {
	read        func(core.Source) ([]byte, error)
	parse       Parser
	limit       int64 // max_bytes, which the files read hold at most in all
	left        int64 // the bytes the files read may still hold
	nodes       int64 // the nodes the files read so far can hold
	included    int
	info        map[*core.Node]nodeInfo
	unreachable []string // the includes errata cannot read, as a message names them
	work        int      // the nodes merging, extends and !reference have touched
	expanded    int      // the nodes YAML aliases and merge keys added, in every file read
}

// target is a mapping a merge builds, and the mappings below it that the
// merge made, which it may change. Any other node belongs to a file's tree,
// or to another merge's result, which an alias, a job's parents or a
// !reference may share, so a merge copies it before changing it.
//
// Each mapping the target owns has an index of its keys, so a merge that
// sets a key the mapping has replaces it in constant time: core.Node.Set
// searches the keys, and a merge sets keys twice by design, an include's
// and then the main file's, a parent's and then the job's.
type target struct {
	root  *core.Node
	owned map[*core.Node]map[string]int
}

func newTarget(line int) *target {
	n := core.NewMapping(line)
	return &target{root: n, owned: map[*core.Node]map[string]int{n: {}}}
}

// set sets key k of dst, a mapping t owns, to v.
func (t *target) set(dst *core.Node, k string, v *core.Node) {
	idx := t.owned[dst]
	if i, ok := idx[k]; ok {
		dst.Vals[i] = v
		dst.Field[k] = v
		return
	}
	idx[k] = len(dst.Keys)
	dst.Keys = append(dst.Keys, k)
	dst.Vals = append(dst.Vals, v)
	dst.Field[k] = v
}

// load reads file and returns its configuration with its includes merged
// in: "If an included file also uses include, that nested include
// configuration is merged first (recursively)", each included file in its
// order, then the file's own keys over them all. included is whether
// another file includes it.
func (l *loader) load(file string, included bool) (*core.Node, error) {
	src := core.Source{File: file, Limit: l.left}
	data, err := l.read(src)
	if err != nil && l.left < l.limit && errors.Is(err, core.ErrTooLarge) {
		// Only a file over the bytes left is refused for them, and the
		// limit the message gives is what the files before left of
		// max_bytes, so the message says so.
		return nil, fmt.Errorf("%w; the file and the files it includes hold at most max_bytes, %d, in all", err, l.limit)
	}
	if err != nil {
		return nil, err
	}
	l.left -= int64(len(data))
	// The files hold at most max_bytes in all, and so at most the nodes
	// max_bytes allows in all, as one file of that size would.
	f := core.YAMLFormat
	if l.nodes += int64(f.CountNodes(data)); l.nodes > l.limit/f.BytesPerNode {
		return nil, fmt.Errorf("%s: the files read so far can hold up to %d nodes, over the limit of %d, one for every %d bytes of max_bytes; set max_bytes: higher to read files this dense",
			file, l.nodes, l.limit/f.BytesPerNode, f.BytesPerNode)
	}
	var tagErr error
	docs, expanded, err := l.parse(data, l.expanded, func(n *core.Node, tag string, plain bool) {
		l.info[n] = nodeInfo{file: file, tag: tag, plain: plain}
		if tagErr == nil {
			tagErr = checkTag(n, tag)
		}
	})
	l.expanded = expanded
	if err == nil {
		err = tagErr
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if included && len(docs) == 0 {
		// GitLab refuses an empty include: "Local file ... is empty!"
		return nil, fmt.Errorf("%s: the included file is empty, which GitLab refuses", file)
	}
	own, err := configDoc(file, docs)
	if err != nil {
		return nil, err
	}
	merged := newTarget(own.Line)
	if inc := own.Field["include"]; inc != nil {
		files, err := l.includes(file, inc)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if l.included++; l.included > maxIncludes {
				return nil, fmt.Errorf("%s: includes more than %d files, GitLab's limit, nested includes counted", file, maxIncludes)
			}
			sub, err := l.load(f, true)
			if err != nil {
				return nil, err
			}
			if err := l.merge(merged, merged.root, sub); err != nil {
				return nil, err
			}
		}
	}
	for i, k := range own.Keys {
		if k == "include" {
			continue
		}
		if err := l.mergeKey(merged, merged.root, k, own.Vals[i], own.Line); err != nil {
			return nil, err
		}
	}
	return merged.root, nil
}

// configDoc is the document of a file that holds its configuration: its
// one document, or the second when the first is a spec: header, as a file
// that declares inputs writes it. An empty file is an empty configuration.
func configDoc(file string, docs []*core.Node) (*core.Node, error) {
	if len(docs) == 2 && docs[0].Shape == core.Mapping && len(docs[0].Keys) == 1 && docs[0].Keys[0] == "spec" {
		docs = docs[1:]
	}
	switch {
	case len(docs) == 0:
		return core.NewMapping(1), nil
	case len(docs) > 1:
		return nil, fmt.Errorf("%s: holds %d YAML documents; a GitLab CI file holds one, after a spec: header if it has one", file, len(docs))
	case docs[0].Shape != core.Mapping:
		return nil, fmt.Errorf("%s: line %d: the configuration is %s, not a mapping of jobs and keywords", file, docs[0].Line, shapeName(docs[0]))
	}
	return docs[0], nil
}

// includes lists the local files an include: names, in its order, and adds
// each one errata cannot read to l.unreachable. The forms are GitLab's: a
// string, a list of strings and mappings, or one mapping, and a string is a
// remote file when it is a URL and a local one otherwise.
func (l *loader) includes(file string, inc *core.Node) ([]string, error) {
	items := []*core.Node{inc}
	if inc.Shape == core.Sequence {
		items = inc.Items
	}
	var files []string
	for _, it := range items {
		at := fmt.Sprintf("include at %s:%d", file, it.Line)
		var local string
		switch it.Shape {
		case core.Scalar:
			if strings.HasPrefix(it.Text, "http://") || strings.HasPrefix(it.Text, "https://") {
				l.unreachable = append(l.unreachable, "the remote "+at)
				continue
			}
			local = it.Text
		case core.Mapping:
			if it.Field["rules"] != nil {
				l.unreachable = append(l.unreachable, fmt.Sprintf("the %s, whose rules: a pipeline decides", at))
				continue
			}
			loc := it.Field["local"]
			if loc == nil {
				l.unreachable = append(l.unreachable, fmt.Sprintf("the %s %s", includeKind(it), at))
				continue
			}
			if loc.Shape != core.Scalar {
				return nil, fmt.Errorf("%s: line %d: include:local is %s, not a file", file, loc.Line, shapeName(loc))
			}
			local = loc.Text
		default:
			return nil, fmt.Errorf("%s: line %d: an include is a file or a mapping, not a sequence", file, it.Line)
		}
		if strings.ContainsAny(local, "*$") {
			l.unreachable = append(l.unreachable, fmt.Sprintf("the %s, whose file %s a wildcard or a variable names", at, core.Quote(local)))
			continue
		}
		// "include:local" names a file of the project's repository; a
		// leading slash is its root. The name is held to core's bounds
		// on a file name, so a message quotes a short part of it.
		name := strings.TrimLeft(local, "/")
		if name == "" || !filepath.IsLocal(name) {
			return nil, fmt.Errorf("%s: line %d: include %s is outside the repository", file, it.Line, core.Quote(local))
		}
		if _, err := core.CheckSource("include", name, 0); err != nil {
			return nil, fmt.Errorf("%s: line %d: %w", file, it.Line, err)
		}
		// GitLab reads only YAML: "does not have YAML extension!"
		if ext := filepath.Ext(name); ext != ".yml" && ext != ".yaml" {
			return nil, fmt.Errorf("%s: line %d: include %s is not a .yml or .yaml file, which GitLab refuses", file, it.Line, core.Quote(local))
		}
		files = append(files, name)
	}
	return files, nil
}

// includeKind names an include errata cannot read by its key.
func includeKind(it *core.Node) string {
	for _, k := range []string{"remote", "project", "template", "component"} {
		if it.Field[k] != nil {
			return k
		}
	}
	return "unknown kind of"
}

func shapeName(n *core.Node) string {
	if n.Shape == core.Scalar {
		return "a scalar"
	}
	return n.Shape.String()
}

// merge merges src into dst, a mapping of t, as GitLab merges an include
// and extends: "a deep merge, where hash maps are merged at any depth", and
// any other value, an array or a null among them, replaces the one before
// it. Each key counts against the work limit.
func (l *loader) merge(t *target, dst, src *core.Node) error {
	for i, k := range src.Keys {
		if err := l.mergeKey(t, dst, k, src.Vals[i], src.Line); err != nil {
			return err
		}
	}
	return nil
}

// mergeKey merges the value v of key k into dst, a mapping of t.
func (l *loader) mergeKey(t *target, dst *core.Node, k string, v *core.Node, line int) error {
	if err := l.count(1, line); err != nil {
		return err
	}
	old := dst.Field[k]
	if old != nil && old.Shape == core.Mapping && v.Shape == core.Mapping {
		o := old
		if _, owned := t.owned[o]; !owned {
			if err := l.count(len(old.Keys), line); err != nil {
				return err
			}
			o = core.NewMapping(old.Line)
			idx := make(map[string]int, len(old.Keys))
			for i, ok := range old.Keys {
				idx[ok] = i
				o.Keys = append(o.Keys, ok)
				o.Vals = append(o.Vals, old.Vals[i])
				o.Field[ok] = old.Vals[i]
			}
			t.owned[o] = idx
			l.info[o] = nodeInfo{file: l.info[old].file} // a message names the copy's place
		}
		if err := l.merge(t, o, v); err != nil {
			return err
		}
		v = o
	}
	t.set(dst, k, v)
	return nil
}

// checkTag refuses a tag GitLab does not read, as its YAML loader does: it
// reads !reference on a sequence, and YAML's own tags, such as !!str.
func checkTag(n *core.Node, tag string) error {
	switch {
	case tag == "" || strings.HasPrefix(tag, "!!"):
		return nil
	case tag != "!reference":
		return fmt.Errorf("line %d: the tag %s is not one GitLab reads; it reads !reference", n.Line, core.Quote(tag))
	case n.Shape != core.Sequence:
		return fmt.Errorf("line %d: !reference takes a sequence of a job and the keys below it, such as [.setup, script]", n.Line)
	}
	return nil
}

// count adds n to the nodes merging, extends and !reference have touched,
// and refuses them past core.MaxExpanded, the bound YAML aliases have too:
// each costs a step, and repeated !reference tags could otherwise multiply
// a small file into a large one, as aliases could.
func (l *loader) count(n, line int) error {
	if l.work += n; l.work > core.MaxExpanded {
		return fmt.Errorf("line %d: includes, extends and !reference tags take more than %d steps to resolve, the limit; write the repeated part out, or %s", line, core.MaxExpanded, core.OtherWay)
	}
	return nil
}
