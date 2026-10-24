package gitlabci

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// mergeHeavy is a file whose merge keys add n*101 nodes: an anchor of 100
// keys merged into n mappings.
func mergeHeavy(n int) string {
	var b strings.Builder
	b.WriteString(".a: &a {")
	for i := range 100 {
		fmt.Fprintf(&b, "k%d: 1, ", i)
	}
	b.WriteString("}\n")
	for i := range n {
		fmt.Fprintf(&b, ".m%d: {<<: *a}\n", i)
	}
	return b.String()
}

// The nodes YAML aliases and merge keys add count against
// core.MaxExpanded for every file read together, as the bytes and the
// nodes do: two included files, each under the bound, are over it
// together. Planted break: each file's count started at 0, which read
// both.
func TestAliasBoundCoversEveryFile(t *testing.T) {
	files := map[string]string{
		".gitlab-ci.yml": "include: [a.yml, b.yml]\njob:\n  script: [x]\n",
		"a.yml":          mergeHeavy(3000),
		"b.yml":          mergeHeavy(3000),
	}
	extracttest.WantError(t, refused(t, files, "job", "script"), "b.yml: line")
	extracttest.WantError(t, refused(t, files, "job", "script"), "aliases and merge keys expand to more than 1000000 nodes, the limit")
	delete(files, "b.yml")
	files[".gitlab-ci.yml"] = "include: [a.yml]\njob:\n  script: [x]\n"
	want(t, see(t, files, "job", "script"), "x@.gitlab-ci.yml:3")
}

// An include's name is held to core's bound on a file name, 4096 bytes, so
// a message quotes a short part of it rather than the whole name. Planted
// break: the name not checked, which put all 4097 bytes in the message.
func TestIncludeNameBound(t *testing.T) {
	// At 4096 bytes the name passes the bound, as a spec's own file does,
	// and the file system refuses it.
	name := strings.Repeat("x", 4092) + ".yml"
	err := refused(t, main("include: "+name+"\njob:\n  script: [x]\n"), "job", "script")
	extracttest.WantError(t, err, "file name too long")
	name = strings.Repeat("x", 4093) + ".yml"
	err = refused(t, main("include: "+name+"\njob:\n  script: [x]\n"), "job", "script")
	extracttest.WantError(t, err, "is 4097 bytes long, over the limit of 4096")
	if len(err.Error()) > 400 {
		t.Fatalf("the message is %d bytes, quoting the name whole", len(err.Error()))
	}
}

// Only a file over the bytes left says the files hold max_bytes in all; a
// symlink that leads out of the repository, or a missing file, says what
// it is. Planted break: the remark on every read error, which named the
// budget for the symlink.
func TestReadErrorsNameTheirCause(t *testing.T) {
	root := extracttest.Repo(t, main("include: [a.yml, link.yml]\njob:\n  script: [x]\n"))
	if err := os.WriteFile(filepath.Join(root, "a.yml"), []byte("variables: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	extracttest.Symlink(t, "../secret.yml", filepath.Join(root, "link.yml"))
	_, err := extracttest.Run(t, build, spec("job", "script"), root)
	extracttest.WantError(t, err, "read link.yml: a symlink leads outside the repository")
	if strings.Contains(err.Error(), "max_bytes") {
		t.Fatalf("%v: names the byte budget", err)
	}
}

// A chain that alternates extends and !reference is held to both limits:
// GitLab merges extends before it follows !reference, so a parent that is
// a !reference is refused, and so is a job that is a !reference to one
// that extends. A job resolved first through a short chain and reached
// again through a long one is held to the limit from where the long one
// reaches it. Planted breaks: the parent's tag not checked, which followed
// the alternating chain; the depth not checked on a job resolved before,
// which read the long chain.
func TestChainsHoldTheirLimits(t *testing.T) {
	alternating := "build:\n  extends: q0\nq0: !reference [j1, k]\nj1:\n  extends: q1\n  k: {}\nq1:\n  image: deep\n"
	extracttest.WantError(t, refused(t, main(alternating), "build", "image"), `job "build" extends "q0", a !reference`)
	viaRef := "build: !reference [j]\nj:\n  extends: .t\n.t:\n  image: x\n"
	extracttest.WantError(t, refused(t, main(viaRef), "build", "image"), "is a !reference to a job that extends another")

	var b strings.Builder
	// c5 extends c4 ... c0: five levels below c5.
	b.WriteString("c0:\n  image: deep\n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "c%d:\n  extends: c%d\n", i, i-1)
	}
	// shallow reaches c5 at the second level; e6 ... e0 reach it at the
	// eighth, which puts c1's extends at the twelfth, and e4 ... e0 at the
	// sixth, which puts it at the tenth.
	b.WriteString("shallow:\n  extends: c5\n")
	b.WriteString("e0:\n  extends: c5\n")
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&b, "e%d:\n  extends: e%d\n", i, i-1)
	}
	b.WriteString("deep:\n  extends: [shallow, e6]\n  script: [x]\n")
	extracttest.WantError(t, refused(t, main(b.String()), "deep", "image"), "inherits through more than 11 levels of extends")
	want(t, see(t, main(strings.Replace(b.String(), "[shallow, e6]", "[shallow, e4]", 1)), "deep", "image"), "deep@.gitlab-ci.yml:2")

	// A job resolved first through one !reference, shallow, and reached
	// again through a chain of them: .j5 takes five levels, and the chain
	// of .d reaches it at the sixth.
	refChain := func(d int) string {
		var r strings.Builder
		r.WriteString(".j0:\n  k: v\n  inh: {variables: true}\n")
		for i := 1; i <= 5; i++ {
			fmt.Fprintf(&r, ".j%d: !reference [.j%d]\n", i, i-1)
		}
		for i := range d {
			next := fmt.Sprintf(".d%d", i+1)
			if i == d-1 {
				next = ".j5"
			}
			fmt.Fprintf(&r, ".d%d: !reference [%s]\n", i, next)
		}
		r.WriteString("job:\n  inherit: !reference [.j5, inh]\n  variables:\n    A: !reference [.d0, k]\n  script: [x]\n")
		return r.String()
	}
	extracttest.WantError(t, refused(t, main(refChain(5)), "job", "A"), "nest more than 10 levels deep")
	want(t, see(t, main(refChain(3)), "job", "A"), "v@.gitlab-ci.yml:2")
	_ = core.MaxExpanded
}
