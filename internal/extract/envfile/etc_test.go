package envfile

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// markedIn is marked for a file named file.
func markedIn(t *testing.T, file, body string, s Spec) string {
	t.Helper()
	s.File = file
	return texts(extracttest.MustRun(t, Build, s, extracttest.Repo(t, map[string]string{file: body})))
}

// A file's name chooses its dialect, a template suffix taken off: a file
// directly in a default directory beneath etc is sourced, a file named
// environment directly beneath etc is pam_env's, and any other compose's.
// Planted breaks: the default directory's parent not asked; a file below a
// directory in default taken; the template suffix kept.
func TestEtcDialect(t *testing.T) {
	for file, want := range map[string]dialect{
		"etc/default/grub": sourced, "rootfs/etc/default/ssh": sourced, "etc/default/grub.j2": sourced,
		"default/grub": compose, "etc/default/grub.d/x.cfg": compose, "etc/default": compose,
		"usr/default/grub": compose,
		"etc/environment":  pamEnv, "srv/etc/environment.tpl": pamEnv, "./etc/environment": pamEnv,
		"environment": compose, "etc/x/environment": compose, "etc/environment/x": compose,
		".env": compose, "etc/.env": compose,
	} {
		if got := etcDialect(file); got != want {
			t.Errorf("%s: %d, want %d", file, got, want)
		}
	}
}

