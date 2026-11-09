package table

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// show lists values as file:line=text, with a not literal mark.
func show(vs []check.Value) string {
	var parts []string
	for _, v := range vs {
		s := fmt.Sprintf("%s:%d=%s", v.At.File, v.At.Line, v.Text)
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

func where(file string) core.Where { return core.Where{File: file} }

// col is the spec of column of file, with row's criteria as pairs.
func col(file, column string, row ...string) Spec {
	s := Spec{Where: where(file), Column: column}
	if len(row) > 0 {
		s.Row = map[string]string{}
		for i := 0; i+1 < len(row); i += 2 {
			s.Row[row[i]] = row[i+1]
		}
	}
	return s
}

const fstab = `# /etc/fstab
UUID=1234-abcd  /       ext4  defaults,noatime  0  1
LABEL=data	/srv	xfs	rw,nodev,context="a,b"	0 2
  # tmp
tmpfs /tmp tmpfs mode=1777
/dev/sdb1 /mnt
/dev/sdc1 /data ext4
/dev/sdd1 /mnt/my\040disk ext4 ro\054user 0 0
/dev/sde1 /bad ext4 rw 0 x
/dev/sdf1 /big ext4 rw 0 99999999999
/dev/sdh1 /huge ext4 rw 0 99999999999999999999
/dev/sdg1 /lead ext4 rw 0 010
/dev/sdi1 /tail ext4 rw 1x 2
/dev/sdj1 /over ext4 rw 99999999999999999999 1
/dev/sdk1 /ptail ext4 rw 0 3y
/dev/sdl1 /edge ext4 rw 0 2147483648 more
/dev/sdo1 /hex ext4 rw 0 0x1
`

// Each dialect splits a line as its tool does, and names its columns as
// its man page does: fstab's options are items, a UUID= is the file's text,
// a row of three fields mounts with no option, a row that leaves out freq
// and passno has their default 0, each \ooo is the byte it writes, as
// libmount decodes it before it splits the options, and a row of two
// fields is passed over. freq and passno are the numbers next_s32 reads:
// leading zeros dropped, past 64 bits the clamped long's low 32 bits; a
// row whose freq or passno has no digit or anything after its digits
// (3y, 0x1), or whose number past 64 bits has a field after it, is passed
// over. Planted breaks: fstab's default for passno dropped; fstab's
// required fields restored to 4; the octal escapes kept; a passno past 32
// bits refused; text after a passno's digits read.
func TestFstab(t *testing.T) {
	files := map[string]string{"etc/fstab": fstab}
	runReads(t, []readCase{
		{"mount points", files, col("etc/fstab", "file"), "etc/fstab:2=/ | etc/fstab:3=/srv | etc/fstab:5=/tmp | etc/fstab:7=/data | etc/fstab:8=/mnt/my disk | etc/fstab:10=/big | etc/fstab:11=/huge | etc/fstab:12=/lead | etc/fstab:16=/edge"},
		{"spec by mount point", files, col("etc/fstab", "spec", "file", "/"), "etc/fstab:2=UUID=1234-abcd"},
		{"options are items, quotes keep a comma", files, col("etc/fstab", "mntops", "file", "/srv"), "etc/fstab:3=rw | etc/fstab:3=nodev | etc/fstab:3=context=\"a,b\""},
		{"a row by an option", files, col("etc/fstab", "file", "mntops", "noatime"), "etc/fstab:2=/"},
		{"passno's default", files, col("etc/fstab", "passno"), "etc/fstab:2=1 | etc/fstab:3=2 | etc/fstab:5=0 | etc/fstab:7=0 | etc/fstab:8=0 | etc/fstab:10=1215752191 | etc/fstab:11=-1 | etc/fstab:12=10 | etc/fstab:16=-2147483648"},
		{"freq's default", files, col("etc/fstab", "freq", "file", "/tmp"), "etc/fstab:5=0"},
		{"two criteria", files, col("etc/fstab", "vfstype", "file", "/srv", "spec", "LABEL=data"), "etc/fstab:3=xfs"},
		{"two criteria, one not met", files, col("etc/fstab", "vfstype", "file", "/srv", "spec", "/dev/sdb1"), ""},
		{"three fields, no option", files, col("etc/fstab", "mntops", "file", "/data"), ""},
		{"three fields, the type", files, col("etc/fstab", "vfstype", "file", "/data"), "etc/fstab:7=ext4"},
		{"an escaped space", files, col("etc/fstab", "spec", "file", "/mnt/my disk"), "etc/fstab:8=/dev/sdd1"},
		{"an escaped comma splits", files, col("etc/fstab", "mntops", "spec", "/dev/sdd1"), "etc/fstab:8=ro | etc/fstab:8=user"},
	})
}

// passwd and group split at :, the last column the rest of the line; a
// comment starts a line, a # elsewhere is text, and a group's members are
// items, none for a group without members, the white space before each
// skipped and a double quote no quote. glibc reads a passwd row of four
// fields, gecos, directory and shell empty, and a group row of three, with
// no member; a row of fewer is passed over. A uid or gid is the number
// glibc's strtoul reads, white space, a sign and leading zeros allowed, so
// -0 is 0; a row whose id has no digit, anything after its digits, or a
// value past 32 bits, -1 included, is passed over. A row of nss compat's,
// whose name starts with + or -, reads an empty id as 0, in passwd when a
// : follows it, in group always. Planted breaks: SplitN reading every :
// so the shell loses what follows one; passwd's required fields restored
// to 7; group's to 4; the white space before a member kept; an id
// returned as written; the compat rule dropped.
func TestPasswdAndGroup(t *testing.T) {
	files := map[string]string{
		"etc/passwd": "root:x:0:0:root:/root:/bin/bash\n # c\ngit:x:998:998:Git #1:/home/git:/bin/sh:x\nshort:x:1\n+@admins::::::\ntoor:x:0:0\nhalf:x:5:5:Half\na:x:00:0\nb:x:+0:0\nc:x: 0:0\nd:x:-0:0\ne:x:abc:0\nf:x:0x1:0\ng:x:99999999999:0\nh:x::0\ni:x:0 :0\nj:x:5:-1\nk:x:4294967295:007\n+:x::\n-v::::::\n",
		"etc/group":  "wheel:x:10:root, alice,\t\"bob\nusers:x:100:a,,b\ndocker:x:999:\nstaff:x:50\nshort:x\nneg:x:-0:x\nbad:x:1x:y\n-g:x::\n+:x:\n",
	}
	runReads(t, []readCase{
		{"names", files, col("etc/passwd", "name"), "etc/passwd:1=root | etc/passwd:3=git | etc/passwd:5=+@admins | etc/passwd:6=toor | etc/passwd:7=half | etc/passwd:8=a | etc/passwd:9=b | etc/passwd:10=c | etc/passwd:11=d | etc/passwd:18=k | etc/passwd:20=-v"},
		{"ids as glibc reads them", files, col("etc/passwd", "gid", "name", "k"), "etc/passwd:18=7"},
		{"a # inside a field", files, col("etc/passwd", "gecos", "name", "git"), "etc/passwd:3=Git #1"},
		{"the shell is the rest", files, col("etc/passwd", "shell", "name", "git"), "etc/passwd:3=/bin/sh:x"},
		{"uid", files, col("etc/passwd", "uid", "name", "root"), "etc/passwd:1=0"},
		{"every uid 0", files, col("etc/passwd", "name", "uid", "0"), "etc/passwd:1=root | etc/passwd:5=+@admins | etc/passwd:6=toor | etc/passwd:8=a | etc/passwd:9=b | etc/passwd:10=c | etc/passwd:11=d | etc/passwd:20=-v"},
		{"four fields, an empty shell", files, col("etc/passwd", "shell", "name", "toor"), "etc/passwd:6="},
		{"five fields, an empty directory", files, col("etc/passwd", "directory", "name", "half"), "etc/passwd:7="},
		{"members", files, col("etc/group", "user_list", "group_name", "wheel"), "etc/group:1=root | etc/group:1=alice | etc/group:1=\"bob"},
		{"a member after a space", files, col("etc/group", "group_name", "user_list", "alice"), "etc/group:1=wheel"},
		{"no member", files, col("etc/group", "user_list", "group_name", "docker"), ""},
		{"empty items left out", files, col("etc/group", "user_list", "group_name", "users"), "etc/group:2=a | etc/group:2=b"},
		{"three fields", files, col("etc/group", "gid", "group_name", "staff"), "etc/group:4=50"},
		{"groups", files, col("etc/group", "group_name"), "etc/group:1=wheel | etc/group:2=users | etc/group:3=docker | etc/group:4=staff | etc/group:6=neg | etc/group:8=-g | etc/group:9=+"},
		{"a compat gid", files, col("etc/group", "gid", "group_name", "+"), "etc/group:9=0"},
		{"a gid as glibc reads it", files, col("etc/group", "gid", "group_name", "neg"), "etc/group:6=0"},
	})
}

// hosts reads a # anywhere as a comment, the aliases as items, and names
// as the canonical name and the aliases, compared in any case, ASCII only;
// a row whose address inet_pton does not read is passed over, a zone
// included. Planted breaks: names compared as written; a bad address
// kept.
func TestHosts(t *testing.T) {
	files := map[string]string{"etc/hosts": "127.0.0.1 localhost # loop\n10.0.0.5\tweb1.lan  Web1 www # app\n10.0.0.6 db1\n#10.0.0.7 web1\n10.0.0.8\nweb1 web1\n010.0.0.9 web1\nfe80::1%eth0 web1\n::1 web1\n"}
	runReads(t, []readCase{
		{"address by a name", files, col("etc/hosts", "ip_address", "names", "web1"), "etc/hosts:2=10.0.0.5 | etc/hosts:9=::1"},
		{"address by the canonical name, any case", files, col("etc/hosts", "ip_address", "canonical_hostname", "WEB1.LAN"), "etc/hosts:2=10.0.0.5"},
		{"aliases", files, col("etc/hosts", "aliases", "ip_address", "10.0.0.5"), "etc/hosts:2=Web1 | etc/hosts:2=www"},
		{"names", files, col("etc/hosts", "names", "ip_address", "10.0.0.5"), "etc/hosts:2=web1.lan | etc/hosts:2=Web1 | etc/hosts:2=www"},
		{"no alias", files, col("etc/hosts", "aliases", "ip_address", "10.0.0.6"), ""},
		{"ASCII only", files, col("etc/hosts", "ip_address", "names", "Keb1"), ""},
	})
}

// exports reads one row for each client, with the defaults each - word
// where a client would start adds before the client's own options, the word
// after it a client whatever it starts with and the client * when the line
// ends there, a quote left open at a join going on as one space, a \ right
// before the line end joining lines, quotes dropped anywhere in a word,
// \ooo decoded, a # that starts a word starting a comment, a netgroup as
// written, and the client * for (options) alone and for a path with no
// client. Planted breaks: the defaults left out of a client's options; a
// later - word replacing the defaults; the client of (options) alone left
// empty; no row for a - word at the line end; a quote at a join not
// carried.
func TestExports(t *testing.T) {
	files := map[string]string{
		"etc/exports":             "# c\n/srv/nfs -sync web1(rw,no_subtree_check) \\\n  @trusted(ro)\n\"/srv/my share\" (rw)\n/srv/open\n/srv/two -ro a -sync b(rw)\n/srv/a#b host(rw) #c\n/srv/sp host(rw) \\ \nnext(ro)\n/srv/\"q d\"\\040x \"h\"(\"rw\")\n/srv/tail a(rw) -ro\n/srv/dd -ro -sync a\n\"/srv/my \\\n   share\" host(rw)\n\"/srv/b \\\n   \\\nx\" c\n",
		"etc/exports.d/a.exports": "/data 10.0.0.0/24(rw)\n",
	}
	runReads(t, []readCase{
		{"clients", files, col("etc/exports", "client", "path", "/srv/nfs"), "etc/exports:2=web1 | etc/exports:3=@trusted"},
		{"options with defaults", files, col("etc/exports", "options", "path", "/srv/nfs", "client", "web1"), "etc/exports:2=sync | etc/exports:2=rw | etc/exports:2=no_subtree_check"},
		{"a joined line", files, col("etc/exports", "options", "client", "@trusted"), "etc/exports:2=sync | etc/exports:3=ro"},
		{"a quoted path", files, col("etc/exports", "options", "path", "/srv/my share"), "etc/exports:4=rw"},
		{"every host", files, col("etc/exports", "path", "client", "*"), "etc/exports:4=/srv/my share | etc/exports:5=/srv/open | etc/exports:9=next(ro) | etc/exports:11=/srv/tail"},
		{"a - word at the line end", files, col("etc/exports", "options", "path", "/srv/tail", "client", "*"), "etc/exports:11=ro"},
		{"a - word after a - word is a client", files, col("etc/exports", "client", "path", "/srv/dd"), "etc/exports:12=-sync | etc/exports:12=a"},
		{"its options", files, col("etc/exports", "options", "path", "/srv/dd", "client", "a"), "etc/exports:12=ro"},
		{"a quote open across a join", files, col("etc/exports", "client", "path", "/srv/my  share"), "etc/exports:14=host"},
		{"a join of a blank line inside a quote", files, col("etc/exports", "path", "client", "c"), "etc/exports:15=/srv/b   x"},
		{"the path's line", files, col("etc/exports", "path", "client", "host"), "etc/exports:7=/srv/a#b | etc/exports:8=/srv/sp | etc/exports:13=/srv/my  share"},
		{"defaults add up", files, col("etc/exports", "options", "path", "/srv/two", "client", "b"), "etc/exports:6=ro | etc/exports:6=sync | etc/exports:6=rw"},
		{"a # inside a word", files, col("etc/exports", "client", "path", "/srv/a#b"), "etc/exports:7=host"},
		{"a \\ then a space joins nothing", files, col("etc/exports", "client", "path", "/srv/sp"), "etc/exports:8=host | etc/exports:8=\\"},
		{"quotes and an escape", files, col("etc/exports", "options", "path", "/srv/q d x", "client", "h"), "etc/exports:10=rw"},
		{"exports.d", files, col("etc/exports.d/a.exports", "client"), "etc/exports.d/a.exports:1=10.0.0.0/24"},
	})
}

// crypttab reads the key file as none and no option when a row leaves
// them out, a key file of - as none, and options of - or none as no
// option, as systemd's mangle_none reads both. Planted breaks: the key
// file's default dropped; - kept as written; options of - kept.
func TestCrypttab(t *testing.T) {
	files := map[string]string{"etc/crypttab": "# c\ncryptroot UUID=abc none luks,discard\nswap /dev/sdb2\nhome /dev/sdc - luks\nvar /dev/sdd none -\ntmp /dev/sde - none\n"}
	runReads(t, []readCase{
		{"options", files, col("etc/crypttab", "options", "target", "cryptroot"), "etc/crypttab:2=luks | etc/crypttab:2=discard"},
		{"key file's default", files, col("etc/crypttab", "key_file", "target", "swap"), "etc/crypttab:3=none"},
		{"- is none", files, col("etc/crypttab", "target", "key_file", "none"), "etc/crypttab:2=cryptroot | etc/crypttab:3=swap | etc/crypttab:4=home | etc/crypttab:5=var | etc/crypttab:6=tmp"},
		{"options of - and none", files, col("etc/crypttab", "target", "options", ""), ""},
		{"no option of -", files, col("etc/crypttab", "options", "target", "var"), ""},
		{"no option of none", files, col("etc/crypttab", "options", "target", "tmp"), ""},
		{"no option", files, col("etc/crypttab", "options", "target", "swap"), ""},
		{"source", files, col("etc/crypttab", "source"), "etc/crypttab:2=UUID=abc | etc/crypttab:3=/dev/sdb2 | etc/crypttab:4=/dev/sdc | etc/crypttab:5=/dev/sdd | etc/crypttab:6=/dev/sde"},
	})
}

// A template is read as the table it renders: a cell of a line with the
// engine's marks or inside a block is not literal, and a row whose
// criterion's cell the engine writes is selected perhaps, its values not
// literal; a literal line in a block is selected only by its own text.
// Planted break: varies taken as computed, so the /home row in a block
// is selected perhaps.
func TestTemplate(t *testing.T) {
	files := map[string]string{"roles/x/templates/fstab.j2": "/dev/a /srv ext4 defaults\n{{ dev }} {{ mnt }} ext4 noatime\n{% if x %}\n/dev/b /home ext4 rw\n/dev/c /srv xfs ro\n{% endif %}\n"}
	runReads(t, []readCase{
		{"options of /srv", files, col("roles/x/templates/fstab.j2", "mntops", "file", "/srv"),
			"roles/x/templates/fstab.j2:1=defaults | roles/x/templates/fstab.j2:2=noatime !lit | roles/x/templates/fstab.j2:5=ro !lit"},
	})
}

// A set reads each file as one table in the dialect its first entry
// chooses, as files: lists them.
func TestSet(t *testing.T) {
	files := map[string]string{"hosts/a/fstab": "/dev/a /srv ext4 rw\n", "hosts/b/fstab": "/dev/b /srv ext4 ro\n"}
	runReads(t, []readCase{
		{"two hosts", files, Spec{Where: core.Where{Files: []string{"hosts/*/fstab"}}, Column: "mntops", Row: map[string]string{"file": "/srv"}}, "hosts/a/fstab:1=rw | hosts/b/fstab:1=ro"},
	})
}

// Lines end at a LF, a CRLF and a lone CR alike.
func TestLineEnds(t *testing.T) {
	runReads(t, []readCase{
		{"lone CR", map[string]string{"hosts": "# a\r10.0.0.1 a\r\n10.0.0.2 b\r"}, col("hosts", "ip_address"), "hosts:2=10.0.0.1 | hosts:3=10.0.0.2"},
	})
}

// Build refuses a spec errata cannot read, naming why. Planted break:
// checkRow taking an empty row as none.
func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{col("fstab", ""), "table: column is required; write one of the fstab columns: spec, file, vfstype, mntops, freq, passno"},
		{col("fstab", "mount"), `table: column names "mount", which is no column of fstab; write one of spec, file, vfstype, mntops, freq, passno`},
		{col("fstab", "spec", "dir", "/"), `table: row names "dir", which is no column of fstab`},
		{Spec{Where: where("fstab"), Column: "spec", Row: map[string]string{}}, "table: row names no column"},
		{col("tab", "spec"), `table: no dialect reads a file named "tab"; write dialect: one of fstab, passwd, group, hosts, exports, crypttab`},
		{Spec{Where: where("fstab"), Dialect: "shadow", Column: "spec"}, `table: dialect "shadow" is not one errata reads`},
		{Spec{Where: core.Where{File: "fstab", Files: []string{"fstab"}}, Column: "spec"}, "table: file and files are both set"},
		{col("/etc/fstab", "spec"), "outside the repository"},
	} {
		_, err := Build(c.spec)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want %q", c.spec, err, c.want)
		}
	}
}

