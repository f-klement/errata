package gitindex

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// headTags is HeadTags at root under testLimits.
func headTags(t *testing.T, root, prefix string) ([]string, error) {
	t.Helper()
	return HeadTags(t.Context(), root, prefix, testLimits)
}

// pointsAt is git tag --points-at HEAD of root, the tags that start with
// prefix: what HeadTags must list.
func pointsAt(t *testing.T, root, prefix string) []string {
	t.Helper()
	var out []string
	for tag := range strings.FieldsSeq(git(t, root, "tag", "--points-at", "HEAD")) {
		if strings.HasPrefix(tag, prefix) {
			out = append(out, tag)
		}
	}
	return out
}

// committed is a repository of one file and commits, made with init's
// extra arguments; each commit is tagged by the function at its index.
func committed(t *testing.T, commits int, init ...string) string {
	t.Helper()
	root := repo(t, map[string]string{"a": "x"}, init...)
	for i := range commits {
		git(t, root, "commit", "-q", "--allow-empty", "-m", "c"+string(rune('0'+i)))
	}
	return root
}

// HeadTags lists what git tag --points-at HEAD lists, the tags packed or
// loose, lightweight or annotated, peeled by packed-refs or through their
// objects, loose or in a pack, in either object format. Planted breaks:
// the ^ line taken for the ref's own object, which listed no annotated
// packed tag; a loose ref read after the packed one, which listed v1 moved
// off HEAD; and peel stopping at the first tag object, which missed the
// tag of a tag.
func TestHeadTagsIsPointsAt(t *testing.T) {
	for _, c := range []struct {
		name   string
		init   []string
		setup  func(t *testing.T, root string)
		prefix string
		want   []string
	}{
		{"a lightweight tag", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1")
			git(t, root, "tag", "old", "HEAD~1")
		}, "", []string{"v1"}},
		{"an annotated tag, loose", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "-a", "-m", "m", "v1")
			git(t, root, "tag", "-a", "-m", "m", "old", "HEAD~1")
		}, "", []string{"v1"}},
		{"annotated, packed and peeled", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "-a", "-m", "m", "v1")
			git(t, root, "tag", "-a", "-m", "m", "old", "HEAD~1")
			git(t, root, "tag", "light")
			git(t, root, "pack-refs", "--all")
		}, "", []string{"light", "v1"}},
		{"annotated, its object in a pack", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "-a", "-m", "m", "v1")
			git(t, root, "tag", "-a", "-m", "m", "old", "HEAD~1")
			git(t, root, "repack", "-a", "-d", "-q")
			git(t, root, "prune-packed")
		}, "", []string{"v1"}},
		{"annotated, a delta by name in a pack", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "-a", "-m", "m", "v1")
			git(t, root, "tag", "-a", "-m", "m", "v2")
			git(t, root, "tag", "-a", "-m", "m", "old", "HEAD~1")
			git(t, root, "-c", "repack.useDeltaBaseOffset=false", "repack", "-a", "-d", "-q")
			git(t, root, "prune-packed")
		}, "", []string{"v1", "v2"}},
		{"after gc", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "-a", "-m", "m", "v1")
			git(t, root, "tag", "w1")
			git(t, root, "gc", "-q")
		}, "", []string{"v1", "w1"}},
		{"a loose ref over a packed one", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1")
			git(t, root, "tag", "v2")
			git(t, root, "pack-refs", "--all")
			git(t, root, "tag", "-f", "v1", "HEAD~1")
		}, "", []string{"v2"}},
		{"a tag of a tag", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "-a", "-m", "m", "inner")
			git(t, root, "tag", "-a", "-m", "m", "outer", "inner")
			git(t, root, "tag", "-d", "inner")
		}, "", []string{"outer"}},
		{"a tag of a tag, packed", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "-a", "-m", "m", "inner")
			git(t, root, "tag", "-a", "-m", "m", "outer", "inner")
			git(t, root, "gc", "-q")
		}, "", []string{"inner", "outer"}},
		{"a HEAD that is a symlink into refs/", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1")
			branch := strings.TrimSpace(git(t, root, "symbolic-ref", "HEAD"))
			if err := os.Remove(gitPath(root, "HEAD")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(branch, gitPath(root, "HEAD")); err != nil {
				t.Fatal(err)
			}
		}, "", []string{"v1"}},
		{"an empty packed-refs", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1")
			write(t, root, "packed-refs", "")
		}, "", []string{"v1"}},
		{"a detached HEAD", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "-a", "-m", "m", "v1", "HEAD~1")
			git(t, root, "checkout", "-q", "--detach", "HEAD~1")
		}, "", []string{"v1"}},
		{"a prefix", nil, func(t *testing.T, root string) {
			t.Helper()
			for _, tag := range []string{"v1.2.0", "api/v2.0.0", "latest", "va"} {
				git(t, root, "tag", tag)
			}
		}, "v", []string{"v1.2.0", "va"}},
		{"a prefix with a slash", nil, func(t *testing.T, root string) {
			t.Helper()
			for _, tag := range []string{"v1.2.0", "api/v2.0.0", "api/x/v3"} {
				git(t, root, "tag", tag)
			}
		}, "api/v", []string{"api/v2.0.0"}},
		{"SHA-256", []string{"--object-format=sha256"}, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "-a", "-m", "m", "v1")
			git(t, root, "tag", "-a", "-m", "m", "v2")
			git(t, root, "tag", "old", "HEAD~1")
			git(t, root, "repack", "-a", "-d", "-q")
			git(t, root, "prune-packed")
		}, "", []string{"v1", "v2"}},
		{"SHA-256, packed refs", []string{"--object-format=sha256"}, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "-a", "-m", "m", "v1")
			git(t, root, "gc", "-q")
		}, "", []string{"v1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := committed(t, 2, c.init...)
			c.setup(t, root)
			got, err := headTags(t, root, c.prefix)
			if err != nil {
				t.Fatal(err)
			}
			if want := pointsAt(t, root, c.prefix); !slices.Equal(want, c.want) {
				t.Fatalf("git lists %q, the case wants %q", want, c.want)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// A packed-refs that does not say it peels every tag, as one an older git
// or another tool wrote, is peeled through the objects. Planted break: a
// packed tag without a ^ line taken for a lightweight one, which listed
// none.
func TestHeadTagsPeelsAPackedRefsOfNoTraits(t *testing.T) {
	root := committed(t, 2)
	git(t, root, "tag", "-a", "-m", "m", "v1")
	git(t, root, "tag", "-a", "-m", "m", "old", "HEAD~1")
	git(t, root, "pack-refs", "--all")
	packed := filepath.Join(root, ".git", "packed-refs")
	data, err := os.ReadFile(packed)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
		if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "^") {
			kept = append(kept, line)
		}
	}
	if err := os.WriteFile(packed, []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := headTags(t, root, "")
	if err != nil || !slices.Equal(got, []string{"v1"}) {
		t.Errorf("got %q, %v, want v1", got, err)
	}
}

// A worktree reads its own HEAD and the main repository's tags, through
// its commondir. Planted break: commonDir giving the worktree's own git
// directory, which holds no tag.
func TestHeadTagsOfAWorktree(t *testing.T) {
	root := committed(t, 2)
	git(t, root, "tag", "-a", "-m", "m", "v1", "HEAD~1")
	git(t, root, "tag", "v2")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", "--detach", wt, "HEAD~1")
	got, err := headTags(t, wt, "")
	if err != nil || !slices.Equal(got, []string{"v1"}) {
		t.Errorf("got %q, %v, want v1", got, err)
	}
	// git writes commondir relative, but reads an absolute one too.
	// Planted break: the absolute path joined below the git directory.
	gitdir := filepath.Join(root, ".git", "worktrees", "wt")
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte(filepath.Join(root, ".git")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := headTags(t, wt, ""); err != nil || !slices.Equal(got, []string{"v1"}) {
		t.Errorf("an absolute commondir: got %q, %v, want v1", got, err)
	}
	if err := os.Remove(filepath.Join(gitdir, "commondir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(gitdir, "commondir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := headTags(t, wt, ""); err == nil || !strings.Contains(err.Error(), "read its commondir: it is not a regular file") {
		t.Errorf("a commondir directory: got %v", err)
	}
}

// commonDir refuses a commondir it cannot read, with the reason. Planted
// break: the read's error passed over, which took the git directory for
// the common one.
func TestCommonDirRefuses(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "commondir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := commonDir(dir, testLimits); err == nil || err.Error() != "read its commondir: it is not a regular file" {
		t.Errorf("got %v", err)
	}
}

// HEAD may lead through 5 symbolic refs, as git's SYMREF_MAXDEPTH, and
// a tag through 16 tag objects; one more of either is refused. Planted
// breaks: either loop made one shorter.
func TestHeadTagsDepths(t *testing.T) {
	symrefs := func(t *testing.T, n int) error {
		t.Helper()
		root := committed(t, 1)
		git(t, root, "tag", "v1")
		id := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
		write(t, root, "HEAD", "ref: refs/heads/s1\n")
		for i := 1; i < n; i++ {
			write(t, root, "refs/heads/s"+strconv.Itoa(i), "ref: refs/heads/s"+strconv.Itoa(i+1)+"\n")
		}
		write(t, root, "refs/heads/s"+strconv.Itoa(n), id+"\n")
		_, err := headTags(t, root, "")
		return err
	}
	if err := symrefs(t, 5); err != nil {
		t.Errorf("5 symbolic refs: %v", err)
	}
	if err := symrefs(t, 6); err == nil || !strings.Contains(err.Error(), "leads through more than 5 symbolic refs") {
		t.Errorf("6 symbolic refs: got %v", err)
	}
	peels := func(t *testing.T, n int) ([]string, error) {
		t.Helper()
		root := committed(t, 1)
		head := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
		id := func(i int) string { return fmt.Sprintf("%040x", 0xabc000+i) }
		write(t, root, "refs/tags/v1", id(1)+"\n")
		for i := 1; i <= n; i++ {
			target := id(i + 1)
			if i == n {
				target = head
			}
			name := id(i)
			write(t, root, "objects/"+name[:2]+"/"+name[2:], deflate(t, "tag 48\x00object "+target+"\n"))
		}
		return headTags(t, root, "")
	}
	if got, err := peels(t, 16); err != nil || !slices.Equal(got, []string{"v1"}) {
		t.Errorf("16 tag objects: got %q, %v", got, err)
	}
	if _, err := peels(t, 17); err == nil || !strings.Contains(err.Error(), "leads through more than 16 tag objects") {
		t.Errorf("17 tag objects: got %v", err)
	}
}

// The limits on a ref's name hold at the limit and refuse a byte past
// it, in refs/tags and in packed-refs. Planted breaks: each compared with
// >=.
func TestHeadTagsNameLimit(t *testing.T) {
	root := committed(t, 1)
	// Longer than refs/heads/master, which pack-refs packs too.
	git(t, root, "tag", "v1234567890")
	at := Limits{Bytes: testLimits.Bytes, Entries: testLimits.Entries, Name: len("refs/tags/v1234567890")}
	if got, err := HeadTags(t.Context(), root, "", at); err != nil || len(got) != 1 {
		t.Errorf("loose, at the limit: got %q, %v", got, err)
	}
	at.Name--
	if _, err := HeadTags(t.Context(), root, "", at); err == nil || !strings.Contains(err.Error(), "refs/tags holds a name over") {
		t.Errorf("loose, past the limit: got %v", err)
	}
	git(t, root, "pack-refs", "--all")
	at.Name++
	if got, err := HeadTags(t.Context(), root, "", at); err != nil || len(got) != 1 {
		t.Errorf("packed, at the limit: got %q, %v", got, err)
	}
	at.Name--
	if _, err := HeadTags(t.Context(), root, "", at); err == nil || !strings.Contains(err.Error(), "packed-refs: line") {
		t.Errorf("packed, past the limit: got %v", err)
	}
}

// More than 1000 packs are refused, and 1000 are read. Planted break: the
// count compared with >=.
func TestHeadTagsPackLimit(t *testing.T) {
	for _, c := range []struct {
		packs int
		want  string
	}{
		{1000, "it is no version 2 pack index"},
		{1001, "the repository holds more than 1000 packs, the limit; git gc packs them into one"},
	} {
		root := committed(t, 2)
		git(t, root, "tag", "v1", "HEAD~1")
		write(t, root, "refs/tags/v1", strings.Repeat("9", 40)+"\n")
		for i := range c.packs {
			write(t, root, fmt.Sprintf("objects/pack/pack-%04d.idx", i), "")
		}
		if _, err := headTags(t, root, ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%d packs: got %v, want %q", c.packs, err, c.want)
		}
	}
}

// No tag on HEAD gives none: tags elsewhere, a repository that holds none,
// and a HEAD that names no commit yet. A shallow checkout with no tag is
// refused, since its fetch may have left HEAD's behind, and one that
// fetched the tag of HEAD lists it. Planted breaks: the shallow test
// dropped, which gave none; and the test made on every checkout, which
// refused the full clone with no tag.
func TestHeadTagsWithoutATagOnHEAD(t *testing.T) {
	t.Run("tags elsewhere", func(t *testing.T) {
		root := committed(t, 2)
		git(t, root, "tag", "v1", "HEAD~1")
		if got, err := headTags(t, root, ""); err != nil || got != nil {
			t.Errorf("got %q, %v, want none", got, err)
		}
	})
	t.Run("no tag", func(t *testing.T) {
		root := committed(t, 1)
		if got, err := headTags(t, root, ""); err != nil || got != nil {
			t.Errorf("got %q, %v, want none", got, err)
		}
	})
	t.Run("no tag matches the prefix", func(t *testing.T) {
		root := committed(t, 1)
		git(t, root, "tag", "v1")
		if got, err := headTags(t, root, "api/"); err != nil || got != nil {
			t.Errorf("got %q, %v, want none", got, err)
		}
	})
	t.Run("no refs/tags directory", func(t *testing.T) {
		root := committed(t, 1)
		if err := os.Remove(gitPath(root, "refs/tags")); err != nil {
			t.Fatal(err)
		}
		if got, err := headTags(t, root, ""); err != nil || got != nil {
			t.Errorf("got %q, %v, want none", got, err)
		}
	})
	t.Run("an unborn branch", func(t *testing.T) {
		root := committed(t, 1)
		git(t, root, "tag", "v1")
		git(t, root, "checkout", "-q", "--orphan", "fresh")
		if got, err := headTags(t, root, ""); err != nil || got != nil {
			t.Errorf("got %q, %v, want none", got, err)
		}
	})
	origin := committed(t, 3)
	git(t, origin, "tag", "-a", "-m", "m", "v1")
	git(t, origin, "tag", "v0", "HEAD~2")
	clone := func(t *testing.T, args ...string) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "c")
		git(t, origin, append(append([]string{"clone", "-q"}, args...), "file://"+origin, dir)...)
		return dir
	}
	t.Run("a shallow clone without tags", func(t *testing.T) {
		_, err := headTags(t, clone(t, "--depth=1", "--no-tags"), "")
		if !errors.Is(err, ErrShallowUntagged) {
			t.Errorf("got %v, want %v", err, ErrShallowUntagged)
		}
	})
	t.Run("a shallow clone with HEAD's tag", func(t *testing.T) {
		if got, err := headTags(t, clone(t, "--depth=1"), ""); err != nil || !slices.Equal(got, []string{"v1"}) {
			t.Errorf("got %q, %v, want v1", got, err)
		}
	})
	t.Run("a full clone without tags", func(t *testing.T) {
		if got, err := headTags(t, clone(t, "--no-tags"), ""); err != nil || got != nil {
			t.Errorf("got %q, %v, want none", got, err)
		}
	})
}

// gitPath is the file at rel in root's git directory.
func gitPath(root, rel string) string { return filepath.Join(root, ".git", filepath.FromSlash(rel)) }

// write writes data to the file at rel in root's git directory.
func write(t *testing.T, root, rel, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(gitPath(root, rel)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitPath(root, rel), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// What git would not write, or errata does not read, is refused, each
// for its reason; each case is the planted break of its refusal.
func TestHeadTagsRefuses(t *testing.T) {
	zero := strings.Repeat("0", 40)
	for _, c := range []struct {
		name  string
		init  []string
		setup func(t *testing.T, root string)
		want  string
	}{
		{"a reftable", []string{"--ref-format=reftable"}, func(t *testing.T, _ string) { t.Helper() },
			"the repository keeps its refs in a reftable, which errata does not read"},
		{"a packed-refs line of no ref", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "packed-refs", "# pack-refs with: peeled\n"+zero+" refs/tags/a\nnonsense\n")
		}, "packed-refs: line 3 is no line git writes"},
		{"a packed ref outside refs/", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "packed-refs", zero+" tags/a\n")
		}, "packed-refs: line 1 is no line git writes"},
		{"a packed ref of a short name", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "packed-refs", zero[:39]+" refs/tags/a\n")
		}, "packed-refs: line 1 is no line git writes"},
		{"a header past the first line", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "packed-refs", zero+" refs/tags/a\n# pack-refs with: peeled\n")
		}, "packed-refs: line 2 is no line git writes"},
		{"a ^ line first", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "packed-refs", "^"+zero+"\n")
		}, "packed-refs: line 1 is no line git writes"},
		{"two ^ lines", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "packed-refs", zero+" refs/tags/a\n^"+zero+"\n^"+zero+"\n")
		}, "packed-refs: line 3 is no line git writes"},
		{"a ^ line of no object name", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "packed-refs", zero+" refs/tags/a\n^"+strings.Repeat("G", 40)+"\n")
		}, "packed-refs: line 2 is no line git writes"},
		{"a packed ref name over the limit", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "packed-refs", zero+" refs/tags/"+strings.Repeat("n", maxFileName)+"\n")
		}, "packed-refs: line 1 is no line git writes"},
		{"a symlink in refs/tags", nil, func(t *testing.T, root string) {
			t.Helper()
			if err := os.Symlink("elsewhere", gitPath(root, "refs/tags/v1")); err != nil {
				t.Fatal(err)
			}
		}, `refs/tags/"v1" is neither a file nor a directory, which errata does not read as a ref`},
		{"a loose tag name over the limit", nil, func(t *testing.T, root string) {
			t.Helper()
			deep := strings.Repeat("d/", maxFileName/2)
			write(t, root, "refs/tags/"+deep[:200]+"v1", zero+"\n")
		}, ""},
		{"a HEAD of no ref", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1")
			write(t, root, "HEAD", "nonsense\n")
		}, `"HEAD" holds no object name and no ref: line, which git writes in a ref`},
		{"a HEAD outside refs/", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1")
			write(t, root, "HEAD", "ref: heads/main\n")
		}, `"HEAD" names "heads/main", which is no ref name git takes below refs/`},
		{"a loop of symbolic refs", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1")
			write(t, root, "HEAD", "ref: refs/heads/a\n")
			write(t, root, "refs/heads/a", "ref: refs/heads/b\n")
			write(t, root, "refs/heads/b", "ref: refs/heads/a\n")
		}, "leads through more than 5 symbolic refs, which git refuses"},
		{"a symbolic tag out of refs/", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "refs/tags/evil", "ref: refs/../../../secret\n")
		}, `"refs/tags/evil" names "refs/../../../secret", which is no ref name git takes below refs/`},
		{"a HEAD symlink out of refs/", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1")
			if err := os.Remove(gitPath(root, "HEAD")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("refs/../../x", gitPath(root, "HEAD")); err != nil {
				t.Fatal(err)
			}
		}, `"HEAD" names "refs/../../x", which is no ref name git takes below refs/`},
		{"a packed name with a space", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "packed-refs", zero+" refs/tags/a b\n")
		}, "packed-refs: line 1 is no line git writes"},
		{"a packed name with ..", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "packed-refs", zero+" refs/tags/../../x\n")
		}, "packed-refs: line 1 is no line git writes"},
		{"a loose name git refuses", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "refs/tags/.hidden", zero+"\n")
		}, `refs/tags/".hidden" is no ref name git takes`},
		{"refs/tags as a symlink", nil, func(t *testing.T, root string) {
			t.Helper()
			elsewhere := t.TempDir()
			if err := os.WriteFile(filepath.Join(elsewhere, "v1"), []byte(zero+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(gitPath(root, "refs/tags")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, gitPath(root, "refs/tags")); err != nil {
				t.Fatal(err)
			}
		}, "refs/tags is a symlink, which errata does not follow to read refs or packs"},
		{"refs as a symlink", nil, func(t *testing.T, root string) {
			t.Helper()
			moved := filepath.Join(t.TempDir(), "refs")
			if err := os.Rename(gitPath(root, "refs"), moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, gitPath(root, "refs")); err != nil {
				t.Fatal(err)
			}
		}, "refs is a symlink, which errata does not follow to read refs or packs"},
		{"objects/pack as a symlink", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1", "HEAD~1")
			write(t, root, "refs/tags/v1", strings.Repeat("1", 40)+"\n")
			moved := filepath.Join(t.TempDir(), "pack")
			if err := os.Rename(gitPath(root, "objects/pack"), moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, gitPath(root, "objects/pack")); err != nil {
				t.Fatal(err)
			}
		}, "objects/pack is a symlink, which errata does not follow to read refs or packs"},
		{"a symbolic tag to no ref", nil, func(t *testing.T, root string) {
			t.Helper()
			write(t, root, "refs/tags/v1", "ref: refs/tags/none\n")
		}, `"refs/tags/v1" leads to a ref the repository does not hold, which git ignores as broken`},
		{"a missing object", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1", "HEAD~1")
			write(t, root, "refs/tags/v1", strings.Repeat("1", 40)+"\n")
		}, "read the object " + strings.Repeat("1", 40) + ": it is in no loose object and no pack the repository holds"},
		{"a loose object of no header", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1", "HEAD~1")
			id := strings.Repeat("2", 40)
			write(t, root, "refs/tags/v1", id+"\n")
			write(t, root, "objects/22/"+id[2:], deflate(t, "tag"))
		}, "its loose object has no header git writes"},
		{"a loose object of no space in its header", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1", "HEAD~1")
			id := strings.Repeat("8", 40)
			write(t, root, "refs/tags/v1", id+"\n")
			write(t, root, "objects/88/"+id[2:], deflate(t, "tag\x00object "+id+"\n"))
		}, "its loose object has no header git writes"},
		{"a loose object that does not inflate", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1", "HEAD~1")
			id := strings.Repeat("3", 40)
			write(t, root, "refs/tags/v1", id+"\n")
			write(t, root, "objects/33/"+id[2:], "plain")
		}, "it is no zlib stream"},
		{"a tag object of no object line", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1", "HEAD~1")
			id := strings.Repeat("4", 40)
			write(t, root, "refs/tags/v1", id+"\n")
			write(t, root, "objects/44/"+id[2:], deflate(t, "tag 9\x00type tag\n"))
		}, "the tag object " + strings.Repeat("4", 40) + " does not start with the object line git writes"},
		{"a tag object that names a SHA-256 object in a SHA-1 repository", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1", "HEAD~1")
			id := strings.Repeat("5", 40)
			write(t, root, "refs/tags/v1", id+"\n")
			write(t, root, "objects/55/"+id[2:], deflate(t, "tag 72\x00object "+strings.Repeat("6", 64)+"\n"))
		}, "does not start with the object line git writes"},
		{"a loop of tag objects", nil, func(t *testing.T, root string) {
			t.Helper()
			git(t, root, "tag", "v1", "HEAD~1")
			id := strings.Repeat("7", 40)
			write(t, root, "refs/tags/v1", id+"\n")
			write(t, root, "objects/77/"+id[2:], deflate(t, "tag 48\x00object "+id+"\n"))
		}, `the tag "refs/tags/v1" leads through more than 16 tag objects`},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := committed(t, 2, c.init...)
			c.setup(t, root)
			_, err := headTags(t, root, "")
			if c.want == "" {
				c.want = "refs/tags holds a name over"
				_, err = HeadTags(t.Context(), root, "", Limits{Bytes: testLimits.Bytes, Entries: testLimits.Entries, Name: 100})
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want %q", err, c.want)
			}
		})
	}
}

