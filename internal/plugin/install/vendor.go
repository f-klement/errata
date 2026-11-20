package install

import (
	"context"
	_ "embed"
	"errors"
	"path/filepath"

	"github.com/f-klement/errata/internal/plugin"
)

// license is errata's LICENSE, which --vendor writes beside the builds it
// vendors, since MIT asks that its notice go with every copy (ADR 0021). A
// test holds it equal to the repository's LICENSE.
//
//go:embed LICENSE
var license []byte

// vendorBase is the repository's vendored directory.
func (j *job) vendorBase() string {
	return filepath.Join(j.in.Root, filepath.FromSlash(plugin.VendorDir))
}

// vendorRelease writes, with --vendor, every platform's build of r that
// sums lists into the vendored directory, each checked against its sum:
// this platform's copied from cached, its path in the cache, and the
// others downloaded. It then writes the notices file when sums lists one,
// and errata's LICENSE.
func (j *job) vendorRelease(ctx context.Context, r release, sums checksums, cached string) error {
	if !j.vendor {
		return nil
	}
	for _, p := range plugin.Platforms {
		sum, ok := sums.platforms[p]
		if !ok {
			continue
		}
		d := j.fromURL(ctx, r.url(assetName(r.name, p)), sum, checksumsName(r.name))
		if err := j.vendorBuild(j.here(d, cached, sum, p), r.name, r.version, p, sum); err != nil {
			return err
		}
	}
	if sums.notices != "" {
		d := j.fromURL(ctx, r.url(noticesName(r.name)), sums.notices, checksumsName(r.name))
		if err := j.vendorText(d, r.name, r.version, noticesName(r.name), sums.notices); err != nil {
			return err
		}
	}
	return j.vendorText(fromBytes("errata's LICENSE", license), r.name, r.version, "LICENSE", sumOf(license))
}

// here is d, or for this platform the copy of the build at cached, which
// errata checked and described as it kept it.
func (j *job) here(d download, cached, sum, platform string) download {
	if platform != j.in.Platform {
		return d
	}
	return fromFile(cached, sum, "the cache's path")
}

// vendorBuild writes d, the build of name at version for platform whose
// sha256 is sum, into the vendored directory, under its sum. A build
// vendored there already is not fetched again.
func (j *job) vendorBuild(d download, name, version, platform, sum string) error {
	base := j.vendorBase()
	dst := plugin.BuildPath(base, name, version, platform, sum)
	if !holds(dst, sum) {
		t, err := j.fetch(j.in.Root, base, plugin.Executable(name, osOf(platform)), d, 0o555)
		if err != nil {
			return err
		}
		defer t.remove()
		if err := j.keepVendored(t.path, dst, sum); err != nil {
			return err
		}
	}
	j.say("errata-ext-%s %s %s: vendored at %s", name, version, platform, j.rel(dst))
	return nil
}

// vendorText writes d, a text file of name at version whose sha256 is
// sum, as file in the version's directory of the vendored directory. A
// different file there, such as a LICENSE from an older errata or one git
// checked out with CRLF line ends, is left as it is and said: a run never
// reads it.
func (j *job) vendorText(d download, name, version, file, sum string) error {
	base := j.vendorBase()
	t, err := j.fetch(j.in.Root, base, file, d, 0o444)
	if err != nil {
		return err
	}
	defer t.remove()
	dst := filepath.Join(base, name, version, file)
	err = j.keepVendored(t.path, dst, sum)
	if errors.Is(err, errVendoredDiffers) {
		j.say("errata-ext-%s %s: %s differs from the one errata would write, and errata left it as it is; remove it to write it anew", name, version, j.rel(dst))
		return nil
	}
	return err
}

// rel is path from the repository's root, as a message names it.
func (j *job) rel(path string) string {
	if r, err := filepath.Rel(j.in.Root, path); err == nil {
		return filepath.ToSlash(r)
	}
	return path
}

// vendorPin writes, with --vendor, every build pin pins into the vendored
// directory: this platform's copied from cached, and from a URL entry each
// other platform's downloaded from its URL and checked against its pin.
func (j *job) vendorPin(ctx context.Context, pin plugin.Pin, cached string) error {
	if !j.vendor {
		return nil
	}
	for _, p := range plugin.Platforms {
		sum, ok := pin.Sums[p]
		if !ok {
			continue
		}
		d := j.fromURL(ctx, pin.URLs[p], sum, plugin.LockFile)
		if err := j.vendorBuild(j.here(d, cached, sum, p), pin.Name, pin.Version, p, sum); err != nil {
			return err
		}
	}
	return j.vendorText(fromBytes("errata's LICENSE", license), pin.Name, pin.Version, "LICENSE", sumOf(license))
}
