package udev

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// show lists values as file:line key=text, with the condition and a not
// literal mark when a value has them.
func show(vs []check.Value) string {
	var parts []string
	for _, v := range vs {
		s := fmt.Sprintf("%s:%d %s%s", v.At.File, v.At.Line, v.Key, v.Text)
		if v.Condition != "" {
			s += " [" + v.Condition + "]"
		}
		if v.NotLiteral {
			s += " !lit"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " | ")
}

// extract builds s and reads it from a repository of files git tracks.
func extract(t *testing.T, files map[string]string, s Spec) (string, error) {
	t.Helper()
	x, err := Build(s)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	vs, err := x.Extract(t.Context(), extracttest.Tracked(t, files))
	return show(vs), err
}

type readCase struct {
	name  string
	files map[string]string
	spec  Spec
	want  string
}

func runReads(t *testing.T, cases []readCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := extract(t, c.files, c.spec)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func one(file, key string) Spec { return Spec{Where: core.Where{File: file}, Key: key} }

func rules(body string) map[string]string { return map[string]string{"r.rules": body} }

// A rule is a line of keys and values, the value at the line of its key and
// the rule's match keys its condition, as written. Planted break: a value's
// line taken from the rule's first line.
func TestRulesReadAsUdevReadsThem(t *testing.T) {
	runReads(t, []readCase{
		{"keys and values", rules("# c\nSUBSYSTEM==\"usb\", ATTR{idVendor}==\"046d\", MODE=\"0666\", GROUP=\"plugdev\"\n"),
			one("r.rules", "MODE"), `r.rules:2 MODE=0666 [SUBSYSTEM=="usb", ATTR{idVendor}=="046d"]`},
		{"space and commas", rules("  KERNEL == \"sda\" ,,MODE:=  \"0600\"OWNER=\"root\"\n"),
			one("r.rules", "MODE"), `r.rules:1 MODE:=0600 [KERNEL=="sda"]`},
		{"a \\ joins lines, a comment between them passed over", rules("KERNEL==\"sd*\", \\\n# note\n   SYMLINK+=\"disk/%k\", \\\n  SYMLINK+=\"x\"\n"),
			one("r.rules", "SYMLINK"), `r.rules:3 SYMLINK+=disk/%k [KERNEL=="sd*"] !lit | r.rules:4 SYMLINK+=x [KERNEL=="sd*"]`},
		{"an empty line after a doubled \\ takes the kept \\", rules("KERNEL==\"a\",\\\\\n\n MODE=\"1\"\n"),
			one("r.rules", "MODE"), `r.rules:3 MODE=1 [KERNEL=="a"]`},
		{"empty lines after a tripled \\ take one \\ each", rules("KERNEL==\"a\",\\\\\\\n\n\n MODE=\"1\"\n"),
			one("r.rules", "MODE"), `r.rules:4 MODE=1 [KERNEL=="a"]`},
		{"every operator", rules("SYMLINK=\"a\"\nSYMLINK+=\"b\"\nSYMLINK-=\"c\"\nSYMLINK:=\"d\"\nSYMLINK==\"e\", MODE=\"1\"\n"),
			one("r.rules", "SYMLINK"), "r.rules:1 SYMLINK=a | r.rules:2 SYMLINK+=b | r.rules:3 SYMLINK-=c | r.rules:4 SYMLINK:=d"},
		{"op keeps one operator", rules("SYMLINK=\"a\"\nSYMLINK+=\"b\"\n"),
			Spec{Where: core.Where{File: "r.rules"}, Key: "SYMLINK", Op: "+="}, "r.rules:2 SYMLINK+=b"},
		{"RUN is RUN{program}", rules("RUN+=\"/bin/a\"\nRUN{program}+=\"/bin/b\"\nRUN{builtin}+=\"kmod load x\"\n"),
			one("r.rules", "RUN"), "r.rules:1 RUN+=/bin/a | r.rules:2 RUN{program}+=/bin/b"},
		{"attributes", rules("ENV{ID_A}=\"1\", ENV{ID_B}=\"2\", ATTR{power/control}=\"auto\"\n"),
			one("r.rules", "ENV{ID_B}"), "r.rules:1 ENV{ID_B}=2"},
		{"quotes and escapes", rules("ENV{A}=\"a\\\"b\\n\"\nENV{A}=e\"t\\tx\\x41\\101\\u00e9\\s\\ud7ff\\ue000\\ufdcf\\ufdf0\\U0010fffd\\xff\\060\\177\\377\"\n"),
			one("r.rules", "ENV{A}"), "r.rules:1 ENV{A}=a\"b\\n | r.rules:2 ENV{A}=t\txAAé \ud7ff\ue000\ufdcf\ufdf0\U0010fffd\xff0\x7f\xff"},
		{"%% and $$ as udev applies them", rules("RUN+=\"/bin/sh -c 'date +%%s'\"\nRUN+=\"a$$b%%%k\"\n"),
			one("r.rules", "RUN"), "r.rules:1 RUN+=/bin/sh -c 'date +%s' | r.rules:2 RUN+=a$$b%%%k !lit"},
		{"OPTIONS as written, which udev never formats", rules("OPTIONS=\"static_node=a%%b\"\nOPTIONS=\"static_node=%k$kernel\"\n"), one("r.rules", "OPTIONS"),
			"r.rules:1 OPTIONS=static_node=a%%b | r.rules:2 OPTIONS=static_node=%k$kernel"},
		{"both prefixes, in either order", rules("KERNEL==ei\"sd\\x61\", MODE=\"1\"\nATTR{x}==ie\"S\\x44A\", MODE=\"2\"\n"),
			Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{"ATTR{x}": "sda"}}, `r.rules:2 MODE=2 [ATTR{x}==ie"S\x44A"]`},
		{"both prefixes, the rule's condition", rules("KERNEL==ei\"sd\\x61\", MODE=\"1\"\n"), one("r.rules", "MODE"), `r.rules:1 MODE=1 [KERNEL==ei"sd\x61"]`},
		{"\\u takes any code point, \\U a character", rules("ENV{A}=e\"\\ud800\\ufdd0\\uffff\\U0001f600\\u0041\\u007f\\u0080\\u07ff\\u0800\\U0000fdcf\\U0000fdf0\"\n"),
			one("r.rules", "ENV{A}"), "r.rules:1 ENV{A}=\xed\xa0\x80\uFDD0\uFFFF\U0001F600A\x7f\u0080\u07ff\u0800\uFDCF\uFDF0"},
		{"i\"...\" folds ASCII alone", rules("ATTR{x}==i\"\u212a\", MODE=\"1\"\nATTR{x}==i\"K\", MODE=\"2\"\n"),
			Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{"ATTR{x}": "k"}}, `r.rules:2 MODE=2 [ATTR{x}==i"K"]`},
		{"i\"...\" folds A to Z", rules("ATTR{x}==i\"AZ@[\", MODE=\"1\"\n"),
			Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{"ATTR{x}": "az@["}}, `r.rules:1 MODE=1 [ATTR{x}==i"AZ@["]`},
		{"i\"...\" keeps @ and [", rules("ATTR{x}==i\"@\", MODE=\"1\"\nATTR{x}==i\"[\", MODE=\"2\"\n"),
			Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{"ATTR{x}": "`"}}, ""},
		{"a case insensitive match is kept as written", rules("ATTR{x}==i\"AbC\", MODE=\"1\"\n"),
			one("r.rules", "MODE"), `r.rules:1 MODE=1 [ATTR{x}==i"AbC"]`},
	})
}

