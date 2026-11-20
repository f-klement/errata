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
	dir, err := os.MkdirTemp("", "errata-ext-hcl-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	path := filepath.Join(dir, "errata-ext-hcl")
	ldflags := "-X github.com/f-klement/errata/internal/ext/hcl.Version=" + version
	build := exec.CommandContext(context.Background(), "go", "build", "-ldflags", ldflags, "-o", path, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building errata-ext-hcl: %v\n%s", err, out)
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

// config holds one rule: the module's default region is the one the
// production variables set, two entries of the plugin's kind in one rule.
const config = `checks:
  - id: region
    reason: the module's default region is the one production sets
    scope: repo
    extract:
      default: {hcl: {file: infra/variables.tf, path: variable.region.default}}
      prod: {hcl: {file: infra/prod.tfvars, path: region}}
    agree: [default, prod]
`

const variablesTF = `variable "region" {
  type    = string
  default = "eu-west-1"
}
`

// repo is a git repository holding files and errata.yaml, with the plugin
// vendored at its sum and pinned in errata.lock, as errata plugin install
// --vendor will leave one.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	s := sha256.Sum256(bin)
	sum := hex.EncodeToString(s[:])
	write(t, plugin.BuildPath(filepath.Join(root, plugin.VendorDir), "hcl", version, plugin.Platform(), sum), string(bin), 0o755)
	write(t, filepath.Join(root, plugin.LockFile), "hcl "+version+" "+plugin.Platform()+" sha256:"+sum+"\n", 0o644)
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

// errata runs the built plugin from the lock's pin in a repository,
// through its host, and the rule's verdict follows the values: a
// variable's default and a .tfvars key agree when they are one region,
// and the rule fails naming the .tfvars file's line when they are not.
// Planted breaks: Version left at its default, which the lock's 0.1.0
// refused; line counting HCL's lines, which named line 2 for 3.
func TestErrataRunsThePluginFromItsPin(t *testing.T) {
	code, out := check(t, repo(t, map[string]string{"infra/variables.tf": variablesTF, "infra/prod.tfvars": "region = \"eu-west-1\"\n"}))
	if code != cli.ExitOK {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	code, out = check(t, repo(t, map[string]string{"infra/variables.tf": variablesTF, "infra/prod.tfvars": "# a\rb\nregion = \"us-east-1\"\n"}))
	if code != cli.ExitErrata || !strings.Contains(out, "infra/prod.tfvars:3") || !strings.Contains(out, "us-east-1") {
		t.Fatalf("exit %d, want 1 naming infra/prod.tfvars:3 and us-east-1:\n%s", code, out)
	}
}

// A value the file computes is not compared as text: the rule is unknown,
// naming the not literal value, and skip_not_literal leaves it out.
// Planted break: built describing NotLiteral false, which made errata
// refuse the plugin's not literal value as a reply the protocol forbids.
func TestAValueTheFileComputesIsNotComparedAsText(t *testing.T) {
	files := map[string]string{"infra/variables.tf": variablesTF, "infra/prod.tfvars": "region = var.home\n"}
	code, out := check(t, repo(t, files))
	if code != cli.ExitErrata || !strings.Contains(out, "unknown") || !strings.Contains(out, `extractor "prod" returned 1 not literal value`) {
		t.Fatalf("exit %d, want 1 with the rule unknown for a not literal value:\n%s", code, out)
	}
	files["errata.yaml"] = strings.Replace(config, "    agree:", "    skip_not_literal: [prod]\n    allow_empty: [prod]\n    agree:", 1)
	code, out = check(t, repo(t, files))
	if code != cli.ExitOK {
		t.Fatalf("with skip_not_literal: exit %d, want 0:\n%s", code, out)
	}
}

// A file that does not parse makes the rule unknown, with the plugin's
// message naming the file and the line. Planted break: read ignoring
// HCL's diagnostics, which held the rule.
func TestAFileThatDoesNotParseMakesTheRuleUnknown(t *testing.T) {
	code, out := check(t, repo(t, map[string]string{"infra/variables.tf": variablesTF + "variable \"zone\" {\n", "infra/prod.tfvars": "region = \"eu-west-1\"\n"}))
	if code != cli.ExitErrata || !strings.Contains(out, "unknown") || !strings.Contains(out, "infra/variables.tf:5: Unclosed configuration block") {
		t.Fatalf("exit %d, want 1 with the rule unknown at infra/variables.tf:5:\n%s", code, out)
	}
}

// A path outside the repository is refused: a file named with .. does not
// load, and a link that leads out makes the rule unknown, and the plugin
// is never sent the bytes. Planted break: the host's CheckSource passing
// a path with .., which loaded the config.
func TestAPathOutsideTheRepositoryIsRefused(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "prod.tfvars")
	write(t, outside, "region = \"eu-west-1\"\n", 0o644)
	text := strings.Replace(config, "file: infra/prod.tfvars", "file: ../prod.tfvars", 1)
	code, out := check(t, repo(t, map[string]string{"errata.yaml": text, "infra/variables.tf": variablesTF}))
	if code != cli.ExitUsage || !strings.Contains(out, "outside the repository") {
		t.Fatalf("a file with ..: exit %d, want 2 naming the path outside the repository:\n%s", code, out)
	}
	root := repo(t, map[string]string{"infra/variables.tf": variablesTF})
	if err := os.Symlink(outside, filepath.Join(root, "infra", "prod.tfvars")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	code, out = check(t, root)
	if code != cli.ExitErrata || !strings.Contains(out, "unknown") || !strings.Contains(out, "outside the repository") {
		t.Fatalf("a link out: exit %d, want 1 with the rule unknown, naming the path outside the repository:\n%s", code, out)
	}
}

// filesConfig holds one rule over a glob: every instance in the module's
// .tf files has the type production sets.
const filesConfig = `checks:
  - id: instance-type
    reason: every instance runs the type production sets
    scope: repo
    extract:
      types: {hcl: {files: ["infra/*.tf"], path: "resource.aws_instance[*].instance_type"}}
      prod: {hcl: {file: infra/prod.tfvars, path: instance_type}}
    agree: [types, prod]
`

// A files entry with [*] reads every file its glob matches, in path
// order, and each value carries the path it matched: the rule holds when
// every instance of both files has production's type, and fails naming
// the one that does not by its file, its line and its resource. Planted
// break: the plugin reading only the first file of the request.
func TestAFilesEntryWithASplatReadsEveryFile(t *testing.T) {
	files := map[string]string{
		"errata.yaml":       filesConfig,
		"infra/prod.tfvars": "instance_type = \"t3.micro\"\n",
		"infra/web.tf":      "resource \"aws_instance\" \"web\" {\n  instance_type = \"t3.micro\"\n}\nresource \"aws_instance\" \"api\" {\n  instance_type = \"t3.micro\"\n}\n",
		"infra/db.tf":       "resource \"aws_instance\" \"db\" {\n  ami           = \"ami-1\"\n  instance_type = \"t3.micro\"\n}\n",
	}
	if code, out := check(t, repo(t, files)); code != cli.ExitOK {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	files["infra/db.tf"] = strings.Replace(files["infra/db.tf"], "t3.micro", "m5.large", 1)
	code, out := check(t, repo(t, files))
	for _, want := range []string{`"m5.large" at infra/db.tf:3 key "instance_type" when "resource.aws_instance.db.instance_type"`, `infra/web.tf:5 key "instance_type" when "resource.aws_instance.api.instance_type"`} {
		if code != cli.ExitErrata || !strings.Contains(out, want) {
			t.Fatalf("exit %d, want 1 naming %s:\n%s", code, want, out)
		}
	}
}
