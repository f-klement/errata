package install

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"strings"

	"github.com/f-klement/errata/internal/plugin"
	"github.com/f-klement/errata/internal/semver"
)

// DefaultSource is the plugin source when no mirror is set: the releases
// of errata's public repository, under which a release's asset is
// <tag>/<asset>.
const DefaultSource = "https://github.com/f-klement/errata/releases/download"

// releasesAPI lists the repository's releases, errata's and every
// plugin's, newest first.
const releasesAPI = "https://" + apiHost + "/repos/f-klement/errata/releases"

// perPage is how many releases a page of the API lists, its most; a page
// with fewer is the last. A variable, so a test pages a few releases.
var perPage = 100

// maxPages is the most pages errata reads of the releases API: 10,000
// releases.
const maxPages = 100

// maxChecksums is the largest checksums file errata reads: 64 KiB.
const maxChecksums = 64 << 10

// release is one release of a plugin at a source.
type release struct {
	source, name, version string
}

// tag is r's tag, errata-ext-<name>/v<version>.
func (r release) tag() string { return "errata-ext-" + r.name + "/v" + r.version }

// url is where asset of r is at its source: <source>/<tag>/<asset>, the
// tag's / escaped as %2F, as GitHub's own links write it.
func (r release) url(asset string) string {
	return strings.TrimRight(r.source, "/") + "/" + url.PathEscape(r.tag()) + "/" + url.PathEscape(asset)
}

// label is how a message names r: errata-ext-csv 0.1.0.
func (r release) label() string { return "errata-ext-" + r.name + " " + r.version }

// checksumsName is the name of the checksums file of name's releases.
func checksumsName(name string) string { return "errata-ext-" + name + "_checksums.txt" }

// noticesName is the name of the notices file of name's releases.
func noticesName(name string) string { return "errata-ext-" + name + "_NOTICES.txt" }

