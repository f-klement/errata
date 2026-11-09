package sudoers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// show lists values as file:line=text, with the condition and a not
// literal mark when a value has them. A record is its fields, each list as
// its items joined by a space, and empty fields left out.
func show(vs []check.Value) string {
	var parts []string
	for _, v := range vs {
		text := v.Text
		if v.Data != nil {
			text = showRecord(v.Data)
		}
		s := fmt.Sprintf("%s:%d=%s", v.At.File, v.At.Line, text)
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

// showRecord writes a record's fields as key=value, a list's items joined
// by a comma, in the order of Fields, an empty list left out.
func showRecord(d *check.Data) string {
	var parts []string
	for i, k := range d.Keys {
		it := d.Items[i]
		if it.Kind == check.DataScalar {
			parts = append(parts, k+"="+it.Text)
			continue
		}
		if len(it.Items) == 0 {
			continue
		}
		var texts []string
		for _, x := range it.Items {
			texts = append(texts, x.Text)
		}
		parts = append(parts, k+"="+strings.Join(texts, ","))
	}
	return "{" + strings.Join(parts, " ") + "}"
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

// refuseCase is a read that must fail with a message holding want.
type refuseCase struct {
	name  string
	files map[string]string
	spec  Spec
	want  string
}

func runRefusals(t *testing.T, cases []refuseCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := extract(t, c.files, c.spec)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %q, %v; want an error holding %q", got, err, c.want)
			}
		})
	}
}

func where(file string) core.Where { return core.Where{File: file} }

func defaults(file, name string) Spec { return Spec{Where: where(file), Defaults: name} }

func user(file, name string) Spec { return Spec{Where: where(file), User: name} }

// Defaults lines give each setting its value as sudo reads it: a flag true,
// or false after a !, a value after =, quoted or escaped, and += and -=
// with the operator before the value, each with the line's scope as its
// condition and at the line of its value. Planted break: the scope left
// off the condition.
func TestDefaults(t *testing.T) {
	file := "Defaults env_reset, !lecture\n" +
		"Defaults\tsecure_path=\"/usr/local/sbin:/usr/bin\"\n" +
		"Defaults:alice,%ops  !requiretty, secure_path = /bin\n" +
		"Defaults@web1 timestamp_timeout=15\n" +
		"Defaults>root env_keep += \"HOME PATH\"\n" +
		"Defaults!/usr/bin/su env_keep-=LANG\n" +
		"Defaults passprompt=a\\,b, \\\n  secure_path=/x\n" +
		"# Defaults secure_path=/never\n"
	runReads(t, []readCase{
		{"flags", map[string]string{"sudoers": file}, defaults("sudoers", "lecture"), "sudoers:1=false"},
		{"value and scope", map[string]string{"sudoers": file}, defaults("sudoers", "secure_path"),
			"sudoers:2=/usr/local/sbin:/usr/bin | sudoers:3=/bin [Defaults:alice,%ops] | sudoers:8=/x"},
		{"negated flag in a scope", map[string]string{"sudoers": file}, defaults("sudoers", "requiretty"), "sudoers:3=false [Defaults:alice,%ops]"},
		{"host scope", map[string]string{"sudoers": file}, defaults("sudoers", "timestamp_timeout"), "sudoers:4=15 [Defaults@web1]"},
		{"list operators", map[string]string{"sudoers": file}, defaults("sudoers", "env_keep"),
			"sudoers:5=+=HOME PATH [Defaults>root] | sudoers:6=-=LANG [Defaults!/usr/bin/su]"},
		{"escaped comma", map[string]string{"sudoers": file}, defaults("sudoers", "passprompt"), "sudoers:7=a,b"},
	})
}

// A user specification gives a record for each command, with its hosts,
// its Runas lists, and the tags and options the commands before it in the
// list carry over, each command at its own line. Planted break: the tags
// not carried to the next command.
func TestGrants(t *testing.T) {
	file := "root ALL=(ALL:ALL) ALL\n" +
		"alice web1, web2 = (root) NOPASSWD: /usr/bin/systemctl restart app, \\\n" +
		"   /usr/bin/journalctl, (:adm) TIMEOUT=10 PASSWD: /bin/ls \"\" : db1 = NOEXEC: /bin/x\n" +
		"%admin ALL = (ALL) ALL\n"
	runReads(t, []readCase{
		{"carried over", map[string]string{"sudoers": file}, user("sudoers", "alice"),
			"sudoers:2={command=/usr/bin/systemctl restart app host=web1,web2 runas=root tags=NOPASSWD user=alice}" +
				" | sudoers:3={command=/usr/bin/journalctl host=web1,web2 runas=root tags=NOPASSWD user=alice}" +
				" | sudoers:3={command=/bin/ls \"\" host=web1,web2 options=TIMEOUT=10 runas=(invoking user) runas_group=adm tags=PASSWD user=alice}" +
				" | sudoers:3={command=/bin/x host=db1 tags=NOEXEC user=alice}" +
				" | sudoers:4={command=ALL host=ALL runas=ALL user=%admin} !lit"},
		{"a group", map[string]string{"sudoers": file}, user("sudoers", "%admin"), "sudoers:4={command=ALL host=ALL runas=ALL user=%admin}"},
		{"root", map[string]string{"sudoers": file}, user("sudoers", "root"),
			"sudoers:1={command=ALL host=ALL runas=ALL runas_group=ALL user=root} | sudoers:4={command=ALL host=ALL runas=ALL user=%admin} !lit"},
	})
}