// udev reads some operators as others, with a warning: += on a key of one
// value as =, := on ENV, TAG, ATTR and SECLABEL as =, and any assignment
// to PROGRAM and IMPORT as ==. Planted break: setOps keeping += as +=.
func TestUdevReadsSomeOperatorsAsOthers(t *testing.T) {
	runReads(t, []readCase{
		{"MODE+=", rules("MODE+=\"0600\"\n"), one("r.rules", "MODE"), "r.rules:1 MODE=0600"},
		{"ENV:=", rules("ENV{A}:=\"1\"\n"), one("r.rules", "ENV{A}"), "r.rules:1 ENV{A}=1"},
		{"TAG:=", rules("TAG:=\"a\"\nTAG-=\"b\"\n"), one("r.rules", "TAG"), "r.rules:1 TAG=a | r.rules:2 TAG-=b"},
		{"ATTR+=", rules("ATTR{a}+=\"1\"\n"), one("r.rules", "ATTR{a}"), "r.rules:1 ATTR{a}=1"},
		{"SECLABEL:=", rules("SECLABEL{selinux}:=\"x\"\n"), one("r.rules", "SECLABEL{selinux}"), "r.rules:1 SECLABEL{selinux}=x"},
		{"PROGRAM= is a match", rules("PROGRAM=\"/bin/x\", IMPORT{program}+=\"y\", OWNER=\"a\"\n"),
			one("r.rules", "OWNER"), `r.rules:1 OWNER=a [PROGRAM=="/bin/x", IMPORT{program}=="y"]`},
	})
}

