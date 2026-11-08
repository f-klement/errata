package etc

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// show lists values as file:line=text, with the condition and a not
// literal mark when a value has them.
func show(vs []check.Value) string {
	var parts []string
	for _, v := range vs {
		s := fmt.Sprintf("%s:%d=%s", v.At.File, v.At.Line, v.Text)
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

func str(s string) *string { return &s }

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

func where(file string) core.Where { return core.Where{File: file} }

func one(file, key string) Spec { return Spec{Where: where(file), Key: key} }

// Each dialect reads a line as its tool does: the separators, comments and
// case its tool takes, the value at the line of its keyword. Planted break:
// sshd's comment that starts a word kept.
func TestDialectsReadTheirLines(t *testing.T) {
	runReads(t, []readCase{
		{"sshd separators and case", map[string]string{"sshd_config": "# Port 1\nport=22\n  PORT = 2222 # admin\nPort\t\"a #b\" #c\n"},
			one("sshd_config", "Port"), "sshd_config:2=22 | sshd_config:3=2222 | sshd_config:4=\"a #b\""},
		{"sshd blocks", map[string]string{"etc/ssh/sshd_config": "PasswordAuthentication no\nMatch User git\n  PasswordAuthentication yes\nmatch   Address 10.0.0.0/8\nPasswordAuthentication yes\n"},
			one("etc/ssh/sshd_config", "passwordauthentication"), "etc/ssh/sshd_config:1=no | etc/ssh/sshd_config:3=yes [Match User git] | etc/ssh/sshd_config:5=yes [Match Address 10.0.0.0/8]"},
		{"sshd tokens are not literal", map[string]string{"sshd_config": "AuthorizedKeysFile .ssh/keys %h/.ssh/x\nBanner /etc/100%%\n"},
			one("sshd_config", "authorizedkeysfile"), "sshd_config:1=.ssh/keys %h/.ssh/x !lit"},
		{"sshd %% is literal", map[string]string{"sshd_config": "Banner /etc/100%%\n"},
			one("sshd_config", "banner"), "sshd_config:1=/etc/100%%"},
		{"ssh Host blocks", map[string]string{"home/.ssh/config": "Host web*\n  User deploy\nHost *\n  User root\n"},
			one("home/.ssh/config", "user"), "home/.ssh/config:2=deploy [Host web*] | home/.ssh/config:4=root [Host *]"},
		{"ssh ~ is not literal", map[string]string{"ssh_config": "IdentityFile ~/.ssh/id\nIdentityFile ${HOME}/x\n"},
			one("ssh_config", "identityfile"), "ssh_config:1=~/.ssh/id !lit | ssh_config:2=${HOME}/x !lit"},
		{"sysctl", map[string]string{"sysctl.conf": "# net.ipv4.ip_forward = 0\n; x=1\nnet/ipv4/ip_forward=1\n -net.ipv4.ip_forward =  0 \nnoequals\n"},
			one("sysctl.conf", "net.ipv4.ip_forward"), "sysctl.conf:3=1 | sysctl.conf:4=0"},
		{"sysctl key in the / form", map[string]string{"sysctl.conf": "kernel.domainname = example.com\n"},
			one("sysctl.conf", "kernel/domainname"), "sysctl.conf:1=example.com"},
		{"resolv", map[string]string{"resolv.conf": "nameserver 1.1.1.1#c\n nameserver 9.9.9.9\n;nameserver 8.8.8.8\nnameserver\t8.8.4.4 x\nnameserver\n"},
			one("resolv.conf", "nameserver"), "resolv.conf:1=1.1.1.1 | resolv.conf:4=8.8.4.4"},
		{"resolv domain and search", map[string]string{"resolv.conf": "domain a.example b\nsearch a.example b.example \n"},
			one("resolv.conf", "search"), "resolv.conf:2=a.example b.example"},
		{"limits", map[string]string{"limits.conf": "* soft nofile 1024 # c\n@admin hard nofile 4096\n* - nofile 2048\nbad soft nofile\nx y nofile 1  2\n"},
			one("limits.conf", "nofile"), "limits.conf:1=1024 [* soft] | limits.conf:2=4096 [@admin hard] | limits.conf:3=2048 [* -] | limits.conf:5=1  2 [x y]"},
		{"limits in any case", map[string]string{"limits.conf": "* Hard NOFILE Unlimited\n"},
			one("limits.conf", "HARD nofile"), "limits.conf:1=unlimited [* hard]"},
		{"limits by type", map[string]string{"limits.conf": "* soft nofile 1024\n* hard nofile 4096\n* - nofile 2048\n"},
			one("limits.conf", "hard  nofile"), "limits.conf:2=4096 [* hard] | limits.conf:3=2048 [* -]"},
		{"logrotate", map[string]string{"logrotate.conf": "weekly\nrotate 4\n/var/log/wtmp  /var/log/btmp {\n  rotate 1\n  postrotate\n    rotate 9\n  endscript\n}\n# rotate 7\n"},
			one("logrotate.conf", "rotate"), "logrotate.conf:2=4 | logrotate.conf:4=1 [/var/log/wtmp /var/log/btmp]"},
		{"logrotate flag", map[string]string{"logrotate.conf": "compress\n"},
			one("logrotate.conf", "compress"), "logrotate.conf:1="},
		{"nsswitch", map[string]string{"nsswitch.conf": "passwd:   files systemd # c\nhosts: files [NOTFOUND=return] dns\n#hosts: x\n"},
			one("nsswitch.conf", "hosts"), "nsswitch.conf:2=files [NOTFOUND=return] dns"},
		{"modprobe", map[string]string{"modprobe.d/a.conf": "options snd-hda-intel power_save=1 \\\n  model=auto\n# options snd_hda_intel x=1\nblacklist pcspkr extra\n"},
			one("modprobe.d/a.conf", "options"), "modprobe.d/a.conf:1=power_save=1   model=auto [snd_hda_intel]"},
		{"modprobe blacklist", map[string]string{"modprobe.d/a.conf": "blacklist pcspkr extra\nblacklist\tnouveau\\"},
			one("modprobe.d/a.conf", "blacklist"), "modprobe.d/a.conf:1=pcspkr | modprobe.d/a.conf:2=nouveau"},
		{"chrony", map[string]string{"chrony.conf": "Pool  2.debian.pool.ntp.org \t iburst\n! pool x\n%pool y\nmakestep 1 3\n"},
			one("chrony.conf", "pool"), "chrony.conf:1=2.debian.pool.ntp.org iburst"},
		{"ntp", map[string]string{"ntp.conf": "server 0.pool.ntp.org iburst # c\n#server x\nServer y\n"},
			one("ntp.conf", "server"), "ntp.conf:1=0.pool.ntp.org iburst"},
	})
}

// A lone CR, a CRLF and a LF each end a line, in every dialect, as every
// kind numbers lines. Planted break: eachLine's lines cut at a LF alone.
func TestLoneCRLines(t *testing.T) {
	runReads(t, []readCase{
		{"sshd", map[string]string{"sshd_config": "# c\rPort 22\r\nPort 23"}, one("sshd_config", "port"), "sshd_config:2=22 | sshd_config:3=23"},
		{"sysctl", map[string]string{"sysctl.conf": "a=1\rb=2\rc=3"}, one("sysctl.conf", "c"), "sysctl.conf:3=3"},
		{"modprobe", map[string]string{"modprobe.d/m.conf": "#\roptions m a=1 \\\r b=2\roptions m c=3"}, one("modprobe.d/m.conf", "options"), "modprobe.d/m.conf:2=a=1  b=2 [m] | modprobe.d/m.conf:4=c=3 [m]"},
	})
}

// block: keeps the values of one block as written, and "" those outside
// any. Planted break: "" taken as no filter for a value outside any block.
func TestBlock(t *testing.T) {
	sshd := map[string]string{"sshd_config": "X 0\nmatch user  git\nX 1\nMatch User git\nX 2\nMatch User other\nX 3\n"}
	logrotate := map[string]string{"logrotate.conf": "rotate 4\n/var/log/a {\n  rotate 1\n}\n"}
	modprobe := map[string]string{"modprobe.d/m.conf": "options snd_hda_intel a=1\noptions other b=2\n"}
	limits := map[string]string{"limits.conf": "* soft nofile 1\n@a soft nofile 2\n"}
	runReads(t, []readCase{
		{"sshd", sshd, Spec{Where: where("sshd_config"), Key: "x", Block: str("MATCH User   git")}, "sshd_config:5=2 [Match User git]"},
		{"sshd outside", sshd, Spec{Where: where("sshd_config"), Key: "x", Block: str("")}, "sshd_config:1=0"},
		{"logrotate", logrotate, Spec{Where: where("logrotate.conf"), Key: "rotate", Block: str(" /var/log/a ")}, "logrotate.conf:3=1 [/var/log/a]"},
		{"modprobe", modprobe, Spec{Where: where("modprobe.d/m.conf"), Key: "options", Block: str("snd-hda-intel")}, "modprobe.d/m.conf:1=a=1 [snd_hda_intel]"},
		{"limits", limits, Spec{Where: where("limits.conf"), Key: "nofile", Block: str("@a")}, "limits.conf:2=2 [@a soft]"},
	})
}