// assetName is the name of name's build for platform in a release:
// errata-ext-<name>_<os>_<arch>, with .exe on Windows.
func assetName(name, platform string) string {
	goos, arch, _ := strings.Cut(platform, "/")
	n := "errata-ext-" + name + "_" + goos + "_" + arch
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

// isDefault reports whether j installs from DefaultSource.
func (j *job) isDefault() bool {
	return j.req.Source == "" || strings.TrimRight(j.req.Source, "/") == DefaultSource
}

// source is the plugin source j installs from.
func (j *job) source() string {
	if j.req.Source == "" {
		return DefaultSource
	}
	return j.req.Source
}

// named installs a release of each plugin the request names, after it has
// checked every name and version.
func (j *job) named(ctx context.Context) error {
	targets, err := j.targets()
	if err != nil {
		return err
	}
	for _, t := range targets {
		if err := j.release(ctx, t.name, t.version); err != nil {
			return err
		}
	}
	return nil
}

// target is a plugin the request names, and its version if it gives one.
type target struct{ name, version string }

// targets reads the request's names, refusing one named twice.
func (j *job) targets() ([]target, error) {
	var out []target
	seen := map[string]bool{}
	for _, arg := range j.req.Plugins {
		t, err := j.parseTarget(arg)
		if err != nil {
			return nil, err
		}
		if seen[t.name] {
			return nil, fmt.Errorf("%s is named twice: install one version of a plugin", t.name)
		}
		seen[t.name] = true
		out = append(out, t)
	}
	return out, nil
}

// parseTarget reads name or name@version.
func (j *job) parseTarget(arg string) (target, error) {
	name, version, at := strings.Cut(arg, "@")
	if err := j.checkName(name); err != nil {
		return target{}, err
	}
	if at {
		if err := plugin.CheckVersion(version); err != nil {
			return target{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	return target{name, version}, nil
}

// release installs name's release at version, or at the version
// j.version chooses when it is "".
func (j *job) release(ctx context.Context, name, version string) error {
	pin, pinned := j.lock.Pin(name)
	if pinned && pin.Source != plugin.SourceRelease {
		return fmt.Errorf("%s pins errata-ext-%s from a %s, and errata installs no release over it: delete its lines from %s to install the release", plugin.LockFile, name, pin.Source, plugin.LockFile)
	}
	version, err := j.version(ctx, name, version, pin, pinned)
	if err != nil {
		return err
	}
	r := release{source: j.source(), name: name, version: version}
	samePin := pinned && pin.Version == version
	if !samePin && !j.isDefault() && !j.req.TrustSource {
		return j.untrusted(r)
	}
	return j.fetchRelease(ctx, r, pin, samePin)
}

// version is the version of name to install: the one given; else the
// lock's, when it pins name; else the newest release's, which only the
// default source's API can say.
func (j *job) version(ctx context.Context, name, given string, pin plugin.Pin, pinned bool) (string, error) {
	switch {
	case given != "":
		return given, nil
	case pinned:
		return pin.Version, nil
	case !j.isDefault():
		return "", fmt.Errorf("a mirror has no releases API to find the newest release of errata-ext-%s: give its version, as %s@<version>, or install one %s pins", name, name, plugin.LockFile)
	}
	return j.newest(ctx, name)
}

// untrusted refuses the first pin of r through a source other than the
// default (ADR 0021).
func (j *job) untrusted(r release) error {
	return fmt.Errorf("%s pins no %s, and the plugin source %s is not errata's releases, so it would choose the bytes %s pins: pin the version once from errata's releases and commit %s, or pass --trust-source to trust this source as errata's releases are trusted",
		plugin.LockFile, r.label(), r.source, plugin.LockFile, plugin.LockFile)
}

// fetchRelease installs r: it reads r's checksums file, checks it against
// pin when the lock pins this version, keeps this platform's build in the
// cache, and with --vendor every platform's in the vendored directory, and
// pins every platform's sum.
func (j *job) fetchRelease(ctx context.Context, r release, pin plugin.Pin, samePin bool) error {
	sums, err := j.checksums(ctx, r)
	if err != nil {
		return err
	}
	sum, ok := sums.platforms[j.in.Platform]
	if !ok {
		return fmt.Errorf("%s of %s lists no build for %s", checksumsName(r.name), r.label(), j.in.Platform)
	}
	if samePin {
		if err := differs(r, pin.Sums, sums.platforms); err != nil {
			return err
		}
	}
	k, err := j.keepRelease(ctx, r, sum)
	if err != nil {
		return err
	}
	if err := j.vendorRelease(ctx, r, sums, k.path); err != nil {
		return err
	}
	pin = plugin.Pin{Name: r.name, Version: r.version, Sums: maps.Clone(sums.platforms), Source: plugin.SourceRelease}
	j.put(pin, k.desc)
	j.say("%s %s: sha256 %s matches %s%s", r.label(), j.in.Platform, sum, checksumsName(r.name), pinSaid(samePin))
	j.sayProvides(pin, k.desc)
	return nil
}

// pinSaid is how the line of a release ends: whether the lock pinned it
// already, or pins it now.
func pinSaid(samePin bool) string {
	if samePin {
		return " and " + plugin.LockFile
	}
	return ", and " + plugin.LockFile + " now pins it"
}

// keepRelease downloads r's build for this platform, checks it against
// sum, describes it, keeps it in the cache and returns it as kept there.
// A good build the cache holds already is described and kept, not
// downloaded again.
func (j *job) keepRelease(ctx context.Context, r release, sum string) (kept, error) {
	cached := plugin.BuildPath(j.cacheBase(), r.name, r.version, j.in.Platform, sum)
	if holds(cached, sum) {
		desc, err := plugin.Describe(ctx, j.in.Cache, cached, r.name, r.version, sum)
		return kept{path: cached, version: desc.Version, sum: sum, desc: desc}, err
	}
	d := j.fromURL(ctx, r.url(assetName(r.name, j.in.Platform)), sum, checksumsName(r.name))
	return j.keep(ctx, d, r.name, r.version)
}

// kept is a build errata kept in the cache: its path there, the version it
// gives, its sha256, and what its describe reply said.
type kept struct {
	path, version, sum string
	desc               plugin.Described
}

// keep fetches d, a build of name for this platform, into the cache's
// directory, checking it as d says; sends it describe, which must give
// version, or any version for ""; and keeps it in the cache.
func (j *job) keep(ctx context.Context, d download, name, version string) (kept, error) {
	t, err := j.fetch(j.in.Cache, j.cacheBase(), plugin.Executable(name, j.goos()), d, 0o555)
	if err != nil {
		return kept{}, err
	}
	defer t.remove()
	desc, err := plugin.Describe(ctx, j.in.Cache, t.path, name, version, t.sum)
	if err != nil {
		return kept{}, err
	}
	k := kept{path: plugin.BuildPath(j.cacheBase(), name, desc.Version, j.in.Platform, t.sum), version: desc.Version, sum: t.sum, desc: desc}
	return k, j.keepInCache(t.path, k.path, t.sum)
}

// differs refuses a checksums file that lists other platforms, or other
// sums, than the lock pins for r: a release that changed after it was
// pinned, or a source that serves other bytes. errata never rewrites a pin
// to match one.
func differs(r release, pinned, listed map[string]string) error {
	var diffs []string
	for _, p := range plugin.Platforms {
		a, inLock := pinned[p]
		b, inFile := listed[p]
		switch {
		case inLock && !inFile:
			diffs = append(diffs, p+": the lock pins sha256:"+a+", and the file lists none")
		case inFile && !inLock:
			diffs = append(diffs, p+": the file lists sha256:"+b+", and the lock pins none")
		case a != b:
			diffs = append(diffs, p+": the lock pins sha256:"+a+", and the file lists sha256:"+b)
		}
	}
	if len(diffs) == 0 {
		return nil
	}
	return fmt.Errorf("%s of %s differs from %s (%s): the release changed after it was pinned, or the source serves other bytes. errata never rewrites a pin: delete the plugin's lines from %s to pin it anew",
		checksumsName(r.name), r.label(), plugin.LockFile, strings.Join(diffs, "; "), plugin.LockFile)
}

// checksums reads and parses r's checksums file.
func (j *job) checksums(ctx context.Context, r release) (checksums, error) {
	at := r.url(checksumsName(r.name))
	body, err := j.open(ctx, at)
	if err != nil {
		return checksums{}, err
	}
	defer body.Close()
	data, err := readAll(body, maxChecksums, at)
	if err != nil {
		return checksums{}, err
	}
	c, err := parseChecksums(r.name, data)
	if err != nil {
		return checksums{}, fmt.Errorf("%s of %s: %w", checksumsName(r.name), r.label(), err)
	}
	return c, nil
}

// checksums is what a checksums file lists: the sha256 of each platform's
// build, and of the notices file, or "" when it lists none.
type checksums struct {
	platforms map[string]string
	notices   string
}

// parseChecksums reads a checksums file of name's release strictly: each
// line is 64 lower case hex digits, two spaces and an asset's name; an
// asset is a build of name for a platform errata ships for, or its
// notices file; and no asset is listed twice, even with the same sum.
func parseChecksums(name string, data []byte) (checksums, error) {
	assets := assetsOf(name)
	c := checksums{platforms: map[string]string{}}
	seen := map[string]bool{}
	for i, line := range lines(string(data)) {
		asset, sum, err := checksumLine(line)
		if err == nil && seen[asset] {
			err = fmt.Errorf("it lists %s twice", asset)
		}
		if err == nil {
			err = c.add(assets, name, asset, sum)
		}
		if err != nil {
			return checksums{}, fmt.Errorf("line %d: %w", i+1, err)
		}
		seen[asset] = true
	}
	return c, nil
}

// lines are the lines of text, each without its newline, and none after
// the last newline.
func lines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// checksumLine reads one line of a checksums file.
func checksumLine(line string) (asset, sum string, err error) {
	sum, asset, ok := strings.Cut(line, "  ")
	if !ok || !sumPattern.MatchString(sum) || asset == "" {
		return "", "", fmt.Errorf("%q is not 64 lower case hex digits, two spaces and an asset's name", line)
	}
	return asset, sum, nil
}

// assetsOf maps each asset name a release of name may list to its
// platform, and the notices file to "".
func assetsOf(name string) map[string]string {
	m := map[string]string{noticesName(name): ""}
	for _, p := range plugin.Platforms {
		m[assetName(name, p)] = p
	}
	return m
}

// add records asset's sum, refusing an asset that is neither a build of
// name for a platform nor its notices file.
func (c *checksums) add(assets map[string]string, name, asset, sum string) error {
	platform, ok := assets[asset]
	switch {
	case !ok:
		return fmt.Errorf("it lists %s, which is neither a build of errata-ext-%s for a platform errata ships for nor %s", asset, name, noticesName(name))
	case platform == "":
		c.notices = sum
	default:
		c.platforms[platform] = sum
	}
	return nil
}

// apiRelease is what errata reads of a release the API lists.
type apiRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// newest is the highest version, by semantic versioning's precedence, of
// name's releases that are neither drafts nor prereleases. It reads every
// page of the API until one lists fewer than perPage, since the API lists
// every release of the repository, errata's and every plugin's, by date.
func (j *job) newest(ctx context.Context, name string) (string, error) {
	prefix := "errata-ext-" + name + "/v"
	best := ""
	for page := 1; page <= maxPages; page++ {
		rs, err := j.releases(ctx, page)
		if err != nil {
			return "", err
		}
		best = highest(best, prefix, rs)
		if len(rs) < perPage {
			return found(name, best)
		}
	}
	return "", fmt.Errorf("the releases API lists more than %d pages of %d", maxPages, perPage)
}

// found is best, the newest version of name, or an error when there is
// none.
func found(name, best string) (string, error) {
	if best == "" {
		return "", fmt.Errorf("errata's releases hold no release of errata-ext-%s that is neither a draft nor a prerelease", name)
	}
	return best, nil
}

// highest is the highest of best and the versions of rs whose tags start
// with prefix and that are neither drafts nor prereleases.
func highest(best, prefix string, rs []apiRelease) string {
	for _, r := range rs {
		v, ok := strings.CutPrefix(r.Tag, prefix)
		if !ok || r.Draft || r.Prerelease || plugin.CheckVersion(v) != nil || isPrerelease(v) {
			continue
		}
		if best == "" || newer(v, best) {
			best = v
		}
	}
	return best
}

// isPrerelease reports whether v, a semantic version, has a prerelease
// part, as 1.0.0-rc.1 does: a release GitHub does not flag as one is still
// no newest release.
func isPrerelease(v string) bool {
	core, _, _ := strings.Cut(v, "+")
	return strings.Contains(core, "-")
}

// newer reports whether a comes after b by semantic versioning's
// precedence. Both are semantic versions.
func newer(a, b string) bool {
	va, _ := semver.ParseVersion(a)
	vb, _ := semver.ParseVersion(b)
	return semver.Compare(va, vb) > 0
}

// releases reads one page of the releases API.
func (j *job) releases(ctx context.Context, page int) ([]apiRelease, error) {
	at := fmt.Sprintf("%s?per_page=%d&page=%d", releasesAPI, perPage, page)
	body, err := j.in.get(ctx, at, true)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := readAll(body, maxDownload, at)
	if err != nil {
		return nil, err
	}
	var rs []apiRelease
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	return rs, nil
}
