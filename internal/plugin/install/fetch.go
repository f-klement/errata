package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// maxDownload is the most errata reads of one download, a build, a
// notices file or a page of the releases API: 256 MiB (ADR 0021). A test
// lowers it.
var maxDownload int64 = 256 << 20

// apiHost is the one host GITHUB_TOKEN goes to.
const apiHost = "api.github.com"

// headerTimeout is how long errata waits for a server's answer, and
// idleTimeout how long for each read of its body. No bound covers a whole
// download, so a slow link still fetches 256 MiB, but a server that stalls
// fails the install rather than holding a CI job to its own limit. A test
// lowers them.
var (
	headerTimeout = 30 * time.Second
	idleTimeout   = 60 * time.Second
)

// errStalled is the cause of a request errata cancelled because the server
// sent nothing for too long.
var errStalled = errors.New("the server sent nothing")

// stalled is the cause of a request to raw that waited longer than wait.
func stalled(raw string, wait time.Duration) error {
	return fmt.Errorf("GET %s: %w for %s, so errata gave up", raw, errStalled, wait)
}

// causeOf is err, or the stall that cancelled ctx when one did.
func causeOf(ctx context.Context, err error) error {
	if c := context.Cause(ctx); errors.Is(c, errStalled) {
		return c
	}
	return err
}

// download is one file errata fetches: where from, for a message, how to
// open it, and the check of its sha256.
type download struct {
	from  string
	open  func() (io.ReadCloser, error)
	check func(sum string) error
}

// fromURL is the download of raw, an https or a file URL, whose sha256
// must be want, as wantFrom gives it.
func (j *job) fromURL(ctx context.Context, raw, want, wantFrom string) download {
	return download{
		from:  raw,
		open:  func() (io.ReadCloser, error) { return j.open(ctx, raw) },
		check: matches(raw, want, wantFrom),
	}
}

// fromFile is the download of the file at path, whose sha256 must be want.
func fromFile(path, want, wantFrom string) download {
	return download{
		from:  path,
		open:  func() (io.ReadCloser, error) { return os.Open(path) },
		check: matches(path, want, wantFrom),
	}
}

// anySum is the check of a build whose sha256 nothing gives before it is
// read: a file the user names, which is pinned at the sum errata reads.
func anySum(string) error { return nil }

// fromBytes is a download of data, which errata carries itself.
func fromBytes(name string, data []byte) download {
	return download{
		from:  name,
		open:  func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
		check: matches(name, sumOf(data), "errata"),
	}
}

// matches is the check that a download from has the sha256 want, which
// wantFrom gives.
func matches(from, want, wantFrom string) func(string) error {
	return func(got string) error {
		if got != want {
			return fmt.Errorf("%s has the sha256 %s, and %s gives %s, so errata keeps nothing of it", from, got, wantFrom, want)
		}
		return nil
	}
}

// sumOf is the sha256 of data, in hex.
func sumOf(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// open opens raw: a file URL's file, whose path errata decodes, so a tag's
// %2F reads as the nested directory; or an https URL, fetched with no
// token.
func (j *job) open(ctx context.Context, raw string) (io.ReadCloser, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "file" {
		return os.Open(localPath(u.Path, runtime.GOOS))
	}
	return j.in.get(ctx, raw, false)
}

// localPath is the file a file URL's decoded path names on goos: on
// Windows, /C:/x is C:\x.
func localPath(path, goos string) string {
	if goos == "windows" && len(path) > 2 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path)
}

// get fetches raw over https and returns its body once the server answered
// 200 OK with no more than maxDownload bytes announced. api marks a
// request to the releases API, which says which version of the API it
// reads and carries GITHUB_TOKEN when it is set and the host is
// api.github.com. Nothing else carries the token: a mirror, a release's
// download and a URL entry, whose URL a pull request can change, get none.
//
// A server that sends no answer within headerTimeout, or nothing of the
// body for idleTimeout, has the request cancelled.
func (in *Installer) get(ctx context.Context, raw string, api bool) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	resp, err := in.answer(ctx, cancel, raw, api)
	if err != nil {
		err = causeOf(ctx, err)
		cancel(nil)
		return nil, err
	}
	return watchBody(ctx, cancel, raw, resp.Body), nil
}

