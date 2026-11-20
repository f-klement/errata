package hcl

import (
	"strings"
	"testing"
)

// main.tf, a Terraform module with two instances, a data source of the
// same labels as one of them, a variable and locals.
const mainTF = `variable "region" {
  type    = string
  default = "eu-west-1"
}

resource "aws_instance" "web" {
  ami           = data.aws_ami.web.id
  instance_type = "t3.micro"
}

data "aws_instance" "web" {
  instance_type = "t3.large"
}

resource "aws_instance" "db" {
  instance_type = "m5.large"
}
`

// A path selects an attribute by its block's type and labels and its own
// name: the ticket's resource.aws_instance.web.instance_type is the web
// resource's, not the data source's of the same labels nor the db
// resource's. A label that matches no block selects nothing, and so do
// too few labels on a path that goes on. A path without [*] sets no
// condition. Planted breaks: block matching any label; body matching
// blocks of any type; add setting the condition on every path.
func TestAPathSelectsAnAttributeByBlockTypeLabelsAndName(t *testing.T) {
	same(t, extractPath(t, "resource.aws_instance.web.instance_type", file("main.tf", mainTF)), "main.tf:8=t3.micro key instance_type")
	same(t, extractPath(t, "data.aws_instance.web.instance_type", file("main.tf", mainTF)), "main.tf:12=t3.large key instance_type")
	same(t, extractPath(t, "resource.aws_instance.api.instance_type", file("main.tf", mainTF)),
		"no value: nothing in main.tf matches the path resource.aws_instance.api.instance_type")
	same(t, extractPath(t, "resource.aws_instance.instance_type", file("main.tf", mainTF)),
		"no value: nothing in main.tf matches the path resource.aws_instance.instance_type")
}

// variable.region.default in a module and the key region of a .tfvars file
// read the same way, as an attribute of a body: the file's or the block's.
// Planted break: body matching an attribute only inside a block.
func TestAVariableDefaultAndATfvarsKeyReadTheSameWay(t *testing.T) {
	same(t, extractPath(t, "variable.region.default", file("main.tf", mainTF)), "main.tf:3=eu-west-1 key default")
	same(t, extractPath(t, "region", file("prod.tfvars", "# the production region\nregion = \"eu-west-1\"\nzones  = 3\n")), "prod.tfvars:2=eu-west-1 key region")
	same(t, extractPath(t, "[*]", file("prod.tfvars", "region = \"eu-west-1\"\nzones  = 3\n")),
		"prod.tfvars:1=eu-west-1 key region when region",
		"prod.tfvars:2=3 key zones when zones")
}

// A literal is returned as the file wrote it, read as HCL reads it: a
// number as written, not as its value prints, a string with its escapes
// read, a heredoc's text with its indent cut as <<- cuts it, $${ and %%{
// as the text they escape, a negative number as a minus sign and its
// digits, whatever the file writes between them. Planted breaks: literal
// returning a string's text as written, quotes and all; literal returning
// a number as cty prints it; literal leaving a negative number not
// literal; literal returning a negative number as the file wrote it.
func TestALiteralIsReturnedAsWritten(t *testing.T) {
	src := "a = 1.50\nb = 1e3\nc = -2\nd = true\ne = null\n" +
		"f = \"say \\\"hi\\\"\\t\\u00e9\"\ng = \"\"\nh = \"$${x} %%{y}\"\n" +
		"i = <<EOT\none\n  two\nEOT\nj = <<-EOT\n    one\n      two\n    EOT\nk = - 1.50\nl = -/* c */2\n"
	same(t, extractPath(t, "[*]", file("t.tfvars", src)),
		"t.tfvars:1=1.50 key a when a",
		"t.tfvars:2=1e3 key b when b",
		"t.tfvars:3=-2 key c when c",
		"t.tfvars:4=true key d when d",
		"t.tfvars:5=null key e when e",
		"t.tfvars:6=say \"hi\"\té key f when f",
		"t.tfvars:7= key g when g",
		"t.tfvars:8=${x} %{y} key h when h",
		"t.tfvars:9=one\n  two\n key i when i",
		"t.tfvars:13=one\n  two\n key j when j",
		"t.tfvars:17=-1.50 key k when k",
		"t.tfvars:18=-2 key l when l")
}

