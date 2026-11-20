package plugin

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// LockFile is the lock file's name in the repository's root.
const LockFile = "errata.lock"

// maxLockBytes is the largest lock file errata reads. A line pins one build
// of one plugin in about 120 bytes, so this holds thousands of them.
const maxLockBytes = 1 << 20

// Pin is what the lock file says of one plugin: its name, the one version a
// run uses, the sha256 of its executable for each platform, as Go names a
// platform (linux/amd64), in lower case hex, and where the builds came
// from.
//
// Source is one of SourceRelease, SourceFile and SourceURL, the same for
// every platform: a release of this repository, tagged
// errata-ext-<name>/v<version>, which a line leaves unsaid; a file the user
// named (source=file); or a URL the user gave with its sum
// (source=url:<url>), one URL for each platform, in URLs. A mirror that
// took the release's place is not recorded, so the lock reads the same
// wherever a build was fetched from. A run checks every build against its
// sum alike, whatever its source.
type Pin struct {
	Name    string
	Version string
	Sums    map[string]string
	Source  string
	URLs    map[string]string
}

// The sources of a Pin.
const (
	SourceRelease = "release"
	SourceFile    = "file"
	SourceURL     = "url"
)

// readSource reads a line's fifth field, or the release for none: the
// source and, for a URL, the URL.
func readSource(fields []string) (source, at string, err error) {
	if len(fields) == 0 {
		return SourceRelease, "", nil
	}
	v, ok := strings.CutPrefix(fields[0], "source=")
	if ok && v == SourceFile {
		return SourceFile, "", nil
	}
	if at, isURL := strings.CutPrefix(v, SourceURL+":"); ok && isURL {
		return SourceURL, at, checkURL(at)
	}
	return "", "", fmt.Errorf("the field %s is no source: write source=file or source=url:<url>, or leave it out for a release", strconv.Quote(fields[0]))
}

// checkURL refuses a source URL that is not https with a host, or that
// carries user info: a lock is committed, so a token in it would be
// published, and the install that reads it fetches from the URL in every
// CI run (ADR 0021).
func checkURL(at string) error {
	u, err := url.Parse(at)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("the source URL %s is no https URL: write source=url:https://<host>/<path>", strconv.Quote(at))
	}
	if u.User != nil {
		return fmt.Errorf("the source URL for %s holds a user name or password, which a committed lock would publish: write the URL without it", u.Host)
	}
	return nil
}

// Platforms are the platforms errata ships for, as the lock names them;
// a line names one of them.
var Platforms = []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"}

// Lock is the plugins the lock file pins, in the order of their first line,
// and Header, the lines before the first of them that are blank or
// comments, as the file has them. errata plugin install writes Header back
// as it was and drops every other comment (owner, 2026-11-20).
type Lock struct {
	Header []string
	Pins   []Pin
}

// Pin is the pin of the plugin name, and whether the lock has one.
func (l Lock) Pin(name string) (Pin, bool) {
	i := slices.IndexFunc(l.Pins, func(p Pin) bool { return p.Name == name })
	if i < 0 {
		return Pin{}, false
	}
	return l.Pins[i], true
}

// ReadLock reads the lock file in root. A repository with no lock file pins
// no plugin, so a missing file is an empty lock; one errata cannot read, or
// one past maxLockBytes, is an error.
func ReadLock(root string) (Lock, error) {
	f, err := os.Open(filepath.Join(root, LockFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Lock{}, nil
	}
	if err != nil {
		return Lock{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxLockBytes+1))
	if err != nil {
		return Lock{}, err
	}
	if len(data) > maxLockBytes {
		return Lock{}, fmt.Errorf("%s: the file is over %d bytes, the largest lock file errata reads", LockFile, maxLockBytes)
	}
	return ParseLock(data)
}

// ParseLock reads a lock file's bytes. Each line that is not blank and does
// not start with # pins one build of one plugin, in four fields and an
// optional fifth:
//
//	csv 0.1.0 linux/amd64 sha256:<64 hex digits>
//	nginx 0.3.0 linux/amd64 sha256:<64 hex digits> source=file
//
// the plugin's name, its version, the platform, the sha256 of the
// executable, and where it came from when that is not a release of this
// repository (Pin). Every line of one name names the same version and the
// same kind of source, and no platform twice. A line that breaks any of
// this is refused with its number, and so is the whole file. The blank and
// comment lines before the first line that pins a build are the Header.
func ParseLock(data []byte) (Lock, error) {
	var l Lock
	at := map[string]int{}
	for i, line := range lockLines(data) {
		text := strings.TrimSpace(string(line))
		if text == "" || strings.HasPrefix(text, "#") {
			l.keepHeader(line)
			continue
		}
		if err := l.add(text, at); err != nil {
			return Lock{}, fmt.Errorf("%s:%d: %w", LockFile, i+1, err)
		}
	}
	return l, nil
}

// lockLines are the lines of a lock file's data, with no empty line after
// its last newline.
func lockLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	return bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
}

