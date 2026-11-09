package sudoers

import "testing"

// User and group names match in any case, as sudo's case_insensitive_user
// and case_insensitive_group, on by default, have them; a Defaults line
// may turn either off, and a file outside the repository may too, so a
// name that differs only in case may match, unless a global Defaults line
// in the files turns the flag off and no scoped one sets it. Planted
// break: compareUser leaving out the case-only difference.
func TestCaseOfNames(t *testing.T) {
	file := "Bob ALL = (ALL) NOPASSWD: ALL\n%Ops ALL = /a\nbob ALL = /b\n"
	runReads(t, []readCase{
		{"a user", map[string]string{"sudoers": file}, user("sudoers", "bob"),
			"sudoers:1={command=ALL host=ALL runas=ALL tags=NOPASSWD user=Bob} !lit | sudoers:2={command=/a host=ALL user=%Ops} !lit | sudoers:3={command=/b host=ALL user=bob}"},
		{"a group", map[string]string{"sudoers": file}, user("sudoers", "%ops"), "sudoers:2={command=/a host=ALL user=%Ops} !lit"},
		{"turned off", map[string]string{"sudoers": "Defaults !case_insensitive_user, !case_insensitive_group\n" + file}, user("sudoers", "bob"),
			"sudoers:3={command=/a host=ALL user=%Ops} !lit | sudoers:4={command=/b host=ALL user=bob}"},
		{"group turned off", map[string]string{"sudoers": "Defaults !case_insensitive_group\n" + file}, user("sudoers", "%ops"), ""},
		{"turned off for one user only", map[string]string{"sudoers": "Defaults:root !case_insensitive_user\n" + file}, user("sudoers", "bob"),
			"sudoers:2={command=ALL host=ALL runas=ALL tags=NOPASSWD user=Bob} !lit | sudoers:3={command=/a host=ALL user=%Ops} !lit | sudoers:4={command=/b host=ALL user=bob}"},
	})
}

// A group named by a %#gid, or as a non-Unix group (%:ops), may be the
// group a user: of %ops names, and the host decides. Planted break:
// compareGroups taking the other forms as different groups.
func TestFormsOfAGroup(t *testing.T) {
	files := map[string]string{"sudoers": "%#1000 ALL = /a\n%:ops ALL = /b\n%#1000, %#1001 ALL = /c\n%staff ALL = /d\n"}
	runReads(t, []readCase{
		{"by name", files, user("sudoers", "%ops"), "sudoers:1={command=/a host=ALL user=%#1000} !lit | sudoers:2={command=/b host=ALL user=%:ops} !lit | sudoers:3={command=/c host=ALL user=%#1000,%#1001} !lit"},
		{"by ID", files, user("sudoers", "%#1000"), "sudoers:1={command=/a host=ALL user=%#1000} | sudoers:2={command=/b host=ALL user=%:ops} !lit | sudoers:3={command=/c host=ALL user=%#1000,%#1001} | sudoers:4={command=/d host=ALL user=%staff} !lit"},
	})
}

// A Runas spec of () runs the command as the invoking user, and (:grp) as
// the invoking user with that group, not as runas_default; a Runas list
// reads a non-Unix group. Planted break: runas leaving the users empty
// for ().
func TestRunasForms(t *testing.T) {
	runReads(t, []readCase{
		{"forms", map[string]string{"sudoers": "bob ALL = (root) /a, () /b, (:adm) /c, /d, (%:admins) /e\n"}, user("sudoers", "bob"),
			"sudoers:1={command=/a host=ALL runas=root user=bob} | sudoers:1={command=/b host=ALL runas=(invoking user) user=bob}" +
				" | sudoers:1={command=/c host=ALL runas=(invoking user) runas_group=adm user=bob} | sudoers:1={command=/d host=ALL runas=(invoking user) runas_group=adm user=bob}" +
				" | sudoers:1={command=/e host=ALL runas=%:admins user=bob}"},
	})
}

