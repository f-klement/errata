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

// A live and an applied check run when the plugin they name is installed
// and provides read and predict, and a live check whose plugin provides
// render alone is skipped with a gap naming what it provides; the run
// sees the installed plugins through errata.lock. Planted break: check
// building the engine with no providers, as before #138, which skipped
// all three.
func TestAnInstalledProviderMakesItsScopeReachable(t *testing.T) {
	bin := t.TempDir()
	kube, _ := fakePlugin(t, bin, "kube")
	renderer, _ := fakePlugin(t, bin, "renderer")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	var out, errs bytes.Buffer
	for _, path := range []string{kube, renderer} {
		if code := Execute(t.Context(), []string{"plugin", "install", "--root", root, "--file", path}, &out, &errs); code != ExitOK {
			t.Fatalf("install %s: exit %d: %s", path, code, errs.String())
		}
	}
	for _, want := range []string{
		"errata-ext-kube 0.1.0 provides render, read, predict; its read and predict get FAKE_KUBE, FAKE_ABSENT from errata's environment, as errata.lock now records\n",
		"errata-ext-renderer 0.1.0 provides render\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the install does not say %q:\n%s", want, out.String())
		}
	}
	if lock, err := os.ReadFile(filepath.Join(root, "errata.lock")); err != nil || !strings.Contains(string(lock), " source=file env=FAKE_KUBE,FAKE_ABSENT\n") {
		t.Errorf("the lock does not record kube's env: %s, %v", lock, err)
	}
	config := filepath.Join(root, "errata.yaml")
	if err := os.WriteFile(config, []byte(`checks:
  - {id: live, reason: r, scope: live, provider: kube, run: ['true']}
  - {id: applied, reason: r, scope: applied, provider: kube, run: ['true']}
  - {id: rendered, reason: r, scope: live, provider: renderer, run: ['true']}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Execute(t.Context(), []string{"check", "--root", root, "--config", config}, &out, &errs); code != ExitOK {
		t.Fatalf("exit %d: %s%s", code, out.String(), errs.String())
	}
	for _, want := range []string{
		"Coverage: ran 2, skipped 1.",
		"skipped rendered (live): no installed plugin provides read: errata-ext-renderer 0.1.0 provides render\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the run does not say %q:\n%s", want, out.String())
		}
	}
}

// An install that pins a provider again says which variables its read and
// predict get that its last pin did not record. Planted break: newNames
// comparing with the new pin, which named none.
func TestAnUpgradeNamesTheVariablesItAdds(t *testing.T) {
	kube, _ := fakePlugin(t, t.TempDir(), "kube")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	old := "kube 0.0.1 " + plugin.Platform() + " sha256:" + strings.Repeat("a", 64) + " source=file env=FAKE_KUBE\n"
	if err := os.WriteFile(filepath.Join(root, "errata.lock"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := Execute(t.Context(), []string{"plugin", "install", "--root", root, "--file", kube}, &out, &errs); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if want := "as errata.lock now records; new since its last pin: FAKE_ABSENT\n"; !strings.Contains(out.String(), want) {
		t.Errorf("the install does not say %q:\n%s", want, out.String())
	}
}
