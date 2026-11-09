package etc

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Includes are read in place, inside the repository: a relative path and an
// absolute one under the tool's home below the directory of the file the
// spec names, a glob's files in name order, a directory's files for
// logrotate without the leftovers it passes over, chrony's confdir by name
// across its directories, and a file an include names outright that is not
// there passed over. An Include inside a block puts its values under that
// block. Planted breaks: taboo not asked; the outer block left off a value
// an included file sets under a block of its own.
func TestIncludes(t *testing.T) {
	runReads(t, []readCase{
		{"sshd Include, Debian's", map[string]string{
			"ssh/sshd_config":            "Include /etc/ssh/sshd_config.d/*.conf\nPort 22\nMatch User git\n  Include extra.conf\n",
			"ssh/sshd_config.d/b.conf":   "Port 2\n",
			"ssh/sshd_config.d/a.conf":   "Port 1\n",
			"ssh/sshd_config.d/c.conf~":  "Port 9\n",
			"ssh/extra.conf":             "Port 3\nMatch Address 10.0.0.1\nPort 4\n",
			"ssh/sshd_config.d/x/d.conf": "Port 8\n",
		}, one("ssh/sshd_config", "port"),
			"ssh/sshd_config.d/a.conf:1=1 | ssh/sshd_config.d/b.conf:1=2 | ssh/sshd_config:2=22 | ssh/extra.conf:1=3 [Match User git] | ssh/extra.conf:3=4 [Match User git; Match Address 10.0.0.1]"},
		{"ssh Include not there", map[string]string{"ssh_config": "Include missing.conf \"q d/*.conf\"\nUser a\n", "q d/x.conf": "User b\n"},
			one("ssh_config", "user"), "q d/x.conf:1=b | ssh_config:2=a"},
		{"logrotate include of a directory", map[string]string{
			"etc/logrotate.conf":                    "rotate 4\ninclude /etc/logrotate.d\n",
			"etc/logrotate.d/nginx":                 "/var/log/nginx/*.log {\n  rotate 14\n}\n",
			"etc/logrotate.d/apt":                   "/var/log/apt/term.log {\n  rotate 12\n}\n",
			"etc/logrotate.d/apt.dpkg-old":          "/x {\n  rotate 1\n}\n",
			"etc/logrotate.d/apt.rhn-cfg-tmp-1":     "/x {\n  rotate 1\n}\n",
			"etc/logrotate.d/apt.rpmsave":           "/x {\n  rotate 1\n}\n",
			"etc/logrotate.d/sub/never-in-the-glob": "rotate 1\n",
		}, one("etc/logrotate.conf", "rotate"),
			"etc/logrotate.conf:1=4 | etc/logrotate.d/apt:2=12 [/var/log/apt/term.log] | etc/logrotate.d/nginx:2=14 [/var/log/nginx/*.log]"},
		{"logrotate include of a file", map[string]string{"logrotate.conf": "include more\nrotate 2\n", "more": "rotate 1\n"},
			one("logrotate.conf", "rotate"), "more:1=1 | logrotate.conf:2=2"},
		{"chrony on Debian", map[string]string{
			"chrony/chrony.conf":        "confdir /etc/chrony/conf.d /etc/chrony/more.d\nsourcedir /run/chrony-dhcp\nsourcedir /etc/chrony/sources.d\nmakestep 1 3\npool p iburst\n",
			"chrony/conf.d/b.conf":      "makestep 2 3\n",
			"chrony/more.d/a.conf":      "makestep 3 3\n",
			"chrony/more.d/b.conf":      "makestep 9 9\n",
			"chrony/sources.d/x.source": "pool never\n",
		}, one("chrony/chrony.conf", "makestep"),
			"chrony/more.d/a.conf:1=3 3 | chrony/conf.d/b.conf:1=2 3 | chrony/chrony.conf:4=1 3"},
		{"chrony on Fedora", map[string]string{"chrony.conf": "include /etc/chrony.d/*.conf\nserver a\n", "chrony.d/x.conf": "server b\n"},
			one("chrony.conf", "server"), "chrony.d/x.conf:1=b | chrony.conf:2=a"},
		{"ntp includefile", map[string]string{"ntp.conf": "includefile /etc/ntp/servers\n", "ntp/servers": "server a\n"},
			one("ntp.conf", "server"), "ntp/servers:1=a"},
	})
}