// The values are held to the value limit and to max_bytes, which an
// exports default written once for many clients passes while the file
// does not. Planted breaks: either bound's check removed.
func TestBounds(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{"etc/exports": "/p -" + strings.Repeat("ro,", 20) + " a b c\n", "etc/hosts": "10.0.0.1 a\n10.0.0.2 b\n10.0.0.3 c\n"})
	x := extractor{Source: core.Source{File: "etc/exports", Limit: 100}, d: exportsDialect, col: 2}
	_, err := x.Extract(t.Context(), root)
	extracttest.WantError(t, err, "etc/exports: the values the extractor selects hold more than 100 bytes in all")
	y := extractor{Source: core.Source{File: "etc/hosts", Limit: 100}, d: hostsDialect, valueLimit: 2}
	_, err = y.Extract(t.Context(), root)
	extracttest.WantError(t, err, "etc/hosts: the extractor selects more than")
}

// A read past its work budget is refused, naming the file. Planted
// break: the charge for each exports row's cells removed.
func TestWork(t *testing.T) {
	data := "/p -" + strings.Repeat("ro,", 4000) + " " + strings.Repeat("c ", 4000) + "\n"
	root := extracttest.Tracked(t, map[string]string{"exports": data})
	x := extractor{Source: core.Source{File: "exports", Limit: core.DefaultMaxBytes}, d: exportsDialect, col: 0, row: []criterion{{col: 1, want: "z"}}}
	_, err := x.Extract(t.Context(), root)
	extracttest.WantError(t, err, "exports: reading the table and matching its rows take more than")
}

