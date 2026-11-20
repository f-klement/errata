package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/program"
)

// VendorDir is the repository's vendored plugin directory, from its root.
// When the repository has it, errata looks for plugins there and nowhere
// else, so a CI job with no network and no cache runs from it.
const VendorDir = ".errata/plugins"

// Platform is the platform errata runs on, as the lock file names it.
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Executable is the file name of the plugin name's executable on goos:
// errata-ext-<name>, with .exe on Windows.
func Executable(name, goos string) string {
	if goos == "windows" {
		return "errata-ext-" + name + ".exe"
	}
	return "errata-ext-" + name
}

// CacheDir is errata's plugin cache: errata/plugins in the user's cache
// directory, as os.UserCacheDir names it (XDG_CACHE_HOME or ~/.cache on
// Linux, ~/Library/Caches on macOS, %LocalAppData% on Windows).
// errata plugin install, which is still to come, writes into it.
func CacheDir() (string, error) {
	_, base, err := cacheBase()
	return base, err
}

// cacheBase is the user's cache directory, which errata does not own, and
// errata's plugin cache below it.
func cacheBase() (owner, base string, err error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", "", err
	}
	return d, filepath.Join(d, "errata", "plugins"), nil
}

// BuildPath is where the build of the plugin name at version for platform
// whose sha256 is sum, in hex, sits below base, the cache or the vendored
// directory: <base>/<name>/<version>/<os>_<arch>/<sum>/<Executable>. The
// path holds the sum, so two builds of one name and version never share a
// file: two repositories that pin different builds each find their own, and
// an install of one build cannot overwrite another that a run has hashed
// and is about to start. The install command writes a build's file once,
// by a rename into place, and never writes it again. The cache and the
// vendored directory share the layout, so a vendored directory is a copy
// of the part of a cache a repository needs.
func BuildPath(base, name, version, platform, sum string) string {
	goos, arch, _ := cut(platform)
	return filepath.Join(base, name, version, goos+"_"+arch, sum, Executable(name, goos))
}

// cut splits a platform into its os and its arch.
func cut(platform string) (goos, arch string, ok bool) {
	for i := range len(platform) {
		if platform[i] == '/' {
			return platform[:i], platform[i+1:], true
		}
	}
	return platform, "", false
}

// The timeouts of the protocol's requests (docs/plugin-protocol.md,
// Bounds). They are variables so a test can shorten them.
var (
	describeTimeout = 10 * time.Second
	buildTimeout    = 10 * time.Second
	extractTimeout  = 30 * time.Second
)

// The protocol's bounds on output.
const (
	describeStdout = 1 << 20
	buildStdout    = 32 << 20
	stderrBound    = 1 << 20
	// saidBytes is how much of stderr a message keeps, its last bytes.
	saidBytes = 256
)

// limits are the bounds of one request: the timeout, and the bound on
// stdout.
type limits struct {
	timeout time.Duration
	stdout  int64
}

// exe is one plugin's build: the name a message gives it, such as
// errata-ext-csv 0.1.0, where its executable is, the sha256 the lock pins
// for it, and the check of the one against the other, made once a run.
type exe struct {
	label string
	path  string
	// owner is the directory the build is below that errata does not own:
	// the repository's root, or the user's cache directory. No part of the
	// path below it may be a symbolic link.
	owner   string
	sum     string
	checked *checked
}

// checked is the result of hashing a build, kept for the run once it is
// made: a plugin is hashed once a run, not once a request (ADR 0021).
type checked struct {
	once sync.Once
	err  error
}

// newExe is the build at path below owner, which the lock pins at sum.
func newExe(label, owner, path, sum string) exe {
	return exe{label: label, path: path, owner: owner, sum: sum, checked: &checked{}}
}

// run starts the plugin with req as its one request and returns what it
// wrote to stdout and stderr, once the plugin exited 0 within the bounds of
// lim. It
// checks the build against the lock's checksum first and does not start one
// that differs, hashing it at its first request of the run. The plugin
// runs in an empty directory made for the request and removed after it,
// with only the environment environ passes.
func (x exe) run(ctx context.Context, req any, lim limits) (stdout, stderr []byte, err error) {
	x.checked.once.Do(func() { x.checked.err = x.verify() })
	if err := x.checked.err; err != nil {
		return nil, nil, err
	}
	in, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "errata-plugin-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	p := program.Run(ctx, program.Spec{
		Argv:        []string{x.path},
		Dir:         dir,
		Env:         environ(runtime.GOOS, os.LookupEnv),
		Stdin:       in,
		Timeout:     lim.timeout,
		Bound:       lim.stdout,
		StderrBound: stderrBound,
	})
	if msg := x.failed(ctx, p, lim); msg != "" {
		return nil, nil, errors.New(msg)
	}
	return p.Stdout, p.Stderr, nil
}