// udev drops a rule it cannot read whole, so none of its values applies:
// an unknown key, a key with an operator or an attribute it does not take,
// a value not in quotes or not closed, a comment after the keys, the i
// prefix on an assignment, NAME="%k", a property no rule may set, and an
// OPTIONS link_priority or log_level it cannot read. An OPTIONS value it
// does not know is passed over alone. Planted break: readRule keeping a
// rule udev drops.
func TestRulesUdevDrops(t *testing.T) {
	for _, line := range []string{
		`KERNL=="sda", MODE="1"`,
		`KERNEL="sda", MODE="1"`,
		`MODE=="1"`,
		`MODE-="1"`,
		`KERNEL{x}=="sda", MODE="1"`,
		`ENV=="1", MODE="1"`,
		`ENV{}=="1", MODE="1"`,
		`CONST{os}=="x", MODE="1"`,
		`TEST{999}=="x", MODE="1"`,
		`RUN{other}+="x", MODE="1"`,
		`IMPORT{x}=="y", MODE="1"`,
		`KERNEL==sda, MODE="1"`,
		`KERNEL=="sda, MODE="1`,
		`MODE="1" # owner only`,
		`MODE=i"1"`,
		`KERNEL {x}=="a", MODE="1"`,
		`KERNEL<>"a", MODE="1"`,
		`ATTR{x=="a", MODE="1"`,
		`KERNEL`,
		`NAME="%k", MODE="1"`,
		`NAME="", MODE="1"`,
		`ENV{DEVNAME}="x", MODE="1"`,
		`OPTIONS="link_priority=x", MODE="1"`,
		`OPTIONS="log_level=loud", MODE="1"`,
		`ENV{A}=e"\q", MODE="1"`,
		`ENV{A}=e"\x00", MODE="1"`,
		`ENV{A}=e"\U0001fffe", MODE="1"`,
		`ENV{A}=e"\U0000d800", MODE="1"`,
		`ENV{A}=e"\U0000dfff", MODE="1"`,
		`ENV{A}=e"\U0000fdd0", MODE="1"`,
		`ENV{A}=e"\U0000fdef", MODE="1"`,
		`ENV{A}=e"\U00110000", MODE="1"`,
		`ENV{A}=e"\U0", MODE="1"`,
		`ENV{A}=e"\400", MODE="1"`,
		`ENV{A}=e"\x4", MODE="1"`,
		`ENV{A}=e"a\"`,
		`MODE=e"1`,
		`KERNEL==ii"a", MODE="1"`,
		`TEST==i"/x", MODE="1"`,
		`PROGRAM==i"/x", MODE="1"`,
		`IMPORT{program}!=i"/x", MODE="1"`,
		`KERNEL==ee"a", MODE="1"`,
		`KERNEL==iei"a", MODE="1"`,
		`KERNEL==ix"a", MODE="1"`,
		`MODE="1", NAME-="x"`,
		`MODE="1", SECLABEL{x}=="y"`,
		`MODE="1", GOTO+="x"`,
		`MODE="1", KERNEL+`,
		`MODE="1", KERNEL==`,
		`OPTIONS="log_level=8", MODE="1"`,
		`TEST{10000}=="x", MODE="1"`,
	} {
		t.Run(line, func(t *testing.T) {
			got, err := extract(t, rules(line+"\nMODE=\"2\"\n"), one("r.rules", "MODE"))
			if err != nil || got != "r.rules:2 MODE=2" {
				t.Errorf("got %q, %v; want only line 2's value", got, err)
			}
		})
	}
	runReads(t, []readCase{
		{"an unknown option passed over", rules("OPTIONS=\"bogus\", OPTIONS=\"watch\", OPTIONS=\"link_priority=-5\", OPTIONS=\"log_level=7\", OPTIONS=\"log_level=0\", OPTIONS=\"log_level=reset\", OPTIONS=\"log_level=debug\", OPTIONS=\"static_node=tty\"\n"),
			one("r.rules", "OPTIONS"), "r.rules:1 OPTIONS=watch | r.rules:1 OPTIONS=link_priority=-5 | r.rules:1 OPTIONS=log_level=7 | r.rules:1 OPTIONS=log_level=0 | r.rules:1 OPTIONS=log_level=reset | r.rules:1 OPTIONS=log_level=debug | r.rules:1 OPTIONS=static_node=tty"},
		{"a second GOTO passed over", rules("GOTO=\"a\", GOTO=\"b\", MODE=\"1\"\nMODE=\"2\"\nLABEL=\"a\"\nLABEL=\"b\"\nMODE=\"3\"\n"),
			one("r.rules", "MODE"), "r.rules:1 MODE=1 | r.rules:5 MODE=3"},
		{"TEST with a mode", rules("TEST{0644}==\"/x\", CONST{arch}==\"x86-64\", MODE=\"1\"\nTEST{7777}==\"/y\", MODE=\"2\"\nTEST{}==\"/z\", MODE=\"3\"\n"),
			one("r.rules", "MODE"), `r.rules:1 MODE=1 [TEST{0644}=="/x", CONST{arch}=="x86-64"] | r.rules:2 MODE=2 [TEST{7777}=="/y"] | r.rules:3 MODE=3 [TEST{}=="/z"]`},
	})
}

