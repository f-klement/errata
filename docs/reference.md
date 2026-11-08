# errata reference

The reference manual: every key of `errata.yaml`, every check and rule key, every extractor with
its fields, limits and values a file only computes, the check protocol, scopes, the command line,
exit codes and the output formats, each in a section of its own, for looking up. The
[README](../README.md) introduces errata, the [handbook](handbook.md) teaches it in the order a
new user needs it, with four checks worked through from the files to the fix, and
[GLOSSARY.md](../GLOSSARY.md) fixes the meaning of each word used here.

## Contents

- [The config file](#the-config-file)
  - [`required`](#required)
  - [`defaults`](#defaults)
- [Checks](#checks)
  - [`generated`](#generated)
  - [`witness`](#witness)
- [Rules](#rules)
  - [`compare`](#compare)
  - [`assert`](#assert)
- [Extractors](#extractors)
  - [A set of files](#a-set-of-files)
  - [A root below the top](#a-root-below-the-top)
  - [Paths](#paths)
  - [Filters](#filters)
  - [Values](#values)
  - [Values a file only computes](#values-a-file-only-computes)
  - [Several YAML documents](#several-yaml-documents)
  - [Documents in strings](#documents-in-strings)
  - [Mappings whole](#mappings-whole)
  - [Records](#records)
- [Extractor kinds](#extractor-kinds)
  - [`yaml`](#yaml)
  - [`json`](#json)
  - [`jsonc` and `json5`](#jsonc-and-json5)
  - [`toml`](#toml)
  - [`regex`](#regex)
  - [`text`](#text)
  - [`comment`](#comment)
  - [`literal`](#literal)
  - [`code`](#code)
  - [`markdown`](#markdown)
  - [`asciidoc`](#asciidoc)
  - [`rst`](#rst)
  - [`html`](#html)
  - [`marker`](#marker)
  - [`xml`](#xml)
  - [`ini`](#ini)
  - [`properties`](#properties)
  - [`tool_version`](#tool_version)
  - [`package_version`](#package_version)
  - [`dependency`](#dependency)
  - [`envfile`](#envfile)
  - [`gitlab-ci`](#gitlab-ci)
  - [`shell`](#shell)
  - [`github-actions`](#github-actions)
  - [`jenkins`](#jenkins)
  - [`etc`](#etc)
  - [`systemd`](#systemd)
  - [`env`](#env)
  - [`exists` and `size`](#exists-and-size)
- [Limits](#limits)
- [When a rule is unknown](#when-a-rule-is-unknown)
- [The check protocol](#the-check-protocol)
- [Scopes](#scopes)
- [Command line](#command-line)
- [Listing the checks](#listing-the-checks)
- [Output formats](#output-formats)
  - [The text report](#the-text-report)
  - [JUnit XML](#junit-xml)
  - [SARIF](#sarif)

## The config file

errata reads `errata.yaml` at the repository root, or the file `--config` names
([Command line](#command-line)). It has three top-level keys:

| Key | Required | Meaning |
|---|---|---|
| `checks` | yes | the list of checks, each a mapping of the [check keys](#checks) |
| `required` | no | the id of every check the file must hold: see [`required`](#required) |
| `defaults` | no | the `run` a program check takes when it sets none: see [`defaults`](#defaults) |

Keys are matched exactly: `Agree` is refused, not read as `agree`. The file is one YAML document;
a second `---` does not load, since the checks after it would never run. errata counts the
documents with a YAML decoder, so a `---` after a CR, NEL, LS or PS line break, or in a UTF-16
file, counts too. An empty document at the end, such as the one a last `---` makes, holds no check
and is ignored.

errata reads the file as YAML 1.1, where an unquoted `y`, `yes`, `on`, `no` or `off` is a bool,
`1e3`, `007`, `0x10` or `.inf` a number, and `~` null, and a key that takes a string gets the bool,
number or null back as a string: `true`, `1000`, `7`, `16`, `+Inf`, the empty string. A key that
takes a string and is written as an unquoted value that comes back as a string other than the text
written does not load: the error names the check, the key and the text, and says to quote it.
`id: y` would otherwise be the id `true`, and `run: [ls, .inf]` would pass `+Inf`. Write
`id: "y"` and `run: [ls, ".inf"]`. A value that comes back as written loads, so
`run: [head, -n, 5]`, `true` and `20.04` need no quotes. Every key that takes a string is held to
this, the extractors' names included.

### `required`

A config of fewer checks than its author meant loads and passes: delete an entry, and a CI job
that runs errata stays green without it. The top-level `required` key lists the id of every check
the config must hold:

```yaml
required:
  - lint-passes
  - version-agrees
checks:
  - id: lint-passes
    ...
  - id: version-agrees
    ...
```

With `required` set, the config loads only when `required` lists exactly the ids of its checks.
An id under `required` that no check has, and a check whose id `required` does not list, each
stop the load: `errata check` exits 2 before it runs anything, and the message names every such
id. An empty or null `required`, and an id listed twice, do not load either. An id under
`required` is held to the quoting rule above, as `id` is. A config without the key loads any
number of checks, as before. `required` says which checks the config holds, not which ones run:
a check whose scope the run cannot reach is still skipped, with its gap in the coverage.

The list sits in `errata.yaml` itself. Written as above, one id a line, the change that deletes a
check deletes its id's line under `required` in the same file and the same diff, and the change that
deletes `required` deletes a line for every check. The flow form, `required: [a, b]`, loads the
same, but it hides either deletion in one changed line, so write the list one id a line. A list in a
file of its own, named by `errata.yaml`, would be switched off by deleting the one line that names
it, with the list file left as it was. errata cannot stop an author who edits both the check and the
list; a reviewer sees both. A gate that must not rest on review keeps the list where the author's
change cannot reach it without an approval: a code owner on `errata.yaml`, or a CI job that compares
the `required` that `errata list --format json` prints with a list in a file only that job's owners
may change.

### `defaults`

Program checks often run one program each with their own id, such as a script that takes the id
of the check to run. The top level `defaults` key writes that command once:

```yaml
defaults:
  run: [python3, scripts/check-invariants.py, --only, <errata.id_underscored>]
checks:
  - id: no-latest-tags
    reason: An image tagged latest changes under a pinned deploy.
    scope: repo
```

A check with no `run` and no rule key is a program check and runs `defaults.run`, with its
placeholders replaced: `no-latest-tags` runs `python3 scripts/check-invariants.py --only
no_latest_tags`. A check with a `run` of its own keeps it, and a rule takes no default. Without
`defaults.run`, a check with neither `run` nor a rule key does not load, as before.

| Placeholder | Becomes |
|---|---|
| `<errata.id>` | the check's id: `no-latest-tags` |
| `<errata.id_underscored>` | the id with each hyphen turned into an underscore: `no_latest_tags` |

errata replaces a placeholder inside one argument and never through a shell, so `--only=<errata.id>`
and `checks/<errata.id>.sh` work, and an argument stays one argument. An id is lower-case letters,
digits and single hyphens, so the text it puts in holds no space, slash, dot or leading hyphen: it
adds no argument and no option, and leaves no directory. A check's own `run` takes the same
placeholders. Any other text that starts with `<errata.`, in any case, such as a misspelt
placeholder, does not load, and neither does a program check's `run` or a `defaults.run` that lists
no argument. The placeholders need no quotes in YAML. A program that reads the id from
`ERRATA_CHECK_ID` needs no placeholder. `errata list` prints each check's `run` with the
placeholders replaced, as the command errata starts.

A default `run` is a program errata starts with the job's environment, secrets included, as a
check's own `run` is. [Checks](#checks) says what that means on a ref you do not trust.

## Checks

A check is a program check, a rule or a [generated check](#generated). A program check has
`run`, or takes the file's [default run](#defaults); a rule has `extract`; a generated check
has `generated`. A check with two of them does not load, and neither does a check with none when
the file sets no default run. A file with no checks does not load
either: it would run nothing and pass.

A program check is code. Its `run`, or the [default run](#defaults) it takes, is a program
errata starts, and so is a generated check's generator. Each runs from the repository root with
the environment of the job that runs errata, secrets included, plus the variables
[the check protocol](#the-check-protocol) names. A rule's extractors read files and start
nothing. On a ref you do not trust, such as a merge request from a fork, the config's
programs, its default `run` and its generators are that ref's, and so are the scripts they start.
Run errata there in a job without secrets, or with a config of rules only that a ref you trust
holds, named with `--config`: the ref's own `errata.yaml` can add a `run`.

Every check has these keys:

| Key | Required | Meaning |
|---|---|---|
| `id` | yes | lower-case words joined by single hyphens, unique in the file |
| `reason` | yes | why the check exists and what breaks without it |
| `scope` | yes | `repo`, `live` or `applied`; a rule and a generated check are always `repo` |
| `run` | program check, unless the file sets a [default run](#defaults) | the command, as a list of arguments, run from the repository root |
| `timeout` | no | program check and generated check: a positive Go duration such as `10s` or `2m`; default `30s`. Zero or less does not load |
| `generated` | generated check | the committed file and its generator: see [`generated`](#generated) |
| `extract` | rule | the rule's extractors, each under a name of its own that is a CEL identifier (see [assert](#assert)) |
| `agree` | rule: this or `assert` | the extractors whose values must all agree |
| `compare` | no | agree only: how the values must agree, `text` (the default) or `semver` (see [`compare`](#compare)) |
| `syntax` | no | `compare: semver` only: the ecosystem whose rules read the values, `npm`, `cargo`, `pep440`, `go` or `rubygems`; unset, errata reads all five (see [`compare`](#compare)) |
| `assert` | rule: this or `agree` | a [CEL](https://cel.dev) expression over the extracted values that must be true |
| `allow_empty` | no | rule only: the extractors that may find no value, such as `[client]` |
| `skip_not_literal` | no | rule only: the extractors whose not literal values are left out, such as `[php]`; only an extractor that can return a not literal value may be listed (see [Values a file only computes](#values-a-file-only-computes)) |
| `normalize` | no | rule only: `trim`, `lower` or both, in order, applied before `agree` compares or `assert` sees the values |
| `witness` | no | scope `repo` only: one to eight fixtures under `.errata/witness/` on which the check must report false, such as `[toolchain-drifts]`: see [`witness`](#witness) |

### `generated`

A file the repository commits and a program generates, such as a public copy of a document with
private sections stripped, must be what the program prints today. A generated check names the
file and the generator:

```yaml
checks:
  - id: architecture-public-copy-is-current
    reason: architecture.md is handed to people, and is generated from architecture-full.md.
    scope: repo
    generated:
      file: architecture.md
      run: [python3, scripts/build-architecture.py, --stdout]
```

errata reads the file, then runs `generated.run` and compares what it prints on stdout with the
file as it was read, byte for byte.
When they differ, the check is an erratum that names the first line and column where they part,
and quotes each side from a little before that column to the end of the line:

```text
FALSE architecture-public-copy-is-current (repo)
  why:  architecture.md is handed to people, and is generated from architecture-full.md.
  said: architecture.md:15, column 7, differs from what its generator prints: the file has "## 1. The shape of it\n", the generator printed "## 1. Planted The shape of it\n"
```

The text and JUnit reports carry the place in that message, and SARIF puts the result on that
line of the file.

| Key | Required | Meaning |
|---|---|---|
| `generated.file` | yes | the committed file, relative to the repository root and inside it, as an extractor's `file` |
| `generated.run` | yes | the generator, as a list of arguments, run from the repository root; it takes the placeholders of a [default run](#defaults) |
| `generated.max_bytes` | no | the most bytes errata reads of the file and holds of the generator's stdout, and of its stderr apart: from 1 to 67108864 (64 MiB), default 33554432 (32 MiB) |

The generator runs as a program check does, through the same code: argv and never a shell, the
check's `timeout`, the environment the [check protocol](#the-check-protocol) gives, and on Linux
and macOS its own process group, which errata kills as the check protocol says. Its stdin is empty,
so a generator that reads stdin reads nothing and goes on. errata compares the output and never
runs it.

| What happened | Verdict |
|---|---|
| the output equals the file | true |
| the output differs from the file in any byte, a CR before a newline and the last newline included | false, at the first line that differs |
| nothing is at `generated.file` | false: the generator printed a file the repository lacks. On a [witness](#witness) the witness is unknown instead, and the generator does not run |
| the generator exits with any status but 0, exit 1 included, or cannot be started | unknown, with the end of its stderr |
| the generator runs past `timeout`, or writes more than `max_bytes` to stdout or to stderr | unknown; on Linux and macOS errata kills its process group |
| the file is over `max_bytes`, a directory, or a link that leads out of the repository | unknown |

Line ends are compared as they are: a file of CRLF lines differs from a generator that prints LF.
A generator that only writes its file needs a mode that prints it and writes nothing. Do not
wrap it as `make docs && cat docs/api.md`: errata reads the file before the generator starts,
so the check still fails on a stale file, but the generator has rewritten the file in the
working tree, and every check that runs after it reads the new one. A generated check takes
no default run, and a `run` or a rule's key beside `generated` does not load. Its scope is `repo`,
so `--fail-on-skip` never sees it skipped, and `required` lists it by its id as any other check.
[ADR 0011](adr/0011-a-generated-check-runs-its-generator-and-compares-what-it-prints.md)
says why errata runs a generator although rules never run anything
([ADR 0002](adr/0002-errata-reads-configuration-and-never-runs-it.md)), and argues each
verdict above.

### `witness`

A check that cannot fail passes on every repository, and a suite of such checks goes green on
nothing: a regex that never matches under `allow_empty`, an assert that is true of any list, a
program that reads the wrong directory. A witness shows that a check can fail. The check names a
fixture, a small tree the repository commits under `.errata/witness/`, with a break planted in it,
and every `errata check` runs the check on that fixture as well and requires it to report false:

```yaml
checks:
  - id: go-toolchain-agrees
    reason: The module and the image build with one Go.
    scope: repo
    extract:
      mod: {regex: {file: go.mod, pattern: '(?m)^go (\S+)'}}
      image: {regex: {file: Dockerfile, pattern: 'golang:(\S+)'}}
    agree: [mod, image]
    witness: [toolchain-drifts]
```

```text
.errata/witness/toolchain-drifts/go.mod        go 1.21
.errata/witness/toolchain-drifts/Dockerfile    FROM golang:1.22
```

A fixture holds only the files the check reads, laid out as in the repository. `witness` lists
one to eight fixture names, each lower-case words joined by single hyphens, so a check with three
branches can plant a break in each; checks may share a fixture. It loads only on a check of scope
`repo`, since files cannot stand in for a live system.

Each witness runs after its check, whatever the check's verdict, and adds a result of its own,
named `id [witness name]` in the text report and in JUnit:

| The check on its fixture | The witness |
|---|---|
| false | holds; the coverage line counts it, as `1 witness held.` |
| true | an erratum: `go-toolchain-agrees held on its witness toolchain-drifts, so it could not be shown to fail`. SARIF puts it at the check in the config |
| unknown, or the fixture is not a directory, or is reached through a symlink, wherever it leads, or git's index lists no file below it, or lists one below its path spelt in another case | unknown, which fails the run |

The exit codes do not change. A clone in CI holds only the files git's index lists, so every kind
of check needs the repository's git index on a witness, a program check's included: a fixture the
index lists no file below makes the witness unknown before its check runs. A rule's reads of the
fixture (a `file:`, a glob, `exists` and a generated check's `generated.file`) then find only the
files the index lists below it, so a file the fixture does not commit reads as not there, as in a
clone. A walk, such as `tool_version`'s without `files:`, and `package_version`'s look at the
root list what the index lists below the fixture and never the disk, so neither finds an
untracked file. A file, a symlink or a directory the index lists that the disk does not hold as
one, such as a committed file deleted from the working tree, is unknown, since a clone holds it,
as ADR 0008 has it for a glob. A read through a committed directory the disk lacks is unknown,
whatever lies below it, even a path the fixture never committed. A symlink on the way is followed
only where the index lists it as a symlink: a clone checks out a file where the index lists one,
so a link put in its place on disk leads nowhere a clone's path does. A file on disk where the
index lists a symlink is unknown, since a clone follows the link there. An index entry below
`.Errata/witness/w` makes the witness `w` unknown on every OS, since macOS and Windows open that
directory as `.errata/witness/w`. Each kind of check runs on the fixture this way:

- A rule reads the fixture as its root. A `files:` glob matches the files git's index lists
  below the fixture, with the fixture's path cut off, so a file the fixture does not commit is
  not read, as in CI. An index entry with a `.` or `..` name or an empty one, which git does not
  write, is refused on every run, a real run's included: `a/../../x` below the fixture is
  outside it once cut, and `.errata/./witness/w/x` names a fixture's file that a real run would
  not pass over. A `file:` names a path inside the fixture, and a symlink that leads out of it
  is refused, as one that leads out of the repository is. errata opens the fixture's directory
  when it checks that no symlink leads to it, and the rule reads every file, walk and listing
  through that open directory, so a symlink put at `.errata`, `.errata/witness` or the fixture
  while the run is under way moves no read.
- A program check starts in the repository root, as on any run, so `[python3, scripts/x.py]`
  finds its script, and `ERRATA_ROOT` is the fixture's absolute path. A program must read the
  tree from `ERRATA_ROOT`. It reads by that path, so the directory errata checked does not bind
  it as it binds a rule: a symlink put in its way during the run moves its reads. It need not be
  bound, since it runs the repository's code with the job's rights. One that finds the tree from
  its working directory or its own path reads the repository, holds there, and its witness is an
  erratum, which is the point: a check that never reads the tree it is handed cannot be shown to
  fail. A program reads its fixture from the disk, not through git's index, so a file the
  fixture does not commit is read on a local run and is missing in CI: commit every file the
  program reads.
- A generated check reads `generated.file` from the fixture and runs its generator from the
  repository root with `ERRATA_ROOT` at the fixture. A fixture that holds nothing at
  `generated.file` makes the witness unknown without running the generator: on the repository
  the missing file is an erratum, which would hold the witness, but the fixture left the file
  out instead of planting a break in it, as a rule's file that is not there is unknown. The
  generator reads the fixture from the disk, as a program does, so commit every file it reads.

A witness runs under the check's own `timeout` and bounds, so each fixture costs one more run of
its check; a check without one costs nothing more. The real run passes over `.errata/witness/`:
a glob leaves its files out and the `tool_version` walk does not enter it, so a `**/*.yaml` does
not read a planted break, while a `file:` that names a path inside still reads it. It passes
over `.Errata/Witness/` and any other case of the two names too, on every OS, since macOS and
Windows open that as `.errata/witness/` and run its fixtures as witnesses. Other tools
do not know the tree: a dependency bot such as Renovate updates a `go.mod` or a `Dockerfile` in a
fixture as in the repository, and can bump both sides of a planted break into agreement, so list
`.errata/witness/` among the paths it ignores.
[ADR 0015](adr/0015-a-check-names-witnesses-on-which-it-must-report-false.md) says why the
witnesses run on every `errata check` and why a fixture is a tree of the repository.

## Rules

A rule names extractors and states how their values relate. This one says that the S3 endpoint in
a Service and the one in the client's config are the same host:

```yaml
checks:
  - id: s3-endpoint-agrees
    reason: >
      Clients read the endpoint from their config. If it stops naming the Service's address,
      every upload fails while the Service itself stays healthy.
    scope: repo
    extract:
      service: {yaml: {file: deploy/service.yaml, path: spec.clusterIP}}
      client:  {regex: {file: config/client.yaml, pattern: 'endpoint: "([^:]+):'}}
    agree: [service, client]
```

`agree` holds when every value from every named extractor has the same string form, or with
`compare: semver` the same version (see [`compare`](#compare)). It names
only extractors the rule declares, each once, and every extractor the rule declares, so none is
left out of the comparison by mistake. For an extractor that returns records, it names one field
of them, as `name.field` (see [Records](#records)). When the values differ, the message lists each distinct
value with every `file:line` it was found at:

```text
FALSE s3-endpoint-agrees (repo)
  why:  Clients read the endpoint from their config. If it stops naming the Service's address, every upload fails while the Service itself stays healthy.
  said: agree: the values differ
          "10.43.0.5" at deploy/service.yaml:12 (service)
          "s3.internal" at config/client.yaml:3 (client)
```

`normalize: [trim, lower]` compares the values with surrounding white space removed and in lower
case; the message shows them in that form. A rule has `agree` or `assert`, never both: one check
tests one relation, so two relations are two checks.

A rule fails closed. An extractor that cannot read its source makes the rule unknown, with the
extractor's error in the message. So does an extractor that finds no value, unless `allow_empty`
lists it, and one that returns a not literal value, unless `skip_not_literal` lists it (see [Values
a file only computes](#values-a-file-only-computes)). An agree rule that has fewer than two values
to compare is unknown too, so `agree: [tags]` over a regular expression with one match is unknown
and over two matches compares them. The exception is an agree rule where an extractor that
`allow_empty` lists came back empty: one value left is true. With no value at all an agree rule is
unknown, even when `allow_empty` lists every extractor. An assert rule has no such floor: an
extractor that `allow_empty` lists and that finds nothing is an empty list, and the expression
decides, even when every extractor came back empty. Two extractors that read the same place, or a
name listed twice in `agree` or `allow_empty`, do not load. The same place is the same file,
however its path is spelt (`f`, `./f` and `d/../f` are one file), read by the same kind with the
same fields; a different `max_bytes` does not make a second place. An INI entry is one place
however the spec spells it, as its tool reads it: `section: remote, subsection: origin` and
`section: remote.origin` are one git entry. The load error names each extractor that reads a place
an extractor before it in name order reads, beside the first one, at most 8 of them, and then
counts the rest. A rule takes no `run` and no `timeout`: one that writes either does not load,
whatever the value, an empty or null `run` included. Its extractors read files inside errata
rather than running a program, and its scope is `repo` for the same reason.

### `compare`

`compare: semver` makes `agree` read every value as a version or a version range, so files that
state one version at different precision agree:

```yaml
checks:
  - id: node-agrees
    reason: CI, the image and local shells must run one Node release line.
    scope: repo
    extract:
      nvmrc:      {regex: {file: .nvmrc, pattern: '(.+)'}}
      dockerfile: {regex: {file: Dockerfile, pattern: 'FROM node:([0-9.]+)'}}
      engines:    {json: {file: package.json, path: engines.node}}
    agree: [nvmrc, dockerfile, engines]
    compare: semver
```

Versions agree at the precision each states. A version of one or two numbers, such as `20` or
`v20.11`, stands for every release that starts with them; a version of three numbers or more, or
with a pre-release, stands for itself. So `20`, `20.11.1` and `v20.11` agree, and `20` and
`18.19.0` do not. A range agrees when every version among the values satisfies it: `20.11.1`
agrees with `>=20` and with `^20.1`. A version of one or two numbers satisfies a range when some
release it stands for does, so `20` satisfies `^20.1`. That reading suits a floating version, such
as `node:20` or `.nvmrc` with `20`, which installs the newest release. It does not suit go.mod's
`go 1.21`, which is Go's language version 1.21 and still satisfies `>=1.21.4` here: set
`syntax: go` (below), or write the third number where the lowest release is meant. When no value
is a version, the ranges
agree when one release satisfies them all, and the text output says under the coverage line that
only ranges were compared:

```text
0 errata, 0 unknown. Coverage: ran 1, skipped 0.
  note on engines-agree (repo): agree: only ranges were compared, since no value is a version, and they overlap
```

When the values do not agree, the message names the first pair that does not, then lists each
value with every place it was found at and what errata read it as. A bound written with `-0`, as
in `<23.0.0-0`, lies below every pre-release of that version, as npm writes it:

```text
FALSE node-agrees (repo)
  why:  CI, the image and local shells must run one Node release line.
  said: agree: the version "18.19.0" does not satisfy the range ">=20 <23"
          "18.19.0" (version 18.19.0) at .nvmrc:1 (nvmrc)
          ">=20 <23" (range >=20.0.0 <23.0.0-0) at package.json:3 (engines)
```

A value that reads as neither a version nor a range, such as `lts/iron` or `latest`, makes the
rule unknown, and the message names the value with the reason. errata never compares it as text.
`normalize` applies before a value is read. A value of more than 1024 bytes is neither a version
nor a range. After the numbers, a version holds a pre-release after `-`, or one of the words `a`,
`alpha`, `b`, `beta`, `c`, `rc`, `pre`, `preview`, `post`, `rev`, `r` and `dev` in any case, so
`10ms`, `1st` and `20H2` are not versions. A value that is only a wildcard, such as `*` or `x`, is the range of
every version, and `vx` is neither.

The ranges of one rule may hold at most 1000000 intervals in all, each alternative of a range, each
piece a `!=` leaves and each release a comparator names a pre-release of counting as one. errata
counts them as it reads the values, with a version among them or not, and the value that passes the
bound makes the rule unknown, naming the bound, before any value after it is read. Real ranges hold
one to a few. On a build machine, in a pod of four cores, with 100000 distinct values of 1024 bytes
each, the most one extractor returns:

| Values | Time | Allocated |
|---|---|---|
| chains of `!=`, the most intervals for their length, refused at the bound after about 5000 | 1.2 s | 2.1 GB, most of it freed at once |
| as many ranges of about 140 alternatives as the bound allows, compared with each other | 2.9 s | 0.7 GB |
| ranges of about 140 comparators that fold into one interval, beside one version | 6.0 s | 3.2 GB, most of it freed at once |
| ranges of about 90 comparators that each name a pre-release of another release, beside one version, refused at the bound after about 11000 | 1.3 s | 1.0 GB |
| ranges of about 90 comparators that name pre-releases of one release, which errata keeps once, beside one version | 6.6 s | 3.4 GB, most of it freed at once |

The bounds hold under every `syntax` (below). Each syntax's own shapes of the same 100000 values
took no longer than the shapes above, on the same machine in the same run:

| `syntax` | Values | Time | Allocated |
|---|---|---|---|
| `npm` | ranges of about 140 alternatives, refused at the bound | 1.2 s | 0.9 GB |
| `npm` | ranges that fold into one interval, beside one version | 6.1 s | 3.3 GB |
| `npm` | comparators that each name a pre-release of another release, refused at the bound | 1.3 s | 1.1 GB |
| `cargo` | requirements that fold into one interval, beside one version | 6.1 s | 3.0 GB |
| `cargo` | comparators that each name a pre-release of another release, refused at the bound | 1.2 s | 1.0 GB |
| `pep440` | chains of `!=`, refused at the bound | 1.0 s | 1.7 GB |
| `pep440` | specifiers that fold into one interval, beside one version | 4.8 s | 2.2 GB |
| `pep440` | chains of `!=` with a local version, refused at the bound | 2.3 s, on another machine (below) | 1.9 GB |
| `go` | ranges of about 120 alternatives, refused at the bound | 0.9 s | 0.8 GB |
| `go` | ranges that fold into one interval, beside one version | 2.6 s | 1.4 GB |
| `rubygems` | chains of `!=`, refused at the bound | 0.8 s | 1.6 GB |
| `rubygems` | requirements that fold into one interval, beside one version | 3.4 s | 0.7 GB |

The process peaked at 951 MiB across the seventeen. The row for local versions was measured again
after local versions came to be ordered, on another machine, in a pod of three cores, where the
unset chains of `!=` took 2.2 s and 2.1 GB in the same run and the process peaked at 923 MiB.
`compare: text`, the default, compares the string form, as above.

errata reads the version and range syntax of five ecosystems:

| Ecosystem | Versions | Ranges |
|---|---|---|
| npm | `1.2.3`, `v1.2.3`, `1.0.0-rc.1`, `1.0.0+build.5` | `^1.2`, `~1.2.3`, `>=20 <23`, `>= 20`, `1.2.x`, `*`, `^1 \|\| ^3`, and a hyphen range: two versions with a spaced hyphen between them, the second at its precision |
| Cargo | as npm | `^1.2.3`, `~1.2`, `=1.2.3`, `>=1.2, <1.5`, `1.*` |
| PEP 440 | `3.12.0rc1`, `1.0a1`, `1.0.post2`, `1.0.dev0`, `1!2.0`, `1.0+ubuntu.1` | `~=2.2`, `==1.2`, `==1.2.*`, `!=1.5`, `!=1.5.*`, `>=3.8, !=3.9.0, <4` |
| Go modules | `v1.2.3+incompatible`, `v0.0.0-20191109021931-daa7c04131f5`, `go1.22.1`, `go1.22rc1` | none: Go writes versions only |
| RubyGems | `3.3.0.preview1`, `1.0.0.pre`, `7.1.0.beta.2`; a word other than the ones above, such as `2.0.0.racecar1`, is refused | `~> 3.2`, `~> 3.2.1`, `~> 3.2, >= 3.2.1` |

Where the syntaxes differ, a rule that sets no `syntax` reads each form one way, and a value from
Python, Cargo or Go may then read otherwise than its tool reads it. `syntax` (below) reads it as
the tool does. Without it:

- A bare version in a range is that version at its precision, as in npm: `1.2` is every 1.2
  release. Cargo reads a bare `1.2` in a requirement as `^1.2`, so read a Cargo requirement
  through [`dependency`](#dependency), which writes the `^`.
- `>1.2` and `<=1.2` compare at the precision written, as npm and Cargo do: `>1.2` starts at 1.3.0,
  and `<=1.2` admits 1.2.9. PEP 440 and RubyGems would admit 1.2.1 above `>1.2` and refuse it under
  `<=1.2`. `==1.2` and `!=1.2` are PEP 440's, and compare with 1.2.0.
- A pre-release satisfies a range only when one of the range's comparators names a pre-release of
  the same numbers, as npm and Cargo decide: `2.0.0-beta.1` does not satisfy `>=1.0.0`, and does
  satisfy `>=2.0.0-alpha`. Two ranges overlap when they share a release; sharing only pre-releases
  is not enough.
- After `-`, a pre-release is SemVer's and orders by SemVer's rules, so `1.0.0-1` is a pre-release,
  not PEP 440's post release. Written without `-`, as PEP 440 and RubyGems write them, its words
  and numbers order apart, so `rc2` comes before `rc10`.
- A dev release comes before the pre-releases of its version, and a post release after the
  version, as PEP 440 orders them. Build metadata and local versions after `+` are never compared.
- PEP 440 spellings are read as written, not normalised. `1.0-post1` and `1.0-dev1` are SemVer
  pre-releases, where PEP 440 reads a post and a dev release. `alpha`, `beta`, `c`, `pre` and
  `preview` are not `a`, `b` and `rc`, so `1.0beta1` and `1.0b1` differ. A word is read in any case
  but compared as written, so `1.0RC1` is a version that differs from `1.0rc1`. `1.0.0-rc1` (one
  SemVer identifier) and `1.0.0rc1` (`rc` then `1`) differ.
- RubyGems accepts any word after the numbers. errata reads only the words above, so a version
  such as `2.0.0.racecar1` is neither a version nor a range, and its rule is unknown.
- `>1.7.0` admits `1.7.0.post1`, which PEP 440 leaves out of `>V` unless V is itself a post
  release.
- `===` compares text, so a value that holds it is neither a version nor a range.

`syntax` reads every value of the rule as one ecosystem documents its versions and ranges, and a
value written another way is neither, so the rule is unknown and names it. A bare value is a
version, so a requirement reaches the rule with its operator: here `dependency` hands back
Cargo.toml's `tokio = "1.35"` as `^1.35`, as Cargo reads it, which Cargo.lock's `1.40.0`
satisfies. A regex would hand back `1.35`, the partial version:

```yaml
checks:
  - id: tokio-locked
    reason: The lock resolves a tokio the manifest accepts.
    scope: repo
    extract:
      tokio: {dependency: {name: tokio, files: [Cargo.toml, Cargo.lock]}}
    agree: [tokio]
    compare: semver
    syntax: cargo
```

| `syntax` | Versions | Ranges | Read otherwise than with no `syntax` |
|---|---|---|---|
| `npm` | node-semver's: up to three numbers with no leading zero, a `v` before them, a pre-release after `-` and build after `+` only after three | node-semver's: comparators separated by spaces, sets of them joined by `\|\|`, `<`, `<=`, `>`, `>=`, `=`, `^`, `~`, x-ranges and hyphen ranges; an empty range, or an empty set beside `\|\|`, is `*`, and a number after a wildcard is one, so `1.*.3` is `1.x` | `~>1.2` is node-semver's `~1.2`, below 1.3.0, where the unset reading takes RubyGems' 2.0; commas, `==`, `!=`, `~=` and the words of PEP 440 and RubyGems are refused |
| `cargo` | up to three numbers with no leading zero, a pre-release and build after three: a bare value is a version, as `rust-version = "1.56"` is one in the Cargo book, and one or two numbers stay partial | the Cargo book's: comparators separated by commas, a bare comparator beside another read as `^`, `~`, `=`, `<`, `<=`, `>`, `>=`, a wildcard with no operator but `=`, and `*` as the only comparator | a comparator with no operator is `^`, so `1.2, <1.5` admits 1.4.0. `^1.2.3`, `~1.2` and `<2` stop below the pre-releases of the version after them, as the semver crate's `matches_caret`, `matches_tilde` and `matches_less` (src/eval.rs) hold the major, and the minor where written, so `2.0.0-alpha.1` satisfies neither `^1.2.3` nor `<2` beside `>=2.0.0-alpha.0`. `\|\|` and `v` are refused |
| `pep440` | PEP 440's, in its normal form: `1.0RC1`, `1.0c1` and `1.0-rc.1` are `1.0rc1`, `1.0-1` is `1.0.post1`, `1.0+Ubuntu-1` is `1.0+ubuntu.1` | PEP 440's specifiers: clauses separated by commas, each with an operator: `~=`, `==`, `!=`, `<=`, `>=`, `<`, `>`, and `.*` after `==` and `!=` | `>1.2` admits 1.2.1 and `<=1.2` refuses it; `>V` admits no post release of V and `<V` no pre-release of V, unless V is one; a pre-release or dev release satisfies a range only when an operator other than `!=` names one, as packaging 25.0 decides and the specification's `<V.rc1` asks; `==1.0` and `<=1.0` admit `1.0+abc`, `==1.0+abc` admits only it, and local versions order as the specification orders them, so `==2.1.0+cpu` and `==2.1.0+cu118` do not overlap. A bound written `+*` after a version lies above all its local versions |
| `go` | a module's, after `v`, where `v1.2` is `v1.2.0`; a toolchain's, with or without `go`, such as `1.21`, `1.21rc1` and `go1.21.0` | Go writes none: errata reads `<`, `<=`, `>`, `>=` and `=` before a version, separated by spaces or commas, sets of them joined by `\|\|` | every version stands for itself. `go 1.21` is the language version 1.21, below 1.21rc1 and 1.21.0, so it fails `>=1.21.4` and does not agree with `1.21.0`; before Go 1.21 the first release was 1.N, so `1.20` is 1.20.0. A pre-release satisfies a range as any version does |
| `rubygems` | Gem::Version's: a number, then any letters and digits after dots, a `-` read as `.pre.`, `1.0` equal to `1.0.0` | Gem::Requirement's: requirements separated by commas, a bare version read as `=` | `~> 3.2` stops below 4.0 and `~> 3.2.6` below 3.3; `>1.2` admits 1.2.1; `= 7.1` is 7.1 alone; a pre-release, a version with a letter, satisfies only beside a requirement that names one; any word is a pre-release, compared by its bytes, so `2.0.0.racecar1` is a version and `1.0.RC1` differs from `1.0.rc1` |

Under every syntax but `go`, a version of one or two numbers stays partial, as above: under
`pep440`, `3.11` stands for every 3.11 release, so it satisfies `>3.11`, as 3.11.1 does, and
agrees with `3.11.4`. Under `cargo`, `rust-version = "1.70"` does not agree with a toolchain
channel of `1.82`, and `tokio = "1.35.0"` read by a regex is the version 1.35.0; read the
requirement through `dependency`, which hands it back as `^1.35.0`. The message names what each
value was read as, in the syntax: `(version 1.21)`, `(range >=1.21.4)`. Under `pep440` a bound
written `.post*` after a version lies above every post release of it, where `>1.7` starts:
`(range >1.7.post*)`.
`semver_satisfies(v, r, s)` reads in the syntax `s` (see [`assert`](#assert)), and `errata list`
names a rule's syntax, `any` for one that sets none.

`assert` reads versions with the same code, through `semver()` and `semver_satisfies()` (see
below), so the two never read one value two ways. Each call costs 1 and the bytes it reads toward
the expression's cost limit.

### `assert`

`assert` states a relation that equality cannot: a derived number, an order, a set. This one says
that a backup alert's staleness threshold lies between one backup interval and two:

```yaml
checks:
  - id: staleness-threshold
    reason: >
      The alert fires when no backup finished within the threshold. Below one interval it fires
      between healthy runs; above two it stays quiet through a missed run.
    scope: repo
    extract:
      schedule:  {yaml: {file: deploy/backup-cronjob.yaml, path: spec.schedule}}
      threshold: {regex: {file: monitoring/backup-rules.yaml, pattern: 'for: (\S+)'}}
    assert: |
      duration(threshold[0]) >= cron_interval(schedule[0]) &&
      duration(threshold[0]) <= cron_interval(schedule[0]) * 2
```

Each extractor is a variable named after it, and every variable is a `list(string)`: `schedule[0]`
is the first value the extractor found. An extractor with `as: map` or `as: record` is a list of
maps instead (see [Mappings whole](#mappings-whole) and [Records](#records)). Every extractor's name is a CEL identifier,
under `agree` as under `assert`: letters, digits and `_`, not starting with a digit, and none of the
words CEL reserves, such as `in`, `null` and `true`. Any other name does not load, the empty one
included. `normalize` applies to the values before the
expression sees them. Numbers, durations and ports go through the functions below, so errata knows
the type of every part of the expression when it loads the file, except a value read from a map,
which CEL types `dyn`; a comparison of such a value with a bool, a number, null, bytes, a duration
or a timestamp does not load (see [Mappings whole](#mappings-whole)).

| Function | Gives |
|---|---|
| `int(s)`, `double(s)`, `uint(s)` | the number in `s`, by CEL's own conversions; an error names `s` |
| `duration(s)` | a duration from a Go duration (`90s`, `1h30m`, `1.5h`) or a Prometheus one (`2d`, `1w`, `1d12h`; a day is 24 hours, a year 365 days) |
| `cron_interval(s)` | the time between the runs of a five field cron schedule, as a duration |
| `calendar_interval(s)` | the time between the runs of a systemd calendar event, such as a timer's `OnCalendar=`, as a duration |
| `port_split(s)` | a Compose port such as `"127.0.0.1:8080:80/udp"` as a map with `ip`, `host`, `container` and `protocol`; the protocol is `tcp` when the string names none |
| `same_set(a, b)` | true when two lists hold the same members, in any order and with any repeats |
| `subset(a, b)` | true when every member of `a` is in `b` |
| `semver(s)` | the version `s`, which `<`, `<=`, `>`, `>=`, `==` and `!=` compare with another `semver()`, a missing number as 0, so `semver('20') == semver('20.0.0')`; `string()` writes it |
| `semver_satisfies(v, r)` | true when the version `v` satisfies the range `r`, as `compare: semver` decides (see [`compare`](#compare)) |
| `semver_satisfies(v, r, s)` | the same, reading `v` and `r` in the syntax `s`, one of `'npm'`, `'cargo'`, `'pep440'`, `'go'` and `'rubygems'`, as `compare: semver` with that `syntax` does; any other `s` is an error |
| `by_file(x)` | the values of the extractor `x` grouped by the file each came from, as a map from file to list (see [A set of files](#a-set-of-files)) |
| `renovate_match(s, p)` | true when Renovate matches the string `s` with the pattern `p`, as it reads one entry of `managerFilePatterns` or `matchPackageNames` (see [Renovate's patterns](#renovates-patterns)) |
| `s.split(sep)`, `s.split(sep, n)` | the parts of `s` between each `sep`, as a list; with `n`, at most `n` parts, the last holding the rest, and none for `n` of 0 |
| `list.join()`, `list.join(sep)` | the members of a list of strings, with `sep` between them |
| `s.replace(old, new)`, `s.replace(old, new, n)` | `s` with each `old` replaced by `new`; with `n`, the first `n` of them, and every one for a negative `n` |
| `s.charAt(i)` | the character of `s` at index `i`, counting from 0, and `''` at the end of `s` |
| `s.substring(i)`, `s.substring(i, j)` | the characters of `s` from index `i`, and with `j`, up to but not including index `j` |
| `s.indexOf(t)`, `s.indexOf(t, i)` | the index of the first `t` in `s`, from index `i` with `i`, and -1 when there is none |
| `s.lastIndexOf(t)`, `s.lastIndexOf(t, i)` | the index of the last `t` in `s`, at or before index `i` with `i`, and -1 when there is none |
| `s.trim()` | `s` without the white space at its start and end |
| `s.lowerAscii()`, `s.upperAscii()` | `s` with the letters A to Z in lower or upper case, and every other character as it is |
| `s.reverse()` | the characters of `s` in reverse order |
| `s.format(list)` | `s` with each clause replaced by the next member of `list`: `%s` a string, bytes, a number, a bool, null, a duration, a timestamp, a type, or a list or map of those, `%d`, `%f` and `%e` a number, with a precision such as `%.2f` of at most 100, `%x` and `%X` hexadecimal, `%o` octal, `%b` binary, and `%%` a `%` |
| `strings.quote(s)` | `s` as a CEL string literal in double quotes, with `\`, `"`, and `\a`, `\b`, `\f`, `\n`, `\r`, `\t` and `\v` escaped |

`same_set` and `subset` compare lists of strings, ints, uints, doubles or bools, so
`same_set(ports.map(p, int(p)), [80, 443])` works. A duration times a whole number is a duration.
The rest of CEL's standard library is there too: `size`, `in`, `contains`, `startsWith`,
`matches`, and the macros `all`, `exists`, `exists_one`, `map` and `filter`. `cel.bind(name,
value, expr)` names `value` in `expr`, so an intermediate value is written once. The timestamp
accessors that take a time zone, such as `getHours('Europe/Berlin')`, read `'UTC'`, fixed offsets
such as `'+01:00'`, and IANA zone names. errata carries its own copy of the IANA time zone
database (release 2026c in this version) and never reads the zone files, `ZONEINFO` or `TZ` of
the machine it runs on, so a check gives the same verdict on a laptop and in the release image,
which has no zone files. Zone rules change only when you upgrade errata. A name the database does not
hold, such as `'Local'`, makes the rule unknown.

A helper reads a string of at most 1024 bytes; a longer one makes the rule unknown, naming the
limit. The longest real arguments are far shorter: about 270 bytes for a cron schedule that lists
every minute, every hour and every day by name.

`split`, `join`, `replace` and the functions below them are cel-go's strings extension, with its
cost model, so a list written one way in one file and another way in a second compares as one
list: `buckets[0].split(',') == loop[0].split(' ')` holds for `loki,mimir` and `loki mimir`. An
empty `sep` splits `s` into its characters, and an empty `old` inserts `new` before each character
and at the end. A character is a Unicode code point, so `é` written as `e` and a combining accent
is two characters, which `reverse` puts in the other order, and each byte that is not valid UTF-8
is one. An index counts characters, not bytes. `charAt` or `substring` past the end of `s` makes
the rule unknown. They read strings of any length, since CEL charges them by what they read and
build: a tenth for each character read, a tenth for each character of `s` times each of `t` that a
search compares, an empty `t` counting as one, and one for each character of a result. errata
charges `format` and `strings.quote` for their result too, and a search for an empty `t`, which
cel-go itself does not.

Each call that the cost limit would stop by itself once it returned makes the rule unknown before
it runs, naming the bound:

- `split`, `join`, `replace`, `format` and `strings.quote`: a result of more than 4000000 bytes, or
  a split into more than 1000000 parts. `v[0].replace('', v[0])` over a value of 3000 bytes would
  build 9006000 bytes, and is refused. `format` counts the most each clause could write: a number
  counts 512 bytes, and `%x` two for each byte of a string.
- `lowerAscii`, `upperAscii`, `reverse` and `substring`: a result of more than 1000000
  characters.
- `charAt`, `substring` and `trim`: a string of more than 10000000 characters.
- `indexOf` and `lastIndexOf`: a string whose characters, times those of `t`, are more than
  10000000. An empty `t` counts as one character.

A refused call costs what CEL charges for its arguments, so refusals in a comprehension use up the
cost limit as the calls would, and the message then names the first refusal:
`the evaluation exceeded the cost limit of 1000000; before it, charAt: the call would read ...`. A
smaller call can still exceed the cost limit, and the cost limit then stops the evaluation once the
call returns. The rule is unknown either way. Apart from `format`'s count, a call the limit would
let through is never refused.

`cron_interval` reads the minute, hour and day of week fields: numbers, `*`, ranges, lists, steps
such as `*/15` and `5/15`, day names, and Jenkins' `H` and `H/N`. A step past the field's range
selects only its first value, as cronie and Kubernetes read it, so `*/90 * * * *` runs once an
hour. Day of month and month must be
`*`, because months differ in length. `@hourly`, `@daily`, `@midnight` and `@weekly` are read as
cron writes them. A schedule whose runs are not evenly spaced, such as `30 5 * * 1-5`, has no
interval, and the rule is unknown.

`calendar_interval` reads a calendar event as systemd 257 does (systemd.time(7)): weekdays such
as `Mon..Fri` or `Sat,Sun`, a date, a time of hours, minutes and seconds, each written as a
value, `*`, a range `a..b`, a repeat `a/n` or `a..b/n`, or a list of these, and a time zone at the
end, `UTC` or an IANA name such as `Europe/Berlin`. Seconds take a fraction, as in `*:*:0/0.5`.
`minutely`, `hourly`, `daily` and `weekly` are read as systemd writes them. The year, the month
and the day of the month must be `*`, since months differ in length, so `monthly`, `quarterly`,
`yearly` and `*-*-01` have no interval. The runs are evenly spaced when the finest field that
holds more than one value spaces its values evenly and every coarser field is `*`:
`*:0/15` runs every 15 minutes, `00/6:15` every 6 hours, `Mon 03:00` every week. `Mon..Fri
09:00`, which skips the weekend, has no interval, and neither has `*:0/25`, whose last run of an
hour is 10 minutes before the next hour's first. A field whose components take more than 60000
steps in all, such as a second split finer than a thousandth, is not read; a value two
components both take counts twice. A call costs a unit for each byte of the event and each step,
toward the cost limit. The interval is that of a clock without daylight
saving changes, so the zone does not change it. An event systemd refuses, and `@` with a time in
seconds, which is one moment, make the rule unknown. The names of the machine's own zone, which
systemd also reads, are read only where the IANA database holds them, as it holds `CET`.

When the expression is false, the message shows it with every value each extractor found, by the
index the expression reads it at:

```text
FALSE staleness-threshold (repo)
  why:  The alert fires when no backup finished within the threshold. Below one interval it fires between healthy runs; above two it stays quiet through a missed run.
  said: assert: the expression is false
          expression: duration(threshold[0]) >= cron_interval(schedule[0]) &&
                      duration(threshold[0]) <= cron_interval(schedule[0]) * 2
          schedule[0] = "17 */6 * * *" at deploy/backup-cronjob.yaml:9
          threshold[0] = "13h" at monitoring/backup-rules.yaml:14
```

errata type checks the expression when it loads the file. A name that is not one of the rule's
extractors, a type error such as comparing a list with a string, a result that is not a bool and a
syntax error each stop the load with exit code 2, naming the check and pointing at the column.
Here the first line of the assert above reads `treshold`:

```console
$ errata check -c errata.yaml
errata: load errata.yaml: checks[0]: check "staleness-threshold": assert: undeclared reference to 'treshold' (did you mean 'threshold'?), at line 1 column 10 of the expression:
    duration(treshold[0]) >= cron_interval(schedule[0]) &&
             ^
```

The error names the config as `-c` gave it. A plain `errata check` names it by its absolute path.

So does an extractor that the expression never uses, since the rule would then hold or fail
without testing the place it was added for.

`assert` fails closed, as `agree` does. An extractor that cannot read its source, that finds no
value while `allow_empty` does not list it, or that returns a not literal value while
`skip_not_literal` does not list it, makes the rule unknown before the expression runs. An
extractor that `allow_empty` lists and that finds nothing is an empty list.

Any error while the expression runs makes the rule unknown, with the error and the values: an
index past the end of a list, `int("${PORT}")`, a duration that does not parse. That holds even
where CEL alone would let `&&`, `||`, `exists` or `all` decide around the error, so
`ports.exists(p, int(p) == 443)` over `["${HTTPS_PORT}", "443"]` is unknown, not true.

`exists`, `all` and `exists_one` run their predicate on every member, so the verdict does not
depend on the order of the values: the same expression over `["443", "${HTTPS_PORT}"]` is unknown
too. `&&` and `||` run left to right and stop when the left side decides, and a side that does not
run is not an error. Write the case the expression should skip as a guard on the left:
`ports.exists(p, p.matches('^[0-9]+$') && int(p) == 443)`,
`ports.filter(p, p.matches('^[0-9]+$')).exists(p, int(p) == 443)`, or
`size(v) > 0 && int(v[0]) > 0`. All three are true over either order.

An expression may be 4096 characters long and nest 32 levels deep, where each term of a `+`
chain, each `?:` and each `.call` is a level; a longer or deeper one does not load, with the limit
named. One evaluation may spend a CEL cost of 1,000,000, the budget Kubernetes gives one CRD
validation rule, and may run for 5 seconds; an expression that exceeds either is unknown, with the
limit named. The cost is CEL's own, plus 1 for each side of an `&&` or `||` that runs: errata
reads its error log there. `v.all(x, x != '')` over 1000 values costs 4002 in both, and
`v.all(x, x != '' && x != 'a' && x != 'b' && x != 'c')` costs 16002 where plain CEL charges
10002. An `exists` that plain CEL would stop at its first match costs every member here.

The time a comprehension takes grows with the square of its members, because CEL's cost
tracking scans a stack once per member. `v.all(x, x != '')` takes about 5 ms over 1000 values,
1 s over 16000 and 4 s over 32000; over 64000 it reaches the 5 second limit and the rule is
unknown, well before the cost limit. The limit is checked after every step of a comprehension,
so an evaluation stops within one step of it. With these limits neither loading nor checking an
expression in a repository's configuration can stall CI.

#### Renovate's patterns

`renovate_match(s, p)` answers what Renovate's `matchRegexOrGlob` answers, the function behind
`managerFilePatterns`, `matchPackageNames` and the other keys Renovate's documentation calls string
pattern matching. `*` matches every string. An entry that starts with `/` or `!/` and ends with `/`
or `/i` is a regex, searched anywhere in `s`, with the `i` flag after `/i` and the answer inverted
after `!`. Any other entry is a minimatch glob, with dotfiles matched and case ignored. So an
entry that lost its slashes is a glob that matches next to nothing, in errata as in Renovate. This
rule holds every Argo CD Application file to Renovate's argocd manager:

```yaml
extract:
  patterns: {json: {file: renovate.json, path: 'argocd.managerFilePatterns[*]'}}
  apps: {regex: {files: ['infra/argocd/applications/*.yaml'], pattern: '\A([\s\S])'}}
allow_empty: [patterns]
assert: size(patterns) > 0 && by_file(apps).all(f, patterns.exists(p, renovate_match(f, p)))
```

errata reads a regex with Go's RE2, as Renovate reads it with the `re2` module, and a regex RE2
does not compile, such as a lookahead, makes the rule unknown. It reads a glob with a port of the
minimatch Renovate pins, so braces, extglobs such as `+(a|b)`, a leading `!` that negates or `#`
that comments, POSIX classes such as `[[:alpha:]]` and every other glob mean what they mean to
Renovate. A glob on which minimatch throws makes the rule unknown, since Renovate throws on it
too. So does a glob past the port's bounds: braces that expand past 64 globs, a segment whose
negations build a tree past 4096 nodes (about 10 of `!(a)`), more than 32768 characters of regex
source, and a match that takes more than 131072 steps.
[ADR 0014](adr/0014-renovate-match-reads-a-pattern-as-renovate-does.md) has the details and
the cases checked against Renovate's own code.

## Extractors

An extractor reads one file of the repository and returns a list of values, each with the file and
the line where it starts, or the file alone for `exists` and `size`; `yaml`, `json`, `jsonc`,
`json5`, `toml` and `regex` read every file `files:` names instead of one (see [A set of files](#a-set-of-files)),
`tool_version` reads every file that pins one tool, `package_version` each manifest of the package,
`dependency` one package in every manifest and lockfile a rule lists, `env` one variable in every file a rule lists, `etc` a configuration file and the files it includes, or the drop-ins `files:` names, and `marker` the markers that name its rule in every
file git tracks (see [`marker`](#marker)); `exists` and `size` look at one file and read
none of it. A rule names its extractors under `extract:` and sets exactly one key in each, the
extractor's kind:

```yaml
extract:
  replicas:
    yaml: {file: deploy/web.yaml, path: spec.replicas, kind: Deployment, name: web}
  engine:
    json: {file: package.json, path: engines.node}
  ts_target:
    jsonc: {file: tsconfig.json, path: compilerOptions.target}
  cpu_limit:
    toml: {file: runner/config.toml, path: "runners[0].kubernetes.cpu_limit"}
  base_image:
    regex: {file: Dockerfile, pattern: '^FROM (\S+)'}
  ci_node:
    text: {file: README.md, pattern: 'CI runs on node (\w+)'}
  listen_port:
    comment: {file: internal/server/server.go, pattern: 'listens on port (\d+)'}
  default_port:
    code: {file: internal/server/server.go, const: DefaultPort}
  go_needed:
    markdown: {file: README.md, target: section, section: Install > Requirements, pattern: 'Go (\S+)'}
  docs_port:
    html: {file: docs/index.html, target: table, column: Port, key: Host, row: web}
  tagged:
    marker: {}
  guava:
    xml:
      file: pom.xml
      path: //m:dependency[m:artifactId='guava']/m:version
      namespaces: {m: http://maven.apache.org/POM/4.0.0}
  version:
    ini: {file: setup.cfg, section: metadata, key: version}
  lib_url:
    ini: {file: .gitmodules, section: submodule, subsection: vendor/lib, key: url}
  port:
    properties: {file: src/main/resources/application.properties, key: server.port}
  node:
    tool_version: {tool: node}
  release:
    package_version: {}
  zod:
    dependency: {name: zod, files: [package.json, package-lock.json]}
  api_port:
    envfile: {file: .env, name: API_PORT}
  deploy_tag:
    gitlab-ci: {file: .gitlab-ci.yml, job: deploy, variable: TAG}
  start_port:
    shell: {file: deploy.sh, name: API_PORT}
  test_node:
    github-actions: {file: .github/workflows/ci.yml, job: test, step: setup, key: with.node-version}
  image_tag:
    jenkins: {file: Jenkinsfile, variable: IMAGE_TAG, stage: deploy}
  every_port:
    env: {name: API_PORT, files: [.env, deploy.sh, .gitlab-ci.yml, .github/workflows/*.yml, Jenkinsfile]}
  old_config:
    exists: {file: legacy/old.conf}
  notes_bytes:
    size: {file: docs/NOTES.md}
```

| Key | Fields | Returns |
|---|---|---|
| [`yaml`](#yaml) | `file` or `files`, `path`, `kind` or `name` for a file of several documents, `embedded` to read a document in a string, and `as: map`, or `as: record` with `fields` | every scalar the path selects, with `as: map` each mapping whole (see [Mappings whole](#mappings-whole)), or with `as: record` a record of the fields for each node (see [Records](#records)) |
| [`json`](#json) | `file` or `files`, `path`, `embedded`, and `as: map`, or `as: record` with `fields` | every scalar the path selects, each mapping whole, or a record for each node |
| [`jsonc`](#jsonc-and-json5) | the fields of `json` | what `json` returns, from a JSONC file: JSON with comments and trailing commas |
| [`json5`](#jsonc-and-json5) | the fields of `json` | what `json` returns, from a JSON5 file |
| [`toml`](#toml) | `file` or `files`, `path`, `embedded`, and `as: map`, or `as: record` with `fields` | every scalar the path selects, each mapping whole, or a record for each node |
| [`regex`](#regex) | `file` or `files`, `pattern`, `as: record`, `values_as` for a pattern that references another extractor's values, and `context` to keep a match by the lines around it | capture group 1 of every match (see [Values of another extractor](#values-of-another-extractor) and [The context of a match](#the-context-of-a-match)), or with `as: record` a record of the named groups of each match |
| [`text`](#text) | `file`, `pattern`, and `code` to read code blocks too | capture group 1 of every match in the prose of a Markdown file |
| [`comment`](#comment) | `file`, `pattern`, and `language` where the file's name chooses none | capture group 1 of every match in the comments of a source file (see [`comment`](#comment)) |
| [`literal`](#literal) | `file`, `path`, and `language` where the file's name chooses none | every scalar the path selects in the literal data of a file written in a programming language, any of them marked not literal (see [`literal`](#literal)) |
| [`code`](#code) | `file`, `name` or `const`, and `language` where the file's name chooses none | the value one name a source file declares has: a Go `const` or `var`, or another language's module level name or class member, marked not literal when the program computes it (see [`code`](#code)) |
| [`markdown`](#markdown) | `file`, `target` (`comment`, `section`, `code` or `table`), `section` for a heading path, `pattern`, `language` for code, and `column`, `key` and `row` for a table | each comment, section, fenced code block or table cell of a Markdown file, whole or capture group 1 of every match in it (see [`markdown`](#markdown)) |
| [`asciidoc`](#asciidoc) | the `markdown` extractor's fields | each comment, section, listing, literal or source block, or table cell of an AsciiDoc file, whole or capture group 1 of every match in it (see [`asciidoc`](#asciidoc)) |
| [`rst`](#rst) | the fields of `markdown` | each comment, section, literal or code block or table cell of a reStructuredText file, whole or capture group 1 of every match in it (see [`rst`](#rst)) |
| [`html`](#html) | `file`, `target` (`comment`, `section`, `code` or `table`), `section` for a heading path, `pattern`, `language` for code, and `column`, `key` and `row` for a table | each comment, section, pre block or table cell of an HTML file, whole or capture group 1 of every match in it (see [`html`](#html)) |
| [`marker`](#marker) | nothing, or `files` to narrow the walk | the value of every marker that names the rule, in the files git tracks (see [`marker`](#marker)) |
| [`xml`](#xml) | `file`, `path`, and `namespaces` for a file with namespaces | every node the path selects: an element's text, an attribute's value or a text node |
| [`ini`](#ini) | `file`, `key`, `section`, and `dialect`, `subsection` (git) or `host` (ansible) | every value of the key in the section, or the hosts or child groups of an ansible group |
| [`properties`](#properties) | `file`, `key` | every value of the key |
| [`tool_version`](#tool_version) | `tool`, and `files` to name the files | every version of the tool in the files that pin it (see [`tool_version`](#tool_version)) |
| [`package_version`](#package_version) | `files` to name the files, `tag` to read the git tags on HEAD too, or nothing | the version the package gives itself in each manifest (see [`package_version`](#package_version)) |
| [`dependency`](#dependency) | `name` or `module: true`, `files`, and `only` to keep ranges or resolved versions | every range a manifest gives the package and every version a lockfile resolved for it, each naming its reader, or each go.mod's module path (see [`dependency`](#dependency)) |
| [`envfile`](#envfile) | `file`, and `name` for one key | every value of the key in a dotenv file, or of every key (see [`envfile`](#envfile)) |
| [`gitlab-ci`](#gitlab-ci) | `file`, `job`, and `variable` or `key` | the value the job sees of a CI/CD variable or a job keyword (see [`gitlab-ci`](#gitlab-ci)) |
| [`shell`](#shell) | `file`, `name`, and `language` to name the dialect | every value the script assigns the variable, and every default an expansion gives it (see [`shell`](#shell)) |
| [`github-actions`](#github-actions) | `file`, `job`, `step` for a step by its id or name, and `variable` or `key` | the value the step, or the job, sees of an environment variable or a key such as `runs-on` or `with.node-version`, one for each job of a matrix (see [`github-actions`](#github-actions)) |
| [`jenkins`](#jenkins) | `file`, `variable`, and `stage` | the value a stage of a declarative Jenkinsfile sees of an environment variable (see [`jenkins`](#jenkins)) |
| [`systemd`](#systemd) | `file`, `section` and `key`, and `variable` for one variable of `Environment=`; `variable` alone for an environment file | every value of the key in a systemd unit or a Podman Quadlet file with its drop-ins applied, or every value the unit's `Environment=` or an environment file gives the variable (see [`systemd`](#systemd)) |
| [`env`](#env) | `name`, `files`, and `readers` to give a file its reader | every value of one variable in the files, each read by the reader its name chooses, and each naming its reader and its job, step or stage (see [`env`](#env)) |
| [`exists`](#exists-and-size) | `file` | `true` when a regular file is there, and `false` when none is, not an unknown (see [`exists` and `size`](#exists-and-size)) |
| [`size`](#exists-and-size) | `file` | the size of the regular file in bytes, in decimal (see [`exists` and `size`](#exists-and-size)) |

Each key but `exists` and `size`, which read no byte, also takes `max_bytes`, the largest file it
reads; for `files:`, `tool_version`, `package_version`, `dependency` and `env`, the files it reads in all, and
for `gitlab-ci` the file and the files it includes in all
(see [Limits](#limits)).

`file`, and each of `files`, is relative to the repository root. An absolute path, or one whose `..`
leaves the root, does not load. A `..` in `file` is resolved by name, before any symlink:
`link/../f.yaml` reads the repository's `f.yaml`, wherever `link` points. errata then follows the
symlinks on the way to the file as the operating system does, so a `..` in a link's target goes up
from where the link points. A link that ends inside the repository is read, whether it is relative
or absolute, and so is one whose route goes up through the repository's parent directories and back.
A link that ends outside is refused when the file is read, which makes the rule unknown. So is a
link whose route passes through any other directory outside the repository, even when it ends
inside, and an absolute link written through a symlink, such as one to `/tmp/...` on a machine where
`/tmp` is a link. The message says only that a symlink leads outside the repository: not where it
points, nor whether that exists. errata looks at nothing outside the repository to decide. These
rules hold for each file a glob in `files:` matches too. A file
that is not a regular file, such as a FIFO or a device, which git never checks out, is refused
before a byte of it is read, so a FIFO with no writer makes the rule unknown rather than hang the
run. A device is refused before it is even opened, since opening one can act on it: `/dev/watchdog`
arms, and a serial port raises DTR. A location, and every message, names the file as `file` wrote
it.

### A set of files

`yaml`, `json`, `jsonc`, `json5`, `toml` and `regex` take `files:` in place of `file:`, a list of files and globs, and
read each file as `file:` would, so one rule holds every file of a kind to a claim, the file added
tomorrow included. Setting both does not load. This rule says that every Argo CD Application that
ignores a Secret's data syncs with `RespectIgnoreDifferences=true`, without which a sync rewrites
the data the ignore was meant to keep:

```yaml
checks:
  - id: ignored-secret-data-is-respected-on-sync
    reason: A sync applies the whole manifest unless RespectIgnoreDifferences is set.
    scope: repo
    extract:
      apps:
        yaml: {files: [infra/argocd/applications/*.yaml], kind: Application, path: metadata.name}
      ignores:
        yaml:
          files: [infra/argocd/applications/*.yaml]
          kind: Application
          path: "spec.ignoreDifferences[?@.kind == 'Secret' && @.jsonPointers[?@ == '/data']].kind"
      options:
        yaml: {files: [infra/argocd/applications/*.yaml], kind: Application, path: "spec.syncPolicy.syncOptions[*]"}
    allow_empty: [ignores, options]
    assert: |
      size(apps) > 0 &&
      by_file(ignores).all(f, f in by_file(options) && 'RespectIgnoreDifferences=true' in by_file(options)[f])
```

An entry with `*`, `?` or `[` is a glob, and any other entry names one file, which must exist. A
glob is matched a path segment at a time, as Go's `path.Match` matches one: `*` and `?` never cross
a `/`, and `[a-z]` is a class. A segment that is exactly `**` matches zero or more whole segments:
`infra/**/*.yaml` matches `infra/a.yaml` and `infra/a/b/c.yaml`, `**/Chart.yaml` matches a
`Chart.yaml` at any depth, the root's included, and `docs/**` matches every file below `docs`. A
`**` inside a segment, as in `a**`, is a `*`. `env`'s `files:` matches the same way.

A glob matches the files git tracks, read from `.git/index` without running git, so it reads what a
clone holds: never an untracked or ignored file or a submodule. It needs a git repository to hold
the repository root, which may be a directory below its top
([A root below the top](#a-root-below-the-top)); the cases that make such a rule unknown are those of
[`env`](#env). In a sparse checkout, a glob that would
match a file the checkout leaves out (git marks it skip-worktree, and the working tree lacks it),
or may match a path below a directory a sparse index lists as one entry, makes the rule unknown,
and the message names the sparse checkout. A skip-worktree file the working tree holds, as after
`git update-index --skip-worktree` in a full checkout, is read as any other. A file `files:` or
`file:` names outright that the checkout leaves out is unknown too, never missing, whether the
name reaches it directly or through a tracked symlink, and whether the root is the top of the
repository or a directory below it, so `exists` on it is unknown and not `false`. A glob that meets neither reads
as in a full checkout. The values come
in a fixed order: the entries in the order `files:` lists them, a glob's files in the order of
git's index, which sorts paths byte by byte, and each file's values in the order its reader finds
them. A file two entries name is read once, at its first place, and so is a file two names resolve
to, such as a tracked symlink and its target, so a rule that each value occurs once does not count
one file twice.

Each file is read and parsed alone, and the location of each value names its own file, so every
message names the file and line of each value. A file that its reader refuses makes the whole rule
unknown, naming it: one that does not parse, a symlink that leads outside the repository, a file
git tracks that the working tree lacks. A glob over `**/*.yaml` that meets a Helm template, which is
not YAML, is unknown, so narrow the glob or read such files with `regex`. One refusal of `yaml`
does not apply in a set: a `kind` or `name` that selects no document of a file makes that file add
no value, since a glob over every manifest finds the kind in some of them. A file of several
documents still needs `kind` or `name`.

A glob that matches no file adds no value. An extractor that finds no value makes its rule unknown
unless `allow_empty` lists it, so a mistyped glob fails the run, and a rule about every file of a
kind that may have none lists the extractor in `allow_empty`. The rule above lists `ignores` and
`options`, since a repository may have neither, and holds `apps`, which it does not list, to at
least one file.

`max_bytes` is the most bytes the files of the set hold in all, 32 MiB by default and at most 64
MiB, each file read to what the files before it left. The values of every file together hold to
one extractor's limits: 100000 values and `max_bytes` bytes. The files also spend one budget on
what YAML aliases and merge keys add, one on a path's filter work, and one regex time limit, each
file starting at what the files before it spent. A file's node limit is scaled from the bytes the
files before it left, and its refusal names them. `files` lists at most 64 entries, one extractor reads at most 10000 files,
and the globs are matched against at most 256 MiB of path (see [Limits](#limits)).

In `assert`, `by_file(x)` is the values of the extractor `x` grouped by file: a map from each file
that gave `x` a value to its values, in the order `x` found them. A file that gave none is no key,
so `f in by_file(x)` asks whether a file gave `x` a value, and `by_file(x).all(f, ...)` runs over
the files that did, in the order of each file's first value, the same on every run. `by_file`
takes the name of one of the rule's extractors, and an expression that
passes it anything else does not load. `x` itself is still the list of every value, in the order
above.

### A root below the top

`--root` may name a directory below the top of a git repository, such as one subproject of a
monorepo, or errata's own `examples/`:

```sh
errata check --root examples --config examples/errata.example.yaml
```

errata then reads the index of the repository above the root and keeps only its entries below the
root, so a glob, the [marker](#marker) walk and a [witness](#witness) read what a clone holds
there, as at the top
([ADR 0018](adr/0018-a-root-below-the-top-reads-the-index-of-the-repository-above.md)):

- The repository is the one git would find: the nearest `.git` in the root or a directory above it,
  at most 64 up, passing over a `.git` directory git would not take for one and stopping where the
  file system changes, on Linux and macOS. A nested repository or a checked out submodule holds the
  roots inside it. A worktree's `.git` file is followed as at the top. errata reads no `GIT_DIR`,
  and applies neither git's owner test (`safe.directory`) nor `GIT_CEILING_DIRECTORIES`.
- Every path is relative to the root, as `file:` and `files:` write it, in messages and reports
  too, so output for a root at the top is what it was. A directory a sparse checkout leaves out
  at the root or above it is named from the repository's top, and the message says so.
- Nothing above the root is read or matched. `**` matches only below it, an entry such as
  `../*.yaml` does not load, and a tracked symlink below the root that leads above it is refused
  as one that leads outside the repository (errata calls the tree at `--root` the repository
  throughout, so a link out of the root reads so even when it stays in the git repository). The
  marker walk passes over such a link.
- Witnesses are the fixtures in `.errata/witness/` below the root. A root inside a fixture, or
  inside `.errata/witness`, is refused for every read, a `file:` and `exists` as well as a glob:
  its files are a planted break. A root that is a directory `.errata` is refused the same way.
- A sparse checkout below the root makes a glob unknown as at the top, and a root inside a
  directory a sparse index lists as one entry is refused for every glob.
- A root git tracks no file below is refused for a glob, as a clone holds no such directory, and
  so is one below which git's index lists a path spelt in another case, such as `Examples/x` for
  `examples`, which macOS and Windows open as one directory.
- A root no repository holds makes every glob, marker walk and witness unknown.
- The index is read whole and then cut, so its bounds ([Limits](#limits), git index) count the
  whole repository's index: in a monorepo whose index is over 64 MiB or 1000000 entries, a root of
  ten files is refused too. The bound on the paths a glob is matched against counts only those
  below the root.

### Paths

`yaml`, `json`, `jsonc`, `json5`, `toml` and `literal` share one path grammar: part of
[RFC 9535](https://www.rfc-editor.org/rfc/rfc9535) JSONPath, with two extensions: the leading `$` is
optional, and a key after a dot may hold `-`.

| Form | Selects | Example |
|---|---|---|
| `$` | the whole document; optional at the start | `$.spec.replicas` is `spec.replicas` |
| `.key` | the member `key` of a mapping | `spec.template.spec.nodeName`, `agent.node-label` |
| `['key']` or `["key"]` | a member whose key holds a dot, a space or another character `.key` cannot | `metadata.labels['app.kubernetes.io/name']` |
| `[n]` | element `n` of a sequence, counted from 0; a negative `n` counts from the end | `spec.containers[0].image`, `args[-1]` |
| `[*]` | every element of a sequence, or every member of a mapping, in file order | `spec.containers[*].image` |
| `[?@.key == 'value']` | every element of a sequence, or every member of a mapping, in file order, whose `key` equals the literal (see Filters) | `spec.containers[?@.name == 'api'].image` |
| `[?@.key]`, `[?!@.key]` | every element or member that has `key`, or that lacks it (see Filters) | `packageRules[?@.allowedVersions].allowedVersions` |
| `[?@.list[?@ == 'value']]` | every element or member whose `list` holds the literal: a filter inside a filter (see Filters) | `packageRules[?@.matchDatasources[?@ == 'docker']]` |

A key after a dot starts with a letter, `_` or a non-ASCII character, and goes on with those, digits
and `-`. The `-` is an extension: RFC 9535 does not allow it, and without it a key such as
`node-label` would need brackets. A key that starts with a digit does not load, since
`spec.containers.0.image` reads as an index and would select nothing; the message says to write
`[0]` for an index or `['0']` for a key. A quoted key takes RFC 9535's escapes: `\b`, `\f`, `\n`,
`\r`, `\t`, `\/`, `\\`, `\uXXXX` (with a surrogate pair above U+FFFF) and the quote that opened it,
`\'` in single quotes and `\"` in double quotes. A path holds no control character, not even
between quotes, and is valid UTF-8. Recursive descent (`..`) and slices are not supported.
A path that does not parse does not load, and the message names the column: `path "spec..x":
column 6: recursive descent (..) is not supported`.

A key that is absent, an index past the end, a key on a sequence and an index on a mapping all
select nothing. Nothing is zero values: the rule is unknown unless `allow_empty` lists the
extractor.

A path that selects a mapping or a sequence is an error and the rule is unknown, since neither has a
value to compare. Add `[*]` to take its members.

### Filters

A filter selects by a child's value, so a rule names a container by its name, wherever it sits in
the list:

```yaml
extract:
  api_image:
    yaml: {file: deploy/api.yaml, path: "spec.template.spec.containers[?@.name == 'api'].image"}
  prod_api:
    yaml: {file: deploy/api.yaml, path: "spec.template.spec.containers[?@.name == 'api' && @.env == 'prod'].image"}
```

It is RFC 9535's filter selector without `||`, parentheses, functions and every comparison but
`==`. Inside `[?...]` stand one or more terms joined by `&&`, and every one must hold. A term is a
comparison or a test that a path exists.

A comparison is a path from `@`, the element or member being tested, then `==`, then a literal;
the literal may come first. The path after `@` takes `.key`, `['key']` and `[n]` steps, or none,
so `ports[?@ == 80]` tests the element itself.
A literal is a string in single or double quotes with the same escapes as a quoted key, a number
as JSON writes it (`8080`, `-1.5`, `1e3`), `true`, `false` or `null`. Spaces are optional around
the parts of a filter, inside the brackets and before each step after `@`
(`[ ?@ .name == 'api' ]`), where RFC 9535 allows blank space. Between the segments of the path
itself there are none.

A comparison compares the value by the type the file gives it, as RFC 9535 does on JSON:

| File says | Equals | Does not equal |
|---|---|---|
| YAML or TOML `8080`, `8_080`, `0x1F90`, `8080.0`, `8.08e3`; JSON `8080`, `8.08e3` | `8080` | `'8080'` |
| YAML `"8080"` or `!!str 8080`, TOML or JSON `"8080"` | `'8080'` | `8080` |
| YAML `true`, `True`, `TRUE`; TOML or JSON `true` | `true` | `'true'` |
| YAML `null`, `Null`, `NULL`, `~` or nothing; JSON `null` | `null` | `'null'` |
| YAML `2026-10-17`, TOML `1979-05-27T07:32:00Z` | the string the file wrote, `'2026-10-17'` | another spelling of the same day |

A number equals a number of the same value, exactly and whatever its exponent:
`12345678901234567890123` does not equal `12345678901234567890124`, though a double holds
neither, and `0e1000000000000000` equals `0`. YAML reads an integer as yaml.v3 does, so `017` is
octal 15. An infinity or a NaN equals no literal. A YAML number too large for a double, such as
`1e400`, is a string to yaml.v3, so it equals `'1e400'` and not `1e400`, while JSON's `1e400` is
a number. YAML 1.1's `yes`, `no`, `on` and `off` are strings to yaml.v3, so
`[?@.privileged == true]` does not select `privileged: yes`, which Kubernetes reads as true. A
YAML timestamp, a `!!binary` and a scalar with a tag errata does not know, such as `!Ref api`,
compare as the string the file wrote, and so do TOML's dates and times. TOML has no null.

A path after `@` that selects nothing equals no literal, `null` included: `[?@.env == null]`
selects the elements where `env` is null, not those without it. A path that selects a mapping or
a sequence equals no literal either. A filter on a scalar selects nothing.

A path from `@` with no `==` is a test that it exists, RFC 9535's existence test: it holds when
the path selects at least one node from the member, whatever that node holds, `null` and an empty
list included. After `!` it holds when the path selects nothing. Its path takes `[*]` and filters
as well as keys and indices, so a filter can stand inside another:

```yaml
extract:
  # the cap of the one rule that names alpine/k8s and sets allowedVersions
  ceiling:
    json: {file: renovate.json, path: "packageRules[?@.matchPackageNames[?@ == 'alpine/k8s'] && @.allowedVersions].allowedVersions"}
  # the rules on the docker datasource that set minimumReleaseAge
  aged:
    json: {file: renovate.json, path: "packageRules[?@.minimumReleaseAge && @.matchDatasources[?@ == 'docker']].minimumReleaseAge"}
  # the destinations of the egress rules that open port 6443
  api:
    yaml: {file: netpol.yaml, path: "spec.egress[?@.ports[?@.port == 6443]].to[*].ipBlock.cidr"}
```

`[?@.list[?@ == 'x']]` is the test that `list` holds `x`: the inner filter keeps the elements of
`list` equal to `x`, and the outer one keeps the members where it keeps one. On a mapping it tests
the values. `[?@.list[0] == 'x' && !@.list[1]]` tests that `list` is exactly `['x']`. A `!` stands
only before a path from `@`, never before a comparison, since RFC 9535 needs parentheses for that.
A comparison takes no `[*]` or filter after `@`, since either can select many nodes.
[ADR 0004](adr/0004-filters-test-membership-with-rfc-9535s-existence-test.md) says why
membership has no `in` operator.

A filter joins at most 64 terms, and a literal or a key after `@` holds at most 2048 bytes (a
string as its escapes decode it, a number as written). Filters nest at most 4 deep, the filter of
the path itself counting as one: `[?@.a[?@.b[?@.c[?@ == 1]]]]` is the deepest. A path past any of
these does not load. The filters of one path may cost at most 4000000000 bytes. For each member
tested, each term counts 256, and each step after `@` in a comparison counts 256 plus the bytes
of its key, as does the literal compared. A test that a path exists counts 256, plus 256 and the
bytes of its key for each step it takes as it runs, and an inner filter counts its own members the
same way. A path that would cost more makes the rule unknown, and the message names the limit.
At the limit a filter of comparisons takes up to about 1.5 s. On the build node, a test that a path
exists whose `[*]` steps fan out through shared nodes reaches the limit in 0.18 s.

Every other filter construct does not load, and the message names it and its column: `||`,
`!=`, `<`, `<=`, `>`, `>=`, parentheses, functions such as `length()`, `$` inside a filter, two
paths or two literals compared, `!` before a comparison, and `[*]` or a filter after `@` in a
comparison:

```text
path "containers[?@.name != 'api']": column 20: operator != is not supported in a filter; only == is
```

### Values

Every value is a string: the text the file wrote, not what a decoder would make of it.

| File says | Value |
|---|---|
| YAML `1.0`, `true`, `0o17` | `1.0`, `true`, `0o17` |
| YAML `null`, `~`, or nothing after `key:` | `null`, `~`, the empty string |
| YAML `"1.0"` or a block scalar | its content, without quotes |
| JSON `1e3`, `1.50`, `true`, `null` | `1e3`, `1.50`, `true`, `null` |
| JSON `"a\"b"` | `a"b` |
| TOML `0x1F`, `1_000`, `inf` | `0x1F`, `1_000`, `inf` |
| TOML `1979-05-27T07:32:00Z` | `1979-05-27T07:32:00Z` |
| TOML `"a\tb"` | `a`, a tab, `b` |

A YAML alias gives the value at its anchor, with the anchor's line. A merge key (`<<: *base`) adds
the keys of `base` that the mapping does not set itself.

### Values a file only computes

Some files hold a value that only running them produces: `$(git describe)` in a shell script,
`${{ inputs.tag }}` in a CI file, a reference or a function call in HCL or a Makefile. A reader
for such a file marks the value as not literal. Its text is what the file wrote, not the value the
file means, so a rule never compares it. Each extractor's documentation says whether it can return
a not literal value. Of errata's kinds only `ini` can, for the files and sections that
[`ini`](#ini) lists, `properties` for a placeholder Spring Boot or Log4j fills in and for a
template file ([`properties`](#properties)), `tool_version` for a version
that names none, such as `lts/*` or `system` ([`tool_version`](#tool_version)), `package_version`
for a version the build computes, such as `dynamic = ["version"]` in pyproject.toml
([`package_version`](#package_version)), `dependency` for a range or a version the file takes
from elsewhere, such as `workspace:*` or Cargo's `{ workspace = true }`, or a Go version such as `v1` the go command resolves ([`dependency`](#dependency)),
`envfile` for a substitution of a key the file does not
set before it, a name alone that compose takes from the environment, and in a template file
([`envfile`](#envfile)), `gitlab-ci` for a value GitLab computes, such as `$CI_COMMIT_SHA` or a
plain `3.10` ([`gitlab-ci`](#gitlab-ci)), `shell` for a value the shell expands, such as
`$(git describe)` ([`shell`](#shell)), `github-actions` for a `${{ }}` expression
other than a matrix variable, or a plain `3.10` ([`github-actions`](#github-actions)), `jenkins` for
a value Groovy computes, such as `credentials('id')` ([`jenkins`](#jenkins)), `etc` for a value
OpenSSH expands, such as `%h/.ssh/keys`, and in a template file ([`etc`](#etc)), `literal` for a
value a program computes, such as `os.environ["PORT"]` or `"v" + VERSION` ([`literal`](#literal)), `code` for
the same and for a Go value only the running program knows, such as `os.Getenv("PORT")` ([`code`](#code)), and `env` for
any of these that the reader of its file marks; a `${{ }}` read with `yaml:` is still plain text,
and two workflows that use the same expression agree.

PHP reads `error_reporting = E_ALL` as the number the constant stands for, so the text `E_ALL` is
not literal:

```ini
memory_limit = 128M
error_reporting = E_ALL
error_reporting = 32767
```

```yaml
checks:
  - id: reporting-agrees
    reason: The chart sets PHP's error reporting.
    scope: repo
    extract:
      chart: {yaml: {file: values.yaml, path: reporting}}
      php: {ini: {file: php.ini, key: error_reporting}}
    agree: [chart, php]
```

A not literal value makes an agree or an assert rule unknown, and the message names the value,
its file and its line:

```text
UNKNOWN reporting-agrees (repo)
  why:  The chart sets PHP's error reporting.
  said: extractor "php" returned 1 not literal value, which a rule does not compare as text, since the file only computes such a value; list "php" in skip_not_literal to leave such values out
          "E_ALL" at php.ini:2
```

`skip_not_literal: [php]` leaves the not literal values of `php` out of the comparison. The rule
compares the values that are left, and its message counts what each extractor left out. Here the
same rule, named `reporting-drifted`, has `skip_not_literal: [php]` and reads a chart that says
`"1"`:

```text
FALSE reporting-drifted (repo)
  why:  The chart sets PHP's error reporting.
  said: agree: the values differ
          "1" at values.yaml:2 (chart)
          "32767" at php.ini:3 (php)
        skip_not_literal left out 1 not literal value of extractor "php"

1 erratum, 0 unknown. Coverage: ran 1, skipped 0.
  left out of reporting-drifted (repo), not literal: "E_ALL" at php.ini:2 (php)
```

`assert` sees each list without its left out values, so `size()` and an index count only the
values left: with `E_ALL` on line 2 left out, `php[0]` is the value on line 3, and `php[1]` is
out of range.

An extractor whose every value was left out counts as one that found no value: the rule is unknown
unless `allow_empty` lists it. Listed, it is an empty list, as for an extractor that found
nothing, so an agree rule compares the one value left and an assert rule decides on `[]`; the
message then says the rule compared nothing from that extractor. The floor of two values for agree
counts only the values left.

`skip_not_literal` may list only an extractor that can return a not literal value: for any other the
key would load and leave nothing out, so it does not load. That holds for every kind but `ini`,
`properties`, `tool_version`, `package_version`, `dependency`, `envfile`, `gitlab-ci`, `shell`,
`github-actions`, `jenkins`, `literal` and `code`, for an `ini` extractor whose file errata models no transform for, such as
`pytest.ini`, and for a `properties` file that is neither a template nor one of Spring Boot's or
Log4j's. The message names the kind, and
for `ini` the dialect and the file:

```console
$ errata check -c errata.yaml
errata: load errata.yaml: checks[0]: check "pytest-agrees": skip_not_literal names "opts", whose kind of extractor, ini, plain dialect on pytest.ini, returns only literal values, so it has nothing to leave out
```

Nor does a list with no name, a name the rule does not declare, a name given twice, or anything
but a list.

The text output names each value left out under the coverage line, for a rule that holds as for
one that does not, since a rule that holds held only on what was left. JUnit names them in the
case's `<system-out>`. SARIF names them in every case: an erratum links each as a related location
whose message says it is not literal and was left out, and an unknown, or a rule that holds with a
value left out, lists each as a location of its notification.

### Several YAML documents

A file without `kind` or `name` must hold at most one document. Empty documents, such as the one a
trailing `---` makes, do not count. With `kind`, `name` or both, errata keeps each document whose
top-level `kind` and `metadata.name` equal them, and returns the values from all of them. A
selector that matches no document, and a file of several documents without a selector, make the
rule unknown with a message that says which.

### Documents in strings

A config is often carried in a string of another file: Gatus's `config.yaml` in a ConfigMap, a
GitLab runner's `config.toml` in a Helm value, a JSON list in an env var's `value`. `embedded` on a
`yaml`, `json`, `jsonc`, `json5` or `toml` extractor reads such a document. It is a list of steps, and each step
names the `format` the string is parsed in, `yaml`, `json` or `toml`, and a `path` read in that
document, in the grammar and with the filters above:

```yaml
extract:
  region:
    yaml:
      file: infra/observability/15-garage.yaml
      kind: ConfigMap
      path: "data['garage.toml']"
      embedded: [{format: toml, path: s3_api.s3_region}]
  tenants:
    yaml:
      file: infra/observability/15-garage.yaml
      kind: Deployment
      path: "spec.template.spec.containers[*].env[?@.name == 'GARAGE_TENANTS'].value"
      embedded: [{format: json, path: "[*].name"}]
```

A step reads every string the path, or the step before it, selected, and the values are what the
last step selects. A document in a string in a document in a string is two steps, and
`embedded` takes at most 4. A step past the fourth, a format left out or one errata does not read,
and a path left out or that does not parse do not load.

errata parses the string with the parser a file of its format goes through, with the same
refusals: a repeated key, a depth past the limit, a tag that does not fit. A YAML string holds at
most one document, since a step has no `kind` or `name`; an empty one holds none, so its step
selects nothing, which `allow_empty` decides. A value a step reads that is not a string (a number,
a bool, null, a mapping or a sequence), and a string that does not parse, make the rule unknown.
So does a value a filter compares as a string but the file types as something else: a YAML
timestamp, which yaml.v3 resolves for a plain date such as `2024-01-01`, a date and time joined by
`T` or `t` with a zone (`Z` or an offset such as `+01:00`), or a date and time joined by a space
with no zone; a `!!binary`; and a TOML date or time. `2024-01-01T10:00:00`, with no zone, and
`2001-12-14 21:59:43.10Z`, a space with a zone, are strings to yaml.v3, and a step reads them.
Quote the date, or tag it `!!str`, and it is a string a step reads. The message names the file,
every path and the line where the string starts:

```text
infra/gitlab-runner/values.yaml: path "runners.config" then toml path "runners[*].cache.s3.BucketName": the toml document in the string at line 4: toml: line 3: ...
```

A value is reported at a line of the outer file. When every string on its way is a YAML literal
block scalar (`|`), whose lines are the file's lines, that is the line the value is on, also when
an anchor, a tag or a comment stands on a line between the key and the `|`, and through an alias
of such an anchor. Any other string folds or escapes its lines, as `>`, a quoted string, a JSON
string or a TOML multi-line string can, so every value in it is reported at the line where the
outermost such string starts. A block scalar that holds an LS or a PS counts as such a string,
since YAML breaks the file's lines there and JSON and TOML do not, and so does one in a UTF-16
file. No value is reported past the file's last line: in a file whose lines end in CR, NEL, LS or
PS, which YAML counts as line breaks, a value that would land past it is reported at the last.

The documents in an extractor's strings count against its limits together, as one more file (see
Limits): a string that YAML aliases repeat counts each time it is read. Over a set of files they
count together across every file of the set.
[ADR 0007](adr/0007-embedded-reads-a-document-a-string-holds.md) says why the steps are a
list and how lines are reported.

### Mappings whole

A path selects scalars, so a list of objects reaches assert as one flat list for each field, and
which value belonged to which object is lost. Renovate's `packageRules` need the objects whole:
Renovate applies them in order, and a later rule's `automerge` overrides an earlier one's. With
`as: map`, a `yaml`, `json`, `jsonc`, `json5` or `toml` extractor hands assert each mapping its path selects, in
file order. This rule says that no rule after Renovate's never-automerge rule turns automerge back
on for the charts it names:

```yaml
checks:
  - id: blast-radius-charts-never-automerge
    reason: >
      The charts auto-sync, so the never-automerge rule is the only gate before a bump deploys,
      and a later packageRule that names one of them with automerge on would remove it.
    scope: repo
    extract:
      rules: {json: {file: renovate.json, path: "packageRules[*]", as: map}}
    assert: |
      rules.exists(b, has(b.prPriority) && b.prPriority == '5' && has(b.automerge) && b.automerge == 'false' && !has(b.matchUpdateTypes) && has(b.matchPackageNames) && size(b.matchPackageNames) > 0) &&
      rules.filter(b, has(b.prPriority) && b.prPriority == '5' && has(b.automerge) && b.automerge == 'false' && !has(b.matchUpdateTypes) && has(b.matchPackageNames)).all(b,
        b.matchPackageNames.all(n,
          rules.map(r, has(r.automerge) && has(r.matchPackageNames) && n in r.matchPackageNames, r.automerge)[
            size(rules.filter(r, has(r.automerge) && has(r.matchPackageNames) && n in r.matchPackageNames)) - 1] != 'true'))
```

The `exists` at the start holds the rule to the never-automerge rule itself: without it, a
`renovate.json` that lost that rule would pass, since `all` over no member holds.

In assert the extractor is a `list(map(string, dyn))`. A nested mapping is a map, a sequence a
list, and a scalar a string, the text the file wrote as [Values](#values) gives it: `automerge:
false` is `'false'` and `prPriority: 5` is `'5'`, which `int()` converts. So JSON `false` and
`"false"` are one value, `'false'`, and so are `null` and `"null"`, `'null'`; `{}` and `[]` are an
empty map and an empty list. A key set to null is set: `has(r.automerge)` is true for
`"automerge": null`, so a rule that reads null as unset tests `r.automerge != 'null'` too. A key is
its text as the file wrote it, so YAML's `1` and `0x1` are two keys, and `~` and `true` are the
keys `'~'` and `'true'`. A map's keys come in the order the file wrote them, but for a YAML merge
key: the keys `<<` adds come after the mapping's own, wherever `<<` stands, so `{<<: *b, a: 3}`
with `b: {x: 1}` gives `a`, then `x`. `has(r.automerge)` tests whether a mapping sets a key, and
reading a key it lacks makes the rule unknown. The last write of a fold is the last of the
matching values, `l[size(l)-1]`, as above. `cel.bind` writes the filter once:
`cel.bind(l, rules.filter(r, ...), l[size(l)-1].automerge != 'true')`.

CEL types a value of a map `dyn`, which takes any type, and its equality makes a string and a bool
or a number unequal without an error. So `r.automerge != true` would hold on every input, the rule
that turns automerge on included. errata refuses at load every `==`, `!=` and `in` between a value
CEL types `dyn` and an operand whose type such a value never equals: bool, int, uint, double, null,
bytes, duration or timestamp, or a list or map with members, keys or values of such a type. It
goes by the operand's type, not its spelling, so `r.replicas == size(l)`, `r.z == int('5')`, a
ternary and `r.z in v.map(x, int(x))` are refused as `5` is. Inside a list or map literal it looks
at each element, key and value, so `r == {'automerge': true}` is refused at `true`, and `[]` and
`{}` load. It does the same for the literal a `cel.bind` name binds, for the list literal a
comprehension variable runs over, bound to a name or not, and for the member a field or a literal
index selects from such a literal. So `cel.bind(want, {'name': 'x', 'automerge': true},
rules.exists(r, r == want))` is refused at `true`, though that map's type, `map(string, dyn)`, says
nothing. When `want` binds `{'name': 'x', 'replicas': 3}`, `r.replicas == want.replicas` and
`want['replicas']` are refused at `3`. Any other operand, such as the result of `map()` over the
file's values, is checked by its type alone. Following names to their values has a budget of
4194304 steps at load. That is more than ten times the most that nine shapes errata measured at
the length limit took, but a rule built to defeat the tracing, such as many comprehensions over
names that alias one bound list, can reach it within that limit. An expression past the budget
does not load, and the refusal says it is too intricate to check.
A side that is a whole map, or a list of maps, counts as a map's value too. The refusal is at the
operand, or the member, and says what to write instead:

```text
assert: the bool true is compared with a value CEL types dyn, such as one from a map; a map's scalars are always strings, so the two are never equal; compare with the string 'true'; if the other side does not come from a map, wrap it in dyn(), at line 1 column 46 of the expression:
```

A value the expression wraps in `dyn()` itself is left alone. That is the way out for a `dyn` that
does not come from a map, such as a member of a list literal that mixes types: `[1, 'a'][0] == 1`
is refused in any rule, and `dyn([1, 'a'][0]) == 1` loads.

Every node the path selects, or the last `embedded` step selects, must be a mapping. A scalar or a
sequence makes the rule unknown, naming its line; add `[*]` to take a sequence's members. `as`
takes `map`, or `record` with `fields` (see [Records](#records)), and any other word does not
load. Each map and each value in it keeps the file
and the line where it starts, through a document in a string as [Documents in
strings](#documents-in-strings) gives them, and the message shows each map at its place, close to
JSON and cut at 256 bytes, and a report names it the same way:

```text
          rules[41] = {"matchPackageNames": ["cert-manager"], "automerge": "true"} at renovate.json:607
```

Over `files:`, each map is at its own file, and `by_file(x)` maps each file to its maps. `normalize`
applies to every scalar of a map and to no key. `agree` compares flat values, so a rule that names
an extractor with `as: map` in `agree` does not load. Every node of the maps counts as one of the
extractor's 100000 values, and the bytes of their keys and scalars count against `max_bytes`,
also when YAML aliases select one mapping many times (see [Limits](#limits)). An assert over maps runs under
the cost limit and the timeout of any other. errata builds the maps in time linear in their nodes,
and reads one by index at once. cel-go makes a comprehension take time quadratic in the members it
runs over, so an `all` over 16000 maps takes 1.5 s, as one over 16000 strings does, and one over
50000 maps, or over the keys of one mapping of 99999 keys, reaches the timeout and is unknown.
[ADR 0009](adr/0009-as-map-hands-assert-each-mapping-whole.md) says why a scalar stays a
string and how a record of several paths uses the same maps.

### Records

Two fields of one element are often a pair: a Gatus endpoint's `name` and `group` make the key an
app pushes to, and a regex match's name and source groups belong together. Read as two flat
lists, the pairing is lost. A record keeps it: one mapping for each element, whose keys are the
fields the rule names. A `yaml`, `json`, `jsonc`, `json5` or `toml` extractor with `as: record` reads each path in
`fields` from every node its path selects, after any `embedded` step:

```yaml
checks:
  - id: landingpage-push-keys-match-gatus
    reason: A push to a key Gatus does not define 404s and is swallowed, so the alert never fires.
    scope: repo
    extract:
      pushed: {regex: {file: app/monitoring.py, pattern: '^[A-Z_]+ = "([a-z0-9_-]+)"$'}}
      gatus:
        yaml:
          file: infra/gatus/gatus.yaml
          kind: ConfigMap
          path: "data['config.yaml']"
          embedded: [{format: yaml, path: "external-endpoints[*]"}]
          as: record
          fields: {name: name, group: group}
    assert: pushed.all(k, gatus.exists(e, e.group + '_' + e.name == k))
```

A field's path starts at the element, and `$` is the element itself. A field takes the node its path
selects whole, so a mapping is a map and a sequence a list, as under `as: map`. A field whose path
selects nothing is left out of that record, so `has(e.desc)` tests it; one that selects more than
one node makes the rule unknown, naming the element's line, so write `[0]` to take the first. A
scalar or a sequence that holds none of the fields makes the rule unknown at its line; a mapping
that holds none is an empty record. A field that no record holds makes the rule unknown, since
`!has(e.url) || ...` would then hold on every record over a mistyped path or a key the file renamed;
`allow_empty` on the extractor says that no record need hold it. The same typo in the expression
does not load: `e.ulr`, `has(e.ulr)`, `'ulr' in e` and `e['ulr']` are refused when the fields are
`name` and `url`, and the message names both. errata finds a record by following the expression:
a comprehension variable over the extractor, a `cel.bind` name bound to the extractor or to one of
its records, an index such as `gatus[0]`, and the list `filter` or `map(x, x)` keeps of them. A
record reached through `by_file`, or a variable that may also be another value, is not checked.
Two fields over one path do not load, since `agree` over them would compare each node with
itself. A record's keys are sorted by name. A field name is a letter or `_` and then letters,
digits and `_`, and none of the words CEL reserves (`true`, `false`, `null`, `in`, and `as`,
`break`, `const`, `continue`, `else`, `for`, `function`, `if`, `import`, `let`, `loop`,
`namespace`, `package`, `return`, `var`, `void`, `while`), so assert reads it as `e.name`.

A `regex` extractor with `as: record` returns a record for each match, whose fields are the
pattern's named groups, `(?P<name>...)` or `(?<name>...)`, in the order the pattern opens them. A
group without a name is no field, and a named group that took no part in a match is left out of
that record. A pattern with no named group, one name given to two groups, or a name that is no
field name, does not load. With a [`context`](#the-context-of-a-match), only a match that has it
gives a record. A pattern that references another extractor's values does not load with `as:
record`, since its searches return group 1 of each value's matches.

```yaml
extract:
  images: {regex: {file: deploy.yaml, pattern: 'image: (?P<repo>[^:\s]+):(?P<tag>\S+)', as: record}}
  chart:  {yaml: {file: Chart.yaml, path: appVersion}}
agree: [images.tag, chart]
```

In assert, a record is a map like any other under `as: map`: the same `list(map(string, dyn))`, the
same strings, the same refusal of a comparison with a type such a value never equals, and `by_file`
groups records by file. `agree` names one field of each record as `name.field`, and compares that
field of every record as a flat value at its own line; a record without the field adds nothing. Two
fields of the same records can agree with each other, and the extractor runs once. A field that
holds a mapping or a sequence makes the rule unknown, and so do records none of which has the field,
unless `allow_empty` lists the extractor. A field the records do not have, a field of an extractor
that returns no record, and a record extractor named whole in `agree` do not load. No extractor's
name holds a dot, so `a.b` always names the field `b` of the extractor `a`.

Each record keeps the line where its element or match starts, and each field the line where its
value starts, so a message about a whole record names the record, and `agree` names each field:

```text
          "1.2.2" at deploy.yaml:5 (images.tag)
```

`normalize` applies to every field. Each record and each node of its fields counts as one of the
extractor's 100000 values, and the bytes of field names, keys and scalars count against
`max_bytes`, in one file and over a set together. [ADR 0013](adr/0013-a-record-is-a-map-of-the-fields-a-rule-names.md)
says how records meet the rules for flat values.

## Extractor kinds

Each kind below is the key an extractor sets under its name in `extract:`. The [table of
kinds](#extractors) gives the fields each takes and what it returns; its section here gives the
rules it reads its file by. The fields and values that `yaml`,
`json`, `jsonc`, `json5` and `toml` share are in [Extractors](#extractors) above, as are `file`, `files` and
`max_bytes`, which most kinds take; [Limits](#limits) bounds every kind.

### `yaml`

`yaml` reads a YAML file, or a [set of files](#a-set-of-files), and returns every scalar its path
selects, each as a string ([Values](#values)). Its fields are in the [table of
kinds](#extractors). [Paths](#paths) and [Filters](#filters) give the path's grammar, and
[Several YAML documents](#several-yaml-documents), [Documents in strings](#documents-in-strings),
[Mappings whole](#mappings-whole) and [Records](#records) say what its other fields do.

### `json`

`json` reads a JSON file as [`yaml`](#yaml) reads a YAML file, by the same sections, but for
[Several YAML documents](#several-yaml-documents): a JSON file holds one document. Its fields are
in the [table of kinds](#extractors).

`json` reads strict JSON, as RFC 8259 writes it. A file with a comment or a comma after the last
member, as `tsconfig.json` and VS Code's settings often hold, does not parse, so a rule that reads
it with `json` is unknown, never true. Read such a file with [`jsonc` or `json5`](#jsonc-and-json5).

### `jsonc` and `json5`

`jsonc` and `json5` read a JSON file written in a dialect that strict JSON refuses, as
[`json`](#json) reads a JSON file: one document, the same [path](#paths), `embedded`, `as` and
`fields`, and each value at the line it starts on. The kind names the dialect, and every file the
extractor reads is read in it, whatever its name:

- `jsonc` reads JSONC: JSON with `//` and `/* */` comments and a comma after the last member of an
  object or the last item of an array. A string, a number, every other value and the white space
  between them are JSON's, and a byte order mark may open the file.
- `json5` reads JSON5, the dialect of the [JSON5 specification](https://spec.json5.org/) 1.0.0:
  JSONC, and a key written as an ECMAScript identifier, a string in single quotes with
  ECMAScript's escapes and a backslash that continues it on the next line, and a number with a
  sign, a point that leads or ends it, in hexadecimal, `Infinity` and `NaN`. White space includes
  Unicode's space characters.

JSON5 reads every JSONC file into the same values, so a `files:` set that holds files of both
dialects is read with `json5`. `jsonc` refuses what only JSON5 allows, such as an unquoted key in
`renovate.json5`, so a rule that asks for JSONC is unknown on such a file, never true.

```yaml
extract:
  target:
    jsonc: {file: tsconfig.json, path: compilerOptions.target}
  presets:
    json5: {file: renovate.json5, path: "extends[*]"}
```

Which kind reads a tool's file is what the tool says it reads, in its documentation or, where that
is silent, in the code that parses the file:

| File | Kind | Source |
|---|---|---|
| any `*.jsonc` | `jsonc` | the name |
| any `*.json5` | `json5` | the [JSON5 specification](https://spec.json5.org/) |
| `tsconfig.json`, `tsconfig.*.json`, `jsconfig.json` | `jsonc` | TypeScript's parser, [`parseConfigFileTextToJson`](https://github.com/microsoft/TypeScript/blob/v5.9.3/src/compiler/commandLineParser.ts#L2243), which reads both files with comments and trailing commas; its documentation does not say |
| `.vscode/settings.json`, `launch.json`, `tasks.json` and the other `.json` files of `.vscode`, `*.code-workspace` | `jsonc` | [VS Code](https://code.visualstudio.com/docs/languages/json#_json-with-comments), and for a workspace file [VS Code's multi-root workspaces](https://code.visualstudio.com/docs/editing/workspaces/multi-root-workspaces) |
| `devcontainer.json`, `.devcontainer.json` | `jsonc` | [Dev Containers](https://containers.dev/implementors/spec/) |
| `renovate.json`, `.github/renovate.json`, `.gitlab/renovate.json`, `.renovaterc`, `.renovaterc.json` | `jsonc` | [Renovate](https://docs.renovatebot.com/configuration-options/) |
| `renovate.json5`, `.renovaterc.json5` | `json5` | [Renovate](https://docs.renovatebot.com/configuration-options/) |
| `.babelrc`, `.babelrc.json`, `babel.config.json` | `json5` | [Babel](https://babeljs.io/docs/config-files) |
| `deno.jsonc` | `jsonc` | [Deno](https://docs.deno.com/runtime/fundamentals/configuration/); `deno.json` is strict JSON, read with `json` |
| `.eslintrc.json` | not covered whole | [ESLint](https://eslint.org/docs/latest/use/configure/configuration-files-deprecated) takes comments but no trailing comma. `json` reads one with no comment; `jsonc` reads one with comments, and also a trailing comma ESLint refuses, so a rule can hold on a config ESLint does not load |

A number keeps the text the file wrote, as in `json`: `0x1F` stays `0x1F` and `+1` stays `+1`. A
key the file repeats, however JSON5 quotes or escapes it, does not parse, since readers disagree
on which of the two counts. JSONC takes JSON's white space only, so a no-break space or a form
feed between values does not parse in `jsonc`, though VS Code reads it. A file that does not parse
in its dialect makes the rule unknown, and the message names the line, such as
`tsconfig.json: line 3: a key cannot start with 't'`, or for a string the file ends inside, the
line the string opens on.

### `toml`

`toml` reads a TOML file as [`json`](#json) reads a JSON file. Its fields are in the [table of
kinds](#extractors).

### `regex`

`regex` searches a file, or a [set of files](#a-set-of-files), and returns capture group 1 of
every match, or a [record](#records) of each match's named groups. Its fields are in the [table of
kinds](#extractors). [Values of another extractor](#values-of-another-extractor) and [The context
of a match](#the-context-of-a-match), below, say what `values_as` and `context` do.

`pattern` is Go's [RE2 syntax](https://github.com/google/re2/wiki/Syntax) and needs a capture
group; a pattern without one, or with more than 32, does not load. Write `(?:...)` for a group
that need not capture. The `m` flag is on, so `^` and `$` match at each line;
`(?-m)` at the start turns it off. `.` does not match a newline unless the pattern sets `(?s)`. A
CRLF line ending and a lone `\r` read as LF, so `$` matches before either and no value ends in a
`\r`. Each value is at the line where its match starts, counted in the file as it is. A match in
which group 1 took no part gives no value. A final newline ends the last line and starts no empty
one, as it does for the context, so `^(.*)$` over `a` and a newline gives `a` alone, not a second,
empty value at line 2. The same holds for `as: record` and for the `text` extractor's prose.

#### Values of another extractor

Some claims say that every name one file lists appears in another file of free text. A `regex`
pattern may reference another extractor of its rule as `(?{name})`: the rule runs that extractor
first, and searches the file once for each of its values, with the value in the reference's place,
quoted as text. This rule says that every release a bootstrap script pins with `--version` is
named by a Renovate `matchString`:

```yaml
extract:
  releases:
    regex: {files: ['infra/**/bootstrap.sh'], pattern: '^\s*helm upgrade --install ([\w.-]+) \S+ .*--version'}
  tracked:
    regex: {file: renovate.json, pattern: '--install ((?{releases})) '}
allow_empty: [tracked]
assert: releases.all(r, r in tracked)
```

- The value goes in inside a group of its own, `(?:value)`, so `.` in `a.b` matches only a dot,
  and a repeat after the reference repeats the whole value. The values come in the order their
  matches start in the file, and two that start at one place in the order of the values. A value
  found twice is searched for once.
- A pattern holds at most one reference. A reference to an extractor the rule does not declare,
  and extractors that reference each other, a pattern its own included, do not load, and
  `errata check` exits 2. So does a reference inside a character class or `\Q...\E`, where RE2
  reads it as text, and a pattern that does not load with an empty value in the reference's place.
  RE2 refuses `(?{` as syntax, so no pattern that loaded before means anything else now.
- A rule runs each extractor after the ones it references, and otherwise in the order it would
  have. A referenced extractor that cannot be read makes the rule unknown. One that finds no value
  leaves nothing to search for, so the referencing extractor finds none either, and `allow_empty`
  decides: `extractor "tracked" found no value: extractor "releases" gave no value to search for`.
  Only the literal values are searched for, after `skip_not_literal`, and each as its file holds
  it, before `normalize`, since a lowered value would miss the text it came from; a pattern that
  should ignore case starts with `(?i)`.
- An empty value, and one longer than 4096 bytes, make the rule unknown, naming where the value
  was found. An empty value would leave the pattern to match its context alone, so
  `--install ((?{releases})) ` would return the empty text for `--install  x`, and
  `releases.all(r, r in tracked)` would hold for an empty release no file names. A long value's
  pattern is compiled once for each file it searches, and a literal of 4 MiB took 5.6 s and 3.8 GB
  to compile.
- With `files:`, each file is searched for every value, and each value keeps its file, so
  `by_file` groups by the files searched.
- `values_as: pattern` puts each value in as RE2 syntax instead, for a rule that runs a regex
  another file holds, such as a Renovate `matchString`. The value goes in as `(?-m:value)`, so its
  `^` and `$` match at the start and end of the text, as in a JavaScript regex without the `m` flag,
  which is how Renovate compiles one; the pattern around it keeps the `m` flag. What matches is
  RE2's reading of the value, which differs from JavaScript's: `\s` matches only ASCII spaces, `.`
  matches `\r`, U+2028 and U+2029, and RE2 accepts syntax JavaScript rejects or reads otherwise,
  such as `(?m)` inside a value, `\p{..}`, `\Q...\E`, `\A`, `\z`, `\x{..}` and `[[:alpha:]]` ([ADR
  0010](adr/0010-a-regex-pattern-references-another-extractors-values.md)). The reference must
  then stand inside capture group 1 or after it, or the pattern does not load, since a value's own
  groups would come first. A value that is no RE2 pattern by itself, such as a JavaScript lookahead,
  one that ends inside `\Q`, and one whose groups take the pattern past 32 make the rule unknown,
  naming the value and where it was found. `literal` is the default.

The cost is the number of values times the size of the file, since each value is one linear search
of it. The regex time limit (see [Limits](#limits)) starts before the first value is checked and is shared by
checking the values, compiling their patterns and every search, so many values over a large file
make the rule unknown rather than run long. On the
build node, one value searched over 32 MiB of short lines with `^\s*((?{v}))$` took 0.69 s and four
values 2.64 s, and 50 values were refused at 10 s; a value costs what the same pattern with the
value written in costs. 100000 values over a 1 KB file took 0.97 s, at a peak of 222 MiB. Values
of 4096 bytes, the most a value may hold, were refused at 10.2 s, 100000 of them over a 2 byte file
and over 32 MiB alike, and so were 100000 values of `(?:\pL{1000}|\pN{1000}|\pS{1000}|\pP{1000})`
padded to 4 KiB under `values_as: pattern`, each of which costs two parses to check. The
matches of every value together are held to 100000 values and `max_bytes` bytes.
[ADR 0010](adr/0010-a-regex-pattern-references-another-extractors-values.md) says why the
reference is spelled this way and why each value is its own search.

#### The context of a match

RE2 has no lookahead or lookbehind, so a regex cannot say "a `kubectl apply`, except on a line that
applies the upgrade Plans". A `regex` may take a `context`, which keeps a match only where the
lines around it hold what `with` asks for and nothing `without` names. This rule says that a
provisioning script changes the cluster only to ask Argo CD to refresh, to apply the k3s upgrade
Plans, or in a comment:

```yaml
extract:
  changes:
    regex:
      file: scripts/node-provisioning.sh
      pattern: '\b(kubectl[^\S\n]+(?:-n[^\S\n]+\S+[^\S\n]+)?(?:apply|annotate|delete)|helm[^\S\n]+(?:upgrade|install))\b'
      context: {without: '^\s*#|^(?:[^;&|`$<>]|[$<>][^(;&|`$<>])*(?:infra/k3s-upgrade/|argocd\.argoproj\.io/refresh)(?:[^;&|`$<>]|[$<>][^(;&|`$<>])*$'}
allow_empty: [changes]
assert: size(changes) == 0
```

`[^\S\n]` is a space that is no newline: with `\s` a command could start on one line and end on
the next, and both lines would be its context. A context is whole lines, the text before the match
as well as after it, so an exemption anywhere on a line exempts every command on it. The `without`
above exempts only a line with no `;`, `&`, `|`, backtick, `$(`, `<(` or `>(`, so `kubectl apply
-f infra/k3s-upgrade/p.yaml && kubectl apply -f staging.yaml` is still an erratum, and so is a
command in `$( )` or `<( )` on that line. A redirect such as `2>&1` or `>>` on an exempt line is
refused too, which fails closed.

- A match's context is its own lines, `before` lines before the first and `after` lines after the
  last, cut to the file; a final newline ends the last line and starts no empty one. Both default
  to 0 and cannot be below 0. `context: {before: 2, with:
  '^# managed'}` keeps a match only when one of the two lines above it, or its own, holds the
  comment.
- A match is kept when `with` matches in a line of its context and `without` in none. A context
  needs one of them or both, and neither may reference values. A pattern that matches the empty
  text at the start or the end of every line, such as `x*`, `^`, `$` or `^\s*#?`, does not load,
  since it holds in every line; `^$`, which matches only an empty line, loads.
- Each of the two is searched in each line alone, as though the line were the whole text. Nothing
  in it matches a line break, so `a\sb` never matches `a` at the end of one line and `b` at the
  start of the next, and `\A` and `\z` match at the start and end of each line. Its groups capture
  nothing.
- A kept match returns group 1, or with `as: record` its record, as it would without a context.
  The limits of 100000 values and `max_bytes` bytes count the matches before the context drops
  any.
- For a pattern that references values, the context applies to the matches of every value. With
  `files:`, each file's lines are its own.

Each context pattern is one search of the file, which reads each line about once, and errata keeps
the lines it matched in as one bit each, so whether a match has its context is two lookups however
wide the context is. The cost is linear in the file and does not depend on `before` or `after`. The
context's searches share the regex time limit of the pattern's search. On the slowest node, over
32 MiB of short lines with 99274 matches, a context as wide as the file took 4.1 s against 2.2 s
for the pattern alone, and 32 MiB of empty lines each matched by `with: '^$'`, the most calls a
file can cost, was refused at 10 s.
[ADR 0012](adr/0012-a-regex-keeps-a-match-by-the-lines-around-it.md) says why each line is
searched alone.

### `text`

A sentence in a README breaks across lines and carries backticks and emphasis, so a `regex` over
the raw file misses `CI runs on node` followed by `` `builder` `` on the next line. The `text`
extractor parses the file as CommonMark and matches `pattern` against its prose: the text of its
paragraphs, headings and list items, spaced as a browser shows it.

| Field | Required | Meaning |
|---|---|---|
| `file` | yes | the Markdown file |
| `pattern` | yes | an RE2 pattern with a capture group, as for `regex`; group 1 is the value |
| `code` | no | `true` reads code blocks, HTML blocks and front matter as well; default `false` |
| `max_bytes` | no | the largest file read (see [Limits](#limits)) |

```yaml
checks:
  - id: ci-runner-agrees
    reason: >
      The README tells contributors which runner builds the project. If it names a runner the
      pipeline no longer uses, people debug the wrong machine.
    scope: repo
    extract:
      readme: {text: {file: README.md, pattern: 'CI runs on node (\w+)'}}
      ci:     {yaml: {file: .gitlab-ci.yml, path: 'build.tags[0]'}}
    agree: [readme, ci]
```

errata builds the prose from the file this way:

- Each paragraph and each heading is one line of the prose, in a list item too, so an item of two
  paragraphs is two lines. The pattern has the `m` flag set, as for `regex`, so `^` and `$` match
  at the start and end of each.
- A line wrap is a space. A tag of inline HTML is a space when a browser starts a line, a cell or
  a rule at it: `address`, `article`, `aside`, `blockquote`, `br`, `caption`, `center`, `dd`,
  `details`, `dialog`, `dir`, `div`, `dl`, `dt`, `fieldset`, `figcaption`, `figure`, `footer`,
  `form`, `h1` to `h6`, `header`, `hgroup`, `hr`, `legend`, `li`, `main`, `menu`, `nav`,
  `noscript`, `ol`, `optgroup`, `option`, `p`, `pre`, `search`, `section`, `summary`, `table`,
  `tbody`, `td`, `tfoot`, `th`, `thead`, `tr` and `ul`, opening or closing, in any case. The
  name must match whole, so a custom element such as `<h1-x>` is inline. Any other tag and a
  comment join the text on either side, so `b<i>uild</i>er` reads `builder` and
  `H<sub>2</sub>O` reads `H2O`.
- A run of spaces, tabs, line feeds, form feeds and carriage returns is one space, and a line of
  prose neither starts nor ends with one.
- Code span backticks, emphasis and strong markers, and link destinations are dropped. A link
  keeps its text, an image its alt text and an autolink its address.
- An HTML block is read as HTML. A line starts one when it starts with a comment, with a block
  tag such as `<div>` or `<p>`, or holds one tag and nothing else. The block runs to the next
  blank line for a tag, and to its end for a comment, a `<script>`, `<pre>`, `<style>` or
  `<textarea>` element, a processing instruction, a declaration or CDATA.
  - A block that a blank line ends, and a `<pre>` or `<textarea>` block, is prose as the `html`
    extractor reads a page ([`html`](#html)): each tag of the elements above
    that a browser starts a line at ends a line of prose, any other tag joins the text around
    it, and a `<pre>` or `<textarea>` keeps its lines. A comment, a `<script>`, a `<style>` and
    a `<template>` inside the block show nothing. An entity stays as written, as in the rest of
    the prose. `<div>\nCI runs on node builder\n</div>` reads `CI runs on node builder`. Each
    block is read alone, so a `<pre>` ends at an end tag of an element the block opened around
    it, such as `<details><pre>log</details>`. An end tag in a `<pre>` of an element the block
    did not open, such as a `</div>` whose `<div>` is in another block, may or may not end the
    pre in a browser, and the file is refused at its line.
  - Of a comment, a `<script>` or `<style>` block, a processing instruction, a declaration and
    CDATA, only the text after the end on its line is prose, as a browser shows it:
    `<!-- marker --> CI runs on node builder` reads `CI runs on node builder`. `<!-->` and
    `<!--->` are whole comments. Tags and comments in that text follow the rule above.

  Until v0.2.5 the first kind was left out of the prose too; a pattern that matched only the
  prose around such a block may now match inside it.
- Fenced and indented code blocks are left out, and so is front matter: a block that starts the
  file (after a UTF-8 byte order mark, if any) with a line of `---` and ends at a line of `---` or
  `...`, or starts and ends with `+++`. Without its closing line it is no front matter.
- With `code: true`, code blocks, HTML blocks and front matter are in, each line as the block
  holds it, without its fences or the markers and indent of the blocks around it.
- A backslash escape and an entity such as `&amp;` stay as written.
- CRLF and lone CR line endings read as LF.
- errata runs no Markdown extensions, so a GitHub table is a paragraph whose rows are joined with
  spaces, pipes and delimiter row included: `runs on \| (\w+)` reads a cell. The `markdown`
  extractor reads a table by its cells ([`markdown`](#markdown)).

The value is the prose's text of group 1: `node *builder*` wrapped over two lines gives
`node builder`. Its line is the line of the file where the match starts.

goldmark, the parser, is quadratic in the file on some shapes of input, such as an unclosed HTML
comment on every line, and nothing outside it can stop it. errata looks at the clock, and counts
what the parse has built, on every call goldmark makes into its parsers, and refuses a file past
any of the bounds under [Limits](#limits). Each refusal makes the rule unknown and names the bound.

### `comment`

Agents put the same facts in code comments as in Markdown: a port in a comment above a constant,
a "keep in step with" note naming another file. The `comment` extractor matches `pattern` against
the comments of one source file. A lexer finds them, driven by a table with one row per language:
it knows each language's comment delimiters and its string literal forms, so `// port 80` inside a
string is no comment. It parses nothing else.

| Field | Required | Meaning |
|---|---|---|
| `file` | yes | the source file |
| `pattern` | yes | an RE2 pattern with a capture group, as for `regex`; group 1 is the value |
| `language` | no | the row of the table to read the file with; default the one the file's name chooses |
| `max_bytes` | no | the largest file read (see [Limits](#limits)) |

```yaml
checks:
  - id: server-port-agrees
    reason: >
      The comment above the listener tells readers which port the server takes. If the chart
      moves the port and the comment stays, the next person configures the old one.
    scope: repo
    extract:
      code:  {comment: {file: internal/server/server.go, pattern: 'listens on port (\d+)'}}
      chart: {yaml: {file: deploy/values.yaml, path: service.port}}
    agree: [code, chart]
```

The file's base name chooses its language, then its longest extension that a row lists, in any
case. A name no row lists, such as `README.md` or `Dockerfile.prod`, is a load error that names
the file; set `language:` to read it as one of the rows. The rows:

| Family | Language | Files | Comments | Strings the lexer skips |
|---|---|---|---|---|
| C-like | `go` | `.go` | `//`, `/* */` | `"..."`, `'.'`, `` `...` `` |
| | `c` | `.c`, `.h` | `//`, `/* */` | `"..."`, `'.'` |
| | `cpp` | `.cc`, `.cpp`, `.cxx`, `.c++`, `.hh`, `.hpp`, `.hxx`, `.h++` | `//`, `/* */` | `"..."`, `'.'`, `R"d(...)d"` |
| | `java` | `.java` | `//`, `/* */` | `"..."`, `"""..."""`, `'.'` |
| | `kotlin` | `.kt`, `.kts` | `//`, `/* */` nested | `"..."`, `"""..."""` (closed by the last three of a run of quotes), `'.'` |
| | `javascript` | `.js`, `.mjs`, `.cjs`, `.jsx` | `//`, `/* */` | `"..."`, `'...'`, `` `...` ``, `/.../` |
| | `typescript` | `.ts`, `.mts`, `.cts`, `.tsx` | `//`, `/* */` | `"..."`, `'...'`, `` `...` ``, `/.../` |
| | `php` | `.php` | `//` and `#` to the line's end or `?>`, `/* */` | `"..."`, `'...'`, `` `...` ``, heredocs `<<<TAG` and `<<<'TAG'`; the text outside `<?php ... ?>` |
| | `groovy` | `.groovy`, `.gradle`, `.gvy` | `//`, `/* */` | `"..."`, `'...'`, `"""..."""`, `'''...'''`, `$/.../$`, `/.../` |
| | `scala` | `.scala`, `.sc`, `.sbt` | `//`, `/* */` nested | `"..."`, `"""..."""` (closed by the last three of a run of quotes), `'.'` |
| | `rust` | `.rs` | `//`, `/* */` nested | `"..."`, `r#"..."#`, `'.'` |
| | `swift` | `.swift` | `//`, `/* */` nested | `"..."`, `"""..."""`, `#"..."#`, `#"""..."""#` |
| | `csharp` | `.cs` | `//`, `/* */` | `"..."`, `@"..."`, `"""..."""`, `'.'` |
| | `css` | `.css` | `/* */` | `"..."`, `'...'` |
| Hash | `python` | `.py`, `.pyi` | `#` | `"..."`, `'...'`, `"""..."""`, `'''...'''`, each with `f`, `rf` or `fr` before it |
| | `shell` | `.sh`, `.bash`, `.zsh`, `.ksh` | `#` at the start of a word | `"..."`, `'...'`, `$'...'`, and `\#` |
| | `ruby` | `.rb`, `.rake`, `.gemspec`, `Rakefile`, `Gemfile` | `#`, `=begin` to `=end` | `"..."`, `'...'`, `` `...` ``, `/.../` over lines, percent literals such as `%q(...)`, `%w[...]` and `%r{...}`, heredocs `<<~TAG`, `<<-TAG` and `<<TAG`; the data after `__END__` |
| | `elixir` | `.ex`, `.exs` | `#` | `"..."`, `'...'`, `"""..."""`, `'''...'''`, sigils such as `~s(...)` and `~S"""..."""` |
| | `perl` | `.pl`, `.pm` | `#` after a blank, `;`, a brace or a parenthesis, POD to `=cut` | `"..."`, `'...'` |
| | `yaml` | `.yaml`, `.yml` | `#` after a blank | `"..."`, `'...'` after a blank, `[`, `{` or `,`; a `\|` or `>` block scalar |
| | `toml` | `.toml` | `#` | `"..."`, `'...'`, `"""..."""`, `'''...'''` |
| | `dockerfile` | `Dockerfile`, `Containerfile`, `.dockerfile` | `#` at the start of a line | none |
| | `makefile` | `Makefile`, `makefile`, `GNUmakefile`, `.mk`, `.mak` | `#` | `"..."`, `'...'`, and `\#` |
| | `hcl` | `.tf`, `.tfvars`, `.hcl` | `#`, `//`, `/* */` | `"..."` |
| SQL and Lua | `sql` | `.sql` | `--`, `/* */` | `'...'`, PostgreSQL's `E'...'` and `$tag$...$tag$`, and the names `"..."`, `` `...` `` and `[...]` |
| | `lua` | `.lua`, `.rockspec` | `--`, `--[[ ]]` | `"..."`, `'...'`, `[[...]]` |
| HTML and XML | `html` | `.html`, `.htm`, `.xhtml` | `<!-- -->` | attribute values after `=` |
| | `xml` | `.xml`, `.xsd`, `.xsl`, `.xslt`, `.svg`, `.plist`, `.csproj`, `.props`, `.targets`, `.xaml` | `<!-- -->` | attribute values after `=`, `<![CDATA[...]]>` |

A `'.'` is a character literal: one character or one escape, so a Rust lifetime such as `'a`
opens no string. A run of `#`, `=` or quotes in a raw or long string, as in `r##"..."##`,
`[==[...]==]` or C#'s `""""...""""`, must close with the same run. A run longer than 16, in any of them or in a C++ raw string's
delimiter, or a PostgreSQL dollar quote's tag longer than 63 bytes, makes the rule unknown, as a
string that runs to the end of the file does (below). A YAML block scalar's body is
the lines after its `|` or `>` that are blank or indented as far as its first line, or as far as
an indentation indicator such as `|2` says, which must be further than the key or list entry it
belongs to; a `---` or `...` line ends it. A comment on the
line of the `|` itself is part of the scalar and is not read.
A heredoc's body is the lines after the line it opens on, up to a line that holds its tag alone,
after blanks for Ruby's `<<~` and `<<-` and for PHP, whose closer may have code after it, as in
`EOT;`. The rest of the opener's line is code, so a comment there is read, and a second heredoc
opened on the line has its body after the first one's closer. Ruby's `<<` starts a heredoc only
before an upper case letter, `_` or a quote, so `a << b` is a shift. A tag past 63 bytes opens
none, and `arr <<FOO` with no closer line makes the rule unknown, as Ruby reads it after a method
name. A heredoc with no closer, or a comment or string opened after it on its line that runs past
the line's end, makes the rule unknown. An Elixir sigil is `~` and a letter, or several upper case
letters, then a body between parentheses, brackets, braces, angle brackets, quotes, bars or
slashes, or three quotes over lines.
The code inside an interpolation, JavaScript's `` `${...}` ``, Ruby's and Elixir's `#{...}`,
Python's f-string `{...}` (where `{{` is a brace, and a `:` or `!` with no other bracket open and
no `=` after it starts a format spec, which holds no code but its own `{...}` fields, as in
`f"{n:#x}"` and `f"{x:{w}}"`), Kotlin's and Groovy's `${...}`, Scala's in an `s`, `f` or `raw`
string, Swift's
`\(...)` and `\#(...)`, and PHP's `{$...}` and `${...}`, is read as the language's code, up to the
bracket that closes it, at any depth up to 256: a string or a comment inside it is one, and its
quote does not close the string around it. Code that never closes makes the rule unknown, as a
string that runs to the end of the file does, and so does a heredoc whose code closes past its
closer's line. No other language writes a format spec inside its interpolation. A Scala string
with another prefix, a custom interpolator such as `sql"..."`, is read as a plain string, whose `${`
is text. A regular expression `/.../` opens only where a value starts, after `=`, `(`, `,`, `:`,
`[`, `!`, `&`, `|`, `?`, `{`, `;` or `~`, or a word such as `return`, `case` or `typeof`, past the
blanks and line ends before it, or at the start of the file. In Ruby and Groovy it opens after an
operator too, after `if`, `unless`, `when`, `and`, `or` and the other words a value follows, and at
the start of a line that no backslash joins to the line before, since a line end ends the statement
there, but not straight after the `:` of a symbol or the `?` of a character, as in `:/` and `?/`.
After a word that may name a method or a variable, `p`, `puts`, `print` or `assert`, it opens only
with a blank before it and none after it, as Ruby decides it: `p /x/` is a regex and `p / 2` a
division; Ruby's percent literals follow the same rule. A `/`
in its `[...]` closes nothing; anywhere else `/` divides, so a division
continued on the next line, as in `(1\n  / 2)`, stays one. Ruby's and Groovy's may run over lines,
as one written with Ruby's `x` flag does; JavaScript's ends at its line. In a Python f-string a
backslash takes no brace: `rf'\{{'` is a backslash and a brace. Ruby's percent literals, `%` or
`%` and one of `q`, `Q`, `r`, `w`, `W`, `i`, `I`, `s`, then a body between brackets, which nest, or
between `|`, `!` or `/`, open where a value starts; a `%` after a value is the operator. A PHP file's text before its first
`<?php`, `<?=` or `<?` and a blank, and from each `?>` to the next of them, is not code: a URL or
an apostrophe in its HTML opens no comment and no string, and `<?xml` stays text, as it does in PHP
with `short_open_tag` off. Ruby's `__END__` alone on a line makes the rest of the file data,
which neither the comment nor the literal extractor reads.
Markdown's HTML comments are read by Markdown's own extractor, not this one.

errata builds the text the pattern searches this way:

- Each comment is its body, without its delimiters: `// port 80` reads ` port 80`, and a block
  comment keeps its lines as written, a leading `*` on each included.
- The comments follow one another in the file's order with a line feed between two, so `\n` or
  `(?s)` in a pattern can match from one comment into the next, as a comment wrapped over
  several `//` lines needs. The `m` flag is set, as for `regex`.
- CRLF and lone CR line endings read as LF.

The value is group 1's text; its line is the file's line where the match starts.

A comment or string that never meets its closer and runs to the end of the file makes the rule
unknown. No file its compiler accepts ends that way, so it means the lexer lost its place, and from
there it could read code as comment. The message names the line where the comment or string
opened: `a.js: line 2: a comment opens here and runs to the end of the file, so the lexer lost its
place ...`. Set the rule aside for that file, or move the construct below.

The lexer reads one language at a time and does not understand these. Where one holds a quote or
a comment's opener, a match can be wrong, or the rule unknown as above:

- A shell heredoc, a Ruby `?"` or percent literal with a delimiter other than a bracket, `|`, `!`
  or `/` (`%q^...^`), a Perl `q(...)`, `qw(...)` or regex, Perl's
  variables `$"` and `$'`, a regular expression after a value's end, such as `if (x) /re/`, which
  reads as a division, an HCL heredoc: their
  text is read as code. A `#` or `//` inside one reads as a comment. A quote inside one opens a
  string, and shell, Ruby and Perl strings run over lines, so strings and comments change places
  up to the next quote: a real comment there is missed and a `#` inside a string is matched. An
  apostrophe in a heredoc's usage text is enough. When no later quote closes the string, the rule
  is unknown.
- Code inside a string of C#'s `$"{...}"`, HCL's `${...}` or a shell `"$(...)"`: the string is
  skipped up to the first quote of its own kind. A quote of that kind inside the code ends the
  string there, and what follows reads as code: in `$"{d["// k"]}"` the text ` k"]}"` is a
  comment. The rows above that interpolate read their code, as said before this list.
- JSX and TSX text between tags is read as code: a `//` in it, as in a URL, starts a comment, and
  an apostrophe, as in `Don't`, opens a string to the end of its line, which hides a comment there.
  The literal extractor refuses such a file, since that string ends unclosed. A JSX comment written
  `{/* ... */}` reads right.
- A line comment continued with a backslash at the end of the line, as C allows: the next line
  is code.
- Groovy's slashy string `/.../`, which reads as code, so a `//` in it is a comment; Elixir's
  character `?"`, which opens a string; and PHP's attribute `#[...]`, which reads as a comment to
  the end of its line.
- SQL dialects: MySQL's backslash escape in `'...'` and its `#` comments are not read, so
  `'it\'s -- no'` ends at `\'` and `-- no` reads as a comment. A block comment never nests, though
  PostgreSQL nests them. A block comment nests in Kotlin, Rust and Swift.
- A YAML plain scalar with a quote after a blank, such as `a: x 'y # z`, reads the quote as a
  string's start. In a Makefile, make ends a variable's value at `#` even inside quotes, and the
  lexer reads the quotes as a string.
- The content of a `<script>` or `<style>` element is read as HTML: a `//` comment there is not
  read, and a `<!--` in a JavaScript string there is.
- Another language embedded in a string of the file, such as SQL in a Go string: the string is
  skipped, so its comments are not read. A YAML block scalar is a string, so a shell comment in a
  CI step's `run: |` is not read either.

The lexer is one pass over the file: every fence is at most 16 bytes, or 63 for a dollar quote's
tag, whose bytes start no other fence, so each byte is looked at a bounded number of times. The search has the time limit of `regex`.

### `literal`

Configuration is often a program: a Django `settings.py`, a Laravel `config/app.php`, a
`vite.config.ts`, a Gradle build, a `Package.swift`, an Elixir `config.exs`. The `literal`
extractor reads the values such a file writes as literal data, without running it, without an
interpreter and without importing anything ([ADR
0002](adr/0002-errata-reads-configuration-and-never-runs-it.md)). It reads the file into a tree
and selects from it with the [path](#paths) `yaml`, `json` and `toml` use.

| Field | Required | Meaning |
|---|---|---|
| `file` | yes | the source file |
| `path` | yes | the path of the value in the file's literal data (see [Paths](#paths)) |
| `language` | no | the language to read the file as; default the one the file's name chooses |
| `max_bytes` | no | the largest file read (see [Limits](#limits)) |

```yaml
checks:
  - id: db-port-agrees
    reason: >
      Django connects to the port its settings name, and the chart opens the port the database
      listens on. When they differ, the app starts and every request fails to connect.
    scope: repo
    extract:
      settings: {literal: {file: app/settings.py, path: DATABASES.default.PORT}}
      chart:    {yaml: {file: deploy/values.yaml, path: postgresql.port}}
    agree: [settings, chart]
```

It reads Python, Ruby, JavaScript, TypeScript, PHP, Groovy, Kotlin, Scala, Swift, Elixir and Lua,
each through its row of the [comment](#comment) extractor's lexer table, which knows its comments
and string forms and how the language spells literal data. One grammar serves them all, so a
language is one row of the table.

**The tree.** The statements at the top of the file make a mapping:

- `name = value` sets the key `name`; a declaration word before it is left out (`const`, `let`,
  `var`, `export`, `val`, `local`, `def` and the like), and so is a type after a `:` (`X: int = 1`,
  `val v: String = "a"`). Scala's `:=` assigns as `=` does, for an sbt build, and PHP's `$` is
  not part of the name. `a.b.c = value` sets `c` in the mapping `a.b`, as a gemspec's
  `s.version = "1.0"` does. A name assigned twice at the top takes its last value.
- A call `name(...)`, or a call without parentheses in Ruby, Groovy, Elixir and Lua
  (`gem "rails"`, `implementation 'g:a:1'`, `config :app, port: 4000`), adds the call to the list
  under `name`, so `gem[*]` is every gem. A call is a mapping from each keyword argument's name, and
  from each positional argument's place counted from 0, to the argument:
  `setup(name="a", version="1.0")` is read with `setup[0].version`, and `gem "rails", "~> 7.1"`
  with `gem[0]['1']`. A place is a key, so it takes quotes: `['0']`, never `.0`.
- `return value` in PHP and Lua sets the key `return`, so a Laravel config file's
  `return ['name' => 'x']` is read with `return.name`. JavaScript's `export default value` sets
  `default`, and `module.exports = value` sets `module.exports`.

A value is a string, a number as the file wrote it (`0x1F`, `1_000`, `-2.5`), a boolean, a null
(`None`, `nil`, `null`, `undefined`, as the file wrote it), a list, a mapping, a call, or in Ruby
and Elixir a symbol, read as its name (`:app` is `app`), and in Elixir an alias such as
`MyApp.Repo`. Lists and mappings are each language's: `[...]`, `{...}`, a Python tuple, PHP's
`array(...)`, Elixir's `%{...}` and keyword lists, Lua's tables, Groovy's and Swift's `[a: 1]` and
`[:]`. A string is its text with its escapes decoded, Python's adjacent strings and Ruby's joined.
Each language decodes its own escapes: `\n`, `\t`, the quotes and the others it lists, `\x`, and
`\u` in the forms it writes (four digits, or braces in JavaScript, Ruby, PHP, Swift, Elixir and Lua).
An escape the language does not decode makes the string not literal, below.
A heredoc, a Swift or Elixir `"""` string, or PHP's heredoc takes off the indentation the language
takes off; Ruby's `<<~` takes off the indentation its lines share.

**Values a program computes.** Anything else is a value marked not literal, with the text the file
wrote (see [Values a file only computes](#values-a-file-only-computes)): a reference to a name, an
operator (`"v" + VERSION`, `1 if DEBUG else 2`), a method called on a value (`"""a""".trimIndent()`;
Ruby's `.freeze` straight after a string literal is the one exception: `"1.2.3".freeze` is the
string `1.2.3`, since it returns the string it is called on, while `"1.2".dup` and
`"1.2".freeze.upcase` are not literal),
a string with code inside it (`f"{x}"`, `"#{x}"`, `` `${x}` ``, `"$x"`, `"\(x)"`; in Scala `${x}` in
any string, since the lexer does not see its `s` prefix), a string with an escape the language does
not decode as errata does (an octal escape, `\x` past ASCII, which is a character in Python and a
byte in Ruby, one the language keeps as written, such as Python's `\d` and `\e`, one it reads as
the letter alone, such as JavaScript's `\a`, and `\u` in a Python bytes literal), a command in
backquotes, a regular expression or another sigil that is no string, Ruby's `%w`, `%i`, `%s` and
`%r` literals, a call with an argument that
is not literal, a mapping that names a key twice, a key that is a variable, and a list that mixes
items and entries. A call with literal arguments is the call's data, never what it returns:
`env('APP_NAME', 'Laravel')` is a mapping whose `['1']` is `Laravel`. A path that selects a call or
a list is an error, as it is for `yaml`. A filter that would compare a value marked not literal
makes the rule unknown, and a filter that only tests that a key exists reads it.

A name the file changes after it assigns it is marked not literal too, since the text it wrote is
not the value it ends with: assigned again inside a block, a function or a condition; assigned
through an element or a member (`D['k'] = 1`, `A.b = 1`), or with an operator such as `+=` or
Ruby's `<<`; a method called on it (`APPS.append('x')`), and in Ruby, Groovy, Elixir and Lua,
which call without parentheses, any member of it (`LIST.push 'b'`); handed first to a function
that changes it, which only Lua names (`table.insert(deps, 'b')`, `table.remove`, `table.sort`,
`rawset`); bound again by a word such as `def`, `for`, `import` or Python's `case`; and in Python,
every name a `from m import *` after it could replace. Reading the name elsewhere changes nothing.
The reader cannot tell a mutating method from one that only reads, so `NAME.upper()` marks `NAME`
too. It does not watch a name handed to any other function: `register(APPS)` leaves `APPS` as the
file wrote it, though the function may change it.

**What it does not read.** It reads the top of the file only: a statement inside a function, a
class, a module or any block is not a value, though an assignment there makes the name it assigns
not literal. A Ruby gemspec's `Gem::Specification.new do |s| ... end`, an Elixir `defmodule`, a
Gradle `android { ... }` block and a Python class body hold no values the reader reads. A
statement it does not know, such as `if`, `def` or `import`, is skipped. In Python a statement that
starts its line further in than the first column is part of a block; one after a `;` is read with
the statement before it. Ruby's `__END__` alone on a line ends the code: the data after it is not
read. A PHP file's text outside `<?php` (or `<?=`, or `<?` and a blank) and `?>` is not code; a file that holds
more than blanks there makes the rule unknown, since its code is not all it says. A `//` or `#`
comment ends at `?>`, as PHP ends it.

It reads the string forms the [comment](#comment) extractor's lexer knows, the code inside an
interpolation and regular expressions where a value starts included. The constructs the lexer does
not understand there can hide a value, make the rule unknown, or make the reader take a string's
text for code: Ruby's `?"` and a percent literal with any other delimiter, Elixir's `?"`, a
regular expression after a value's end, such as `)`, or after JavaScript's `of` in a `for (x of
/re/...)`, each of which reads as a division, and JSX text with an apostrophe. In Ruby and Groovy, whose regular expressions run over lines, such a construct can
also hide the lines up to the next `/`, which a `/` the lexer takes for an opener reads as a
regular expression. A block that never ends, a bracket that never
closes or closes another, a string that runs to the end of the file, one that ends unclosed at its
line's end (as one does whose line Lua's `\z` runs on), and code in an interpolation that never
closes make the rule unknown, and the message names the line: `settings.py: line 4: a bracket
opens here and never closes, so the literal reader lost its place ...`. Ruby's `if`, `unless`,
`while` and `until` after a value are modifiers and open no block.

The read is one pass over the file, under the node limit and the depth limit of `toml` (see
[Limits](#limits)): a dotted name of more than 1000 parts is refused as a bracket nested that deep
is. Setting a name again costs as setting it the first time, so a file that reassigns one name
many times reads in time linear in its size.

### `code`

A version constant in `version.go`, `__version__` in `__init__.py`, a default port in `main.go` or
`constants.ts` state the same fact the docs, the manifests and the CI files repeat. The `code`
extractor reads the value one name of a source file has, without compiling or running the program
([ADR 0002](adr/0002-errata-reads-configuration-and-never-runs-it.md)), and returns it as one value
at the line the value is written on.

| Field | Required | Meaning |
|---|---|---|
| `file` | yes | the source file |
| `name` | one of `name` and `const` | the name whose value to read: a top level name such as `__version__`, or a class's member as `Config.PORT` |
| `const` | one of `name` and `const` | a name as `name` takes it, which the file must declare a constant |
| `language` | no | the language to read the file as; default the one the file's name chooses |
| `max_bytes` | no | the largest file read (see [Limits](#limits)) |

```yaml
checks:
  - id: port-agrees
    reason: >
      The server listens on its default port unless told otherwise, and the chart routes traffic
      to the port it names. When they differ, every request reaches a port nothing listens on.
    scope: repo
    extract:
      server: {code: {file: internal/server/server.go, const: DefaultPort}}
      chart:  {yaml: {file: deploy/values.yaml, path: service.port}}
    agree: [server, chart]
  - id: version-agrees
    reason: The package reports the version its manifest publishes it under.
    scope: repo
    extract:
      module:   {code: {file: src/app/__init__.py, name: __version__}}
      manifest: {toml: {file: pyproject.toml, path: project.version}}
    agree: [module, manifest]
```

It reads Go, and every language the [`literal`](#literal) extractor reads, Java besides. A file
that declares no such name gives no value, which makes the rule unknown unless `allow_empty` lists
the extractor. A name that holds a list, a mapping or a class is an error: name one of its members,
or read it with `literal` and a path.

**Go.** A Go file is read with the standard library's `go/parser`, and a constant is evaluated with
`go/constant` by the rules the compiler follows. The name is a `const` or a `var` at the top of the
file:

- A constant written as one literal number is its text as the file wrote it (`0x1F`, `1e6`,
  `1_000`, and a float's `0.10`, whatever its type rounds it to); a rune written as one literal is
  its character, a string (`','` is `,`, `'\n'` a line feed), while it is untyped or of type
  `rune` or `int32`, or a type the file declares over one of those (`type Delim rune`); given
  another number type it is that number in decimal (`const I int = '0'`
  is `48`, `const F float64 = 'a'` is `97`); a string is its text with its escapes decoded; a bool
  is `true` or `false`.
- A constant the compiler works out is its value: an `iota` block counts each spec from 0, a spec
  with no value repeats the one before it with its own `iota`, an expression of constants
  (`1 << 10`, `Version + "-rc1"`, `7 / 2`) is evaluated, and a typed constant
  (`const Admin Port = 9000 + iota*10`, `type Port int`) is held to its type's range and rounded
  to a float type's precision. An integer is written in decimal, and so is a float that holds an
  integer under `1e21`; any other float is written in Go's shortest form, `3.5` or `1.5e-07`. A
  rune the compiler works out, such as `'a' + 1`, is its number, `98`. `int`, `uint` and `uintptr`
  have 64 bits. An alias (`type T = int`) is the type
  it names, and so are `byte` and `rune`, `uint8` and `int32`.
- A `var` with a constant value is that value with its default type (`int`, `float64`,
  `string`): `var Name = "errata"` is `errata`.

Not literal, with the text the file wrote: a `var` the file assigns anywhere, increments, or takes
the address of (`Name = "x"`, `Count++`, `&Name`), even inside a function where a local of that
name stands; a `var` with no value, or one whose value is a call (`os.Getenv("ENV")`), a slice, a
map or a struct; a constant that refers to a name the file does not declare, such as one in
another file of the package or another package (`5 * time.Second`); a call, a builtin such as
`len`, a conversion to a type of another package, and `string(65)`; and a constant the compiler
refuses: one past its type's range, a division by zero, two types mixed, a cycle, a name declared
twice, an imaginary number. The reader reads the one file a rule names, so a program can still set
a `var` that another file of the package assigns, in an `init`, a flag's set up or a setter, or
that holds a string set at link time (`go build -ldflags "-X main.version=1.2"`), which no file
shows: read a value that must not change with `const:`. A Go name has no dot.

A file `go/parser` refuses makes the rule unknown, and the message names the line of its first
error. So does an evaluation that goes more than 1000 levels deep, counting each operator,
parenthesis and constant it passes through, and a string constant that would be longer than the
file, which a few lines that each join a string to itself would build. The evaluation's work is
bounded too (see [Limits](#limits)): each expression it evaluates costs a unit, and each
comparison of two strings the bytes of both, and a file past its budget makes the rule unknown.
The specs that repeat an expression that reads no `iota` evaluate it once.

**Other languages.** Any other file is read with the literal reader's grammar and table (see
[`literal`](#literal)), with the classes added. The name is a module level name, such as an
assignment, `export const X = ...` or a `val`, or a member of a class, an object or a module,
written after the class's name with a dot:

| Language | A class whose members it reads | A member |
|---|---|---|
| Python | `class` | `PORT = 8080`, `PORT: int = 8080` |
| Ruby | `class`, `module` | `VERSION = "1.0"` |
| JavaScript, TypeScript | `class` | `static PORT = 8080`, `private static readonly PORT: number = 8080` |
| PHP | `class`, `interface`, `trait`, `enum` | `public const VERSION = '2.1'`, `public static $port = 80` (named `Config.port`, without its `$`), `private ?int $x = null` |
| Java | `class`, `interface`, `enum`, `record` | `public static final String VERSION = "3.4.1";`, `int retries = 3;` |
| Groovy | `class`, `interface`, `enum`, `trait` | `static final String VERSION = '1.0'`, `def name = 'b'` |
| Kotlin | `object`, `class`, `interface` | `const val PORT = 8080`, `var mode = "dev"` |
| Scala | `object`, `class`, `trait` | `val Version = "0.9"` |
| Swift | `class`, `struct`, `enum` | `static let port = 8080` |

A class inside a class is read under both names, `Version.Inner.N`. A Kotlin `companion object`'s
members are its class's, `App.NAME`. An annotation such as `@Deprecated` or `@dataclass(...)`
before a member or a class is skipped. A method, and anything else in a class's body that assigns
nothing, holds no value. A member a statement in the class's body changes is not literal, and is
no constant, as a name the file changes is for `literal`: assigned again in a method, a
constructor or a static block, `X += 1`, `X << 'b'`, and through the name a method reaches its
class by: `this.port = p` (JavaScript, TypeScript, Java, Groovy, Kotlin, Scala), `self.port = p`
(Swift, Ruby), `self.PORT = p` and `cls.PORT += 1` (Python), `$this->port = p`, `self::$port = 2`
and `static::$port++` (PHP). A method called through one of these names changes the member only
when it is a method that changes what it is called on: `self.ITEMS.append(x)`,
`this.list.push(x)`, `this.names.add(x)`, `self.D.update(...)` and the like for each language,
while `self.HOST.rstrip('/')` and `this.name.equals(x)` read the member and leave it literal, and
`self.f()` calls a method of the class. A method called on any other name makes the name not
literal, as for `literal`. So a TypeScript `readonly` field a constructor assigns is not literal,
and `const:` refuses it. The reader cannot tell a local from a member: a local of the member's name
assigned in a method (`String V = "x";`, `val x = 2`) makes the member not literal too. A change
through any other name (`other.port = 2`, or a reference handed to a function) is not watched. A class the file changes as a whole
(`Config.X = 2` outside it, or `A.X = 2` in one of its methods) is not literal, and so is every
member read through it. A Ruby string literal followed by `.freeze` is the string
(`VERSION = "1.2.3".freeze`), as for `literal`.

These hold no value the reader reads: a member declared with no value, such as Java's
`static final int X;` that a static block sets, or Python's `X: int`, which gives no value rather
than one not literal; a Java text block, which is not literal, since errata does not take off its
indentation as Java does; a Python class written on one line (`class A: X = 1`); Scala 3's
indented `object A:`, which reads neither `A.X` nor `X`; a Ruby `class << self`; the declarators
after the first of one statement, as Java's `int A = 1, B = 2;` (which makes `A` not literal and
reads no `B`) and PHP's `const X = 1, Y = 2;`; and a Kotlin property with a getter
(`val Y get() = 2`). Everything else `literal` says about a value a program computes, and about the
forms its lexer does not understand, holds here.

**const.** `const:` reads the name as `name:` does, and refuses a name the file does not declare a
constant, so a rule fails as unknown when someone turns the constant into something the program
may change. A constant is a Go `const`; a JavaScript or TypeScript `const`, or TypeScript
`readonly`; a PHP `const`; a Java or Groovy `final`, and every field of a Java `interface`, which
Java makes `static final`; a Kotlin `const` or `val`; a Scala `val`; and a Swift `let`. A constant
the class's body or one of its methods changes is none. Python, Ruby, Elixir and Lua declare no
constant, so `const:` does not load for their files.

The read is one pass over the file, under the node limit and the depth limit of `toml` (see
[Limits](#limits)): classes nested more than 1000 deep are refused as brackets are. A Go file is
held to a token limit as well, before `go/parser` builds its syntax.

### `markdown`

Docs keep facts in a Markdown file's structure as well as in its sentences: a marker in an HTML
comment, a requirement under one heading, a command in a fenced block, a port in a table. A test that
greps the file for them finds the same text in the wrong place. The `markdown` extractor parses the
file as the `text` extractor does, and reads one `target` by its structure:

| Field | Required | Meaning |
|---|---|---|
| `file` | yes | the Markdown file |
| `target` | yes | `comment`, `section`, `code` or `table` |
| `section` | for `section` | a heading path such as `Install > Requirements`; with any other target, it keeps only what lies in those sections |
| `pattern` | no | an RE2 pattern with a capture group, as for `regex`; without it a value is the whole comment, section, block or cell |
| `language` | no, `code` only | the language a fenced block must have, the first word of its info string, such as `bash` |
| `column` | for `table` | the header of the column whose cells are the values |
| `key`, `row` | no, `table` only, both or neither | keep only the rows whose cell under the header `key` is `row` |
| `max_bytes` | no | the largest file read (see [Limits](#limits)) |

```yaml
extract:
  marker:   {markdown: {file: README.md, target: comment, pattern: 'errata:version (\S+)'}}
  go:       {markdown: {file: README.md, target: section, section: Install > Requirements, pattern: 'Go (\d+\.\d+)'}}
  install:  {markdown: {file: README.md, target: code, language: bash, section: Install}}
  web_port: {markdown: {file: docs/hosts.md, target: table, column: Port, key: Host, row: web}}
```

- **comment.** Each HTML comment, `<!--` to `-->`, in an HTML block or inline in text. One in a
  code span or a code block is no comment, and neither is one in a `<script>`, `<style>` or
  `<textarea>` block; one in a `<pre>` block is. `<!-->` and `<!--->` are empty comments, and a
  `<!--` with no `-->` after it is none. The value is the comment's text with the blanks at either
  end left out, or the pattern's captures in that text, and its line is where the comment starts.
  A `<!--` inside a tag, such as in an attribute's value, is no comment, as in a browser: a tag
  runs from `<` and a letter or `/` to the first `>` outside quotes. A comment in a block quote
  reads without the quote's `>` markers. In an HTML block a blank line ends, and in a `<pre>` or
  `<textarea>` block, the comments are the ones the `html` extractor reads in the same HTML
  ([`html`](#html)): one in a `<script>`, `<style>`, `<template>` or
  `<textarea>` inside a `<div>` is none, as in a browser.
- **section.** The prose under each heading the path names, to the next heading of the same or a
  higher level, so it holds its subsections, their headings included. The prose is the `text`
  extractor's ([`text`](#text)), code blocks left out and the text of HTML blocks
  in, a `<pre>` block's included. A heading names the path
  when the headings it sits under end with the path's, from the nearest up: `Install >
  Requirements` is a `Requirements` heading whose nearest heading of a higher level is `Install`,
  at any level of the file. A heading's text is its prose, compared whole and in its case, so a
  heading whose text holds `>` cannot be named. Only a
  heading of the document counts, not one in a block quote or a list item, and a `#` line in a
  code block is no heading. A path that names no heading gives no value, which `allow_empty`
  decides on; a path that names two headings gives the values of both. Without a pattern the value
  is the section's prose at its heading's line; with one, each capture is at the line where its
  match starts.
- **code.** Each fenced code block, or each whose language, the first word of its info string, is
  `language`, its lines as the block holds them without the fences. Indented code blocks are not
  read. The value's line is the first line inside the fences, or the opening fence for an empty
  block, and a capture's the line where its match starts.
- **table.** Each GitHub table, as GitHub reads it: a delimiter row such as `|---|:-:|` under a
  header row. The value is the cell under the header `column`, in each row, or in each row whose
  `key` cell is `row`; a cell is read as prose, so `*db*` is `db`, and its line is its row's. A
  `\|` in a cell is a pipe, in a code span too. A table with no such header gives no value, and of
  two headers alike the first counts. A row with fewer cells than the header has an empty cell for
  each one missing.

A match never runs from one comment, section, block or cell into the next. All the matches share
one time limit and one cap on values, the `regex` extractor's for the file. A pattern runs inside
each piece, so `^` and `$` match at its lines. The parse is the `text` extractor's, under the same
bounds; for a table target the parse counts each table's cells against the node limit before it
makes them, and refuses a table that would pad its short rows with more empty cells than it has
bytes (Limits, Markdown table padding). The text the extractor reads may be at most 12 times the size of the
file: sections of one path may nest, and a table's key cells are read twice.

### `asciidoc`

The `asciidoc` extractor reads an AsciiDoc file as the `markdown` extractor reads a Markdown file:
the same fields, the same four targets and the same values, so a rule reads a claim the same way
whichever markup a page uses. No maintained AsciiDoc parser in Go was found, so the reader is
errata's own, written for the common forms of the language below, and it reads each file in one
pass over its lines.

```yaml
extract:
  marker: {asciidoc: {file: README.adoc, target: comment, pattern: 'errata:version (\S+)'}}
  go:     {asciidoc: {file: README.adoc, target: section, section: Install > Requirements, pattern: 'Go (\d+\.\d+)'}}
  web:    {asciidoc: {file: docs/hosts.adoc, target: table, column: Port, key: Host, row: web}}
```

- **comment.** Each line comment, `//` and not `///`, and each comment block between two lines of
  four or more `/`. The value is the text after `//`, or the block's lines, with the blanks at either
  end left out, at the comment's first line. A `//` line in a listing, literal or passthrough block
  is no comment; one in a paragraph, an example, sidebar, quote or open block, or a table is.
- **section.** The prose under each title the path names, to the next title of the same or a higher
  level, its subsections and their titles included. A title is one to six `=` or `#`, a blank and
  its text, with the same marks after it left out (`== Install ==`), on a line that does not go on
  a paragraph and is in no delimited block. A title under `[discrete]` or `[float]`, or in an
  example or sidebar block, is prose and starts no section. The prose is the section's lines less
  comments, attribute lines, attribute entries, block delimiters and the content of listing,
  literal, passthrough and indented literal blocks; list markers and the formatting marks of
  `*bold*`, `_italic_`, `` `mono` ``, `#mark#` and their doubled forms are left out where they pair,
  as Asciidoctor pairs them: over the lines of one paragraph or list item, a comment line in it
  left out, and never from one into the next. A block title's and a title's marks pair on their
  own line. A table's lines are prose as written, `|` included.
- **code.** Each listing block (`----`), literal block (`....`) and fenced block (```` ``` ````). Its
  language is the second positional attribute of the attribute line above it when the style is
  `source` or empty, `[source,sh]` or `[,sh]`, or the word after ```` ``` ````. Attribute lines one
  after another merge, as in Asciidoctor: `[source,sh]` over `[#id]` is still `sh`, and a later
  style replaces an earlier one. The value is the block's lines as written, at the first of them, or
  at the opening delimiter for an empty block.
- **table.** Each table between two `|===` lines, or `!===` with `!` separating cells. A cell
  starts at each `|`, may run over lines, and loses the cell specifier before its `|`, such as `a`
  in `a|`. A row has as many cells as `cols` gives, or as the table's first line holds. `cols` is
  read as Asciidoctor reads it: a number alone (`cols=3`), or entries split at commas, or at
  semicolons when there is none, quoted with `"` or `'` or not, each one column or `n*` for n, with
  its alignment, width and style (`"2*,<.^1a,<.,20%,~"`); any other form is refused. The header row is
  that first line when a blank line follows it, or the first row under `%header` or
  `options="header"`, and never under `%noheader`; a table with no header row gives no value. A cell is read as prose and its
  value is at the line of its row's first cell. A row the table ends before is left out, as
  Asciidoctor leaves it out.

What the reader does not understand, it reads as the text it is, or refuses:

- Preprocessor directives, `include::`, `ifdef::`, `ifndef::`, `ifeval::` and the rest, are lines of
  prose. No include is followed, local or remote, and every branch of a conditional is read: read an
  included file with an extractor of its own.
- Attribute references such as `{version}` are not replaced, and inline macros, links and
  passthroughs are read as written.
- Two line titles are not read, and a file that holds one is refused, at its underline: the first
  line of a paragraph outside every delimited block, holding a letter or a digit and starting with
  no dot, over a line of two or more of one of `=`, `-`, `~`, `^` or `+`, as many as the line has
  characters or one more or less, as Asciidoctor reads such a title. Inside a block, `Run:` over
  `----` is a paragraph and a listing block, as in Asciidoctor. Without the
  refusal, `Install` over `-------` would open a listing block and read the prose after it as
  code. Write the title on one line, `== Install`.
- A `[source]` paragraph with no delimiters is prose, and an indented literal paragraph is not
  read.
- The table target refuses a table whose cells span columns or rows or repeat (`2+|`, `.2+|`,
  `3*|`), one of comma or colon separated values (`,===`, `:===`, `format=csv`), and one with a
  `separator` of its own.

A delimited block with no closing line is refused, at the line that opened it: Asciidoctor warns
and reads it to the end of the file, so an unclosed `----` would make the rest of the file code.
Blocks nest at most 1000 deep, and a read holds at most one piece, a value, a run of lines or a
table cell, for every 4 bytes of `max_bytes`; past either bound the file is refused (Limits,
AsciiDoc pieces and AsciiDoc nesting). The text the extractor reads is at
most the file's size, and the search shares the `regex` extractor's time limit and cap on values.

### `rst`

The `rst` extractor reads a reStructuredText file by the same targets and fields as the `markdown`
extractor ([`markdown`](#markdown)), so one rule reads a claim the same way
whichever of the two a page is written in:

```yaml
extract:
  marker:   {rst: {file: README.rst, target: comment, pattern: 'errata:version (\S+)'}}
  go:       {rst: {file: README.rst, target: section, section: Install > Requirements, pattern: 'Go (\d+\.\d+)'}}
  install:  {rst: {file: README.rst, target: code, language: bash, section: Install}}
  web_port: {rst: {file: docs/hosts.rst, target: table, column: Port, key: Host, row: web}}
```

No maintained parser of reStructuredText in Go was found, so errata parses the file itself, in one
pass over its lines, and reads the common forms of what docutils reads. A form it cannot read as
docutils does is refused, and the message names its line, rather than read wrong; the rule is then
unknown.

- **comment.** Explicit markup, `..` and a blank, that is no directive (`name::`, or `name ::`
  with one space), hyperlink target (`.. _name:`, `_` and no blank), substitution definition
  (`.. |name| ...`), footnote (`.. [1]`, `[#]`, `[#name]`, `[*]`) or citation (`.. [name]`).
  Its text is what follows the `..` and the lines indented under it, those lines dedented; the
  value is that text with the blanks at either end left out, at the line of its `..`. A `..` alone
  with a blank line after it is an empty comment, whose value is empty, and the indented block
  after it is no part of it. A comment counts wherever rST is read: in a list item, a block quote,
  or the content of an admonition (`attention`, `caution`, `danger`, `error`, `hint`,
  `important`, `note`, `tip`, `warning` and `seealso`) or of `admonition`, `compound`,
  `container`, `only`, `sidebar`, `table` and `topic`. A directive's options, the lines starting
  with `:` right under it and the lines indented more than each that go on it, are none of its
  content.
- **section.** A title is one line at the first column, or anywhere between an overline and an
  underline, under an adornment of one ASCII punctuation character repeated, as wide as the title
  (an East Asian wide character counts two columns) or at least four characters, with an overline
  the same as its underline or none. A line that starts a bullet list item, such as `- foo`, is a
  list item and no title. Its level is the order in which its style, the character and whether it
  has an overline, first appears in the file, as docutils counts it. A title more than one level
  below the section it is in, or of a new style below a section that is not the deepest, is an
  error in docutils, and errata refuses the file. A title that is indented is none; an overline
  shorter than four characters is text, and an overline of four or more that differs from its
  underline is neither title nor prose. The path names titles as it names Markdown headings, and
  the prose under a title runs to the next title of the same or a higher level. The prose is each
  paragraph's lines joined by a space, a block to a line, with the markup below left out; a list
  item's bullet or number is no part of it. Titles of subsections are prose, and so is the content
  of the directives listed under comment; an admonition's text after its `::` and the lines under
  it are the first paragraph of its content. Comments, literal and code blocks, tables, targets
  and other directives are not prose. A paragraph's closing `::` reads as `:`, or as nothing after
  a blank or on its own.
- **code.** Each literal block, the indented block after a paragraph that ends in `::`, and each
  `code-block`, `code` or `sourcecode` directive, whose language is its argument, compared in its
  case: `.. code-block:: bash`. A literal block has no language, so `language` keeps none. The
  value is the block's lines dedented, without its options (`:linenos:`) or the blank lines at its
  end, at the line of its first; an empty directive's is at the directive's line.
- **table.** Each grid table and each simple table that has a header of one row: the row above a
  grid table's border of `=`, or above a simple table's middle border. A table without a header gives no value, since no column is
  named. A cell's lines are joined by a space and read as prose, at its row's first line. A simple
  table's columns start at the runs of `=` of its top border, the last running to the end of the
  line, and a line whose first column is blank goes on the row above it. A grid table's cells are
  cut at the columns of its top border's `+`. Where the table target reads a table, errata refuses
  a grid table with a line those columns do not cut, as where a cell spans columns or rows, and a
  header of more than one row, or with a rule under a header cell that spans columns; any other
  target passes over such a table. A simple table with no bottom border, a border of `=` followed
  by a blank line, a line indented less than the table, as where the next list item follows, or
  the end of the file, is refused whatever the target, since docutils reads no table there and its
  rows would run on into the rest of the file. A column counts one for each
  character and a tab moves to the next multiple of 8, so a table aligned by the width of East
  Asian characters is cut wrong.

Inline markup reads as its text, in titles, prose and cells: ``` ``literal`` ```, `**strong**`,
`*emphasis*`, `` `interpreted text` `` with a role before or after it (`` :ref:`x` ``), and a
reference or a role written with a target, `` `text <target>`_ ``, keep only their text. A
backslash escapes the character after it. Markup starts and ends where docutils lets it, with
ASCII punctuation only: after a blank or an opening punctuation mark, and before a blank or
punctuation. docutils also takes Unicode punctuation there, so `«*x*»` keeps its stars here.

errata does not understand, and reads as written or not at all: any other directive, whose content
it passes over, `include` among them, so an included file is not read; `highlight`, so a literal
block has no language even where Sphinx gives it one; roles and substitutions beyond the inline
markup above, so `name_`, `[1]_` and `|name|` stay as written; quoted literal blocks, doctest
blocks, `csv-table` and `list-table`; and a field or definition list, read as prose.

A match never runs from one piece into the next, and all of them share one time limit and one cap
on values, as for `markdown`. The text read is no longer than the file, give or take a line end.
Sections of one path that nest, `A` under `A`, share it. There are 64 title styles, 32 characters
with an overline or without, so sections nest at most 64 deep, and the pieces the target reads
may be at most 12 times the size of the file in all, checked before a value is copied.

### `html`

A project's docs site or a generated page makes the same claims as its Markdown. The `html`
extractor reads an HTML file with the fields and targets of the `markdown` extractor, so one rule
reads a claim whichever of the two a page is written in:

```yaml
extract:
  marker:   {html: {file: docs/index.html, target: comment, pattern: 'errata:version (\S+)'}}
  go:       {html: {file: docs/install.html, target: section, section: Install > Requirements, pattern: 'Go (\d+\.\d+)'}}
  install:  {html: {file: docs/install.html, target: code, language: bash, section: Install}}
  web_port: {html: {file: docs/hosts.html, target: table, column: Port, key: Host, row: web}}
```

errata reads the file with the tokenizer of
[golang.org/x/net/html](https://pkg.go.dev/golang.org/x/net/html), in one pass, and takes each
line from the offset of a token's bytes in the file. It builds no document tree: it follows the
headings, `pre` elements and tables as they open and close, and the end tags a browser implies,
such as a `<td>` with no `</td>`, end where the next one starts. Text is what a browser shows: a
character reference such as `&amp;` or `&lt;` reads as the character, and a run of blanks is one
space. A CRLF and a lone CR read as LF before anything else, as the HTML standard reads a file, so
a lone CR in a `pre` ends a line. A line feed right after `<pre>` is the tag's, as in a browser,
and CDATA in an `<svg>` or a `<math>` reads as its text. Inside either, errata reads each tag by
the HTML standard's tree construction, in a template's content too. An element there is the svg's
or the math's, and the tags in it are tags, so a `<b>` in an svg's `<style>` ends the svg and the
text after it shows. HTML resumes in an svg's `<foreignObject>`, `<title>` and `<desc>`, in a math
`<mi>`, `<mo>`, `<mn>`, `<ms>` and `<mtext>`, and in an `<annotation-xml>` whose encoding is HTML,
where a `<style>` or a `<textarea>` is HTML's again and its text raw. errata's tests hold it to
golang.org/x/net/html's tree builder, which follows the standard but in four places, where errata
follows the standard: x/net drops a `<template>` opened below an integration point and everything
after it, it leaves open each formatting element past the third that the adoption agency takes off
its list below a block, it matches an end tag in svg or math to its element by Unicode case folding,
so `</test>` closes `<teſt>`, and its caption mode leaves `<th>` out of the tags that end a
`<caption>`. The tests leave pages with a caption out of the comparison. errata compares tag names
byte for byte, with ASCII letters in either case: the standard's match for names in UTF-8, and a
windows-1252 browser's for bytes that are not UTF-8.

errata reads only what a browser draws. In svg and math it leaves out the text of an element a
browser draws nothing of, and of everything inside it: SVG 2's never rendered elements
(`<clipPath>`, `<defs>`, `<desc>`, `<linearGradient>`, `<marker>`, `<mask>`, `<metadata>`,
`<pattern>`, `<radialGradient>`, `<script>`, `<style>`, `<symbol>` and `<title>`), a `<filter>`,
each child of a `<switch>` but the one it renders, a math `<annotation>` or `<annotation-xml>`,
which a `<semantics>` hides after its first child, and an `<mphantom>`. A switch renders its first
child with no `requiredExtensions` or `systemLanguage`, as errata evaluates neither, so a child
with `systemLanguage="en"` that an English browser draws is left out. Text in an
svg shows only inside a `<text>`, `<tspan>`, `<textPath>` or `<foreignObject>`. A heading with a
KaTeX formula reads without the TeX source; KaTeX's default output writes the formula twice, as
MathML and as drawn glyphs, and errata reads both. An element of svg or math is no cell, heading,
`pre` or link, whatever its name. A rebuild of misnested formatting elements can move an svg
`<foreignObject>` out of every `<svg>` in the tree, as `<b><svg><foreignObject><p></b>` does;
a browser draws no svg element outside an svg, and errata reads its HTML.

errata follows the elements as a browser's parser does, from the start of the file, so a
`<pre>` or a heading ends where a browser ends it: at its own end tag, at the end tag of an
element open around or inside it, such as `<div><pre>a</div>b`'s `</div>`, at the start of the
next cell for one in a cell, or, for a heading, at the next heading. It follows the page's
mode as a browser does, and the page's first doctype sets it by the HTML standard's rules, when
nothing but blanks and comments comes before it. `<!DOCTYPE html>`, in any case and spacing, puts
the page in standards mode, where a `<table>` ends a `<p>` open, and so does a doctype the
standard does not mark quirky, such as XHTML 1.0 Strict's or `about:legacy-compat`. A doctype it
marks quirky, such as HTML 4.01 Transitional's without its system identifier, puts the page in
quirks mode, where the table opens inside the p, and so does a page with no doctype or with text
or a tag before it. A doctype after the first changes nothing. A Markdown file's HTML blocks
always read in standards mode, and a doctype in one changes nothing. An end tag of an element
that is not open closes nothing, as in a browser, and a `<table>` in a table but outside a cell
ends that table. A test holds errata to the HTML standard's tree builder (golang.org/x/net/html's
`Parse`) on generated HTML: each pre's, heading's and cell's text is the text the tree builder
puts in that element. errata reopens a formatting element a block's end closed before the next
text, as a browser does, so `<h1>a<div><b></div>c<h2>` opens the `<h2>` inside the `<h1>`. Where
the builder would reorder formatting elements around a pre or a heading (its adoption agency, for
`<b><pre>a</b>`), and for a heading opened inside a heading, errata refuses the file at that
tag's line rather than read it otherwise; close the elements in order. Elsewhere it follows the
adoption agency's passes, so what the rebuild leaves open, an svg among it, is what the builder
leaves open. Text a page puts in a table outside a cell, which
a browser moves before the table, errata reads where it is written.

- **comment.** Each comment, `<!--` to `-->` or `--!>`. One in a `<script>`, `<style>`,
  `<textarea>`, `<title>` or a tag's attribute is no comment, as in a browser; neither is a
  doctype, a processing instruction such as `<?php ?>`, or a `<!--` with no end after it. `<!-->` and
  `<!--->` are empty. The value is the text with the blanks at either end left out, entities as
  written, at the line where the comment starts.
- **section.** What a browser shows under each heading, `<h1>` to `<h6>`, that the path names, to
  the next heading of the same or a higher level, its subsections and their headings included.
  A heading names the path as in a Markdown file; its text is what a browser shows of it,
  compared whole and in its case, a `<br>` in it a space. A link inside the heading to a place in
  the page, its `href` starting with `#`, whose text is blank or only `¶`, `§`, `#`, `🔗` or a
  zero width space, is the heading's link to itself, which
  Sphinx, MkDocs, Docusaurus, rustdoc, pkg.go.dev and GitHub write, and no part of its text or of
  the section's prose: `<h2>Install<a class="headerlink" href="#install">¶</a></h2>` is
  `Install`. Every heading counts, inside a `<section>`, a `<div>` or a table
  too; a heading's start ends a heading still open. Each element a browser starts a line at, the
  list under [`text`](#text), ends a line, so a paragraph or a list item is a
  line; a `<pre>` or a `<textarea>` keeps its lines. The value is at the heading's line, and a
  capture at the line where its match starts.
- **code.** Each `<pre>` element (and `<listing>`), its text as written, tags left out and
  references decoded, less a newline at its end. Its language is the one a class names,
  `language-x` or `lang-x`, the first such class of the `<pre>` or else of a `<code>` inside it:
  `<pre><code class="hljs language-go">` is `go`. A `<code>` outside a `<pre>` is inline code,
  not a block. The value is at the line of its first character, or of the `<pre>` when it is
  empty.
- **table.** Each `<table>`. Its first row with a cell holds the headers, `<th>` or `<td>`; each
  row after it gives the cell under the header `column`, or only the rows whose `key` cell is
  `row`, at the row's line (its `<tr>`, or its first cell when the row has no `<tr>`). A cell is
  what a browser shows of it, on one line. `colspan` and `rowspan` are not followed: each cell is
  one column of its row. A table inside a cell is a table of its own, and the cell's text ends
  where it starts.

errata does not read what a browser runs or does not show: `<script>`, `<style>`, `<template>`,
`<noscript>`, `<iframe>`, `<title>`, `<noembed>` and `<noframes>`, nor attributes other than a
`<pre>` or `<code>` class, nor anything JavaScript builds when the page loads. It leaves out the
text of the obsolete `<xmp>` and `<plaintext>` too, although a browser shows it. A page whose content a script renders has nothing for errata to read; point the rule at the
source the page is built from.

Matches, values and the heading path work as for the `markdown` extractor: a match never runs from
one piece into the next, and all of them share the `regex` extractor's time limit and cap on
values. The pass is bounded by the tokens a file may hold and by 1000 elements open at once
(Limits); the text read may be at most 12 times the size of the file, since sections of one path
nest at most six deep.

### `marker`

A rule names the files it reads. A marker turns that around: the claim names the rule. A document
tags a claim inline with the id of the rule that checks it, errata finds the markers in the files
git tracks, and the `marker` extractor gives the rule the value of each marker that names it.

```markdown
The API listens on port <!-- errata: api-port -->8080<!-- /errata -->.
```

```yaml
checks:
  - id: api-port
    reason: >
      The docs tell an operator which port to open. If the chart moves the port and the docs
      stay, the firewall is opened on the old one.
    scope: repo
    extract:
      doc:  {marker: {}}
      conf: {yaml: {file: deploy/api.yaml, path: 'spec.ports[0].port'}}
    agree: [doc, conf]
```

| Field | Required | Meaning |
|---|---|---|
| `files` | no | the files to walk, each a file or a glob over the files git tracks (see [A set of files](#a-set-of-files)); default every file git tracks, `**` |
| `max_bytes` | no | the largest file the walk parses, and what the values hold in all (see [Limits](#limits)) |

A marker opens with the HTML comment `<!-- errata: ID -->`, where `ID` is a rule's id, and closes
with `<!-- /errata -->`. Its value is the text between the two, with the blanks at either end left
out and each run of blanks inside, a line break among them, read as one space, so a value that a
line wrap splits reads as one line. Markers do not nest. Each value is at its file and the line
where it starts, so one rule gathers markers from many files and an `agree` that fails lists each
place:

```text
  said: agree: the values differ
          "8080" at README.md:3 (doc), cmd/main.go:3 (doc), deploy/api.yaml:3 (conf)
          "8081" at docs/ops.md:3 (doc)
```

Where a marker may stand depends on the file:

- In Markdown (`.md`, `.markdown`), a marker is an HTML comment the CommonMark parse reads as
  HTML: inline in a paragraph, a heading, a list item or a table cell, or a comment block on a
  line of its own. The value is the prose between the markers, as the `text` extractor reads it,
  so `**8080**` reads `8080`, and it stays in one block and one table cell: a marker opened in one
  paragraph or cell and closed in the next is unknown. A marker inside a fenced or indented code
  block or a code span is no marker. One inside other HTML, such as a `<div>` block, makes the
  rule unknown: errata reads markers only in comments the Markdown parse gives it, and pairing
  one with its closer across the HTML's own text is not built.
- In a file the comment lexer reads (see [`comment`](#comment)), a marker is written
  inside a comment of the language, as the same HTML comment text: `// Listens on <!-- errata:
  api-port -->8080<!-- /errata -->.` A marker inside a string literal is no marker, and that
  includes a Python docstring. A value may run on through a run of comments with only blanks
  between them, such as consecutive `//` lines, and a `*` that starts a line of a block comment
  is left out of it; a value that runs out of its comments into code makes the rule unknown.
- In HTML and XML, a marker is a comment of the file, and its value the text between, tags and
  all.
- Any other file is passed over, unless it holds a marker, which makes the rule unknown: errata
  cannot tell whether the file reads it as one. reStructuredText and AsciiDoc are such files: an
  HTML comment there renders as visible text, so a marker written in one is refused rather than
  read.

A marker that cannot be read whole makes every rule whose walk reads its file unknown, never a
value: one no `<!-- /errata -->` closes, a closer with no marker open, a marker opened inside
another, an id that is no check's id form, a closer with more after `/errata`, and a `<!--` that
reads like a marker but stands inside another comment or has no `-->`. A near miss is no marker
and is passed over without a word: `ERRATA:` in capitals, a blank before the colon, and a comment
in a link reference definition, which the Markdown parse does not read as HTML. Only one whose
closer is right is refused, as a closer with no marker open.

A marker that no rule reads tags a claim that nothing checks, which is a check silently off. So
whenever a rule reads markers, errata adds a check of its own, `errata-unbound-markers`, after the
config's checks. It walks the files the `marker` extractors of every rule walk, together, and is
false for each marker there whose file no `marker` extractor of the rule it names reads. That is a
marker naming no check, one naming a check that reads no markers, and one in a file its rule's
`files` leave out while another rule's take it in. A marker outside every rule's `files` is not
its to report: a repository whose test fixtures hold marker text, as errata's own does, narrows
`files` to its documents and code and leaves the fixtures out of every walk (owner decision
2026-11-03). It is a rule, with one extractor `unbound` and the assert `size(unbound) == 0`, so it
reports like one, and errata list prints it with the rules' files; `required` does not list it,
and a check of the config may not take its id while a rule reads markers. A rule that reads
markers and finds none is unknown unless `allow_empty` lists the extractor.

The walk reads git's index, as a glob does: only the files a clone holds, never an untracked or
ignored file, the fixtures of witnesses only on a witness's run, and in a sparse checkout it is
unknown wherever the checkout leaves a file out (see [A set of files](#a-set-of-files)). A file
git tracks as a symlink that a glob matched is passed over: what it leads to is read under its
own name when git tracks it, so a repository that tracks `AGENTS.md` as a link to `CLAUDE.md`
reads its markers once. A link that `files` names outright makes the walk unknown, and the message
names the target to list instead: read under the link's name, a marker would be bound to a file
the built-in check reads under the target's. It parses only a file that holds the bytes
`errata`, and a file over `max_bytes` it reads in pieces for those bytes alone: without them it is
passed over, and with them the rule is unknown. A run reads and parses each file once, however
many rules' walks and the built-in check's take it in, and each walk filters the one result.
[ADR 0017](adr/0017-a-marker-names-its-rule-and-errata-finds-every-marker.md) says why the
syntax and the walk are what they are.

### `xml`

`xml` reads an XML file and returns every node its path selects. Its fields are in the [table of
kinds](#extractors), and the three sections below give the path's grammar, its namespaces and the
values it returns.

#### XML paths

`xml` reads a path in the part of [XPath 1.0](https://www.w3.org/TR/1999/REC-xpath-19991116/)
below, in its abbreviated syntax. A path starts at the root, with `/` or `//`.

| Form | Selects | Example |
|---|---|---|
| `/name` | the child elements called `name` | `/project/version` |
| `//name` | the elements called `name` anywhere below | `//dependency` |
| `*` | every element, in any namespace | `/project/*` |
| `@name`, `@*` | an attribute, or every attribute, of the element before it | `/manifest/@package` |
| `text()` | each text node of the element before it, as the file has it | `/a/text()` |
| `[n]` | the `n`th of the nodes the step selects among one parent's children, counted from 1 | `//dependency[2]` |
| `[child='v']`, `[@attr='v']` | the elements whose child or attribute has the value `v`, joined with `and` | `//plugin[artifactId='surefire' and @inherited='true']` |

A position counts among the children of each parent, as in XPath: `//item[1]` is the first `item`
of every element that holds one, not the first `item` in the file. A position after a comparison
counts among the elements the comparison kept: `//item[@kind='tool'][2]` is the second tool of
each parent. Positions count from 1, so `[0]` does not load. A comparison compares strings, so a
number is written in quotes, `[port='8080']`; an unquoted number does not load. A child compares
its value as the table under XML values gives it, and only a child element counts, not one
further down. A
literal has no escapes: it ends at the next quote of the kind that opened it, so a value that holds
`'` is written in double quotes. Spaces, tabs, CRs and LFs may stand between the parts of a path,
as in `/ r / item [ 1 ]`. A path holds no other control character and is valid UTF-8.

Anything else XPath has does not load, and the message names it and its column: `..` and `.`, an
axis such as `child::`, a function such as `last()` or `contains()`, `node()`, a prefixed wildcard
such as `m:*`, `|`, `or`, `!=`, `<` and the other operators, arithmetic, a variable, a test that a
child exists (`[b]`), a path inside a predicate (`[a/b='x']`), two nodes compared, a negative
position, and a comparison on an attribute or on `text()`. `@` and `text()` end a path, since
neither has children. A path holds at most 32 steps and predicates in all, a predicate at most 64
comparisons, and a literal at most 2048 bytes; a path past one of these does not load.

A path that selects nothing gives zero values, which the rule decides about.

#### XML namespaces

A prefix in a path names the namespace `namespaces:` binds it to, whatever prefix the file writes
for it. A prefix the path uses and `namespaces:` does not bind does not load; `xml:` names the XML
namespace without a binding, as in every file. A binding does not load when its prefix is not a
name without a colon, when it binds `xmlns`, when it binds `xml` to another namespace, or when its
namespace is empty. A name without a prefix selects only elements in no namespace, as XPath has
it. So under a default namespace, which most POMs, .csproj files and Android manifests declare, a
path without prefixes selects nothing:

```xml
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <version>1.4.0</version>
</project>
```

```yaml
extract:
  wrong:  {xml: {file: pom.xml, path: /project/version}}   # no value: the rule is unknown
  right:
    xml:
      file: pom.xml
      path: /m:project/m:version                             # 1.4.0
      namespaces: {m: http://maven.apache.org/POM/4.0.0}
```

An attribute without a prefix is in no namespace even under a default namespace, so `@id` needs
no prefix. A namespace declaration (`xmlns`, `xmlns:p`) is not an attribute and `@*` does not
select it.

#### XML values

| File says | Value |
|---|---|
| `<version> 1.4.0 </version>` | `1.4.0`: an element's text, without the white space at its ends |
| `<a>x <b>y</b> z</a>` | `x y z`: the text of every element inside it too |
| `<a>&lt;&#x41;&amp;</a>` | `<A&` |
| `<a><![CDATA[<b>]]></a>` | `<b>` |
| `<a b=" 1 "/>`, path `/a/@b` | ` 1 `: an attribute's value as written, its entities read |
| `<a b="x<tab>y<newline>z"/>` | `x y z`: in an attribute a tab, a CR, an LF or a CRLF written in the file reads as a space (XML 1.0 section 3.3.3); `&#9;` and `&#10;` keep the character they name |
| `<a>x<!-- c -->y</a>`, path `/a/text()` | `x` and `y`: a comment or a processing instruction ends a text node, and CDATA beside text joins it |

White space is the four characters XML names: space, tab, CR and LF. A no-break space is text.
Each value is at the line where its element's start tag, its attribute's name or its text node
starts. Lines end as XML 1.0 section 2.11 ends them, at a CRLF, an LF or a lone CR, so a file with
the CR line ends of classic Mac OS reports the lines an editor shows. Every kind numbers lines this
way; `yaml`, `gitlab-ci` and `github-actions` also end a line at NEL, LS and PS, as their parser
does ([ADR 0007](adr/0007-embedded-reads-a-document-a-string-holds.md)).

errata reads no DTD. A `<!DOCTYPE>` is skipped, its external parts are never fetched, and an
entity it declares is never expanded: a reference to one, internal or external, makes the rule
unknown. That keeps out both an external entity that would read a file or a URL (XXE) and entities
that expand to gigabytes (billion laughs). The five predefined entities and character references
are read.

A file in UTF-8 is read, with or without a byte order mark. A file that declares US-ASCII,
ISO-8859-1, ISO-8859-15 or windows-1252 (or the labels `ascii` and `latin1`) is read when every
byte is ASCII, since it then reads the same as UTF-8. With any other byte, and in any other
encoding, UTF-16 too, the rule is unknown.

### `ini`

`ini` reads a file as the tool that reads it does. `dialect` names the tool; left out, the
file's name chooses:

| Dialect | Chosen for | Reads as |
|---|---|---|
| `git` | `.gitconfig`, `.gitmodules`, `gitconfig`, a name ending in `.gitconfig`, `config` in a directory named `.git` or `git` | git's own parser (git 2.47) |
| `ansible` | `hosts`, `hosts.ini`, `inventory`, `inventory.ini`, any file under a directory named `inventory` or `inventories` | ansible-core's ini inventory plugin (2.21) |
| `tox` | `tox.ini` | tox 4 |
| `php` | `php.ini` and a name that starts with `php.ini`, such as `php.ini-production`, `php.ini-development` and `php.ini.dist`; and a name ending in `.ini` under a directory named `php` | PHP 8.4, in the mode PHP reads php.ini in |
| `systemd` | a unit of a type systemd reads (`.service`, `.socket`, `.timer`, `.mount` and the rest), a Podman Quadlet file (`.container`, `.volume`, `.network`, `.kube`, `.image`, `.pod`, `.build`), and a `.conf` file in a drop-in directory such as `api.service.d` or `service.d` | systemd 257's unit file parser, and for a Quadlet file and its drop-ins Podman 5.4.0's ([`systemd`](#systemd)); before this release these files were read in `plain`, which `dialect: plain` still reads them in |
| `plain` | every other file: `setup.cfg`, `pytest.ini`, `mypy.ini`, `.flake8` | Python's configparser |

A template file, named with one of the template suffixes listed below, is read as the file it
renders: its name without the suffix chooses the dialect and the values the tool computes, so
`php.ini.j2` is read as php.ini, `hosts.j2` as an inventory and `setup.cfg.j2` as setup.cfg, and
the template's own marks hold on top. `tox.ini.j2` is read as tox reads tox.ini, which refuses a
key set twice in a section, so a template that sets `deps` once in each branch of an `if`, with
its tags in comment lines, is refused.

```yaml
extract:
  deps:
    ini: {file: tox.ini, section: testenv, key: deps}
  memory:
    ini: {file: php.ini, section: PHP, key: memory_limit}
  web1:
    ini: {file: inventory/hosts, section: web, host: web1, key: ansible_host}
```

A `dialect` that is not one of these six does not load. Neither does `subsection` outside git,
`host` outside ansible, or a spec without `key`, except in ansible, where the table below says
when `key` is left out. git, ansible and systemd also need `section`. The systemd dialect
reads the one file named; the [`systemd`](#systemd) kind reads a unit with its drop-ins, as
systemd does, and splits `Environment=` into variables. In plain and php, leaving
`section` out selects the keys before the first header; tox refuses such keys.

`section` and `key` are matched as text, never read as a path: an INI file is two levels deep,
section names hold the characters the path grammar gives a meaning to (`testenv:py311`,
`url "https://x/"`), and one key can have several values. A key set more than once in a
section returns every value, each at its line, so `agree` sees a file that disagrees with
itself; tox, which refuses a repeated key, is the exception. Each value is at the line where
its key starts. A line the dialect's tool refuses makes the rule unknown, with the line named.

A value that the file's tool changes is returned as written and marked not literal (see
[Values a file only computes](#values-a-file-only-computes)). errata decides it from its model of
that tool's transform: the value is not literal exactly when the transform's output differs from
the text errata returns. These are the values errata marks:

- In php.ini: a boolean word such as `On` or `none`, a constant PHP defines before it reads php.ini,
  such as `E_ALL`, a `${}` reference and an expression.
- In tox.ini: a value with a factor condition (`py311: mock`) or a substitution (`{posargs}`,
  `{env:X}`, `[]`). In setup.cfg's `[tox:tox]`, `[testenv]` and `[testenv:name]`, which errata
  reads as plain configparser does, also a value with a comment or a joined line, which tox drops
  or joins.
- In setup.cfg: a value with a `%`, and one that starts with an `attr:` or `file:` directive.
- In mypy.ini, .mypy.ini and the `[mypy]` and `[mypy-*]` sections of setup.cfg: a path option,
  such as `mypy_path`, `files` or `cache_dir`, with an entry that starts with `~` or a `$`
  variable anywhere, and a `files` entry with a glob (`*`, `?` or `[`). mypy's expandvars keeps a
  variable the environment does not set, so `python_executable = $PY` is `$PY` to a mypy run
  without `PY`; errata marks it all the same, since it cannot know the environment mypy runs in.
- In .coveragerc and the `[coverage:*]` sections of setup.cfg and tox.ini: a `$$`, a `$name`, and a
  `${name}`, `${name?}` or `${name-default}`, the forms coverage.py substitutes; `${ name }` and a `${`
  with no `}` are text, as coverage leaves them.
- In an ansible inventory: a host with a range (`web[01:20]`) or with a port ansible takes off
  (`db1:2222`, or a port of any decimal digits Python's `\d` reads, such as `٢٢`), which it does only when what is left is an address or a host name, so `fe80::1`
  and `we$b:22` are hosts ansible keeps whole and errata returns literal; a number
  Python writes otherwise (such as `0x10` or `1+2j`), a list or a dict Python writes otherwise (`[80,443]`), a tuple, a set, and a
  Jinja2 template (`{{`, `{%` or `{#`).
- In a template file of any dialect, and a properties template, named with `.j2`, `.jinja`,
  `.jinja2`, `.tmpl`, `.tpl`, `.tftpl` or `.erb` at the end: a value holding `{{`, `{%` or `{#`,
  `<%` in ERB, and `${` or `%{` in Terraform's `.tpl` and `.tftpl`; and every value on a line
  inside one of the engine's blocks, since the rendered file may hold it once, many times or not at
  all. After `{% if x %}`, `a=1` and `{% endif %}`, `a` is not literal.

errata finds a template's blocks by their tags, without reading the expressions in them: Jinja's
`{% %}` in `.j2`, `.jinja`, `.jinja2`, `.tmpl` and `.tpl`, Go's `{{ if }}`, `{{ range }}`,
`{{ with }}`, `{{ define }}` and `{{ block }}` to `{{ end }}` in `.tmpl` and `.tpl`, as Helm and
chezmoi write them, ERB's `<% %>` in `.erb`, and Terraform's `%{ if }` and `%{ for }` in `.tpl`
and `.tftpl`. A block runs from the line of the tag that opens it to the line of the tag that
closes it. A Jinja tag opens a block unless it is an `end` tag, `else`, `elif`, or one that holds
no body, such as `include`, `import` or `set x = 1`, so an extension's own tag opens one too; an
ERB tag opens one when its Ruby starts with `if`, `unless`, `while`, `until`, `case`, `for` or
`begin`, or ends in `do` or `{`, and `end` or `}` closes it. Where errata cannot tell, it marks:
a block or a tag left open, a closing tag that no block opened, and `{% extends %}` mark every
line, since the engine refuses such a template or renders only the blocks it overrides. In a
`.tmpl` or `.tpl`, which errata reads as both Jinja and Go, a tag whose text holds the other's
tag, such as a Jinja comment `{# ... #}` around `{{ if .x }}`, marks its own lines. A tag in a
comment line of the file still renders, so it counts. errata finds the blocks in two passes over
the file and keeps only the block open at the line it reads, so a template costs no more memory
than another file.
Most INI dialects refuse a line that holds only a Jinja, Go or Terraform tag, so such a rule is
unknown with the line named; a properties file, php.ini's ERB and a tag in a comment line read on.

These values depend on the key's type or on which tool reads the file, which errata does not
know, so errata returns them as written and literal; a rule over one compares the text the file
holds:

- In php.ini: a size such as `128M`. PHP's parser returns the text, and `ini_get` gives it back;
  only the directive that reads it, such as `memory_limit`, converts it to a number.
- In a plain file: a key that only `[DEFAULT]` sets. configparser gives every section the keys of
  `[DEFAULT]`, but errata reads `[DEFAULT]` as a section like any other, so such a key is found
  with `section: DEFAULT` and not under another section.
- In mypy's files: `$MYPY_CONFIG_FILE_DIR` in an option that is not a path option, which mypy reads
  as written.
- In git: a typed value, such as `10k`, `yes` or `~/x`, which `git config --get` prints as written
  and only `--type` or the command that reads it converts; an `include`, which errata does not
  follow; and a URL that a `url.<base>.insteadOf` rewrites, since the rule may sit in another
  config file.
- In an ansible inventory, listing a group's hosts: a host with a range or a port is one value, its
  text as written, marked. ansible reads a range as the hosts it expands to, which errata never
  builds (see the ansible paragraph below), and a port as the host without it; the listing keeps
  the line's one value rather than turn one line into many.

git and other plain files, such as pytest.ini and .flake8, return only literal values,
so `skip_not_literal` cannot list them. Neither can any other file errata models no transform for,
unless it is a template.

**plain.** A line ends at `\n`, `\r\n` or a lone `\r`, as Python's text mode reads it, and a
file that is not UTF-8 is refused, as Python refuses to decode it. A line whose first character
other than white space is `#` or `;` is a comment; a `;` or `#` later in a line is part of the
value, so `url = https://x;y` keeps the `;`. `[name]` is a header, the name everything between
the first `[` and the last `]`. `key = value` and `key: value` split at the first `=` or `:`,
and a line that is none of these is refused. A line indented deeper than its key continues the
value, joined to it with a newline, as `install_requires =` and the lines below it are; an empty
line between them stays in the value, and a comment line is left out. errata reads what
configparser refuses in its default strict mode: a repeated key gives every value, and a
repeated section adds to the first. Names are compared as written (configparser lowercases
keys; setuptools does not). `[DEFAULT]` is a section like any other, and `%(name)s` is returned
as written; in setup.cfg, where setuptools interpolates it, it is marked not literal.

**tox.** configparser as tox runs it, and then tox's own pass over each value: a `#` that no
`\` comes before starts a comment, `\#` is `#`, and a `\` at the end of a line joins it to the
next, so `deps = pytest # pin` is `pytest`. Keys are compared in lower case, as tox's
configparser lowers them, the spec's key too; sections are compared as written. A key set twice
in a section, a section written twice and a key before the first header are refused, as tox
refuses them; errata keeps the names of the sections and the keys of one section to check it. A
byte order mark is dropped. Factor conditions (`py311: pytest`) and substitutions (`{posargs}`,
`{env:X}`) are returned as written and marked not literal, since tox computes them for an
environment.

**php.** `;` outside quotes starts a comment, so `memory_limit = 128M ; note` is `128M`. A value is
assembled as PHP's parser assembles it: `"double quoted"` strings with `\"`, `\\` and `\$` escapes,
`'single quoted'` strings, and words and the blanks between them; blanks next to a quoted string go,
so `a "b c" d` is `ab cd`. A line PHP refuses, such as one with `=` in its value or a boolean word
as its key, makes the rule unknown. What PHP then computes is returned as written and marked not
literal: `On`, `Off` and the other boolean words, a constant such as `E_ALL`, `${VAR}`, and an
expression of `|`, `&`, `^`, `~`, `!` and parentheses. PHP reads php.ini at startup, before any
extension defines its constants, so it replaces only the constants of the engine and of PHP itself:
the names that start with `E_`, `PHP_`, `PEAR_`, `ZEND_`, `DEBUG_BACKTRACE_` or `UPLOAD_ERR_`, and
`DEFAULT_INCLUDE_PATH`. An extension's constant, such as `M_PI`, `CODESET`, `SIGTERM` or
`SOMAXCONN`, is text, as is any other word, such as `UTC`. That holds for php.ini and the `.ini`
files PHP scans at startup, and errata reads every `.ini` file so, wherever the repository keeps
it, such as `docker/opcache.ini`. PHP reads a `.user.ini` for each request, and an FPM pool file
after the extensions start, when their constants are defined; so for a `.user.ini`, and for a file
whose name does not end in `.ini` that a spec reads with `dialect: php`, such as a pool file
`www.conf`, errata marks every name written as a constant is. `key[] = v` is the key `key`. A key with no
`=` is ignored, as PHP ignores it, and a file with a NUL byte is refused.

**git.** `[section]` and `[section "subsection"]`; the deprecated `[section.subsection]` is the
subsection in lower case. Section and key are compared without regard to case, a subsection as
written. `section: remote, subsection: origin, key: url` and `section: remote.origin, key: url`
name the same entry, as `remote.origin.url` does to git. A section or a key that git would not
accept on its command line does not load. A value is git's value: quotes and escapes read, `#`
and `;` comments dropped, white space at both ends gone, and a tab or carriage return inside
kept, as git 2.45 and later keep it (older git made one outside quotes a space). A key with no
`=` is `true`, as git reads it as a boolean (`git config --get-all` prints an empty line for
it). `include` is not followed, as `git config --file` does not follow it. A line git refuses
makes the rule unknown, and so does a NUL byte, which git reads as the end of a name or a value.

**ansible.** `section` is a group's name for its hosts (`[web]`, also written `[web:hosts]`),
`web:vars` for its variables, or `web:children` for its child groups; the hosts above the first
header are in `ungrouped`.

| Selects | `section` | `host` | `key` |
|---|---|---|---|
| the hosts of a group | `web` | | |
| one host, to check it is listed | `web` | `web1` | |
| a variable of every host line | `web` | | `ansible_host` |
| a variable of one host | `web` | `web1` | `ansible_host` |
| a group variable | `web:vars` | | `ntp_server` |
| the child groups | `prod:children` | | |

Any other combination does not load: a vars section needs `key`, and takes no `host`; a
children section takes neither.

Lines end where Python's `splitlines` ends them: at `\n`, `\r\n` and `\r`, and at `\v`, `\f`,
`\x1c`, `\x1d`, `\x1e`, NEL, LS and PS too. A host line splits as a shell splits words, `#` outside
quotes starting a comment. `host` selects a host line when it is the line's first word as written,
or one of the hosts ansible reads from it: `host: db1` selects `db1:2222`, and `host: web05` selects
`web[01:20]`, at that line, with zeros, steps (`[0:20:5]`), letters (`[a:f]`) and several ranges in
one name as ansible expands them. errata never builds a range's hosts: it walks the pattern against
the name and decides each range by arithmetic, so a range of a million hosts costs its bytes.
Listing a group's hosts (no `host`, no `key`) gives each line's first word, `web[01:20]` as written
and marked not literal. A host ansible refuses is refused: one that ends in `:`, `---`, a range
ansible cannot expand, and a range bound of more than 4300 digits, which Python's `int()` refuses. A
range that holds no host, such as `h[3:1]`, reads no host and none of its line's variables, as
ansible reads none, but its variables must still be `key=value`.

ansible evaluates each value as a Python literal. errata keeps the text of a literal that is not
a string: `8080`, `True` and `[1, 2]` are the text written. A literal whose value Python writes
otherwise is marked not literal, since ansible uses the value and not the text: a number such as
`0x10`, a list or a dict written otherwise than Python's `repr` writes it, such as `[1,2]`,
`["a"]` or `{'a': 1, 'a': 2}`, a tuple, in parentheses or as literals joined by commas
(`x = 1, 2`), which ansible holds as a list, and a set, whose order is
Python's. `[1, 2]` and `{'a': [True, None]}` are literal. errata is sure of a container only when
each string in it is printable ASCII and each key a string, and marks any other. A `#` comment after a literal goes, as
Python drops it, so `x = 8080 # http` is `8080` and `x = [1, 2] # c` is `[1, 2]`. A string
literal is the text Python makes of it: `'x\ty'` has a tab, `'a' 'b'` is `ab`, `b'\x41'` is `A`,
and `r'a\tb'` keeps its backslash. A string errata cannot decode exactly, one with `\N{...}` or a
surrogate escape, makes the rule unknown. A value that is no literal is the text written, a `#`
in it too, as in `pool.org # c` and `f'x'`. A byte that is not UTF-8 is kept, and so is the value
that holds it. errata does not check that a group a vars or children section names is declared.

### `properties`

`properties` reads a file as `java.util.Properties.load` does: `=`, `:` or white space between
key and value, `\` at the end of a line to continue it, `\uXXXX`, `\t`, `\n`, `\r` and `\f`
escapes, and `#` and `!` comments. A `\` before any other character is dropped. The value is at
the line where its key starts. `key` is the key after its escapes are read, matched as text, so
`server.port` is the key `server.port`. Java keeps the last value of a key set twice; errata
returns every one. A `\u` without four hex digits makes the rule unknown, as Java throws. The
file is read as UTF-8, as `load(Reader)` reads it, and a sequence that is not UTF-8 is U+FFFD,
as Java's decoder makes it. A surrogate pair written as two escapes is its one character; a
surrogate alone is U+FFFD. Lines are numbered at `\n`, `\r\n` and a lone `\r`, as the
[XML values](#xml-values) says every kind numbers them. Spring's `application-<profile>.properties`
files are plain files: name the one to read, errata does not merge profiles. errata reads the
value Java reads, which is literal, with two exceptions. Spring Boot and Log4j fill in `${name}`
from their own properties and the environment, so in `application.properties`,
`bootstrap.properties`, their `-profile` files such as `application-prod.properties`,
`log4j2.properties`, `log4j2-test.properties` and `log4j.properties`, a value with `${` is marked not
literal: `server.port=${PORT:8080}` is not the text to Spring. And in a template file, as
[`ini`](#ini) says, read as the file it renders, so `application.properties.j2` is marked as
both. A placeholder that Maven's resource filtering fills in, `${project.version}` or
`@project.version@` in any file, is returned as written: the pom, not the file, turns filtering
on, and errata reads only the file.

### `tool_version`

`tool_version` returns every version of one tool that the repository pins, each with its file
and line, so one rule says that the Node version of the image and of every pin agree:

```yaml
checks:
  - id: node-agrees
    reason: The image and a developer's shell run one Node.
    scope: repo
    extract:
      pins: {tool_version: {tool: node}}
      image: {regex: {file: Dockerfile, pattern: '^FROM node:([\d.]+)'}}
    agree: [pins, image]
```

Without `files`, it walks the repository and reads every file of the table below that pins the
tool, wherever it is: a directory's files in the table's order, then its subdirectories, each by
name. The walk enters no directory of the list below, wherever it is, since each holds files the
repository installs, downloads, builds or keeps for its tools, and a pin there is another
project's; `.errata/witness` is a directory `witness` in a directory `.errata`, each name in any
case, and holds the fixtures of [witnesses](#witness). Every other name of the list matches
byte for byte, the same on every OS. Nor does it enter a directory below the root that holds an entry named `.git`, a file
or a directory: a submodule, a worktree or a repository of its own, whose pins are another
project's too. It does not read `.gitignore`. It does not enter a symlink to a directory, so a pin
below one is not found; a symlink at a pin's name is read as `file` reads one, followed when it
ends inside the repository and refused when it leads out, and passed over when it dangles inside.
Several links to one file read it once, and give its versions at each link's path. A walk that
passes a limit, meets a path over 4096 bytes or a directory it cannot open or read makes the rule
unknown (see [Limits](#limits)). `files` names the files instead, and each one it names must exist, inside a
skipped directory or a submodule too. errata reads a file by its name, so a file the table does
not name, or one that pins another tool, does not load. The files one extractor reads hold at most
its `max_bytes` in all, as one file does: a repository's pins are small files, and a walk that
finds many, or copies of one large file, costs no more than reading one file of that size.

| Directory | Why |
|---|---|
| `.git` | git's own store: objects, refs and hooks, no file of the working tree |
| `node_modules` | the packages npm, Yarn and pnpm install, each with pins of its own |
| `bower_components` | the packages Bower installs |
| `.yarn` | Yarn's cache and its own releases |
| `.pnpm-store` | pnpm's store of packages |
| `vendor` | dependencies Go, Composer and Bundler copy into the repository, each with its own go.mod or version files |
| `.venv` | a Python virtual environment, with the packages installed in it |
| `venv` | a Python virtual environment, by the other name python -m venv is given |
| `.tox` | the virtual environments tox builds |
| `.nox` | the virtual environments nox builds |
| `.direnv` | the environments direnv builds |
| `__pycache__` | Python's compiled files |
| `.terraform` | the providers and modules terraform init downloads |
| `.gradle` | Gradle's caches |
| `.cache` | the caches tools keep in the repository |
| `testdata` | test fixtures, which the go command passes over too: a fixture is no claim of the repository |
| `.errata/witness` | the fixtures of witnesses, each a break planted for a check to report false on, which globs pass over too |

| File | Tool | Read as |
|---|---|---|
| `.tool-versions` | the tool each line names | asdf and mise: a tool, then its versions, the first wanted and each after it a fallback; every version is a value |
| `.sdkmanrc` | the candidate each line names | SDKMAN!: `candidate=version` on each line |
| `.nvmrc`, `.node-version` | `node` | the first version |
| `.python-version` | `python` | pyenv: the first word of every line, each a version pyenv activates |
| `runtime.txt` | `python` | Heroku: `python-3.12.1` on the first line; the value is `3.12.1` |
| `.ruby-version` | `ruby` | the first version, `ruby-3.3.0` as written |
| `.go-version` | `go` | the first version |
| `go.mod` | `go` | as the go command picks its Go: the `toolchain` directive's version when there is one (`toolchain go1.22.3` gives `1.22.3`, `default` stays `default`), and the `go` directive's when there is none |
| `.terraform-version` | `terraform` | the first version |
| `.java-version` | `java` | the first version |
| `rust-toolchain` | `rust` | rustup: a file that parses as TOML is read as `rust-toolchain.toml` is; a file of one line that does not is the legacy form, that line the channel |
| `rust-toolchain.toml` | `rust` | `toolchain.channel`; a toolchain given by `path` has none |
| `.bazelversion` | `bazel` | the first version |
| `.bun-version` | `bun` | the first version |
| `VERSION` | the tool the spec names | the first version; only when `files` names it, since the file names no tool |
| `package.json` | the tool `engines` names, and the package manager | `engines.<tool>`, and `packageManager`'s version when its name is the tool (`pnpm@9.1.0+sha512...` gives `9.1.0`); only when `files` names it |
| `pyproject.toml` | `python` | `project.requires-python` and Poetry's `tool.poetry.dependencies.python`; only when `files` names it |
| `Cargo.toml` | `rust` | `package.rust-version` and `workspace.package.rust-version`; `rust-version.workspace = true` gives nothing, since the workspace's line holds the version; only when `files` names it |

The search leaves the three manifests out because they state a range a tool must satisfy, such as
`>=20`, not the version a repository pins. A file of one version gives the first field of its
first line that holds one, and a later line is no value, since nvm, rbenv, goenv, tfenv, jenv,
bazelisk and bun read nothing after it. In every file of lines a `#` starts a comment, a blank line
is passed over, and fields are split at white space, so a CRLF file reads as an LF one. A version
comes back as the file wrote it: `v20.11.1`, `20` and `>=20` are three texts, and comparing them as
versions is a comparison's work, not the reader's.

`tool` is spelled as the tool's command is: `node` and `go`. asdf's plugins are named `nodejs` and
`golang`, and a `.tool-versions` line under either name pins the tool; `tool: nodejs` and
`tool: golang` do not load, and the message names the spelling to write. A tool the table does not
name, such as `maven`, is read from `.tool-versions` and `.sdkmanrc`. `tool` holds letters, digits
and the characters `._-:/@+`, so a mise backend such as `npm:@biomejs/biome` loads, and at most 64
bytes.

A version that names no version until a tool resolves it is not literal (see
[Values a file only computes](#values-a-file-only-computes)):

| Version | Read by |
|---|---|
| `system` | asdf, mise, pyenv, rbenv, nvm, goenv, jenv: the version installed outside the version manager |
| `latest`, `latest:18`, `latest-1` | asdf and mise, tfenv, bazelisk |
| `lts`, `lts/*`, `lts/iron`, `node`, `stable`, `unstable`, `iojs` | nvm and mise: an alias that moves with each release |
| `ref:v1.2`, `path:~/src/node`, `prefix:1.2`, `sub-1:latest` | asdf and mise: a build from a git ref or a directory, or the newest of a prefix |
| `min-required`, `latest-allowed` | tfenv: read from the Terraform code |
| `rolling`, `last_green`, `last_rc`, `last_downstream_green` | bazelisk |
| `stable`, `beta`, `nightly`, and each with a host such as `stable-x86_64-unknown-linux-gnu` | rustup: a channel that moves; `nightly-2024-01-01` names one build and is literal |
| `default` | go.mod's `toolchain default` |

A file its tool refuses makes the rule unknown: a `go.mod` the go command refuses, as
golang.org/x/mod/modfile parses it (an unknown directive, a second `go` line), an `.sdkmanrc` line
without `=` or with no version, a `runtime.txt` that does not start with `python-`, a TOML or JSON
file that does not parse, a file of one version (each file the table reads as the first version,
`.python-version`, `runtime.txt`, a legacy `rust-toolchain`) that holds none, only blank lines and
comments, a TOML date or time where the version goes, such as `channel = 2024-01-01`, which rustup,
Cargo and the Python tools refuse as no string, and a version that is empty or holds a control
character, a direction control such as U+202E, a line or paragraph separator or a byte that is not
UTF-8. A byte order mark at the start of a file of lines is dropped, as errata's other readers drop
it, and so is one at the start of `package.json`, `Cargo.toml`, `rust-toolchain` and
`rust-toolchain.toml`, as npm, Cargo and rustup skip it. `pyproject.toml` keeps it and does not
parse, as Python's tomllib refuses it.

Each file is read up to `max_bytes`, and the versions of all the files hold at most `max_bytes`
bytes and 100000 values in all. A file of lines is read in one pass that keeps only the versions,
so it needs no node limit: at 32 MiB on the build node the densest shapes take 0.2 s to 1.1 s and
at most 110 MB, a line of 16 million versions refused at the value limit among them. The JSON and
TOML files have the node limit of their format, and a `go.mod` a limit of its own, under [Limits](#limits).

### `package_version`

`package_version` returns the version the package gives itself in each of its manifests, each with
its file and line, so one rule says that a release names one version everywhere:

```yaml
checks:
  - id: version-agrees
    reason: The npm package, the wheel and the VERSION file the image reads name one release.
    scope: repo
    extract:
      release: {package_version: {}}
    agree: [release]
```

Without `files`, it reads each manifest of the table below that the repository root holds, in the
table's order, and passes over one the root does not hold. It reads a name only as the table spells
it, as the root's listing gives it, so on a file system that folds case, as macOS and Windows ship
theirs, a `version` directory or a `Package.json` is not read as `VERSION` or `package.json`. A
directory at a manifest's name, or a symlink inside the repository to one, is passed over too, and
so is a symlink that dangles inside the repository; any other symlink is read as `file` reads one.
It does not walk the repository: a manifest below the root, such as a workspace member's, is another
package's, with a version of its own. `files` names the manifests instead, below the root too, at
most 64 of them, and each one it names must exist and be a file. errata reads a file by its name, so
a file the table does not name does not load. The files one extractor reads hold at most its
`max_bytes` in all, as one file does.

| File | Read as |
|---|---|
| `package.json` | `version`, the version npm publishes; a package.json without one, such as an application's, gives nothing. A byte order mark at its start is skipped, as npm skips it |
| `pyproject.toml` | `project.version` (PEP 621). When `project.dynamic` lists `version`, the build backend computes it, and the value is not literal, at that entry's line; under Poetry's backend, `build-backend = "poetry.core.masonry.api"`, poetry-core fills it from `tool.poetry.version`, which is then the value. A file with neither is Poetry's form before PEP 621 and gives `tool.poetry.version`. With `enable = true` under `[tool.poetry-dynamic-versioning]`, that plugin replaces `tool.poetry.version` from a git tag at build time, so the version the file writes, often `0.0.0`, is a placeholder and not literal. A byte order mark is refused, as Python's tomllib refuses it |
| `Cargo.toml` | `package.version`, and `workspace.package.version`, the version a workspace's members inherit. `version.workspace = true`, or `version = { workspace = true }`, inherits it: from the same file's `workspace.package.version` when it holds one, which is then the one value, and otherwise from a Cargo.toml further up, which errata does not look for, so the value is not literal, at the line of `workspace = true`. A file with a `[workspace]` table is its own workspace's root, so one that inherits a version it does not set is refused, as Cargo refuses it. A byte order mark at its start is skipped, as Cargo skips it |
| `VERSION` | the first field of the first line that holds one, as `tool_version` reads the file: a `#` starts a comment, and a blank line is passed over |

A version must be a string: a TOML `version = 1.0`, a JSON `"version": 1` or a table is refused,
since npm, the Python build tools and Cargo refuse it. So is a TOML date or time, such as
`version = 1979-05-27`, which Python's tomllib reads as a date and Cargo refuses, and a
`pyproject.toml` that sets `project.version` and lists it in `dynamic` too, which PEP 621 forbids.
Cargo refuses a `package.version` table other than `{ workspace = true }`, and so does errata. A
version that is empty or holds a control character, a direction control, a line or paragraph
separator or a byte that is not UTF-8 makes the rule unknown, as does a JSON or TOML file that does
not parse, past the node limit of its format (see [Limits](#limits)), and a `VERSION` file of only
blank lines and comments.

The text of a not literal version the build computes is the line that says so, as the file wrote
it, with the spaces and tabs around it taken off: `dynamic = ["version", "readme"]`, or
`version = { workspace = true }`, a comment after it on the line included. In a `dynamic` list
written over several lines, it is the line of the `"version"` entry, such as `"version",`. That
line is no version, so the check above does not apply to it: TOML puts a tab between a key and
its value as readily as a space. The reports escape what a terminal or a viewer would act on in
it. A placeholder that `poetry-dynamic-versioning` replaces is the version the file writes, and
the check applies.

The version a lockfile records for the package itself, and every version of a package it depends
on, are [`dependency`](#dependency)'s to read.

A rule that compares a member of a Cargo workspace or a package with a dynamic version lists the
extractor in `skip_not_literal`, and names the file that holds the version: the workspace's
`Cargo.toml`, or the file the build backend reads, through a regex or toml extractor.

#### The tag on HEAD

`tag` reads the git tags of the commit HEAD names as well, after the files. Its value is a prefix:
each tag on that commit whose name starts with it gives the rest of its name as a version, at the
tag's ref, such as `refs/tags/v1.4.0`. `tag: v` reads the tag `v1.4.0` as `1.4.0`, `tag: api/v`
reads a monorepo's `api/v2.0.0` as `2.0.0`, and `tag: ""` reads every tag on HEAD as it is named.
A tag that is the prefix alone names no version and is passed over. The ref names the tag, not a
file of the tree: in the text report it reads `refs/tags/v1.4.0`, and in SARIF it is the artifact
of the result with no region, a path that holds no file in the checkout, so a viewer that opens the
file to show or fingerprint the result finds none. An annotated tag is on the
commit it points at, as `git tag --points-at HEAD` lists it. Without `tag`, no tag is read.

```yaml
checks:
  - id: release-is-tagged
    reason: The release job publishes the tag's version; a manifest left behind ships another.
    scope: repo
    extract:
      release: {package_version: {files: [package.json, pyproject.toml], tag: v}}
    agree: [release]
```

errata reads the tags from git's files and runs no git, as
[ADR 0002](adr/0002-errata-reads-configuration-and-never-runs-it.md) has it: HEAD, the tags under
`.git/refs/tags`, `.git/packed-refs`, and, for an annotated tag that `packed-refs` does not peel,
its tag object, loose or in a pack. In a worktree it reads the worktree's HEAD and the tags of the
repository it belongs to. A root below the top of its repository reads that repository's tags, and
so does a witness, since a fixture holds no `.git`.

The tag on HEAD is the one compared, not the newest: a manifest is bumped before the tag is cut, so
the newest tag lags behind every manifest between releases. A commit no tag names gives no version
from the tags, so the rule above compares the manifests alone between releases and the tag with
them on the release commit. `agree` with one value has nothing to compare and is unknown, so such a
rule reads two manifests or more, or another extractor beside them.

A shallow checkout that holds no tag makes the rule unknown: CI often clones with a depth and
without tags, as `actions/checkout` does by default, and errata cannot tell a commit no tag names
from a tag the clone left out. Fetch the tags, with `fetch-tags: true` for `actions/checkout` or
`git fetch --tags`, or clone the whole history. A full clone with no tag gives none. A repository
that keeps its refs in a reftable, a ref or a packed-refs line git would not write, a tag that leads
through more than 16 tag objects, and an object that is in no loose object and no pack of the
repository, as in a clone that borrows objects through alternates, make the rule unknown too. A ref
name is held to what git's `check-ref-format` takes, in packed-refs, below `.git/refs/tags` and as
the target of a symbolic ref or of HEAD, so no ref leads out of the repository; and `.git/refs`,
`.git/refs/tags` and `.git/objects/pack` are refused when they are symlinks, which git follows,
since the names listed there would be read as refs of a directory anywhere on the machine. The
bounds are under [Limits](#limits).

### `dependency`

`dependency` returns one package's version in every manifest and lockfile `files` lists, the way
`env` asks every reader for one variable, so one rule says that a dependency pinned in several
places agrees:

```yaml
checks:
  - id: zod-in-range
    reason: The lockfile installs a zod that the app and the SDK both accept.
    scope: repo
    extract:
      zod: {dependency: {name: zod, files: [package.json, package-lock.json, clients/js/package.json]}}
    agree: [zod]
    compare: semver
```

A manifest states a range, the versions it accepts, such as `^3.22.0`; a lockfile states the
version the package manager resolved, such as `3.22.4`. Each value says which it is, and names its
reader and the part of the file it stands in, so an `agree` failure lists which file pins which
version:

```console
FALSE zod-in-range (repo)
  why:  The lockfile installs a zod that the app and the SDK both accept.
  said: agree: the version "4.0.1" does not satisfy the range "^3.22.0"
          "^3.22.0" (range >=3.22.0 <4.0.0-0) at package.json:5 from package.json dependencies (range) (zod)
          "4.0.1" (version 4.0.1) at package-lock.json:14 from package-lock.json packages (resolved) (zod)
```

Under `compare: semver` every version among the values must satisfy every range (see
[`compare`](#compare)). `only: range` keeps the ranges alone and `only: resolved` the resolved
versions alone, so `assert` can test one against the other with `semver_satisfies`. The same rule
holds for a Rust workspace, since the Cargo.toml reader hands back the range Cargo reads (below):

```yaml
extract:
  asked: {dependency: {name: tokio, files: [Cargo.toml], only: range}}
  locked: {dependency: {name: tokio, files: [Cargo.lock], only: resolved}}
assert: locked.all(v, asked.all(r, semver_satisfies(v, r)))
```

errata's semver reads a range by npm's rules where the ecosystems differ (see
[`compare`](#compare)), so a reader whose tool reads a range otherwise hands back the range the
tool reads, written so that semver reads it the same way:

| Ecosystem | The file writes | The value | Why |
|---|---|---|---|
| Cargo | `tokio = "1.38"`, `"1.2, <1.5"` | `^1.38`, `^1.2, <1.5` | Cargo reads a comparator with no operator as `^`, so 1.40.0 satisfies `1.38`; a wildcard such as `1.*` is left as it is |
| Composer | `~1.2` | `~>1.2` | Composer's tilde of two numbers runs to the next major, >=1.2 <2.0, where npm stops at 1.3; a tilde of three numbers reads alike in both |
| Composer | `7.1`, `=7.1` | `==7.1` | a bare version is that version alone, where npm reads `7.1` as every 7.1 release |
| Composer | `^3.0@dev`, `1.0 \| 2.0` | `^3.0`, `==1.0 \|\| ==2.0` | a stability flag says which releases Composer may pick, not which versions, and is cut; a single `\|` is `\|\|` |
| Composer | `@dev` | `*` | a stability flag alone is any version at that stability |
| RubyGems | `"7.1"`, `"= 7.1"` | `==7.1` | a bare version is that version alone |
| Poetry | `foo = "1.2"`, `{ version = "1.2" }` | `==1.2` | a bare version is that version alone; `^`, `~` and `~=` read alike in Poetry and in semver |

npm's, pnpm's and Yarn's ranges, PEP 508 requirements, which always write an operator, and every
resolved version but a requirements file's pin, which loses its `==` (below), are the text the
file wrote.

`files` is required. It reads as `env`'s does: each entry a file, which must exist, or a glob
matched against the files git tracks (see [A set of files](#a-set-of-files)), and each file is read
by the reader its name chooses, from the table below. A file no reader reads does not load
when an entry names it, and makes the rule unknown when a glob matches it. The files hold at most
`max_bytes` in all, and so do the values.

`name` is the package as its ecosystem names it: `golang.org/x/mod`, `@types/node`, `requests`,
`serde`, `rails` or `monolog/monolog`. Python's readers compare names as PEP 503 normalizes them, so
`Requests_OAuthlib` and `requests-oauthlib` are one package, and Composer's in any case; the others
compare them as written. The value is the text the file wrote, `v0.42.0` in a go.mod, `>=2.31,<3`
in a requirements file, `~> 7.1` in a Gemfile, but for the ranges the table above rewrites. errata
reads no registry, so a range the file
takes from elsewhere is not literal (see [Values a file only computes](#values-a-file-only-computes)).

`module: true`, in place of `name`, returns the module path each go.mod's `module` directive
declares, so a rule can check that the import path the docs give is the module's. It reads go.mod
files only, and takes no `only`.

| File | Reader | Read as |
|---|---|---|
| `go.mod` | go.mod | each `require` of the package (resolved), and each `replace` of it, with the version it is replaced by; a replace by a directory, or by another module such as a fork, whose version is not the package's, is not literal, and a replace of one version, `replace x v1.1.0 => ...`, gives nothing when the file requires no such version, since it then does nothing. A version written in a form that is not canonical, such as `v1`, `v1.2` or `v1.2.3+meta`, is a query the go command resolves through the module proxy and writes back, `v1` to the latest v1 release: errata gives it as written and not literal, and a replace whose own or required version is such a query may apply, so it is not literal either. Go keeps no lockfile: since Go 1.17 a module's go.mod requires every module its build needs at the version the build selects, so both are resolved. Parsed with golang.org/x/mod/modfile, as the go command parses it |
| `go.work` | go.work | each `replace` of the package, which overrides it in every module of the workspace; a go.work requires nothing, so a replace of one version counts whichever version the modules require. A version written in a form that is not canonical, such as `v1`, which the go command reads as `v1.0.0`, is given as written and not literal |
| `package.json` | package.json | the range `dependencies`, `devDependencies`, `peerDependencies` and `optionalDependencies` give it. A protocol, such as `workspace:*`, `file:`, `npm:` or `git+https:`, a GitHub shorthand such as `user/repo`, and a dist-tag such as `latest` are not literal, and so is an empty range, which npm reads as any version and errata writes as `""`. An alias, `"zod-old": "npm:zod@^3.20.0"`, installs zod under another key and gives `^3.20.0` for zod. The package's own `version` is `package_version`'s |
| `package-lock.json`, `npm-shrinkwrap.json` | package-lock.json | from lockfile version 2 on, the version of each copy `packages` installs, such as `node_modules/zod` or `node_modules/a/node_modules/zod`, and of the package's own entry, `""`, or a workspace's, which name it by `name`; in version 1, each entry of the nested `dependencies`. The `name` and `version` at the top are the package's own too. An alias's entry names the package it installs, by its `name` from version 2 on and by a version `npm:zod@3.20.2` in version 1. A version that is a URL, as a package from git records, is not literal |
| `pnpm-lock.yaml` | pnpm-lock.yaml | each key of `packages` that names the package: `/zod/3.22.4` in lockfile version 5, `/zod@3.22.4` in 6 and `zod@3.22.4` from 9 on, without the peers a key ends in. A key from a path, git or a tarball, such as `foo@file:../foo`, names no version and is not literal |
| `yarn.lock` | yarn.lock | the `version` of each entry whose descriptors name the package, in Yarn 1's own format and in the YAML of Yarn 2 and later. A workspace's, a link's or a portal's entry, `name@workspace:.`, `name@link:../foo` or `name@portal:../foo`, records `0.0.0-use.local`, no version, and is passed over. An alias, `zod-old@npm:zod@^3`, names zod |
| `pyproject.toml` | pyproject.toml | the specifiers of each PEP 508 requirement of `project.dependencies`, `project.optional-dependencies`, `dependency-groups`, `build-system.requires`, `tool.uv.dev-dependencies` and each group of `tool.pdm.dev-dependencies`, such as `>=2.31,<3`, without the extras and the marker; a requirement from a URL, `name @ https://...`, is not literal. Poetry's `tool.poetry.dependencies`, `tool.poetry.dev-dependencies` and each group's `dependencies`: a string, or the `version` of a table, read as Poetry reads it (above). A requirement without a specifier, or a table without a version, from git or a path, gives nothing |
| `requirements*.txt`, `requirements*.in`, `constraints.txt`, and each `.txt` and `.in` file in a directory named `requirements`, such as `requirements/base.txt` | requirements | the specifiers of each line's PEP 508 requirement, at the line they start on. In a `.txt` file, a requirement that pins one version, `requests==2.31.0`, is that version, `2.31.0`, resolved: pip installs exactly it, and pip-compile writes its output as such pins. In a `.in` file, pip-tools' input, a pin is what a person asks for, which pip-compile then resolves, so it stays a range. A pin with a wildcard, `==2.31.*`, stays a range. A `\` at the end of a line that is not a comment joins the next line to it with nothing between, as pip joins them, and so does one on the last line; a `#` at the start or after a blank then starts a comment, which takes in any line a `\` joined after it; a line that starts with `-`, such as `-r` or `-e`, names no version, and the options after a requirement, such as `--hash=` after a space or a tab, are not part of its range. A `${VAR}`, which pip takes from the environment, is not literal |
| `poetry.lock`, `uv.lock` | poetry.lock, uv.lock | the `version` of each `[[package]]` named for the package; uv lists the project itself too |
| `Cargo.toml` | Cargo.toml | the range each of `[dependencies]`, `[dev-dependencies]` and `[build-dependencies]` gives it, each under `[target.<cfg>]` too, and `[workspace.dependencies]`: a string or a table's `version`, read as Cargo reads it (above), the key or a renamed dependency's `package =` naming it. `[patch]` is not read: it replaces a package's source, most often with git or a path, which names no version. `{ workspace = true }` inherits the workspace's and is not literal, with the line of its `workspace = true` as the file wrote it as the text, such as `serde = { workspace = true }`, unless the same file's `[workspace.dependencies]` gives the package, which is then the value |
| `Cargo.lock` | Cargo.lock | the `version` of each `[[package]]` named for it, the workspace's own packages included |
| `Gemfile` | Gemfile | each requirement of a `gem` statement for the package: `gem "rails", "~> 7.1", ">= 7.1.2"` gives two values, each read as RubyGems reads it (above). A Gemfile is Ruby, so errata reads it with its lexer (see [`comment`](#comment)) and runs nothing: a string with `#{}` and a requirement that is no string, such as a constant, are not literal, the options after the requirements, such as `require: false`, give nothing, and a gem a method or a loop declares, or one whose name or requirements are written as `%q(...)`, is not read |
| `Gemfile.lock` | Gemfile.lock | the version of each gem in the `specs:` of a `GEM`, `PATH`, `GIT` or `PLUGIN SOURCE` section, without the platform after a `-`, as in `nokogiri (1.16.2-x86_64-linux)`. The ranges of a gem's own dependencies, indented below it, and the `DEPENDENCIES` section, which copies the Gemfile, are not read |
| `composer.json` | composer.json | the range `require` and `require-dev` give it, read as Composer reads it (above); `self.version`, the root package's version, and a branch, such as `dev-main` or `2.x-dev`, are not literal |
| `composer.lock` | composer.lock | the `version` of each package of `packages` and `packages-dev`; a branch installed, such as `dev-main`, is not literal |

The JSON, TOML and YAML files hold to the node limits of their formats, and go.mod and go.work to
theirs (see [Limits](#limits)). A file that does not parse, or that writes a range or a version as
anything but a string, which its tool refuses too, makes the rule unknown, and so does a value that
is empty or holds a control character. The text of a Cargo dependency that inherits the
workspace's range is its line, and that line takes no such check, since TOML puts a tab between a
key and its value as readily as a space; the reports escape what a terminal or a viewer would act
on in it. A TOML date or time, such as `x = 1979-05-27` in a Cargo.toml
or `version = 1979-05-27` in a lockfile, is no string. A byte order mark is skipped where the file's
tool skips one: in npm's and Cargo's files, the line files and the Gemfile. Python's TOML files and
Composer's JSON refuse one, as their tools do.

### `envfile`

`envfile` reads a dotenv file as docker compose reads its `.env` file and a service's
`env_file:`, with the rules of compose-go v2.16.2, the parser compose uses. `name` selects one
key; without it the extractor returns the values of every key, in file order. Each value is at
the line of its key.

```yaml
extract:
  port: {envfile: {file: .env, name: API_PORT}}
  every: {envfile: {file: deploy/app.env}}
```

- An assignment is `KEY=value`, `export KEY=value` or `KEY: value`, and white space around the
  separator is dropped. A key holds letters, digits, `_`, `.`, `-`, `[` and `]`.
- A line that starts with `#` is a comment. In an unquoted value a space and `#` start a comment;
  a `#` after no space, or after a tab, is part of the value. White space at the end of an
  unquoted value is dropped.
- A value in `'` is as written, with `\'` read as `'`, and nothing in it is substituted.
- A value in `"` reads the escapes `\n`, `\t`, `\r`, `\a`, `\b`, `\f`, `\v`, `\"`, `\\`, `\$`
  and `\0` with three octal digits (`\0101` is `A`), then substitutes. compose keeps any other
  `\` as written, and writes `\012` as `\12`.
- A quoted value can span lines. compose reads what follows its closing quote on the line as the
  next assignment.
- `$NAME`, `${NAME}`, and `${NAME}` with `:-`, `-`, `:+`, `+`, `:?` or `?` and a word substitute,
  in an unquoted or a `"` value; `$$` is `$`. A `${` that starts none of these makes the rule
  unknown.
- A line of a name alone, `KEY`, takes the value from the environment.
- A file whose last line holds no `=`, `:` or line break sets the empty key to that line, as
  compose does, and so does a line `=x`, which has no name. Without `name:` such a value comes
  back with the others.

A substitution reads the keys the file sets before it. compose reads any other name from the
shell's environment, which errata does not read, so a value that needs one is returned as the
file wrote it between its quotes and marked not literal (see
[Values a file only computes](#values-a-file-only-computes)). compose also lets the environment
override a key the file sets, in the substitutions after it; errata reads the file's own value.
A line of a name alone, `A`, is an empty value marked not literal, and so is every substitution
of `A` after it. That is cautious: compose keeps an earlier `A=1` when the environment does not
set `A`, but errata cannot tell whether it does. In a template file, named with a suffix such as
`.j2` or `.tpl` ([`ini`](#ini) lists them), a value whose text as written the engine
renders is not literal, and so is every substitution of its key, since the engine may render it
to anything: `A={{ port }}` in `.env.j2`, and `B=${A}` after it. So is a key set on a line inside
one of the engine's blocks, which the rendered file may not hold, as [`ini`](#ini) says, and every
substitution of it: after `# {% if prod %}`, `A=1` and `# {% endif %}`, both `A` and a `B=${A}`
below are not literal. compose refuses a line that is only a tag, so in an env file only a tag in
a comment line reads on.

Each value carries the key it is set under, and the text and SARIF reports print it beside the
location: `"8080" at .env:2 key "API_PORT" (env)`.

| File says | Value |
|---|---|
| `A=x`, then `B=${A}/y` | `x/y` |
| `A=`, then `B=${A:-d}` | `d` |
| `B=${A}/y`, with no `A` set before | `${A}/y`, not literal |
| `B=${A:-d}`, with no `A` set before | `${A:-d}`, not literal |
| `A`, then `B=${A}` | `${A}`, not literal; the line `A` is an empty value, not literal |
| `B='${A}'` | `${A}` |

compose keeps the last value of a key set twice; errata returns every one, so an agree rule sees
the file disagree with itself. A file compose refuses makes the rule unknown: a key with another
character or a space inside it, a quote never closed, `export` with no name after it, a `${`
that starts no substitution, and `${A:?}` where the file sets `A` empty. A `name` that no line
of an env file can set, such as `API PORT`, does not load.

systemd's `EnvironmentFile=` reads another dialect (systemd.exec(5), and systemd 257 run with
`systemd-run -p EnvironmentFile=`), which the [`systemd`](#systemd) kind reads. `envfile` reads only compose's; a plain `KEY=value` line
of letters, digits and `_` reads the same in both. Where the two differ:

- systemd substitutes nothing: `${OTHER}` is the value's text.
- A line that starts with `;` is a comment to systemd; compose refuses `;` in a key.
- systemd ignores a line with no `=`: a name alone, which compose takes from the environment,
  and `KEY: value`, which compose reads as an assignment.
- systemd ignores `export KEY=value` as an invalid assignment; compose sets `KEY`.
- systemd ignores a key with `.` or `-` in it, such as `x.y=3`; compose sets it.
- A `#` inside an unquoted value is part of it to systemd; compose cuts the value at a space and
  `#`.
- An unquoted value reads `\` as POSIX shell does: `\` keeps the character after it, and a `\`
  at the end of a line joins the next. compose keeps an unquoted `\` as written, and joins no
  lines.
- In `'` systemd reads no escape at all; compose reads `\'` as `'`.
- In `"` systemd keeps the character after `\` only for `"`, `\`, `` ` `` and `$`, joins a line a
  `\` ends, and keeps any other `\` with the character after it; compose reads `\n` as a line
  break, and the other escapes above.
- systemd joins what follows a closing quote to the value: `E="x" y=2` sets `E` to `xy=2`.
  compose sets `E` to `x` and reads `y=2` as the next assignment.

### `gitlab-ci`

`gitlab-ci` returns the value one job of a GitLab CI file sees: of a CI/CD variable with
`variable`, or of a job keyword with `key`, such as `image` or `script`, or a key below one, such
as `image.name`. Each value is at the file and line that set it, which may be an included file.

```yaml
checks:
  - id: tag-agrees
    reason: The pipeline deploys the tag the chart names.
    scope: repo
    extract:
      ci: {gitlab-ci: {file: .gitlab-ci.yml, job: deploy, variable: TAG}}
      chart: {yaml: {file: chart.yaml, path: image.tag}}
    agree: [ci, chart]
```

errata builds the configuration as GitLab does, from the repository's files only, in GitLab's
documented order:

1. Each `include:` of a local file is read and merged, a nested include first, then each file in
   its order, and the including file's own keys last. Hashes merge at any depth and any other
   value replaces the one before it, so a list is never merged item by item. A leading `/` is the
   repository root. A local include with `*` or `**` includes every file git tracks that it
   matches, in git's order, as GitLab's code matches it (`search_files_by_wildcard_path` in its
   `app/models/repository.rb`): `*` within one directory, `**/` zero or more directories, and any
   other `**` across directories too. So `configs/*.yml` reads the `.yml` files in `configs`, and
   `configs/**.yml` and `configs/**/*.yml` both read those in `configs` and every directory below
   it. GitLab's documentation says the last reads only the directories below `configs`; its code
   reads `configs` too, and errata follows the code. `?` and `[` are literal. The file that
   includes the wildcard is left out of what it matches, as GitLab leaves it out, and a name with a
   `.` or `..` name or an empty one, such as `./ci/*.yml`, matches no file, since GitLab does not
   clean it. The files come from the listing a `files:` glob reads
   ([A set of files](#a-set-of-files)), so its bounds and its sparse checkout rule hold, and no
   witness's fixture is ever among them; each counts against the 150 files. A wildcard that
   matches no file includes none.
2. A job's `extends` is merged deeply, each parent in its order with the job's own keys last, over
   at most 11 levels. A keyword set to `null` is unset; a variable set to `null` is not literal. A
   parent that is a `!reference` is refused, since GitLab merges extends before it follows one.
3. A `!reference [job, keyword, ...]`, or `[job]` for the whole job, selects from that
   configuration, after extends, so it sees what the job it names inherits. A list inside a list,
   which a `!reference` in a script makes, is flattened. Tags and lists nest at most 10 levels
   deep, counted along every chain, through a job or a tag reached twice too.
4. A keyword the job does not set comes from `default:`, or from the top level for the deprecated
   `image`, `services`, `cache`, `before_script` and `after_script`, unless `inherit:default`
   leaves it out.
5. A variable comes from the highest of the job's `rules:variables`, the job's `variables` with a
   `parallel:matrix` instance's over them, `workflow:rules:variables` and the top level's
   `variables`. `inherit:variables` can leave out the last two.

YAML anchors and merge keys work within one file, as in GitLab; an alias of an anchor in another
file does not parse. A variable's long form gives its `value:`, and an empty string without one,
at the line of the variable's name. A `!!bool` scalar holds any word Psych reads as a boolean,
such as `!!bool yes` or `!!bool On`, as GitLab loads it.

Which rule of a `rules:` list matches is the pipeline's to decide, so every rule that can match
gives a value: its own variable, or the value below it when it sets none. A rule with
`when: never` gives none, since the job does not run when it matches, and so do the rules after
one with no `if`, `changes` or `exists`, which always matches. Each `parallel:matrix` instance is
a job of its own, so each gives the value it sees. An agree rule therefore holds only when every
value the job can see agrees. A value that holds only in some cases carries its condition, which
a message names after its place: the rule's test, such as `if $CI_COMMIT_TAG` or
`changes [src/a.go]`, prefixed `workflow` for a workflow rule, or the instance's name as GitLab
gives it, such as `build: [linux, 3.12]`, with `010` written as `8` as GitLab's loader reads it. A
message quotes 64 bytes of a condition, so a longer name writes as `...` the value of a key every
entry sets to one value, such as `build: [..., ..., monitoring-and-alerting-two]`. Two instances
whose different names still begin with the same 64 bytes have their names cut and numbered by
their place in the matrix, so no two print the same condition, such as
`build: [aws, europe-west-1-a-long-zone-name, monitoring-and-a #1`; instances GitLab names
alike keep one name:

```text
FALSE tag-agrees (repo)
  why:  The pipeline deploys the tag the chart names.
  said: agree: the values differ
          "1.4.0" at .gitlab-ci.yml:12 when "if $CI_COMMIT_TAG" (ci)
          "1.3.9" at .gitlab-ci.yml:9 (ci), chart.yaml:1 (chart)
```

A variable that some rules or instances set and nothing else sets is refused: in the other
cases the job sees no value from the files.

Some values are out of reach offline, and errata never guesses them:

| What | Result |
|---|---|
| a `remote`, `project`, `template` or `component` include, a local include whose name holds a variable (`$`), wildcard or not, or an include with `rules:`, local or not | the rule is unknown, naming the include, since such a file could set any key |
| a `trigger:` job's variable | refused: the job passes it to a downstream pipeline |
| a variable of a job that gets a dotenv report: from a job its `needs:` names with artifacts, or with no `needs:` one `dependencies:` lists, or by default any job of an earlier stage, that declares `artifacts:reports:dotenv`, under `default:` too | unknown, naming the job that writes the report: the pipeline writes it as it runs, and it outranks the file |
| a variable set in a project's, group's or instance's settings, by a pipeline, or a predefined one such as `CI_COMMIT_SHA` | not in the files: a variable no file sets is no value, and the message says it may come from those settings; `allow_empty` still lets the rule hold |
| a value that names a variable, as `$NAME`, `${NAME}`, `%NAME%`, `$$` or an input's `$[[ ]]` | not literal; `expand: false` keeps it literal, and so does a `script` line, whose `$` is the shell's |
| a plain scalar that GitLab's YAML loader, Psych, types as something other than a string: `3.10` (the float 3.1), `010` (8), `1:30` (90), `yes`, `null` or a date, and a scalar with a tag other than `!!str`, which Psych types the same way: `!!int 010`, and every `!!float` and `!!binary` | not literal; quote it, or tag it `!!str`, to give the job the text. A decimal integer such as `20` is literal |

Variables in GitLab's settings or a pipeline rank above every value in the file, so a value errata
returns is what the file gives the job; a setting in GitLab can still override it.

errata refuses what GitLab refuses where the check is cheap: a keyword set both at the top level
and under `default:`, a long form in a rule's `variables:`, a job variable with a `description:`,
a variable named as YAML reads a boolean, such as `ON`, an include that is not a `.yml` or
`.yaml` file or that is empty, an `expand:` that is not `true` or `false`, a tag other than
`!reference` and YAML's own, a `parallel:matrix` value that Psych reads as a boolean or a null,
such as `true`, `off`, `~` or an empty one, and a `parallel:matrix` of more than 200 jobs.

The file, and every file it includes, hold at most `max_bytes` bytes in all, the nodes that size
allows, and 1000000 nodes that YAML aliases and merge keys add, as one file does; a configuration
includes at most 150 files, GitLab's default limit, nested includes counted, and an include's name
is at most 4096 bytes. Merging, extends and `!reference` take one step for each key they merge or
item they follow, at most 1000000 for one extractor, the bound YAML aliases have: a few hundred
bytes of nested `!reference` lists could otherwise stand for millions of script lines. A merge
replaces a key it sets twice in constant time, and the values are kept in a set and refused at
the value limit as they are gathered, so each pass is linear in the files. A job that is hidden
(its name starts with `.`) or a global keyword such as `workflow` does not load as `job`, and a
job the files do not define makes the rule unknown.

### `shell`

`shell` returns every value a shell script gives one variable, each with its line, in file order:
`X=v`, `export X=v`, `readonly X=v`, `declare X=v`, `typeset X=v`, `local X=v`, the `X=v` before a
command, and the default in `${X:-v}`, `${X-v}`, `${X:=v}` and `${X=v}`. errata parses the script
with mvdan.cc/sh, the parser behind shfmt, and runs none of it. Text that only looks like an
assignment is none: `X=1` in a string, a comment, a here-document whose delimiter is quoted, or an
argument such as `echo X=1`. A default in a here-document whose delimiter is not quoted is an
expansion, so it counts.

```yaml
checks:
  - id: port-agrees
    reason: The chart and the start script use one port.
    scope: repo
    extract:
      chart: {yaml: {file: values.yaml, path: port}}
      script: {shell: {file: deploy.sh, name: API_PORT}}
    agree: [chart, script]
```

The dialect is `language:` when the extractor names one: `bash`, `posix` or `mksh`. Otherwise the
shebang names it, through `env` too: `bash`; `sh`, `dash`, `ash` and `posh` as posix; `mksh`,
`ksh`, `lksh` and `pdksh` as mksh. A shebang that names another program, such as `python3` or
`zsh`, makes the rule unknown, and `language:` reads the file anyway. A script without a shebang
is read by its name, `.bash` as bash and `.mksh` or `.ksh` as mksh, and any other as bash, which
parses the most. A script that does not parse in its dialect makes the rule unknown, and the
message names the line, the dialect and what chose it. In posix the parser reads `export`,
`readonly` and `local` as commands, and errata reads their `X=v` arguments as dash does.

A value is the text the variable holds once the shell has removed the quotes: `X='a b'`,
`X="a b"` and `X=a\ b` each give `a b`, and bash's `$'a\tb'` gives a tab between them. A default
in double quotes keeps single quotes as text, so `"${X:-'q'}"` gives `'q'`. What errata makes of
quotes and backslashes is compared with bash 5.2 and dash 0.5.12 over 800 generated scripts each.

A reference `$Y` or `${Y}` in a value reads the value the script last gave `Y` before it, while
nothing the script ran could have changed `Y` another way, so `BASE=/opt` and then `X=$BASE/bin`
give `X` the value `/opt/bin`. A shell has more ways to change a variable than errata could list
(`declare -i`, `unset "Y"`, `read` with no name, a nameref, arithmetic in `[[ ]]`, a function), so
errata lists the statements it knows change none, and stops resolving at the first statement that
is not one of them. A reference resolves when every statement before it in the file keeps the
variables, its own statement is quiet, the last assignment of `Y` before it gave a literal value,
and the shell does not own `Y`. Otherwise its value is not literal.

A statement is quiet when expanding its words changes no variable:

- it runs in the foreground, not negated with `!`, and is a simple command or a declaration, not
  a pipeline, a list, a loop, a condition, a group, a subshell or a function;
- its words hold only text, quotes, and parameters (`$Y`, `${Y}`, `$1`, `"$@"`) with no operator
  but a default, `${Y:-v}`, `${Y-v}`, `${Y:+v}` or `${Y+v}`, whose word is quiet too; no `$( )`,
  no arithmetic, no `${Y:=v}`, no index or slice, no pattern such as `@(a|b)`;
- an assignment in it has no index and is no array;
- its redirects hold no here-document, and name no descriptor by a variable (`{fd}>f`); a
  statement with a redirect assigns nothing, and is not posix's `export`, `readonly` or `local`.

A quiet statement keeps the variables when it is one of these:

- assignments alone: `Y=v Z=w`;
- `export` or `readonly` with no option, each argument a name alone or `NAME=v`, in posix too,
  where the name is plain text at the start of the argument;
- a command with no assignment before it, named by plain text with no quote or backslash, from
  this list: `echo`, `true`, `false`, `:`, `cd`, `mkdir`; `printf` when its format is plain text
  that does not start with `-` and holds no `%`, since bash's `%n` assigns; and `set` when each
  argument is plain text, options of `e` and `u` after `-` or `+`, and `-o` or `+o` with
  `errexit`, `nounset` or `pipefail`.

`set -x` is not on the list: xtrace expands `PS4` before each command, and `PS4` may assign. For
the same reason a shebang ends resolution from the first line, whatever `language:` says, unless
each argument it passes the shell is an option `set` may take here: `#!/bin/sh -eu` keeps it,
and `#!/bin/bash -x` ends it. The first argument after `env` or `busybox`, which names the shell,
is passed over.

A variable made read only keeps its value when a later statement assigns it, which bash reports
and then goes on from, so after that its references are not literal. The shell owns `RANDOM`,
`SRANDOM`, `SECONDS`, `LINENO`, `EPOCHSECONDS`, `EPOCHREALTIME`, `_`, `PPID`, `UID`, `EUID`,
`SHELLOPTS`, `GROUPS`, `FUNCNAME`, `HISTCMD`, `DIRSTACK`, `PIPESTATUS`, `PWD`, `OLDPWD`,
`COLUMNS`, `LINES`, `OPTIND`, `OPTARG`, `REPLY`, `MAPFILE`, `USER_ID`, `PATHSEP`, `ERRNO`, and
every name that starts with `BASH`, `KSH` or `COMP_`: it sets them itself, or keeps them from an
assignment. A value that would make the text longer than `max_bytes` is not resolved, and the walk
keeps at most `max_bytes` of variables' values for references to resolve. errata reads the
script, not the environment it runs in: a function that the environment or `BASH_ENV` defines
under the name of a command on the list is not seen.

A declaration expands all its arguments before it assigns any, so `declare Y=2 X=$Y` reads the
`Y` before it. A variable the script never assigns, such as `$HOME`, comes from the environment,
which errata does not read, so its reference is not literal.

A value the shell computes otherwise comes back as the script wrote it, marked not literal
([Values a file only computes](#values-a-file-only-computes)): a rule that would compare it is
unknown, and a rule that lists the extractor in `skip_not_literal` leaves it out. These are not
literal:

- a value that expands a command, arithmetic or a parameter errata does not resolve:
  `$(cat VERSION)`, `` `date` ``, `"$HOME/bin"`, `${Y:-v}`, `$((1+2))`;
- a tilde at the start of a value or after a colon, which the shell turns into a home directory:
  `~/bin`, `a:~/b`;
- an array (`X=(a b)`), an element (`X[0]=v`) and an append (`X+=v`);
- a `{` in an argument of a declaration in bash or mksh, since bash may expand braces there:
  `declare X={a,b}` sets `b`;
- every value of a variable that a declaration anywhere in the script gives an option that
  changes what the shell stores, such as `-i` (`declare -i X; X=1+2` holds 3), `-l`, `-u`, `-a`
  or `-n`, and every value of a `nameref`. The options that keep a value as written are `-g`,
  `-r`, `-t`, `-x` and `-I` for `declare`, `typeset` and `local`, and `-n` for `export`. A value
  after an argument errata cannot read as an option or a name, such as `"$opts"`, is not literal
  either;
- the forms errata does not decode: `$"..."`, an escape in `$'...'` other than `\a \b \e \E \f
  \n \r \t \v \\ \' \" \?`, an octal byte or a `\x` byte, `$'...'` of any kind in posix and
  mksh, a pattern such as `@(a|b)`, a quote or backslash in a default in a here-document, in
  arithmetic, or in the pattern or replacement of `${Y#p}`, `${Y%p}` or `${Y/p/r}`, and in mksh a
  single quote in a double-quoted default;
- in a template file, named with `.j2`, `.jinja`, `.jinja2`, `.tmpl`, `.tpl`, `.tftpl` or `.erb`,
  a value that holds what the engine renders, as [`ini`](#ini) says, and an assignment on a line
  inside one of the engine's blocks, which the rendered script may not hold; the variable is then
  unknown, so `X=$Y` is not literal after `Y=1` in a block.

A quoted argument of a declaration assigns too, since the shell splits it at its `=` once the
quotes are removed: `export "X=1"` gives `1`, and `export "X=$PATH:/x"` comes back as written,
not literal. errata reads no other way a script sets a variable: `read X`, `for X in`,
`let X=1`, `(( X=1 ))`, `printf -v X`, `eval`, `env X=v cmd`, `command export X=v`,
`builtin export X=v`, or a file the script sources.

mvdan.cc/sh parses by recursion and sets no limit on nesting, so a script of nothing but `"$("`
grows its stack until Go ends the process: 1 MiB of them passed Go's limit of 1 GB. errata stops
the parse once its stack is 20000 calls deep, which about 340 levels of parentheses in `(( ))`
reach, the costliest nesting, or 1800 levels of `$( )`. A pipeline, a list of `&&` and `||`, and a
sum such as `1+1+1` are parsed in a loop but walked by recursion, one level for each command or
term, so errata refuses a script whose syntax is more than 20000 levels deep. Defaults nested in
defaults return each one whole, so their values can hold more than the file; they hold at most
`max_bytes` in all, as every extractor's values do.

errata parses one statement at a time and holds the syntax of one statement whole while it reads
it, so it refuses a statement longer than an eighth of `max_bytes`, 4 MiB at the default, counted
to the 1 KiB the parser reads at a time. A function or a group that holds the whole script is one
statement: of eight real scripts measured, the largest statement is nvm.sh's, 149802 bytes.
The densest statement measured, a pipeline of `a|a|a`, is refused at the limit with 0.88 GB at
the default; a group of `a;a;a` at the limit reads with 0.60 GB, and a word of `$a$a$a` with
0.46 GB. The parser hands out words from batches that keep earlier statements reachable, so errata
clears each statement's syntax once it has read it: eight groups at the limit, 31 MiB, take 16.5 s
and 1.43 GB, and eight words 8.9 s and 0.77 GB, where before they took eight times one statement.
At 64 MiB a statement may be twice as large. 32 MiB of 16 million one-word commands takes 10.3 s and
157 MB, 8.4 million assignments 9.2 s and 148 MB, refused at the value limit, and statements of
3 MiB each 5.1 s and 583 MB. Values with references and nested defaults are charged to the work
meter like the rest of a read.

### `github-actions`

`github-actions` returns the value one job of a GitHub Actions workflow sees, or one step of it
with `step`: of an environment variable with `variable`, or of a key with `key`, such as
`runs-on`, `uses`, `container.image` or a `with:` input such as `with.node-version`, which is
where workflows pin versions. Without `step`, `key` reads the job's keys and `variable` the value
the job's `env:` gives its steps. A step is named by its `id`, or else by its `name`, which must
then be one step's; a step inside a `parallel:` group counts as the job's. `key` reads the key at
the level it names: `key: shell` of a step is the step's own `shell:`, not a `defaults.run.shell`
the step runs with.

```yaml
checks:
  - id: node-agrees
    reason: CI tests on the node version .nvmrc pins.
    scope: repo
    extract:
      ci: {github-actions: {file: .github/workflows/ci.yml, job: test, step: setup, key: with.node-version}}
      nvm: {regex: {file: .nvmrc, pattern: '^(\S+)'}}
    agree: [ci, nvm]
```

A variable comes from the step's `env:`, else the job's, else the workflow's: "GitHub uses the
most specific variable". When none sets it, the job's `container.env` gives it. GitHub documents
no order between `container.env` and the others, so a variable both set is unknown.

Each combination of a job's `strategy.matrix` is a job of its own, so a value whose expressions
are all matrix variables, such as `${{ matrix.node }}`, `node-${{ matrix.os }}` or
`${{ matrix.node.version }}`, gives one value for each job. errata builds the combinations as
GitHub documents: every combination of the variables, the last varying fastest, less those an
`exclude:` entry partly matches, and then each `include:` entry added to every combination whose
variables it agrees with, or made a job of its own when it agrees with none. A variable a job
does not have is the empty string. Each value carries the job's name as GitHub gives it, which a
message names after its place: the job's `name:`, or its id when it has none, followed by the
combination's values, such as `test (ubuntu-latest, 18)`. A `name:` that references the matrix,
such as `Unit ${{ matrix.node }}`, has its references replaced and no values after it. A value
that names no matrix variable, such as `runs-on: ubuntu-latest`, is one value with no name, even
when the matrix is out of reach. A value that is one matrix variable alone is at the line of the
matrix that sets it. A message quotes 64 bytes of a name, so a longer one cuts the `name:` to leave
room for the values, to no fewer than 16 bytes, and writes the values every job shares as `...`:

```text
FALSE node-agrees (repo)
  why:  CI tests on the node version .nvmrc pins.
  said: agree: the values differ
          "20.11.1" at .github/workflows/ci.yml:9 when "test (20.11.1)" (ci), .nvmrc:1 (nvm)
          "18.20.4" at .github/workflows/ci.yml:9 when "test (18.20.4)" (ci)
```

An agree rule therefore holds only when every job of the matrix agrees.

Some values are out of reach offline, and errata never guesses them:

| What | Result |
|---|---|
| any other `${{ }}` expression: a secret, a repository variable (`vars.`), an input, the `env` context, a step's output, or a matrix variable in the workflow's own `env:` | not literal, as written |
| a matrix, a matrix variable or an `include:` or `exclude:` list that an expression writes | unknown for a value that names the matrix, naming the expression |
| an expression in an `include:` or `exclude:` entry or in a matrix variable's value, where errata would compare it to decide whether a job runs or what it holds | unknown for a value that names the matrix, naming the job and the expression: GitHub evaluates it before it compares |
| an `env:` that is an expression | unknown, naming it |
| a step or a variable of a job that calls a reusable workflow with `uses:` | unknown: for a local one (`./` or `$/`) the message names the file to read instead, since the caller's `env:` does not reach it, and for one from another repository it names that workflow |
| a variable read after a `run:` step whose script, `shell:` or `env:`, or the `defaults.run.shell` it runs with, names `GITHUB_ENV` in any case (PowerShell's `$env:github_env`) or as `${{ github.env }}` or `${{ github['env'] }}` | unknown, naming that step: what the script writes there reaches every later step |
| a variable read after a `run:` step when the job's `env:`, its `container.env:` or the workflow's `env:` names `GITHUB_ENV` in one of those ways | unknown, naming that `env:` and the step: the script sees all three |
| an `include:` or `exclude:` entry whose nested mapping matches a value's keys only in another case, such as `{x: 1}` against `{X: 1}`, where the values agree | unknown for a value that names the matrix, naming the job: GitHub reads such keys in any case and does not document how it compares them |
| a matrix that passes 256 jobs only through jobs an expression may remove or add, as with an `exclude:` entry an expression writes | unknown for a value that names the matrix, naming the expression; a matrix of more than 256 jobs no expression decides is refused |
| a variable read after a step that runs an action not on the list below, a local (`./path`) or a Docker action among them | unknown, naming the action and its step: an action writes `GITHUB_ENV` with `core.exportVariable` |
| a variable no `env:` sets: one the runner sets, or the repository's variables | no value, and the message names where errata looked; `allow_empty` still lets the rule hold |
| a plain scalar that YAML's core schema types as something other than a string: `3.10` (the number 3.1), `010` (10), `1e3`, `0x1f`, `True`, `null`, or an integer of more than 15 digits, which a double cannot hold, and a scalar with a tag other than `!!str` | not literal; quote it to give the step the text. A decimal integer such as `20`, and `true` and `false`, are literal |

errata reads no action's code. It knows a short list of actions that set no variable for the
steps after them: `actions/checkout`, `actions/cache` (with `actions/cache/restore` and
`actions/cache/save`), `actions/upload-artifact` and `actions/download-artifact`. Each one's
`action.yml` declares only outputs, and its source calls no `core.exportVariable`. A variable read
after any other action is unknown. The list matches the action by its owner and name, the part
before the `@`, and not by its version, so a later version of a listed action that starts to
export a variable would go unseen. Nor is a ref checked: GitHub can resolve a commit of a fork
of the repository through the listed name, as `actions/checkout@<sha>`, and errata does not
detect that the code is the fork's. errata looks only at the steps before the one read, but an
action's `pre:` runs at the start of the job, before the first step, so an action not on the list
that comes after the step read can still set the variable through its `pre:`, and errata does not
see it.

errata refuses a `GITHUB_` or `RUNNER_` variable, which GitHub does not let a workflow set, a
variable named in another case beside the one asked for (a Windows runner reads them as one), two
keys of one mapping that differ only in case, anywhere in the workflow, such as the matrix
variables `OS` and `os` (GitHub refuses the workflow; errata compares other letters as the runner
compares a context's keys, so `é` and `É` are one and the Kelvin sign is not `k`), an empty
matrix variable, an exclusion
that leaves no job, and a matrix of more than 256 jobs that no expression decides, GitHub's limit.
The product of the variables holds at most 65536 combinations before `exclude:`, and matching the
`include:` and `exclude:` entries takes one step for each key compared and for each 64 bytes of a
value compared, at most 1000000 for one extractor. The values hold at most 100000 in count and
`max_bytes` bytes in all, since a matrix repeats a value for each job.

### `jenkins`

`jenkins` returns the value of one environment variable in a declarative Jenkinsfile: as the stage
that `stage:` names sees it, or as the whole pipeline sees it without `stage:`. errata reads the
file with a lexer of Groovy's comments and string forms and runs none of it.

```yaml
checks:
  - id: tag-agrees
    reason: The chart deploys the tag the pipeline builds.
    scope: repo
    extract:
      ci: {jenkins: {file: Jenkinsfile, variable: IMAGE_TAG, stage: deploy}}
      chart: {yaml: {file: values.yaml, path: image.tag}}
    agree: [ci, chart]
```

errata looks in this order and stops at the first place that gives a value:

1. The `environment` block of the stage, then of each stage around it, innermost first. A stage
   in the `stages`, `parallel` or `matrix` of another stage is inside it.
2. The `environment` block at the top of the `pipeline`.
3. Every `env.X = v` that starts a statement, in file order: in a `script` block, a function, or
   outside the pipeline. errata does not follow the order the stages run in, so each is a value the stage may
   see, and an agree rule holds only when they all agree.
4. Every `def X = v`, only when no environment block and no `env.X =` sets X anywhere in the file,
   since a step's shell does not see a Groovy variable. A type may stand for `def`: a name that
   starts with a capital, as in `String X = 'v'` or `Integer X = 5`, or one of Groovy's primitive
   types, as in `int X = 1`. So may `final`, `static` and `var`. A generic type such as
   `List<String> X`, an array type, and a qualified type such as `java.lang.String X` are not
   read. An `X = v`, `X += v`, `X++` or `++X`, or the same with `--` or with `(X)` for `X`,
   before the `def` or after it, makes the value unknown wherever it stands, as in
   `if (c) X = v`, `def Y = X = v` or the multiple assignment `(a, X) = v`, and so does one
   inside a GString's `${ }`, as in `sh "echo ${X++}"`. A parameter's default, as in
   `def f(X = 2)`, a multiple declaration, as in `def (y, X) = [1, 2]`, and a declaration with a
   generic or array type, as in `List<String> X = []` or `String[] X = []`, count as one too,
   though they declare another variable.

Of two assignments in one environment block the last counts. An environment block counts only
inside the `pipeline` block, at its top or directly in a stage.

A value is literal when it is one string or one decimal integer: `'v'`, `"v"`, `'''v'''`,
`"""v"""`, `/v/`, `$/v/$` or `42`. errata decodes the escapes `\b`, `\t`, `\n`, `\f`, `\r`, `\\`,
`\'` and `\"`; Groovy 2.4 has no `\s`, so `'a\sb'` is not literal. Any other value comes back as the file wrote it, from its first token to its
last, marked not literal ([Values a file only computes](#values-a-file-only-computes)):

- a call, such as `credentials('id')` or `sh(script: 'git describe', returnStdout: true).trim()`,
  a sum such as `'a' + 'b'`, a name such as `params.TAG`, and `true`;
- a string with a `$` in any form, `${BUILD_NUMBER}` and `$NAME` in a GString, and a `$` in
  single quotes too, since errata cannot tell where Jenkins or a step expands it;
- a number Groovy writes back otherwise: `010` is octal, `1_000` is 1000, `1.50` is a BigDecimal;
- a string with an octal or `\u` escape, a backslash in a slashy string, or a carriage return.

Some values are out of reach, and errata never guesses them:

| What | Result |
|---|---|
| a `withEnv(['X=v'])` anywhere in the file, or a `withEnv` whose list holds anything but literal strings, such as a variable, a call, a sum or a string whose name has a `$` | unknown, naming its line: it sets the variable for the steps inside it, over every environment block. `PATH+X=v` counts as setting `PATH` |
| a stage inside a `matrix` that has an axis of the variable's name | unknown, over every environment block: the matrix sets the axis in each cell |
| `@Library`, the `libraries` directive, or a `library`, `load` or `evaluate` step, `this.load` included | unknown when no environment block sets the variable, since a shared library or a loaded file may set it |
| a parameter of the variable's name in `parameters`, or a matrix axis outside the stage's matrix | unknown when no environment block sets it: whoever starts the build chooses a parameter, and errata does not read a matrix's cells |
| `env['X'] = v`, `env."X" = v`, `env?.'X' = v`, `env.setProperty('X', v)`, `env.putAt('X', v)`, or one of these with a name that is not literal; `env.X += v` or another compound assignment, `env.X++`, `++env.X` and the same with `--` or with `(env.X)` for `env.X`; and an `env.X = v` that does not start a statement, such as `env?.X = v`, `this.env.X = v`, `this?.env.X = v`, `(env).X = v` or `if (c) env.X = v`; and each of these inside a GString's `${ }`, as in `"${env.X = 'b'}"` | unknown when no environment block sets the variable: errata reads none of them |
| the variable's name in another case, such as `image_tag` for `IMAGE_TAG`, in an environment block, an `env.` assignment, a `withEnv` or a parameter | unknown: Jenkins does not tell names apart by case, and errata does not guess which spelling a stage sees |
| a variable no file sets, such as `BUILD_NUMBER`, or one from the controller's, a folder's or a node's settings or a plugin | no value, and the message says where errata looked; `allow_empty` still lets the rule hold. Without `stage:`, the message names the stages whose environment sets it |

errata refuses what Jenkins refuses where the check is cheap: a second `pipeline` block, and two
stages of the name `stage:` gives. A `stage:` that names no stage is refused, and so is one the
file may name with a name that is not literal, such as `stage("${name}")`. A file that does not
lex makes the rule unknown: a string or a comment that is not closed, a `'...'` or `"..."` string
over a line end, a `}` that closes no block, a `)` or `]` that closes no bracket, a `{`, `(` or
`[` still open at the end of the file,
and a `}` that closes a block in which a `(` or `[` is still open. In a template file, such as `Jenkinsfile.j2`, a
value with `{{ }}` is not literal, and so is a value on a line inside one of the engine's blocks, as
[`ini`](#ini) says.

The lexer does not understand Groovy beyond what a declarative pipeline states literally. Of a
scripted pipeline it reads only the literal `env.X =` and `def X =` assignments. It does not read:

- `X = v` without `def` or `env.`, which only makes a `def X` unknown;
- which branch of an `if`, a `when` or a loop runs, or what a function or closure returns: an
  assignment inside one is a value like any other;
- a stage's `when`, `options`, `tools` or `agent`, and a matrix's cells;
- which order the stages run in;
- one variable set from another, such as `X = "${Y}-1"`, which is not literal.

Where Groovy's grammar needs more than a lexer, errata takes the reading of the lexer of Groovy
2.4, which Jenkins compiles a pipeline with. A `/` divides after a name, after every keyword but
`final`, `abstract`, `strictfp`, `do`, `goto` and `const`, and after a number, a string, `)`, `]`,
`}`, `++` or `--`, so `return /re/` is a division. It starts a slashy string anywhere else, the
start of a line included, but Groovy skips a line end inside `( )` or `[ ]`, so a `/` after one
divides. In a dollar slashy string only `$$` and `$/` are escapes, and a backslash is itself, so
`$/a\/$` ends at its `/$`. A line end ends a statement, unless it is inside brackets, follows an
operator, or the next line starts with `.`, `?.`, `*.`, `?`, `:`, `?:`, `&&` or `||`. A `++` or
`--` at a line's end goes on to the next line's operand unless a value stands before it, and a
keyword an expression follows (`else`, `return`, `assert`, `throw`, `case`, `in`) or the `)` of
the condition of an `if`, a `while` or a `for` is no value there, so `if (c) ++` then `X` changes
`X`.

The lexer reads the file once, twice with `stage:`, and holds no statement: it keeps the stack of
blocks and strings it is inside, at most 1000 levels deep, and at most 100001 assignments of the
variable, so its memory does not grow with the shape of the file. Of 19 dense shapes at 32 MiB
on the build node (4 cores, 4 GiB), each in a process of its own, every one peaked between 149
and 155 MB, the file's own copies included. A backslash again and again in code took 8.6 s, and a
comma again and again 8.4 s, since a byte that starts no operator of more than one byte is tried
against each. The slowest shape measured since, `[` again and again and never closed, took 9.8 s
on the same node and 33 MiB above the process's base, refused at the end of the file. Blocks nested 999 deep again and again took 7.3 s, a statement
of 16 million names 5.5 s, and 3.4 million `env.X = 1` lines 3.1 s, refused at the value limit.

### `etc`

`etc` reads a Unix configuration file of the keyword and value family as the tool that reads it
does, and returns every value of one keyword, `key`, in the order the tool reads them, each at the
file and the line of its keyword. `dialect` names the tool; left out, the file's name chooses, once
a template suffix is taken off:

| Dialect | Chosen for | Reads as |
|---|---|---|
| `sshd` | `sshd_config`, any file in `sshd_config.d` | OpenSSH's sshd |
| `ssh` | `ssh_config`, any file in `ssh_config.d`, `config` in `.ssh` | OpenSSH's ssh |
| `sysctl` | `sysctl.conf`, any file in `sysctl.d` | procps's `sysctl --system` |
| `resolv` | `resolv.conf` | glibc's resolver |
| `limits` | `limits.conf`, any file in `limits.d` | pam_limits |
| `logrotate` | `logrotate.conf`, any file in `logrotate.d` | logrotate |
| `nsswitch` | `nsswitch.conf` | glibc |
| `modprobe` | `modprobe.conf`, any file in `modprobe.d` | kmod |
| `chrony` | `chrony.conf`, a file in `conf.d` or `sources.d` under `chrony` | chronyd |
| `ntp` | `ntp.conf` | ntpd |

A file whose name chooses no dialect is refused at load; write `dialect:`. The field tables
(`fstab`, `passwd`, `hosts` and the like), `sudoers` and udev rules are not read yet.

```yaml
extract:
  port:     {etc: {file: roles/ssh/files/sshd_config, key: Port}}
  forward:  {etc: {files: [etc/sysctl.conf, etc/sysctl.d/*.conf], key: net.ipv4.ip_forward}}
  git_pw:   {etc: {file: roles/ssh/files/sshd_config, key: PasswordAuthentication, match: {user: git, address: 10.0.0.5}}}
  rotate:   {etc: {file: etc/logrotate.d/nginx, key: rotate, block: /var/log/nginx/*.log}}
  nofile:   {etc: {file: etc/security/limits.conf, key: hard nofile}}
```

Each dialect reads a line as its tool does:

- `sshd` and `ssh`: the keyword runs to white space or `=`, an `=` may stand between keyword and
  arguments, and a `#` that starts a word outside double or single quotes ends the line; a `\`
  escapes a quote, a `\` and, outside quotes, a space after it, and is kept before anything else. Keywords match in
  any case. `Match`, and for `ssh` `Host`, opens a block that runs to the next one or to the end of
  its file. The value is the arguments as written, quotes kept.
- `sysctl`: a line that starts with `#` or `;` is a comment, the key runs to the `=`, a `-` before
  it is dropped, and white space around key and value is taken off. A key written with `/`, such
  as `net/ipv4/ip_forward`, is the key with `.`, in the file and in `key` alike. A key with a glob,
  such as `net.ipv4.conf.*.rp_filter`, is matched as written, never against the keys it covers.
- `resolv`: a line that starts with `;` or `#` is a comment, and a keyword counts only at the start
  of a line with white space after it. `key` is one of `nameserver`, `domain`, `search`,
  `options` and `sortlist`. A nameserver is its first word, cut at `;` or `#` too; a domain is its
  first word; the others are the rest of the line.
- `limits`: a `#` ends the line, and a line of four words or more is a domain, a type, an item and
  a value, the rest of the line. The type, the item and the value are read in lower case, as
  pam_limits lowers them, and so is `key`. `key` is an item, such as `nofile`, or a type and an
  item, such as `hard nofile`, which takes a line of type `-` too, since it sets both. Each value's
  condition is its domain and type.
- `logrotate`: a line that ends with `{` opens a block for the paths before it, `}` closes it, and
  the lines of a script, from `prerotate`, `postrotate`, `firstaction`, `lastaction` or
  `preremove` to `endscript`, are passed over. A directive with no argument, such as `compress`,
  has the empty value. A block whose `{` is not on the line of its paths is not read.
- `nsswitch`: a `#` ends the line, and a database's name runs to the `:`.
- `modprobe`: a `\` at the end of a line joins the next to it, with the value at the line where
  the directive starts. `key` is one of `alias`, `blacklist`, `mask`, `options`, `install`,
  `remove`, `softdep` and `weakdep`; the value of `blacklist` and `mask` is the module. `options`, `install`, `remove`, `softdep` and `weakdep` put their value
  under a block, the module they name, with each `-` read as `_` as kmod reads it.
- `chrony`: a line that starts with `#`, `%`, `!` or `;` is a comment, a run of white space in a
  value is one space, and keywords match in any case.
- `ntp`: a `#` ends the line.

Lines end at `\n`, `\r\n` and a lone `\r` in every dialect, as the [XML values](#xml-values) says every
kind numbers them, though the C tools end a line at `\n` alone.

#### Drop-in directories

`sysctl`, `limits` and `modprobe` take `files:` for a directory of drop-ins, a list or a glob
matched against the files git tracks, as [A set of files](#a-set-of-files) says, and read the files
in the tool's order. Of a directory whose name ends in `.d`, only the `.conf` files whose name does
not start with a `.` are read, as the tools read them. The drop-ins are read by base name, and of two files of one name only the one
in the directory the tool ranks first: the directory above the drop-in directory (above `security`
for pam_limits) ending in `etc`, then `run`, `usr/local/lib`, `usr/lib`, `lib` and `usr/etc`, so
`etc/sysctl.d/50-x.conf` masks `usr/lib/sysctl.d/50-x.conf` whatever the order `files:` lists
them. Of two files of one name in directories the tools do not rank, such as `a/sysctl.d` and
`b/sysctl.d`, the first listed masks the other. sysctl reads `sysctl.conf` after the drop-ins, and
pam_limits `limits.conf` before them. A later value overrides an earlier one for sysctl and pam_limits, so the last value is
the one the host applies; errata returns every one, so an `agree` rule sees the files disagree,
and an `assert` can name the last. The other dialects read one `file:`, and refuse `files:`.

#### Includes

A file's includes are read in place, before the lines after them: `Include` in `sshd` and `ssh`,
each word a file or a glob; `include` in `logrotate`, a file or every file of a directory but the
editor and package manager leftovers logrotate passes over, such as `.dpkg-old` and `~`;
`include` in `chrony`, a glob, and `confdir` and `sourcedir`, the `.conf` and `.sources` files of
their directories by name across them, the first directory listed masking the others; and
`includefile` in `ntp`. A glob is matched against the files git tracks as glob(3) matches it and
read in name order, as glob(3) sorts: a `*` or a `?` matches no `.` at the start of a name, and
`**` is `*`. logrotate reads an included directory with readdir, not a glob, so its files whose
name starts with a `.` are read too, and none of them is a leftover logrotate passes over. A file
named outright that is not there is passed over, as the tools pass over it,
but for chrony, which refuses to start on it. chronyd reads a `sourcedir`'s files after the whole
configuration, where errata reads them in place, so the order of `server` and `pool` values can
differ from chronyd's; no value is left out. Values an included file sets in an `sshd` or
`ssh` block are under that block too, and a block an included file opens ends with that file.

The file `file:` names stands for the tool's main file, and its directory for the tool's home:
`/etc/ssh` for `sshd` and for `ssh`, but `~/.ssh` for a user's `ssh` config (a file named `config`
in a directory named `.ssh`), and `/etc` for `logrotate` and `ntp`. A relative path, and one under
the home, is read below that directory, so Debian's `Include /etc/ssh/sshd_config.d/*.conf` in
`roles/ssh/files/sshd_config` reads `roles/ssh/files/sshd_config.d/*.conf`, and
`Include ~/.ssh/config.d/*` in `home/.ssh/config` reads `home/.ssh/config.d/*`. chrony keeps its
file in two places: a `chrony.conf` in a directory named `chrony` stands for Debian's
`/etc/chrony/chrony.conf`, and the directory above it for `/etc`; any other `chrony.conf` stands
for Fedora's `/etc/chrony.conf`, and the `chrony` directory beside it for `/etc/chrony`. A role
may keep Debian's chrony.conf in `files/` with its `conf.d` beside it, so when a path under
`/etc/chrony` (or, for a file in `chrony`, under `/etc`) matches nothing where errata reads it but
matches files below the directory of chrony.conf itself, the rule is unknown and the message names
both. A directory named `etc` is the host's `/etc`, so this check is left out when chrony.conf's
directory is named `etc`, or, for a file in `chrony`, the directory above it. Drop-ins kept in
another directory of a role are not looked for: a `templates/chrony.conf.j2` reads its `conf.d`
below `templates/`, never in the role's `files/`. An include of any other path, one from `~` but a
user's `~/.ssh`, one that climbs out of the repository and one a template computes makes the rule
unknown, since what it sets is not in the repository: `Include /usr/local/etc/ssh/extra.conf`, or `sourcedir
/run/chrony-dhcp` in Debian's chrony.conf. A `sourcedir` holds only sources, so it does so only
for `key: server`, `pool` or `peer`. Includes nest at most 16 deep, OpenSSH's limit, so a file
that includes itself is refused.

#### Blocks and conditions

A value under a block carries the block as its condition, which a report names after its place:
an `sshd` or `ssh` `Match` or `Host` line, with its words joined by one space and the keyword
written `Match` or `Host`, and for an included block the outer and inner lines joined by `; `;
a logrotate block's paths; a limits domain and type; a modprobe module. `block:` keeps only the
values of one block, compared in that form: `block: Match User git`, `block: /var/log/nginx/*.log`,
`block: '@admin'` or `block: snd-hda-intel`. The words after `Match` and `Host` are compared as
written, so `Match user git` is another block than `Match User git`. `block: ""` keeps the values
outside any block.

#### Match: the values OpenSSH applies

Without `match:`, `sshd` and `ssh` return every value of the keyword, each with its block, and the
file decides between them for each connection. `match:` names one connection, as Match criteria
and their values, and keeps the values OpenSSH applies to it: for a keyword that takes one value
the first it obtains, and for one that takes several every one. ssh reads its file in order, and a
value outside any block always applies. sshd applies the values of the Match blocks the connection
matches over those outside any block, so it takes those first, and the others only when no Match
block it matches sets the keyword.

| Dialect | Criteria `match:` gives | Keywords of several values |
|---|---|---|
| `sshd` | `user`, `group` (the user's groups, split at commas), `host`, `address`, `localaddress`, `localport`, `rdomain` | `Port`, `ListenAddress`, `HostKey`, `HostCertificate`, `AcceptEnv`, `Subsystem`, `AllowUsers`, `DenyUsers`, `AllowGroups`, `DenyGroups` |
| `ssh` | `host`, `originalhost`, `user`, `localuser`, `tagged` | `IdentityFile`, `CertificateFile`, `LocalForward`, `RemoteForward`, `DynamicForward`, `SendEnv` |

Patterns match as OpenSSH matches them: `*` and `?`, lists split at commas with `!` to negate an
entry, a host in any case, and for an address a network such as `10.0.0.0/8` too. A `Host` line
holds when the host matches one of its patterns and no negated one; `ssh` also takes `!` before a
Match criterion. A criterion may be written `User=git` as well as `User git`. `match: {host: ...}`
is the name the `Host` lines and `Match host` see; errata does not apply canonicalization. ssh
matches `Match user`, `Match host` and `Match tagged` against the `User`, `Hostname` and `Tag` a
line before the Match line sets, rather than the connection's own: when such a line may apply to
the connection, that criterion is one errata cannot decide.

What a block holds may depend on the connecting client or the host in ways the file does not
show. When a block errata cannot decide sets the keyword before the value errata would return, or
anywhere for a keyword of several values, the rule is unknown, and the message names the block and
why: a criterion `match:` does not give; `Match exec`, which runs a command errata never runs;
`Match canonical` and `Match final`, which hold on a pass of ssh's reading errata does not model;
sshd's `Match invalid-user`, which holds for a user the host does not know; `localnetwork`,
`version`, `sessiontype`, `command` and any other criterion errata does not read; a `user`, `host`
or `tagged` a line before shadows, as above; and an address list with a network sshd refuses.
sshd keeps the first `Subsystem` of each name and lets a Match block's replace the global one of
its name; errata returns every `Subsystem` line that applies, so an `assert` names the one it
means. A value is the file's, not the host's, wherever
it depends on what the host adds: the user's groups, the networks it is on, the drop-ins a
package installs outside the repository.

#### Values etc marks

`sshd` and `ssh` expand some values before they use them: a `%` token other than `%%`, such as
`%h` or `%u`, a `${VAR}` variable, and a `~` for the home directory at the start. errata marks
such a value not literal, as [Values a file only computes](#values-a-file-only-computes) says. A
template file is read as the file it renders, as [`ini`](#ini) says, so `sshd_config.j2` is read in
the `sshd` dialect, and a value with the engine's marks, or inside one of its blocks, is not
literal. Every other value is the text the tool reads, which errata does not check: a `Port` of
`x` is returned as written.

### `systemd`

`systemd` returns the values of a key in a section of a systemd unit, read as systemd 257
reads it, with the drop-ins the repository holds for the unit applied in systemd's order. With
`variable`, `key` is `Environment`, and the values are those the unit's assignments give the
variable. A Podman Quadlet file (`.container`, `.volume`, `.network`, `.kube`, `.image`,
`.pod`, `.build`) is read with Podman 5.4.0's grammar, which differs in the ways
[Quadlet files](#quadlet-files) lists below.

```yaml
extract:
  port:    {systemd: {file: deploy/api.service, section: Service, key: Environment, variable: PORT}}
  command: {systemd: {file: deploy/api.service, section: Service, key: ExecStart}}
  image:   {systemd: {file: deploy/web.container, section: Container, key: Image}}
  every:   {systemd: {file: deploy/backup.timer, section: Timer, key: OnCalendar}}
  vars:    {systemd: {file: deploy/api.vars, variable: PORT}}
```

The file's name says what it is: a unit of a type systemd reads (`.service`, `.socket`,
`.device`, `.mount`, `.automount`, `.swap`, `.target`, `.path`, `.timer`, `.slice`, `.scope`),
a Quadlet file, or a drop-in, a `.conf` file in a directory such as `api.service.d` or
`service.d`. A `.network` file is Quadlet's, but one directly in a directory named `network`, as
in `/etc/systemd/network`, and not below `containers/systemd`, is systemd-networkd's, read with
systemd's grammar: networkd reads no subdirectory, and Quadlet reads every directory below its
own. A unit and a Quadlet file need `section` and `key`. Any other file is an
environment file, as `EnvironmentFile=` names one, and takes `variable` alone. A template file
is read as the file it renders, as [`ini`](#ini) says.

The unit file is read as systemd's own parser reads it:

- A line ends at `\n`, `\r` or NUL, and so does a run of them in which none repeats, so `\r\n`
  and `\n\r` each end one line. errata numbers lines as it numbers every file's, so a value
  after `\n\r` is one line further down than systemd counts it.
- A line whose first character other than a space or a tab is `#` or `;` is a comment, also
  between the lines of a value a `\` continues. A `#` later in a line is part of the value.
- A `\` at the end of a line continues the value on the next, and stands as a space. An empty
  line ends it. The value is at the line its key is on.
- `key=value` splits at the first `=`, and spaces and tabs around both go. A line with no `=`,
  one with nothing before it, and a key before the first section header are ignored, as systemd
  ignores them with a warning.
- A key set more than once gives every value, in order. A key set to nothing drops the values it
  had before in its section, in the unit or a drop-in before, as systemd empties a list such as
  `ExecStart=` or `Environment=`. For a key that holds one value, systemd uses the last.
- Section and key names are compared as written, so case matters. errata returns the keys of
  any section, where systemd ignores a section the unit's type does not read.
- A section header that does not end in `]`, or whose name holds a quote, a `\` or a control
  character, a line that is not UTF-8, and a line of 1 MiB or more, joined lines too, make the
  rule unknown, as systemd refuses to load the unit. systemd's UTF-8 is stricter than most: it
  refuses the noncharacters U+FDD0 to U+FDEF and U+FFFE and U+FFFF of every plane, and errata
  refuses them with it, in a unit, in an environment file and in a `\U` escape.

The drop-ins are the `.conf` files of the unit's drop-in directories beside it, found in git's
index as a glob's files are ([A set of files](#a-set-of-files)), so the repository root must be
in a git repository. For `foo-bar@x.service` the directories are, in order, `foo-bar@x.service.d`,
`foo-bar@.service.d`, `foo-.service.d`, `foo-@x.service.d`, `foo-@.service.d` and
`service.d`: the unit's own, its template's, those of each prefix of the name up to a dash, from
the longest, each with the template's after it, and the type's. Their files
are applied in the order of their names, byte by byte, and a name in two directories is read
from the first. A name that starts with `.` and a file that does not end in `.conf` are not read.
A drop-in named as `file` is read alone. `max_bytes` is what the unit and its drop-ins hold in
all. errata finds drop-ins only beside the unit: systemd also reads `/etc`, `/run` and
`/usr/lib`, which a repository does not hold. A unit name of 256 bytes or more makes the rule
unknown, as systemd refuses it. A drop-in masked as systemd masks one, a symlink to
`/dev/null`, is a link that leads out of the repository, and makes the rule unknown as any such
link does; an empty drop-in reads as one that sets nothing.

A value systemd changes before it uses it is returned as written and marked not literal (see
[Values a file only computes](#values-a-file-only-computes)): a value with a specifier, such as
`%i`, `%h` or `%%`, and a value of a key that starts with `Exec` that holds a `$`, since systemd
expands `$PORT` and `${PORT}` from the environment.

`variable` splits each `Environment=` of the section into assignments as systemd does: words
are separated by spaces, a `"` or `'` quotes up to the next of the same, and `\` reads one C
escape such as `\n`, `\x41` or `\u00e9`. A word that is not `NAME=value` with a name of letters,
digits and `_` is ignored. A word systemd cannot read, such as one with an open quote or `\q`,
ends the line: the words before it count, those after it do not. A word with a specifier is not
literal. In a unit systemd runs, each `EnvironmentFile=` of the section that no reset dropped
makes every value of the variable not literal: systemd reads that file after `Environment=`, and
errata does not read a file outside the repository, even one a `-` marks as optional, since it
overrides the unit when it is there. A Quadlet `[Container]` is the other way round, as
[Quadlet files](#quadlet-files) says. `UnsetEnvironment=` of the section, which systemd applies
once every other environment is merged and the last assignment of a name has won, drops every
value of the variable when a word is its name, or its name, `=` and the value it ends with. When
an `EnvironmentFile=` is left, which may set another value, that second word makes the values
not literal instead. A reset of `UnsetEnvironment=` drops the words before it. A `%` in a word's
name, or in the value of a word that names the variable, may expand to a word that unsets it, so
it makes the values not literal; a specifier in the value of a word that names another variable
leaves them literal.

| Unit says | `variable: PORT` gives |
|---|---|
| `Environment=PORT=8080 HOST=a` | `8080` |
| `Environment="PORT=80 81"` | `80 81` |
| `Environment=PORT=8080`, then `Environment=` in a drop-in, then `Environment=PORT=9090` | `9090` |
| `Environment=PORT=%i` | `%i`, not literal |
| `Environment=PORT=8080` and `EnvironmentFile=-/etc/api.env` | `8080`, not literal |
| `Environment=PORT=8080` and `UnsetEnvironment=PORT` | no value |
| `Environment=PORT=1 PORT=2` and `UnsetEnvironment=PORT=2` | no value |
| `Environment=PORT=1 PORT=2` and `UnsetEnvironment=PORT=1` | `1` and `2`: PORT ends as 2 |
| `Environment=PORT=1`, `EnvironmentFile=/etc/a.env` and `UnsetEnvironment=PORT=1` | `1`, not literal |
| `Environment=PORT=1` and `UnsetEnvironment=OTHER=%i` | `1` |

An environment file is read as `EnvironmentFile=` reads one: `#` or `;` starts a comment line,
the value is unquoted, `'...'` or `"..."`, systemd substitutes nothing, and a name with `.` or `-`,
or `export A=1`, is ignored. [`envfile`](#envfile) lists where this differs from compose. A key or
a value that is not UTF-8 makes the rule unknown, as systemd refuses the file.

`Image=` of a Quadlet file gives the image Quadlet uses, one value: the last `Image=`, as
Podman's `Lookup` takes it, with the white space at its end and `"` at either end trimmed. One
that names a `.image` file beside the unit, such as `Image=web.image`, gives that file's
`ImageTag=` when it sets one that is not empty, and its `Image=` otherwise, each the last and
trimmed the same way; one that names a `.build` file gives that file's first `ImageTag=`, as
written. Each file is read with its drop-ins, once. A file that is not there, a `.image` with no
`Image=`, a `.build` with no `ImageTag=`, and a name with a `/`, which Quadlet does not find,
make the rule unknown. Any other `Image=` is the reference, which a rule compares with the image
a chart or a compose file names. A timer's `OnCalendar=` is returned as
written; `calendar_interval` in an [`assert`](#assert) gives its interval.

#### Quadlet files

Podman 5.4.0 reads a Quadlet file and its drop-ins with its own parser (`UnitFile.Parse` in
pkg/systemd/parser/unitfile.go), and errata reads them as it does:

- A line ends at `\n` alone. A `\r` or a NUL is a byte of the line, and white space at both ends
  of a line goes, `\r` with it, as Go's `strings.TrimSpace` takes it.
- An empty line is a comment, so inside a value a `\` continues it does not end the value: the
  line after it is joined.
- A `\` at the end of a line continues the value with no space for it, even after another `\`.
  On the last line it only goes.
- A group (`[Container]`) may hold a quote or a `\`; an empty name, or one with `[` or a control
  character, makes the rule unknown, and so does text other than spaces and tabs after the `]`.
- A line that is no comment, group or `key=value`, and a key before the first group, make the
  rule unknown, as Quadlet refuses to generate a unit from the file.
- No line is too long, and no byte is refused for its UTF-8.

`Environment=` of `[Container]` and of a `.build`'s `[Build]` is split as Podman's
`LookupAllKeyVal` splits it, which differs from systemd: a quote never closed and a `\` at the
end are read; an escape writes a character, so `\xff` is `ÿ`; an empty word, such as `""`, ends
the value and the words after it are dropped; a name is not checked; and an escape Podman does not
read, such as `\q`, drops the whole line. A word with `%` or `$` is not literal, since the value
reaches the command line systemd expands. `[Service]` of a Quadlet file goes to the unit Quadlet
writes, so its `Environment=` is split as systemd splits it. Quadlet passes `[Container]`'s
`EnvironmentFile=` to `podman run` as `--env-file` and its `Environment=` as `--env`, and Podman
sets the file's variables first and `--env` over them, so a value `Environment=` sets stays
literal beside an `EnvironmentFile=`. `[Container]` has no `UnsetEnvironment=`, which Quadlet
refuses, so errata does not read one there. errata applies the drop-ins of a
Quadlet file in systemd's order, which differs from Quadlet's in two ways: Quadlet lists the
directory `foo-@.container.d` before `foo-.container.d` and has no `foo-@x.container.d`, and it
reads a drop-in whose name starts with `.`.

`systemd` is an env reader: [`env`](#env) reads `Environment=` of a `.service`, `.socket`,
`.mount`, `.swap` or Quadlet `.container` with it, and an environment file `readers:` gives it.
errata does not read: a unit in `/etc` or `/usr/lib` that the unit file overrides or is
overridden by, an alias's drop-ins, `PassEnvironment=` and the manager's own environment, which
values systemd's specifiers expand to, and which keys a section of a unit type takes.

### `env`

`env` reads one variable in every file a rule lists, so one rule says that every place that sets
it agrees. The rule below holds when the env file, the deploy script, every job of the GitLab CI
file, every job and step of each workflow and every stage of the Jenkinsfile that set `API_PORT`
give one port:

```yaml
checks:
  - id: api-port-agrees
    reason: Every place that sets API_PORT names the port the server listens on.
    scope: repo
    extract:
      port:
        env:
          name: API_PORT
          files: [.env, deploy.sh, .gitlab-ci.yml, .github/workflows/*.yml, Jenkinsfile]
    agree: [port]
```

When the deploy job sets 8081 and a smoke test step 9090, the erratum names each place with the
reader that read it and the job, step or stage the value holds for:

```text
FALSE api-port-agrees (repo)
  why:  Every place that sets API_PORT names the port the server listens on.
  said: agree: the values differ
          "8080" at .env:1 from envfile (port), deploy.sh:2 from shell (port), .gitlab-ci.yml:2 from gitlab-ci job "test" (port), .github/workflows/ci.yml:3 from github-actions job "test" (port), Jenkinsfile:3 from jenkins pipeline (port)
          "8081" at .gitlab-ci.yml:6 from gitlab-ci job "deploy" (port)
          "9090" at .github/workflows/ci.yml:10 from github-actions job "test" step "smoke" (port)
```

A file's name chooses its reader:

| Name | Reader | What it returns |
|---|---|---|
| `.env`, any name that starts with `.env` but `.envrc`, and any name that ends in `.env`, such as `prod.env`; not a code file the [`literal`](#literal) reader reads, such as `.env.d.ts` or `.env.py` | `envfile` | every value of the key ([`envfile`](#envfile)) |
| a name that ends in `.sh`, and `.envrc`, direnv's script | `shell` | every value the script gives the variable ([`shell`](#shell)) |
| `.gitlab-ci.yml`, with its local includes | `gitlab-ci` | the value each job sees, as `job:` would name it ([`gitlab-ci`](#gitlab-ci)) |
| a `.yml` or `.yaml` file directly in `.github/workflows` | `github-actions` | the value each job's steps see from the workflow's and the job's `env:`, and the value of each step whose own `env:` sets the variable ([`github-actions`](#github-actions)) |
| `Jenkinsfile` | `jenkins` | the value the pipeline's top sees, and the value of each stage whose `environment` sets the variable ([`jenkins`](#jenkins)) |
| a unit ending in `.service`, `.socket`, `.mount` or `.swap`, and a Quadlet `.container` | `systemd` | each value `Environment=` gives the variable in the unit's `[Service]`, `[Socket]`, `[Mount]` or `[Swap]`, or the Quadlet's `[Container]`, with the unit's drop-ins applied ([`systemd`](#systemd)) |

A file whose name chooses none is read as a shell script when its first line is a `#!` that names a
shell errata reads, such as `bin/deploy` with `#!/usr/bin/env bash`. Any other file makes the rule
unknown, naming it, until `readers:` gives it a reader by its entry in `files`. `systemd` reads
such a file as `EnvironmentFile=` reads one:

```yaml
env:
  name: API_PORT
  files: [ci/build.yml, config/app.env, deploy/api.vars]
  readers: {ci/build.yml: gitlab-ci, config/app.env: envfile, deploy/api.vars: systemd}
```

An entry with `*`, `?` or `[` is a glob. errata matches it against the files git tracks, read from
`.git/index` without running git, a path segment at a time as Go's `path.Match` does: `*` never
crosses a `/`, and a segment `**` matches any number of segments, as in
[A set of files](#a-set-of-files). A file git does not track is not read, nor is a submodule. A glob
that would match a file a sparse checkout leaves out, or a path below a directory it leaves out, makes
the rule unknown, naming the sparse checkout, and so does a file named outright that it leaves out. A glob that matches no file adds no value, so a rule whose `env` finds none is unknown
unless `allow_empty` lists it. A glob needs a git repository to hold the repository root, at its
top or below it ([A root below the top](#a-root-below-the-top)). A
worktree's or a submodule's `.git` file is followed to the git directory it names, and one that
names a directory that is not there, as when a worktree is mounted alone into a container, or one
that is there but is no git directory as git tests it (a `HEAD` file or a symlink into `refs/`, and
`objects` and `refs` directories, in the `commondir` of a worktree), such as an empty mount point,
makes the rule unknown rather than match nothing. So does a `.git` file with more than its `gitdir:`
line. So do a split index (`git update-index --split-index`) and an index that is not a regular
file, a device among them, which is refused before it is opened. On Windows so does an index
with an entry Windows would open as another file than the one git tracks: a path that holds a
backslash, which git writes as part of a name and Windows reads as a separator, so that
`.errata\witness\w\x.yaml` would name a witness's file; a name such as `ERRATA~1`, which
Windows may open as `.errata` by its short name; a colon; or a name Windows keeps for a device,
such as `aux`. git for Windows writes none of them, but an index written on another OS
can hold them. An entry without a glob names one
file, which must exist. A file that two entries name is read once, by the reader one of them gives
it under `readers:`, whichever entry comes first; two readers for one file make the rule unknown,
naming both entries.

Each reader reads its file as its own extractor does and refuses what it refuses, so a value that is
unknown for one job makes the whole rule unknown, and a value a file computes is not literal
([Values a file only computes](#values-a-file-only-computes)): the rule above is unknown when the
Jenkinsfile writes `API_PORT = "${PORT_BASE}1"`, unless `skip_not_literal` lists `port`. A job,
step or stage that sees no value from the files adds none. Some places are no job: a GitLab CI
hidden job (`.base`) counts only through the jobs that extend it, a trigger job gives the values it
passes downstream unless its `trigger:forward:yaml_variables` is `false`, and a GitHub Actions job
that calls a reusable workflow gives none, since neither its `env:` nor the workflow's reaches the
called workflow's steps. A step's own `env:` wins over a `GITHUB_ENV` write before it, as the runner
merges it over the job's environment, so its value is read. A step without one, after a step that
may write `GITHUB_ENV` (the steps `github-actions` with `step:` refuses for), sees what the write
left, so the rule is unknown, whether or not the job sets the variable itself. A Jenkins stage whose
name is not literal and whose environment sets the variable is unknown, since errata cannot name
it.

The name must be a letter or `_` and then letters, digits and `_`, which every reader reads, and
each reader a listed file's name chooses checks it as the rule loads: `GITHUB_TOKEN` does not load
for a workflow. `max_bytes` is the most bytes the listed files hold in all, a GitLab CI file's
includes too, each read to what the files before it left. `files` lists at most 64 entries, and
one extractor reads at most 10000 files, empty ones too, since each costs an open whatever it
holds (see [Limits](#limits)). Dockerfile
`ENV` and `ARG`, compose `environment:` and Kubernetes `env:` entries join the readers in later
releases.

### `exists` and `size`

`exists` gives one value, `true` or `false`, so a rule can say that a file stays removed, or that
one is there:

```yaml
checks:
  - id: secret-generators-stay-removed
    reason: >
      The key files are gitignored, so with the generators Argo cannot render the app. A re-vendor
      of upstream restores them.
    scope: repo
    extract:
      app: {exists: {file: infra/argocd/applications/wazuh.yaml}}
      generator: {regex: {file: infra/wazuh/upstream/kustomization.yml, pattern: '^(secretGenerator):'}}
    allow_empty: [generator]
    assert: (app[0] == 'true') == (size(generator) == 0)
```

A missing file is the value `false`, not an unknown. Every other extractor is unknown for a file
that is not there, because it was asked for a value inside the file; `exists` was asked whether
the file is there, and "no" is that answer
([ADR 0006](adr/0006-a-missing-file-is-a-value-for-exists.md)). What `exists` cannot decide
still makes the rule unknown.

| At the name | `exists` | `size` |
|---|---|---|
| a regular file, readable or not | `true` | its size |
| nothing, or a name below a regular file, such as `f/x` | `false` | unknown |
| a directory | unknown | unknown |
| a symlink that ends at a file inside the repository | `true` | the size of the file it ends at |
| a symlink that ends at a directory | unknown | unknown |
| a symlink that dangles inside the repository | `false` | unknown |
| a symlink that leads outside the repository, whether or not its target exists | unknown | unknown |
| a loop of symlinks, a FIFO, a socket or a device | unknown | unknown |
| a name below a directory errata cannot open, to read it and search it | unknown | unknown |
| a root that is not there, or is no directory | unknown | unknown |
| nothing, where a sparse checkout leaves the file out, or a directory above it | unknown | unknown |
| nothing, where git's index, which says whether a sparse checkout leaves the file out, cannot be read | unknown | unknown |

`file` is read as for any extractor: relative to the root, its `..` resolved by name before any
symlink, and its symlinks followed as the operating system follows them, with the same refusal of
a link that leads out. errata looks at nothing outside the repository, so a link out is unknown
even when its target is missing. Both kinds look at the file with `lstat` once its links are
followed, and open no file, so a FIFO does not hang the run and a device is not acted on. The
`lstat` goes through Go's `os.Root`, which opens each directory on the way, so a file below a
directory errata may search but not read (mode 0111) is unknown. A directory at the name is
unknown: a rule that a file is there is about a file, and a directory is no file, while a rule
that a file stays removed is about what reads that name, and kustomize, for one, reads a
directory in a file's place. A file is what the working tree holds, tracked by git or not, as for
every extractor. On a file system that ignores case, as macOS and Windows set theirs up by
default, `exists: {file: Values.yaml}` is `true` when `values.yaml` is there, where a Linux CI
job finds nothing; every reader reads such a name the same way.

`size` is the byte count `lstat` gives once the links are followed, as `wc -c` counts it. It has
no limit, since nothing is read, and so takes no `max_bytes`. `size(bytes(...))` over a regex
capture of the whole file counts differently: the regex reads a CRLF line end as LF, so a file
of CRLF lines counts one byte fewer for each line, and a file over `max_bytes` is unknown.
Compare the value as a number with `int()`:

```yaml
    extract:
      notes: {size: {file: CLAUDE.md}}
    assert: int(notes[0]) <= 7000
```

Each value is at the file as `file` wrote it, at no line, so a message names the file alone, and
a SARIF result has no region. A SARIF result that blames an `exists` value of `false` is at the
check in the config, since code scanning cannot show a file that is not there; its message names
the path, and so does a related location that holds such a value, which places nothing.

A `file` with `*`, `?` or `[` does not load. A `files:` entry with one is a glob, and `exists`
would look for a file of that very name, find none, and say `false`, so a rule that files stay
removed would hold whatever the glob matched. `exists` and `size` take no `files:`. A file whose
name holds one of these characters, such as `app/[slug]/page.tsx`, cannot be named yet.

## Limits

A file in a pull request can be large or hostile, and an extractor must not stall or kill the run
over it. errata enforces the limits below on what it reads: the file size, the nodes a file can
hold, the nodes YAML aliases add, the values, the depth, a pattern's capture groups, the regex time,
the work of a path's filters and an XML path's terms, what a Markdown parse may build and how long
it may run, the tokens of HTML and how deep its tables nest, the work of an INI, properties or env file read, how deep a shell script or a
Jenkinsfile nests, the
lines and tokens of a go.mod, the tokens of a Go source and the work of evaluating its constants, and the entries and depth of a walk of the repository. It does not
bound the memory a read takes. Peak memory depends on the file's shape within those limits, and the
figures below are the densest shapes measured so far, as examples, not as a ceiling; a file that
needs more memory than the runner has kills the run, which fails it rather than passing it. Every
pass errata makes over a file takes time in proportion to its size, but for the regex search, the
Markdown parse and a path's filters, so the size cap bounds the cost of a read, and the other limits
bound what a small file can make of itself. A regex search costs the size of the file times the
threads its pattern keeps alive, which a counted repeat such as `{0,1000}` makes many, so the regex
time limit bounds it. Each limit fails closed: a file over it makes the rule unknown, with a message
that names the limit, the file and what to do. Times and memory below were measured on a loaded 4
core machine.

| Limit | Value | Why |
|---|---|---|
| the config file | 1 MiB (1048576 bytes) | checked while reading, so a pipe given with `--config` is held to it too. YAML aliases may expand it to at most 4 MiB, counted as the bytes of every scalar and one for each node before anything is expanded: one scalar of 64 KiB aliased by 8000 checks, 528 KB of config, ran the loader out of memory, since the YAML library's own guard counts aliased nodes, not their bytes. Unlike the limits below, a config over it does not load, and errata exits 2. The largest real config known, a GitOps repository's, is 37 KB for 72 checks, 28 times under the limit. Loading costs memory in proportion to the config: the densest shape measured, one rule of thousands of regex extractors, allocates about 630 bytes for each byte of config |
| file size | 32 MiB, or `max_bytes` | checked before reading. An ordinary file costs about 27 times its size in memory: an 8.2 MB OpenAPI document takes 0.95 s and 223 MB |
| nodes | `max_bytes` divided by 7 (YAML), 3 (TOML, `literal` and `code`), 8 (JSON) or 3 (XML) | checked before parsing, since the YAML and TOML parsers build a whole document or expression before errata sees it. errata counts in one pass an upper bound on the nodes the parser can build: two for each byte after which a key, a value or an item can start, with YAML's NEL, LS and PS line breaks among them, and a UTF-16 YAML file counted as the UTF-8 the parser reads. A file of one-character items counts one for each byte, an ordinary file one for every 3 to 16: a 6.3 MB Helm index has 6 times room at the default, a 7.9 MB uv.lock 4.8 times, a 7.4 MB Google API document 8 times. The limit bounds nodes, not memory, since a node costs more or less by its shape. The densest files measured so far at the default peak at 2.0 GB in YAML (mappings of 62 anchored, tagged keys), 2.55 GB in TOML (inline tables of dotted keys of 500 parts) and 0.42 GB in JSON (objects nested 900 deep), and grow in proportion to `max_bytes`; 32 MiB of one-character items ran out of 4.7 GB in YAML without the limit. Raise `max_bytes` to read a denser file. For XML errata counts each element, attribute, namespace declaration and text node as it builds them from encoding/xml's tokens, since it builds the tree itself. The densest of 39 real XML files, MARC library records, holds a node for every 11.5 bytes, almost 4 times the room at the default. The densest file XML can write, text between empty elements (`x<a/>`), reaches the limit at 28 MB, which takes 2.3 to 2.4 s and 0.9 to 1.1 GB; 32 MiB of empty elements takes 2.7 to 2.8 s and 1.3 to 1.6 GB. An attribute costs more memory than an element, since encoding/xml builds a whole start tag before errata counts it: the densest XML measured so far, one start tag of 8.4 million attributes in 32 MiB, takes 2.8 to 2.9 s and peaks at 1.9 to 2.1 GB before it is refused. Each XML figure is the range of five runs of `errata check` on the build node (4 cores, 4 GiB), timed whole and with the process's peak resident memory |
| values per extractor | 100000 | each node of a mapping that `as: map` hands over counts as one, the mapping itself included, and so does each record and each node of its fields. Every value is kept in memory and printed; a regex `(\d*)` matches at every byte. A path that selects more nodes at any step stops there. The values of one extractor also hold at most `max_bytes` bytes in all, since YAML aliases can repeat a value, decoding can lengthen one, and an XML element's value holds the text of every element inside it: `//a` over 1000 nested elements around 30 MB would return 30 GB, and is refused. Each reader counts as it reads, and the rule counts every extractor's values again, with their bytes, before it compares them, and refuses a value whose location names no path inside the repository; it reads the name as written, without following links, which the readers check when they open a file. It reads the name the same on every OS: a path that starts with `/` or `\`, whose first name once `.` and `..` are resolved is a drive such as `C:` (`C:x`, `./C:x`), or whose `..` leaves the root with `\` read as a separator or as part of a name, is outside, and a name Windows keeps for a device, such as `aux.yaml`, is inside. A `file:` or a `files:` entry that is outside this way is refused when the config loads. A path that holds a control character, such as a newline, which the text output would print as a line of its own, a direction control (U+061C, U+200E, U+200F, U+202A to U+202E, U+2066 to U+2069) or a line or paragraph separator (U+2028, U+2029), which a SARIF viewer would act on, is refused in a `file:` when the config loads, in a tracked file a glob matches before it is read, and in a value's location. 100000 regex matches in 4.3 MB take 0.8 s and 45 MB |
| documents in strings | `max_bytes` in all, and the nodes `max_bytes` allows | the documents an extractor's `embedded` steps parse out of strings count together, as one more file: their bytes against `max_bytes`, and the nodes each can hold, counted as for a file of its format at its bytes per node, against `max_bytes` too. A string YAML aliases repeat counts each time. The nodes YAML aliases add, the filter work and the values count over the file and its documents together, so a run reads at most twice `max_bytes` for one extractor. Over a set of files, the documents in the strings of every file count together against the set's `max_bytes`, as one more file of the set |
| depth | 1000 | each key, each part of a dotted TOML key or table header, and each element is one level, and an array table's element one more. The deepest of 56 real files measured nests 25 |
| YAML aliases and merge keys | 1000000 nodes, for every file of `files` together | each alias adds the nodes of its anchor, and so does each mapping a merge key names, counted for the whole file before anything is built from them. A key counts by its length too: each 64 bytes of a key that a merge key copies count as one node more, and an alias used as a key counts one node and one more for each 64 bytes of its anchor's text, so a file that merges or repeats long keys many times reaches the limit instead of taking minutes. errata keeps one copy of an anchor, so 5000 mappings that merge one of 99 keys, at the limit, take 0.15 s and 51 MB |
| GitLab CI resolution | 150 included files, 11 levels of `extends`, 10 of `!reference`, 200 `parallel:matrix` jobs, 1000000 steps | GitLab's own limits on includes (its default), extends, `!reference` and matrices, and a step for each key a merge, an extends or a `!reference` touches, the bound YAML aliases have, since nested `!reference` lists repeat what they select without the file growing. Each is checked as the configuration is built (see [`gitlab-ci`](#gitlab-ci)) |
| GitHub Actions matrix | 256 jobs, 65536 combinations before `exclude:`, 1000000 steps | GitHub's own limit on a matrix's jobs, and a step for each key an `include:` or `exclude:` entry compares and for each 64 bytes of a value it compares, the bound YAML aliases have, since each entry is matched against every combination (see [`github-actions`](#github-actions)) |
| `etc` includes and Match decisions | 16 nested includes, 10000 files, 100000 `User`, `Hostname` and `Tag` lines, and 128 units of work a byte | OpenSSH's own limit on nested includes, and core's on files and values. With `match:`, errata decides each block's line once, and each `User`, `Hostname` or `Tag` line once, in the order read, so a file of 8000 of those lines under Host blocks and 8000 Match lines of distinct users takes about eight times as long as one of 1000 (TestMatchShadowIsLinear); each block decided, and each pattern matched, is charged to the read's work budget, the one an INI file's read has (see [`etc`](#etc)) |
| regex time | 10 s for each 32 MiB of `max_bytes`, and never less than 10 s; for `files`, one limit the searches of every file share; for a pattern that references values, one limit that checking every value and its search share; a `context`'s searches share the limit of the pattern's | the search looks at the clock every 256 characters it reads, so one long search is stopped too. A pattern whose optional tail scans the rest of the line, such as `(\d)(?:.*x)?`, searches the line again after every match: 40 KB of one line took 27 s. One search that finds nothing costs the file times the pattern's width: `([0-9a-f]{40})` over 32 MiB of hex words took 28 s, and `([ab]{0,1000}c)` over 1 MiB 62 s; each is refused at 10 s. The slowest ordinary pattern measured over 32 MiB, `([A-Za-z0-9+/]{64,}={0,2})`, takes 4.7 s. A pattern that starts with a literal skips to each place the literal occurs and tries the pattern there. Where the next place is less than 64 bytes past the text the last try read, or a try reads more than 4 KiB, it searches every place from there on, as it does a pattern without a literal, so it reads at most 4 KiB more than a search of every place does. Over 32 MiB of errata's README, repeated, on the build node, `commit ([0-9a-f]{40})` takes 0.01 s, where a search of every place takes 0.82 s, and `(\d+\.\d+\.\d+)`, which starts with no literal, 1.2 s |
| a value a regex pattern references | 4096 bytes, and not empty | its pattern is compiled once for each file it searches, and up to twice more, and Go's size check does not stop a long literal: one value of 1 MiB took 1.4 s and 824 MiB of allocation, and one of 4 MiB 5.6 s and 3.8 GB. An empty value would let the pattern match its context alone. See [Values of another extractor](#values-of-another-extractor) |
| regex capture groups | 32 | a search tracks every group for every thread it keeps alive; 100000 matches of a pattern of 32 groups take 0.5 s and 29 MB |
| filter work | 4000000000 bytes for the filters of one path, over every file of `files` together | for each member tested, each comparison and each step after `@` counts 256, plus the bytes of each key it looks up and of the literal it compares, counted before the members are tested. A test that a path exists counts each step as it takes it, and a filter inside it counts its members as the outer one does, against the same total. At the limit, with 64 comparisons on each member, the worst shapes measured take 0.36 s to 1.42 s; on the build node, a test whose `[*]` steps fan out through shared nodes reaches the limit and is refused in 0.18 s |
| filter depth | 4 | parsing a filter and testing a member each recurse once for each filter nested inside another; membership takes 2 |
| XML path terms | 32 steps and predicates | each step reads at most every node of the file once, and each predicate the nodes its step keeps, so a path costs the file's nodes times its terms. 32 `//` steps over 8.4 million elements take 2.4 s after the parse. A comparison is counted against the filter work: 256 for each child or attribute it looks at, and the literal's length for each value of the same length it compares |
| Markdown parse time | 30 s for each 32 MiB of `max_bytes`, and never less than 30 s | goldmark is quadratic on some shapes and cannot be stopped from outside, so errata looks at the clock on every call goldmark makes into its parsers. 32 MiB of express's changelog, repeated, parsed in 15 s on a loaded 4 core machine and in 1.9 s on the build node. Of 42 hostile shapes at 32 MiB, each was read or refused at most 0.08 s after the limit on the build node. Three of them, lines of `*` and `_` runs, ran to the limit until the scan for closing runs below had a limit of its own. A fourth ran to the limit until that scan counted goldmark's walk of the paragraph's runs: a paragraph of runs followed by lines of emphasis inside words |
| Markdown nodes | `max_bytes` divided by 4 | counted as the parse builds them: three for each inline node, two for each line, one for each block and each emphasis. For the `markdown` extractor's table target, each paragraph that may hold a table counts, before the table is read, three for each cell its rows may make, a cell for each column of the delimiter row, and one for each row and three for the table. The aws-sdk-go-v2 changelog of 2.9 MB makes the most of 5962 real Markdown files, 321763, 4% of the limit at the default |
| Markdown table padding | as many empty cells as the table has bytes | a GitHub table gives each row a cell for each column of its delimiter row, so n columns over n rows of one byte made n squared cells: 9.6 KB made 2.56 million and allocated 512 MiB. errata counts the cells each row lacks, at least the columns less the row's pipes and one, and refuses the table past its bytes, as cmark-gfm bounds the cells it completes, so a table's cells are linear in its bytes |
| Markdown open blocks from line to line | `max_bytes` divided by 4 | goldmark records each block other than a paragraph that goes on to the next line and makes no node for it: 32 MiB of lines of 100 block quotes made 33 million and took 3.3 GB without the limit. The most in a real file is 56921 |
| pieces of the prose | `max_bytes` divided by 4 | a run of the file's bytes, or a space or newline errata writes. A run of blanks rewritten as one space starts two, so 32 MiB of a letter and a tab took 3.3 GB without the limit. The most in a real file is 92147 |
| Markdown nesting | 1000 levels | the text of a paragraph in 998 block quotes is at the limit |
| AsciiDoc pieces | `max_bytes` divided by 4 | a piece is a value, a run of lines of the text read, or a table cell, each 24 to 32 bytes, so a read holds about six times its limit at most, as the Markdown pieces do. The densest shape, lines of `//` read by a comment pattern, reaches the limit at 8388608 comments in 2.5 s with 1480 MiB allocated on a worker node (3 cores) at 32 MiB, and a paragraph of 32 MiB of `a` and `//` lines, which the limit lets through, takes 2.4 s and allocates 1645 MiB. Without a pattern each piece is a value, and the read stops at 100000 |
| AsciiDoc nesting | 1000 levels | delimited blocks inside one another, such as `====` and `****` in turn, each one level. 998 nested blocks around 32 MiB of text take 1.4 s and allocate 32 MiB on a worker node |
| HTML tokens | `max_bytes` divided by 4, and one more | a tag, a comment, a doctype or a run of text is one token, for the `html` extractor and for the HTML blocks of a Markdown file together. One tokenizer reads all of a Markdown file's HTML blocks; a block that ends inside a tag, a comment or a script's text needs a fresh one, with a buffer of 4096 bytes, which counts as 1024 tokens, and the tokenizer has read that comment or text past the block's end, to the end of the HTML at worst, so those bytes count too, one token for every 4. Without that count 500 blocks of `<div><!--` before 16 MiB of HTML took 56 s. The tokenizer reads each byte once; the bound is on what errata builds for each token. The densest of 347 real HTML files over 10 KB measured, 14 MB in all, has a token for every 6.4 bytes (RE2's syntax page). On builder, 32 MiB of `<b>xyzwv`, at the limit, read as one code block in 0.84 s with at most 745 MB of heap, and the Go spec repeated to 32 MiB in 0.25 s and 306 MB |
| HTML text pieces | `max_bytes` divided by 4 | as for the pieces of Markdown prose: 32 MiB of a letter and a tab under a heading is refused at the limit after 0.78 s and 1.04 GB of heap, the densest shape measured; cells of `<td>ab` reach the token limit at 0.94 GB |
| HTML nesting | 1000 elements open | errata follows the elements open in an HTML file, or in each HTML block of a Markdown file, with one entry for each (HTML structure). Every element open counts: a table nested in a cell opens four, `table`, `tbody`, `tr` and `td`, the `tbody` and `tr` implied when a page leaves them out, as a browser implies them, so tables nest 250 deep. A formatting element a block's end closed is reopened before each text that follows, as a browser reopens it, at most 3 of a name and 39 in all: 8 MiB of short paragraphs after a `<div>` that closed 36 of them scan in 0.59 s on builder, against 0.25 s with none to reopen, about 2.4 times as long, and the time still grows with the file's size alone. An end tag that a browser's adoption agency rebuilds around blocks counts each element and entry the rebuild moves as a token, so a page that rebuilds a deep stack end tag after end tag meets the token limit: 1 MiB of such end tags under 950 elements took 14.66 s on builder before the charge, against 34 ms for the same end tags with nothing open, and is now refused |
| runs of `*` and `_` in one paragraph | 5000 | goldmark matches each closing run against the runs open before it, so a paragraph costs up to the square of its runs: with goldmark 2.1.8 the slowest of ten shapes of 5000 measured, such as `****_a*a` 1663 times on one line, takes 0.062 s on the build node. From 256 bytes or more before the end of a line goldmark matches no emphasis at once, so there each emphasis is two runs. A CommonMark table, which errata reads as one paragraph, of 300-byte rows with `**x**` and `_y_` in the cells is refused at 1251 rows; a table of narrow rows, whose emphasis matches at once, is not. The most in a real paragraph is 286 |
| Markdown scan for closing runs | `max_bytes` times 2 | from each run of `*` or `_` that opens with fewer than 256 bytes left on its line, goldmark looks to the end of the line for a run to close it, so runs that close nothing cost up to 255 bytes each: without the limit, 32 MiB of lines of 85 of them scanned 330 MB, ten bytes for each of the file's, in 2.2 s on the build node. errata counts the rest of the line for each such run left open, and the bytes up to the closing run for each emphasis goldmark matches at once, which goldmark reads at most twice. From such a run that may also close, goldmark walks the runs of the paragraph before it, and errata counts a byte for each run so far, before every unmatched run that may close and every emphasis matched at once whose opening run follows a character goldmark does not read as a space (goldmark's spaces are tab, line feed, carriage return, space and the spaces above ASCII, not the vertical tab or the form feed): 4990 runs followed by lines of `a*b*c` took 3.99 s at 1 MiB and 16.6 s at 4 MiB on the build node, and ran to the time limit at 32 MiB, where the build node now refuses it in at most 0.39 s. Of 5341 real Markdown files (a Go module cache and npm packages, 42 MB, measured on 2026-10-25 and not kept), the most is 140541 bytes, 0.2% of the limit at the default, and the densest counts 0.99 for each of its bytes. goldmark up to 2.1.6 scanned from every run to the end of its line, so a line of runs cost the square of its length |
| link reference definitions | 10000000, the definitions at the start of a paragraph times its lines | goldmark copies the paragraph's lines once for each definition it takes out: 3162 definitions, at the limit, take 0.08 s. The most in a row in a real file is 343. Put blank lines between the definitions |
| a line of an HTML block | 1 MiB | goldmark scans such a line in calls errata cannot stop: a tag of 32 MiB took 0.59 s, and 0.02 s at the limit. The limit dates from goldmark v1, whose regular expressions took 11 s over the same line. The longest in a real file is 1801 bytes |
| prose | twice the size of the file | the regex time limit, which scales with the file, then holds for the prose too |
| ini and properties work | 128 units for each byte of the file and of the spec's names, and 1048576 more | each read makes one pass over the file and keeps only the values it selects, so it needs no node limit. A template's read makes two more, for its blocks, which keep a few words whatever the file holds: 32 MiB of the densest ERB blocks, `<%{%><%}%>` on each line, allocates 1072 bytes against 600 for the same bytes in a plain properties file, and reads in 3.4 s against 0.55 s on a worker of 2 cores, anon at most 110 MiB: the slowest shape that reads at 32 MiB. Every loop that reads a line, compares a name or matches an ansible host against a range charges the budget; a unit is 0.3 to 3.5 ns on the dense shapes measured, the ERB scan dearest, so a read that spends the whole budget at 32 MiB (about 4.3 billion units) takes 1.5 s to 15 s. Real files spend 2 to 8 units a byte. On the same worker tox, a key of each letter in each of a million sections, reads in 3.1 s. The worst shape found, lines of ten 17 digit ranges against a spec host of 1024 bytes, is refused at the budget in 5.9 s, where it would have run for about 350 s; a range bound of 4 million digits is refused in 0.1 s, and php fallbacks nested 131000 deep in under 0.01 s |
| envfile work | the budget of an ini or properties read | each statement costs a line's work, each pass of a substitution its bytes, and each byte it copies from a key 32 units, so a read copies at most a 32nd of its budget, 134 MB at 32 MiB, and a chain of keys that each repeat the one before is refused before its values fill memory. That bounds the bytes copied; the memory is about three times that, since a value grows by appends and the reader keeps a copy of each key's value: 32 MiB of `${A}` for a value of 4 KiB is refused in 0.45 s after 733 MB allocated, anon 395 MiB above its base on a 2 core worker. A word inside a word is read again at each level, so nesting costs the square of its depth and is refused for work long before 1000 levels, the depth limit. Real files spend 1.9 to 5.6 units a byte (20 files: compose-go's fixtures and five projects' `.env.example`). At 32 MiB on a worker node the slowest shape that reads takes 1.6 s on a 4 core worker and 3.1 s on a 2 core one (a key on each line). The most memory measured is 6.7 million distinct keys of four letters: the reader keeps every key's value for the substitutions after it, so it reads in 3.1 s after 801 MB allocated, anon 517 MiB above its base, on the 2 core worker, and the same as a template file such as `.env.j2`. Words nested 2.8 million deep are refused in 2.3 s |
| shell nesting | 20000 calls of the parser's stack, and syntax 20000 levels deep | mvdan.cc/sh recurses on nesting, and a stack overflow ends the process: 1 MiB of `"$("` passed Go's 1 GB limit. The parser reads through a check that counts its stack every 16 KiB, so it stops within 16 KiB of the limit; about 340 levels of parentheses in `(( ))` or 1800 of `$( )` reach it. A pipeline, a list or a sum is one level deeper for each command or term, and errata walks it by recursion: 32 MiB of `+1` passed Go's limit there. See [`shell`](#shell) for the times and memory |
| a shell statement | an eighth of `max_bytes`, 4 MiB at the default | the parser holds one statement's syntax whole while it reads it, up to 200 bytes of memory for each byte: 4 MiB of `a\|a\|a` took 0.87 GB and 8 MiB 1.7 GB, so 32 MiB would take about 6.6 GB; the limit refuses it with 0.88 GB, and eight groups of `a;a;a` at the limit read with 1.43 GB. Counted to the 1 KiB the parser reads at a time. The largest statement of eight real scripts measured is 149802 bytes |
| Jenkinsfile nesting | 1000 levels of blocks, and of strings and the `${ }` inside them | the lexer reads a string's `${ }` and the strings inside it on a stack of its own, and the scan keeps one entry for each block it is inside, so a file of nothing but `{` or `"${` is refused at the limit instead of growing either stack. Memory stays at about 150 MB for every shape measured at 32 MiB (see [`jenkins`](#jenkins)) |
| go.mod and go.work lines and tokens | `max_bytes` divided by 64 | `tool_version` and `dependency` check it before golang.org/x/mod/modfile parses the file, since it builds the syntax of the whole file first, at 150 to 440 bytes a line or token: without the limit, 32 MiB of lines of one short token took 2.1 GB and 4.1 s, and of commas on one line 1.9 GB. errata counts an upper bound in one pass: each line end, each `(`, `)`, `[`, `]`, `{`, `}` and comma, each quote and backquote that can start a string, and each start of a word. A go.mod of requirements holds one for every 7 bytes, so at the default the limit reads one of about 3.5 MB. The densest files measured at the limit take about 0.5 to 0.75 s and up to about 320 MB: 32 MiB of unknown directives of 129 bytes, or of lines ending in a comment, since modfile keeps an error for each line |
| Go source tokens | `max_bytes` divided by 8, 4194304 at the default | `code` counts a Go file's tokens with `go/scanner`, which builds nothing, before `go/parser` builds the syntax of the whole file and the reader its constants, up to about 175 bytes of memory for each token. Without the limit, 8 MiB of `const A = 1+1+...` lines took 447 MiB, so 32 MiB would take about 1.8 GB, past a 2 GiB runner. The densest shape measured at the limit, 400000 `const Ai float32 = P` lines and a constant that sums them, 15 MB, read in 2.2 s with 691 MiB anon on the build node, and 743 MiB on a slower worker node; 15 MB of `const A1 = 1` lines took 314 MiB. A real Go file has a token for every 4 to 6 bytes, so one of up to about 16 MiB reads at the default |
| Go constant work | 8 units for each token of the file, and 65536 more | each expression `code` evaluates costs a unit, and a number literal a unit for each 8 bytes. An operation on numbers costs a unit for each 64 bits of its operands, a rational's numerator and denominator together, and a multiplication, a division, and an addition, a subtraction or a comparison of rationals, which `go/constant` cross-multiplies, a unit more for each pair of the operands' 64 bit words. A comparison of two strings costs the bytes of both, which `go/constant` joins to compare. So a read costs time in proportion to the file's tokens, and a comment, which is no token, buys no work. Without the budget, an `iota` block whose every spec repeats an expression of m literals cost m squared (98 KB took 2.7 s), a string compared with itself again and again its length each time, and a rational of 8169 bits summed a million times, 4 MB, 12.8 s. Each figure is an example, not a ceiling. At the token limit that `iota` block, 10 MB, is refused after 7.0 s on the slowest worker node and 3.4 s on another. The slowest shape measured is the same block of typed `float32` leaves, each addition rounded to `float32`, which costs 4.4 times the time per unit: 1.5 million tokens of it are refused after 5.8 s on the slowest worker node, so at the token limit about 16 s; the sum of a million rationals of 8169 bits is refused after 0.96 s, and of 3300 bits after 0.41 s, on worker nodes, and the `iota` block behind a comment of 22 MiB after 1.1 s. The specs that repeat an expression that reads no `iota` share its one value |
| walk entries | 1000000 files and directories | a `tool_version` extractor without `files` walks the repository; the entries of the directories it passes over are counted, what is inside them is not. The largest tree measured, a Go module cache, holds 136007. One directory of 999999 files takes 3.0 s and 3 MB on the build node: a file costs the walk a few microseconds |
| walk directories | 200000 | a directory costs the walk an open and a listing, and a pin in it a read, so this is the walk's bound on time: 199000 directories with a 144-byte `.tool-versions` of another tool in each take 26.3 to 30.0 s and 69 MB on the build node (66.4 s on a busier node), and 199999 empty ones, which git cannot check out, 7.0 to 9.2 s. A million empty directories took 82 s before the limit. The largest tree measured holds 27149 |
| walk names | 16 MiB | the walk opens each directory from the one above it and holds the names of the directories it has still to walk, not their paths, so a long path costs its length once: 60000 directories of 250-byte names below a path of 3765 bytes take 8.2 s and 36 MB, where whole paths took 1.67 GB for 200000. Past the limit, as for 200000 such names in one directory, the walk is refused in 0.9 s |
| walk depth | 64 directories | the deepest tree measured nests 17. A chain 64 deep with 15000 files in each directory takes 3.9 s and 3 MB |
| git tags on HEAD | 64 MiB of `packed-refs` and 1000000 refs in it; 1000000 entries below `.git/refs/tags`; a ref name of 4096 bytes; 5 symbolic refs; 16 tag objects a tag leads through; 1000 packs; of a packed object, 4095 deltas and 64 KiB of each object of the chain; 32 MiB of rebuilt objects kept; 256 MiB of work in all | `package_version`'s `tag` reads the refs whole, as git's index is read, and an annotated tag's object only where `packed-refs` does not peel it. A loose object is inflated to its first 256 bytes, from at most 64 KiB of the file, enough for its header and a tag's object line. A pack is searched through its index where it lies, some 20 reads for millions of objects; an object it stores as a delta is rebuilt from its chain of bases, each object at most 64 KiB inflated, since git deltas a tag only against other tags, which are a few hundred bytes or a few thousand signed. Each object rebuilt is kept, so the chains of the tags a fetch packs, which share their bases, are read once. git's `gc --auto` repacks at 50 packs. One budget of work holds the whole read, shared by every tag: each byte a zlib stream takes or gives, each byte a delta writes, 1 KiB for each object or delta read, and 4 KiB for each search of an index, some 20 reads; an object peeled once is remembered, so tags that name it again cost nothing. Each `package_version` extractor with `tag` has its own budget, so the config, never the repository, sets how many reads there are. 20001 annotated tags off HEAD, each a loose ref with its object a delta in a pack, as a fetch writes them, do 66 MB of it in 0.91 s on the builder node; packed-refs with ^ lines do none. The shapes a sender may choose are refused at the budget: a 64 KiB tag under a chain of 4095 deltas that each copy it whole, named by 1000 tags, in 47 ms, and 64 such chains of a 200 byte tag, 262144 objects to read, in 0.78 s, the slowest shape measured; each figure on builder (4 cores, 4 GiB), anon at most 115 MiB |
| git index | 64 MiB, whatever `max_bytes` is, and 1000000 entries; a path of 4096 bytes, and 64 MiB of paths in all | a glob in `files` reads `.git/index` whole and keeps the path of each entry: an entry is about 70 bytes and its path, so 64 MiB holds some 600000 files, and the entries limit is the walk's. A version 4 index writes each path as what it keeps of the one before and a suffix, so 65 bytes of the index can make a path of 4096 and 64 MiB of them gigabytes of paths: a path is held to 4096 bytes, the longest a file may be named, and the paths together to 64 MiB, which an index of plain paths cannot pass. An index read with the wrong object format is refused, since each entry's path must be the length its flags give |
| `files` of `env`, `yaml`, `json`, `jsonc`, `json5`, `toml` and `regex` | 64 entries in `files`; 256 MiB of paths matched, each glob's weight times the bytes of the tracked paths it is matched against; 10000 files read | each glob is matched against the tracked files below its literal directory, `deploy/` for `deploy/*.env`, which a binary search finds in git's index, sorted by path; a glob whose first segment holds a wildcard, such as `*/*.env` or `**/*.yaml`, is matched against every tracked file, and so is every glob over an index out of git's order, which git itself refuses to read, and the refusal says so. A match costs time in proportion to the path, about 33 ns a byte on a 3 core worker node. A glob without `**` weighs 1, and one with `**` weighs its other segments, since the match retries what follows the last `**` at each later segment of a path: `infra/**/*.yaml` weighs 2. The weight is then multiplied by one for each 8 bytes of the glob's longest segment that holds a `*`, since a match tries what follows a `*` at each byte where it could end: `**/*` and 251 more bytes, over segments of 255 bytes, took 655 ns a byte of path at a weight of 1, and takes 28 ns a weighed byte on a 2 core worker node at its weight of 32. Each byte of a character class, its brackets included, counts as 3 bytes of the segment, since a match reads a class again at each byte it tests: twenty classes of 13 bytes after a `*` took 72 ns a weighed byte with each byte counted once, and take 21 ns with 3 cores at their weight of 98. An index may hold 64 MiB of paths, over which 64 globs took 167 s. The bytes to match are each glob's weight times the bytes of the paths it is matched against, known once the index is read, so a spec over the bound is refused before any glob is matched. The bound fits about 22 MB of paths for a dozen globs with a wildcard first, some 450000 files of 50 bytes or 320000 of 70, and 4 such globs over a million files of 60 bytes. A very large monorepo with many such globs is refused, which makes the rule unknown: list fewer globs, or start each with its directory. A file costs an open, a stat and a read of its first bytes before its reader runs, however small it is, so a glob over a million empty files would run for minutes; 10000 empty files take 0.9 s on the build node, with the 4 cores its runner pod gets, and allocate 12 KB each |
| the values of `env`, of `dependency`, or of `files` | 100000 and `max_bytes` in all | a CI file repeats a value for each job that inherits it, so the values of every file, job and step together hold to the limits of one extractor's. A Jenkinsfile is read again for each stage that sets the variable, all within the extractor's one work budget, so a file of hundreds of such stages is refused at it: 400 of them are refused in 1 s |
| `package_version` files | 64 in `files` | a manifest gives at most two values, so 64 files stay under the limit on values by construction; a package has a manifest or two of each kind, and a rule over more packages names several extractors |
| files one extractor reads | `max_bytes` in all | a walk may find many files, and symlinks or copies may repeat one, so the files of one extractor are read up to its `max_bytes` together, as one file is; in `tool_version` and the `files:` of yaml, json, jsonc, json5, toml and regex, links that end at one file read it once, and `env` and `package_version` read each name they list. 100 links to a 32 MiB file take 0.4 to 0.7 s, where reading each link took 31 to 64 s in all before |
| a walk for markers | 100000 files, 2 GiB read, 100000 markers | the `marker` extractor opens every file its `files` name, and `errata-unbound-markers` every file any of them names. Measured on the builder node, each figure in proportion to the walk: 20000 small files, a tenth of them Markdown and a tenth Go holding a marker each, took 0.66 s and 144 MiB allocated, so 100000 take about 3.3 s; 256 MiB of files without the bytes `errata` took 0.34 s, so 2 GiB about 2.7 s; 128 MiB of Markdown that holds them, each file parsed, took 3.0 s, so 2 GiB about 48 s. A run reads and parses each file once, whichever walks take it in, so the cost of n rules that read markers over one tree is one walk and n listings of git's index, not n + 1 walks. Every file counts its size against the bytes read, a file over `max_bytes` too, which the walk reads in pieces of 1 MiB for the bytes `errata`. A Markdown file it parses holds to the Markdown limits above, and the markers of one walk to the cap on values |
| file name | 4096 bytes | the longest path Linux opens. A longer `file:` in an extractor is a load error, since every message about a file names it whole |
| quoted text | 64 bytes | errata's message that quotes a key, an anchor or a number from a file, or a path, a pattern, a file or a selector from `errata.yaml`, cuts it there and writes `...`, since a message goes into the CI log and the JUnit report. A parser's message, such as yaml.v3's for an unknown anchor, which quotes the anchor's name, is cut at 256 bytes. A rule's message quotes a value of at most 256 bytes whole. Of a longer one it quotes 256 bytes, from 32 bytes before the first byte where it differs from the other values of the message, marked as in `(from byte 266 of 299)`, so two long values that differ only at the end still print differently |

The regex search finds exactly the matches Go's `FindAllSubmatchIndex` finds over the whole file.
The clock only ends a search early, and a search it ends makes the rule unknown, never a short list
of values. Apart from the regex search, errata holds memory in
proportion to the nodes a file holds, not its lines: 32 MiB of newlines after one JSON value peaks
at 182 MB, and a regex over them at 85 MB.

A `text` extractor's match time, values and capture groups are bounded as a `regex` extractor's
are. The real files measured are 5962 Markdown files, 88 MB of changelogs and READMEs from Go
modules and npm packages, and none is refused. Peak memory depends on the file's shape within
these bounds, so errata states no ceiling. The densest shapes measured at the default, as
examples: 2.0 GB for one paragraph of nine-byte lines lazily under 499 list items, 2.0 GB for the
same lines under a setext underline, and 1.7 GB for GitHub table rows read as one paragraph. A
refusal for nodes, open blocks or pieces says to raise `max_bytes`; one for the time, the nesting,
the runs, an HTML line or the prose says that a `regex` extractor can still read the file.

A `comment` extractor's match time, values and capture groups are bounded as a `regex` extractor's
are. Its lexer reads the file once, each byte a bounded number of times, since every fence of a raw or
long string is bounded, and holds the comments' text, which is never longer than the
file.

The `ini` and `properties` values hold at most `max_bytes` bytes in all, as every extractor's
do; a git key with no `=` reads as `true` and a byte Java reads as U+FFFD takes three, so the
values can outgrow the file. At 32 MiB the most memory a shape measured takes is 403 MB, an ansible
host of many variables, and a properties value of bytes that are not UTF-8 takes 194 MB. A
64 MiB file read with `max_bytes: 67108864` takes at most 714 MB, for such a value joined over
1000 lines (383 MB on one line).

`max_bytes` sets one extractor's file size limit, in bytes, higher or lower, and every limit
above that scales with it: the nodes, the regex and Markdown time, and the Markdown counts. The
INI work scales with the file itself. Leaving it out, or writing 0, keeps 32 MiB. A negative
number, or one over 67108864 (64 MiB), does not load: the densest file measured at 32 MiB peaks at
2.55 GB, so one at 64 MiB may take about 5 GB, under the 7 GB of a hosted CI runner. GitHub warns
about a pushed file over 50 MiB.

```yaml
extract:
  chart_version:
    yaml: {file: charts/index.yaml, path: "entries.web[0].version", max_bytes: 67108864}
```

## When a rule is unknown

A file that is missing, does not parse or is over a limit makes the rule unknown, except a missing
file for `exists`, which is the value `false` (see [`exists` and `size`](#exists-and-size)). So does a YAML or
JSON file that repeats a key in one mapping, and a TOML file that defines a key or a table twice or
holds a number or a date out of range. errata finds a repeated key itself, with one hash map per
mapping, since the checks of the YAML and TOML libraries compare every key with every other. A YAML
file also makes the rule unknown for a key that is a mapping or a sequence, two alias keys of one
name in a mapping, a tag its value does not fit (`!!int x`), a merge key that names something other
than mappings, and an alias inside the anchor it names. errata refuses these inside a value a merge
key would add but the mapping sets itself, at any depth of merges, which yaml.v3 skips; that
includes an alias merged into the anchor it names, which yaml.v3 reads when the key that would
repeat the anchor is one the mapping sets. errata's messages quote at most 64 bytes of the file's
text, since a key or a number can be as long as the file; a parser's message is cut at 256 bytes. A file that parses but holds
no match gives zero values, which the rule decides about. A value the file only computes makes
the rule unknown too, unless `skip_not_literal` lists its extractor (see
[Values a file only computes](#values-a-file-only-computes)).

An XML file also makes the rule unknown when it is not well formed, as encoding/xml reads it in
strict mode, or not namespace well formed: no root element or a second one, an element left open,
an end tag that closes another element or none, text outside the root, a repeated attribute (two
prefixes bound to one namespace repeat one too), a prefix no `xmlns:` declares, `xmlns:p=""`, a
declaration of the prefix `xmlns` or of `xml` to another namespace, a reference to an entity a DTD
declares, a `<!DOCTYPE>` without a name, after the root element starts or a second one, any other
`<!...>` directive, an XML declaration anywhere but at the very start of the file (a byte order
mark may come before it) or a second one, and a processing instruction named `xml` in another
case, such as `<?XML ...?>`.

An INI, properties or env file makes the rule unknown for what its tool refuses, as the sections
on them say, and so does a file a `tool_version` extractor reads ([`tool_version`](#tool_version))
or a `package_version` extractor reads ([`package_version`](#package_version)), a manifest or a
lockfile a `dependency` extractor reads ([`dependency`](#dependency)),
a script that does not parse in its dialect ([`shell`](#shell)), and a Jenkinsfile
that does not lex ([`jenkins`](#jenkins)). A Markdown file parses whatever it holds, so only a bound under [Limits](#limits) refuses one.

## The check protocol

| Exit code | Meaning |
|---|---|
| 0 | the claim is true |
| 1 | the claim is false |
| anything else, or a timeout | unknown: the check could not evaluate. Not an erratum, but it fails the run |

Stdout is the message. If stdout is a JSON object with a `message` string, errata uses that string.
errata holds at most 33554432 bytes (32 MiB) of stdout, and of stderr apart. It reads what the
program writes past that and drops it, and the program runs on to its exit or its timeout: the
verdict is still the exit code, and the message ends with a line naming each stream errata cut and
the bound. A cut stdout is no longer a whole JSON object, so the message is the text errata held.
A program check takes no `max_bytes`. The program runs with the repository root as its working
directory and receives `ERRATA_ROOT`, `ERRATA_CHECK_ID` and `ERRATA_SCOPE` in its environment. Its
stdin is empty. Read the tree from `ERRATA_ROOT`, not the working directory: on a
[witness](#witness) it is the fixture, while the working directory stays the repository root.

On Linux and macOS the program runs in a process group of its own, which errata kills on a timeout
and again once the program exits, so a child it starts in the background does not outlive the
check, unless the child leaves the group, as `setsid` does. A child that keeps the program's stdout
or stderr open holds the check one second past the program's exit; a program that exited 0 is then
unknown, and any other exit keeps its verdict. On Windows errata starts no process group, and a
child left running there runs on. A generated check's generator runs the same way, and its exit
status is not a verdict ([`generated`](#generated)).

## Scopes

| Scope | Question | Needs |
|---|---|---|
| `repo` | Is this claim true of the files? | nothing |
| `live` | Is this claim true of the running system? | read access (not built yet) |
| `applied` | Will this claim be true after an apply? | read access and a dry run (not built yet) |

errata never applies anything. Until a provider for a running system exists, `live` and `applied`
checks are skipped by name, and the coverage line gives the gap for each. A skip does not fail the
run unless `errata check` is given `--fail-on-skip`.

## Command line

[cli.md](cli.md) is the command line reference: every command and flag, the exit codes, and what
`--fail-on-skip`, `--output` and the escaping of control characters do. It is the help of the
last release, written from it by `make cli-doc` in the commit a release tags, and a release whose
file differs from its help is refused. Between releases it describes the image and binaries you
download. `errata --help` and `errata <command> --help` print the help of
the binary in hand.
[`errata check`](cli.md#errata-check), [`errata list`](cli.md#errata-list) and
[`errata version`](cli.md#errata-version) each have a section there.

Two decisions behind it have their reasons in an ADR. A skip under `--fail-on-skip` fails the run
as an unknown does, exit code 1 and no erratum
([ADR 0001](adr/0001-an-unknown-fails-the-run-without-being-an-erratum.md)). `--output` writes a
format to a file beside the text report on stdout
([ADR 0005](adr/0005-output-writes-a-format-to-a-file-beside-the-text-report.md), which has the
reasons and the rest of the rules).

## Listing the checks

`errata list` prints the checks a config holds, as `errata check` loads them, and runs none. It
reads the config with the loader `check` uses, so a config that `check` refuses, `list` refuses
with the same message and exit code 2. It reads none of the files the checks name. A program
that needs to know what errata will run reads `errata list --format json` instead of parsing
`errata.yaml` itself, and compares values rather than spellings.

Every value is the one the loader resolved, with these defaults written out:

- `run` of a program check: the [default run](#defaults) when the check sets none, and the
  placeholders replaced by the check's id in either;
- `run` of a generated check's generator: the placeholders replaced by the check's id;
- `timeout` of a program check and a generated check: `30s` when the check sets none;
- `max_bytes` of every extractor and of a generated check: 33554432 (32 MiB) when it sets none;
- `dialect` of an `ini` extractor: the one the file's name chooses when the extractor names none
  (see [`ini`](#ini));
- `language` of a `comment` extractor: the one the file's name chooses when the extractor names
  none (see [`comment`](#comment));
- `compare` of an `agree` rule: `text` when the rule sets none;
- `normalize`, `allow_empty` and `skip_not_literal` of a rule: an empty list when the rule sets
  none.

For this `errata.yaml`:

```yaml
checks:
  - id: lint-passes
    reason: The linter guards the style.
    scope: repo
    run: [make, lint]
  - id: version-agrees
    reason: Every manifest names one version.
    scope: repo
    extract:
      app: {json: {file: package.json, path: version}}
      deps: {ini: {file: tox.ini, section: testenv, key: deps}}
    agree: [app, deps]
    normalize: [trim]
    skip_not_literal: [deps]
    witness: [version-differs]
  - id: replicas-fit
    reason: The cluster has a node for every replica.
    scope: repo
    extract:
      replicas: {yaml: {file: deploy.yaml, path: spec.replicas, max_bytes: 65536}}
      nodes: {regex: {file: README.md, pattern: '(\d+) nodes'}}
    assert: int(replicas[0]) <= int(nodes[0])
  - id: api-docs-current
    reason: docs/api.md is generated from the schema.
    scope: repo
    generated:
      file: docs/api.md
      run: [python3, scripts/api-docs.py]
required:
  - lint-passes
  - version-agrees
  - replicas-fit
  - api-docs-current
```

`errata list --format json` prints one object:

```json
{
  "version": 1,
  "checks": [
    {
      "id": "lint-passes",
      "reason": "The linter guards the style.",
      "scope": "repo",
      "at": {"file": "errata.yaml", "line": 2, "outside": false},
      "type": "program",
      "run": ["make", "lint"],
      "timeout": "30s",
      "witness": []
    },
    {
      "id": "version-agrees",
      "reason": "Every manifest names one version.",
      "scope": "repo",
      "at": {"file": "errata.yaml", "line": 6, "outside": false},
      "type": "rule",
      "extract": {
        "app": {"json": {"file": "package.json", "max_bytes": 33554432, "path": "version"}},
        "deps": {"ini": {"file": "tox.ini", "max_bytes": 33554432, "dialect": "tox", "section": "testenv",
                         "key": "deps"}}
      },
      "relation": {"agree": ["app", "deps"], "compare": "text"},
      "normalize": ["trim"],
      "allow_empty": [],
      "skip_not_literal": ["deps"],
      "witness": ["version-differs"]
    },
    {
      "id": "replicas-fit",
      "reason": "The cluster has a node for every replica.",
      "scope": "repo",
      "at": {"file": "errata.yaml", "line": 16, "outside": false},
      "type": "rule",
      "extract": {
        "nodes": {"regex": {"file": "README.md", "max_bytes": 33554432, "pattern": "(\\d+) nodes"}},
        "replicas": {"yaml": {"file": "deploy.yaml", "max_bytes": 65536, "path": "spec.replicas"}}
      },
      "relation": {"assert": "int(replicas[0]) <= int(nodes[0])"},
      "normalize": [],
      "allow_empty": [],
      "skip_not_literal": [],
      "witness": []
    },
    {
      "id": "api-docs-current",
      "reason": "docs/api.md is generated from the schema.",
      "scope": "repo",
      "at": {"file": "errata.yaml", "line": 23, "outside": false},
      "type": "generated",
      "generated": {"file": "docs/api.md", "run": ["python3", "scripts/api-docs.py"], "max_bytes": 33554432},
      "timeout": "30s",
      "witness": []
    }
  ],
  "required": ["lint-passes", "version-agrees", "replicas-fit", "api-docs-current"]
}
```

| Key | Meaning |
|---|---|
| `version` | the version of this shape, now `1` |
| `checks` | every check, in the order the config holds them |
| `id`, `reason`, `scope` | as the config writes them; a reason in a block keeps its last line break |
| `at` | where the check is written |
| `at.file` | the config's path relative to the root, or as given for a config outside the root |
| `at.line` | the line the check starts on, counted from 1 |
| `at.outside` | true for a config outside the root |
| `type` | `program`, `rule` or `generated` |
| `run` | program check: the command, as a list of arguments, placeholders replaced |
| `timeout` | program check and generated check: a Go duration as Go prints it, so `2m` reads `2m0s` |
| `generated` | generated check: the committed file and the generator errata will run |
| `generated.file` | the committed file, as the config writes it |
| `generated.run` | the generator, as a list of arguments, placeholders replaced |
| `generated.max_bytes` | the bound on the file and on the generator's output |
| `extract` | rule: each extractor under its name, as `errata.yaml` spells the entry, with the defaults above written out; a key at its zero value (false, 0, empty) that the defaults above do not name is left out |
| `relation` | rule: what its values must satisfy, with exactly one of `agree` and `assert` |
| `relation.agree` | the names, in the order `agree` lists them |
| `relation.compare` | beside `agree`: how its values are compared, `text` (the string form of each value after `normalize`) or `semver` (as versions and ranges) |
| `relation.syntax` | beside `compare: semver`: the syntax its values are read in, as `syntax` names it, or `any` for a rule that sets none (see [`compare`](#compare)) |
| `relation.assert` | the expression as written |
| `normalize`, `allow_empty`, `skip_not_literal` | rule: as the config lists them |
| `witness` | every check: the fixtures it names, in order; an empty list when it names none (see [`witness`](#witness)) |
| `required` | the ids `required` lists, in its order; an empty list when the config has no `required` (see [`required`](#required)) |

Values print as the config writes them, but for the defaults above. A file path, an ini section
and a key are not cleaned or folded, so `./f` and `f`, or git's `Remote.Origin` and
`remote.origin`, print differently although `check` reads them as one place: compare them by the
rules of their kind (see [`ini`](#ini) for each dialect's).

`version` changes when a key is removed or renamed or a value changes its meaning. A new key does
not change it, and neither does a new value of `type` or `compare`, or a new kind of
extractor under `extract`. errata plans more of each, such as a way to compare images, and more
extractors. A program that meets a value or a kind it does not know should read
that check as one that changed. Both formats write control characters as
[`errata list`](cli.md#errata-list) says.

The text format prints the same checks for a person to read, each with its scope, its type and the
labels of the text output, then a count. A name, an argument and a string of an extractor print as
written when they hold only letters A to Z, digits and `_./@%+=:$*?^()\[]|-`; between backquotes,
still as written, when they hold anything else that prints, such as a space, a comma or `é`; and
quoted with Go's escapes when they are empty or hold a backquote or a character that does not
print, such as a tab, a control character, a direction control or a line separator, so none reads
as two. Its layout may change from one release to the next; a program reads the JSON.

## Output formats

`errata check` writes its report in one of three formats, chosen with `--format` for stdout and
with `--output` for a file ([`errata check`](cli.md#errata-check)). `errata list` writes text or
JSON ([Listing the checks](#listing-the-checks)). Every format escapes the control characters a
file or a program's output holds, as [`errata check`](cli.md#errata-check) says.

### The text report

`--format text`, the default, is for a person to read. The README's
[Quick Start](../README.md#quick-start) shows one erratum in it.

| Part | What it holds |
|---|---|
| a block for each erratum and each unknown | `FALSE` or `UNKNOWN`, the check's id (`id [witness name]` for a witness) and its scope, then `why:` with the reason and `said:` with the message, which is a program's output or a rule's account of its values |
| the coverage line, on every run | the errata and unknowns counted, then `Coverage: ran N, skipped M.`; then `N witnesses held.` when a check names witnesses, and `A skip fails this run.` under `--fail-on-skip` with a check skipped |
| under the coverage line | `skipped id (scope): gap` for each skipped check, `left out of id (scope), not literal:` for each value `skip_not_literal` left out, whatever the verdict, and `note on id (scope):` for a rule that held with a note, such as an agree over ranges alone |

A check whose claim holds prints nothing above the coverage line. The layout may change from one
release to the next; a program reads JUnit, SARIF or the JSON of `errata list`.

### JUnit XML

`--format junit` writes one `<testsuite name="errata">` with a `<testcase>` for each check and
each witness, which GitLab, GitHub Actions and Jenkins render per check without a plugin.

| errata | JUnit |
|---|---|
| a check | a `<testcase>` whose `name` is the id, or `id [witness name]` for a witness, whose `classname` is `errata.` and the scope, such as `errata.repo`, and whose `time` is the time the check took, in seconds |
| an erratum | a `<failure>` whose `message` is the message, with the reason as its text |
| an unknown | an `<error>`, as a failure is written |
| a skipped check | a `<skipped>` whose `message` is the gap; under `--fail-on-skip` an `<error>` whose `message` is `skipped: ` and the gap, with the reason as its text |
| a value `skip_not_literal` left out | a line in the testcase's `<system-out>`, whatever the verdict |
| a check whose claim holds | a `<testcase>` with no `<failure>`, `<error>` or `<skipped>` |

The suite's `tests`, `failures`, `errors` and `skipped` count the testcases of each kind.

### SARIF

`--format sarif` writes [SARIF 2.1.0](https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/sarif-v2.1.0-errata01-os-complete.html),
the format GitHub code scanning reads. The [handbook](handbook.md#sarif-for-github-code-scanning)
shows the workflow that uploads it.

What goes where in the file:

| errata | SARIF |
|---|---|
| a check | a rule in `tool.driver.rules`, with the check's id, and its reason as the full description and the help text |
| an erratum | a result, at the location of the first value that disagrees, with links `[value 1](1)`, `[value 2](2)` and so on to every other value, then to each value `skip_not_literal` left out |
| an unknown | an `error` notification in `invocations[0].toolExecutionNotifications`, with the message and the reason, and each value `skip_not_literal` left out as a location; `executionSuccessful` is then false |
| a skipped check | a `note` notification with its gap, so the coverage is in the file; under `--fail-on-skip` an `error` notification, and `executionSuccessful` is then false |
| a check whose claim holds | its rule, and no result |
| a check whose claim holds with values left out | its rule, no result, and a `note` notification that counts the values `skip_not_literal` left out and lists each as a location |
| errata itself | `tool.driver`, with errata's version as `version` |

Code scanning does not display notifications. An unknown or a skip shows on GitHub only as the
failed job; the file holds them, and `errata check` in the text format prints them. A clean code
scanning tab is therefore not a clean run: read the job's result.

The result points at the value the rule blames and links every other value as a related location.
An agree rule compared as text groups the values by their normalised text and takes the largest
group as the majority, and blames the first value outside it. Values come in the order `agree:`
names their extractors, so on a tie the first name in `agree:` counts as right. With two values
that differ, the alert is on the second one. List the source of truth first. With
`compare: semver` there are no groups of text: of the two values the message names, the rule
blames the later, the one that does not agree with a value before it, so over `20`, `20.11.1` and
`18` the alert is on `18`. The semver blame goes by position alone, however many places hold each
value: with `18` in one file and `20.1.0` in two after it, semver blames `20.1.0`, where the text
rule would blame `18`, the minority. Ranges alone that no release satisfies blame the first range
at which the ranges before it stop overlapping, in the same order: over `>=20`, `<22` and `>=23`
the alert is on `>=23`. An assert rule has no majority, so it blames the first
value it extracted. A program check reports no values, so its result points at the line where the
check starts in the config file, and so does a rule whose extractors found nothing; the message is
what the program printed. A rule that blames an `exists` value of `false` points at its check
too, since no file is at the value's path, and its message names that path.

Every path is relative to the repository root, with `uriBaseId` `%SRCROOT%`, so code scanning
places it in the checkout. A value whose path is absolute, names a host or climbs out with `..` gets
no place in a file; its message names the path. So does a check in a config outside the root, given
with `-c`: its result names the config as given and points at no file. A backslash in a file name is part of the name, and
the URI encodes it as `%5C`. upload-sarif decodes the URI to open the file, and on Windows a
backslash separates, so a name is placed only when it stays inside under that reading too: no `..`
between backslashes, no drive such as `C:` and no `\\host` prefix. A name that starts with one
letter and a colon, such as `a:b.yaml`, reads as a drive there and gets no place either. errata
writes no `partialFingerprints`; `upload-sarif` computes them from the files when it uploads.

Text from the repository and from a program's output reads as itself, with two changes. Each
brace is written twice, as SARIF requires, since one brace starts a placeholder. Between a `]` and a
`(` errata puts U+2060, a character of no width, so that no text from the repository forms a link
in a SARIF viewer; the links in a result are errata's own `[value N](N)`.

GitHub rejects a whole upload over 10 MB gzipped, with more than 25,000 results in a run, or with
more than 1,000 locations in a result, and it shows only the top 5,000 results by severity and 100
locations of each. To stay inside the limits, a message quotes at most 200 characters of a value
and 4,096 of a program's output, saying so where it cuts. A result links at most 999 other values.
The compared values come first, since they show the disagreement, but 100 of the slots stay for
values `skip_not_literal` left out, so neither kind pushes the other out; a kind that needs fewer
slots leaves them to the other. The message counts the values it could not link, how many
compared and how many left out. The count of values left out stays whole at the end of a message
cut at 4,096 characters. A run keeps at most 25,000 results.

The size that counts is what upload-sarif sends: it adds a fingerprint to each result and gzips the
file as compact JSON. errata measures that, with 40 bytes for each fingerprint, against 9,800,000
bytes, 2% under 10 MB read as 10,000,000 bytes. Past it, errata leaves out results from the end;
if the file does not fit with none, it cuts each rule's help to 1,024 characters and each
notification's message and reason to 200, then leaves out notifications. A warning notification
counts what was left out or cut. Code scanning does not display notifications, so read the file or
the job to see it.