// In a template a field runs on through an expression of the engine, whose
// spaces and colons split nothing, and a tag that no end closes splits as
// any text; passwd's last column still takes the rest of the line. Planted
// breaks: splitSpaces' first field taken from offset 1, splitColons
// splitting once more than its columns.
func TestTemplateSplits(t *testing.T) {
	q := func(f [][]byte) string { return fmt.Sprintf("%q", f) }
	for _, c := range []struct{ got, want string }{
		{q(splitSpaces([]byte("ab {{ x y }}  c{%d %}e {{ f"), true)), `["ab" "{{ x y }}" "c{%d %}e" "{{" "f"]`},
		{q(splitSpaces([]byte("ab {{ x y }}"), false)), `["ab" "{{" "x" "y" "}}"]`},
		{q(splitColons([]byte("a:{{ x:y }}:c:d"), 3, true)), `["a" "{{ x:y }}" "c:d"]`},
		{q(splitColons([]byte("a:b"), 5, true)), `["a" "b"]`},
		{q(splitColons([]byte("a::b:"), 4, true)), `["a" "" "b" ""]`},
		{q(splitSpaces([]byte("<%= a %>/x ${b c} %{ if d }"), true)), `["<%= a %>/x" "${b c}" "%{ if d }"]`},
	} {
		if c.got != c.want {
			t.Errorf("got %s, want %s", c.got, c.want)
		}
	}
}

