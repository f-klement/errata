// Package heapcap stops a test binary whose Go heap and stacks pass a cap.
// Gremlins reruns a package's tests once for every mutant, and a mutant that
// breaks a bound or a loop condition can make one test allocate without
// limit, faster than the per mutant timeout fires: one such mutant took a
// test binary from 20 MB to 3.4 GB in ten seconds and the kernel killed the
// whole run. With the watchdog the binary exits instead, and Gremlins counts
// the failed run as the mutant killed, which it is: a test noticed the broken
// bound.
//
// Only test files built with the mutation tag import it, each package's
// mutation_test.go, so make test, make cover and -race link it into no other
// package's tests. make test does build and test heapcap itself; the
// watchdog is armed only by init in mutation.go, under the tag, so there it
// runs only in the child processes its tests start.
// TestEveryTestedPackageLinksTheWatchdog holds every package with tests to
// that import.
package heapcap

import (
	"fmt"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// Cap is the Go heap and stacks, in bytes, past which a test binary built
// with the mutation tag exits. Each package's tests under the tag were
// measured from gctrace on 2026-10-18, at GOMAXPROCS 2 and 8 alike: extract
// peaks at 67 MB of heap (90 MiB resident) and 1 MB of stacks, report at
// 36 MB, rule at 8 and the rest under 5. So 256 MiB is four times the
// largest. Above it, the cap has to fit two workers in CI's job. A binary
// stopped at the cap is resident for more than the cap: the heap grows
// between two readings, and one growth of a large slice, a bytes.Buffer or a
// map allocates the new array beside the old one. The largest measured was a
// bytes.Buffer, stopped at 448 MiB of heap and 519 MiB resident. Two such
// binaries take about 1 GiB; beside them, the go tool, the compiler, the
// linker and Gremlins took 450 MiB in the run the kernel killed. That fits a
// 2 GiB job with room for the page cache, where a cap of 512 MiB would not.
// cli is the exception: each errata process its scripts start carries its
// own cap, so a cli mutant that makes errata itself allocate without limit
// can hold GOMAXPROCS+1 capped processes in one worker.
//
// A cap below a normal peak cannot pass as killed mutants: Gremlins first
// runs every test under the tag for coverage, and a binary the watchdog stops
// fails that run.
const Cap = 256 << 20

// maxStack is the largest stack one goroutine may grow under the mutation
// tag; past it the runtime ends the binary with a stack overflow, which go
// test reports as a failure. A mutant that recurses without end otherwise
// grows one stack toward the runtime's default of 1 GB, and was resident for
// 1031 MiB when it died there: a stack doubles by copying, within
// milliseconds, so between two readings it can pass the cap by hundreds of
// MB. The deepest normal goroutine, in extract's TestReplayMeasuredInputs,
// needs between 1 and 2 MiB: it fails under a 1 MiB bound and passes under 2.
// A recursion stopped here was resident for 133 MiB.
const maxStack = 64 << 20

// poll is how often the watchdog reads the heap and stacks. The runaway
// mutant grew at about 330 MB a second, so a reading every 10ms finds it
// within 4 MB of the cap; one growth of a large slice, buffer or map can
// carry it further, which Cap allows for. A reading took 0.56µs on a
// developer's machine, so the watchdog costs a test binary under 0.01% of one
// core, and it stops no other goroutine.
const poll = 10 * time.Millisecond

// exitCode is the watchdog's status, neither a failed test's 1 nor a panic's
// 2, so the status alone names the watchdog. go test reports any failing
// binary as its own exit 1, which Gremlins counts as killed.
const exitCode = 3

// metricNames are what the cap counts: the bytes of heap objects, live ones
// and dead ones not yet swept, which is what the heap holds now, and the
// bytes of goroutine stacks, which a leak of blocked goroutines grows
// without touching the heap. The live heap metric only moves at the end of a
// collection, which a runaway outpaces.
var metricNames = []string{
	"/memory/classes/heap/objects:bytes",
	"/memory/classes/heap/stacks:bytes",
}

// armed is the limit the last call of arm set, for the test that holds init
// to Cap.
var armed uint64

// arm bounds each goroutine's stack at maxStack and runs the watchdog for
// limit in its own goroutine, for the life of the process.
func arm(limit uint64) {
	debug.SetMaxStack(maxStack)
	armed = limit
	go func() {
		tick := time.NewTicker(poll)
		used := watch(limit, tick.C, reader())
		tick.Stop()
		stop(used, limit)
	}()
}

// reader returns a function that reads the heap and stacks from
// runtime/metrics, which does not stop the world as runtime.ReadMemStats
// does.
func reader() func() uint64 {
	samples := make([]metrics.Sample, len(metricNames))
	for i, name := range metricNames {
		samples[i].Name = name
	}
	return func() uint64 {
		metrics.Read(samples)
		var sum uint64
		for _, s := range samples {
			sum += s.Value.Uint64()
		}
		return sum
	}
}

// watch reads the memory at every tick and returns the first reading above
// limit, or 0 when tick closes first.
func watch(limit uint64, tick <-chan time.Time, read func() uint64) uint64 {
	for range tick {
		if used := read(); used > limit {
			return used
		}
	}
	return 0
}

// message is the one line the watchdog prints before the binary exits.
func message(used, limit uint64) string {
	return fmt.Sprintf("heapcap: the Go heap and stacks hold %d MiB, past the mutation run's cap of %d MiB: "+
		"a test allocates without limit, so the binary exits (internal/heapcap)", used>>20, limit>>20)
}

// stop prints message to stderr and exits the process at once, from any
// goroutine, without waiting for the running tests.
func stop(used, limit uint64) {
	_, _ = fmt.Fprintln(os.Stderr, message(used, limit))
	os.Exit(exitCode)
}