// In a file a shell sources, a value is literal only where a shell, systemd's
// EnvironmentFile= and compose read it alike; elsewhere it is the text as
// written, marked not literal. Planted breaks: the check dropped; export
// taken; a line a lone CR starts taken; a word with a space taken; a quote
// with a \ taken; white space after a closing quote taken.
func TestSourcedDialect(t *testing.T) {
	for _, c := range []struct{ name, body, key, want string }{
		{"a plain value", "GRUB_TIMEOUT=5\n", "GRUB_TIMEOUT", "5@1"},
		{"a path list", "A=/usr/bin:/bin\n", "A", "/usr/bin:/bin@1"},
		{"white space before and at the end", "  A=1  \n", "A", "1@1"},
		{"an empty value", "A=\n", "A", "@1"},
		{"empty quotes", "A=\"\"\n", "A", "@1"},
		{"double quotes", "A=\"quiet splash\"\n", "A", "quiet splash@1"},
		{"single quotes", "A='don\"t'\n", "A", "don\"t@1"},
		{"a ' in double quotes", "A=\"don't\"\n", "A", "don't@1"},
		{"export, which systemd ignores", "export A=1\n", "A", "1!@1"},
		{"a space in a word, a command to a shell", "A=x y\n", "A", "x y!@1"},
		{"white space around =", "A = 1\n", "A", "1!@1"},
		{"white space after =", "A= 1\n", "A", "1!@1"},
		{"yaml style", "A: 1\n", "A", "1!@1"},
		{"a comment, a value to systemd", "A=1 # c\n", "A", "1!@1"},
		{"a tilde", "A=~/x\n", "A", "~/x!@1"},
		{"a substitution", "B=1\nA=$B\n", "A", "$B!@2"},
		{"a \\ in double quotes", `A="a\tb"` + "\n", "A", `a\tb!@1`},
		{"a \\ in single quotes", `A='a\b'` + "\n", "A", `a\b!@1`},
		{"a quote closed before the end", "A=\"x\"y\n", "A", "x!@1"},
		{"white space after the quote", "A=\"x\" \n", "A", "x!@1"},
		{"a quote of its own kind inside", "A=\"a\"#\"\n", "A", "a!@1"},
		{"the next statement on a line", "A='x' B=y\n", "B", "y!@1"},
		{"a quote across lines", "A=\"x\ny\"\n", "A", "x\ny!@1"},
		{"a key with a dot", "x.y=1\n", "x.y", "1!@1"},
		{"a key that starts with a digit", "1A=1\n", "1A", "1!@1"},
		{"CRLF, a CR in the value to a shell", "A=1\r\nB=2\r\n", "B", "2!@2"},
		{"a lone CR before the key", "A='x'\rB=y\n", "B", "y!@2"},
		{"a byte order mark", "\uFEFFA=1\n", "A", "1!@1"},
		{"a key alone", "A\n", "A", "!@1"},
		{"after a word after the value, a command", "A=x y\nB=2\n", "B", "2!@2"},
		{"a plain line after export", "export A=1\nB=2\n", "B", "2@2"},
		{"after an open single quote", "A=it's\nB=2\n", "B", "2!@2"},
		{"after a \\ that joins the next line", "A=x\\\nB=2\n", "B", "2!@2"},
		{"after a heredoc", "A=x <<B=2\nC=3\nB=2\n", "C", "3!@2"},
		{"after an open backtick", "A=x`\nC=3\nB=`\n", "C", "3!@2"},
		{"after a pipe at the end of a line", "A=x |\nB=2\n", "B", "2!@2"},
		{"after a keyword a ; starts", "A=1; if true\nthen\nB=2\nfi\n", "B", "2!@3"},
		{"after a quote across lines", "A=\"x\ny\"\nB=2\n", "B", "2!@3"},
		{"every line after", "A=it's\nB=2\nC=3\nB=4\n", "B", "2!@2 4!@4"},
		{"after a $( command", "A=$(x\nB=2)\n", "B", "2)!@2"},
		{"a { in a word carries nothing", "A={x}\nB=2\n", "B", "2@2"},
		{"after a < redirect", "A=x <y\nB=2\n", "B", "2!@2"},
		{"after a ) out of place", "A=x )\nB=2\n", "B", "2!@2"},
		{"after a < at the end of a line", "A=x <\nB=2\n", "B", "2!@2"},
		{"after a > at the end of a line", "A=x >\nB=2\n", "B", "2!@2"},
		{"after a ) with no space", "A=x)\nB=2\n", "B", "2!@2"},
		{"after a < with no space", "A=x<\nB=2\n", "B", "2!@2"},
		{"after a > with no space", "A=x>\nB=2\n", "B", "2!@2"},
		{"after return, a key alone", "return\nB=2\n", "B", "2!@2"},
		{"after fi, a key alone", "fi\nB=2\n", "B", "2!@2"},
		{"after exit as a command word", "A=x exit\nB=2\n", "B", "2!@2"},
		{"after return as a command word", "A=x return\nB=2\n", "B", "2!@2"},
		{"after set -n", "A=x set -n\nB=2\n", "B", "2!@2"},
		{"after a word past white space after =", "A= y\nB=2\n", "B", "2!@2"},
		{"after a quote past white space after =", "A= \"x\"\nB=2\n", "B", "2!@2"},
		{"after a key alone past a closing quote", "A=\"x\" exit\nB=2\n", "B", "2!@2"},
		{"after white space around =, a command", "A = 1\nB=2\n", "B", "2!@2"},
		{"a comment after the value runs nothing", "A=x # c\nB=2\n", "B", "2@2"},
		{"a tab and a comment run nothing", "A=x\t#c\nB=2\n", "B", "2@2"},
		{"a space and a comment run nothing", "A=x #c\nB=2\n", "B", "2@2"},
		{"white space at the end runs nothing", "A=x \t\nB=2\n", "B", "2@2"},
		{"a # inside a word is no comment", "A=x#c y\nB=2\n", "B", "2!@2"},
		{"a yaml line runs A:, which changes nothing", "A: x\nB=2\n", "B", "2@2"},
		{"a substitution in quotes closed on its line", "A=\"$B x\"\nC=3\n", "C", "3@2"},
		{"the value of a closed span is not literal", "A=\"$B x\"\nC=3\n", "A", "$B x!@1"},
		{"a $( command closed on its line", "A=$(x y)\nC=3\n", "C", "3@2"},
		{"a nested $( command closed on its line", "A=$(x $(y) (z))\nC=3\n", "C", "3@2"},
		{"a backtick command closed on its line", "A=`x y`\nC=3\n", "C", "3@2"},
		{"a single quote closed on its line", "A='x y' \nC=3\n", "C", "3@2"},
		{"a comment after a closed span", "A=`x` # it's\nC=3\n", "C", "3!@2"},
		{"two spans on one line", "A=`x`$(y)\nC=3\n", "C", "3!@2"},
		{"a quote after a span", "A=`x`\"y\"\nC=3\n", "C", "3!@2"},
		{"a ; after a span", "A=`x`;exit\nC=3\n", "C", "3!@2"},
		{"a word after a span", "A=`x` exit\nC=3\n", "C", "3!@2"},
		{"a span after white space past =", "A= `x`\nC=3\n", "C", "3!@2"},
		{"a quote inside a $( command", "A=$(echo \"x\")\nC=3\n", "C", "3!@2"},
		{"a $( command inside quotes", "A=\"$(x)\"\nC=3\n", "C", "3!@2"},
		{"a \\ inside a backtick command", "A=`x\\`\nC=3\n", "C", "3!@2"},
		{"a $( command not closed", "A=$(x\nC=3\n", "C", "3!@2"},
		{"a backtick before a $(", "A=`x`$(y\nC=3\n", "C", "3!@2"},
		{"exit with white space before =", "exit =1\nB=2\n", "B", "2!@2"},
		{"export with white space before =", "export A =1\nB=2\n", "B", "2!@2"},
		{"a line =x runs no command", "=x\nB=2\n", "B", "2@2"},
		{"an empty quote closed in a word", "A=x''\nC=3\n", "C", "3@2"},
		{"a $ that starts no $( command", "A=x$Y`y`\nC=3\n", "C", "3!@2"},
		{"a $( not closed at the end of the file", "A=$(x", "A", "$(x!@1"},
		{"a here-document in a $( command", "A=$(cat <<E)\nB=2\nC=3\n", "C", "3!@3"},
		{"a # in a $( command", "A=$(echo x #)\nB=2\nC=3\n", "C", "3!@3"},
		{"a ' inside ${ in a \" value", "A=\"${D:-'}x\"\nB=2\nC=3\n", "C", "3!@3"},
		{"a ${ closed in a \" value", "A=\"${D:-x}\"\nB=2\nC=3\n", "C", "3!@3"},
		{"an open $( in a backtick command", "A=`echo $(`\nB=2\nC=3\n", "C", "3!@3"},
		{"an open ( in a backtick command", "A=`(`\nB=2\nC=3\n", "C", "3!@3"},
		{"a ) before its ( in a backtick command", "A=`)(`\nB=2\nC=3\n", "C", "3!@3"},
		{"balanced ( and ) in a backtick command", "A=`(x) $(y)`\nB=2\nC=3\n", "C", "3@3"},
		{"a backtick command at the end of the file", "A=`x`", "A", "`x`!@1"},
		{"a value before a line that carries", "C=3\nA=it's\n", "C", "3!@1"},
		{"a key set again by a second assignment", "C=3\nA=x C=5\nB=2\n", "C", "3!@1"},
		{"a key set again after a ;", "C=3\nA=1;C=5\nB=2\n", "C", "3!@1"},
		{"a key set again in arithmetic", "C=3\nA=$((C=5))\nB=2\n", "C", "3!@1"},
		{"a key added to in arithmetic", "C=3\nA=$((C+=1))\nB=2\n", "C", "3!@1"},
		{"a key set again in bash's $[", "C=3\nA=\"$[C=5]\"\nB=2\n", "C", "3!@1"},
		{"arithmetic that sets nothing", "A=$((1+2))\nC=3\n", "C", "3!@2"},
		{"a # hiding a ) in a backtick command", "A=`(echo x #)`\nB=2\nC=3\n", "C", "3!@3"},
		{"a # hiding a $( ) in a backtick command", "A=`echo $(echo x #)`\nB=2\nC=3\n", "C", "3!@3"},
		{"exit with white space before :", "exit : 1\nB=2\n", "B", "2!@2"},
	} {
		if got := markedIn(t, "etc/default/app", c.body, name(c.key)); got != c.want {
			t.Errorf("%s: %q: got %q, want %q", c.name, c.body, got, c.want)
		}
	}
}