// deflate is data as a zlib stream, as git writes a loose object.
func deflate(t *testing.T, data string) string {
	t.Helper()
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	if _, err := z.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The loose tags and the packed refs are held to l.Entries, and a run
// stops when its context ends. Planted breaks: each count compared with
// > l.Entries+1.
func TestHeadTagsLimits(t *testing.T) {
	root := committed(t, 1)
	for _, tag := range []string{"a", "b", "c"} {
		git(t, root, "tag", tag)
	}
	few := Limits{Bytes: testLimits.Bytes, Entries: 2, Name: testLimits.Name}
	if _, err := HeadTags(t.Context(), root, "", few); err == nil || err.Error() != "refs/tags holds more than 2 entries, the limit" {
		t.Errorf("loose: got %v", err)
	}
	if got, err := HeadTags(t.Context(), root, "", Limits{Bytes: few.Bytes, Entries: 3, Name: few.Name}); err != nil || len(got) != 3 {
		t.Errorf("at the limit: got %q, %v", got, err)
	}
	// packed-refs then holds 4 refs: the branch and the three tags.
	git(t, root, "pack-refs", "--all")
	if _, err := HeadTags(t.Context(), root, "", Limits{Bytes: few.Bytes, Entries: 3, Name: few.Name}); err == nil || err.Error() != "packed-refs lists more than 3 refs, the limit" {
		t.Errorf("packed: got %v", err)
	}
	if got, err := HeadTags(t.Context(), root, "", Limits{Bytes: few.Bytes, Entries: 4, Name: few.Name}); err != nil || len(got) != 3 {
		t.Errorf("packed, at the limit: got %q, %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := HeadTags(ctx, root, "", testLimits); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled run: got %v", err)
	}
	git(t, root, "tag", "d")
	if _, err := HeadTags(ctx, root, "", testLimits); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled walk: got %v", err)
	}
}

