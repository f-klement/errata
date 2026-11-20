package install

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/f-klement/errata/internal/plugin"
)

// file installs the build the request's File names, a plugin someone else
// built: it copies it into the cache's directory, hashing the copy as it
// writes it, so the bytes errata hashes, describes and keeps are one read
// of a file an author may be rebuilding; describes the copy; keeps it in
// the cache; and pins it for this platform as a file.
func (j *job) file(ctx context.Context) error {
	name, err := j.fileName(j.req.File)
	if err != nil {
		return err
	}
	if err := j.mayReplace(name); err != nil {
		return err
	}
	info, err := os.Lstat(j.req.File)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (%s): name the build itself, not a link to it", j.req.File, info.Mode().Type())
	}
	d := download{from: j.req.File, open: func() (io.ReadCloser, error) { return os.Open(j.req.File) }, check: anySum}
	k, err := j.keep(ctx, d, name, "")
	if err != nil {
		return err
	}
	pin := plugin.Pin{Name: name, Version: k.version, Sums: map[string]string{j.in.Platform: k.sum}, Source: plugin.SourceFile}
	j.lock.Put(pin)
	j.say("errata-ext-%s %s %s: sha256 %s, copied from %s; %s pins it as a file", name, k.version, j.in.Platform, k.sum, j.req.File, plugin.LockFile)
	return j.vendorPin(ctx, pin, k.path)
}

// fileName is the name of the plugin whose build is at file: its file
// name after errata-ext-, without .exe on Windows.
func (j *job) fileName(file string) (string, error) {
	base := filepath.Base(file)
	if j.goos() == "windows" {
		base = strings.TrimSuffix(base, ".exe")
	}
	name, ok := strings.CutPrefix(base, "errata-ext-")
	if !ok {
		return "", fmt.Errorf("%s is not named errata-ext-<name>, which gives the plugin's name", file)
	}
	return name, j.checkName(name)
}

// url installs the build at the request's URL, whose sha256 the request
// gives: for this platform, described and pinned; for another, only
// checked and added to the URL entry that pins the plugin already.
func (j *job) url(ctx context.Context) error {
	name, err := j.urlName()
	if err != nil {
		return err
	}
	sum := strings.ToLower(j.req.SHA256)
	if j.req.Platform != "" && j.req.Platform != j.in.Platform {
		return j.urlPlatform(ctx, name, j.req.Platform, sum)
	}
	if err := j.mayReplace(name); err != nil {
		return err
	}
	k, err := j.keep(ctx, j.fromURL(ctx, j.req.URL, sum, "--sha256"), name, "")
	if err != nil {
		return err
	}
	pin := j.urlPin(name, k.version)
	pin.Sums[j.in.Platform], pin.URLs[j.in.Platform] = sum, j.req.URL
	j.lock.Put(pin)
	j.say("errata-ext-%s %s %s: sha256 %s matches --sha256; %s pins it with its URL", name, k.version, j.in.Platform, sum, plugin.LockFile)
	return j.vendorPin(ctx, pin, k.path)
}

// urlName is the plugin's name for an install from a URL: the one given,
// else the URL's file name after errata-ext-, without the platform's
// _<os>_<arch> and .exe, as a release names its assets.
func (j *job) urlName() (string, error) {
	if len(j.req.Plugins) == 1 {
		return j.req.Plugins[0], j.checkName(j.req.Plugins[0])
	}
	u, err := url.Parse(j.req.URL)
	if err != nil {
		return "", err
	}
	base := strings.TrimSuffix(path.Base(u.Path), ".exe")
	base = strings.TrimSuffix(base, "_"+strings.ReplaceAll(j.in.Platform, "/", "_"))
	name, ok := strings.CutPrefix(base, "errata-ext-")
	if !ok {
		return "", fmt.Errorf("the URL's file name is not errata-ext-<name>, which would give the plugin's name: name the plugin, as errata plugin install <name> --url %s", j.req.URL)
	}
	return name, j.checkName(name)
}

// urlPin is the URL entry of name at version to add a build to: the lock's,
// when it pins name at version from URLs, else a new one.
func (j *job) urlPin(name, version string) plugin.Pin {
	pin, ok := j.lock.Pin(name)
	if ok && pin.Source == plugin.SourceURL && pin.Version == version {
		pin.Sums, pin.URLs = maps.Clone(pin.Sums), maps.Clone(pin.URLs)
		return pin
	}
	return plugin.Pin{Name: name, Version: version, Sums: map[string]string{}, Source: plugin.SourceURL, URLs: map[string]string{}}
}

// urlPlatform downloads the build of name for platform, another than the
// one errata runs on, from the request's URL, checks it against sum, keeps
// it in the cache and adds it to the URL entry of name, without running
// it: only a build for this platform can be described, and the entry's
// first install, of that one, set its version.
func (j *job) urlPlatform(ctx context.Context, name, platform, sum string) error {
	pin, ok := j.lock.Pin(name)
	if !ok || pin.Source != plugin.SourceURL {
		return fmt.Errorf("%s pins errata-ext-%s from no URL: install its build for %s first, which sets its version, then add %s", plugin.LockFile, name, j.in.Platform, platform)
	}
	t, err := j.fetch(j.in.Cache, j.cacheBase(), plugin.Executable(name, osOf(platform)), j.fromURL(ctx, j.req.URL, sum, "--sha256"), 0o555)
	if err != nil {
		return err
	}
	defer t.remove()
	cached := plugin.BuildPath(j.cacheBase(), name, pin.Version, platform, sum)
	if err := j.keepInCache(t.path, cached, sum); err != nil {
		return err
	}
	pin = j.urlPin(name, pin.Version)
	pin.Sums[platform], pin.URLs[platform] = sum, j.req.URL
	j.lock.Put(pin)
	j.say("errata-ext-%s %s %s: sha256 %s matches --sha256; not run, since errata runs on %s; %s pins it with its URL", name, pin.Version, platform, sum, j.in.Platform, plugin.LockFile)
	if !j.vendor {
		return nil
	}
	return j.vendorBuild(fromFile(cached, sum, "--sha256"), name, pin.Version, platform, sum)
}