// The four kinds of alias resolve to their entries, each at the line it
// is written on, an alias inside another too, a negated alias of one
// entry as that entry negated and of several as one entry, and a command
// alias gives a record for each of its commands. Planted break: an alias's
// negation left off.
func TestAliases(t *testing.T) {
	file := "User_Alias ADMINS = alice, bob : OPS = carol\n" +
		"Host_Alias WEB = web1, 10.0.0.0/8, fe80::1\n" +
		"Runas_Alias DB = postgres, mysql\n" +
		"Cmnd_Alias SVC = /usr/bin/systemctl restart *, !/usr/bin/su\n" +
		"Cmd_Alias ONE = /bin/one\n" +
		"User_Alias ALLADM = ADMINS, OPS\n" +
		"ALLADM WEB = (DB) SVC, !ONE\n" +
		"ADMINS ALL = !SVC\n"
	runReads(t, []readCase{
		{"resolved", map[string]string{"sudoers": file}, user("sudoers", "carol"),
			"sudoers:4={command=/usr/bin/systemctl restart * host=web1,10.0.0.0/8,fe80::1 runas=postgres,mysql user=alice,bob,carol}" +
				" | sudoers:4={command=!/usr/bin/su host=web1,10.0.0.0/8,fe80::1 runas=postgres,mysql user=alice,bob,carol}" +
				" | sudoers:5={command=!/bin/one host=web1,10.0.0.0/8,fe80::1 runas=postgres,mysql user=alice,bob,carol}"},
		{"negated whole", map[string]string{"sudoers": file}, user("sudoers", "bob"),
			"sudoers:4={command=/usr/bin/systemctl restart * host=web1,10.0.0.0/8,fe80::1 runas=postgres,mysql user=alice,bob,carol}" +
				" | sudoers:4={command=!/usr/bin/su host=web1,10.0.0.0/8,fe80::1 runas=postgres,mysql user=alice,bob,carol}" +
				" | sudoers:5={command=!/bin/one host=web1,10.0.0.0/8,fe80::1 runas=postgres,mysql user=alice,bob,carol}" +
				" | sudoers:8={command=!(/usr/bin/systemctl restart *, !/usr/bin/su) host=ALL user=alice,bob}"},
	})
}

// sudo takes the last entry of a user list that matches, so a later !
// denies; a group, a netgroup and a user ID may hold a user named by
// name, so a grant they decide is not literal; a group selects only its
// own entry and ALL. Planted break: compareUser's mayMatch taken as a
// match.
func TestUserLists(t *testing.T) {
	files := map[string]string{"sudoers": "ALL, !bob ALL = /a\n" +
		"%wheel, alice ALL = /b\n" +
		"ALL, !%wheel ALL = /c\n" +
		"#1000 ALL = /d\n" +
		"+ops ALL = /e\n" +
		"bob, !ALL ALL = /f\n" +
		"User_Alias NOTBOB = ALL, !bob\n" +
		"!NOTBOB ALL = /g\n"}
	runReads(t, []readCase{
		{"alice", files, user("sudoers", "alice"),
			"sudoers:1={command=/a host=ALL user=ALL,!bob} | sudoers:2={command=/b host=ALL user=%wheel,alice}" +
				" | sudoers:3={command=/c host=ALL user=ALL,!%wheel} !lit | sudoers:4={command=/d host=ALL user=#1000} !lit" +
				" | sudoers:5={command=/e host=ALL user=+ops} !lit"},
		{"bob", files, user("sudoers", "bob"),
			"sudoers:2={command=/b host=ALL user=%wheel,alice} !lit | sudoers:3={command=/c host=ALL user=ALL,!%wheel} !lit" +
				" | sudoers:4={command=/d host=ALL user=#1000} !lit | sudoers:5={command=/e host=ALL user=+ops} !lit" +
				" | sudoers:8={command=/g host=ALL user=!(ALL, !bob)}"},
		{"a group", files, user("sudoers", "%wheel"), "sudoers:1={command=/a host=ALL user=ALL,!bob} | sudoers:2={command=/b host=ALL user=%wheel,alice}"},
		{"by ID", files, user("sudoers", "#1000"),
			"sudoers:1={command=/a host=ALL user=ALL,!bob} !lit | sudoers:2={command=/b host=ALL user=%wheel,alice} !lit" +
				" | sudoers:3={command=/c host=ALL user=ALL,!%wheel} !lit | sudoers:4={command=/d host=ALL user=#1000}" +
				" | sudoers:5={command=/e host=ALL user=+ops} !lit | sudoers:8={command=/g host=ALL user=!(ALL, !bob)} !lit"},
	})
}

