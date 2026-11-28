package compose

import "testing"

// foldCase is a read of the service web and what it gives.
type foldCase struct {
	name      string
	files     map[string]string
	overrides []string
	key, want string
}

func (c foldCase) check(t *testing.T) {
	t.Helper()
	if got := mustRead(t, c.files, Spec{Service: "web", Key: c.key, Overrides: c.overrides}); got != c.want {
		t.Errorf("%s, %s:\n got %s\nwant %s", c.name, c.key, got, c.want)
	}
}

// web is a compose.yaml of the service web with an image and body, and
// an override o.yaml of web with over.
func web(body, over string) map[string]string {
	return map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n" + body, "o.yaml": "services:\n  web:\n" + over}
}

var o = []string{"o.yaml"}

// A value of a list is not literal when an item of the list, in any file
// that writes it, looks up a variable no file sets: compose-go keeps one
// item of those alike, or of a key alike, such as a volume's target, so
// the shell's value may stand beside the item, take its place or move its
// index. That holds for an override, an extends base and within one file,
// for every list compose-go merges, by its items or by a key of theirs, and
// for logging, whose options an override keeps only when both files name
// the same driver. Across files the taint marks these too
// (TestMergedFromTheShellNotLiteral); within one file a pre_start hook
// merges with its service as an override would. A list an override
// replaces, such as command, takes the hook's items alone, and a list no
// file writes a $ in reads literal. Planted breaks: value not asking
// folded; foldAt taking no list as a fold; foldedMappings without logging
// (TestMergesAreComposeGos).
func TestFoldedFromTheShellNotLiteral(t *testing.T) {
	extends := map[string]string{
		"compose.yaml": "services:\n  web:\n    extends: {file: b.yaml, service: b}\n    cap_add: [NET_ADMIN]\n",
		"b.yaml":       "services:\n  b:\n    image: w\n    cap_add: [\"${C:-NET_ADMIN}\"]\n",
	}
	cases := []foldCase{
		{"an override", web("    cap_add: [\"${C:-NET_ADMIN}\"]\n", "    cap_add: [NET_ADMIN]\n"), o, "cap_add", "cap_add.0=NET_ADMIN@o.yaml:3!"},
		{"an index the shell moves", web("    cap_add: [\"${C:-NET_ADMIN}\", SYS_TIME]\n", "    cap_add: [NET_ADMIN]\n"), o, "cap_add", "cap_add.0=NET_ADMIN@o.yaml:3!; cap_add.1=SYS_TIME@compose.yaml:4!"},
		{"an extends base", extends, nil, "cap_add", "cap_add.0=NET_ADMIN@compose.yaml:4!"},
		{"one file", web("    cap_add: [\"${C:-SYS_TIME}\", NET_ADMIN]\n", ""), nil, "cap_add.1", "cap_add.1=NET_ADMIN@compose.yaml:4!"},
		{"cap_drop", web("    cap_drop: [\"${C:-ALL}\"]\n", "    cap_drop: [ALL]\n"), o, "cap_drop", "cap_drop.0=ALL@o.yaml:3!"},
		{"dns", web("    dns: [\"${D:-1.1.1.1}\"]\n", "    dns: 1.1.1.1\n"), o, "dns", "dns.0=1.1.1.1@o.yaml:3!"},
		{"dns_search", web("    dns_search: [\"${D:-a}\"]\n", "    dns_search: [a]\n"), o, "dns_search", "dns_search.0=a@o.yaml:3!"},
		{"tmpfs", web("    tmpfs: [\"${T:-/run}\"]\n", "    tmpfs: [/run]\n"), o, "tmpfs", "tmpfs.0=/run@o.yaml:3!"},
		{"expose", web("    expose: [\"${E:-80}\"]\n", "    expose: [\"80\"]\n"), o, "expose", "expose.0=80@o.yaml:3!"},
		{"build.tags", web("    build: {context: ., tags: [\"${T:-a}\"]}\n", "    build: {tags: [a]}\n"), o, "build.tags", "build.tags.0=a@o.yaml:3!"},
		{"security_opt", web("    security_opt: [\"${S:-a}\"]\n", "    security_opt: [a]\n"), o, "security_opt", "security_opt.0=a@o.yaml:3!"},
		{"volumes by target", web("    volumes: [\"./a:${T:-/data}\"]\n", "    volumes: [\"./b:/data\"]\n"), o, "volumes.0.source", "volumes.0.source=./b@o.yaml:3!"},
		{"ports by their key", web("    ports: [\"${P:-8080}:80\"]\n", "    ports: [\"8080:80\"]\n"), o, "ports.0.published", "ports.0.published=8080@o.yaml:3!"},
		{"logging by its driver", web("    logging: {driver: local, options: {max-size: 1m}}\n", "    logging: {driver: \"${D:-local}\"}\n"), o, "logging.options", "logging.options.max-size=1m@compose.yaml:4!"},
		{"a replaced list", web("    command: [\"${X:-a}\"]\n    pre_start: [{command: [a]}]\n", ""), nil, "pre_start.0.command", "pre_start.0.command.0=a@compose.yaml:5"},
		{"a list without a $", web("    cap_add: [NET_ADMIN]\n    dns: [\"${D:-1.1.1.1}\"]\n", ""), nil, "cap_add", "cap_add.0=NET_ADMIN@compose.yaml:4"},
	}
	for _, c := range cases {
		c.check(t)
	}
}

