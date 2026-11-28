// Package install is errata plugin install (ADR 0021). It fetches a
// plugin's build from a release of this repository, from a mirror of the
// releases, from a URL the user gives with its sum, or copies it from a
// file the user names; checks it; keeps it in the user's cache and, with
// --vendor, in the repository's vendored directory; and writes errata.lock
// last. It is the one package of errata that reaches the network: it alone
// imports net/http, and a run, which package plugin makes, never fetches.
package install

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/plugin"
)

// Installer installs plugins for one repository: into its errata.lock, the
// user's cache and, on request, its vendored directory.
type Installer struct {
	// Root is the repository's root, which holds errata.lock.
	Root string
	// Cache is the user's cache directory. The plugin cache is below it
	// (plugin.CacheIn), and so is every temporary file of a build.
	Cache string
	// Client fetches over https. Install refuses a redirect to anything
	// but https, whatever the client's own policy.
	Client *http.Client
	// Token is GITHUB_TOKEN, or "". It goes with the requests to the
	// releases API on api.github.com and no other.
	Token string
	// Builtin are the keys of errata's built in kinds, which no plugin
	// takes.
	Builtin []string
	// Platform is the platform errata runs on, as the lock names it: the
	// one whose build Install describes.
	Platform string
	// Out is where Install prints what it installed and what it checked.
	Out io.Writer
}

// Request is what one errata plugin install is asked to do: install a
// release of each of Plugins (name or name@version), or with none every
// plugin the lock pins; or a build from File; or one from URL whose sha256
// is SHA256.
type Request struct {
	Plugins []string
	// Source is the plugin source, --source or ERRATA_PLUGIN_SOURCE: a
	// mirror of the releases. "" is DefaultSource.
	Source string
	// TrustSource lets Source pin a version the lock does not pin yet.
	TrustSource bool
	// SourceFrom is what names Source in a message: --source, or the
	// variable it came from; "" is --source.
	SourceFrom string
	// Vendor writes every platform's build into the vendored directory
	// too. An install vendors as if it were set when the repository has
	// the vendored directory (owner, 2026-11-20).
	Vendor bool
	File   string
	URL    string
	SHA256 string
	// Platform is the platform a URL's build is for, "" for this one.
	Platform string
	// Replace lets a file or a URL take the place of a release the lock
	// pins.
	Replace bool
}

// Install does what req asks. It writes errata.lock last, and only when
// every plugin of req is installed, then prints a line for each build it
// installed and what it checked. When the repository has the vendored
// directory, where a run looks for plugins alone, Install vendors what it
// installs, as --vendor does, and says so. A request that fails leaves the
// lock as it was, at most files in the cache that no lock pins, and in the
// vendored directory whole files that git status shows; a temporary file,
// and a directory of the repository it made and left empty, are removed.
func (in *Installer) Install(ctx context.Context, req Request) error {
	if err := req.check(in.Platform); err != nil {
		return err
	}
	lock, err := plugin.ReadLock(in.Root)
	if err != nil {
		return err
	}
	j := &job{in: in, req: req, lock: &lock, vendor: req.Vendor}
	if j.nothing() {
		_, err := fmt.Fprintln(in.Out, plugin.LockFile+" pins no plugin, so there is nothing to install")
		return err
	}
	j.vendorsAnyway()
	if err := j.install(ctx); err != nil {
		j.unmake()
		return err
	}
	_, err = fmt.Fprint(in.Out, strings.Join(j.said, "\n")+"\n")
	return err
}

// install installs what j's request names, then writes the lock.
func (j *job) install(ctx context.Context) error {
	if err := j.run(ctx); err != nil {
		return err
	}
	return plugin.WriteLock(j.in.Root, *j.lock)
}

// vendorsAnyway has j vendor what it installs when the repository has the
// vendored directory and the request did not say --vendor: a run looks
// there alone, so a build only the cache held would not be installed for
// it.
func (j *job) vendorsAnyway() {
	if j.vendor || !isDir(j.vendorBase()) {
		return
	}
	j.vendor = true
	j.say("%s is there, and a run looks for plugins in it alone, so errata vendors what it installs, as --vendor does", plugin.VendorDir)
}

// isDir reports whether path is a directory, following a link as a run
// does.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// job is one Install under way: the lock it changes, whether it vendors,
// the lines it will print once the lock is written, and the directories
// of the repository it made.
type job struct {
	in     *Installer
	req    Request
	lock   *plugin.Lock
	vendor bool
	said   []string
	made   []string
	// before is the env each plugin's pin recorded before j put a new
	// one, for the plugins j pinned again.
	before map[string][]string
}

// nothing reports whether j installs what the lock pins and the lock pins
// nothing.
func (j *job) nothing() bool {
	return j.req.File == "" && j.req.URL == "" && len(j.req.Plugins) == 0 && len(j.lock.Pins) == 0
}

