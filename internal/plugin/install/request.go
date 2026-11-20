package install

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/plugin"
)

// sumPattern is a sha256 as errata writes it: 64 lower case hex digits.
var sumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// check refuses a request whose flags do not go together, before Install
// reads the lock or opens a connection. this is the platform errata runs
// on.
func (r Request) check(this string) error {
	switch {
	case r.File != "":
		return r.checkFile()
	case r.URL != "" || r.SHA256 != "":
		return r.checkURL(this)
	}
	return r.checkRelease()
}

// checkFile checks the form errata plugin install --file <path>.
func (r Request) checkFile() error {
	switch {
	case r.URL != "" || r.SHA256 != "":
		return errors.New("--file and --url each name a build: give one of them")
	case len(r.Plugins) > 0:
		return errors.New("--file takes the plugin's name from the file's, errata-ext-<name>: give no name")
	case r.Platform != "":
		return errors.New("--platform goes with --url: a file is installed for the platform errata runs on")
	}
	return r.noSource("--file")
}

// checkURL checks the form errata plugin install [<name>] --url <url>
// --sha256 <hex> [--platform <os>/<arch>].
func (r Request) checkURL(this string) error {
	switch {
	case r.URL == "":
		return errors.New("--sha256 goes with --url, the build it is the sum of")
	case r.SHA256 == "":
		return errors.New("--url goes with --sha256, the sum errata checks the download against")
	case len(r.Plugins) > 1:
		return errors.New("--url installs one plugin: give at most one name")
	case !sumPattern.MatchString(strings.ToLower(r.SHA256)):
		return fmt.Errorf("--sha256 %s is not 64 hex digits", strconv.Quote(r.SHA256))
	}
	if err := checkHTTPS("--url", r.URL); err != nil {
		return err
	}
	if err := r.checkPlatform(this); err != nil {
		return err
	}
	return r.noSource("--url")
}

// checkPlatform refuses a --platform errata does not ship for, and one
// other than this with no name to add it to.
func (r Request) checkPlatform(this string) error {
	switch {
	case r.Platform == "":
		return nil
	case !slices.Contains(plugin.Platforms, r.Platform):
		return fmt.Errorf("--platform %s is none errata ships for: give one of %s", strconv.Quote(r.Platform), strings.Join(plugin.Platforms, ", "))
	case r.Platform != this && len(r.Plugins) == 0:
		return fmt.Errorf("--platform %s adds a build to a plugin %s pins from a URL: name the plugin", r.Platform, plugin.LockFile)
	}
	return nil
}

// noSource refuses the flags of an install by name with form, a file or a
// URL, which names its own place.
func (r Request) noSource(form string) error {
	switch {
	case r.Source != "":
		return fmt.Errorf("--source does not apply to %s, which names the build's own place", form)
	case r.TrustSource:
		return fmt.Errorf("--trust-source does not apply to %s, which uses no plugin source", form)
	case r.Vendor:
		return fmt.Errorf("--vendor does not go with %s: run errata plugin install --vendor after it, which vendors what %s pins", form, plugin.LockFile)
	}
	return nil
}

// checkRelease checks the form errata plugin install [<name>[@<version>]...].
func (r Request) checkRelease() error {
	switch {
	case r.Replace:
		return errors.New("--replace goes with --file or --url, a build that takes the place of a release")
	case r.Platform != "":
		return errors.New("--platform goes with --url: a release is installed for the platform errata runs on, and --vendor fetches every other")
	case r.Source == "":
		return nil
	}
	return checkSource(r.sourceFrom(), r.Source)
}

// sourceFrom is what names the request's source in a message.
func (r Request) sourceFrom() string {
	if r.SourceFrom == "" {
		return "--source"
	}
	return r.SourceFrom
}

// checkSource refuses a plugin source, which what names, that is neither
// an https URL with a host nor a file URL of this machine; one that holds
// a user name or a password, which errata's messages would print; and one
// with a query or a fragment, after which errata's paths would not go.
func checkSource(what, source string) error {
	u, err := url.Parse(source)
	if err == nil && hasQuery(u) {
		return fmt.Errorf("%s for %s has a query or a fragment, and errata adds a release's path to the source: write the URL of the directory that holds the tags, with neither", what, u.Host+u.Path)
	}
	if err != nil || u.Scheme != "file" {
		return checkHTTPS(what, source)
	}
	if u.Host != "" && u.Host != "localhost" || u.Path == "" {
		return fmt.Errorf("%s %s is no file URL of this machine: write file:///<path>", what, strconv.Quote(source))
	}
	return nil
}

// hasQuery says whether u has a query or a fragment, even an empty query.
func hasQuery(u *url.URL) bool {
	return u.RawQuery != "" || u.ForceQuery || u.Fragment != ""
}

// checkHTTPS refuses what, a URL, unless it is https with a host and
// holds no user name or password.
func checkHTTPS(what, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s %s is no https URL: write https://<host>/<path>", what, strconv.Quote(raw))
	}
	if u.User != nil {
		return fmt.Errorf("%s for %s holds a user name or password, which errata would print and a lock would publish: write the URL without it", what, u.Host)
	}
	return nil
}
