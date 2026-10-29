package core_test

import (
	encjson "encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/regex"
	"github.com/f-klement/errata/internal/extract/text"
	"github.com/f-klement/errata/internal/extract/toml"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// each kind with its file set to f and a valid path or pattern.
func eachKind(f string) map[string]extract.Spec {
	return map[string]extract.Spec{
		"yaml":  extract.Of(yaml.Kind, yaml.Spec{File: f, Path: "a"}),
		"json":  extract.Of(json.Kind, json.Spec{File: f, Path: "a"}),
		"toml":  extract.Of(toml.Kind, toml.Spec{File: f, Path: "a"}),
		"regex": extract.Of(regex.Kind, regex.Spec{File: f, Pattern: "(a)"}),
		"text":  extract.Of(text.Kind, text.Spec{File: f, Pattern: "(a)"}),
	}
}

// The planted breaks: an absolute path, a .. that leaves the root, and one
// that leaves it after entering a directory. Each kind refuses each one.
func TestBuildRefusesFileOutsideRepository(t *testing.T) {
	for _, f := range []string{"/etc/passwd", "../secret.yaml", "a/../../secret.yaml", ".."} {
		for kind, s := range eachKind(f) {
			_, err := extract.Build(s)
			extracttest.WantError(t, err, kind+": file \""+f+"\" is outside the repository")
		}
	}
	for kind, s := range eachKind("") {
		_, err := extract.Build(s)
		extracttest.WantError(t, err, kind+": file is required")
	}
}

// The planted break: a kind with every field empty, as {yaml: {}} decodes,
// and then each required field empty on its own. Build names the field.
func TestBuildRefusesEmptyField(t *testing.T) {
	for want, s := range map[string]extract.Spec{
		"yaml: file is required":                                 extract.Of(yaml.Kind, yaml.Spec{}),
		"json: file is required":                                 extract.Of(json.Kind, json.Spec{}),
		"toml: file is required":                                 extract.Of(toml.Kind, toml.Spec{}),
		"regex: file is required":                                extract.Of(regex.Kind, regex.Spec{}),
		"yaml: path is required; write $ for the whole document": extract.Of(yaml.Kind, yaml.Spec{File: "f"}),
		"json: path is required; write $ for the whole document": extract.Of(json.Kind, json.Spec{File: "f"}),
		"toml: path is required; write $ for the whole document": extract.Of(toml.Kind, toml.Spec{File: "f"}),
		"regex: pattern is required":                             extract.Of(regex.Kind, regex.Spec{File: "f"}),
	} {
		_, err := extract.Build(s)
		if err == nil || err.Error() != want {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}

func TestBuildAcceptsDotDotThatStaysInside(t *testing.T) {
	for kind, s := range eachKind("a/../b.txt") {
		if _, err := extract.Build(s); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
}

// A symlink that resolves outside the repository is only knowable when the
// file is read, so the refusal comes from Extract and the rule is unknown.
// The planted breaks: a relative link out, an absolute link out, a link to a
// directory outside in the middle of the path, links to a file outside that
// does not exist, directly and through that directory, a link to / with the
// rest of the path to the file outside after it, and a link that goes out
// through a directory outside and back into the repository, which ends
// inside but leaves by a directory that is not one of the root's parents.
// Every refusal is the same message, which names neither where the link
// points nor whether that exists.
func TestExtractRefusesSymlinkOutOfRepository(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := extracttest.Repo(t, nil)
	rel, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	extracttest.Symlink(t, filepath.Join(rel, "secret"), filepath.Join(root, "relative.txt"))
	extracttest.Symlink(t, filepath.Join(outside, "secret"), filepath.Join(root, "absolute.txt"))
	extracttest.Symlink(t, rel, filepath.Join(root, "middle"))
	extracttest.Symlink(t, filepath.Join(rel, "nope"), filepath.Join(root, "dangling.txt"))
	extracttest.Symlink(t, "/", filepath.Join(root, "slash"))
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Join would clean the route out and back away.
	extracttest.Symlink(t, rel+"/../"+filepath.Base(root)+"/a", filepath.Join(root, "outthenback.yaml"))
	if kernel, err := os.ReadFile(filepath.Join(root, "outthenback.yaml")); err != nil || string(kernel) != "a: 1\n" {
		t.Fatalf("the kernel reads %q, %v", kernel, err)
	}
	for _, f := range []string{"relative.txt", "absolute.txt", "middle/secret", "dangling.txt", "middle/nope", "slash" + outside + "/secret", "slash" + outside + "/nope", "outthenback.yaml"} {
		for kind, s := range eachKind(f) {
			_, err := run(t, s, root)
			want := "read " + f + ": a symlink leads outside the repository"
			if err == nil || err.Error() != want {
				t.Errorf("%s: error %v, want %q", kind, err, want)
			}
		}
	}
}

// A symlink that resolves inside the repository is read, whether it is
// relative, absolute, or relative by a route that goes up through the root's
// own parents and comes back. Each location names the file as the spec wrote
// it.
func TestExtractReadsSymlinkThatStaysInside(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"real/f.yaml": "a: 1\n"})
	extracttest.Symlink(t, "real/f.yaml", filepath.Join(root, "relative.yaml"))
	extracttest.Symlink(t, filepath.Join(root, "real", "f.yaml"), filepath.Join(root, "absolute.yaml"))
	extracttest.Symlink(t, filepath.Join("..", filepath.Base(root), "real", "f.yaml"), filepath.Join(root, "back.yaml"))
	extracttest.Symlink(t, "../real", filepath.Join(root, "dir", "link"))
	extracttest.Symlink(t, "relative.yaml", filepath.Join(root, "chain.yaml"))
	for _, f := range []string{"relative.yaml", "absolute.yaml", "back.yaml", "dir/link/f.yaml", "chain.yaml"} {
		got, err := run(t, extract.Of(yaml.Kind, yaml.Spec{File: f, Path: "a"}), root)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if extracttest.Texts(got) != "1@1" || got[0].At.File != f {
			t.Errorf("%s: got %s in %q", f, extracttest.Texts(got), got[0].At.File)
		}
	}
}

// A .. in a link's target goes up from where the link points, as the kernel
// reads it, while a .. in file is resolved by name before any link. The
// planted break is cleaning the target by name, which would read
// real/f.yaml for link.yaml instead of deep/real/f.yaml.
func TestExtractResolvesDotDotAsTheKernelDoes(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"real/f.yaml": "a: top\n", "deep/real/f.yaml": "a: deep\n", "deep/dir/x": ""})
	extracttest.Symlink(t, "deep/dir", filepath.Join(root, "sub"))
	extracttest.Symlink(t, "sub/../real/f.yaml", filepath.Join(root, "link.yaml"))
	kernel, err := os.ReadFile(filepath.Join(root, "link.yaml"))
	if err != nil || string(kernel) != "a: deep\n" {
		t.Fatalf("the kernel reads %q, %v", kernel, err)
	}
	for f, want := range map[string]string{"link.yaml": "deep@1", "sub/../real/f.yaml": "top@1"} {
		if got := extracttest.Texts(mustRun(t, extract.Of(yaml.Kind, yaml.Spec{File: f, Path: "a"}), root)); got != want {
			t.Errorf("%s: got %q, want %q", f, got, want)
		}
	}
}