// writeIdx is a version 2 pack index of names, each 20 bytes, sorted,
// at offsets: one of 2^31 or more goes in the table of 8-byte offsets.
func writeIdx(names [][]byte, offsets []uint64) []byte {
	var b bytes.Buffer
	b.WriteString("\xfftOc\x00\x00\x00\x02")
	for i := range 256 {
		n := 0
		for _, name := range names {
			if int(name[0]) <= i {
				n++
			}
		}
		_ = binary.Write(&b, binary.BigEndian, uint32(n))
	}
	for _, name := range names {
		b.Write(name)
	}
	b.Write(make([]byte, 4*len(names))) // the CRCs
	var large []uint64
	for _, o := range offsets {
		if o < 1<<31 {
			_ = binary.Write(&b, binary.BigEndian, uint32(o))
		} else {
			_ = binary.Write(&b, binary.BigEndian, uint32(1<<31|len(large)))
			large = append(large, o)
		}
	}
	for _, o := range large {
		_ = binary.Write(&b, binary.BigEndian, o)
	}
	return b.Bytes()
}

// name20 is the 20-byte object name of hex digits h, repeated.
func name20(h string) []byte {
	b, _ := hex.DecodeString(strings.Repeat(h, 40/len(h)))
	return b
}

// lookup finds each object of an index at its offset, an offset of 2^31
// or more through the table of 8-byte offsets, and no object the index
// does not list, at either end of a fan out's span. Planted breaks: the
// search's halves swapped, which missed 10aa in the span of three; the
// high bit not masked off the large table's index; lo taken from the
// byte's own count; the large table read from the 4-byte offsets' start.
func TestLookupOfAnIndex(t *testing.T) {
	// Three names share their first byte, so the search takes steps.
	names := [][]byte{name20("00"), name20("10aa"), name20("10bb"), name20("10cc"), name20("ff")}
	offsets := []uint64{12, 1 << 33, 40, 3 << 31, 1<<31 - 1}
	idx := mustPack(t, writeIdx(names, offsets), "")
	for i, name := range names {
		at, found, err := idx.lookup(name)
		if err != nil || !found || uint64(at) != offsets[i] {
			t.Errorf("%x: got %d, %v, %v, want %d", name, at, found, err, offsets[i])
		}
	}
	for _, h := range []string{"01", "0f", "13", "fe", "20", "1000", "10ab", "10dd"} {
		if at, found, err := idx.lookup(name20(h)); err != nil || found {
			t.Errorf("%s: got %d, %v, %v, want none", h, at, found, err)
		}
	}
}

