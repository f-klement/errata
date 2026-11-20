package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/plugin"
	"github.com/f-klement/errata/internal/rule"
)

// pluginRepo is a repository with the config text, the files, and the
// fake plugin of package plugin's tests vendored as fake and pinned.
// Without vendored, the lock pins fake and nothing holds it.
func pluginRepo(t *testing.T, text string, files map[string]string, vendored bool) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	sum := strings.Repeat("a", 64)
	if vendored {
		bin := filepath.Join(t.TempDir(), "fake")
		if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "../plugin/testdata/fake").CombinedOutput(); err != nil {
			t.Fatalf("building the fake plugin: %v\n%s", err, out)
		}
		data, err := os.ReadFile(bin)
		if err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(data)
		sum = hex.EncodeToString(s[:])
		path := plugin.BuildPath(filepath.Join(root, plugin.VendorDir), "fake", "0.1.0", plugin.Platform(), sum)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files[plugin.LockFile] = "fake 0.1.0 " + plugin.Platform() + " sha256:" + sum + "\n"
	files[DefaultFile] = text
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// runAll loads root's config and runs each check.
func runAll(t *testing.T, root string) []check.Result {
	t.Helper()
	cfg, err := Load(t.Context(), root, filepath.Join(root, DefaultFile))
	if err != nil {
		t.Fatal(err)
	}
	var out []check.Result
	for _, c := range cfg.Checks {
		out = append(out, rule.Run(context.Background(), root, c))
	}
	return out
}

const pluginConfig = `
checks:
  - id: port-agrees
    reason: r
    scope: repo
    extract:
      plug: {fake: {file: a.txt, word: "port:", tags: {z: "1", a: "2"}}}
      chart: {yaml: {file: b.yaml, path: port}}
    agree: [plug, chart]
`

// A config's entry under a key the lock pins is the plugin's, and its rule
// runs with the plugin's values like a built in kind's. Planted break:
// readPlugins skipping every key, which refused fake as an unknown field.
func TestARuleRunsWithAPluginsValues(t *testing.T) {
	root := pluginRepo(t, pluginConfig, map[string]string{"a.txt": "port: 8080\n", "b.yaml": "port: 8080\n"}, true)
	if res := runAll(t, root); res[0].Verdict != check.VerdictTrue {
		t.Fatalf("got %+v", res[0])
	}
	root = pluginRepo(t, pluginConfig, map[string]string{"a.txt": "port: 8081\n", "b.yaml": "port: 8080\n"}, true)
	if res := runAll(t, root); res[0].Verdict != check.VerdictFalse {
		t.Fatalf("got %+v", res[0])
	}
}

// A rule that needs a plugin no one installed loads, and is unknown with
// the plugin named. Planted break: an unusable plugin's entry refused at
// load, which failed Load.
func TestARuleWhosePluginIsNotInstalledIsUnknown(t *testing.T) {
	root := pluginRepo(t, pluginConfig, map[string]string{"a.txt": "port: 8080\n", "b.yaml": "port: 8080\n"}, false)
	res := runAll(t, root)[0]
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, "plugin errata-ext-fake 0.1.0 is not installed") {
		t.Fatalf("got %+v", res)
	}
}

// An extractor that only an unusable plugin's entry reads is not refused
// as unused: errata cannot ask the plugin which extractors it reads, so the
// config loads and the rule is unknown, naming the plugin. Planted break:
// describe leaving ReadsAny unset, which refused other and failed Load.
func TestAnExtractorOnlyAnUnusablePluginReadsLoads(t *testing.T) {
	text := `
checks:
  - id: reads-other
    reason: r
    scope: repo
    extract:
      plug: {fake: {file: a.txt, word: "port:", mode: reads}}
      other: {yaml: {file: b.yaml, path: port}}
    assert: size(plug) > 0
`
	root := pluginRepo(t, text, map[string]string{"a.txt": "port: 8080\n", "b.yaml": "port: 8080\n"}, false)
	res := runAll(t, root)[0]
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, "plugin errata-ext-fake 0.1.0 is not installed") {
		t.Fatalf("got %+v", res)
	}
}

// Load runs the plugins under the caller's context, so a run stopped
// while they describe and build stops them, and the rule says the run was
// cancelled. Planted break: parseWith reading plugins under
// context.Background(), which ran the plugin to the end.
func TestLoadStopsThePluginsWithItsContext(t *testing.T) {
	root := pluginRepo(t, pluginConfig, map[string]string{"a.txt": "port: 8080\n", "b.yaml": "port: 8080\n"}, true)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cfg, err := Load(ctx, root, filepath.Join(root, DefaultFile))
	if err != nil {
		t.Fatal(err)
	}
	res := rule.Run(t.Context(), root, cfg.Checks[0])
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, "cancelled: errata's run stopped before plugin errata-ext-fake 0.1.0 finished") {
		t.Fatalf("got %+v", res)
	}
}

// An entry of a plugin's kind is read as a built in kind's: a key in
// another case is refused with the spelling it must have, not only as a key
// the plugin does not take, and a string field written as what YAML 1.1
// reads as a bool is refused.
// Planted breaks: pluginJSON skipping exactKeys; skipping stringWalk.
func TestAPluginEntryIsReadAsABuiltInOne(t *testing.T) {
	for entry, want := range map[string]string{
		`{file: a.txt, Word: x}`:   `key "Word" must be spelled "word"`,
		`{file: a.txt, word: yes}`: `YAML 1.1 reads yes`,
	} {
		text := strings.Replace(pluginConfig, `{file: a.txt, word: "port:", tags: {z: "1", a: "2"}}`, entry, 1)
		root := pluginRepo(t, text, map[string]string{"a.txt": "", "b.yaml": ""}, true)
		if _, err := Load(t.Context(), root, filepath.Join(root, DefaultFile)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %s", entry, err, want)
		}
	}
}

// A map field reaches the plugin in the order the config writes it, not
// sorted, and through an alias as through its anchor. Planted breaks:
// inNodeOrder returning the JSON as it is; expand leaving the alias.
func TestAMapFieldKeepsTheConfigsOrder(t *testing.T) {
	root := pluginRepo(t, pluginConfig, map[string]string{"a.txt": "", "b.yaml": ""}, true)
	set, err := plugin.Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("x: &t {z: '1', a: '2'}\ny: {file: a.txt, word: w, tags: *t}\n"), &doc); err != nil {
		t.Fatal(err)
	}
	n := valueOf(doc.Content[0], "y")
	spec, err := decodePlugin(set.Kind(context.Background(), "fake"), nil, n, "p")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(spec)
	if string(raw) != `{"file":"a.txt","word":"w","tags":{"z":"1","a":"2"}}` {
		t.Fatalf("spec %s", raw)
	}
}
