package udev

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// The reader reads any file into rules whose lines are inside the file and
// in order, each part of a rule the text of its line after the white
// space before it, and each key of a rule at its place in the rule. A
// value written without e holds no line end. A read stops on no file but
// one past its work budget, one with a NUL byte and one with a line udev
// stops at.
func FuzzUdev(f *testing.F) {
	f.Add("SUBSYSTEM==\"usb\", ATTR{idVendor}==\"046d\", MODE=\"0666\"\n")
	f.Add("KERNEL==\"sd*\", \\\n# c\n  SYMLINK+=\"a\\\"b\" \\\r\n\\\n\rGOTO=\"x\"\nLABEL=\"x\"")
	f.Add("ENV{A}=e\"\\x41\\u00e9\\101\"i\"x\",,RUN{builtin}+=\"kmod\"")
	f.Add("\\\n\\\n\\")
	f.Add("A{=\"\nB{}}=\"\nC  :=  i\"")
	f.Add("K==ie\"\\n\", L==ei\"x\"")
	f.Add("0 ")                   // blanks at a line's end, which core.LineText takes off (the gate on 3850d35e)
	f.Add("0\\\\\n\n x \\\n y\n") // an empty line takes the \ a doubled \\ kept
	f.Add("\\\\\\\n\n\n\xdc")     // two empty lines take one \ each (make fuzz on 9bb4a819)
	f.Fuzz(func(t *testing.T, data string) {
		raw := []byte(data)
		last, prev := core.LastLine(raw), 0
		err := eachRule(raw, core.NewMeter(len(raw)), func(l *logical) error {
			for i, p := range l.parts {
				if p.line < 1 || p.line > last || p.line <= prev {
					t.Fatalf("a line %d after line %d in a file of %d lines", p.line, prev, last)
				}
				prev = p.line
				end := len(l.text)
				if i+1 < len(l.parts) {
					end = l.parts[i+1].off
				}
				line := bytes.TrimLeft(rawLine(raw, p.line), whitespace)
				if !bytes.HasPrefix(line, l.text[p.off:end]) {
					t.Fatalf("line %d is %q, which does not start with the rule's part %q", p.line, line, l.text[p.off:end])
				}
			}
			return checkTokens(t, l)
		})
		if err != nil && !errors.Is(err, core.ErrWork) && !strings.Contains(err.Error(), "NUL byte") && !strings.Contains(err.Error(), "bytes long") {
			t.Fatal(err)
		}
	})
}

// rawLine is line n of data as udev reads it: without its line end, but
// with the spaces and tabs at its end and a byte order mark at its start,
// which core.LineText takes off and udev keeps.
func rawLine(data []byte, n int) []byte {
	start := 0
	for range n - 1 {
		start = core.NextLine(data, start)
	}
	return bytes.TrimRight(data[start:core.NextLine(data, start)], "\r\n")
}

// checkTokens holds the keys of the rule l to FuzzUdev's rules.
func checkTokens(t *testing.T, l *logical) error {
	t.Helper()
	err := eachToken(l, nil, func(tk *token) error {
		if !bytes.HasPrefix(l.text[tk.off:], []byte(tk.key)) {
			t.Fatalf("the key %q is not at %d of %q", tk.key, tk.off, l.text)
		}
		if prefix, _, _ := bytes.Cut(tk.written, []byte(`"`)); !bytes.Contains(prefix, []byte("e")) && strings.ContainsAny(tk.value, "\r\n") {
			t.Fatalf("the value %q holds a line end", tk.value)
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDrop) {
		t.Fatal(err)
	}
	return nil
}
