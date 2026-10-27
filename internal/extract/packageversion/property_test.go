package packageversion

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// version draws a version as manifests write them.
var drawnVersion = rapid.StringMatching(`[0-9][0-9a-z.+-]{0,8}`)

// filler writes TOML lines that name no version of the package: blank ones,
// comments, and keys of other tables, some of them named version.
func filler(t *rapid.T, w *extracttest.LineWriter) {
	for range rapid.IntRange(0, 3).Draw(t, "filler") {
		// Each table is new, numbered by the line it starts at, since TOML
		// refuses a table defined twice.
		n := w.Line + 1
		switch rapid.IntRange(0, 3).Draw(t, "kind") {
		case 0:
			w.Add("")
		case 1:
			w.Add("# version = %q", drawnVersion.Draw(t, "comment"))
		case 2:
			w.Add("[dependencies.serde%d]\nversion = %q", n, drawnVersion.Draw(t, "dependency"))
			w.Line++
		default:
			w.Add("[tool.other%d]\nversion = %q", n, drawnVersion.Draw(t, "other"))
			w.Line++
		}
	}
}

// A Cargo.toml's version comes back at its line, whatever tables, comments
// and other keys named version are around it: a literal version as written,
// an inherited one as the workspace's when the file holds that, and as not
// literal when it does not.
func TestPropertyCargoVersionSitsAtItsLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		var w extracttest.LineWriter
		filler(t, &w)
		var want []string
		form := rapid.SampledFrom([]string{"literal", "dotted", "inline"}).Draw(t, "form")
		shared := rapid.Bool().Draw(t, "workspace")
		w.Add("[package]")
		w.Add("name = \"p\"")
		v := drawnVersion.Draw(t, "version")
		switch form {
		case "literal":
			want = append(want, fmt.Sprintf("%s@%d", v, w.Add("version = %q", v)))
		case "dotted":
			n := w.Add("version.workspace = true")
			if !shared {
				want = append(want, fmt.Sprintf("version.workspace = true!@%d", n))
			}
		default:
			n := w.Add("version = { workspace = true }")
			if !shared {
				want = append(want, fmt.Sprintf("version.workspace = true!@%d", n))
			}
		}
		filler(t, &w)
		if shared {
			w.Add("[workspace.package]")
			sv := drawnVersion.Draw(t, "shared version")
			want = append(want, fmt.Sprintf("%s@%d", sv, w.Add("version = %q", sv)))
		}
		checkFile(t, root, "Cargo.toml", w.Text.String(), want)
	})
}

// A pyproject.toml's version comes back at its line in each form the build
// reads it from: project.version, a dynamic version as not literal at its
// entry, and tool.poetry.version under poetry-core or without PEP 621.
func TestPropertyPyprojectVersionSitsAtItsLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		var w extracttest.LineWriter
		var want string
		form := rapid.SampledFrom([]string{"pep621", "dynamic", "poetry-core", "poetry"}).Draw(t, "form")
		if form == "poetry-core" {
			w.Add("[build-system]\nbuild-backend = \"poetry.core.masonry.api\"")
			w.Line++
		}
		filler(t, &w)
		v := drawnVersion.Draw(t, "version")
		if form != "poetry" {
			w.Add("[project]")
			w.Add("name = \"p\"")
		}
		switch form {
		case "pep621":
			want = fmt.Sprintf("%s@%d", v, w.Add("version = %q", v))
		case "dynamic", "poetry-core":
			w.Add("dynamic = [")
			n := w.Add("  \"version\",")
			w.Add("]")
			want = fmt.Sprintf(`dynamic = ["version"]!@%d`, n)
		}
		filler(t, &w)
		if form == "dynamic" || form == "poetry-core" || form == "poetry" {
			w.Add("[tool.poetry]")
			n := w.Add("version = %q", v)
			if form != "dynamic" {
				want = fmt.Sprintf("%s@%d", v, n)
			}
		}
		checkFile(t, root, "pyproject.toml", w.Text.String(), []string{want})
	})
}

// checkFile writes data to name under root and fails unless the extractor's
// values, marked, are want, each at a line of the file.
func checkFile(t *rapid.T, root, name, data string, want []string) {
	if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got := extracttest.MustExtract(t, build, one(name), root)
	lines := strings.Split(data, "\n")
	var texts []string
	for _, v := range got {
		extracttest.CheckLine(t, v, lines, []byte(data))
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		texts = append(texts, fmt.Sprintf("%s%s@%d", v.Text, mark, v.At.Line))
	}
	if strings.Join(texts, " ") != strings.Join(want, " ") {
		t.Fatalf("got %q, want %q\n%s", texts, want, data)
	}
}
