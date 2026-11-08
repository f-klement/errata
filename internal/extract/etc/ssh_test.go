package etc

import (
	"errors"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

func matching(file, key string, c map[string]string) Spec {
	return Spec{Where: where(file), Key: key, Match: c}
}

const sshdFile = `PasswordAuthentication no
Port 22
Port 2222
AcceptEnv LANG
Match User git,!root Address 10.0.0.0/8,198.51.100.*
  PasswordAuthentication yes
  AcceptEnv GIT_*
Match Group admin,ops LocalPort 2222
  PasswordAuthentication maybe
Match Host *.example.com RDomain blue LocalAddress 127.0.0.1
  PasswordAuthentication host
Match All
  AcceptEnv ALL
`

// With match:, sshd's values are those its Match blocks give the
// connection, first obtained, over those outside any block; a keyword of
// several values takes every one. Planted break: applied reading sshd in
// file order.
func TestSSHDMatch(t *testing.T) {
	files := map[string]string{"sshd_config": sshdFile}
	runReads(t, []readCase{
		{"no block matches", files, matching("sshd_config", "passwordauthentication", map[string]string{"user": "bob", "address": "1.2.3.4", "group": "staff", "localport": "22", "host": "a", "rdomain": "x", "localaddress": "::1"}),
			"sshd_config:1=no"},
		{"user and address", files, matching("sshd_config", "passwordauthentication", map[string]string{"user": "git", "address": "10.1.2.3"}),
			"sshd_config:6=yes [Match User git,!root Address 10.0.0.0/8,198.51.100.*]"},
		{"address by pattern", files, matching("sshd_config", "passwordauthentication", map[string]string{"user": "git", "address": "198.51.100.7"}),
			"sshd_config:6=yes [Match User git,!root Address 10.0.0.0/8,198.51.100.*]"},
		{"group and port", files, matching("sshd_config", "passwordauthentication", map[string]string{"user": "bob", "address": "1.2.3.4", "group": "staff,ops", "localport": "02222"}),
			"sshd_config:9=maybe [Match Group admin,ops LocalPort 2222]"},
		{"host in any case", files, matching("sshd_config", "passwordauthentication", map[string]string{"user": "bob", "address": "1.2.3.4", "group": "x", "localport": "1", "host": "WWW.Example.com", "rdomain": "blue", "localaddress": "127.0.0.1"}),
			"sshd_config:11=host [Match Host *.example.com RDomain blue LocalAddress 127.0.0.1]"},
		{"several values, Match over outside", files, matching("sshd_config", "acceptenv", map[string]string{"user": "git", "address": "10.0.0.1"}),
			"sshd_config:7=GIT_* [Match User git,!root Address 10.0.0.0/8,198.51.100.*] | sshd_config:13=ALL [Match All]"},
		{"several values outside any block", map[string]string{"sshd_config": sshdFile}, matching("sshd_config", "port", map[string]string{"user": "a"}),
			"sshd_config:2=22 | sshd_config:3=2222"},
	})
}

// A Match block errata cannot decide makes the read an error, naming the
// block and why, when it comes before the value OpenSSH would apply; one
// after that value does not. Planted break: an undecided block taken as
// not matching.
func TestSSHDMatchUndecided(t *testing.T) {
	files := map[string]string{"sshd_config": sshdFile}
	for _, c := range []struct {
		match map[string]string
		want  string
	}{
		{map[string]string{"user": "git"}, `sshd_config:6 sets passwordauthentication under "Match User git,!root Address 10.0.0.0/8,198.51.100.*", which errata cannot decide for the connection match: names (user git): Match address needs the connection's address, which match: does not give`},
		{map[string]string{"user": "bob", "address": "1.1.1.1"}, "Match group needs the connection's group"},
		{map[string]string{"user": "bob", "address": "1.1.1.1", "group": "admin"}, "Match localport needs"},
	} {
		_, err := extract(t, files, matching("sshd_config", "passwordauthentication", c.match))
		extracttest.WantError(t, err, c.want)
	}
	got, err := extract(t, files, matching("sshd_config", "passwordauthentication", map[string]string{"user": "git", "address": "10.0.0.1"}))
	if err != nil || got != "sshd_config:6=yes [Match User git,!root Address 10.0.0.0/8,198.51.100.*]" {
		t.Errorf("a value before the undecided blocks: %q, %v", got, err)
	}
	for file, want := range map[string]string{
		"Match LocalAddress 10.0.0.1/8\n X a\n":  "holds a network sshd refuses",
		"Match LocalAddress 10.0.0.0/33\n X a\n": "holds a network sshd refuses",
		"Match LocalPort ssh\n X a\n":            `Match localport "ssh" is no port`,
		"Match User\n X a\n":                     `Match "user" has no argument`,
		"Match Version 9\n X a\n":                `errata does not read the criterion "version"`,
		"Match Exec true\n X a\n":                "Match exec runs a command",
		"Match User a\nInclude b\n":              "", // nothing sets X
		"Match User a Address 1.1.1.1\n X a\n":   "Match address needs",
	} {
		_, err := extract(t, map[string]string{"sshd_config": file, "b": "Y 1\n"}, matching("sshd_config", "x", map[string]string{"user": "a", "localaddress": "1.1.1.1", "localport": "22"}))
		if want == "" {
			if err != nil {
				t.Errorf("%q: %v", file, err)
			}
			continue
		}
		extracttest.WantError(t, err, want)
	}
}

// ssh reads its file in order, a value outside any block applying always,
// and the first value obtained wins. A Host line holds when the host, in
// lower case, matches a pattern and no negated one; a ! negates a Match
// criterion. Planted break: the ! of a criterion ignored.
func TestSSHMatch(t *testing.T) {
	file := `Host *.internal !bastion.internal
  User ops
  IdentityFile ~/.ssh/internal
Match !host *.internal user deploy
  User deployer
Match originalhost gw localuser me tagged x
  User gw
Host *
  User nobody
  IdentityFile ~/.ssh/id
`
	files := map[string]string{"ssh_config": file}
	runReads(t, []readCase{
		{"host", files, matching("ssh_config", "user", map[string]string{"host": "DB.internal", "user": "x"}), "ssh_config:2=ops [Host *.internal !bastion.internal]"},
		{"negated host", files, matching("ssh_config", "user", map[string]string{"host": "bastion.internal", "user": "x", "originalhost": "a"}), "ssh_config:9=nobody [Host *]"},
		{"negated criterion", files, matching("ssh_config", "user", map[string]string{"host": "a.example", "user": "deploy"}), "ssh_config:5=deployer [Match !host *.internal user deploy]"},
		{"every criterion", files, matching("ssh_config", "user", map[string]string{"host": "a.example", "user": "x", "originalhost": "gw", "localuser": "me", "tagged": "x"}), "ssh_config:7=gw [Match originalhost gw localuser me tagged x]"},
		{"several values", files, matching("ssh_config", "identityfile", map[string]string{"host": "a.internal"}),
			"ssh_config:3=~/.ssh/internal [Host *.internal !bastion.internal] !lit | ssh_config:10=~/.ssh/id [Host *] !lit"},
	})
	_, err := extract(t, files, matching("ssh_config", "user", map[string]string{"user": "x"}))
	extracttest.WantError(t, err, "a Host line matches the host, which match: does not give")
	for line, want := range map[string]string{
		"Match canonical\n User a\n":       "Match canonical holds on a pass",
		"Match final all\n User a\n":       "Match final holds on a pass",
		"Match localnetwork 10/8\n User a": `errata does not read the criterion "localnetwork"`,
	} {
		_, err := extract(t, map[string]string{"ssh_config": line}, matching("ssh_config", "user", map[string]string{"host": "h"}))
		extracttest.WantError(t, err, want)
	}
}

// wild is OpenSSH's match_pattern, and it charges the product of the
// lengths before it runs. Planted break: ? taken as a literal.
func TestWild(t *testing.T) {
	for _, c := range []struct {
		s, p string
		want bool
	}{
		{"", "", true}, {"", "*", true}, {"", "?", false}, {"a", "", false},
		{"abc", "a?c", true}, {"abc", "a*", true}, {"abc", "*c", true}, {"abc", "*b*", true},
		{"abcbd", "a*bd", true}, {"abcbe", "a*bd", false}, {"ab", "a**", true}, {"aXbXc", "a*b*c", true},
		{"abc", "abcd", false}, {"abc", "ab", false}, {"aaa", "a*a*a*a", false},
	} {
		got, err := wild(c.s, c.p, nil)
		if err != nil || got != c.want {
			t.Errorf("wild(%q, %q) = %t, %v; want %t", c.s, c.p, got, err, c.want)
		}
	}
	m := &core.Meter{Budget: 3 * 4}
	if _, err := wild("ab", "abc", m); err != nil {
		t.Errorf("a match of 12 units under a budget of 12: %v", err)
	}
	m = &core.Meter{Budget: 3*4 - 1}
	if _, err := wild("ab", "abc", m); !errors.Is(err, core.ErrWork) {
		t.Errorf("a match of 12 units under a budget of 11: %v", err)
	}
}

// The values outside any block of sshd are those no Match block reached:
// a Match line opens a block to the end of its file, and an included file's
// blocks end with it. Planted break: readOpenSSH's block kept across the
// files it reads.
func TestBlockEndsWithItsFile(t *testing.T) {
	runReads(t, []readCase{
		{"included Match", map[string]string{"sshd_config": "Include a.conf\nPort 1\n", "a.conf": "Match User x\nPort 2\n"},
			one("sshd_config", "port"), "a.conf:2=2 [Match User x] | sshd_config:2=1"},
	})
}