// A key below a fold that holds nothing is refused where a source of the
// fold looks up a variable no file sets: the shell's value may stand
// there. A key below a fold without one holds nothing as before, a
// hook's index too. Planted breaks: values not asking absentFromShell;
// absentFromShell not checking the service for a hook's key.
func TestAbsentBelowAFoldFromTheShellRefused(t *testing.T) {
	files := web("    cap_add: [\"${C:-NET_ADMIN}\"]\n", "    cap_add: [NET_ADMIN]\n")
	refused(t, files, Spec{Service: "web", Key: "cap_add.1", Overrides: o}, `"cap_add.1" holds nothing here, but cap_add takes a value that looks up a variable`)
	logging := web("    logging: {driver: local, options: {max-size: 1m}}\n", "    logging: {driver: \"${D:-syslog}\"}\n")
	refused(t, logging, Spec{Service: "web", Key: "logging.options.max-size", Overrides: o}, `but logging takes a value`)
	hook := web("    cap_add: [\"${C:-NET_ADMIN}\"]\n    pre_start: [{command: x}]\n", "")
	refused(t, hook, Spec{Service: "web", Key: "pre_start.0.cap_add.1"}, `but cap_add takes a value`)
	plain := web("    cap_add: [NET_ADMIN]\n    pre_start: [{command: x}]\n", "    cap_add: [NET_ADMIN]\n")
	for _, key := range []string{"cap_add.1", "pre_start.3"} {
		if got := mustRead(t, plain, Spec{Service: "web", Key: key, Overrides: o}); got != "" {
			t.Errorf("%s without a $: %s", key, got)
		}
	}
}

// A value a pre_start hook takes from its service, which compose-go copies
// into the hook, stands at the service's line and is literal only where
// the service's is: image, user, an environment variable, beside the
// hook's own environment too. A value the hook writes itself stands at the
// hook's line, and an item of a list the hook and its service both write
// is not literal when the service's list looks up a variable no file
// sets. Planted breaks: value not asking inherited; inherited taking a
// value the hook writes for the service's; folded not checking the
// service's fold for a hook.
func TestHookInheritsNotLiteral(t *testing.T) {
	svc := "services:\n  web:\n    image: \"${IMG:-a}\"\n    user: \"${U:-root}\"\n    environment: [\"A=${X}\"]\n    cap_add: [\"${C:-NET_ADMIN}\"]\n"
	bare := map[string]string{"compose.yaml": svc + "    pre_start: [{command: x}]\n"}
	own := map[string]string{"compose.yaml": svc + "    pre_start:\n      - command: x\n        image: b\n        environment: [B=1]\n        cap_add: [SYS_TIME]\n"}
	over := map[string]string{
		"compose.yaml": "services:\n  web:\n    image: a\n    pre_start: [{command: x}]\n",
		"o.yaml":       "services:\n  web:\n    image: \"${IMG:-a}\"\n",
	}
	plain := map[string]string{"compose.yaml": "services:\n  web:\n    image: a\n    cap_add: [NET_ADMIN]\n    pre_start:\n      - command: x\n        cap_add: [SYS_TIME]\n"}
	cases := []foldCase{
		{"image", bare, nil, "pre_start.0.image", "pre_start.0.image=${IMG:-a}@compose.yaml:3!"},
		{"user", bare, nil, "pre_start.0.user", "pre_start.0.user=${U:-root}@compose.yaml:4!"},
		{"environment", bare, nil, "pre_start.0.environment", "pre_start.0.environment.A=A=${X}@compose.yaml:5!"},
		{"the hook's own", own, nil, "pre_start.0.image", "pre_start.0.image=b@compose.yaml:9"},
		{"beside the hook's environment", own, nil, "pre_start.0.environment", "pre_start.0.environment.A=A=${X}@compose.yaml:5!; pre_start.0.environment.B=1@compose.yaml:10"},
		{"a list of both", own, nil, "pre_start.0.cap_add", "pre_start.0.cap_add.0=${C:-NET_ADMIN}@compose.yaml:6!; pre_start.0.cap_add.1=SYS_TIME@compose.yaml:11!"},
		{"an override", over, o, "pre_start.0.image", "pre_start.0.image=${IMG:-a}@o.yaml:3!"},
		{"a list without a $", plain, nil, "pre_start.0.cap_add", "pre_start.0.cap_add.0=NET_ADMIN@compose.yaml:4; pre_start.0.cap_add.1=SYS_TIME@compose.yaml:7"},
	}
	for _, c := range cases {
		c.check(t)
	}
}