// A \ at the end of a line joins the next; a rule whose joined lines reach
// udev's line size is dropped, with the lines that go on joining it, and a
// line that long alone, or a NUL byte, stops the read. Planted break: the
// joined length compared with > for >=.
func TestLongLines(t *testing.T) {
	half := strings.Repeat("x", lineSize/2)
	long := "ENV{A}=\"" + half + "\", \\\nENV{B}=\"" + half + "\", \\\nMODE=\"1\"\nMODE=\"2\"\n"
	got, err := extract(t, rules(long), one("r.rules", "MODE"))
	if err != nil || got != "r.rules:4 MODE=2" {
		t.Errorf("a joined rule past the line size: %q, %v", got, err)
	}
	exact := "ENV{A}=\"" + strings.Repeat("x", lineSize-len("ENV{A}=\"")-len("MODE=\"1\"")-2) + "\",\\\nMODE=\"1\"\n"
	if got, err := extract(t, rules(exact), one("r.rules", "MODE")); err != nil || got != "" {
		t.Errorf("a joined rule of the line size exactly: %q, %v", got, err)
	}
	if got, err := extract(t, rules(exact[:len(exact)-3]+"\"\n"), one("r.rules", "MODE")); err != nil || got != "r.rules:2 MODE=" {
		t.Errorf("a joined rule a byte short of it: %q, %v", got, err)
	}
	for name, body := range map[string]string{
		"line": "MODE=\"1\"\n" + strings.Repeat("x", lineSize) + "\n",
		"NUL":  "MODE=\"1\"\nMODE=\"\x00\"\n",
	} {
		_, err := extract(t, rules(body), one("r.rules", "MODE"))
		if err == nil || !strings.Contains(err.Error(), "r.rules: line 2: ") {
			t.Errorf("%s: %v, want a refusal at line 2", name, err)
		}
	}
	if _, err := extract(t, rules("\x00MODE=\"1\"\n"), one("r.rules", "MODE")); err == nil || !strings.Contains(err.Error(), "r.rules: line 1: holds a NUL byte") {
		t.Errorf("a NUL byte first: %v", err)
	}
	if _, err := extract(t, rules(strings.Repeat("x", lineSize-1)+"\n"), one("r.rules", "MODE")); err != nil {
		t.Errorf("a line a byte short of the line size: %v", err)
	}
}

