package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/plugin"
)

// fakePlugin builds package plugin's fake plugin, named by its file, into
// dir as errata-ext-<name>, and returns its path and its sha256.
func fakePlugin(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	path := filepath.Join(dir, plugin.Executable(name, strings.Split(plugin.Platform(), "/")[0]))
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", path, "../plugin/testdata/fake")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the fake plugin: %v\n%s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(data)
	return path, hex.EncodeToString(s[:])
}

// errata plugin install --file installs into the user's cache and pins
// the build in the root's errata.lock; with no name, it reads the plugin
// source from ERRATA_PLUGIN_SOURCE, and a file URL there names a mirror
// with no network; a plugin it cannot install exits 2 and names it.
// A bad ERRATA_PLUGIN_SOURCE is named by the variable. Planted breaks: the
// install command reading no ERRATA_PLUGIN_SOURCE; --root ignored; the
// command not saying where the source came from.
func TestPluginInstallInstallsAndPins(t *testing.T) {
	// Built before XDG_CACHE_HOME moves, which would move go's build cache
	// with it.
	path, sum := fakePlugin(t, t.TempDir(), "nginx")
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	root := t.TempDir()
	var out, errs bytes.Buffer
	if code := Execute(t.Context(), []string{"plugin", "install", "--root", root, "--file", path}, &out, &errs); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	lock, _ := os.ReadFile(filepath.Join(root, plugin.LockFile))
	if want := "nginx 0.1.0 " + plugin.Platform() + " sha256:" + sum + " source=file\n"; string(lock) != want {
		t.Fatalf("errata.lock is %q, want %q", lock, want)
	}
	base, err := plugin.CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plugin.BuildPath(base, "nginx", "0.1.0", plugin.Platform(), sum)); err != nil || !strings.HasPrefix(base, cache) {
		t.Fatalf("the build is not in the cache at %s: %v", base, err)
	}
	mirror := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, plugin.LockFile), []byte("csv 0.1.0 "+plugin.Platform()+" sha256:"+sum+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SourceEnv, "file://"+filepath.ToSlash(mirror))
	errs.Reset()
	if code := Execute(t.Context(), []string{"plugin", "install", "--root", root}, &out, &errs); code != ExitUsage || !strings.Contains(errs.String(), filepath.Join(mirror, "errata-ext-csv", "v0.1.0", "errata-ext-csv_checksums.txt")) {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	t.Setenv(SourceEnv, "http://m.example")
	errs.Reset()
	if code := Execute(t.Context(), []string{"plugin", "install", "--root", root}, &out, &errs); code != ExitUsage || !strings.Contains(errs.String(), SourceEnv+` "http://m.example" is no https URL`) {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
}
