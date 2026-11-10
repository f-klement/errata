package sudoers

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The reader reads any file into Defaults values, aliases and user
// specifications, or refuses it at one of its lines. Every value it keeps
// is at a line inside the file, holds no line end, and, for an entry
// written without quotes, escapes or a template's expression, is a part of
// its line. A read stops on no file but one past its work budget or one
// sudo refuses to parse.
func FuzzSudoers(f *testing.F) {
	f.Add("Defaults:alice,%x !a, secure_path=\"/a:/b\" # c\nalice ALL=(root:adm) NOPASSWD: /bin/ls, \\\n /bin/x\r")
	f.Add("User_Alias A = b, !c : D = e\nCmnd_Alias C = sha256:x= /a b\nA,%#1 web1, fe80::1 = (ALL) TIMEOUT=1 C : h = !C\n")
	f.Add("#include x\n@includedir /etc/sudoers.d\n#1000 ALL = (#0) \"\" \"a b\"\n")
	f.Add("{{ u }} ALL = /a\n{% if x %}\nDefaults x={{ y }}\n")
	f.Add("a b = (\na b = (c :\nDefaults >\n\\")
	f.Fuzz(func(t *testing.T, data string) {
		raw := []byte(data)
		last := core.LastLine(raw)
		tr := &tree{setting: "secure_path", keepsSpecs: true}
		r := &reading{file: "sudoers"}
		err := statements(raw, true, core.NewMeter(len(raw)), func(s *stmt) error {
			if s.include != nil {
				checkFuzzLine(t, raw, last, s.include.line, string(s.include.rest), true)
				return nil
			}
			return tr.statement(r, s)
		})
		var lineErr bool
		if err != nil {
			lineErr = strings.HasPrefix(err.Error(), "line ")
		}
		if err != nil && !errors.Is(err, core.ErrWork) && !lineErr {
			t.Fatalf("%v", err)
		}
		for _, p := range tr.params {
			checkFuzzLine(t, raw, last, p.at.Line, p.text, false)
		}
		for _, u := range tr.specs {
			checkFuzzSpec(t, raw, last, u)
		}
		for _, aliases := range tr.aliases {
			for _, a := range aliases {
				checkFuzzMembers(t, raw, last, a.members)
			}
		}
	})
}

// checkFuzzSpec holds the entries of a user specification to FuzzSudoers'
// rules.
func checkFuzzSpec(t *testing.T, raw []byte, last int, u userSpec) {
	t.Helper()
	checkFuzzMembers(t, raw, last, u.users)
	for _, p := range u.privs {
		checkFuzzMembers(t, raw, last, p.hosts)
		for _, cs := range p.cmnds {
			checkFuzzMembers(t, raw, last, append(append([]member{cs.cmd}, cs.runas...), cs.groups...))
			for _, s := range append(cs.tags[:], cs.options[:]...) {
				if s.text != "" {
					checkFuzzLine(t, raw, last, s.at.Line, s.text, false)
				}
			}
		}
	}
}

// checkFuzzMembers holds entries to FuzzSudoers' rules.
func checkFuzzMembers(t *testing.T, raw []byte, last int, ms []member) {
	t.Helper()
	for _, m := range ms {
		checkFuzzLine(t, raw, last, m.at.Line, m.text, !strings.Contains(m.text, " "))
	}
}

// checkFuzzLine fails unless text is at a line of raw, holds no line end,
// and, when inLine is set and its line holds no quote, escape or {{, is a
// part of that line.
func checkFuzzLine(t *testing.T, raw []byte, last, line int, text string, inLine bool) {
	t.Helper()
	if line < 1 || line > last {
		t.Fatalf("%q at line %d, in a file of %d lines", text, line, last)
	}
	if strings.ContainsAny(text, "\r\n") {
		t.Fatalf("%q at line %d holds a line end", text, line)
	}
	l := extracttest.RawLine(raw, line)
	if inLine && !strings.ContainsAny(l, "\"\\{") && !strings.Contains(l, text) {
		t.Fatalf("line %d is %q, which does not hold %q", line, l, text)
	}
}

// The value limit holds over Defaults values and over records, each
// record's fields counted, so one record of ten nodes passes a limit of
// one. Planted break: count leaving out the record's
// nodes.
func TestValueLimit(t *testing.T) {
	files := map[string]string{"sudoers": "Defaults a\nDefaults a\nalice ALL = /a\n"}
	for _, s := range []Spec{defaults("sudoers", "a"), user("sudoers", "alice")} {
		b, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		x := b.(extractor)
		x.valueLimit = 1
		_, err = x.Extract(t.Context(), extracttest.Tracked(t, files))
		if err == nil || !strings.Contains(err.Error(), "the extractor selects more than") {
			t.Errorf("%+v: %v, want the value limit", s, err)
		}
	}
}

// An alias that doubles at each step, to a million commands, passes the
// work budget before its entries are built. Planted break: entry not charging the meter.
func TestAliasesBoundTheirWork(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("Cmnd_Alias C0 = /a\n")
	for i := 1; i < 21; i++ {
		b.WriteString("Cmnd_Alias C" + strconv.Itoa(i) + " = C" + strconv.Itoa(i-1) + ", C" + strconv.Itoa(i-1) + "\n")
	}
	b.WriteString("alice ALL = C20\n")
	_, err := extract(t, map[string]string{"sudoers": b.String()}, user("sudoers", "alice"))
	if err == nil || !strings.Contains(err.Error(), "units of work") {
		t.Errorf("got %v, want the work budget", err)
	}
}
