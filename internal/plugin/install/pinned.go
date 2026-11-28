package install

import (
	"context"
	"fmt"
	"slices"

	"github.com/f-klement/errata/internal/plugin"
)

// pinned installs every plugin the lock pins, at the pinned version, and
// stops at the first it cannot install: a release from the plugin source,
// a URL entry from its pinned URL, and a file entry not at all, unless the
// cache or the vendored directory holds it already.
func (j *job) pinned(ctx context.Context) error {
	for _, pin := range slices.Clone(j.lock.Pins) {
		if err := j.reinstall(ctx, pin); err != nil {
			return err
		}
	}
	return nil
}

// reinstall installs pin's build for this platform from where pin says it
// came from.
func (j *job) reinstall(ctx context.Context, pin plugin.Pin) error {
	if err := j.checkName(pin.Name); err != nil {
		return err
	}
	switch pin.Source {
	case plugin.SourceURL:
		return j.reinstallURL(ctx, pin)
	case plugin.SourceFile:
		return j.fileEntry(ctx, pin)
	}
	return j.release(ctx, pin.Name, pin.Version)
}

// reinstallURL downloads the build pin pins for this platform from its
// URL, checks it against the pinned sum, describes it and keeps it.
func (j *job) reinstallURL(ctx context.Context, pin plugin.Pin) error {
	at, ok := pin.URLs[j.in.Platform]
	if !ok {
		return fmt.Errorf("%s pins errata-ext-%s %s from URLs, and from none for %s: add one with errata plugin install %s --platform %s --url <https url> --sha256 <hex>",
			plugin.LockFile, pin.Name, pin.Version, j.in.Platform, pin.Name, j.in.Platform)
	}
	sum := pin.Sums[j.in.Platform]
	k, err := j.keep(ctx, j.fromURL(ctx, at, sum, plugin.LockFile), pin.Name, pin.Version)
	if err != nil {
		return err
	}
	if err := j.vendorPin(ctx, pin, k.path); err != nil {
		return err
	}
	j.put(pin, k.desc)
	j.say("errata-ext-%s %s %s: sha256 %s matches %s, from %s", pin.Name, pin.Version, j.in.Platform, sum, plugin.LockFile, at)
	j.sayProvides(pin, k.desc)
	return nil
}

// fileEntry checks that the build pin pins from a file, which errata
// cannot fetch again, is in the cache or the vendored directory, and with
// --vendor copies the cache's into the vendored directory.
func (j *job) fileEntry(ctx context.Context, pin plugin.Pin) error {
	sum, ok := pin.Sums[j.in.Platform]
	cached := plugin.BuildPath(j.cacheBase(), pin.Name, pin.Version, j.in.Platform, sum)
	vendored := plugin.BuildPath(j.vendorBase(), pin.Name, pin.Version, j.in.Platform, sum)
	switch {
	case ok && holds(cached, sum):
		j.say("errata-ext-%s %s %s: sha256 %s, from a file, is in the cache", pin.Name, pin.Version, j.in.Platform, sum)
		return j.vendorPin(ctx, pin, cached)
	case ok && holds(vendored, sum):
		j.say("errata-ext-%s %s %s: sha256 %s, from a file, is vendored", pin.Name, pin.Version, j.in.Platform, sum)
		return nil
	}
	return fmt.Errorf("%s pins errata-ext-%s %s from a file, which errata cannot fetch, and neither the cache nor the vendored directory holds its build for %s: install it with errata plugin install --file <path to errata-ext-%s>",
		plugin.LockFile, pin.Name, pin.Version, j.in.Platform, pin.Name)
}
