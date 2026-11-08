package etc

import (
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// ssh matches Match user, host and tagged against the User, Hostname and
// Tag a line before the Match line sets, so such a line that may apply to
// the connection leaves the criterion undecided; one under a block that
// does not apply does not. Planted break: shadow returning "" always.
func TestSSHMatchShadowedByEarlierLines(t *testing.T) {
	file := "Host prod\n  User deploy\nMatch user deploy\n  IdentityFile /k/deploy\nHost other\n  Tag t\nMatch tagged t\n  IdentityFile /k/t\n"
	_, err := extract(t, map[string]string{"ssh_config": file}, matching("ssh_config", "identityfile", map[string]string{"host": "prod", "user": "alice"}))
	extracttest.WantError(t, err, `ssh_config:4 sets identityfile under "Match user deploy", which errata cannot decide for the connection match: names (host prod, user alice): ssh_config:2 sets user before the Match line, and ssh matches user against what it sets`)
	got, err := extract(t, map[string]string{"ssh_config": file}, matching("ssh_config", "identityfile", map[string]string{"host": "web", "user": "alice", "tagged": "u"}))
	if err != nil || got != "" {
		t.Errorf("a User and a Tag under Host blocks that do not apply: %q, %v", got, err)
	}
	got, err = extract(t, map[string]string{"ssh_config": "Match user deploy\n  IdentityFile /k\nUser deploy\n"}, matching("ssh_config", "identityfile", map[string]string{"user": "alice"}))
	if err != nil || got != "" {
		t.Errorf("a User after the Match line: %q, %v", got, err)
	}
}

// A shared name in two drop-in directories is read from the one the tool
// ranks first, whatever the order files: lists them, and only .conf files
// of a .d directory are read. Planted break: dropIns not sorting by
// dirRank.
func TestDropInsRankTheToolsDirectories(t *testing.T) {
	runReads(t, []readCase{
		{"sysctl", map[string]string{"usr/lib/sysctl.d/50-x.conf": "a=vendor\n", "etc/sysctl.d/50-x.conf": "a=admin\n", "run/sysctl.d/60-y.conf": "a=run\n", "etc/sysctl.d/README": "a=no\n"},
			Spec{Where: core.Where{Files: []string{"usr/lib/sysctl.d/*", "etc/sysctl.d/*", "run/sysctl.d/*.conf"}}, Key: "a"},
			"etc/sysctl.d/50-x.conf:1=admin | run/sysctl.d/60-y.conf:1=run"},
		{"limits", map[string]string{"usr/etc/security/limits.d/a.conf": "* - nofile 1\n", "etc/security/limits.d/a.conf": "* - nofile 2\n", "etc/security/limits.conf": "* - nofile 0\n"},
			Spec{Where: core.Where{Files: []string{"usr/etc/security/limits.d/*.conf", "etc/security/limits.d/*.conf", "etc/security/limits.conf"}}, Key: "nofile"},
			"etc/security/limits.conf:1=0 [* -] | etc/security/limits.d/a.conf:1=2 [* -]"},
		{"modprobe", map[string]string{"lib/modprobe.d/a.conf": "mask x\n", "usr/local/lib/modprobe.d/a.conf": "mask y\n"},
			Spec{Where: core.Where{Files: []string{"lib/modprobe.d/*.conf", "usr/local/lib/modprobe.d/*.conf"}}, Key: "mask"},
			"usr/local/lib/modprobe.d/a.conf:1=y"},
	})
}

// Includes match as glob(3): no * matches a name that starts with a ., and
// ** is *. ~/.ssh is the home of a user's config, /etc/ssh of any other
// file. chrony's homes follow where its file sits. Planted breaks: hidden
// never asked; sshHomes giving /etc/ssh to a user's config; chronyHomes
// mapping both homes to the file's directory.
func TestIncludesAsTheToolsReadThem(t *testing.T) {
	runReads(t, []readCase{
		{"dot files", map[string]string{"sshd_config": "Include sshd_config.d/*.conf d/**/*.conf\n", "sshd_config.d/.hidden.conf": "Port 1\n", "sshd_config.d/a.conf": "Port 2\n", "d/a/b/c.conf": "Port 3\n", "d/a/c.conf": "Port 4\n"},
			one("sshd_config", "port"), "sshd_config.d/a.conf:1=2 | d/a/c.conf:1=4"},
		{"user config", map[string]string{"home/.ssh/config": "Include ~/.ssh/config.d/*\nInclude more\n", "home/.ssh/config.d/a": "User a\n", "home/.ssh/more": "User b\n"},
			one("home/.ssh/config", "user"), "home/.ssh/config.d/a:1=a | home/.ssh/more:1=b"},
		{"chrony on Debian, /etc", map[string]string{"etc/chrony/chrony.conf": "include /etc/chrony.d/*.conf\n", "etc/chrony.d/x.conf": "server a\n"},
			one("etc/chrony/chrony.conf", "server"), "etc/chrony.d/x.conf:1=a"},
		{"chrony on Fedora, /etc/chrony", map[string]string{"etc/chrony.conf": "confdir /etc/chrony/conf.d\n", "etc/chrony/conf.d/x.conf": "server a\n"},
			one("etc/chrony.conf", "server"), "etc/chrony/conf.d/x.conf:1=a"},
	})
	_, err := extract(t, map[string]string{"sshd_config": "Include ~/.ssh/x\n"}, one("sshd_config", "port"))
	extracttest.WantError(t, err, `names "~/.ssh/x", outside the repository`)
	_, err = extract(t, map[string]string{"chrony.conf": "include /etc/chrony/missing.conf\n"}, one("chrony.conf", "server"))
	extracttest.WantError(t, err, "missing.conf")
}

// Match criteria as OpenSSH writes them: name=value is one word, sshd's
// invalid-user takes none, !all holds for none, and a pattern of a * before
// a literal * matches. Single quotes quote as double quotes do, and kmod's
// mask is a command. Planted breaks: criterionAt not cutting at =;
// negated not applied to all; argv.step not taking a single quote.
func TestOpenSSHForms(t *testing.T) {
	runReads(t, []readCase{
		{"name=value", map[string]string{"sshd_config": "X 0\nMatch User=git Address=10.0.0.0/8\n X 1\n"}, matching("sshd_config", "x", map[string]string{"user": "git", "address": "10.0.0.1"}), "sshd_config:3=1 [Match User=git Address=10.0.0.0/8]"},
		{"!all", map[string]string{"ssh_config": "Match !all\n User a\nHost *\n User b\n"}, matching("ssh_config", "user", map[string]string{"host": "h"}), "ssh_config:4=b [Host *]"},
		{"single quotes", map[string]string{"sshd_config": "Banner '/x #y' #z\n"}, one("sshd_config", "banner"), "sshd_config:1='/x #y'"},
		{"single quoted include", map[string]string{"sshd_config": "Include 'a b.conf'\n", "a b.conf": "Port 7\n"}, one("sshd_config", "port"), "a b.conf:1=7"},
		{"mask", map[string]string{"modprobe.d/a.conf": "mask pcspkr\n"}, one("modprobe.d/a.conf", "mask"), "modprobe.d/a.conf:1=pcspkr"},
	})
	_, err := extract(t, map[string]string{"sshd_config": "Match invalid-user User git\n X 1\n"}, matching("sshd_config", "x", map[string]string{"user": "git"}))
	extracttest.WantError(t, err, "Match invalid-user holds for a user the host does not know")
	if ok, _ := wild("**x", "*x", nil); !ok {
		t.Error("*x does not match **x")
	}
}

// A tree of a host's root states the chrony layout: an empty confdir is
// read as empty, though the other directory a role may keep it in holds
// files. Planted break: rootAlt giving alt whatever the directory's name.
func TestRootfsStatesTheChronyLayout(t *testing.T) {
	runReads(t, []readCase{
		{"fedora, an unrelated etc/conf.d", map[string]string{"etc/chrony.conf": "confdir /etc/chrony/conf.d\nmakestep 1 3\n", "etc/conf.d/x.conf": "makestep 9 9\n"},
			one("etc/chrony.conf", "makestep"), "etc/chrony.conf:2=1 3"},
		{"debian, a confdir under /etc", map[string]string{"etc/chrony/chrony.conf": "confdir /etc/x.d\nmakestep 1 3\n", "etc/chrony/x.d/a.conf": "makestep 9 9\n"},
			one("etc/chrony/chrony.conf", "makestep"), "etc/chrony/chrony.conf:2=1 3"},
	})
}

// Delta review 1. chrony refuses a layout it cannot tell: an include that
// matches nothing where errata maps it while the same path beside
// chrony.conf holds files (D2). logrotate's directory include reads dot
// files, which only a glob passes over (D3). A hidden file of a drop-in
// directory is passed over (D4). Hostname shadows Match host, and one Match
// text before a setter and after it are two verdicts (D5). A \ before a tab
// stays a byte of the word, and the tab ends it (D6). Planted breaks:
// oneLayout returning nil; path listing through list; notConf without its
// dot test; shadowed returning "".
func TestDeltaOneForms(t *testing.T) {
	_, err := extract(t, map[string]string{"roles/chrony/files/chrony.conf": "confdir /etc/chrony/conf.d\nmakestep 1 3\n", "roles/chrony/files/conf.d/x.conf": "makestep 9 9\n"}, one("roles/chrony/files/chrony.conf", "makestep"))
	extracttest.WantError(t, err, `names "/etc/chrony/conf.d", which errata reads at "roles/chrony/files/chrony/conf.d", where no file is, while "roles/chrony/files/conf.d" holds files`)
	runReads(t, []readCase{
		{"chrony layout errata reads", map[string]string{"roles/chrony/files/chrony.conf": "confdir /etc/chrony/conf.d\n", "roles/chrony/files/chrony/conf.d/x.conf": "makestep 9 9\n", "roles/chrony/files/conf.d/y.conf": "makestep 1 1\n"},
			one("roles/chrony/files/chrony.conf", "makestep"), "roles/chrony/files/chrony/conf.d/x.conf:1=9 9"},
		{"logrotate dot files", map[string]string{"etc/logrotate.conf": "include /etc/logrotate.d\n", "etc/logrotate.d/.app": "weekly\n", "etc/logrotate.d/.x.swp": "weekly\n", "etc/logrotate.d/b": "/x {\n weekly\n}\n", "etc/logrotate.d/b.swp": "weekly\n"},
			one("etc/logrotate.conf", "weekly"), "etc/logrotate.d/.app:1= | etc/logrotate.d/.x.swp:1= | etc/logrotate.d/b:2= [/x]"},
		{"modprobe hidden", map[string]string{"etc/modprobe.d/.old.conf": "blacklist old\n", "etc/modprobe.d/a.conf": "blacklist a\n"},
			Spec{Where: core.Where{Files: []string{"etc/modprobe.d/*"}}, Key: "blacklist"}, "etc/modprobe.d/a.conf:1=a"},
		{"backslash before a tab", map[string]string{"sshd_config": "Banner a\\\t#x\n"}, one("sshd_config", "banner"), "sshd_config:1=a\\"},
	})
	_, err = extract(t, map[string]string{"ssh_config": "Hostname real\nMatch host real\n  IdentityFile /k\n"}, matching("ssh_config", "identityfile", map[string]string{"host": "alias"}))
	extracttest.WantError(t, err, "ssh_config:1 sets hostname before the Match line, and ssh matches host against what it sets")
	_, err = extract(t, map[string]string{"ssh_config": "Match user deploy\n  IdentityFile /a\nHost *\n  User deploy\nMatch user deploy\n  IdentityFile /b\n"}, matching("ssh_config", "identityfile", map[string]string{"user": "alice", "host": "h"}))
	extracttest.WantError(t, err, `ssh_config:6 sets identityfile under "Match user deploy"`)
}