// A substitution of a value marked in a sourced file is not literal either.
func TestSourcedMarkReachesASubstitution(t *testing.T) {
	if got := markedIn(t, "etc/default/app", "export A=1\nB=${A}\n", name("B")); got != "${A}!@2" {
		t.Errorf("got %q", got)
	}
}

// etc/environment reads as pam_env reads it. Planted breaks: export kept;
// the comment cut at a space and # only; the white space at the end
// dropped; the value substituted; a key with a dot read; each quote taken
// off; a joined line read literal; a line with a NUL read literal.
func TestPamEnvDialect(t *testing.T) {
	for _, c := range []struct{ name, body, key, want string }{
		{"a quoted path", "PATH=\"/usr/local/bin:/usr/bin\"\n", "PATH", "/usr/local/bin:/usr/bin@1"},
		{"the line of each key", "# c\n\n  B=2\n", "B", "2@3"},
		{"a space in the value", "A=x y\n", "A", "x y@1"},
		{"white space at the end is kept", "A=x  \t\n", "A", "x  \t@1"},
		{"# anywhere starts a comment", "A=x#c\n", "A", "x@1"},
		{"export and a space", "  export A=1\n", "A", "1@1"},
		{"export and a tab is a key pam_env skips", "export\tA=1\n", "A", ""},
		{"no substitution", "A=$HOME/${B:-x}\n", "A", "$HOME/${B:-x}@1"},
		{"no escapes", `A="a\tb"` + "\n", "A", `a\tb@1`},
		{"the opening and the last quote", "A='x\"\n", "A", "x@1"},
		{"a quote inside stays", "A=\"a\"b\"\n", "A", "a\"b@1"},
		{"a quote before white space stays", "A=\"x\" \n", "A", "x\" @1"},
		{"a quote alone", "A=\"\n", "A", "@1"},
		{"no quote at the start", "A=x\"\n", "A", "x\"@1"},
		{"white space around =", "A = 1\n", "A", ""},
		{"a key with a dot", "A.B=1\n", "A.B", ""},
		{"= with no key", "=1\nA=2\n", "A", "2@2"},
		{"a key that starts with a digit", "1A=1\n", "1A", "1@1"},
		{"a key alone unsets", "A\n", "A", "!@1"},
		{"CRLF keeps the CR", "A=1\r\nB=2\r\n", "B", "2\r@2"},
		{"a line \\ ends joins the next", "A=1 \\\nB=2\n", "A", "1 \\!@1"},
		{"a \\ on the last line", "A=x\\", "A", "x\\!@1"},
		{"a \\ on the last line, a line end after it", "A=x\\\n", "A", "x\\!@1"},
		{"the joined line is no statement", "A=1\\ \nB=2\nB=3\n", "B", "3@3"},
		{"a \\ after a comment joins nothing", "# c \\\nA=1\n", "A", "1@2"},
		{"a \\ in a value", "A=a\\b\n", "A", "a\\b@1"},
		{"a NUL byte", "A=a\x00b\n", "A", "a\x00b!@1"},
		{"no line end at the end", "A=1", "A", "1@1"},
		{"a key set twice", "A=1\nA=2\n", "A", "1@1 2@2"},
	} {
		if got := markedIn(t, "etc/environment", c.body, name(c.key)); got != c.want {
			t.Errorf("%s: %q: got %q, want %q", c.name, c.body, got, c.want)
		}
	}
}