// answer sends the request for raw and waits headerTimeout at most for an
// answer errata reads, cancelling ctx with the stall when there is none.
func (in *Installer) answer(ctx context.Context, cancel context.CancelCauseFunc, raw string, api bool) (*http.Response, error) {
	req, err := in.request(ctx, raw, api)
	if err != nil {
		return nil, err
	}
	wait := time.AfterFunc(headerTimeout, func() { cancel(stalled(raw, headerTimeout)) })
	resp, err := in.client().Do(req)
	wait.Stop()
	if err != nil {
		return nil, err
	}
	if err := answered(raw, resp, api); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

// request is the GET of raw, with the token only for the releases API on
// api.github.com.
func (in *Installer) request(ctx context.Context, raw string, api bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	if api && in.Token != "" && req.URL.Host == apiHost {
		req.Header.Set("Authorization", "Bearer "+in.Token)
	}
	return req, nil
}

// idleBody is a response's body that cancels its request when a read
// waits longer than idleTimeout.
type idleBody struct {
	body   io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	timer  *time.Timer
}

// watchBody is body, whose request ctx is, cancelled by cancel when
// nothing of it arrives for idleTimeout.
func watchBody(ctx context.Context, cancel context.CancelCauseFunc, raw string, body io.ReadCloser) *idleBody {
	return &idleBody{body: body, ctx: ctx, cancel: cancel, timer: time.AfterFunc(idleTimeout, func() { cancel(stalled(raw, idleTimeout)) })}
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	b.timer.Reset(idleTimeout)
	if err != nil && !errors.Is(err, io.EOF) {
		err = causeOf(b.ctx, err)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	b.cancel(nil)
	return b.body.Close()
}

// answered refuses a response that is not 200 OK, or announces more than
// maxDownload bytes. A refusal by the releases API for the rate it limits
// a request with no token to says how to get past it.
func answered(raw string, resp *http.Response, api bool) error {
	limited := resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests
	switch {
	case api && limited:
		return fmt.Errorf("GET %s: %s; GitHub limits the requests of a client with no token: set GITHUB_TOKEN, or give the version, as <name>@<version>, which needs no request to the API", raw, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("GET %s: %s", raw, resp.Status)
	}
	if resp.ContentLength > maxDownload {
		return fmt.Errorf("GET %s: it is %d bytes, over %d, the most errata downloads in one file", raw, resp.ContentLength, maxDownload)
	}
	return nil
}

// client is in's client with the redirect policy httpsOnly.
func (in *Installer) client() *http.Client {
	c := http.Client{}
	if in.Client != nil {
		c = *in.Client
	}
	c.CheckRedirect = httpsOnly
	return &c
}

// httpsOnly refuses a redirect to anything but https, so no plain answer
// that a proxy could rewrite stands in for a build, and stops after ten,
// as Go's client does.
func httpsOnly(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("a redirect to %s, which is not https, is refused", req.URL.Redacted())
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// readAll reads r whole, refusing more than limit bytes.
func readAll(r io.Reader, limit int64, what string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is over %d bytes, the most errata reads of it", what, limit)
	}
	return data, nil
}

// copyHashed copies r to w, at most maxDownload bytes, and returns the
// sha256 of what it copied, so the bytes errata checks are the bytes it
// wrote.
func copyHashed(w io.Writer, r io.Reader) (string, error) {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(r, maxDownload+1))
	if err != nil {
		return "", err
	}
	if n > maxDownload {
		return "", fmt.Errorf("the download is over %d bytes, the most errata downloads in one file", maxDownload)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// temp is a build, or another file, while errata writes and checks it: a
// file named as it will be kept, in a new directory of its own in the
// directory it will be kept below, so the rename that keeps it stays on
// one file system.
type temp struct {
	dir, path, sum string
}

// tempPrefix starts the name of an install's temporary directory.
const tempPrefix = ".install-"

// staleAfter is the age past which a temporary directory is one an install
// that was killed left behind, and the next install removes it.
const staleAfter = time.Hour

// sweep removes the temporary directories below base that are older than
// staleAfter at now, so a killed --vendor leaves no part of a file that a
// git add of .errata would commit for long.
func sweep(base string, now time.Time) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && e.IsDir() && strings.HasPrefix(e.Name(), tempPrefix) && now.Sub(info.ModTime()) > staleAfter {
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}

// fetch writes d into a new temporary file named name below base, which
// is below owner, and checks its sha256 as it is written. It then syncs the
// file and gives it mode, so a crash never leaves a part of a file under a
// name errata keeps. A download that fails a check is removed.
func (j *job) fetch(owner, base, name string, d download, mode fs.FileMode) (temp, error) {
	r, err := d.open()
	if err != nil {
		return temp{}, err
	}
	defer r.Close()
	if err := j.makeDir(owner, base); err != nil {
		return temp{}, err
	}
	sweep(base, time.Now())
	dir, err := os.MkdirTemp(base, tempPrefix)
	if err != nil {
		return temp{}, err
	}
	t := temp{dir: dir, path: filepath.Join(dir, name)}
	if t.sum, err = t.fill(r, d.check, mode); err != nil {
		t.remove()
		return temp{}, err
	}
	return t, nil
}

// fill writes r into t's file, checks its sha256, syncs it and gives it
// mode. It returns the sha256.
func (t temp) fill(r io.Reader, check func(string) error, mode fs.FileMode) (string, error) {
	f, err := os.OpenFile(t.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum, err := copyHashed(f, r)
	if err != nil {
		return "", err
	}
	if err := check(sum); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return sum, os.Chmod(t.path, mode)
}

// remove removes t and its directory.
func (t temp) remove() { _ = os.RemoveAll(t.dir) }

// makeDir makes dir and each directory between owner and it, and refuses
// one that is a symbolic link or no directory, so errata writes no file
// through a link to a place outside the vendored directory or the cache,
// such as one another user can write (ADR 0021). It records each directory
// it makes in the repository, which a failed install removes again.
func (j *job) makeDir(owner, dir string) error {
	rel, err := filepath.Rel(owner, dir)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("%s is not below %s", dir, owner)
	}
	at := owner
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		at = filepath.Join(at, part)
		made, err := makeOne(at)
		if err != nil {
			return err
		}
		if made && owner == j.in.Root {
			j.made = append(j.made, at)
		}
	}
	return nil
}

// makeOne makes the directory at, unless it is there, and refuses it when
// it is a symbolic link or no directory. It reports whether it made it.
func makeOne(at string) (bool, error) {
	err := os.Mkdir(at, 0o750)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return false, err
	}
	info, lerr := os.Lstat(at)
	if lerr != nil {
		return false, lerr
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s is a symbolic link or no directory, and errata writes no plugin below one: remove it", at)
	}
	return err == nil, nil
}

// unmake removes, deepest first, each directory of the repository j made
// that is still empty, so a failed --vendor leaves no empty .errata/plugins,
// which git status does not show and every later run would look in. The
// cache's directories stay: another install may be writing below them.
func (j *job) unmake() {
	for i := len(j.made) - 1; i >= 0; i-- {
		_ = os.Remove(j.made[i])
	}
}

// holds reports whether path is a regular file whose sha256 is sum.
func holds(path, sum string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == sum
}

// keepInCache renames the checked file at tmp to dst, its path in the
// cache, whose sha256 is sum, unless a good file is there already. A file
// there whose sha256 is not sum is damaged, and the checked file is
// renamed over it: the one rename over a file errata makes, and a safe
// one, since a run that hashed the damaged file refused it.
func (j *job) keepInCache(tmp, dst, sum string) error {
	if err := j.makeDir(j.in.Cache, filepath.Dir(dst)); err != nil {
		return err
	}
	if holds(dst, sum) {
		return nil
	}
	return replace(tmp, dst)
}

// replace renames tmp over dst. errata keeps a build read only, which on
// Windows is the read only attribute, and Windows renames over no read only
// file, so there dst is made writable first. Elsewhere rename needs no
// chmod, and none is done, since os.Chmod follows a link swapped in after
// the Lstat.
func replace(tmp, dst string) error {
	if runtime.GOOS == "windows" {
		if err := writable(dst); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dst)
}

// writable makes a regular file at dst writable by its owner. A link there
// is left as it is, never followed, and a missing dst is no error.
func writable(dst string) error {
	if info, err := os.Lstat(dst); err == nil && info.Mode().IsRegular() {
		if err := os.Chmod(dst, 0o600); err != nil {
			return fmt.Errorf("making the damaged %s writable to replace it: %w", dst, err)
		}
	}
	return nil
}

// errVendoredDiffers is the error of a vendored file that is not the one
// errata would write there.
var errVendoredDiffers = errors.New("errata never writes over a vendored file")

// keepVendored renames the checked file at tmp to dst, its path in the
// vendored directory, whose sha256 is sum, unless a file is there. errata
// never renames over a vendored file: one whose sha256 is sum is left as
// it is, and any other is refused, since git restores it.
func (j *job) keepVendored(tmp, dst, sum string) error {
	if err := j.makeDir(j.in.Root, filepath.Dir(dst)); err != nil {
		return err
	}
	_, err := os.Lstat(dst)
	switch {
	case err == nil && holds(dst, sum):
		return nil
	case err == nil:
		return fmt.Errorf("%s is not the file whose sha256 is %s, and %w: restore it with git, or remove it", dst, sum, errVendoredDiffers)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return os.Rename(tmp, dst)
}
