package engine

import (
	"context"
	"io/fs"
	"runtime"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/f-klement/errata/internal/check"
)

// host is a root with /proc/self/cgroup holding cgroup and each cpu.max
// under /sys/fs/cgroup at its cgroup's path.
func host(cgroup string, cpuMax map[string]string) fstest.MapFS {
	root := fstest.MapFS{"proc/self/cgroup": {Data: []byte(cgroup)}}
	for dir, limit := range cpuMax {
		root["sys/fs/cgroup"+dir+"/cpu.max"] = &fstest.MapFile{Data: []byte(limit + "\n")}
	}
	return root
}

// The pool's default width is GOMAXPROCS lowered to the whole CPUs of the
// cgroup v2 quota, and at least 1. Planted breaks: the quota rounded up,
// which ran two workers on a pod of 1500m; no floor of 1, which ran none
// under half a core; the quota above GOMAXPROCS taken as it stands; only
// the process's own cgroup read, which missed its parent's limit; a
// GOMAXPROCS in env ignored, which lowered the width the user set; the
// env read with Atoi, which took a value past 32 bits that Go ignores; a
// path with ".." cleaned, which read the quota of another cgroup.
func TestTheDefaultWidthIsTheCPUQuotaRoundedDown(t *testing.T) {
	const kube = "0::/kubepods/pod1/ctr\n"
	for _, tc := range []struct {
		name  string
		procs int
		env   string
		root  fstest.MapFS
		want  int
	}{
		{"one core, two visible", 2, "", host("0::/\n", map[string]string{"": "100000 100000"}), 1},
		{"1500m rounds down", 2, "", host("0::/\n", map[string]string{"": "150000 100000"}), 1},
		{"2500m on eight cores", 8, "", host("0::/\n", map[string]string{"": "250000 100000"}), 2},
		{"half a core is still one", 2, "", host("0::/\n", map[string]string{"": "50000 100000"}), 1},
		{"a quota above GOMAXPROCS", 2, "", host("0::/\n", map[string]string{"": "400000 100000"}), 2},
		{"no quota", 4, "", host("0::/\n", map[string]string{"": "max 100000"}), 4},
		{"a parent's quota", 8, "", host(kube, map[string]string{"/kubepods/pod1/ctr": "max 100000", "/kubepods/pod1": "300000 100000", "/kubepods": "600000 100000"}), 3},
		{"the lowest of the path", 8, "", host(kube, map[string]string{"/kubepods/pod1/ctr": "200000 100000", "/kubepods": "500000 100000", "": "100000 100000"}), 1},
		{"GOMAXPROCS in env", 2, "2", host("0::/\n", map[string]string{"": "100000 100000"}), 2},
		{"an env GOMAXPROCS Go ignores", 2, "0", host("0::/\n", map[string]string{"": "100000 100000"}), 1},
		{"an env GOMAXPROCS past 32 bits", 2, "2147483648", host("0::/\n", map[string]string{"": "100000 100000"}), 1},
		{"an env GOMAXPROCS of 32 bits", 2, "2147483647", host("0::/\n", map[string]string{"": "100000 100000"}), 2},
		{"a path out of the namespace's root", 4, "", host("0::/../x\n", map[string]string{"/x": "100000 100000"}), 4},
		{"a .. inside the path", 4, "", host("0::/a/../x\n", map[string]string{"/x": "100000 100000"}), 4},
		{"cgroup v1 only", 4, "", host("4:cpu,cpuacct:/a\n", map[string]string{"/a": "100000 100000"}), 4},
		{"v1 lines before the v2 one", 4, "", host("4:cpu:/a\n0::/b\n", map[string]string{"/b": "200000 100000"}), 2},
		{"no /proc", 4, "", fstest.MapFS{}, 4},
		{"no cpu.max", 4, "", host("0::/\n", nil), 4},
		{"a cpu.max of one field", 4, "", host("0::/\n", map[string]string{"": "100000"}), 4},
		{"a quota that is no number", 4, "", host("0::/\n", map[string]string{"": "x 100000"}), 4},
		{"a period that is no number", 4, "", host("0::/\n", map[string]string{"": "100000 x"}), 4},
		{"a period of zero", 4, "", host("0::/\n", map[string]string{"": "100000 0"}), 4},
		{"a quota of zero", 4, "", host("0::/\n", map[string]string{"": "0 100000"}), 4},
	} {
		if got := defaultWidth(tc.procs, tc.env, tc.root); got != tc.want {
			t.Errorf("%s: width %d, want %d", tc.name, got, tc.want)
		}
	}
}

// The pool reads the quota under the root of the host's file system, where
// Linux has /proc/self/cgroup. Planted break: hostRoot a directory that
// does not exist, which fell back to GOMAXPROCS on every host, silently.
func TestThePoolReadsTheHostsCgroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/self/cgroup is Linux's")
	}
	if _, err := fs.ReadFile(hostRoot, "proc/self/cgroup"); err != nil {
		t.Errorf("the pool's root does not hold the process's cgroup: %v", err)
	}
}

// countedFS is a root that counts each file opened in it.
type countedFS struct {
	fs.FS
	opened map[string]int
}

func (c countedFS) Open(name string) (fs.File, error) {
	c.opened[name]++
	return c.FS.Open(name)
}

// A run reads the quota once, however many checks run alone between the
// rules. Planted break: the width read in pool, once for each stretch of
// rules, which read /proc/self/cgroup three times here.
func TestARunReadsTheQuotaOnce(t *testing.T) {
	t.Setenv("GOMAXPROCS", "")
	defer func(was fs.FS) { hostRoot = was }(hostRoot)
	root := countedFS{host("0::/\n", map[string]string{"": "100000 100000"}), map[string]int{}}
	hostRoot = root
	checks := rules("a", "b", "c", "d")
	checks = slices.Insert(checks, 1, check.Check{ID: "p1", Scope: check.ScopeRepo, Kind: &check.Program{}})
	checks = slices.Insert(checks, 3, check.Check{ID: "p2", Scope: check.ScopeRepo, Kind: &check.Program{}})
	e := &Engine{Run: func(_ context.Context, _ string, c check.Check) check.Result {
		return check.Result{Check: c, Verdict: check.VerdictTrue}
	}}
	if rep := e.Execute(t.Context(), checks); len(rep.Results) != 6 {
		t.Fatalf("%d results for 6 checks", len(rep.Results))
	}
	if n := root.opened["proc/self/cgroup"]; n != 1 {
		t.Errorf("the run read /proc/self/cgroup %d times, want once", n)
	}
}
