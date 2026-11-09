package table

import (
	"reflect"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Place writes a spec as the dialect compares it, so two spellings of one
// place are one, and a spec whose dialect errata does not read keeps its
// place as written. Planted break: Place leaving a host name unfolded.
func TestPlace(t *testing.T) {
	a := Spec{Where: where("./etc/hosts"), Column: "ip_address", Row: map[string]string{"names": "Web1"}}
	b := Spec{Where: core.Where{File: "etc/hosts", MaxBytes: 9}, Dialect: "hosts", Column: "ip_address", Row: map[string]string{"names": "web1"}}
	if pa, pb := Place(a), Place(b); !reflect.DeepEqual(pa, pb) {
		t.Errorf("one place reads as two: %+v and %+v", pa, pb)
	}
	if got := Place(col("fstab", "spec", "file", "/SRV")).Row["file"]; got != "/SRV" {
		t.Errorf("a path places as %q", got)
	}
	if got := Place(Spec{Where: where("x.conf"), Column: "c"}); got.Dialect != "" {
		t.Errorf("a file no dialect reads places as %+v", got)
	}
}

// Resolve writes out the dialect and max_bytes and shares nothing with the
// spec. Planted break: Resolve not cloning row:.
func TestResolve(t *testing.T) {
	given := col("etc/group", "gid", "group_name", "wheel")
	r := Resolve(given)
	if r.Dialect != "group" || r.MaxBytes != core.DefaultMaxBytes {
		t.Errorf("Resolve gives %+v", r)
	}
	r.Row["group_name"] = "x"
	if given.Row["group_name"] != "wheel" {
		t.Errorf("Resolve shares with the spec: %+v", given)
	}
	if got := Resolve(Spec{Where: where("x.conf")}); got.Dialect != "" {
		t.Errorf("a file no dialect reads resolves as %+v", got)
	}
}

// Describe labels the dialect and the file, and marks not literal a
// template, of file: or of files:. Planted break: templated reading file:
// alone.
func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want check.Description
	}{
		{col("etc/fstab", "spec"), check.Description{Label: "table, fstab dialect on fstab"}},
		{col("roles/x/templates/hosts.j2", "names"), check.Description{Label: "table, hosts dialect on hosts.j2", NotLiteral: true}},
		{Spec{Where: core.Where{Files: []string{"a/passwd", "b/passwd.j2"}}, Column: "uid"}, check.Description{Label: "table, passwd dialect on passwd", NotLiteral: true}},
		{Spec{Where: where("exports.d/nfs.exports")}, check.Description{Label: "table, exports dialect on nfs.exports"}},
		{col("x.conf", "a"), check.Description{}},
		{Spec{}, check.Description{}},
	} {
		if got := Describe(c.spec); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: %+v, want %+v", c.spec, got, c.want)
		}
	}
}

// Each dialect is chosen by the names its tool reads, once a template
// suffix is taken off, and by no other. Planted break: exports.d taking a
// file of any suffix.
func TestDialectFor(t *testing.T) {
	for file, want := range map[string]string{
		"etc/fstab": "fstab", "fstab.j2": "fstab", "etc/passwd": "passwd", "group.tmpl": "group",
		"etc/hosts": "hosts", "etc/exports": "exports", "etc/exports.d/a.exports": "exports",
		"etc/crypttab": "crypttab", "etc/exports.d/a.conf": "", "a.exports": "", "etc/exports.d/.exports": "",
		"etc/shadow": "", "etc/fstab.d": "",
	} {
		got := ""
		if d := dialectFor(file); d != nil {
			got = d.name
		}
		if got != want {
			t.Errorf("%s: dialect %q, want %q", file, got, want)
		}
	}
}
