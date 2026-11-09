package etc

import (
	"reflect"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Place writes a spec as the dialect compares it, so two spellings of one
// place are one, and a spec whose dialect errata does not read keeps its
// place as written. Planted break: Place leaving the key unfolded.
func TestPlace(t *testing.T) {
	a := Spec{Where: where("./ssh/sshd_config"), Key: "Port", Block: str("match  user git"), Match: nil}
	b := Spec{Where: core.Where{File: "ssh/sshd_config", MaxBytes: 9}, Dialect: "sshd", Key: "port", Block: str("Match user git")}
	if pa, pb := Place(a), Place(b); !reflect.DeepEqual(pa, pb) {
		t.Errorf("one place reads as two: %+v and %+v", pa, pb)
	}
	m := Spec{Where: where("ssh_config"), Key: "user", Match: map[string]string{"Host": "a"}}
	if got := Place(m).Match; !reflect.DeepEqual(got, map[string]string{"host": "a"}) {
		t.Errorf("match places as %v", got)
	}
	if got := Place(Spec{Where: where("x.conf"), Key: "K"}); got.Key != "K" || got.Dialect != "" {
		t.Errorf("a file no dialect reads places as %+v", got)
	}
}

// Resolve writes out the dialect and max_bytes and shares nothing with the
// spec. Planted break: Resolve not cloning match:.
func TestResolve(t *testing.T) {
	r := Resolve(Spec{Where: where("sysctl.conf"), Key: "a", Block: str("b"), Match: map[string]string{"user": "u"}})
	if r.Dialect != "sysctl" || r.MaxBytes != core.DefaultMaxBytes {
		t.Errorf("Resolve gives %+v", r)
	}
	given := Spec{Where: where("sshd_config"), Block: str("b"), Match: map[string]string{"user": "u"}}
	r = Resolve(given)
	*r.Block, r.Match["user"] = "c", "v"
	if *given.Block != "b" || given.Match["user"] != "u" {
		t.Errorf("Resolve shares with the spec: %+v", given)
	}
	if got := Resolve(Spec{Where: where("x.conf")}); got.Dialect != "" {
		t.Errorf("a file no dialect reads resolves as %+v", got)
	}
}

// Describe labels the dialect and the file, and marks not literal sshd and
// ssh, which expand tokens, and a template, of file: or of files:. Planted
// break: templated reading file: alone.
func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want check.Description
	}{
		{one("a/sshd_config", "port"), check.Description{Label: "etc, sshd dialect on sshd_config", NotLiteral: true}},
		{one("sysctl.conf", "a"), check.Description{Label: "etc, sysctl dialect on sysctl.conf"}},
		{one("resolv.conf.j2", "search"), check.Description{Label: "etc, resolv dialect on resolv.conf.j2", NotLiteral: true}},
		{Spec{Where: core.Where{Files: []string{"sysctl.d/a.conf", "sysctl.d/b.conf.j2"}}, Key: "a"}, check.Description{Label: "etc, sysctl dialect on a.conf", NotLiteral: true}},
		{one("x.conf", "a"), check.Description{}},
	} {
		if got := Describe(c.spec); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: %+v, want %+v", c.spec, got, c.want)
		}
	}
}

// Odd lines each dialect meets: a \ escapes a # in sshd, a negated pattern
// or address excludes a connection, and a set of sysctl drop-ins without
// sysctl.conf keeps its order, and a template masks the file of the name
// it renders. Planted breaks: patternList taking a negated match as a
// match; FirstOfEachName taking a template's name with its suffix.
func TestOddLines(t *testing.T) {
	runReads(t, []readCase{
		{"escaped #", map[string]string{"sshd_config": "Banner a\\ #b #c\n"}, one("sshd_config", "banner"), "sshd_config:1=a\\ #b"},
		{"negated user", map[string]string{"sshd_config": "X 0\nMatch User *,!root\n X 1\n"}, matching("sshd_config", "x", map[string]string{"user": "root"}), "sshd_config:1=0"},
		{"negated address", map[string]string{"sshd_config": "X 0\nMatch Address 10.0.0.0/8,!10.0.0.9\n X 1\n"}, matching("sshd_config", "x", map[string]string{"address": "10.0.0.9"}), "sshd_config:1=0"},
		{"sysctl without sysctl.conf", map[string]string{"d/b.conf": "a=2\n", "d/a.conf": "a=1\n"},
			Spec{Where: core.Where{Files: []string{"d/*.conf"}}, Dialect: "sysctl", Key: "a"}, "d/a.conf:1=1 | d/b.conf:1=2"},
		{"a template masks the file it renders", map[string]string{"etc/sysctl.d/60-x.conf.j2": "a=1\n", "usr/lib/sysctl.d/60-x.conf": "a=2\n"},
			Spec{Where: core.Where{Files: []string{"usr/lib/sysctl.d/*", "etc/sysctl.d/*"}}, Key: "a"}, "etc/sysctl.d/60-x.conf.j2:1=1"},
	})
}