// A file that cannot be read is named as the spec wrote it, with the reason
// and no absolute path: a missing file, a dangling link inside, a file used
// as a directory, a directory, and a loop of links.
func TestExtractUnreadableFileIsAnError(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"real/f.txt": "a"})
	extracttest.Symlink(t, "nowhere.txt", filepath.Join(root, "dangling.txt"))
	extracttest.Symlink(t, "loop2.txt", filepath.Join(root, "loop1.txt"))
	extracttest.Symlink(t, "loop1.txt", filepath.Join(root, "loop2.txt"))
	for f, want := range map[string]string{
		"absent.txt":   "read absent.txt: no such file or directory",
		"dangling.txt": "read dangling.txt: no such file or directory",
		"real/f.txt/x": "read real/f.txt/x: not a directory",
		"real":         "read real: is a directory",
		"loop1.txt":    "read loop1.txt: too many levels of symbolic links",
	} {
		for kind, s := range eachKind(f) {
			_, err := run(t, s, root)
			if err == nil || err.Error() != want {
				t.Errorf("%s: error %v, want %q", kind, err, want)
			}
		}
	}
}

func TestExtractMissingRootIsAnError(t *testing.T) {
	for kind, s := range eachKind("a.txt") {
		_, err := run(t, s, filepath.Join(t.TempDir(), "gone"))
		want := "open the repository root: no such file or directory"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", kind, err, want)
		}
	}
}

// A read follows at most maxLinks symlinks, as Linux does: a chain of that
// many reaches the file, and one more is a loop.
func TestExtractFollowsAtMostMaxLinks(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.txt": "v=1"})
	prev := "f.txt"
	for i := range core.MaxLinks + 1 {
		name := fmt.Sprintf("l%d", i)
		extracttest.Symlink(t, prev, filepath.Join(root, name))
		prev = name
	}
	if got := extracttest.Texts(mustRun(t, extract.Of(regex.Kind, regex.Spec{File: fmt.Sprintf("l%d", core.MaxLinks-1), Pattern: `v=(\d)`}), root)); got != "1@1" {
		t.Fatalf("a chain of %d links: got %q", core.MaxLinks, got)
	}
	_, err := run(t, extract.Of(regex.Kind, regex.Spec{File: fmt.Sprintf("l%d", core.MaxLinks), Pattern: `v=(\d)`}), root)
	if want := fmt.Sprintf("read l%d: too many levels of symbolic links", core.MaxLinks); err == nil || err.Error() != want {
		t.Fatalf("a chain of %d links: error %v, want %q", core.MaxLinks+1, err, want)
	}
}