// A GOTO skips the rules up to its LABEL where its rule's match keys
// hold, so a rule past it holds where they do not: one key with the
// operator turned, which tests: selects by, or "not" before several. A
// GOTO with no match key skips them always, but a rule an earlier GOTO
// jumps to inside that skip holds where that GOTO's keys do, as the else
// of an if does. A GOTO with no LABEL after it is passed over, and the
// LABEL's own rule is past the skip. Planted breaks: past keeping the
// operator; leave dropping the ways an always jumping GOTO carries.
func TestGotoGuards(t *testing.T) {
	file := `ACTION=="remove", GOTO="end"
SUBSYSTEM!="usb", GOTO="end"
ATTR{idVendor}=="046d", MODE="0666"
KERNEL=="a", DRIVER=="b", GOTO="end"
MODE="0600"
LABEL="end", MODE="0644"
GOTO="none"
MODE="0640"
`
	runReads(t, []readCase{
		{"conditions", rules(file), one("r.rules", "MODE"), `r.rules:3 MODE=0666 [ACTION!="remove", SUBSYSTEM=="usb", ATTR{idVendor}=="046d"] | r.rules:5 MODE=0600 [ACTION!="remove", SUBSYSTEM=="usb", not (KERNEL=="a", DRIVER=="b")] | r.rules:6 MODE=0644 | r.rules:8 MODE=0640`},
		{"match by a guard", rules(file), Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{"SUBSYSTEM": "usb", "ATTR{idVendor}": "046d"}},
			`r.rules:3 MODE=0666 [ACTION!="remove", SUBSYSTEM=="usb", ATTR{idVendor}=="046d"]`},
		{"a negated key does not match", rules(file), Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{"ACTION": "remove"}}, ""},
		{"always jumps", rules("GOTO=\"x\"\nMODE=\"1\"\nGOTO=\"y\"\nLABEL=\"x\"\nMODE=\"2\"\nLABEL=\"y\"\nMODE=\"3\"\n"), one("r.rules", "MODE"), "r.rules:5 MODE=2 | r.rules:7 MODE=3"},
		{"a LABEL before is no target", rules("LABEL=\"x\"\nKERNEL==\"a\", GOTO=\"x\"\nMODE=\"1\"\n"), one("r.rules", "MODE"), "r.rules:3 MODE=1"},
		{"a GOTO line after a \\", rules("KERNEL==\"a\", \\\n GOTO=\"x\"\nMODE=\"1\"\nLABEL=\"x\"\n"), one("r.rules", "MODE"), `r.rules:3 MODE=1 [KERNEL!="a"]`},
		{"nested, known keys", rules("ACTION==\"1\", GOTO=\"o\"\nKERNEL==\"k\", GOTO=\"i\"\nMODE=\"1\"\nLABEL=\"i\"\nMODE=\"2\"\nLABEL=\"o\"\n"),
			one("r.rules", "MODE"), `r.rules:3 MODE=1 [ACTION!="1", KERNEL!="k"] | r.rules:5 MODE=2 [ACTION!="1"]`},
		{"labels are per file", map[string]string{"a.rules": "KERNEL==\"a\", GOTO=\"x\"\n", "b.rules": "MODE=\"1\"\nLABEL=\"x\"\n"},
			Spec{Where: core.Where{Files: []string{"*.rules"}}, Key: "MODE"}, "b.rules:1 MODE=1"},
		{"a GOTO's own LABEL is no target", rules("KERNEL==\"a\", GOTO=\"x\", LABEL=\"x\"\nMODE=\"1\"\nLABEL=\"x\"\n"), one("r.rules", "MODE"), `r.rules:2 MODE=1 [KERNEL!="a"]`},
		{"else", rules("SUBSYSTEM!=\"block\", GOTO=\"end\"\nKERNEL==\"sd*\", GOTO=\"disk\"\nGOTO=\"end\"\nLABEL=\"disk\", MODE=\"0640\"\nMODE=\"0660\"\nLABEL=\"end\"\nMODE=\"0600\"\n"), one("r.rules", "MODE"),
			`r.rules:4 MODE=0640 [SUBSYSTEM=="block", KERNEL=="sd*"] | r.rules:5 MODE=0660 [SUBSYSTEM=="block", KERNEL=="sd*"] | r.rules:7 MODE=0600`},
		{"else, by match", rules("SUBSYSTEM!=\"block\", GOTO=\"end\"\nKERNEL==\"sd*\", GOTO=\"disk\"\nGOTO=\"end\"\nLABEL=\"disk\"\nMODE=\"0660\"\nLABEL=\"end\"\n"),
			Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{"KERNEL": "sd*", "SUBSYSTEM": "block"}}, `r.rules:5 MODE=0660 [SUBSYSTEM=="block", KERNEL=="sd*"]`},
		{"a dead GOTO", rules("GOTO=\"a\"\nKERNEL==\"k\", GOTO=\"b\"\nLABEL=\"a\"\nMODE=\"1\"\nLABEL=\"b\"\n"), one("r.rules", "MODE"), "r.rules:4 MODE=1"},
	})
}