// A dialect's read charges its meter a line's work and its bytes for each
// line, and a cell's work for each cell a row holds, the defaults an
// exports client copies included, a later line charged its own bytes.
// Planted breaks: each charge's sum written as a difference.
func TestCharges(t *testing.T) {
	for _, c := range []struct {
		d    *dialect
		data string
		want int64
	}{
		{fstabDialect, "a b c d\n", core.LineWork + 8 + cellWork*6},
		{fstabDialect, "#\na b c d\n", 2*core.LineWork + 10 + cellWork*6},
		{exportsDialect, "/p -ro,rw c(sync)\n", core.LineWork + 18 + cellWork*5},
	} {
		m := core.NewMeter(len(c.data))
		if err := c.d.read([]byte(c.data), m, &marker{}, func(row) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if m.Spent != c.want {
			t.Errorf("%s: spent %d, want %d", c.d.name, m.Spent, c.want)
		}
	}
}

// The work budget counts the row's values beside the file's bytes, as
// every reader's counts its spec's. Planted break: the spec's bytes taken
// off the budget.
func TestBudgetCountsTheSpec(t *testing.T) {
	data := "/p -" + strings.Repeat("ro,", 100) + " " + strings.Repeat("c ", 1000) + "\n"
	root := extracttest.Tracked(t, map[string]string{"exports": data})
	x := extractor{Source: core.Source{File: "exports", Limit: core.DefaultMaxBytes}, d: exportsDialect, row: []criterion{{col: 1, want: strings.Repeat("z", 10_000)}}}
	if _, err := x.Extract(t.Context(), root); err != nil {
		t.Fatal(err)
	}
}

// The rest of each step's edges: max_bytes holds values of exactly its
// bytes, a host name is folded at A and at Z, an exports comment at the
// start of a line and a quoted empty path, and a word a quote closes ends
// there. Planted breaks: the byte bound refusing its own limit; folding
// from B.
func TestEdges(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{"crypttab": "a b\n"})
	x := extractor{Source: core.Source{File: "crypttab", Limit: 4}, d: crypttabDialect, col: 2}
	if vs, err := x.Extract(t.Context(), root); err != nil || len(vs) != 1 {
		t.Errorf("values of max_bytes: %v, %v", vs, err)
	}
	runReads(t, []readCase{
		{"fold at A and Z", map[string]string{"hosts": "::1 A\n::2 Z\n::3 @\n::4 [\n"}, col("hosts", "ip_address", "names", "a"), "hosts:1=::1"},
		{"fold at Z", map[string]string{"hosts": "::1 A\n::2 Z\n"}, col("hosts", "ip_address", "names", "z"), "hosts:2=::2"},
		{"exports paths", map[string]string{"exports": "#x y\n\"a\"b c\n"}, col("exports", "path"), "exports:2=ab"},
		{"exports clients after a quote", map[string]string{"exports": "\"a\"b c\n"}, col("exports", "client"), "exports:1=c"},
		{"an escape past 255 is kept", map[string]string{"exports": "/a\\777 c\n"}, col("exports", "path"), "exports:1=/a\\777"},
		{"an escape at 255", map[string]string{"exports": "/a\\377 c\n"}, col("exports", "path"), "exports:1=/a\xff"},
		{"a member keeps the white space after it", map[string]string{"group": "g:x:1: a\t,b \n"}, col("group", "user_list"), "group:1=a\t | group:1=b "},
		{"a template's numbers stay as written", map[string]string{"fstab.j2": "/a /b c d {{ f }} x\n"}, col("fstab.j2", "passno"), "fstab.j2:1=x !lit"},
		{"a quote alone before a join", map[string]string{"exports": "\"\\\nbd\" c\n"}, col("exports", "path"), "exports:1= bd"},
		{"a word that is one escape", map[string]string{"exports": "\\101 c\n"}, col("exports", "path"), "exports:1=A"},
		{"a field that starts with an escape", map[string]string{"fstab": "\\101x /b c\n"}, col("fstab", "spec"), "fstab:1=Ax"},
	})
	if got := Place(col("hosts", "ip_address", "ip_address", "FE80::A")).Row["ip_address"]; got != "FE80::A" {
		t.Errorf("an address places as %q", got)
	}
	if got := asciiLower("@AZ["); got != "@az[" {
		t.Errorf("asciiLower gives %q", got)
	}
}