// An include errata cannot read inside the repository makes the read an
// error, since what it would set is unknown: a path under no home, one from
// ~, one that climbs out, and one a template computes. chrony's sourcedir
// holds only sources, so it does so only for server, pool and peer.
// Planted break: an outside include passed over.
func TestIncludesOutsideAreRefused(t *testing.T) {
	for _, c := range []struct {
		files map[string]string
		spec  Spec
		want  string
	}{
		{map[string]string{"sshd_config": "Include /usr/local/etc/ssh/x.conf\nPort 22\n"}, one("sshd_config", "port"),
			`sshd_config: line 1: include "/usr/local/etc/ssh/x.conf" names "/usr/local/etc/ssh/x.conf", outside the repository`},
		{map[string]string{"ssh_config": "Include ~/.ssh/extra\n"}, one("ssh_config", "user"), `names "~/.ssh/extra", outside the repository`},
		{map[string]string{"a/ntp.conf": "includefile ../../x\n"}, one("a/ntp.conf", "server"), `names "../../x", outside`},
		{map[string]string{"sshd_config.j2": "Include {{ dir }}/*.conf\n"}, one("sshd_config.j2", "port"), "a template engine computes"},
		{map[string]string{"chrony.conf": "sourcedir /run/chrony-dhcp\n"}, one("chrony.conf", "pool"), `names "/run/chrony-dhcp", outside`},
		{map[string]string{"sshd_config": "Include sshd_config\n"}, one("sshd_config", "port"), "includes nest more than 16 deep"},
	} {
		_, err := extract(t, c.files, c.spec)
		extracttest.WantError(t, err, c.want)
	}
	got, err := extract(t, map[string]string{"chrony.conf": "sourcedir /run/chrony-dhcp\nmakestep 1 3\n"}, one("chrony.conf", "makestep"))
	if err != nil || got != "chrony.conf:2=1 3" {
		t.Errorf("makestep beside an outside sourcedir: %q, %v", got, err)
	}
}

// files: reads a directory of drop-ins in the tool's order, whatever the
// order written: sysctl by base name, a name two files share read from the
// first listed, and sysctl.conf last; limits.conf first; modprobe by name.
// Planted break: sysctlOrder not moving sysctl.conf last.
func TestDropInOrder(t *testing.T) {
	runReads(t, []readCase{
		{"sysctl", map[string]string{
			"etc/sysctl.conf":         "vm.swappiness = 1\n",
			"etc/sysctl.d/99-z.conf":  "vm.swappiness = 99\n",
			"etc/sysctl.d/10-a.conf":  "vm.swappiness = 10\n",
			"usr/sysctl.d/10-a.conf":  "vm.swappiness = 0\n",
			"usr/sysctl.d/50-m.conf":  "vm.swappiness = 50\n",
			"etc/sysctl.d/README.txt": "vm.swappiness = 7\n",
			"etc/sysctl.d/zz.conf":    "vm.swappiness = 26\n",
		}, Spec{Where: core.Where{Files: []string{"etc/sysctl.conf", "etc/sysctl.d/*.conf", "usr/sysctl.d/*.conf"}}, Key: "vm/swappiness"},
			"etc/sysctl.d/10-a.conf:1=10 | usr/sysctl.d/50-m.conf:1=50 | etc/sysctl.d/99-z.conf:1=99 | etc/sysctl.d/zz.conf:1=26 | etc/sysctl.conf:1=1"},
		{"limits", map[string]string{"limits.d/a.conf": "* soft nproc 1\n", "limits.conf": "* soft nproc 0\n", "limits.d/0.conf": "* soft nproc 2\n"},
			Spec{Where: core.Where{Files: []string{"limits.d/*.conf", "limits.conf"}}, Key: "nproc"},
			"limits.conf:1=0 [* soft] | limits.d/0.conf:1=2 [* soft] | limits.d/a.conf:1=1 [* soft]"},
		{"modprobe", map[string]string{"lib/modprobe.d/b.conf": "blacklist b\n", "etc/modprobe.d/a.conf": "blacklist a\n", "lib/modprobe.d/a.conf": "blacklist c\n"},
			Spec{Where: core.Where{Files: []string{"etc/modprobe.d/*.conf", "lib/modprobe.d/*.conf"}}, Key: "blacklist"},
			"etc/modprobe.d/a.conf:1=a | lib/modprobe.d/b.conf:1=b"},
	})
}

