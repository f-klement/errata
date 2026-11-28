package compose

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A project built from more than one file or service, an override, an
// include or an extends of either kind, reads every value not literal when
// any file it reads looks up a variable no file sets, even one that gives
// nothing to the value read: compose-go merges the files and services by
// rules errata does not follow value by value. The same project with the
// variable set in .env reads literal, and so does one file that looks a
// variable up in another service. Planted breaks: extract not asking
// tainted; tainted not marking a value; merged not set for an override,
// an include or an extends; an extends file not recorded as read;
// fromShell reading no file; use recording a file once whatever project
// loads it, which missed the second include of s.yaml, whose env file
// leaves X unset.
func TestMergedFromTheShellNotLiteral(t *testing.T) {
	web := "services:\n  web:\n    image: w\n"
	other := "services:\n  db:\n    image: \"${X:-d}\"\n"
	cases := []struct {
		name      string
		files     map[string]string
		overrides []string
		want      string
	}{
		{"an override", map[string]string{"compose.yaml": web, "o.yaml": other}, o, "image=w@compose.yaml:3!"},
		{"an include", map[string]string{"compose.yaml": "include: [inc.yaml]\n" + web, "inc.yaml": other}, nil, "image=w@compose.yaml:4!"},
		{"an extends in the file", map[string]string{"compose.yaml": web + "  base:\n    image: b\n  db:\n    image: \"${X:-d}\"\n" + "  api:\n    extends: base\n"}, nil, "image=w@compose.yaml:3!"},
		{"an extends file", map[string]string{"compose.yaml": web + "    extends: {file: b.yaml, service: b}\n", "b.yaml": "services:\n  b:\n    user: u\n  c:\n    image: \"${X:-c}\"\n"}, nil, "image=w@compose.yaml:3!"},
		{"one file in two projects", map[string]string{"compose.yaml": "include:\n  - path: s.yaml\n    env_file: one.env\n  - path: s.yaml\n    env_file: two.env\n" + web, "s.yaml": other, "one.env": "X=1\n", "two.env": "Y=1\n"}, nil, "image=w@compose.yaml:8!"},
		{"set in .env", map[string]string{"compose.yaml": web, "o.yaml": other, ".env": "X=1\n"}, o, "image=w@compose.yaml:3"},
		{"one file", map[string]string{"compose.yaml": web + "  db:\n    image: \"${X:-d}\"\n"}, nil, "image=w@compose.yaml:3"},
	}
	for _, c := range cases {
		if got := mustRead(t, c.files, Spec{Service: "web", Key: "image", Overrides: c.overrides}); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

// The values the verify pass found literal and wrong in a project built
// from more than one file read not literal: a default compose-go's
// normalize filled in for one file, where another file's text for it looks
// up a variable (AA1), and a service an included project merges under a
// file's own service or under another included one's (AA2). A key that
// holds nothing there is refused. Planted break: tainted not refusing an
// empty read.
func TestMergedNotLiteralWhereCrossFileMergesHide(t *testing.T) {
	svc := "services:\n  web:\n"
	inc := map[string]string{
		"compose.yaml": "include: [inc.yaml]\n" + svc + "    cap_add: [NET_ADMIN]\n",
		"inc.yaml":     svc + "    image: \"${IMG:-a}\"\n    user: \"${U:-root}\"\n    cap_add: [\"${C:-NET_ADMIN}\"]\n",
	}
	two := map[string]string{
		"compose.yaml": "include: [a.yaml, b.yaml]\nservices:\n  other:\n    image: o\n",
		"a.yaml":       svc + "    image: w\n    cap_add: [NET_ADMIN]\n",
		"b.yaml":       svc + "    image: w\n    user: \"${U:-root}\"\n    cap_add: [\"${C:-NET_ADMIN}\"]\n",
	}
	cases := []struct {
		name      string
		files     map[string]string
		overrides []string
		key, want string
	}{
		{"B1", map[string]string{"compose.yaml": svc + "    build: {context: ., dockerfile: \"${D:-Dockerfile}\"}\n", "o.yaml": svc + "    build: {context: ./sub}\n"}, o, "build.dockerfile", "build.dockerfile=Dockerfile@o.yaml:3!"},
		{"B2", map[string]string{"compose.yaml": svc + "    build: {context: \"${CTX:-.}\"}\n", "o.yaml": svc + "    build: {dockerfile: X}\n"}, o, "build.context", "build.context=.@o.yaml:3!"},
		{"B3", map[string]string{"compose.yaml": svc + "    extends: {service: b}\n    build: {context: ./sub}\n  b:\n    build: {context: ., dockerfile: \"${D:-Dockerfile}\"}\n"}, nil, "build.dockerfile", "build.dockerfile=Dockerfile@compose.yaml:4!"},
		{"I1", inc, nil, "cap_add", "cap_add.0=NET_ADMIN@compose.yaml:4!"},
		{"I2", inc, nil, "user", "user=root@compose.yaml:3!"},
		{"I6", two, nil, "user", "user=root@a.yaml:2!"},
		{"I7", map[string]string{"compose.yaml": "include: [inc.yaml]\n" + svc + "    cap_add: [NET_ADMIN]\n", "inc.yaml": svc + "    image: \"${IMG:-a}\"\n"}, nil, "image", "image=a@compose.yaml:3!"},
	}
	for _, c := range cases {
		if got := mustRead(t, c.files, Spec{Service: "web", Key: c.key, Overrides: c.overrides}); got != c.want {
			t.Errorf("%s, %s:\n got %s\nwant %s", c.name, c.key, got, c.want)
		}
	}
	refused(t, inc, Spec{Service: "web", Key: "environment.A"}, `"environment.A" holds nothing here, but the project is built from more than one file or service`)
}

// Every, in a project built from more than one file, refuses a variable
// whose name an included project's service of the same name takes from
// the shell, though the main file sets the service too: compose-go merges
// the two, and the shell may give the service the variable. Planted break:
// keysFromShell checking contributors, not sources.
func TestEveryNameFromAnIncludedServiceRefused(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "include: [inc.yaml]\nservices:\n  web:\n    image: w\n    environment: [A=1]\n",
		"inc.yaml":     "services:\n  web:\n    environment: [\"${K:-FOO}=1\"]\n",
	}
	x, err := Every("compose.yaml", "BAR", 0, yamlParse)
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.Extract(t.Context(), extracttest.Repo(t, files))
	if err == nil || !strings.Contains(err.Error(), `inc.yaml: the environment item "${K:-FOO}=1" looks up K`) {
		t.Errorf("got %v", err)
	}
}
