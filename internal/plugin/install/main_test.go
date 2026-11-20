package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/plugin"
)

// fakes are the fake plugin of package plugin's tests, built once for each
// name and version a test publishes: fakes["csv 0.1.0"] describes itself
// as csv 0.1.0. A name of "-" leaves the fake to take its name from its
// file's.
var fakes = map[string][]byte{}

// fakeBuilds are the builds TestMain makes, as name and version.
var fakeBuilds = [][2]string{
	{"csv", "0.1.0"}, {"csv", "0.2.0"}, {"csv", "0.9.0"}, {"csv", "0.10.0"},
	{"other", "0.1.0"}, {"nginx", "0.3.0"}, {"nginx", "0.4.0"}, {"-", "0.1.0"}, {"-", "0.5.0"}, {"-", "../.."},
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "errata-install-fakes-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	err = buildFakes(dir)
	code := 1
	if err == nil {
		code = m.Run()
	} else {
		fmt.Fprintln(os.Stderr, err)
	}
	os.RemoveAll(dir)
	os.Exit(code)
}

// buildFakes builds each of fakeBuilds in dir into fakes.
func buildFakes(dir string) error {
	for i, b := range fakeBuilds {
		out := filepath.Join(dir, strconv.Itoa(i))
		flags := "-X main.version=" + b[1]
		if b[0] != "-" {
			flags += " -X main.fixedName=" + b[0]
		}
		cmd := exec.CommandContext(context.Background(), "go", "build", "-ldflags", flags, "-o", out, "../testdata/fake")
		if msg, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("building the fake %v: %w\n%s", b, err, msg)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			return err
		}
		fakes[b[0]+" "+b[1]] = data
	}
	return nil
}

// fake is the fake built for name at version.
func fake(t *testing.T, name, version string) []byte {
	t.Helper()
	data, ok := fakes[name+" "+version]
	if !ok {
		t.Fatalf("no fake of %s %s is built", name, version)
	}
	return data
}

// this is the platform the tests run on.
var this = plugin.Platform()

// other is a platform that is not this.
func other() string {
	if plugin.Platforms[0] == this {
		return plugin.Platforms[1]
	}
	return plugin.Platforms[0]
}

// seen is one request the stand in for the network was sent.
type seen struct {
	host, path, auth string
}

// world is a repository, a cache, and one TLS server standing in for every
// host: api.github.com, github.com and the mirror mirror.example, which
// serves the nested layout with a static file server, as a mirror's web
// server does. It records each request with its host and its token.
type world struct {
	t      *testing.T
	root   string
	cache  string
	mirror string
	in     *Installer
	out    bytes.Buffer

	mu       sync.Mutex
	assets   map[string][]byte // github.com's path, escaped, to its bytes
	releases []apiRelease
	requests []seen
	// redirect, when set, answers every download with a redirect to it,
	// and apiRedirect every request to the API.
	redirect, apiRedirect string
	// apiStatus, when set, is the API's answer to every request.
	apiStatus int
	// stall, when set, is how github.com stalls on a checksums file:
	// "header", sending no
	// answer; "body", sending the answer and a byte of its body; "slow",
	// sending the whole body a byte at a time, each after a pause.
	stall string
	pause time.Duration
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, root: t.TempDir(), cache: t.TempDir(), mirror: t.TempDir(), assets: map[string][]byte{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(w.serve))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "https://")
	base := srv.Client().Transport
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		w.mu.Lock()
		w.requests = append(w.requests, seen{host: r.URL.Host, path: r.URL.EscapedPath(), auth: r.Header.Get("Authorization")})
		w.mu.Unlock()
		r2 := r.Clone(r.Context())
		r2.Host = r.URL.Host
		r2.URL.Host = addr
		return base.RoundTrip(r2)
	})}
	w.in = &Installer{Root: w.root, Cache: w.cache, Client: client, Token: "a-ci-token", Builtin: []string{"yaml", "json"}, Platform: this, Out: &w.out}
	return w
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// serve answers as the host the request names.
func (w *world) serve(rw http.ResponseWriter, r *http.Request) {
	if mode, pause := w.stalls(); mode != "" && r.Host == "github.com" && strings.HasSuffix(r.URL.Path, "_checksums.txt") {
		w.serveStalling(rw, r, mode, pause)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.redirect != "" && r.Host != apiHost:
		http.Redirect(rw, r, w.redirect, http.StatusFound)
	case w.apiRedirect != "" && r.Host == apiHost:
		http.Redirect(rw, r, w.apiRedirect, http.StatusFound)
	case w.apiStatus != 0 && r.Host == apiHost:
		http.Error(rw, "rate limited", w.apiStatus)
	case r.Host == apiHost:
		w.servePage(rw, r)
	case r.Host == "github.com":
		data, ok := w.assets[r.URL.EscapedPath()]
		if !ok {
			http.NotFound(rw, r)
			return
		}
		rw.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = rw.Write(data)
	case r.Host == "mirror.example":
		http.FileServer(http.Dir(w.mirror)).ServeHTTP(rw, r)
	default:
		http.NotFound(rw, r)
	}
}

// stalls is how github.com stalls, and its pause.
func (w *world) stalls() (string, time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stall, w.pause
}