// An extra host stands at the line of the item that wrote it, though
// compose-go sorts the list, and an item that writes two addresses gives
// both at its line. A host the files write with an address from the shell
// reads, its own value not literal at its line, the list's others not
// literal too, since an address the shell gives may move them. Two items
// of one host stand each at its own line. Planted breaks: rawStep taking
// an extra host by its index; hostItem matching no item; hostItem matching
// an item by its host alone.
func TestExtraHostsAtTheirLines(t *testing.T) {
	cases := []foldCase{
		{"sorted", web("    extra_hosts:\n      - \"z:1.1.1.1\"\n      - \"a:2.2.2.2\"\n", ""), nil, "extra_hosts", "extra_hosts.0=a=2.2.2.2@compose.yaml:6; extra_hosts.1=z=1.1.1.1@compose.yaml:5"},
		{"two addresses", web("    extra_hosts:\n      - \"z=1.1.1.1\"\n      - \"a:2.2.2.2,::1\"\n", ""), nil, "extra_hosts", "extra_hosts.0=a=2.2.2.2@compose.yaml:6; extra_hosts.1=a=::1@compose.yaml:6; extra_hosts.2=z=1.1.1.1@compose.yaml:5"},
		{"one host on two lines", web("    extra_hosts:\n      - \"h=1.1.1.1\"\n      - \"h=2.2.2.2\"\n", ""), nil, "extra_hosts", "extra_hosts.0=h=1.1.1.1@compose.yaml:5; extra_hosts.1=h=2.2.2.2@compose.yaml:6"},
		{"an address from the shell", web("    extra_hosts:\n      - \"z:${IP:-1.1.1.1}\"\n      - \"a:2.2.2.2\"\n", ""), nil, "extra_hosts", "extra_hosts.0=a=2.2.2.2@compose.yaml:6!; extra_hosts.1=z:${IP:-1.1.1.1}@compose.yaml:5!"},
	}
	for _, c := range cases {
		c.check(t)
	}
}

// Within one file, compose-go builds develop from x-develop too, merging
// the alias under the key, and adds to depends_on a service that
// network_mode: service:db names. A value below either key is not literal
// when the key beside it looks up a variable no file sets, and a key below
// either that holds nothing is refused; an alias without one reads
// literal. Planted breaks: folded not asking besideFromShell;
// absentFromShell not asking it; beside without develop; beside without
// network_mode.
func TestBesideFromTheShell(t *testing.T) {
	alias := func(path string) map[string]string {
		return web("    x-develop:\n      watch:\n        - {path: \""+path+"\", action: rebuild}\n    develop:\n      watch:\n        - {path: ./a, action: rebuild}\n", "")
	}
	dep := func(mode string) map[string]string {
		return map[string]string{"compose.yaml": "services:\n  web:\n    image: w\n    network_mode: \"${NM:-" + mode + "}\"\n    depends_on: {cache: {condition: service_started}}\n  db:\n    image: d\n  cache:\n    image: c\n"}
	}
	cases := []foldCase{
		{"x-develop beside develop", alias("${P:-./b}"), nil, "develop.watch.0.path", "develop.watch.0.path=./b@compose.yaml:8!"},
		{"x-develop without a $", alias("./b"), nil, "develop.watch.0.path", "develop.watch.0.path=./b@compose.yaml:8"},
		{"depends_on from network_mode", dep("service:db"), nil, "depends_on.db.condition", "depends_on.db.condition=service_started@compose.yaml:5!"},
	}
	for _, c := range cases {
		c.check(t)
	}
	same := web("    x-develop:\n      watch:\n        - {path: ./b, action: \"${ACT:-rebuild}\"}\n    develop:\n      watch:\n        - {path: ./b, action: rebuild}\n", "")
	refused(t, same, Spec{Service: "web", Key: "develop.watch.1"}, `"develop.watch.1" holds nothing here, but compose-go builds develop from x-develop too`)
	refused(t, dep("host"), Spec{Service: "web", Key: "depends_on.db"}, `"depends_on.db" holds nothing here, but compose-go builds depends_on from network_mode too`)
}