// A pack index that is not version 2, or whose counts the file cannot
// hold, is refused, and so is a pack that is not one. Each case is the
// planted break of its refusal.
func TestLookupRefuses(t *testing.T) {
	good := writeIdx([][]byte{name20("10")}, []uint64{12})
	for _, c := range []struct {
		name string
		data []byte
		want string
		hash int
	}{
		{"version 1", append([]byte("\xfftOc\x00\x00\x00\x01"), good[8:]...), "it is no version 2 pack index, the one git writes", 20},
		{"cut short", good[:100], "it is no version 2 pack index", 20},
		{"more objects than the file", func() []byte {
			d := slices.Clone(good)
			binary.BigEndian.PutUint32(d[8+4*255:], 50)
			return d
		}(), "read p.idx: its fan out lists more objects than the file holds", 20},
		{"a fan out past its total", func() []byte {
			d := slices.Clone(good)
			for b := 0x10; b < 255; b++ {
				binary.BigEndian.PutUint32(d[8+4*b:], 2)
			}
			return d
		}(), "its fan out is not in order", 20},
		{"a fan out out of order", func() []byte {
			d := slices.Clone(good)
			binary.BigEndian.PutUint32(d[8+4*0x0f:], 1)
			binary.BigEndian.PutUint32(d[8+4*0x10:], 0)
			return d
		}(), "its fan out is not in order", 20},
		{"a large offset past any file", func() []byte {
			return writeIdx([][]byte{name20("10")}, []uint64{1 << 63})
		}(), "it gives an offset past any file", 20},
		{"a SHA-256 name in an index of SHA-1 names", good, "its fan out lists more objects than the file holds", 32},
		{"the offsets cut short", good[:len(good)-4], "its fan out lists more objects than the file holds", 20},
		{"as many objects as the file may hold", func() []byte {
			// 1060 bytes hold at most 37 names of 28 bytes: open
			// takes 37, and lookup finds the names' tables past the end.
			d := slices.Clone(good)
			binary.BigEndian.PutUint32(d[8+4*255:], uint32(len(good)/28))
			return d
		}(), "its fan out lists more objects than the file holds", 20},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := testPack(t, c.data, "")
			if err == nil {
				name := bytes.Repeat([]byte{0x10}, max(c.hash, 20))
				_, _, err = p.lookup(name)
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want %q", err, c.want)
			}
			// Only open names the index; lookup's refusal is bare.
			if fromOpen := strings.HasPrefix(err.Error(), "read p.idx: "); fromOpen != strings.HasPrefix(c.want, "read p.idx: ") && c.name != "version 1" && c.name != "cut short" {
				t.Errorf("%s: the refusal %v comes from the other step", c.name, err)
			}
		})
	}
}