// Include lines read as sudo's lexer reads them: #include and
// #includedir only at the start of a line, @include anywhere after white
// space, and a comment after the path. Planted break: includeLine reading
// the trimmed line for #include.
func TestIncludeLines(t *testing.T) {
	runReads(t, []readCase{
		{"indented #include", map[string]string{"etc/sudoers": "  #include /etc/sudoers.local\n  @include /etc/other\n", "etc/sudoers.local": "bob ALL = /never\n", "etc/other": "bob ALL = /a\n"},
			user("etc/sudoers", "bob"), "etc/other:1={command=/a host=ALL user=bob}"},
		{"a comment after the path", map[string]string{"etc/sudoers": "#include /etc/sudoers.local # site rules\n", "etc/sudoers.local": "bob ALL = /a\n"},
			user("etc/sudoers", "bob"), "etc/sudoers.local:1={command=/a host=ALL user=bob}"},
	})
	runRefusals(t, []refuseCase{
		{"relative climb", map[string]string{"etc/sudoers": "#include ../outside\n", "outside": "bob ALL = ALL\n"}, user("etc/sudoers", "bob"), "outside the repository"},
		{"absolute climb", map[string]string{"etc/sudoers": "#include /etc/../outside\n", "outside": "bob ALL = ALL\n"}, user("etc/sudoers", "bob"), "outside the repository"},
	})
}

// Commands read as sudo's lexer reads them: a ! after a digest denies the
// command, a negated alias of a negated alias of several commands allows
// each, a \ before a glob character stays, and quotes are part of an
// argument. A list of digests, which errata does not read, is refused.
// Planted break: entry negating a whole alias twice into one entry.
func TestCommandForms(t *testing.T) {
	runReads(t, []readCase{
		{"digest negated", map[string]string{"sudoers": "bob ALL = sha256:abcd !/bin/su\n"}, user("sudoers", "bob"), "sudoers:1={command=!sha256:abcd /bin/su host=ALL user=bob}"},
		{"double negation", map[string]string{"sudoers": "Cmnd_Alias D = /bin/a, /bin/b\nCmnd_Alias C = !D\nbob ALL = !C\n"}, user("sudoers", "bob"),
			"sudoers:1={command=/bin/a host=ALL user=bob} | sudoers:1={command=/bin/b host=ALL user=bob}"},
		{"escapes and quotes", map[string]string{"sudoers": "bob ALL = /bin/echo \\* \\, \"a\", /bin/ls \"\"\n"}, user("sudoers", "bob"),
			"sudoers:1={command=/bin/echo \\* , \"a\" host=ALL user=bob} | sudoers:1={command=/bin/ls \"\" host=ALL user=bob}"},
		{"a template's expression", map[string]string{"sudoers.j2": "bob ALL = /bin/{{ x, y }} a\n"}, user("sudoers.j2", "bob"), "sudoers.j2:1={command=/bin/{{ x, y }} a host=ALL user=bob} !lit"},
	})
	runRefusals(t, []refuseCase{
		{"! before a digest", map[string]string{"sudoers": "bob ALL = !sha256:abcd /bin/su\n"}, user("sudoers", "bob"), "a ! before a digest"},
		{"a list of digests", map[string]string{"sudoers": "bob ALL = sha224:ab,sha256:cd /bin/su\n"}, user("sudoers", "bob"), "expected a command"},
	})
}

// A Defaults scope is a list with white space around its commas, or after
// its binding character, written in its condition without that white
// space; ROLE and TYPE, and PRIVS and LIMITPRIVS, carry over as pairs.
// Planted break: the scope read to the first blank.
func TestScopesAndPairs(t *testing.T) {
	runReads(t, []readCase{
		{"blanks in a scope", map[string]string{"sudoers": "Defaults:bob, alice !requiretty\nDefaults: bob  ,carol requiretty\n"}, defaults("sudoers", "requiretty"),
			"sudoers:1=false [Defaults:bob,alice] | sudoers:2=true [Defaults:bob,carol]"},
		{"pairs", map[string]string{"sudoers": "bob ALL = ROLE=a TYPE=b /a, ROLE=c /b, /c, PRIVS=x LIMITPRIVS=y /d, LIMITPRIVS=z /e\n"}, user("sudoers", "bob"),
			"sudoers:1={command=/a host=ALL options=ROLE=a,TYPE=b user=bob} | sudoers:1={command=/b host=ALL options=ROLE=c user=bob}" +
				" | sudoers:1={command=/c host=ALL options=ROLE=c user=bob} | sudoers:1={command=/d host=ALL options=ROLE=c,PRIVS=x,LIMITPRIVS=y user=bob}" +
				" | sudoers:1={command=/e host=ALL options=ROLE=c,LIMITPRIVS=z user=bob}"},
	})
}