// A rule udev reaches past one GOTO, or by the jump of an earlier one to
// a LABEL inside that GOTO's skip, holds where either does, which no
// condition errata writes states, so a value there is refused, and one
// past both is not, where the ways meet again. Planted break: pair
// finding no halves.
func TestCrossingGotos(t *testing.T) {
	file := "KERNEL==\"a\", GOTO=\"x\"\nKERNEL==\"b\", GOTO=\"y\"\nMODE=\"1\"\nLABEL=\"x\", MODE=\"2\"\nLABEL=\"y\"\nMODE=\"3\"\n"
	_, err := extract(t, rules(file), one("r.rules", "MODE"))
	if err == nil || !strings.Contains(err.Error(), "r.rules: line 4: MODE=\"2\" is in a rule udev reaches by 2 ways, past or by the jumps of the GOTOs at lines 1, 2") {
		t.Errorf("got %v", err)
	}
	got, err := extract(t, rules(file), Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{"KERNEL": "c"}})
	if err != nil || got != "" {
		t.Errorf("a crossed rule tests: does not select: %q, %v", got, err)
	}
	got, err = extract(t, rules(strings.Replace(file, "MODE=\"2\"", "OWNER=\"2\"", 1)), one("r.rules", "MODE"))
	if want := `r.rules:3 MODE=1 [KERNEL!="a", KERNEL!="b"] | r.rules:6 MODE=3`; err != nil || got != want {
		t.Errorf("got %q, %v, want %q", got, err, want)
	}
}