// An expression that needs evaluation is returned as the file wrote it and
// marked not literal: a reference, an interpolation, a directive, a
// function call, an operator, a conditional, a for expression, and a
// literal in parentheses. Nothing is evaluated: file() names a file the
// plugin never opens, which it could not, and the text is the call.
// Planted breaks: literal taking any expression as literal; templateText
// taking an interpolation's text as literal.
func TestAnExpressionThatNeedsEvaluationIsNotLiteral(t *testing.T) {
	src := "a = var.region\nb = \"${var.env}-a\"\nc = \"${var.env}\"\nd = \"%{if var.x}y%{endif}\"\n" +
		"e = file(\"/etc/passwd\")\nf = 1 + 2\ng = var.x ? 1 : 2\nh = [for z in var.l : z]\ni = (3)\nj = -var.n\n"
	same(t, extractPath(t, "[*]", file("t.tf", src)),
		"t.tf:1=var.region (not literal) key a when a",
		"t.tf:2=\"${var.env}-a\" (not literal) key b when b",
		"t.tf:3=\"${var.env}\" (not literal) key c when c",
		"t.tf:4=\"%{if var.x}y%{endif}\" (not literal) key d when d",
		"t.tf:5=file(\"/etc/passwd\") (not literal) key e when e",
		"t.tf:6=1 + 2 (not literal) key f when f",
		"t.tf:7=var.x ? 1 : 2 (not literal) key g when g",
		"t.tf:8=[for z in var.l : z] (not literal) key h when h",
		"t.tf:9=(3) (not literal) key i when i",
		"t.tf:10=-var.n (not literal) key j when j")
}

// A path that goes on past a value the file computes means a part of that
// value, which only evaluating it would give: the value is returned as
// written, not literal. Past a literal there is nothing. Planted break:
// expr selecting nothing past a value the file computes.
func TestAPathPastAComputedValueReturnsIt(t *testing.T) {
	src := "tags = merge(local.tags, {Name = \"web\"})\nname = \"web\"\n"
	same(t, extractPath(t, "tags.Name", file("t.tf", src)), "t.tf:1=merge(local.tags, {Name = \"web\"}) (not literal) key tags")
	same(t, extractPath(t, "name.first", file("t.tf", src)), "no value: nothing in t.tf matches the path name.first")
	same(t, extractPath(t, "name[0]", file("t.tf", src)), "no value: nothing in t.tf matches the path name[0]")
}

// Repeated blocks are selected by label, and [*] for a label takes every
// block, each value naming the labels it matched. A block with no labels
// is named by its type, so its repeats each give a value. [*] after the
// labels takes every attribute of the block's body. Planted breaks: block
// refusing [*] for a label; body stopping at the first block that
// matches.
func TestRepeatedBlocksAreSelectedByLabelAndStarTakesEvery(t *testing.T) {
	same(t, extractPath(t, "resource.aws_instance[*].instance_type", file("main.tf", mainTF)),
		"main.tf:8=t3.micro key instance_type when resource.aws_instance.web.instance_type",
		"main.tf:16=m5.large key instance_type when resource.aws_instance.db.instance_type")
	same(t, extractPath(t, "resource[*][*].instance_type", file("main.tf", mainTF)),
		"main.tf:8=t3.micro key instance_type when resource.aws_instance.web.instance_type",
		"main.tf:16=m5.large key instance_type when resource.aws_instance.db.instance_type")
	same(t, extractPath(t, "resource.aws_instance.web[*]", file("main.tf", mainTF)),
		"main.tf:7=data.aws_ami.web.id (not literal) key ami when resource.aws_instance.web.ami",
		"main.tf:8=t3.micro key instance_type when resource.aws_instance.web.instance_type")
	sg := "resource \"aws_security_group\" \"web\" {\n  ingress {\n    from_port = 80\n  }\n  ingress {\n    from_port = 443\n  }\n}\n"
	same(t, extractPath(t, "resource.aws_security_group.web.ingress.from_port", file("sg.tf", sg)),
		"sg.tf:3=80 key from_port",
		"sg.tf:6=443 key from_port")
}

