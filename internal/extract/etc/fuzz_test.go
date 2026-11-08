package etc

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// Every dialect's reader reads any file into entries in file order, each
// at a line inside the file that holds its keyword, with a value that holds
// no line end. A value is a part of its line, in lower case for limits,
// which lowers it, and where chrony folds white space, each of its words is; modprobe, whose \ joins lines, is held to
// the lines and the line ends alone. A read stops on no file but one past
// its work budget.
func FuzzEtc(f *testing.F) {
	f.Add("Port=22 #c\nMatch User git\n  X \"a #b\"\r\nHost=*\rInclude a b\n", uint8(0))
	f.Add("net/ipv4/ip_forward = 1\n -a.b=c=d\n;x\nnoequals\r\n", uint8(2))
	f.Add("nameserver 1.1.1.1#x\ndomain a b\nsearch\ta  b \n", uint8(3))
	f.Add("* soft nofile 1 # c\n@a\t-\tnproc 2\n", uint8(4))
	f.Add("/var/log/a {\n postrotate\n  x\n endscript\n rotate 1\n}\ncompress\n", uint8(5))
	f.Add("hosts: files dns # c\n:x\n", uint8(6))
	f.Add("options snd-x a=1 \\\n b=2\\\r#c\\\nblacklist", uint8(7))
	f.Add("\\\n0", uint8(7))   // a line joined to a \ alone (FuzzEtc, first run)
	f.Add("0 0 0 H", uint8(4)) // a value limits lowers (the gate on 6320dfc2)
	f.Add("Pool  a \t iburst\n%x\nconfdir /etc/chrony/conf.d\n", uint8(8))
	f.Add("server a # c\nincludefile x\n", uint8(9))
	f.Fuzz(func(t *testing.T, data string, which uint8) {
		d := dialects[int(which)%len(dialects)]
		raw := []byte(data)
		last := core.LastLine(raw)
		prev := 0
		m := core.NewMeter(len(raw))
		err := d.read(raw, m, func(e *entry) error {
			if e.line < 1 || e.line > last || e.line < prev {
				t.Fatalf("%s: an entry at line %d, after line %d, in a file of %d lines", d.name, e.line, prev, last)
			}
			prev = e.line
			checkFuzzEntry(t, d, raw, e)
			return nil
		})
		if err != nil && !errors.Is(err, core.ErrWork) {
			t.Fatalf("%s: %v", d.name, err)
		}
	})
}

// checkFuzzEntry holds an entry of raw, read in d, to FuzzEtc's rules for
// its key and value.
func checkFuzzEntry(t *testing.T, d *dialect, raw []byte, e *entry) {
	t.Helper()
	if bytes.ContainsAny(e.value, "\r\n") {
		t.Fatalf("%s: the value %q at line %d holds a line end", d.name, e.value, e.line)
	}
	if d == modprobeDialect {
		return // a \ joins lines, so the directive's words may be on the lines after
	}
	line := core.LineText(raw, e.line)
	if d != sysctlDialect && !strings.Contains(strings.ToLower(line), strings.ToLower(string(e.key))) {
		t.Fatalf("%s: line %d is %q, which does not hold the key %q", d.name, e.line, line, e.key)
	}
	parts := [][]byte{e.value}
	switch d {
	case chronyDialect:
		parts = bytes.Fields(e.value)
	case limitsDialect:
		line = string(lower(nil, []byte(line))) // pam_limits lowers the type, the item and the value, ASCII only
	}
	for _, p := range parts {
		if !strings.Contains(line, string(p)) {
			t.Fatalf("%s: line %d is %q, which does not hold the value %q", d.name, e.line, line, p)
		}
	}
}
