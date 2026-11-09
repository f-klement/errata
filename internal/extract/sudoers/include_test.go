package sudoers

import (
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// Includes are read in place, in sudo's order: #include and @include of a
// file, relative to the including file or under /etc below the directory
// that stands for it, and #includedir and @includedir of a directory, its
// files by name but those whose name holds a . or ends in ~, and none
// below it. Aliases a drop-in defines hold in the file that includes it.
// Planted break: dropIns keeping a name with a dot.
func TestIncludes(t *testing.T) {
	files := map[string]string{
		"roles/sudo/files/sudoers": "Defaults secure_path=/a\n#includedir /etc/sudoers.d\nDefaults secure_path=/z\n@include extra\n" +
			"ADMINS ALL = /bin/x\n",
		"roles/sudo/files/sudoers.d/20-b":       "Defaults secure_path=/c\n",
		"roles/sudo/files/sudoers.d/10-a":       "Defaults secure_path=/b\nUser_Alias ADMINS = alice\n",
		"roles/sudo/files/sudoers.d/README.md":  "Defaults secure_path=/never\n",
		"roles/sudo/files/sudoers.d/old~":       "Defaults secure_path=/never\n",
		"roles/sudo/files/sudoers.d/sub/x":      "Defaults secure_path=/never\n",
		"roles/sudo/files/extra":                "@include \"/etc/more file\"\n",
		"roles/sudo/files/more file":            "Defaults secure_path=/m%%\n",
		"roles/sudo/files/sudoers.d/30-c.j2":    "Defaults secure_path=/t\n",
		"roles/sudo/files/sudoers.d/40-d.dpkg~": "Defaults secure_path=/never\n",
	}
	runReads(t, []readCase{
		{"in place", files, defaults("roles/sudo/files/sudoers", "secure_path"),
			"roles/sudo/files/sudoers:1=/a | roles/sudo/files/sudoers.d/10-a:1=/b | roles/sudo/files/sudoers.d/20-b:1=/c" +
				" | roles/sudo/files/sudoers.d/30-c.j2:1=/t | roles/sudo/files/sudoers:3=/z | roles/sudo/files/more file:1=/m%%"},
		{"an alias from a drop-in", files, user("roles/sudo/files/sudoers", "alice"),
			"roles/sudo/files/sudoers:5={command=/bin/x host=ALL user=alice}"},
		{"drop-ins of files:", files, Spec{Where: core.Where{Files: []string{"roles/sudo/files/sudoers.d/*"}}, Defaults: "secure_path"},
			"roles/sudo/files/sudoers.d/10-a:1=/b | roles/sudo/files/sudoers.d/20-b:1=/c | roles/sudo/files/sudoers.d/30-c.j2:1=/t"},
		{"a drop-in's include", map[string]string{"etc/sudoers.d/a": "#include /etc/sudoers.d/b\n", "etc/sudoers.d/b": "Defaults x\n"},
			defaults("etc/sudoers.d/a", "x"), "etc/sudoers.d/b:1=true"},
		{"no directory", map[string]string{"sudoers": "@includedir /etc/sudoers.d\nDefaults x\n"}, defaults("sudoers", "x"), "sudoers:2=true"},
	})
}

// An include errata cannot read inside the repository makes the read an
// error: a path outside /etc, one that climbs out, %h, a template's
// expression, a missing file, and an include of itself past sudo's depth.
// Planted break: mapPath reading any absolute path below the repository's
// /etc.
func TestIncludeRefusals(t *testing.T) {
	runRefusals(t, []refuseCase{
		{"outside /etc", map[string]string{"sudoers": "#include /usr/local/etc/sudoers\n"}, defaults("sudoers", "x"), "outside the repository"},
		{"climbs out", map[string]string{"a/sudoers": "#include ../../x\n"}, defaults("a/sudoers", "x"), "outside the repository"},
		{"host name", map[string]string{"sudoers": "#include /etc/sudoers.%h\n"}, defaults("sudoers", "x"), "%h, the host's name"},
		{"template", map[string]string{"sudoers.j2": "#include /etc/{{ x }}\n"}, defaults("sudoers.j2", "x"), "a template engine computes"},
		{"missing", map[string]string{"sudoers": "@include /etc/sudoers.local\n"}, defaults("sudoers", "x"), "which the repository does not hold"},
		{"itself", map[string]string{"sudoers": "@include sudoers\n"}, defaults("sudoers", "x"), "nest more than 128 deep"},
		{"two paths", map[string]string{"sudoers": "@include a b\n"}, defaults("sudoers", "x"), "an include names one path"},
	})
}