// [*] over nested blocks returns every match, depth first in the file's
// order: Nomad's job, its groups and their tasks. A block and an attribute
// of one name come in the file's order too, though HCL keeps a body's
// attributes apart from its blocks. Planted break: children left
// unsorted, attributes first.
func TestStarOverNestedBlocksReturnsEveryMatchInFileOrder(t *testing.T) {
	job := `job "api" {
  group "a" {
    task "web" { driver = "docker" }
    task "log" { driver = "exec" }
  }
  group "b" {
    task "web" { driver = "raw_exec" }
  }
}
`
	same(t, extractPath(t, "job.api.group[*].task[*].driver", file("api.nomad.hcl", job)),
		"api.nomad.hcl:3=docker key driver when job.api.group.a.task.web.driver",
		"api.nomad.hcl:4=exec key driver when job.api.group.a.task.log.driver",
		"api.nomad.hcl:7=raw_exec key driver when job.api.group.b.task.web.driver")
	same(t, extractPath(t, "job.api.group[*].task.web.driver", file("api.nomad.hcl", job)),
		"api.nomad.hcl:3=docker key driver when job.api.group.a.task.web.driver",
		"api.nomad.hcl:7=raw_exec key driver when job.api.group.b.task.web.driver")
	same(t, extractPath(t, "x.k", file("f.tf", "x { k = 1 }\nx = { k = 2 }\n")),
		"f.tf:1=1 key k",
		"f.tf:2=2 key k")
}

// A path goes on into an object by key and into a list by index, and [*]
// takes every item or element. A key the file computes is matched only by
// [*], and its value is then not literal, since which key holds it is
// computed. Planted breaks: object matching a computed key by its text;
// value leaving a computed key's value literal; list ignoring the index.
func TestAPathGoesIntoObjectsAndLists(t *testing.T) {
	src := "tags = {\n  Name = \"web\"\n  \"team.owner\" = \"ops\"\n  (var.k) = \"x\"\n}\nzones = [\n  \"a\",\n  \"b\",\n]\n"
	same(t, extractPath(t, "tags.Name", file("t.tf", src)), "t.tf:2=web key Name")
	same(t, extractPath(t, `tags["team.owner"]`, file("t.tf", src)), "t.tf:3=ops key team.owner")
	same(t, extractPath(t, `tags["(var.k)"]`, file("t.tf", src)), `no value: nothing in t.tf matches the path tags["(var.k)"]`)
	same(t, extractPath(t, "tags[*]", file("t.tf", src)),
		"t.tf:2=web key Name when tags.Name",
		`t.tf:3=ops key team.owner when tags["team.owner"]`,
		`t.tf:4=x (not literal) key (var.k) when tags["(var.k)"]`)
	same(t, extractPath(t, "zones[1]", file("t.tf", src)), "t.tf:8=b key zones")
	same(t, extractPath(t, "zones[2]", file("t.tf", src)), "no value: nothing in t.tf matches the path zones[2]")
	same(t, extractPath(t, "zones[*]", file("t.tf", src)), "t.tf:7=a key zones when zones[0]", "t.tf:8=b key zones when zones[1]")
}