// testPack opens a pack of the index idx and of objects data, which start
// at 12, after the pack's header.
func testPack(t *testing.T, idx []byte, data string) (*pack, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.idx"), idx, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p.pack"), []byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01"+data), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := openPack(filepath.Join(dir, "p.idx"))
	if err == nil {
		t.Cleanup(p.close)
	}
	return p, err
}

// mustPack is testPack, failing the test on an error.
func mustPack(t *testing.T, idx []byte, data string) *pack {
	t.Helper()
	p, err := testPack(t, idx, data)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// object is the object at at in a pack of data, whose index lists the
// object of name20("10") at 12.
func object(t *testing.T, data string, at int64) (string, []byte, error) {
	t.Helper()
	p := mustPack(t, writeIdx([][]byte{name20("10")}, []uint64{12}), data)
	o := &objects{}
	return o.object(p, at, 20)
}

// objects.object reads an object's type and start through a header of several
// size bytes, rebuilds a delta from its base by offset or by name, and
// refuses what git does not write, each case the planted break of its
// refusal. Planted breaks of the reads: the type read from the low bits;
// the offset's added one dropped, which read the far base from the wrong
// place; the deltas of a chain applied outermost first, which made
// "object X" of the chain that makes "object Y".
func TestObjectOfAPack(t *testing.T) {
	body := deflate(t, "object "+strings.Repeat("a", 40)+"\ntype commit\n")
	// A base at 12 and a delta at 12+len(base) that copies the base's
	// first 7 bytes, then inserts "X".
	base := "\x40" + body
	delta := deflate(t, "\x30\x08\x90\x07\x01X")
	at := 12 + len(base)
	// The distance back, in git's encoding: the first byte's group is one
	// less than its value, for each byte after it.
	back := func(n int) string {
		if n < 128 {
			return string(rune(n))
		}
		return string([]byte{byte(0x80 | (n>>7 - 1)), byte(n & 0x7f)})
	}
	for _, c := range []struct {
		name, data, want, kind, start string
		from                          int64
	}{
		{"a tag, one size byte", "\x40" + body, "", "tag", "object ", 12},
		{"a commit, three size bytes", "\x9f\x80\x01" + body, "", "commit", "object ", 12},
		{"a blob", "\x30" + body, "", "blob", "object ", 12},
		{"a tree", "\x20" + body, "", "tree", "object ", 12},
		{"an offset delta", base + "\x60" + back(at-12) + delta, "", "tag", "object X", int64(at)},
		{"a reference delta", base + "\x70" + string(name20("10")) + delta, "", "tag", "object X", int64(at)},
		{"type 5", "\x50" + body, "its object at 12 is of type 5, which git does not write", "", "", 12},
		{"a header that runs on", strings.Repeat("\xff", 12) + body, "its object at 12 has no header git writes", "", "", 12},
		{"a header cut short", "\xff", "its object at 12 has no header git writes", "", "", 12},
		{"a header of 10 bytes", "\xc0" + strings.Repeat("\x80", 8) + "\x00" + body, "", "tag", "object ", 12},
		{"a header of 11 bytes", "\xc0" + strings.Repeat("\x80", 9) + "\x00" + body, "its object at 12 has no header git writes", "", "", 12},
		{"an offset of 9 bytes", "\x60" + strings.Repeat("\x80", 8) + "\x00", "its delta at 12 names a base outside the pack", "", "", 12},
		{"an offset of 10 bytes", "\x60" + strings.Repeat("\x80", 9) + "\x00", "its delta at 12 names no base git writes", "", "", 12},
		{"a base at the pack's start", "\x60\x0c" + delta, "its object at 0 is of type 5, which git does not write", "", "", 12},
		{"nothing at the offset", "", "its object at 12 is cut short", "", "", 12},
		{"an offset that runs on", "\x60" + strings.Repeat("\xff", 12), "its delta at 12 names no base git writes", "", "", 12},
		{"a base before the pack", "\x60\x0d" + delta, "its delta at 12 names a base outside the pack", "", "", 12},
		{"a base at the delta", "\x60\x00" + delta, "its delta at 12 names a base outside the pack", "", "", 12},
		{"a reference cut short", "\x70\x10\x10", "its delta at 12 is cut short", "", "", 12},
		{"a base the pack does not hold", "\x70" + string(name20("20")) + delta, "its delta names the base 2020", "", "", 12},
		{"a delta that does not inflate", base + "\x60" + back(at-12) + "plain", "it is no zlib stream", "", "", int64(at)},
	} {
		t.Run(c.name, func(t *testing.T) {
			kind, got, err := object(t, c.data, c.from)
			if c.want != "" {
				if err == nil || !strings.HasPrefix(err.Error(), c.want) {
					t.Errorf("got %v, want %q", err, c.want)
				}
				return
			}
			if err != nil || kind != c.kind || !strings.HasPrefix(string(got), c.start) {
				t.Errorf("got %s %q, %v, want a %s starting %q", kind, got, err, c.kind, c.start)
			}
		})
	}
	// A distance of two bytes: a base 200 bytes and more back.
	// A blob stored without compression, so the delta after it is 200
	// bytes and more from the base, and its distance takes two bytes.
	var stored bytes.Buffer
	z, _ := zlib.NewWriterLevel(&stored, zlib.NoCompression)
	_, _ = z.Write([]byte(strings.Repeat("p", 300)))
	_ = z.Close()
	far := base + "\x30" + stored.String()
	t.Run("a distance of two bytes", func(t *testing.T) {
		at := 12 + len(far)
		if at-12 < 128 {
			t.Fatalf("the distance is %d, which takes one byte", at-12)
		}
		kind, got, err := object(t, far+"\x60"+back(at-12)+delta, int64(at))
		if err != nil || kind != "tag" || string(got[:8]) != "object X" {
			t.Errorf("got %s %q, %v", kind, got, err)
		}
	})
	t.Run("a chain of two deltas", func(t *testing.T) {
		second := 12 + len(base) + 2 + len(delta)
		outer := deflate(t, "\x08\x08\x90\x07\x01Y")
		data := base + "\x60" + back(at-12) + delta + "\x60" + back(second-at) + outer
		kind, got, err := object(t, data, int64(second))
		if err != nil || kind != "tag" || string(got) != "object Y" {
			t.Errorf("got %s %q, %v, want a tag of object Y", kind, got, err)
		}
	})
	t.Run("a chain past the limit", func(t *testing.T) {
		_, _, err := object(t, "\x70"+string(name20("10"))+delta, 12)
		if err == nil || err.Error() != "its object leads through more than 4095 deltas" {
			t.Errorf("got %v", err)
		}
	})
	idx := filepath.Join(t.TempDir(), "p.idx")
	if err := os.WriteFile(idx, writeIdx([][]byte{name20("10")}, []uint64{12}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openPack(idx); err == nil || !strings.HasPrefix(err.Error(), "read p.pack: ") || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no pack file: got %v", err)
	}
	if err := os.WriteFile(strings.TrimSuffix(idx, ".idx")+".pack", []byte("KCAP\x00\x00\x00\x02"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openPack(idx); err == nil || err.Error() != "read p.pack: it does not start with PACK, git's signature" {
		t.Errorf("no pack: got %v", err)
	}
	if _, err := openPack(filepath.Join(t.TempDir(), "none.idx")); err == nil || !strings.HasPrefix(err.Error(), "read none.idx: ") {
		t.Errorf("no index: got %v", err)
	}
}

// An object rebuilt from a delta below the top of a chain is kept, so the
// next chain through it stops there, until the kept objects hold
// maxCached bytes; the top is peel's to remember. Planted breaks: keep
// never called, which read the middle delta again from the pack the test
// then broke; the bound compared with < maxCached.
func TestObjectsKeepRebuiltObjects(t *testing.T) {
	body := deflate(t, "object "+strings.Repeat("a", 40)+"\n")
	base := "\x40" + body
	delta := deflate(t, "\x30\x08\x90\x07\x01X")
	outer := deflate(t, "\x08\x08\x90\x07\x01Y")
	middle := int64(12 + len(base))
	top := middle + 2 + int64(len(delta))
	data := base + "\x60" + string(rune(len(base))) + delta + "\x60" + string(rune(top-middle)) + outer
	p := mustPack(t, writeIdx([][]byte{name20("10")}, []uint64{12}), data)
	o := &objects{}
	if _, got, err := o.object(p, top, 20); err != nil || string(got) != "object Y" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, ok := o.cache[cached{p, top}]; ok {
		t.Error("kept the top of the chain, which peel remembers")
	}
	// The middle delta's bytes are now broken: only what o kept rebuilds
	// the top.
	f, err := os.OpenFile(p.data.Name(), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("\xff\xff\xff"), middle); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if kind, got, err := o.object(p, top, 20); err != nil || kind != "tag" || string(got) != "object Y" {
		t.Errorf("again: got %s %q, %v", kind, got, err)
	}
	full := &objects{held: maxCached - 8}
	full.keep(p, 1, 4, []byte("12345678"))
	full.keep(p, 2, 4, []byte("9"))
	if _, ok := full.cache[cached{p, 1}]; !ok || full.held != maxCached {
		t.Errorf("at the bound: kept %v, %d bytes", full.cache, full.held)
	}
	if _, ok := full.cache[cached{p, 2}]; ok {
		t.Error("kept an object past maxCached")
	}
}

// patch applies a delta as git does, and refuses one git would not
// write. The base repeats every 251 bytes, a prime, so a copy from a
// wrong offset reads other bytes. Planted breaks: the offset's second
// byte shifted by 16; a size of 0 taken as 0; the copy check made
// > len(base)+1; a copy's room taken as maxBase+len(out).
func TestPatch(t *testing.T) {
	base := make([]byte, 300000)
	for i := range base {
		base[i] = byte(i % 251)
	}
	at := func(off, size int) string { return string(base[off : off+size]) }
	for _, c := range []struct {
		name, delta, want string
	}{
		{"an insert", "\x05\x02\x02hi", "hi"},
		{"a copy from the start", "\x05\x02\x90\x03", at(0, 3)},
		{"a copy at a 2-byte offset", "\x05\x02\x93\x2d\x01\x04", at(0x012d, 4)},
		{"a copy at a 3-byte offset, 2-byte size", "\x05\x02\xb7\x02\x00\x01\x04\x00", at(0x010002, 4)},
		{"a copy at a 4-byte offset", "\x05\x02\x9f\x00\x00\x00\x00\x01", at(0, 1)},
		{"a copy that ends at the base's end", "\x05\x02\x97\xdc\x93\x04\x04", at(299996, 4)},
		{"sizes of several bytes", "\x85\x01\x82\x01\x01z", "z"},
		{"two copies", "\x05\x02\x90\x05\x90\x05", at(0, 5) + at(0, 5)},
	} {
		got, err := patch(base, []byte(c.delta))
		if err != nil || string(got) != c.want {
			t.Errorf("%s: got %q, %v, want %q", c.name, got, err, c.want)
		}
	}
	for _, c := range []struct {
		name, delta, start string
	}{
		{"a size of 0, 0x10000 bytes", "\x05\x02\x80\x80", at(0, 10)},
		{"a size of 3 bytes, 0x010001", "\x05\x02\xf0\x01\x00\x01", at(0, 10)},
		{"an insert past maxBase", "\x05\x02\x80\x80\x80\x05abcde", at(0, 10)},
		{"an insert across maxBase", "\x05\x02\xb0\xfe\xff\x05abcde", ""},
		{"a copy across maxBase", "\x05\x02\x02ab\x80", "ab" + at(0, 10)},
	} {
		got, err := patch(base, []byte(c.delta))
		if err != nil || len(got) != maxBase || !strings.HasPrefix(string(got), c.start) {
			t.Errorf("%s: got %d bytes, %v, want maxBase", c.name, len(got), err)
		}
	}
	if got, _ := patch(base, []byte("\x05\x02\xb0\xfe\xff\x05abcde")); len(got) == maxBase && string(got[maxBase-2:]) != "ab" {
		t.Errorf("an insert across maxBase keeps the bytes that fit: ends %q", got[maxBase-2:])
	}
	for _, c := range []struct {
		name, delta, want string
	}{
		{"no sizes", "", "its delta is cut short"},
		{"one size", "\x05", "its delta is cut short"},
		{"a size that runs on", "\x85\x85", "its delta is cut short"},
		{"an insert cut short", "\x05\x02\x05ab", "its delta is cut short"},
		{"a copy cut short", "\x05\x02\x91\x01", "its delta is cut short"},
		{"instruction 0", "\x05\x02\x00", "its delta holds an instruction git does not write"},
		{"a copy past the base", "\x05\x02\x9c\xe0\x93\x04\x00\x01", "its delta copies past the part of its base errata reads"},
		{"a copy from inside the base past its end", "\x05\x02\x97\xde\x93\x04\x04", "its delta copies past the part of its base errata reads"},
	} {
		if _, err := patch(base, []byte(c.delta)); err == nil || err.Error() != c.want {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
	if got, err := patch(base[:4], []byte("\x05\x02\x90\x04")); err != nil || string(got) != at(0, 4) {
		t.Errorf("a copy to the base's end: got %q, %v", got, err)
	}
	if _, err := patch(base[:4], []byte("\x05\x02\x90\x05")); err == nil {
		t.Error("a copy one byte past the base's end was not refused")
	}
}

// inflate reads at most maxHead bytes of the object, from at most
// maxDeflated bytes of the file. Planted breaks: either limit dropped.
func TestInflateBounds(t *testing.T) {
	long := strings.Repeat("x", 10*maxHead)
	got, err := inflate(strings.NewReader(deflate(t, long)), maxHead)
	if err != nil || len(got) != maxHead {
		t.Errorf("a long object: got %d bytes, %v, want %d", len(got), err, maxHead)
	}
	// Empty stored blocks inflate to nothing: a stream of them past
	// maxDeflated bytes is cut there, though the final block after them
	// holds "abc" and the checksum is right.
	var b bytes.Buffer
	b.WriteString("\x78\x01")
	for b.Len() < maxDeflated+64 {
		b.WriteString("\x00\x00\x00\xff\xff")
	}
	b.WriteString("\x01\x03\x00\xfc\xffabc\x02\x4d\x01\x27")
	if _, err := inflate(&b, maxHead); err == nil || !strings.Contains(err.Error(), "it does not inflate") {
		t.Errorf("empty blocks: got %v", err)
	}
	// The work of an inflate is the stream's bytes, each counted as the
	// decompressor takes it, and the bytes it gives. Planted breaks: a
	// byte read not counted; the output subtracted.
	stream := deflate(t, "hello, tags")
	var w objects
	if got, err := w.inflate(strings.NewReader(stream+"trailing"), maxHead); err != nil || string(got) != "hello, tags" || w.work != int64(len(stream)+len("hello, tags")) {
		t.Errorf("work: got %q, %v, %d, want %d", got, err, w.work, len(stream)+len("hello, tags"))
	}
	// One reader, reset for each stream, a stream it refused among them.
	// Planted break: the Reset dropped, which read on in the first stream.
	var o objects
	for _, c := range []struct{ in, want string }{
		{deflate(t, "first"), "first"}, {"plain", ""}, {deflate(t, "second"), "second"}, {deflate(t, "third"), "third"},
	} {
		got, err := o.inflate(strings.NewReader(c.in), maxHead)
		if string(got) != c.want || (err != nil) != (c.want == "") {
			t.Errorf("%q: got %q, %v", c.want, got, err)
		}
	}
}

// refName takes what git's check_refname_format takes below refs/, and
// refuses each thing it refuses. Planted breaks, one a row: each test of
// refName dropped in turn.
func TestRefName(t *testing.T) {
	for _, name := range []string{"refs/tags/v1.2.3", "refs/heads/main", "refs/tags/api/v2.0.0", "refs/tags/a@b", "refs/tags/v1\u202e2", "refs/tags/a.lock.b"} {
		if !refName(name) {
			t.Errorf("%q refused", name)
		}
	}
	for _, name := range []string{
		"tags/v1", "refs", "refs/", "refs/tags/", "refs//a", "refs/tags/a/", "refs/tags/..", "refs/tags/../x",
		"refs/./x", "refs/tags/.v1", "refs/tags/v1.lock", "refs/tags/x.lock/y", "refs/tags/v1.",
		"refs/tags/a b", "refs/tags/a\tb", "refs/tags/a\x00", "refs/tags/a\x7f", "refs/tags/a~1",
		"refs/tags/a^", "refs/tags/a:b", "refs/tags/a?", "refs/tags/a*", "refs/tags/a[", "refs/tags/a\\b",
		"refs/tags/a@{1}",
	} {
		if refName(name) {
			t.Errorf("%q taken", name)
		}
	}
}

// varint is n in the 7-bit groups a delta writes its sizes in.
func varint(n int) string {
	var b []byte
	for n >= 0x80 {
		b = append(b, byte(n&0x7f|0x80))
		n >>= 7
	}
	return string(append(b, byte(n)))
}

// chainPack writes into root's git directory a pack and its index:
// chains chains, each a tag object of size bytes that names HEAD, then
// depth offset deltas over it, each a copy of the whole object, the shape
// a sender may choose. The index lists the top of chain c under
// name20 of the byte 0x10+c, which packed-refs, without traits, names as
// tags tags, vc-0, vc-1 and so on.
func chainPack(t *testing.T, root string, size, depth, chains, tags int) {
	t.Helper()
	head := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	delta := deflate(t, varint(size)+varint(size)+"\xb0"+string([]byte{byte(size), byte(size >> 8)}))
	pack := "PACK\x00\x00\x00\x02\x00\x00\x00\x01"
	var names [][]byte
	var tops []uint64
	var refs strings.Builder
	for c := range chains {
		body := fmt.Sprintf("object %s\ntype commit\ntag v%d\n\n", head, c)
		body += strings.Repeat("m", size-len(body))
		// A tag of size bytes: type 4, the size in the header's groups.
		header := []byte{0xc0 | byte(size&0x0f)}
		for n := size >> 4; n > 0; n >>= 7 {
			if n >= 0x80 {
				header = append(header, byte(n&0x7f|0x80))
			} else {
				header = append(header, byte(n))
			}
		}
		prev := len(pack)
		pack += string(header) + deflate(t, body)
		for range depth {
			at := len(pack)
			n := at - prev
			// The distance in git's encoding (TestObjectOfAPack's back).
			enc := []byte{byte(n & 0x7f)}
			for n >>= 7; n > 0; n >>= 7 {
				n--
				enc = append([]byte{byte(0x80 | n&0x7f)}, enc...)
			}
			pack += "\x60" + string(enc) + delta
			prev = at
		}
		name := name20(fmt.Sprintf("%02x", 0x10+c))
		names = append(names, name)
		tops = append(tops, uint64(prev))
		for i := range tags {
			fmt.Fprintf(&refs, "%x refs/tags/v%d-%d\n", name, c, i)
		}
	}
	write(t, root, "objects/pack/pack-chain.pack", pack)
	write(t, root, "objects/pack/pack-chain.idx", string(writeIdx(names, tops)))
	write(t, root, "packed-refs", refs.String())
}

// One budget holds the work of a whole read of the tags: a chain of 200
// deltas of a 4 KiB tag is 800 KiB written, which one read may do once,
// however many tags name the chain's top, and which a budget below it
// refuses. Planted breaks: peel's memory dropped, which rebuilt the
// chain for each of the 50 tags and went past the budget; patch's output
// not counted, which let the low budget pass; and the count compared
// with >= maxWork.
func TestHeadTagsWorkBudget(t *testing.T) {
	old := maxWork
	t.Cleanup(func() { maxWork = old })
	root := committed(t, 1)
	chainPack(t, root, 4<<10, 200, 1, 50)
	maxWork = 2 << 20
	got, err := headTags(t, root, "")
	if err != nil || len(got) != 50 {
		t.Fatalf("50 tags at one chain: got %d tags, %v", len(got), err)
	}
	o := objects{dir: gitPath(root, "objects")}
	defer o.close()
	if _, _, err := o.peel(hex.EncodeToString(name20("10"))); err != nil {
		t.Fatal(err)
	}
	maxWork = o.work
	if _, err := headTags(t, root, ""); err != nil {
		t.Errorf("a budget of exactly the chain's work: %v", err)
	}
	maxWork = 400 << 10
	if _, err := headTags(t, root, ""); err == nil || !strings.Contains(err.Error(), "the tags on HEAD took more than 400 KiB of objects inflated, rebuilt and looked up, the limit") {
		t.Errorf("a budget below one chain: got %v", err)
	}
}