// serveStalling answers r as github.com does when it stalls as mode says,
// until the client gives up.
func (w *world) serveStalling(rw http.ResponseWriter, r *http.Request, mode string, pause time.Duration) {
	w.mu.Lock()
	data, ok := w.assets[r.URL.EscapedPath()]
	w.mu.Unlock()
	if !ok {
		http.NotFound(rw, r)
		return
	}
	if mode == "header" {
		<-r.Context().Done()
		return
	}
	rw.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if mode == "silent" {
		rw.WriteHeader(http.StatusOK)
		rw.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	for i := range data {
		if i > 0 && mode == "body" {
			<-r.Context().Done()
			return
		}
		if mode == "slow" {
			time.Sleep(pause)
		}
		if _, err := rw.Write(data[i : i+1]); err != nil {
			return
		}
		rw.(http.Flusher).Flush()
	}
}

// servePage answers a page of the releases API.
func (w *world) servePage(rw http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.URL.Query().Get("per_page"))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	start := min((page-1)*n, len(w.releases))
	end := min(start+n, len(w.releases))
	if err := json.NewEncoder(rw).Encode(w.releases[start:end]); err != nil {
		w.t.Error(err)
	}
}

// build is the bytes a release holds for platform: the fake of name at
// version for this platform, and for another any bytes, never run.
func (w *world) build(name, version, platform string) []byte {
	if platform == this {
		return fake(w.t, name, version)
	}
	return []byte("the build of " + name + " " + version + " for " + platform)
}

// publish makes a release of csv at version on github.com and in the
// mirror's directory, with a build for each of platforms, the notices
// file when notices is not empty, and its checksums file, and lists it in
// the API. It returns the checksums.
func (w *world) publish(version string, platforms []string, notices string) map[string]string {
	w.t.Helper()
	name := "csv"
	files := map[string][]byte{}
	sums := map[string]string{}
	var lines []string
	for _, p := range platforms {
		data := w.build(name, version, p)
		files[assetName(name, p)] = data
		sums[p] = sumOf(data)
		lines = append(lines, sums[p]+"  "+assetName(name, p))
	}
	if notices != "" {
		files[noticesName(name)] = []byte(notices)
		lines = append(lines, sumOf([]byte(notices))+"  "+noticesName(name))
	}
	files[checksumsName(name)] = []byte(strings.Join(lines, "\n") + "\n")
	r := release{name: name, version: version}
	for asset, data := range files {
		w.put(r, asset, data)
	}
	w.list(apiRelease{Tag: r.tag()})
	return sums
}

// put serves data as asset of r, on github.com and in the mirror.
func (w *world) put(r release, asset string, data []byte) {
	w.t.Helper()
	w.mu.Lock()
	w.assets["/f-klement/errata/releases/download/"+url.PathEscape(r.tag())+"/"+url.PathEscape(asset)] = data
	w.mu.Unlock()
	w.putMirror(r, asset, data)
}

// putMirror writes data as asset of r in the mirror alone.
func (w *world) putMirror(r release, asset string, data []byte) {
	w.t.Helper()
	path := filepath.Join(w.mirror, filepath.FromSlash(r.tag()), asset)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		w.t.Fatal(err)
	}
}

// list adds releases to the API's list, after those it holds.
func (w *world) list(rs ...apiRelease) {
	w.mu.Lock()
	w.releases = append(w.releases, rs...)
	w.mu.Unlock()
}

// install runs Install with req, and fails the test on an error.
func (w *world) install(req Request) {
	w.t.Helper()
	if err := w.in.Install(context.Background(), req); err != nil {
		w.t.Fatalf("install %+v: %v", req, err)
	}
}

// refused runs Install with req and returns its error, failing the test
// when there is none.
func (w *world) refused(req Request) string {
	w.t.Helper()
	err := w.in.Install(context.Background(), req)
	if err == nil {
		w.t.Fatalf("install %+v did not fail; it printed %s", req, w.out.String())
	}
	return err.Error()
}

// lock is the repository's lock file, "" when there is none.
func (w *world) lock() string {
	w.t.Helper()
	data, err := os.ReadFile(filepath.Join(w.root, plugin.LockFile))
	if err != nil && !os.IsNotExist(err) {
		w.t.Fatal(err)
	}
	return string(data)
}

// writeLock writes text as the repository's lock file.
func (w *world) writeLock(text string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.root, plugin.LockFile), []byte(text), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

// cached is the path of a build in the cache.
func (w *world) cached(name, version, platform, sum string) string {
	return plugin.BuildPath(plugin.CacheIn(w.cache), name, version, platform, sum)
}

// vendored is the path of a build in the vendored directory.
func (w *world) vendored(name, version, platform, sum string) string {
	return plugin.BuildPath(filepath.Join(w.root, filepath.FromSlash(plugin.VendorDir)), name, version, platform, sum)
}

// requestsSince are the requests the world was sent after the first n.
func (w *world) requestsSince(n int) []seen {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]seen(nil), w.requests[n:]...)
}

// hosts are the hosts the world was sent a request for, in order.
func (w *world) hosts() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, r := range w.requests {
		out = append(out, r.host)
	}
	return out
}

// lockOf is the lock file that pins csv 0.1.0 from a release for
// each platform of sums.
func lockOf(sums map[string]string) string {
	name, version := "csv", "0.1.0"
	var b strings.Builder
	for _, p := range plugin.Platforms {
		if s, ok := sums[p]; ok {
			b.WriteString(name + " " + version + " " + p + " sha256:" + s + "\n")
		}
	}
	return b.String()
}

// noTemp fails the test when a temporary file of an install is left below
// dir.
func noTemp(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".install-") {
			t.Errorf("a temporary file is left at %s", path)
		}
		return nil
	})
}

// exists reports whether path is there.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