// A path that ends at a block, an object or a list selects what holds no
// value to compare, and the rule is unknown, naming the file and line.
// Planted breaks: block passing a path that ends among its labels; value
// returning an object's text.
func TestAPathThatSelectsNoValueIsAnError(t *testing.T) {
	src := mainTF + "locals {\n  tags = { a = 1 }\n  ids  = [1]\n}\n"
	for path, want := range map[string]string{
		"resource.aws_instance.web": `error: main.tf:6: the path resource.aws_instance.web selects the block resource "aws_instance" "web", which holds no value; name an attribute in it`,
		"resource.aws_instance":     `error: main.tf:6: the path resource.aws_instance selects the block resource "aws_instance" "web"`,
		"locals":                    `error: main.tf:18: the path locals selects the block locals, which holds no value`,
		"locals.tags":               "error: main.tf:19: the path locals.tags selects an object, which is no value to compare; name one of its keys, or add [*] for every value",
		"locals.ids":                "error: main.tf:20: the path locals.ids selects a list, which is no value to compare; add [*] for every element, or an index such as [0]",
	} {
		if got := strings.Join(shown(extractPath(t, path, file("main.tf", src))), "\n"); !strings.HasPrefix(got, want) {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// Each value carries its file and the line its expression starts on, as
// errata counts lines: a lone CR in a comment ends a line for errata and
// not for HCL, and a CRLF ends one; values come file by file, in the
// files' order. A heredoc's line is the line of its <<. Planted break:
// line counting HCL's lines, Range().Start.Line.
func TestEachValueCarriesItsFileAndLine(t *testing.T) {
	a := "# old\rnew\nregion = \"eu-west-1\"\r\n\r\nzones = [\r\n  1,\r\n  2]\r\n"
	b := "/* a\rb */ region = <<EOT\nx\nEOT\n"
	same(t, extractPath(t, "region", file("a.tfvars", a), file("b.tfvars", b)),
		"a.tfvars:3=eu-west-1 key region",
		"b.tfvars:2=x\n key region")
	same(t, extractPath(t, "zones[*]", file("a.tfvars", a)),
		"a.tfvars:6=1 key zones when zones[0]",
		"a.tfvars:7=2 key zones when zones[1]")
}

// A file that does not parse is an error naming the file and the line of
// HCL's first error, as errata counts lines, with HCL's message; the rule
// is unknown. A later file is not read. Planted break: read ignoring the
// diagnostics.
func TestAFileThatDoesNotParseIsAnError(t *testing.T) {
	for src, want := range map[string]string{
		"a = 1\na = 2\n":                   `error: f.tf:2: Attribute redefined: The argument "a" was already set at f.tf:1,1-2. Each argument may be set only once.`,
		"# x\ry\nresource \"a\" \"b\" {\n": "error: f.tf:3: Unclosed configuration block: There is no closing brace for this block before the end of the file.",
		"a = \xff\n":                       "error: f.tf:1: Invalid character encoding: All input files must be UTF-8 encoded.",
		"a = \"x\n":                        "error: f.tf:1: Invalid multi-line string",
	} {
		got := strings.Join(shown(extractPath(t, "a", file("f.tf", src), file("g.tf", "a = 1\n"))), "\n")
		if !strings.HasPrefix(got, want) {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}

// The values stop at wire.MaxValues across the request's files: 100000
// pass, one more is an error at the value past the limit, and the room a
// file leaves counts for the next. Planted breaks: add appending without
// the check; extract handing each file the whole limit.
func TestTheValuesStopAtTheLimit(t *testing.T) {
	list := func(n int) string { return "x = [" + strings.Repeat("1,", n) + "]\n" }
	if got := extractPath(t, "x[*]", file("a.tf", list(100_000))); len(got.Values) != 100_000 {
		t.Fatalf("100000 values: got %d, %v", len(got.Values), shown(got)[:1])
	}
	want := "error: a.tf:1: the files hold more than 100000 values, the limit; narrow the path or the files"
	same(t, extractPath(t, "x[*]", file("a.tf", list(100_001))), want)
	same(t, extractPath(t, "x[*]", file("a.tf", list(100_000)), file("b.tf", list(1))),
		"error: b.tf:1: the files hold more than 100000 values, the limit; narrow the path or the files")
}

// A path that matches nothing in any file finds no value and says so, so
// allow_empty decides; with several files the message counts them.
// With no files the reply is an empty list. Planted breaks: extract
// replying an empty list; extract replying no value for no files.
func TestAPathThatMatchesNothingIsNoValue(t *testing.T) {
	same(t, extractPath(t, "region", file("a.tf", "zone = 1\n"), file("b.tf", "")),
		"no value: nothing in the 2 files matches the path region")
	if r := extractPath(t, "region"); r.Values == nil || len(r.Values) != 0 || r.NoValue != nil || r.Error != nil {
		t.Errorf("no files: got %s, want an empty list of values, which the host reads as it reads a set that matched no file", mustJSON(r))
	}
}
