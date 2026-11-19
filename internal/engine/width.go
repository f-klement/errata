package engine

import (
	"io/fs"
	"os"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// hostRoot is the root the pool reads the CPU quota under; a test sets
// fixture files in its place.
var hostRoot fs.FS = os.DirFS("/")

// width is the most jobs that run at once: e.workers when a test sets it,
// else the CPUs the process may use (defaultWidth). Execute reads it once
// for the run.
func (e *Engine) width() int {
	if e.workers > 0 {
		return e.workers
	}
	return defaultWidth(runtime.GOMAXPROCS(0), os.Getenv("GOMAXPROCS"), hostRoot)
}

// defaultWidth is procs, GOMAXPROCS, lowered to the whole CPUs the cgroup
// v2 quota under root allows, and at least 1. Go rounds a quota up and to
// at least 2, so a pod limited to one CPU ran two workers that the kernel
// throttled, slower than one. A GOMAXPROCS the user sets in env is the
// width as it stands, when the runtime takes it: a base 10 number that
// fits in 32 bits and is above 0.
func defaultWidth(procs int, env string, root fs.FS) int {
	if n, err := strconv.ParseInt(env, 10, 32); err == nil && n > 0 {
		return procs
	}
	cpus, ok := cpuQuota(root)
	if !ok {
		return procs
	}
	return max(1, min(procs, cpus))
}

// cpuQuota is the whole CPUs the cgroup v2 quota allows the process under
// root, which holds proc and sys as a Linux host mounts them: the lowest
// cpu.max of its cgroup and each one above it, rounded down. ok is false
// with no quota: no cgroup v2 line in /proc/self/cgroup, every cpu.max
// "max", or a file errata cannot read, as on another OS.
func cpuQuota(root fs.FS) (cpus int, ok bool) {
	b, err := fs.ReadFile(root, "proc/self/cgroup")
	if err != nil {
		return 0, false
	}
	dir, found := unifiedCgroup(string(b))
	for found {
		if n, limited := cpuMax(root, path.Join("sys/fs/cgroup", dir, "cpu.max")); limited && (!ok || n < cpus) {
			cpus, ok = n, true
		}
		found = dir != "/"
		dir = path.Dir(dir)
	}
	return cpus, ok
}

// unifiedCgroup is the path of the cgroup v2 line ("0::/path") of a
// /proc/self/cgroup, cleaned and absolute. A path with a ".." element is
// none: the process sits outside its cgroup namespace's root, and the
// path under /sys/fs/cgroup would name another cgroup.
func unifiedCgroup(cgroups string) (string, bool) {
	for line := range strings.Lines(cgroups) {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "0::/"); ok {
			return path.Clean("/" + p), !slices.Contains(strings.Split(p, "/"), "..")
		}
	}
	return "", false
}

// cpuMax is the quota of a cpu.max file, "<quota> <period>" in
// microseconds, as whole CPUs rounded down. A quota of "max", a file that
// is missing or does not read so, is no limit.
func cpuMax(root fs.FS, name string) (int, bool) {
	b, err := fs.ReadFile(root, name)
	if err != nil {
		return 0, false
	}
	quota, period, ok := strings.Cut(strings.TrimSpace(string(b)), " ")
	q, qerr := strconv.Atoi(quota)
	p, perr := strconv.Atoi(period)
	if !ok || qerr != nil || perr != nil || q <= 0 || p <= 0 {
		return 0, false
	}
	return q / p, true
}