// run installs what j's request names.
func (j *job) run(ctx context.Context) error {
	switch {
	case j.req.File != "":
		return j.file(ctx)
	case j.req.URL != "":
		return j.url(ctx)
	case len(j.req.Plugins) == 0:
		return j.pinned(ctx)
	}
	return j.named(ctx)
}

// put pins pin in the lock with the variables desc, the describe reply
// of its build for this platform, names for read and predict (withEnv),
// and keeps the names a pin of the same plugin recorded here before, to
// print the ones that are new.
func (j *job) put(pin plugin.Pin, desc plugin.Described) {
	old, ok := j.lock.Pin(pin.Name)
	pin.Env = withEnv(pin, old, j.in.Platform, desc.Env)
	if ok {
		if j.before == nil {
			j.before = map[string][]string{}
		}
		j.before[pin.Name] = old.Env[j.in.Platform]
	}
	j.lock.Put(pin)
}

// withEnv is the env of each platform pin pins, once an install has
// described its build for here, which names names: here gets names; a
// platform old pins the same build for keeps what old records for it,
// since an install on that platform described that build; any other gets
// names, as a plugin built from one source mostly names the same
// variables everywhere. So an install on each OS records that OS's names
// and leaves the others' as they were, and a plugin whose names differ by
// OS runs on each (docs/reference.md, errata.lock).
func withEnv(pin, old plugin.Pin, here string, names []string) map[string][]string {
	env := map[string][]string{}
	for platform, sum := range pin.Sums {
		got := names
		if was, ok := old.Sums[platform]; ok && was == sum && platform != here {
			got = old.Env[platform]
		}
		if len(got) > 0 {
			env[platform] = slices.Clone(got)
		}
	}
	return env
}

// sayProvides adds the line that says what pin's build, a protocol 2
// plugin whose describe reply is desc, provides and which of errata's
// variables its read and predict get, the ones its earlier pin did not
// record named apart, so an install or an upgrade shows every variable a
// provider is given. A protocol 1 build, which provides extract alone and
// gets none, adds nothing.
func (j *job) sayProvides(pin plugin.Pin, desc plugin.Described) {
	if len(desc.Operations) == 0 {
		return
	}
	line := "errata-ext-" + pin.Name + " " + pin.Version + " provides " + strings.Join(desc.Operations, ", ")
	switch {
	case !slices.Contains(desc.Operations, "read") && !slices.Contains(desc.Operations, "predict"):
	case len(desc.Env) == 0:
		line += "; its read and predict get no variable of errata's environment"
	default:
		line += "; its read and predict get " + strings.Join(desc.Env, ", ") + " from errata's environment, as " + plugin.LockFile + " now records" + j.newNames(pin.Name, desc.Env)
	}
	j.say("%s", line)
}

// newNames is what a line adds of the names of env that the plugin name's
// earlier pin did not record, or "" for none or no earlier pin.
func (j *job) newNames(name string, env []string) string {
	old, ok := j.before[name]
	if !ok {
		return ""
	}
	var added []string
	for _, n := range env {
		if !slices.Contains(old, n) {
			added = append(added, n)
		}
	}
	if len(added) == 0 {
		return ""
	}
	return "; new since its last pin: " + strings.Join(added, ", ")
}

// say adds a line to what j prints.
func (j *job) say(format string, args ...any) {
	j.said = append(j.said, fmt.Sprintf(format, args...))
}

// cacheBase is the plugin cache.
func (j *job) cacheBase() string { return plugin.CacheIn(j.in.Cache) }

// goos is the os of the platform errata runs on.
func (j *job) goos() string { return osOf(j.in.Platform) }

// osOf is the os of platform, such as linux of linux/amd64.
func osOf(platform string) string {
	goos, _, _ := strings.Cut(platform, "/")
	return goos
}

// checkName refuses a name the protocol does not allow, and the key of a
// kind errata has built in.
func (j *job) checkName(name string) error {
	if err := plugin.CheckName(name); err != nil {
		return err
	}
	if slices.Contains(j.in.Builtin, name) {
		return fmt.Errorf("%s is the key of a kind errata has built in, so no plugin takes it", name)
	}
	return nil
}

// mayReplace refuses a file or a URL that would take the place of a
// release the lock pins under name, unless the request says --replace.
func (j *job) mayReplace(name string) error {
	pin, ok := j.lock.Pin(name)
	if ok && pin.Source == plugin.SourceRelease && !j.req.Replace {
		return fmt.Errorf("%s pins errata-ext-%s %s from a release: pass --replace to take its place with this build", plugin.LockFile, name, pin.Version)
	}
	return nil
}