// Without a name, every key pam_env sets comes back, and a template's mark
// reaches a pam_env value.
func TestPamEnvEveryKeyAndTemplate(t *testing.T) {
	if got := markedIn(t, "etc/environment", "A=1\n#x\nB=2\n", Spec{}); got != "1@1 2@3" {
		t.Errorf("every key: got %q", got)
	}
	if got := markedIn(t, "etc/environment.j2", "A={{ a }}\nB=2\n", Spec{}); got != "{{ a }}!@1 2@2" {
		t.Errorf("template: got %q", got)
	}
	// In a sourced file what follows a value the engine renders is marked
	// too, however its braces are spaced: what the value renders to, which
	// errata cannot see, may carry a shell's state.
	for _, body := range []string{"A={{ a }}\nB=2\n", "A={{a}}\nB=2\n", "A=\"{{ a }}\"\nB=2\n"} {
		if got := markedIn(t, "etc/default/grub.j2", body, name("B")); got != "2!@2" {
			t.Errorf("sourced template %q: got %q", body, got)
		}
	}
}

// Every line of etc/environment charges the meter: at each budget below
// what a read costs it is refused.
func TestPamEnvWork(t *testing.T) {
	body := "# a comment\nexport A=1\nB=\"x\"\n\nC\nD=1 \\\nE=2\n"
	for _, s := range []Spec{{File: "etc/environment", Name: "A"}, {File: "etc/environment"}} {
		x, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		coretest.RefusesAtEveryBudget(t, x.(core.EntryExtractor), body)
		coretest.LeavesRoom(t, x.(core.EntryExtractor), body)
	}
}

