package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/cli"
	"github.com/f-klement/errata/internal/plugin"
)

// version is the release the tests build the plugin as, set the way the
// release sets it.
const version = "0.1.0"

// bin is the plugin, built once for the tests with the release's ldflags.
var bin []byte

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "errata-ext-csv-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	path := filepath.Join(dir, "errata-ext-csv")
	ldflags := "-X github.com/f-klement/errata/internal/ext/csv.Version=" + version
	build := exec.CommandContext(context.Background(), "go", "build", "-ldflags", ldflags, "-o", path, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building errata-ext-csv: %v\n%s", err, out)
		os.Exit(1)
	}
	bin, err = os.ReadFile(path)
	os.RemoveAll(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// config holds one rule: the ip of the host named builder in a CSV file
// agrees with the inventory's.
const config = `checks:
  - id: builder-ip
    reason: the inventory and the hosts table name one address for builder
    scope: repo
    extract:
      table: {csv: {file: data/hosts.csv, column: ip, where: {name: builder}}}
      inventory: {yaml: {file: inventory.yaml, path: builder.ip}}
    agree: [table, inventory]
`

const hostsCSV = "name,ip\nweb,10.0.0.6\nbuilder,10.0.0.5\n"

// repo is a git repository holding files and errata.yaml, with the plugin
// vendored at its sum and pinned in errata.lock, as errata plugin install
// --vendor will leave one.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	s := sha256.Sum256(bin)
	sum := hex.EncodeToString(s[:])
	write(t, plugin.BuildPath(filepath.Join(root, plugin.VendorDir), "csv", version, plugin.Platform(), sum), string(bin), 0o755)
	write(t, filepath.Join(root, plugin.LockFile), "csv "+version+" "+plugin.Platform()+" sha256:"+sum+"\n", 0o644)
	if _, ok := files["errata.yaml"]; !ok {
		write(t, filepath.Join(root, "errata.yaml"), config, 0o644)
	}
	for name, text := range files {
		write(t, filepath.Join(root, name), text, 0o644)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		git := exec.CommandContext(t.Context(), "git", args...)
		git.Dir = root
		git.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

func write(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

// check runs errata check in root, as a user runs it, and returns its exit
// status and what it wrote.
func check(t *testing.T, root string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := cli.Execute(t.Context(), []string{"check", "--root", root}, &out, &out)
	return code, out.String()
}

// errata runs the built plugin from the lock's pin in a repository, through
// its host, and the rule's verdict follows the cell: it holds when the
// table's address is the inventory's, and fails naming the cell's file and
// line when it is not. Planted breaks: Version left at its default, which
// the lock's 0.1.0 refused; table.line returning the row's index, which
// named the wrong line.
func TestErrataRunsThePluginFromItsPin(t *testing.T) {
	code, out := check(t, repo(t, map[string]string{"data/hosts.csv": hostsCSV, "inventory.yaml": "builder: {ip: 10.0.0.5}\n"}))
	if code != cli.ExitOK {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	code, out = check(t, repo(t, map[string]string{"data/hosts.csv": hostsCSV, "inventory.yaml": "builder: {ip: 10.0.0.9}\n"}))
	if code != cli.ExitErrata || !strings.Contains(out, "data/hosts.csv:3") || !strings.Contains(out, "10.0.0.5") {
		t.Fatalf("exit %d, want 1 naming data/hosts.csv:3 and 10.0.0.5:\n%s", code, out)
	}
}

// A file that does not parse makes the rule unknown, with the plugin's
// message naming the file and the line. Planted break: read skipping the
// rows encoding/csv refuses, which held the rule.
func TestAFileThatDoesNotParseMakesTheRuleUnknown(t *testing.T) {
	code, out := check(t, repo(t, map[string]string{"data/hosts.csv": hostsCSV + "cache,10.0.0.7,extra\n", "inventory.yaml": "builder: {ip: 10.0.0.5}\n"}))
	if code != cli.ExitErrata || !strings.Contains(out, "unknown") || !strings.Contains(out, "data/hosts.csv:4: a row of 3 cells, and the first row has 2") {
		t.Fatalf("exit %d, want 1 with the rule unknown at data/hosts.csv:4:\n%s", code, out)
	}
}

// A path outside the repository is refused: a file named with .. does not
// load, and a link that leads out makes the rule unknown, and the plugin
// is never sent the bytes. Planted break: the host's CheckSource passing
// a path with .., which loaded the config.
func TestAPathOutsideTheRepositoryIsRefused(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "hosts.csv")
	write(t, outside, hostsCSV, 0o644)
	text := strings.Replace(config, "file: data/hosts.csv", "file: ../hosts.csv", 1)
	code, out := check(t, repo(t, map[string]string{"errata.yaml": text, "inventory.yaml": "builder: {ip: 10.0.0.5}\n"}))
	if code != cli.ExitUsage || !strings.Contains(out, "outside the repository") {
		t.Fatalf("a file with ..: exit %d, want 2 naming the path outside the repository:\n%s", code, out)
	}
	root := repo(t, map[string]string{"inventory.yaml": "builder: {ip: 10.0.0.5}\n"})
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "data", "hosts.csv")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	code, out = check(t, root)
	if code != cli.ExitErrata || !strings.Contains(out, "unknown") || !strings.Contains(out, "outside the repository") {
		t.Fatalf("a link out: exit %d, want 1 with the rule unknown, naming the path outside the repository:\n%s", code, out)
	}
}