// nfs-utils stops reading a file at its first error, and errata refuses
// the file there, naming the line: an option list without its ) at the
// end of the word, an empty path, an option nfs-utils does not know, in a
// client's list or a - word, and a quote left open at a line end or at
// the end of the file. A template's word the engine writes is not
// refused. Planted breaks: each check removed.
func TestExportsRefuses(t *testing.T) {
	for _, c := range []struct{ data, want string }{
		{"/a b(rw\n", `exports: line 1: the client "b(rw" has no ) at the end of its options`},
		{"/a\n/b c(rw)x\n", `exports: line 2: the client "c(rw)x" has no ) at the end of its options`},
		{"\"\" b\n", "exports: line 1: the path is empty"},
		{"/a b(rw,bogus)\n", `exports: line 1: the option "bogus" is none nfs-utils knows`},
		{"/a -bogus b\n", `exports: line 1: the option "bogus" is none nfs-utils knows`},
		{"/a b(mapping=x)\n", `exports: line 1: the option "mapping=x" is none nfs-utils knows`},
		{"/a b(fsloc=x)\n", `exports: line 1: the option "fsloc=x" is none nfs-utils knows`},
		{"/a b(rw,,sync)\n", `exports: line 1: the option "" is none nfs-utils knows`},
		{"/a b(,rw)\n", `exports: line 1: the option "" is none nfs-utils knows`},
		{"/a b(anonuid=x)\n", `exports: line 1: the option "anonuid=x" has a value nfs-utils does not read`},
		{"/a b(anongid=)\n", `exports: line 1: the option "anongid=" has a value nfs-utils does not read`},
		{"/a b(sec=krb6)\n", `exports: line 1: the option "sec=krb6" has a value nfs-utils does not read`},
		{"/a b(fsid=08)\n", `exports: line 1: the option "fsid=08" has a value nfs-utils does not read`},
		{"/a b(fsid=0x)\n", `exports: line 1: the option "fsid=0x" has a value nfs-utils does not read`},
		{"/a b(xprtsec=ssl)\n", `exports: line 1: the option "xprtsec=ssl" has a value nfs-utils does not read`},
		{"/a b(reexport=x)\n", `exports: line 1: the option "reexport=x" has a value nfs-utils does not read`},
		{"/a b(fsid=)\n", `exports: line 1: the option "fsid=" has a value nfs-utils does not read`},
		{"/a b(fsid=+)\n", `exports: line 1: the option "fsid=+" has a value nfs-utils does not read`},
		{"\"/a b\n", "exports: line 1: a double quote is left open at the line end"},
		{"/a b\n\"/c \\", "exports: line 2: a double quote is left open at the end of the file"},
	} {
		root := extracttest.Tracked(t, map[string]string{"exports": c.data})
		x, err := Build(col("exports", "path"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = x.Extract(t.Context(), root)
		extracttest.WantError(t, err, c.want)
	}
	runReads(t, []readCase{
		{"a template's words", map[string]string{"exports.j2": "/a {{ host }}({{ opts }}\n\"{{ p }} c\n"}, col("exports.j2", "path"), strings.Repeat("exports.j2:1=/a !lit | ", 5) + "exports.j2:2={{ p }} c !lit"},
		{"options with values", map[string]string{"exports": "/a -fsid=0,sec=krb5p b(anonuid=65534,mp,no_root_squash)\n"}, col("exports", "options"), "exports:1=fsid=0 | exports:1=sec=krb5p | exports:1=anonuid=65534 | exports:1=mp | exports:1=no_root_squash"},
		{"the rarer options", map[string]string{"exports": "/a b(sign_fh,nocrossmnt,reexport=auto-fsidnum,squash_uids=0-15,20-25,anongid=\\040-2,sec=krb5:sys,xprtsec=tls:mtls,)\n"}, col("exports", "options"),
			"exports:1=sign_fh | exports:1=nocrossmnt | exports:1=reexport=auto-fsidnum | exports:1=squash_uids=0-15,20-25 | exports:1=anongid= -2 | exports:1=sec=krb5:sys | exports:1=xprtsec=tls:mtls"},
		{"an fsid of each form", map[string]string{"exports": "/a b(fsid=root) c(fsid=0x1F) g(fsid=0XA) h(squash_uids=1,) d(fsid=017) e(fsid=+12) f(fsid=6ba7b810-9dad-11d1-80b4-00c04fd430c8)\n"}, col("exports", "client"), "exports:1=b | exports:1=c | exports:1=g | exports:1=h | exports:1=d | exports:1=e | exports:1=f"},
		{"a squash list ends at a comma no digit follows", map[string]string{"exports": "/a b(squash_gids=1,2,ro)\n"}, col("exports", "options"), "exports:1=squash_gids=1,2 | exports:1=ro"},
	})
}