// The files of rules.d directories are read by name across them, of two
// files of one name the one in the directory udev ranks first, a symlink
// to /dev/null masking its name, and only .rules files whose name does not
// start with a dot. Planted break: dirRank's ranks of etc and usr/lib
// swapped.
func TestRulesDOrder(t *testing.T) {
	files := map[string]string{
		"usr/lib/udev/rules.d/50-a.rules":       "MODE=\"lib50\"\n",
		"etc/udev/rules.d/50-a.rules":           "MODE=\"etc50\"\n",
		"usr/lib/udev/rules.d/10-b.rules":       "MODE=\"lib10\"\n",
		"run/udev/rules.d/70-c.rules":           "MODE=\"run70\"\n",
		"usr/lib/udev/rules.d/70-c.rules":       "MODE=\"lib70\"\n",
		"lib/udev/rules.d/60-d.rules":           "MODE=\"lib60\"\n",
		"usr/local/lib/udev/rules.d/60-d.rules": "MODE=\"local60\"\n",
		"etc/udev/rules.d/README":               "MODE=\"no\"\n",
		"etc/udev/rules.d/.90-h.rules":          "MODE=\"no\"\n",
		"etc/udev/rules.d/80-t.rules.j2":        "MODE=\"{{ m }}\"\n",
		"usr/lib/udev/rules.d/80-t.rules":       "MODE=\"lib80\"\n",
	}
	spec := Spec{Where: core.Where{Files: []string{"lib/udev/rules.d/*", "usr/lib/udev/rules.d/*", "usr/local/lib/udev/rules.d/*", "run/udev/rules.d/*", "etc/udev/rules.d/*"}}, Key: "MODE"}
	runReads(t, []readCase{{"order", files, spec, "usr/lib/udev/rules.d/10-b.rules:1 MODE=lib10 | etc/udev/rules.d/50-a.rules:1 MODE=etc50 | usr/local/lib/udev/rules.d/60-d.rules:1 MODE=local60 | run/udev/rules.d/70-c.rules:1 MODE=run70 | etc/udev/rules.d/80-t.rules.j2:1 MODE={{ m }} !lit"}})

	root := extracttest.Repo(t, map[string]string{"usr/lib/udev/rules.d/50-a.rules": "MODE=\"lib\"\n", "usr/lib/udev/rules.d/60-b.rules": "MODE=\"b\"\n"})
	if err := os.MkdirAll(filepath.Join(root, "etc", "udev", "rules.d"), 0o750); err != nil {
		t.Fatal(err)
	}
	extracttest.Symlink(t, os.DevNull, filepath.Join(root, "etc", "udev", "rules.d", "50-a.rules"))
	gitAdd(t, root)
	x, err := Build(Spec{Where: core.Where{Files: []string{"usr/lib/udev/rules.d/*.rules", "etc/udev/rules.d/*.rules"}}, Key: "MODE"})
	if err != nil {
		t.Fatal(err)
	}
	vs, err := x.Extract(t.Context(), root)
	if got := show(vs); err != nil || got != "usr/lib/udev/rules.d/60-b.rules:1 MODE=b" {
		t.Errorf("a /dev/null link: %q, %v", got, err)
	}
}