// failed is the message of a request that ended without a reply to read,
// or "" for one that exited 0 within its bounds.
func (x exe) failed(ctx context.Context, p program.Ended, lim limits) string {
	switch {
	case p.OverBound != "":
		bound := lim.stdout
		if p.OverBound == "stderr" {
			bound = stderrBound
		}
		return fmt.Sprintf("plugin %s wrote more than %d bytes to %s, the bound on it, so errata stopped it", x.label, bound, p.OverBound)
	case p.Cancelled:
		return fmt.Sprintf("cancelled: errata's run stopped before plugin %s finished (%v)", x.label, context.Cause(ctx))
	case p.TimedOut:
		return fmt.Sprintf("plugin %s did not finish within %s", x.label, lim.timeout)
	case p.Err != nil:
		return fmt.Sprintf("plugin %s failed: %v", x.label, p.Err) + stderrTail(p.Stderr)
	}
	return ""
}

// stderrTail is what a message adds of stderr: its last saidBytes, quoted,
// so a newline or a control character the plugin wrote prints escaped. An
// empty stderr adds nothing.
func stderrTail(stderr []byte) string {
	if len(stderr) == 0 {
		return ""
	}
	return "; its stderr ends " + strconv.Quote(lastBytes(string(stderr), saidBytes))
}

// lastBytes is the last n bytes of s, after "..." when it cut any, starting
// at a whole rune.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return "..." + s[start:]
}

// verify refuses the build when it is not a regular file, such as a
// symbolic link to a file the lock does not pin, or when its sha256 is not
// the one the lock pins, and says when it is not there at all.
func (x exe) verify() error {
	link, err := x.regular()
	if err != nil {
		return err
	}
	return x.hashed(link)
}

// regular is what Lstat says of the build, refusing one that is not there,
// is not a regular file, or is below a symbolic link.
func (x exe) regular() (fs.FileInfo, error) {
	link, err := os.Lstat(x.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("plugin %s is not installed: errata looks for it at %s", x.label, x.path)
	}
	if err != nil {
		return nil, err
	}
	if !link.Mode().IsRegular() {
		return nil, x.notRegular(link.Mode())
	}
	return link, x.noLink()
}

// hashed refuses the build unless the file it opens is link, the file
// regular saw, and its sha256 is the one the lock pins.
func (x exe) hashed(link fs.FileInfo) error {
	f, err := os.Open(x.path)
	if err != nil {
		return err
	}
	defer f.Close()
	// The file opened is the one Lstat saw, not a link put in its place
	// between the two.
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(link, opened) {
		return x.notRegular(fs.ModeSymlink)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != x.sum {
		return fmt.Errorf("plugin %s at %s has the checksum sha256:%s, and %s pins sha256:%s, so errata does not run it", x.label, x.path, got, LockFile, x.sum)
	}
	return nil
}

// noLink refuses the build when a directory on its path below x.owner is
// a symbolic link: a vendored directory, or a part of it, that links
// elsewhere would run a file the repository does not hold, from a place
// another user may write (ADR 0021). The directories above x.owner, which
// errata does not own, may be links, so a cache below a linked home
// directory is still read.
func (x exe) noLink() error {
	rel, err := filepath.Rel(x.owner, filepath.Dir(x.path))
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("plugin %s at %s is not below %s", x.label, x.path, x.owner)
	}
	dir := x.owner
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("plugin %s at %s is below %s, which is a symbolic link, so errata does not run it: install the build itself there, not a link to it", x.label, x.path, dir)
		}
	}
	return nil
}

// notRegular refuses the build at x's path, which is a file of mode m.
func (x exe) notRegular(m fs.FileMode) error {
	return fmt.Errorf("plugin %s at %s is not a regular file (%s), so errata does not run it: install the build itself there, not a link to it", x.label, x.path, m.Type())
}

// goRuntime are the variables a plugin gets when errata has them: the Go
// runtime's, so an operator can tune a plugin written in Go.
var goRuntime = []string{"GOMAXPROCS", "GOMEMLIMIT", "GOGC"}

// environ is a plugin's whole environment on goos: each of goRuntime that
// lookup finds, and on Windows SYSTEMROOT, which the system's libraries
// need. Nothing else of errata's environment reaches a plugin, so a CI
// job's tokens, HOME and PATH do not. It is never nil, though it may be
// empty.
func environ(goos string, lookup func(string) (string, bool)) []string {
	names := goRuntime
	if goos == "windows" {
		names = append(names[:len(names):len(names)], "SYSTEMROOT")
	}
	env := []string{}
	for _, n := range names {
		if v, ok := lookup(n); ok {
			env = append(env, n+"="+v)
		}
	}
	return env
}
