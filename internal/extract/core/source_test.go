package core

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The read cannot overflow whatever its limit: a source with the largest
// int64 as its limit, which Build refuses, still reads the whole file. The
// planted break is reading limit+1 bytes, which wraps to a negative count
// and reads nothing.
func TestReadDoesNotOverflowAtTheLargestLimit(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "a: 1\n"})
	data, err := Source{File: "f", Limit: math.MaxInt64}.Read(root)
	if err != nil || string(data) != "a: 1\n" {
		t.Errorf("read %q, %v", data, err)
	}
}

// Head reads at most n bytes and gives the whole file's size, and refuses
// as Read does: a file over the limit, one that is not there, and a link
// that leads out. Planted breaks: Head reading the whole file; the limit
// check dropped from open.
func TestHead(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "#!/bin/sh\nX=1\n"})
	extracttest.Symlink(t, "/etc/hostname", root+"/out")
	head, size, err := Source{File: "f", Limit: 100}.Head(root, 4)
	if err != nil || string(head) != "#!/b" || size != 14 {
		t.Errorf("head %q, size %d, %v; want #!/b and 14", head, size, err)
	}
	if head, _, err := (Source{File: "f", Limit: 100}).Head(root, 100); err != nil || len(head) != 14 {
		t.Errorf("head of a short file %q, %v", head, err)
	}
	_, _, err = Source{File: "f", Limit: 13}.Head(root, 4)
	extracttest.WantError(t, err, "over the limit")
	_, _, err = Source{File: "g", Limit: 100}.Head(root, 4)
	extracttest.WantError(t, err, "read g:")
	_, _, err = Source{File: "out", Limit: 100}.Head(root, 4)
	extracttest.WantError(t, err, "a symlink leads outside the repository")
}

// Resolved names the file a link leads to, relative to the root, and refuses
// what Read refuses, with Read's words: a link out of the repository and a
// root that is not there. The planted breaks are each of its two error
// checks negated, which return a path for the refusal and refuse the link
// that stays inside.
func TestResolvedFollowsLinksAndRefusesAsReadDoes(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"real/f.yaml": "a: 1\n"})
	extracttest.Symlink(t, "real/f.yaml", filepath.Join(root, "link.yaml"))
	extracttest.Symlink(t, "/", filepath.Join(root, "slash"))
	if got, err := (Source{File: "link.yaml"}).Resolved(root); err != nil || got != "real/f.yaml" {
		t.Errorf("link.yaml resolves to %q, %v; want real/f.yaml", got, err)
	}
	for _, c := range []struct{ root, file, want string }{
		{root, "slash/etc/passwd", "read slash/etc/passwd: a symlink leads outside the repository"},
		{filepath.Join(t.TempDir(), "gone"), "a.txt", "open the repository root: no such file or directory"},
	} {
		got, err := (Source{File: c.file}).Resolved(c.root)
		if err == nil || err.Error() != c.want || got != "" {
			t.Errorf("%s resolves to %q, %v; want the error %q", c.file, got, err, c.want)
		}
	}
}