// gitAdd tracks every file under root in a new repository.
func gitAdd(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// A value udev substitutes in is not literal: a % format letter, a $
// name, and not %% or $$ or a % before another letter. Planted break:
// substitutes passing over $.
func TestSubstitutions(t *testing.T) {
	for v, want := range map[string]bool{
		"%k": true, "a%n": true, "$kernel": true, "$env{X}": true, "$attr{a}": true, "%E{X}": true, "$devnode": true,
		"100%%": false, "$$x": false, "%q": false, "$other": false, "%": false, "$": false, "%%k": false, "a%%%k": true, "%%%k": true, "%N": true, "aa%k": true,
	} {
		if got := substitutes(v); got != want {
			t.Errorf("substitutes(%q) = %v, want %v", v, got, want)
		}
	}
}

// Build refuses a spec udev could not read: no key, a key a rule assigns
// nothing to, an operator udev never reads the key with, a match key udev
// does not match with, and file with files. Planted break: checkOp taking
// any operator.
func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{one("r.rules", ""), "udev: key is required"},
		{one("r.rules", "KERNEL"), `udev: key "KERNEL" is no key a rule assigns a value to; write one of ATTR, ENV, GROUP, MODE, NAME, OPTIONS, OWNER, RUN, SECLABEL, SYMLINK, SYSCTL, TAG`},
		{one("r.rules", "GOTO"), `udev: key "GOTO" is no key`},
		{one("r.rules", "IMPORT{program}"), `udev: key "IMPORT{program}" is no key`},
		{one("r.rules", "MOD"), `udev: key: "MOD" is no key udev reads`},
		{one("r.rules", "{a}"), `udev: key: "" is no key udev reads`},
		{one("r.rules", "ENV"), `udev: key: "ENV" is no attribute udev reads ENV with`},
		{one("r.rules", "ENV{A"), `udev: key: "ENV{A" has a { with no }`},
		{one("r.rules", "ENV{A}}"), `udev: key: "ENV{A}}" has a { with no }`},
		{one("r.rules", "RUN{x}"), `udev: key: "RUN{x}" is no attribute`},
		{Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Op: "-="}, `udev: op "-=" is no operator udev reads MODE with; write one of :=, =`},
		{Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Op: "=="}, `udev: op "==" is no operator`},
		{Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{"OWNER": "a"}}, `udev: tests names "OWNER", which is no key udev matches with; write one of ACTION, ATTR, ATTRS, CONST,`},
		{Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{"ATTRS": "a"}}, `udev: tests: "ATTRS" is no attribute`},
		{Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{}}, "udev: tests names no key"},
		{Spec{Where: core.Where{File: "r.rules", Files: []string{"a"}}, Key: "MODE"}, "udev: file and files are both set"},
		{Spec{Where: core.Where{Files: []string{}}, Key: "MODE"}, "udev: files lists no entry"},
		{Spec{Where: core.Where{Files: []string{"/a"}}, Key: "MODE"}, "is outside the repository"},
		{Spec{Key: "MODE"}, "udev: file is required"},
	} {
		_, err := Build(c.spec)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.spec, err, c.want)
		}
	}
}

// The values are held to the value limit and the files to max_bytes in
// all. Planted break: add without its limit.
func TestLimits(t *testing.T) {
	x, err := Build(one("r.rules", "MODE"))
	if err != nil {
		t.Fatal(err)
	}
	e := x.(extractor)
	e.valueLimit = 2
	_, err = e.Extract(t.Context(), extracttest.Tracked(t, rules("MODE=\"1\"\nMODE=\"2\"\nMODE=\"3\"\n")))
	if err == nil || !strings.Contains(err.Error(), "r.rules") || !strings.Contains(err.Error(), "values") {
		t.Errorf("past the value limit: %v", err)
	}
	files := map[string]string{"d/rules.d/a.rules": "MODE=\"1\"\n", "d/rules.d/b.rules": "MODE=\"2\"\n"}
	x, err = Build(Spec{Where: core.Where{Files: []string{"d/rules.d/*"}, MaxBytes: 12}, Key: "MODE"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.Extract(t.Context(), extracttest.Tracked(t, files))
	if err == nil || !strings.Contains(err.Error(), "hold at most max_bytes, 12, in all") {
		t.Errorf("past max_bytes in all: %v", err)
	}
	x, err = Build(Spec{Where: core.Where{File: "d/rules.d/a.rules", MaxBytes: 4}, Key: "MODE"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.Extract(t.Context(), extracttest.Tracked(t, files))
	if err == nil || strings.Contains(err.Error(), "in all") {
		t.Errorf("one file past max_bytes: %v, want no word of the files in all", err)
	}
	_, err = x.Extract(t.Context(), extracttest.Tracked(t, map[string]string{"other": ""}))
	if err == nil || !strings.Contains(err.Error(), "d/rules.d/a.rules") {
		t.Errorf("a file that is not there: %v", err)
	}
}
