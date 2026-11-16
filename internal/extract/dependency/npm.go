package dependency

import (
	"bytes"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// npmSections are the parts of a package.json that name a range of each
// package, in the order npm documents them.
var npmSections = []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"}

// readPackageJSON reads the range each section of a package.json gives
// the package. A range that names no version in the file is not literal: a
// protocol such as workspace:, file:, npm: or git+https:, a GitHub
// shorthand such as user/repo, and a dist-tag such as latest, which the
// registry resolves. An empty range, which npm reads as any version, is
// written "" and not literal too. An alias, "zod-old": "npm:zod@^3.20.0",
// installs the package under another name, and gives the range after the
// package's name. A byte order mark at the start is skipped, as npm skips
// it.
func readPackageJSON(rd *reading, data []byte) error {
	t, err := tree(rd, data, core.JSONFormat, rd.x.parsers.JSON, true)
	if err != nil {
		return err
	}
	for _, section := range npmSections {
		if err := npmSection(rd, section, t.Member(section)); err != nil {
			return err
		}
	}
	return nil
}

// npmSection adds the range deps, a section of a package.json, gives the
// package, nothing when it is no mapping.
func npmSection(rd *reading, section string, deps *core.Node) error {
	if deps == nil || deps.Shape != core.Mapping {
		return nil
	}
	for i, key := range deps.Keys {
		if err := npmEntry(rd, section, key, deps.Vals[i]); err != nil {
			return err
		}
	}
	return nil
}

// npmEntry adds the range n of the entry key of a package.json's section
// when the entry names the package, under its key or as an alias.
func npmEntry(rd *reading, section, key string, n *core.Node) error {
	v := n.Text
	if alias, r, ok := npmAlias(v); ok && n.Type == core.TypeString {
		if !rd.is(alias) {
			return nil
		}
		v = r
	} else if !rd.is(key) {
		return nil
	} else if s, err := text(n, section+" of the package"); err != nil {
		return err
	} else {
		v = s
	}
	f := found{text: v, section: section, role: Range, line: n.Line, notLiteral: npmNotLiteral(v)}
	if v == "" {
		f.text = `""`
	}
	return rd.add(f)
}

// npmAlias is the package and the range of an npm alias, npm:name@range,
// which installs name under another key; false for any other text.
func npmAlias(v string) (name, r string, ok bool) {
	rest, ok := strings.CutPrefix(v, "npm:")
	if !ok {
		return "", "", false
	}
	name, r = splitAt(rest)
	return name, r, name != ""
}

// npmNotLiteral reports whether npm reads the range v from somewhere
// other than the text: a protocol or a path holds a : or a /, which no
// range of versions holds, and a dist-tag starts with a letter, which a
// range starts with only as x, as in x or x.1, or as the v of v1.2.
func npmNotLiteral(v string) bool {
	if v == "" || strings.ContainsAny(v, ":/") {
		return true
	}
	c := v[0] | 0x20 // lower case, for a letter
	if c < 'a' || c > 'z' {
		return false
	}
	return npmTag(c, v)
}

// npmTag reports whether v, a range whose first byte is the letter c in
// lower case, is a dist-tag: any word but x or v as a range writes them.
func npmTag(c byte, v string) bool {
	switch c {
	case 'x':
		return len(v) > 1 && v[1] != '.' && v[1] != ' '
	case 'v':
		return len(v) == 1 || v[1] < '0' || v[1] > '9'
	}
	return true
}

// readPackageLock reads a package-lock.json, or an npm-shrinkwrap.json,
// which has its form. From lockfile version 2 on, packages holds every
// package npm installs under its path, such as node_modules/a or
// node_modules/a/node_modules/b, each with the version installed; the
// package's own entry, "", and a workspace's, under its directory, name
// it by name. Version 1 holds dependencies instead, nested as the
// node_modules directories are. The name and version at the top are the
// package's own. A byte order mark at the start is skipped, as npm skips
// it.
func readPackageLock(rd *reading, data []byte) error {
	t, err := tree(rd, data, core.JSONFormat, rd.x.parsers.JSON, true)
	if err != nil {
		return err
	}
	if err := own(rd, t, "version"); err != nil {
		return err
	}
	if packages := t.Member("packages"); packages != nil && packages.Shape == core.Mapping {
		return lockV2(rd, packages)
	}
	return lockV1(rd, t.Member("dependencies"))
}

// lockV2 adds the package's version in packages, the packages of a
// lockfile of version 2 or later: each installed under node_modules, and
// the project's own and its workspaces' by name.
func lockV2(rd *reading, packages *core.Node) error {
	for i, key := range packages.Keys {
		entry := packages.Vals[i]
		if i := strings.LastIndex(key, nodeModules); i >= 0 {
			if err := installed(rd, key[i+len(nodeModules):], entry); err != nil {
				return err
			}
		} else if err := own(rd, entry, "packages"); err != nil {
			return err
		}
	}
	return nil
}

// installed adds the version of entry, which npm installs under the
// directory dir, when it is the package's: an alias's entry names the
// package it installs.
func installed(rd *reading, dir string, entry *core.Node) error {
	if n := entry.Member("name"); n != nil && n.Shape == core.Scalar && n.Type == core.TypeString {
		dir = n.Text
	}
	if !rd.is(dir) {
		return nil
	}
	return lockVersion(rd, entry, "packages")
}

// nodeModules is what a package-lock.json's key of an installed package
// holds before the package's name.
const nodeModules = "node_modules/"

// own adds the version of n, an entry that names its package by name, when
// that is the package.
func own(rd *reading, n *core.Node, section string) error {
	name := n.Member("name")
	if name == nil || name.Shape != core.Scalar || !rd.is(name.Text) {
		return nil
	}
	return lockVersion(rd, n, section)
}

// lockVersion adds the version of a lockfile's entry n, nothing for an
// entry without one, such as a link to a workspace. A version that is a
// URL or a path, as a package from git or a tarball records, is not
// literal.
func lockVersion(rd *reading, n *core.Node, section string) error {
	v := n.Member("version")
	if v == nil {
		return nil
	}
	s, err := text(v, "version")
	if err != nil {
		return err
	}
	return rd.add(found{text: s, section: section, role: Resolved, line: v.Line, notLiteral: strings.ContainsAny(s, ":/")})
}

// lockV1 adds the package's version in deps, a version 1 lockfile's
// dependencies, and in the dependencies nested in each entry.
func lockV1(rd *reading, deps *core.Node) error {
	if deps == nil || deps.Shape != core.Mapping {
		return nil
	}
	for i, key := range deps.Keys {
		entry := deps.Vals[i]
		if err := lockV1Entry(rd, key, entry); err != nil {
			return err
		}
		if err := lockV1(rd, entry.Member("dependencies")); err != nil {
			return err
		}
	}
	return nil
}

// lockV1Entry adds the version of entry, keyed key in a version 1
// lockfile's dependencies, when it is the package's.
func lockV1Entry(rd *reading, key string, entry *core.Node) error {
	alias, err := lockV1Alias(rd, entry)
	if err != nil || alias || key == "" || !rd.is(key) {
		return err
	}
	return lockVersion(rd, entry, "dependencies")
}

// lockV1Alias adds the version of entry when it is an alias of the
// package, and reports whether it is an alias at all: an alias's version
// is npm:name@version.
func lockV1Alias(rd *reading, entry *core.Node) (bool, error) {
	v := entry.Member("version")
	if v == nil || v.Type != core.TypeString {
		return false, nil
	}
	alias, version, ok := npmAlias(v.Text)
	if !ok {
		return false, nil
	}
	if !rd.is(alias) {
		return true, nil
	}
	return true, rd.add(found{text: version, section: "dependencies", role: Resolved, line: v.Line})
}

// readPnpmLock reads a pnpm-lock.yaml's packages, each keyed by the
// package and the version installed: /name/1.2.3 in lockfile version 5,
// /name@1.2.3 in 6, and name@1.2.3 from 9 on. A key may end in the peers
// the package was resolved with, (peer@2.0.0) or, in version 5, _peer@2.0.0,
// which are not part of the version.
func readPnpmLock(rd *reading, data []byte) error {
	t, err := yamlTree(rd, data)
	if err != nil || t == nil {
		return err
	}
	return pnpmPackages(rd, t.Member("packages"), pnpmV5(t))
}

// pnpmV5 reports whether the pnpm-lock.yaml t is of lockfile version 5,
// whose keys are /name/1.2.3.
func pnpmV5(t *core.Node) bool {
	lv := t.Member("lockfileVersion")
	return lv != nil && lv.Shape == core.Scalar && strings.HasPrefix(lv.Text, "5")
}

// pnpmPackages adds the package's version in packages, a pnpm-lock.yaml's
// packages, nothing when it is no mapping.
func pnpmPackages(rd *reading, packages *core.Node, v5 bool) error {
	if packages == nil || packages.Shape != core.Mapping {
		return nil
	}
	for i, key := range packages.Keys {
		name, version := pnpmKey(key, v5)
		if version == "" || !rd.is(name) {
			continue
		}
		// A key from a path, git or a tarball, foo@file:../foo, names no
		// version.
		if err := rd.add(found{text: version, section: "packages", role: Resolved, line: packages.Vals[i].LineOfKey(), notLiteral: strings.ContainsAny(version, ":/")}); err != nil {
			return err
		}
	}
	return nil
}

// pnpmKey is the name and the version of a key of a pnpm-lock.yaml's
// packages, "" for a key of no form pnpm writes.
func pnpmKey(key string, v5 bool) (name, version string) {
	key = strings.TrimPrefix(key, "/")
	if v5 {
		i := strings.LastIndexByte(key, '/')
		if i <= 0 {
			return "", ""
		}
		version, _, _ = strings.Cut(key[i+1:], "_")
		return key[:i], version
	}
	key, _, _ = strings.Cut(key, "(")
	return splitAt(key)
}

// splitAt splits a descriptor such as name@1.2.3 or @scope/name@npm:^1 at
// the @ after the name, which a scoped name starts with too; "" for none.
func splitAt(d string) (name, rest string) {
	i := strings.IndexByte(d[min(1, len(d)):], '@')
	if i < 0 {
		return "", ""
	}
	i += min(1, len(d))
	return d[:i], d[i+1:]
}

// yamlTree is the first document of a YAML file, after the node limit, nil
// for a file of none.
func yamlTree(rd *reading, data []byte) (*core.Node, error) {
	if rd.x.parsers.YAML == nil {
		return nil, core.LineError(1, "errata was built without the reader for %s", rd.r.name)
	}
	if err := rd.src.CheckNodes(data, core.YAMLFormat); err != nil {
		return nil, named{err}
	}
	docs, _, err := rd.x.parsers.YAML(data, 0)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 || docs[0] == nil {
		return nil, nil
	}
	if docs[0].Shape != core.Mapping {
		return nil, core.LineError(docs[0].Line, "the file is no mapping of keys, as %s is", rd.r.name)
	}
	return docs[0], nil
}

// readYarnLock reads a yarn.lock. Yarn 2 and later write YAML, with a
// __metadata key; each entry is keyed by the descriptors it resolves,
// "name@npm:^1, name@npm:^2", and gives the version. Yarn 1 writes its own
// format: a line at the margin lists the descriptors, quoted or not, and an
// indented version line below gives the version. A workspace's entry,
// name@workspace:., and a link's or a portal's record 0.0.0-use.local, no
// version, and are passed over. An alias, zod-old@npm:zod@^3, names zod.
func readYarnLock(rd *reading, data []byte) error {
	if bytes.HasPrefix(data, []byte("__metadata:")) || bytes.Contains(data, []byte("\n__metadata:")) {
		return readYarnBerry(rd, data)
	}
	// Yarn drops a byte order mark before it parses.
	data = core.DropBOM(data)
	y := yarnV1{rd: rd}
	return eachLine(data, y.line)
}

// yarnV1 reads a Yarn 1 yarn.lock a line at a time; matched is whether
// the entry the lines are in names the package.
type yarnV1 struct {
	rd      *reading
	matched bool
}

// line reads line n: a line at the margin starts an entry, and the
// version line of an entry that names the package gives its version.
func (y *yarnV1) line(line []byte, n int) error {
	if len(line) == 0 || line[0] == '#' {
		return nil
	}
	if line[0] != ' ' {
		head, ok := bytes.CutSuffix(bytes.TrimRight(line, " \t"), []byte(":"))
		y.matched = ok && yarnDescribes(y.rd, string(head))
		return nil
	}
	if !y.matched {
		return nil
	}
	rest, ok := bytes.CutPrefix(bytes.TrimLeft(line, " "), []byte("version "))
	if !ok || len(line)-len(bytes.TrimLeft(line, " ")) != 2 {
		return nil
	}
	v := strings.Trim(strings.TrimSpace(string(rest)), `"`)
	return y.rd.add(found{text: v, role: Resolved, line: n})
}

// yarnDescribes reports whether a list of descriptors, comma separated and
// each quoted or not, names the package other than as a workspace.
func yarnDescribes(rd *reading, head string) bool {
	for d := range strings.SplitSeq(head, ",") {
		name, rest := splitAt(strings.Trim(strings.TrimSpace(d), `"`))
		if alias, _, ok := npmAlias(rest); ok {
			name = alias
		}
		if name != "" && rd.is(name) && !localProtocol(rest) {
			return true
		}
	}
	return false
}

// localProtocol reports whether a descriptor's range is a package of the
// repository's own, a workspace, a link or a portal, whose entry records
// 0.0.0-use.local in place of a version.
func localProtocol(r string) bool {
	return strings.HasPrefix(r, "workspace:") || strings.HasPrefix(r, "link:") || strings.HasPrefix(r, "portal:")
}

// readYarnBerry reads the YAML yarn.lock of Yarn 2 and later.
func readYarnBerry(rd *reading, data []byte) error {
	t, err := yamlTree(rd, data)
	if err != nil || t == nil {
		return err
	}
	for i, key := range t.Keys {
		entry := t.Vals[i]
		if key == "__metadata" || !yarnDescribes(rd, key) {
			continue
		}
		v := entry.Member("version")
		if v == nil || v.Shape != core.Scalar {
			continue
		}
		if err := rd.add(found{text: v.Text, role: Resolved, line: v.Line}); err != nil {
			return err
		}
	}
	return nil
}