// keepHeader adds line, a blank or comment line, to l's Header while no
// line has pinned a build yet.
func (l *Lock) keepHeader(line []byte) {
	if len(l.Pins) == 0 {
		l.Header = append(l.Header, string(line))
	}
}

// add reads one line of the lock into l. at is where each name's pin is in
// l.Pins.
func (l *Lock) add(text string, at map[string]int) error {
	ln, err := parseLine(text)
	if err != nil {
		return err
	}
	i, seen := at[ln.name]
	if !seen {
		at[ln.name] = len(l.Pins)
		l.Pins = append(l.Pins, Pin{Name: ln.name, Version: ln.version, Sums: map[string]string{}, Source: ln.source, URLs: map[string]string{}})
		i = len(l.Pins) - 1
	}
	return l.Pins[i].merge(ln)
}

// lockLine is one line of the lock: one build of a plugin.
type lockLine struct {
	name, version, platform, sum, source, url string
}

// parseLine reads one line of the lock, refusing a field that is not what
// its place asks for.
func parseLine(text string) (lockLine, error) {
	f := strings.Fields(text)
	if len(f) != 4 && len(f) != 5 {
		return lockLine{}, fmt.Errorf("%s holds %d fields; a line is a name, a version, a platform and a checksum, such as csv 0.1.0 linux/amd64 sha256:<64 hex digits>, and a source when it is not a release", strconv.Quote(text), len(f))
	}
	ln := lockLine{name: f[0], version: f[1], platform: f[2]}
	var err error
	if ln.source, ln.url, err = readSource(f[4:]); err != nil {
		return lockLine{}, err
	}
	if err := CheckName(ln.name); err != nil {
		return lockLine{}, err
	}
	if err := CheckVersion(ln.version); err != nil {
		return lockLine{}, fmt.Errorf("%s: %w", ln.name, err)
	}
	if !slices.Contains(Platforms, ln.platform) {
		return lockLine{}, fmt.Errorf("the platform %s is none errata ships for: write one of %s", strconv.Quote(ln.platform), strings.Join(Platforms, ", "))
	}
	ln.sum, err = readSum(f[3])
	return ln, err
}

// readSum is the hex digits of sum, a checksum as the lock writes it:
// sha256: and 64 lower case hex digits.
func readSum(sum string) (string, error) {
	hexSum, ok := strings.CutPrefix(sum, "sha256:")
	if b, err := hex.DecodeString(hexSum); !ok || err != nil || len(b) != 32 || hexSum != strings.ToLower(hexSum) {
		return "", fmt.Errorf("the checksum %s is no checksum: write sha256: and 64 lower case hex digits", strconv.Quote(sum))
	}
	return hexSum, nil
}

// merge adds the build ln pins to p, refusing a line that disagrees with
// an earlier line of the same plugin.
func (p *Pin) merge(ln lockLine) error {
	if p.Version != ln.version {
		return fmt.Errorf("%s is pinned at %s on an earlier line and at %s here; a run uses one version of a plugin", p.Name, p.Version, ln.version)
	}
	if p.Source != ln.source {
		return fmt.Errorf("%s has another source here than on an earlier line; every build of one version comes from one kind of source", p.Name)
	}
	if _, dup := p.Sums[ln.platform]; dup {
		return fmt.Errorf("%s pins %s twice; write one checksum for each platform", p.Name, ln.platform)
	}
	if p.Source == SourceFile && len(p.Sums) > 0 {
		return fmt.Errorf("%s pins a file the user named for more than one platform; a file is the build of one platform", p.Name)
	}
	p.Sums[ln.platform] = ln.sum
	p.addURL(ln.platform, ln.url)
	return nil
}