// Build refuses a spec it cannot read, by the reason. Planted break: the
// port bound at 65536.
func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Where: where("a.conf"), Key: "k"}, `etc: no dialect reads a file named "a.conf"; write dialect: one of sshd, ssh, sysctl`},
		{Spec{Where: where("etc/fstab"), Key: "k"}, `, ntp; the table kind reads a field table such as fstab or passwd, the sudoers kind a sudoers file, and the udev kind udev rules`},
		{Spec{Where: where("a.conf"), Dialect: "fstab", Key: "k"}, `etc: dialect "fstab" is not one errata reads`},
		{Spec{Where: core.Where{File: "sysctl.conf", Files: []string{"sysctl.d/*.conf"}}, Key: "k"}, "etc: file and files are both set"},
		{Spec{Where: core.Where{Files: []string{"sshd_config"}}, Key: "k"}, "etc: the sshd dialect reads one file and the files it includes, so files is refused"},
		{Spec{Where: core.Where{Files: []string{}}, Dialect: "sysctl", Key: "k"}, "etc: files lists no entry"},
		{Spec{Where: core.Where{Files: []string{"/abs.conf"}}, Dialect: "sysctl", Key: "k"}, "outside the repository"},
		{Spec{Where: where("../sshd_config"), Key: "k"}, "outside the repository"},
		{Spec{Where: where("sshd_config"), Key: " "}, "etc: key is required"},
		{Spec{Where: where("sshd_config"), Key: "a b"}, "etc: sshd: key \"a b\" holds white space"},
		{Spec{Where: where("resolv.conf"), Key: "nameservers"}, "etc: resolv: key \"nameservers\" is no keyword glibc reads"},
		{Spec{Where: where("modprobe.d/a.conf"), Key: "option"}, "etc: modprobe: key \"option\" is no command kmod reads"},
		{Spec{Where: where("limits.conf"), Key: "both nofile"}, "etc: limits: key \"both nofile\" is no item"},
		{Spec{Where: where("limits.conf"), Key: "soft nofile x"}, "is no item"},
		{Spec{Where: where("sysctl.conf"), Key: "k", Block: str("x")}, "etc: the sysctl dialect has no blocks, so block is refused"},
		{Spec{Where: where("logrotate.conf"), Key: "k", Match: map[string]string{"user": "a"}}, "etc: the logrotate dialect has no Match blocks"},
		{Spec{Where: where("sshd_config"), Key: "k", Block: str(""), Match: map[string]string{"user": "a"}}, "etc: block and match are both set"},
		{Spec{Where: where("sshd_config"), Key: "k", Match: map[string]string{"exec": "a"}}, `etc: match names "exec", which is no criterion of sshd errata matches; write one of address, group`},
		{Spec{Where: where("ssh_config"), Key: "k", Match: map[string]string{"User": "a", "user": "b"}}, `etc: match names "user" twice`},
		{Spec{Where: where("sshd_config"), Key: "k", Match: map[string]string{"address": "10.0.0.0/8"}}, `etc: match address is "10.0.0.0/8", which is no IP address`},
		{Spec{Where: where("sshd_config"), Key: "k", Match: map[string]string{"localaddress": "x"}}, `etc: match localaddress is "x"`},
		{Spec{Where: where("sshd_config"), Key: "k", Match: map[string]string{"localport": "0"}}, `etc: match localport is "0", which is no port`},
		{Spec{Where: where("sshd_config"), Key: "k", Match: map[string]string{"localport": "65536"}}, `which is no port`},
		{Spec{Where: where("sshd_config"), Key: "k", Match: map[string]string{"localport": "x"}}, `which is no port`},
	} {
		_, err := Build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
	for _, s := range []Spec{
		{Where: where("sshd_config"), Key: "k", Match: map[string]string{"localport": "65535", "address": "::1"}},
		{Where: where("sshd_config"), Key: "k", Match: map[string]string{"localport": "1"}},
		{Where: where("x/limits.d/a.conf"), Key: "- nofile"},
	} {
		if _, err := Build(s); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
}

// The values a read returns are held to the value limit, and the files it
// reads to max_bytes in all, each refusal naming its bound. Planted break:
// the value limit one higher.
func TestReadLimits(t *testing.T) {
	x, err := Build(one("sysctl.conf", "a"))
	if err != nil {
		t.Fatal(err)
	}
	e := x.(extractor)
	e.valueLimit = 2
	root := extracttest.Tracked(t, map[string]string{"sysctl.conf": "a=1\na=2\na=3\n", "big.conf": "a=" + strings.Repeat("x", 40) + "\n",
		"sshd_config": "Include x.conf\nPort 1\n", "x.conf": strings.Repeat("#", 30) + "\n"})
	_, err = e.Extract(t.Context(), root)
	extracttest.WantError(t, err, "sysctl.conf: the extractor selects more than")
	x, _ = Build(Spec{Where: core.Where{File: "big.conf", MaxBytes: 44}, Dialect: "sysctl", Key: "a"})
	if _, err := x.Extract(t.Context(), root); err != nil {
		t.Fatalf("a file of 43 bytes under max_bytes 44: %v", err)
	}
	x, _ = Build(Spec{Where: core.Where{File: "sysctl.conf", MaxBytes: 12}, Key: "a"})
	if got, err := x.Extract(t.Context(), root); err != nil || len(got) != 3 {
		t.Fatalf("12 bytes under max_bytes 12: %v %v", got, err)
	}
	x, _ = Build(Spec{Where: core.Where{File: "sshd_config", MaxBytes: 40}, Key: "port"})
	_, err = x.Extract(t.Context(), root)
	extracttest.WantError(t, err, "the files the extractor reads hold at most max_bytes, 40, in all")
}

// A template file is read as the file it renders, and a value with the
// engine's marks, or inside one of its blocks, is not literal. Planted
// break: blocks.Holds' answer dropped.
func TestTemplates(t *testing.T) {
	runReads(t, []readCase{
		{"jinja", map[string]string{"roles/ssh/templates/sshd_config.j2": "Port {{ port }}\n{% if x %}\nPort 2222\n{% endif %}\nPort 22\n"},
			one("roles/ssh/templates/sshd_config.j2", "port"),
			"roles/ssh/templates/sshd_config.j2:1={{ port }} !lit | roles/ssh/templates/sshd_config.j2:3=2222 !lit | roles/ssh/templates/sshd_config.j2:5=22"},
		{"sysctl", map[string]string{"sysctl.conf.j2": "a = {{ a }}\nb = 1\n"}, one("sysctl.conf.j2", "a"), "sysctl.conf.j2:1={{ a }} !lit"},
	})
}