// withMaxBytes is s with max_bytes set to n, written into the entry as
// errata.yaml writes it, so it reads the same for every kind.
func withMaxBytes(s extract.Spec, n int64) extract.Spec {
	raw, err := encjson.Marshal(s)
	if err != nil {
		panic(err)
	}
	var entry map[string]map[string]any
	if err := encjson.Unmarshal(raw, &entry); err != nil {
		panic(err)
	}
	for _, spec := range entry {
		spec["max_bytes"] = n
	}
	if raw, err = encjson.Marshal(entry); err != nil {
		panic(err)
	}
	var out extract.Spec
	if err := encjson.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

// Each extractor reads a file of exactly max_bytes, and refuses a file one
// byte over it before reading; the planted break is that byte. The value
// sits at the end of the file, so a read that stopped short would lose it.
func TestExtractFileSizeLimit(t *testing.T) {
	const limit = 1000
	for _, c := range []struct {
		file string
		tail string
		spec extract.Spec
	}{
		{"f.yaml", "\na: 1", extract.Of(yaml.Kind, yaml.Spec{File: "f.yaml", Path: "a"})},
		{"f.toml", "\na = 1", extract.Of(toml.Kind, toml.Spec{File: "f.toml", Path: "a"})},
		{"f.json", "\n[1]", extract.Of(json.Kind, json.Spec{File: "f.json", Path: "[0]"})},
		{"f.txt", "\na: 1", extract.Of(regex.Kind, regex.Spec{File: "f.txt", Pattern: "a: (1)"})},
		{"f.md", "\na: 1", extract.Of(text.Kind, text.Spec{File: "f.md", Pattern: "a: (1)"})},
	} {
		pad := "#"
		if c.file == "f.json" {
			pad = " "
		}
		body := pad + strings.Repeat(" ", limit-len(c.tail)-1) + c.tail
		got := mustRun(t, withMaxBytes(c.spec, limit), extracttest.Repo(t, map[string]string{c.file: body}))
		if len(got) != 1 || got[0].Text != "1" {
			t.Errorf("%s of %d bytes: got %s", c.file, len(body), extracttest.Texts(got))
		}
		_, err := run(t, withMaxBytes(c.spec, limit), extracttest.Repo(t, map[string]string{c.file: " " + body}))
		want := "read " + c.file + ": the file is 1001 bytes, over the limit of 1000 bytes; set max_bytes: on the extractor to read a larger file"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", c.file, err, want)
		}
	}
}

// Without max_bytes, every kind refuses a file one byte over 32 MiB, and a
// max_bytes above that raises the limit: the file is sparse, so the test
// writes nothing, and the refusal comes before a byte is read.
func TestExtractDefaultFileSizeLimit(t *testing.T) {
	root := extracttest.Repo(t, nil)
	f, err := os.Create(filepath.Join(root, "huge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(core.DefaultMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for kind, s := range eachKind("huge") {
		_, err := run(t, s, root)
		want := "read huge: the file is 33554433 bytes, over the limit of 33554432 bytes; set max_bytes: on the extractor to read a larger file"
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", kind, err, want)
		}
	}
	// A max_bytes over the default is the limit, not capped at the default.
	e, err := extract.Build(withMaxBytes(extract.Of(json.Kind, json.Spec{File: "huge", Path: "a"}), core.DefaultMaxBytes+1))
	if err != nil || e.(core.TreeExtractor).Limit != core.DefaultMaxBytes+1 {
		t.Errorf("max_bytes over the default: %v, %+v", err, e)
	}
}

// The planted breaks: a negative max_bytes, which no file can meet, one
// byte over the ceiling, and the largest int64, which is how someone asks
// for no limit and which read every file as empty before. The ceiling itself
// builds.
func TestBuildRefusesMaxBytesOutsideItsRange(t *testing.T) {
	for kind, s := range eachKind("f") {
		for _, n := range []int64{-1, core.MaxMaxBytes + 1, math.MaxInt64} {
			_, err := extract.Build(withMaxBytes(s, n))
			want := kind + ": max_bytes is " + strconv.FormatInt(n, 10) + "; write a number of bytes from 1 to 67108864, or leave it out for 33554432"
			if err == nil || err.Error() != want {
				t.Errorf("error %v, want %q", err, want)
			}
		}
		if _, err := extract.Build(withMaxBytes(s, core.MaxMaxBytes)); err != nil {
			t.Errorf("%s: max_bytes at the ceiling: %v", kind, err)
		}
	}
}

// Every kind builds with a file name of maxFileName bytes and refuses one a
// byte longer, quoting a short part of it. The planted break is that byte.
func TestBuildRefusesAFileNameOverItsLimit(t *testing.T) {
	for kind, s := range eachKind(strings.Repeat("a", core.MaxFileName)) {
		if _, err := extract.Build(s); err != nil {
			t.Errorf("%s: a file name of %d bytes: %v", kind, core.MaxFileName, err)
		}
	}
	for kind, s := range eachKind(strings.Repeat("a", core.MaxFileName+1)) {
		_, err := extract.Build(s)
		want := kind + `: file "` + strings.Repeat("a", core.MaxQuote) + `"... is 4097 bytes long, over the limit of 4096; write a shorter path`
		if err == nil || err.Error() != want {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}
