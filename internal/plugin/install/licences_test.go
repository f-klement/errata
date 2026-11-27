package install

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/plugin"
)

// Every module a plugin links besides errata's, on any platform errata
// ships for, has a row in its cmd/errata-ext-<name>/licences.txt: the
// licence's SPDX identifier, as a person read it. The release writes the
// plugin's notices from those rows and the module cache
// (ci/plugin-notices.sh, which refuses the same as this test), so a bump of
// a module's version passes here, and a module more or less fails until its
// row is written or removed. A plugin without licences.txt has no rows, so
// it fails here the day it links a module. Planted breaks: go-cty's row
// removed from hcl's; a row for a module hcl does not link; csv's main
// importing a module; go list asked about the standard library alone.
func TestEveryLinkedModuleHasALicenceRow(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "..", "..", "cmd", "errata-ext-*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no cmd/errata-ext-* found three directories up: %v", err)
	}
	for _, dir := range dirs {
		t.Run(strings.TrimPrefix(filepath.Base(dir), "errata-ext-"), func(t *testing.T) { checkLicenceRows(t, dir) })
	}
}

// checkLicenceRows holds the rows of dir's licences.txt to the modules the
// plugin in dir links: a row for each, and none for a module it does not.
func checkLicenceRows(t *testing.T, dir string) {
	t.Helper()
	rows := licenceRows(t, dir)
	linked := linkedModules(t, dir)
	for _, m := range slices.Sorted(maps.Keys(linked)) {
		if rows[m] == "" {
			t.Errorf("the plugin links %s %s, whose licence has no row in %s; read its licence and add the row", m, linked[m], filepath.Join(dir, "licences.txt"))
		}
	}
	for m := range rows {
		if _, ok := linked[m]; !ok {
			t.Errorf("%s has a row for %s, which the plugin no longer links; remove the row", filepath.Join(dir, "licences.txt"), m)
		}
	}
}

// licenceRows are the rows of dir's licences.txt, module path to SPDX
// identifier, and none when dir has no licences.txt.
func licenceRows(t *testing.T, dir string) map[string]string {
	t.Helper()
	rows := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, "licences.txt"))
	if errors.Is(err, fs.ErrNotExist) {
		return rows
	}
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if _, twice := rows[f[0]]; len(f) != 2 || twice {
			t.Fatalf("%s: %q is not a row of its own: want a module path and its licence's SPDX identifier", filepath.Join(dir, "licences.txt"), line)
		}
		rows[f[0]] = f[1]
	}
	return rows
}

// linkedModules are the modules besides errata's that the plugin in dir
// links on any platform errata ships for, path to version. go list must
// name errata's own module on each platform (platformModules), so an answer
// with no module is one about the plugin, not about nothing.
func linkedModules(t *testing.T, dir string) map[string]string {
	t.Helper()
	modules := map[string]string{}
	for _, p := range plugin.Platforms {
		goos, goarch, _ := strings.Cut(p, "/")
		list := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{with .Module}}{{.Main}} {{.Path}} {{.Version}}{{end}}", ".")
		list.Dir = dir
		list.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
		out, err := list.Output()
		if err != nil {
			t.Fatalf("go list in %s for %s: %v", dir, p, err)
		}
		if err := platformModules(string(out), modules); err != nil {
			t.Fatalf("go list in %s for %s: %v", dir, p, err)
		}
	}
	return modules
}

// platformModules adds to modules the modules of out, go list's answer for
// one platform, a line each: whether it is errata's own module, its path
// and its version. It refuses an answer that does not name errata's own
// module, so a go that answers for one platform and prints nothing for
// another does not leave that platform unchecked.
func platformModules(out string, modules map[string]string) error {
	sawMain := false
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
		case f[0] == "true":
			sawMain = true
		case len(f) == 3:
			modules[f[1]] = f[2]
		default:
			return fmt.Errorf("%q is not a module and its version", line)
		}
	}
	if !sawMain {
		return errors.New("named no package of errata's own module, so it did not read the plugin")
	}
	return nil
}

// go list's answer for one platform names errata's own module, or the
// platform is refused, whatever another platform's answer named; a line
// of neither shape is refused too. Planted break: platformModules not
// checking that the answer names errata's module.
func TestPlatformModulesWantsErrataOnEachPlatform(t *testing.T) {
	modules := map[string]string{}
	if err := platformModules("true github.com/f-klement/errata \nfalse example.com/a v1.0.0\n", modules); err != nil || modules["example.com/a"] != "v1.0.0" {
		t.Errorf("an answer naming errata: %v, modules %v", err, modules)
	}
	if err := platformModules("", modules); err == nil {
		t.Error("an empty answer after one that named errata passed")
	}
	if err := platformModules("false example.com/b v1.0.0\n", modules); err == nil {
		t.Error("an answer without errata's module passed")
	}
	if err := platformModules("true\nfalse x\n", modules); err == nil || !strings.Contains(err.Error(), "is not a module") {
		t.Errorf("a line of neither shape: %v", err)
	}
}