// addURL records url as where the build for platform came from, when the
// pin's source is a URL.
func (p *Pin) addURL(platform, url string) {
	if url != "" {
		p.URLs[platform] = url
	}
}

// CheckVersion refuses a version that is not semantic versioning 2.0.0, or
// is longer than 64 bytes, with no v before it: 0.1.0, 1.2.0-rc.1. A version
// is a part of the path errata looks for a build at, so one such as ../..
// never reaches a path.
func CheckVersion(version string) error {
	if len(version) > 64 || !versionPattern.MatchString(version) {
		return fmt.Errorf("the version %s is no semantic version: write the release's version, such as 0.1.0, with no v", strconv.Quote(version))
	}
	return nil
}

var (
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	// versionPattern is semantic versioning 2.0.0's own expression
	// (semver.org), without its named groups.
	versionPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)
)

// CheckName refuses a plugin name the protocol does not allow: 1 to 64
// bytes of lower case ASCII letters, digits, _ and -, starting with a
// letter.
func CheckName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("the plugin name %s is not 1 to 64 lower case letters, digits, _ and -, starting with a letter", strconv.Quote(name))
	}
	return nil
}

// Put sets the pin of p's name to p, in its place when the lock pins that
// name already, else after the others.
func (l *Lock) Put(p Pin) {
	i := slices.IndexFunc(l.Pins, func(q Pin) bool { return q.Name == p.Name })
	if i < 0 {
		l.Pins = append(l.Pins, p)
		return
	}
	l.Pins[i] = p
}

// Format is the lock file that pins what l pins, as ParseLock reads it: a
// line for each build, the plugins in l's order and each plugin's
// platforms in the order of Platforms, after the lines of l's Header. A
// comment between the lines that pin builds is not kept.
func (l Lock) Format() []byte {
	var b strings.Builder
	for _, line := range l.Header {
		b.WriteString(line + "\n")
	}
	for _, p := range l.Pins {
		for _, platform := range Platforms {
			if sum, ok := p.Sums[platform]; ok {
				b.WriteString(p.Name + " " + p.Version + " " + platform + " sha256:" + sum + p.sourceField(platform) + "\n")
			}
		}
	}
	return []byte(b.String())
}

// sourceField is the fifth field of p's line for platform, after its
// space, or "" for a release.
func (p Pin) sourceField(platform string) string {
	switch p.Source {
	case SourceFile:
		return " source=file"
	case SourceURL:
		return " source=url:" + p.URLs[platform]
	}
	return ""
}

// WriteLock writes l as the lock file in root, through a temporary file
// beside it renamed over it, so the lock is the old file or the whole new
// one, never a part. It refuses a lock that ParseLock would not read back
// as l, before it writes anything.
func WriteLock(root string, l Lock) error {
	data := l.Format()
	back, err := ParseLock(data)
	if err != nil {
		return err
	}
	if !slices.Equal(back.Header, l.Header) || !slices.EqualFunc(back.Pins, l.Pins, Pin.same) {
		return fmt.Errorf("%s would not read back as the pins errata means to write", LockFile)
	}
	return writeRenamed(filepath.Join(root, LockFile), data)
}

// same reports whether p and q pin the same builds from the same source.
func (p Pin) same(q Pin) bool {
	return p.Name == q.Name && p.Version == q.Version && p.Source == q.Source && maps.Equal(p.Sums, q.Sums) && maps.Equal(p.URLs, q.URLs)
}

// writeRenamed writes data to path by a temporary file in its directory,
// synced and renamed over it. The file keeps the mode of the one it
// replaces, and a new one is 0644, so a lock is readable as a committed
// file is.
func writeRenamed(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := errors.Join(writeSynced(f, data, modeOf(path)), f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// writeSynced gives f mode, writes data to it and syncs it.
func writeSynced(f *os.File, data []byte, mode fs.FileMode) error {
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// modeOf is the permission bits of the file at path, or 0644 for none.
func modeOf(path string) fs.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return 0o644
}