// Comments, digests and odd lines read as sudo reads them: a # starts a
// comment but before a user or group ID, a non-Unix group keeps its :, a
// line ends at \r too, and a template's tag lines are passed over while a
// value with its marks is not literal, and a user entry it renders may be
// any user. Planted breaks: idAt reading every # as a comment;
// memberVerdict taking a rendered entry as the text it holds.
func TestOddLines(t *testing.T) {
	runReads(t, []readCase{
		{"IDs and comments", map[string]string{"sudoers": "#1000 ALL = (#0) /a # c\r%#50, %:dom\\ users ALL = /b\r\n"}, user("sudoers", "%:dom users"),
			"sudoers:2={command=/b host=ALL user=%#50,%:dom users}"},
		{"digest", map[string]string{"sudoers": "alice ALL = sha256:abc= /bin/x, (bob) NOPASSWD: sha512:ff /bin/y arg\n"}, user("sudoers", "alice"),
			"sudoers:1={command=sha256:abc= /bin/x host=ALL user=alice} | sudoers:1={command=sha512:ff /bin/y arg host=ALL runas=bob tags=NOPASSWD user=alice}"},
		{"template", map[string]string{"sudoers.j2": "{% for u in users %}\n{{ u }} ALL = /a\n{% endfor %}\nDefaults secure_path={{ path }}\nDefaults secure_path=/b\n"},
			defaults("sudoers.j2", "secure_path"), "sudoers.j2:4={{ path }} !lit | sudoers.j2:5=/b"},
		{"template grant", map[string]string{"sudoers.j2": "{% if x %}\nalice ALL = /a\n{% endif %}\n{{ u }}, bob ALL = /b\n"},
			user("sudoers.j2", "alice"), "sudoers.j2:2={command=/a host=ALL user=alice} !lit | sudoers.j2:4={command=/b host=ALL user={{ u }},bob} !lit"},
	})
}

// A file sudo refuses to parse, an alias defined twice, one used and
// defined nowhere, and one that holds itself make the read an error.
// Planted break: define accepting a second definition.
func TestRefusals(t *testing.T) {
	runRefusals(t, []refuseCase{
		{"no host", map[string]string{"sudoers": "alice = /a\n"}, user("sudoers", "alice"), "line 1: expected a host"},
		{"no =", map[string]string{"sudoers": "alice ALL /a\n"}, user("sudoers", "alice"), "expected = after the host list"},
		{"bad Defaults", map[string]string{"sudoers": "Defaults !a=1\n"}, defaults("sudoers", "a"), "takes a ! or a value, not both"},
		{"Defaults without comma", map[string]string{"sudoers": "Defaults a b\n"}, defaults("sudoers", "a"), "expected , between"},
		{"alias twice", map[string]string{"sudoers": "User_Alias A = x\nUser_Alias A = y\n"}, defaults("sudoers", "a"), "sudoers: line 2: the alias A is defined at sudoers:1 already"},
		{"alias name", map[string]string{"sudoers": "User_Alias a = x\n"}, defaults("sudoers", "a"), "is no alias name"},
		{"undefined", map[string]string{"sudoers": "ADMINS ALL = /a\n"}, user("sudoers", "alice"), "sudoers:1: the user alias ADMINS is not defined"},
		{"undefined command alias", map[string]string{"sudoers": "alice ALL = SVC\n"}, user("sudoers", "alice"), "the command alias SVC is not defined"},
		{"cycle", map[string]string{"sudoers": "User_Alias A = B\nUser_Alias B = A\nA ALL = /a\n"}, user("sudoers", "alice"), "holds itself"},
		{"unclosed runas", map[string]string{"sudoers": "alice ALL = (root /a\n"}, user("sudoers", "alice"), "expected ) after the Runas list"},
		{"no command", map[string]string{"sudoers": "alice ALL = NOPASSWD:\n"}, user("sudoers", "alice"), "expected a command"},
	})
}
