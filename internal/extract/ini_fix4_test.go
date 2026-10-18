package extract

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"testing"
)

// A read that spends its budget is refused for its work, whichever loop
// spends the last unit, and a read with budget enough reads what an
// unmetered read reads. Each input is read under every budget from 0 to
// its cost. The planted break is php's syntax refusal ahead of the work
// refusal, for a budget that runs out inside a value.
func TestWorkMeterRefusesAtEveryBudget(t *testing.T) {
	for _, c := range []struct {
		body string
		spec Spec
	}{
		{setupCfg, ini("setup.cfg", "options", "install_requires")},
		{"[testenv]\ndeps = pytest # pin\n  coverage\ncommands = pytest \\\n  -q {posargs}\n", ini("tox.ini", "testenv", "commands")},
		{"[PHP]\na = x \"y ; z\" 'w' ; c\nb = E_ALL & ~E_DEPRECATED\nc = ${HOME:-x}\nd[] = on\ne = 1 2\nc = 128M\n", ini("php.ini", "PHP", "c")},
		{gitconfig, gitSpec(".gitconfig", "remote", ptr("origin"), "fetch")},
		{inventory, Spec{INI: &INI{File: "inventory/hosts", Section: "web", Host: "web3.example.com", Key: "x"}}},
		{"# c\nserver.port=8080\nspring.datasource.url=jdbc:x \\\n  y\n", props("server.port")},
	} {
		e, err := Build(c.spec)
		if err != nil {
			t.Fatal(err)
		}
		x := e.(entryExtractor)
		data := []byte(c.body)
		read := func(m *meter) ([]string, error) {
			var got []string
			match := newMatcher(&x.query, m)
			err := x.read(data, m, func(en *iniEntry) error {
				if match.wants(en) {
					got = append(got, fmt.Sprintf("%s@%d", en.value, en.line))
				}
				return match.err
			})
			return got, err
		}
		want, err := read(nil)
		if err != nil {
			t.Fatalf("%s: %v", x.file, err)
		}
		full := &meter{budget: 1 << 40}
		if _, err := read(full); err != nil {
			t.Fatalf("%s: %v", x.file, err)
		}
		for budget := range full.spent {
			if _, err := read(&meter{budget: budget}); !errors.Is(err, errWork) {
				t.Fatalf("%s: a budget of %d of %d units: %v, want the work refusal", x.file, budget, full.spent, err)
			}
		}
		if got, err := read(&meter{budget: full.spent}); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: at its cost: %q, %v; want %q", x.file, got, err, want)
		}
	}
}

// properties decodes a line of bytes that are not UTF-8, three bytes each
// as U+FFFD, into one buffer sized once, and reads its escapes in place: a
// file of 256 KiB of them allocates three times its bytes. Growing the line
// by appends allocates 16 times, and a value buffer of its own 19 times;
// decoding the whole file first as well peaked at 1.76 GB at 64 MiB. A
// value joined over 16384 lines grows its buffer by doubling, under eight
// times its bytes; sized exactly for each line, it copied the value at
// every line, 128 MiB. The planted breaks are those three.
func TestPropertiesDecodesALineOnce(t *testing.T) {
	for _, c := range []struct {
		data        []byte
		value, most int
	}{
		{append([]byte("a="), bytes.Repeat([]byte{0xff}, 256<<10)...), 3 * (256 << 10), 4 * (256 << 10)},
		{append([]byte("a="), bytes.Repeat([]byte("x\\\n  "), 16384)...), 16384, 8 * 16384},
	} {
		// The fewest bytes of five reads, since the count is the
		// process's and a goroutine another test left can allocate
		// during one.
		least := uint64(1 << 62)
		for range 5 {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			n := 0
			err := readProperties(c.data, nil, func(e *iniEntry) error { n += len(e.value); return nil })
			runtime.ReadMemStats(&after)
			if err != nil || n != c.value {
				t.Fatalf("%d bytes, %v", n, err)
			}
			least = min(least, after.TotalAlloc-before.TotalAlloc)
		}
		if least > uint64(c.most) {
			t.Errorf("allocated %d bytes for a value of %d, over %d", least, c.value, c.most)
		}
	}
}