// The exact work of a small read of etc/environment, so a charge of the
// wrong size fails: each line is a line's work and its bytes, 4 for A=1\n
// and 3 for #c\n, and the matcher's look at the entry 48 and the key's 1
// byte.
func TestPamEnvWorkIsExact(t *testing.T) {
	x, err := Build(Spec{File: "etc/environment", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	want := int64(core.LineWork+4) + int64(core.LineWork+3) + 48 + 1
	if got := coretest.Spent(t, x.(core.EntryExtractor), []byte("A=1\n#c\n")); got != want {
		t.Errorf("spent %d, want %d", got, want)
	}
}

// Debian's stock etc/default/grub sets GRUB_DISTRIBUTOR by a command closed
// on its line, which carries nothing past it, so the values below it stay
// literal; the command's own value is not. The forms are Debian 13's
// (grub 2.12-8 on), the one testing shipped from 2.12-4 to 2.12-7, which
// hosts installed then keep, and the lsb_release one of Debian 12 and
// Ubuntu. Planted break: withoutSpan taking no span out.
func TestSourcedDebianGrub(t *testing.T) {
	for _, distributor := range []string{
		"GRUB_DISTRIBUTOR=`( . /etc/os-release && echo ${NAME} )`",
		"GRUB_DISTRIBUTOR=`( . /etc/os-release; echo ${NAME:-Debian} ) 2>/dev/null || echo Debian`",
		"GRUB_DISTRIBUTOR=`lsb_release -i -s 2> /dev/null || echo Debian`",
	} {
		body := "# If you change this file, run 'update-grub' afterwards to update\n" +
			"# /boot/grub/grub.cfg.\n" +
			"GRUB_DEFAULT=0\n" +
			"GRUB_TIMEOUT=5\n" +
			distributor + "\n" +
			"GRUB_CMDLINE_LINUX_DEFAULT=\"quiet\"\n" +
			"GRUB_CMDLINE_LINUX=\"\"\n"
		if got := markedIn(t, "etc/default/grub", body, name("GRUB_CMDLINE_LINUX_DEFAULT")); got != "quiet@6" {
			t.Errorf("%s: GRUB_CMDLINE_LINUX_DEFAULT %q", distributor, got)
		}
		if got := markedIn(t, "etc/default/grub", body, name("GRUB_CMDLINE_LINUX")); got != "@7" {
			t.Errorf("%s: GRUB_CMDLINE_LINUX %q", distributor, got)
		}
		if got := markedIn(t, "etc/default/grub", body, name("GRUB_DISTRIBUTOR")); !strings.HasSuffix(got, "!@5") {
			t.Errorf("%s: GRUB_DISTRIBUTOR %q, want it marked", distributor, got)
		}
	}
}

// compose refuses A="${D:-"}x, a " inside ${ in a " value, so the rule is
// unknown rather than C literal.
func TestSourcedRefusesAQuoteInsideBraces(t *testing.T) {
	_, err := extracttest.Run(t, Build, Spec{File: "etc/default/app", Name: "C"}, extracttest.Repo(t, map[string]string{"etc/default/app": "A=\"${D:-\"}x\nB=2\nC=3\n"}))
	if err == nil || !strings.Contains(err.Error(), "starts no substitution") {
		t.Errorf("got %v, want compose's refusal", err)
	}
}
