# errata

[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![release](https://img.shields.io/github/v/tag/f-klement/errata?sort=semver&label=release)](https://github.com/f-klement/errata/releases)
[![docker hub](https://img.shields.io/docker/v/fklement/errata?sort=semver&label=docker%20hub&logo=docker&cacheSeconds=3600)](https://hub.docker.com/r/fklement/errata/tags)
[![docker pulls](https://img.shields.io/docker/pulls/fklement/errata?cacheSeconds=21600)](https://hub.docker.com/r/fklement/errata)
[![go](https://img.shields.io/github/go-mod/go-version/f-klement/errata?logo=go)](go.mod)
[![Ko-fi](https://img.shields.io/badge/Ko--fi-buy%20me%20a%20coffee-FF5E5B?logo=kofi&logoColor=white)](https://ko-fi.com/fklement)

A checker for the claims a repository makes about itself.

A repository says the same thing in several places: a sentence in a README, a value in a Helm
chart, a cron string, a command in a runbook, a field on a live object. Nothing keeps those places
in agreement. errata tests each claim against its source, prints the ones that are false with the
reason the check exists, and says how much of the suite it could run.

It is built first for GitOps repositories, where Argo CD can report Synced and Healthy while the
documentation describes a system that changed weeks ago. The same kind of claim lives in every
application repository too: a documented command that no longer exists, a version pinned in two
files that disagree, a pipeline job that no longer runs the step its README promises.

Like errata or find it useful? Offer me a coffee. All features are free either way.

## Motivation

**Every tool in a GitOps repository checks one form of a fact. None checks that the forms agree.**
Argo CD makes the cluster match the manifests and never reads a sentence. Renovate raises versions
and never asks whether the runbook still describes them. A schema validator reads one file at a
time. So a repository can pass every one of them and still contain false claims.

Three real ones, from one morning on one repository, all while Argo CD reported Synced and Healthy:

1. A document said CI runs on the node `atlas`. It had moved to `builder` seven weeks before. An
   agent reading that document would have gone to the wrong machine.
2. A runbook said to copy a file onto the control plane and restart. The file carried a kubelet
   flag whose prerequisite did not exist on the host, so following the documented steps would
   have stopped the only control plane.
3. A staleness threshold of 26 hours watched a backup that ran every 12 hours. A backup could fail
   twice before anybody heard about it.

A person found all three by reading carefully. errata exists so that the next one is found by a
check that runs on every push, and that explains, when it fails, why it was written.

## Quick Start

Install the binary from the [releases page](https://github.com/f-klement/errata/releases), or:

```bash
go install github.com/f-klement/errata/cmd/errata@latest
```

Put an `errata.yaml` at the root of the repository:

```yaml
checks:
  - id: docs-name-the-ci-node
    reason: >
      CI moved to the node `builder`. A document that names another node sends whoever reads it,
      person or agent, to the wrong machine.
    scope: repo
    run: [bash, checks/ci-node.sh]
```

Write the check. It exits 0 if the claim is true and 1 if it is false. What it prints is the
message:

```bash
#!/usr/bin/env bash
if grep -rn 'runs on node' docs/ | grep -v builder; then
    exit 1
fi
```

Run it:

```console
$ errata check
FALSE docs-name-the-ci-node (repo)
  why:  CI moved to the node `builder`. A document that names another node sends whoever reads it, person or agent, to the wrong machine.
  said: docs/ci.md:4:CI runs on node atlas.

1 erratum, 0 unknown. Coverage: ran 1, skipped 0.
```

Or with Docker, with the repository mounted at `/repo` and used as the working directory:

```bash
docker run --rm -v "$PWD:/repo" -w /repo fklement/errata check
```

## Features

**Status: early.** What works today:

- **Checks as programs.** A check is any program in any language, run through the check protocol
  below. An existing suite of shell or Python checks becomes an errata suite without a rewrite.
- **A reason on every check.** A check without one does not load, and the reason is printed beside
  every failure.
- **Fail closed.** A check that crashes, times out or cannot find its program is unknown. An
  unknown is not an erratum, because its claim was not shown false, but it fails the run.
  An unknown key in `errata.yaml` is an error, so a misspelt `reason` cannot switch a check off.
- **Coverage on every run.** Ran N, skipped M, and the gap behind each skip: the provider operation
  the run lacked. A run in CI with no
  cluster says out loud which checks it could not reach.
- **Rules in YAML.** A rule reads values out of YAML, JSON, TOML, XML, INI and Java properties
  files, regular expression captures and the prose of Markdown files, and states that they agree
  or that a [CEL](https://cel.dev) expression over them holds.
  An erratum names every value with the `file:line` it came from.
- **Output for people and for CI.** Readable text, JUnit XML that GitLab, GitHub Actions and
  Jenkins render per check, or SARIF that GitHub code scanning turns into alerts on the line that
  disagrees.
- **One static binary**, for Linux, macOS and Windows on amd64 and arm64, and a container image.

Designed and not built yet, in the order of [ROADMAP.md](ROADMAP.md):

- **Checks against the running system** (`live` scope) and against what an apply would produce
  (`applied` scope, dry run only).
- **Fields git cannot remove.** A server-side apply dry run under the reconciler's field manager
  shows every field another manager keeps alive, and names that manager. No tool reports this
  today.
- **Verdicts, not drift.** A model of each reconciler (whether it runs, how often, whether it
  prunes, whether it repairs a hand edit) separates a difference that heals in three minutes from
  one that never will.
- **Attested facts:** a value no API can read, recorded with a source and a date, checked for both.

The words above have one meaning each, fixed in [GLOSSARY.md](GLOSSARY.md).

## What belongs in errata

Ask where the value a check compares comes from.

- **From a file read as text or as Markdown prose, from YAML, JSON, TOML, XML, INI or Java
  properties, from git, or from a live system:** it is an
  errata check. That holds in a GitOps repository and in an application repository alike:
  documented commands, versions pinned in two places, CI wiring, thresholds derived from schedules.
- **From the language itself** (its parser, its import graph, its types, or an import of the
  application): it belongs in that language's linter or test suite. Module boundaries, banned
  imports and type rules are what ruff, eslint, import-linter and `go vet` analyzers are for.

You can still run such a check through the check protocol to have one runner, and errata will run
it like any other program. It adds no language machinery for it.

errata is not a schema validator, an admission controller or a security policy engine either. Use
[kubeconform](https://github.com/yannh/kubeconform) and [Kyverno](https://kyverno.io) for those.

## Usage

### Checks

A check is a program check or a rule. A program check has `run`; a rule has `extract`. A check
with both, or with neither, does not load, and neither does a file with no checks: it would run
nothing and pass. Every check has these keys:

| Key | Required | Meaning |
|---|---|---|
| `id` | yes | lower-case words joined by single hyphens, unique in the file |
| `reason` | yes | why the check exists and what breaks without it |
| `scope` | yes | `repo`, `live` or `applied`; a rule is always `repo` |
| `run` | program check | the command, as a list of arguments, run from the repository root |
| `timeout` | no | program check only: a Go duration such as `10s` or `2m`; default `30s` |
| `extract` | rule | the rule's extractors, each under a name of its own |
| `agree` | rule: this or `assert` | the extractors whose values must all be equal |
| `assert` | rule: this or `agree` | a [CEL](https://cel.dev) expression over the extracted values that must be true |
| `allow_empty` | no | rule only: the extractors that may find no value, such as `[client]` |
| `skip_not_literal` | no | rule only: the extractors whose not literal values are left out, such as `[php]`; only an extractor that can return a not literal value may be listed (see [Values a file only computes](#values-a-file-only-computes)) |
| `normalize` | no | rule only: `trim`, `lower` or both, in order, applied before `agree` compares or `assert` sees the values |

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

### Rules

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

`agree` holds when every value from every named extractor has the same string form. It names
only extractors the rule declares, each once, and every extractor the rule declares, so none is
left out of the comparison by mistake. When the values differ, the message lists each distinct
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
lists it, and one that returns a not literal value, unless `skip_not_literal` lists it (see
[Values a file only computes](#values-a-file-only-computes)). An agree rule that has fewer than two values
to compare is unknown too, so `agree: [tags]` over a regular expression with one match is unknown
and over two matches compares them. The exception is an agree rule where an extractor that
`allow_empty` lists came back empty: one value left is true. With no value at all an agree rule is
unknown, even when `allow_empty` lists every extractor. An assert rule has no such floor: an
extractor that `allow_empty` lists and that finds nothing is an empty list, and the expression
decides, even when every extractor came back empty. Two extractors that read the same place, or a
name listed twice in `agree` or `allow_empty`, do not load. The same place is the same file, however
its path is spelt (`f`, `./f` and `d/../f` are one file), read by the same kind with the same
fields; a different `max_bytes` does not make a second place. An INI entry is one place however the
spec spells it, as its tool reads it: `section: remote, subsection: origin` and
`section: remote.origin` are one git entry. A rule takes no `timeout`, because its extractors read files
inside errata rather than running a program, and its scope is `repo` for the same reason.

#### assert

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
is the first value the extractor found. An extractor named in `assert` must be a CEL identifier
(letters, digits and `_`, not starting with a digit). `normalize` applies to the values before the
expression sees them. Numbers, durations and ports go through the functions below, so errata knows
the type of every part of the expression when it loads the file.

| Function | Gives |
|---|---|
| `int(s)`, `double(s)`, `uint(s)` | the number in `s`, by CEL's own conversions; an error names `s` |
| `duration(s)` | a duration from a Go duration (`90s`, `1h30m`, `1.5h`) or a Prometheus one (`2d`, `1w`, `1d12h`; a day is 24 hours, a year 365 days) |
| `cron_interval(s)` | the time between the runs of a five field cron schedule, as a duration |
| `port_split(s)` | a Compose port such as `"127.0.0.1:8080:80/udp"` as a map with `ip`, `host`, `container` and `protocol`; the protocol is `tcp` when the string names none |
| `same_set(a, b)` | true when two lists hold the same members, in any order and with any repeats |
| `subset(a, b)` | true when every member of `a` is in `b` |

`same_set` and `subset` compare lists of strings, ints, uints, doubles or bools, so
`same_set(ports.map(p, int(p)), [80, 443])` works. A duration times a whole number is a duration.
The rest of CEL's standard library is there too: `size`, `in`, `contains`, `startsWith`,
`matches`, and the macros `all`, `exists`, `exists_one`, `map` and `filter`. The timestamp
accessors that take a time zone, such as `getHours('Europe/Berlin')`, read `'UTC'`, fixed offsets
such as `'+01:00'`, and IANA zone names. errata carries its own copy of the IANA time zone
database (release 2026c in this version) and never reads the zone files, `ZONEINFO` or `TZ` of
the machine it runs on, so a check gives the same verdict on a laptop and in the release image,
which has no zone files. Zone rules change only when you upgrade errata. A name the database does not
hold, such as `'Local'`, makes the rule unknown.

A helper reads a string of at most 1024 bytes; a longer one makes the rule unknown, naming the
limit. The longest real arguments are far shorter: about 270 bytes for a cron schedule that lists
every minute, every hour and every day by name.

`cron_interval` reads the minute, hour and day of week fields: numbers, `*`, ranges, lists, steps
such as `*/15` and `5/15`, day names, and Jenkins' `H` and `H/N`. A step past the field's range
selects only its first value, as cronie and Kubernetes read it, so `*/90 * * * *` runs once an
hour. Day of month and month must be
`*`, because months differ in length. `@hourly`, `@daily`, `@midnight` and `@weekly` are read as
cron writes them. A schedule whose runs are not evenly spaced, such as `30 5 * * 1-5`, has no
interval, and the rule is unknown.

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
syntax error each stop the load with exit code 2, naming the check and pointing at the column:

```text
errata: load errata.yaml: checks[0]: check "staleness-threshold": assert: undeclared reference to 'treshold', at line 1 column 10 of the expression:
    duration(treshold[0]) >= cron_interval(schedule[0])
             ^
```

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

### Extractors

An extractor reads one file of the repository and returns a list of values, each with the file and
the line where it starts. A rule names its extractors under `extract:` and sets exactly one of the
eight keys in each:

```yaml
extract:
  replicas:
    yaml: {file: deploy/web.yaml, path: spec.replicas, kind: Deployment, name: web}
  engine:
    json: {file: package.json, path: engines.node}
  cpu_limit:
    toml: {file: runner/config.toml, path: "runners[0].kubernetes.cpu_limit"}
  base_image:
    regex: {file: Dockerfile, pattern: '^FROM (\S+)'}
  ci_node:
    text: {file: README.md, pattern: 'CI runs on node (\w+)'}
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
```

| Key | Fields | Returns |
|---|---|---|
| `yaml` | `file`, `path`, and `kind` or `name` for a file of several documents | every scalar the path selects |
| `json` | `file`, `path` | every scalar the path selects |
| `toml` | `file`, `path` | every scalar the path selects |
| `regex` | `file`, `pattern` | capture group 1 of every match |
| `text` | `file`, `pattern`, and `code` to read code blocks too | capture group 1 of every match in the prose of a Markdown file |
| `xml` | `file`, `path`, and `namespaces` for a file with namespaces | every node the path selects: an element's text, an attribute's value or a text node |
| `ini` | `file`, `key`, `section`, and `dialect`, `subsection` (git) or `host` (ansible) | every value of the key in the section, or the hosts or child groups of an ansible group |
| `properties` | `file`, `key` | every value of the key |

Each key also takes `max_bytes`, the largest file it reads (see Limits).

`file` is relative to the repository root. An absolute path, or one whose `..` leaves the root,
does not load. A `..` in `file` is resolved by name, before any symlink: `link/../f.yaml` reads the
repository's `f.yaml`, wherever `link` points. errata then follows the symlinks on the way to the
file as the operating system does, so a `..` in a link's target goes up from where the link
points. A link that ends inside the repository is read, whether it is relative or absolute, and
so is one whose route goes up through the repository's parent directories and back. A link that
ends outside is refused when the file is read, which makes the rule unknown. So is a link whose
route passes through any other directory outside the repository, even when it ends inside, and an
absolute link written through a symlink, such as one to `/tmp/...` on a machine where `/tmp` is a
link. The message says only that a symlink leads outside the repository: not where it points, nor
whether that exists. errata looks at nothing outside the repository to decide. A location, and
every message, names the file as `file` wrote it.

#### Paths

`yaml`, `json` and `toml` share one path grammar: part of
[RFC 9535](https://www.rfc-editor.org/rfc/rfc9535) JSONPath, with one extension.

| Form | Selects | Example |
|---|---|---|
| `$` | the whole document; optional at the start | `$.spec.replicas` is `spec.replicas` |
| `.key` | the member `key` of a mapping | `spec.template.spec.nodeName`, `agent.node-label` |
| `['key']` or `["key"]` | a member whose key holds a dot, a space or another character `.key` cannot | `metadata.labels['app.kubernetes.io/name']` |
| `[n]` | element `n` of a sequence, counted from 0; a negative `n` counts from the end | `spec.containers[0].image`, `args[-1]` |
| `[*]` | every element of a sequence, or every member of a mapping, in file order | `spec.containers[*].image` |
| `[?@.key == 'value']` | every element of a sequence, or every member of a mapping, in file order, whose `key` equals the literal (see Filters) | `spec.containers[?@.name == 'api'].image` |

A key after a dot starts with a letter, `_` or a non-ASCII character, and goes on with those, digits
and `-`. The `-` is the extension: RFC 9535 does not allow it, and without it a key such as
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

#### Filters

A filter selects by a child's value, so a rule names a container by its name, wherever it sits in
the list:

```yaml
extract:
  api_image:
    yaml: {file: deploy/api.yaml, path: "spec.template.spec.containers[?@.name == 'api'].image"}
  prod_api:
    yaml: {file: deploy/api.yaml, path: "spec.template.spec.containers[?@.name == 'api' && @.env == 'prod'].image"}
```

It is RFC 9535's filter selector cut down to equality. Inside `[?...]` stand one or more
comparisons joined by `&&`, and every one must hold. A comparison is a path from `@`, the element
or member being tested, then `==`, then a literal; the literal may come first. The path after `@`
takes `.key`, `['key']` and `[n]` steps, or none, so `ports[?@ == 80]` tests the element itself.
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

A filter joins at most 64 comparisons, and a literal or a key after `@` holds at most 2048 bytes
(a string as its escapes decode it, a number as written). A path past either does not load. The
filters of one path may cost at most 4000000000 bytes: for each member tested, each comparison
and each step after `@` counts 256, plus the bytes of each key it looks up and of the literal it
compares. A path that would cost more makes the rule unknown, and the message names the limit.
At the limit a filter takes up to about 1.5 s.

Every other filter construct does not load, and the message names it and its column: `||`, `!`,
`!=`, `<`, `<=`, `>`, `>=`, parentheses, functions such as `length()`, `$` inside a filter, two
paths or two literals compared, a test that a path exists (`[?@.name]`), `[*]` after `@`, and a
filter inside a filter:

```text
path "containers[?@.name != 'api']": column 20: operator != is not supported in a filter; only == is
```

#### Values

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

#### Values a file only computes

Some files hold a value that only running them produces: `$(git describe)` in a shell script,
`${{ inputs.tag }}` in a CI file, a reference or a function call in HCL or a Makefile. A reader
for such a file marks the value as not literal. Its text is what the file wrote, not the value the
file means, so a rule never compares it. Each extractor's documentation says whether it can return
a not literal value. Of errata's kinds only `ini` can, for the files and sections that
[INI files](#ini-files) lists, and `properties` for a template file; a `${{ }}` read with `yaml:`
is still plain text, and two workflows that use the same expression agree.

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

`skip_not_literal` may list only an extractor that can return a not literal value: for any other
the key would load and leave nothing out, so it does not load. That holds for every kind but `ini`
and `properties`, for an `ini` extractor whose file errata models no transform for, such as
`pytest.ini`, and for a `properties` file that is not a template. The message names the kind, and
for `ini` the dialect and the file:

```text
errata: load errata.yaml: checks[0]: check "pytest-agrees": skip_not_literal names "opts", whose kind of extractor, ini, plain dialect on pytest.ini, returns only literal values, so it has nothing to leave out
```

Nor does a list with no name, a name the rule does not declare, a name given twice, or anything
but a list.

The text output names each value left out under the coverage line, for a rule that holds as for
one that does not, since a rule that holds held only on what was left. JUnit names them in the
case's `<system-out>`. SARIF names them in every case: an erratum links each as a related location
whose message says it is not literal and was left out, and an unknown, or a rule that holds with a
value left out, lists each as a location of its notification.

#### Several YAML documents

A file without `kind` or `name` must hold at most one document. Empty documents, such as the one a
trailing `---` makes, do not count. With `kind`, `name` or both, errata keeps each document whose
top-level `kind` and `metadata.name` equal them, and returns the values from all of them. A
selector that matches no document, and a file of several documents without a selector, make the
rule unknown with a message that says which.

#### Regular expressions

`pattern` is Go's [RE2 syntax](https://github.com/google/re2/wiki/Syntax) and needs a capture
group; a pattern without one, or with more than 32, does not load. Write `(?:...)` for a group
that need not capture. The `m` flag is on, so `^` and `$` match at each line;
`(?-m)` at the start turns it off. `.` does not match a newline unless the pattern sets `(?s)`. A
CRLF line ending reads as LF, so `$` matches before it and no value ends in its `\r`; a lone `\r`
is kept. Each value is at the line where its match starts, counted in the file as it is. A match in
which group 1 took no part gives no value.

#### Markdown prose

A sentence in a README breaks across lines and carries backticks and emphasis, so a `regex` over
the raw file misses `CI runs on node` followed by `` `builder` `` on the next line. The `text`
extractor parses the file as CommonMark and matches `pattern` against its prose: the text of its
paragraphs, headings and list items, spaced as a browser shows it.

| Field | Required | Meaning |
|---|---|---|
| `file` | yes | the Markdown file |
| `pattern` | yes | an RE2 pattern with a capture group, as for `regex`; group 1 is the value |
| `code` | no | `true` reads code blocks, HTML blocks and front matter as well; default `false` |
| `max_bytes` | no | the largest file read (see Limits) |

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
- An HTML block is left out. A line starts one when it starts with a comment, with a block tag
  such as `<div>` or `<p>`, or holds one tag and nothing else. The block runs to the next blank
  line for a tag, and to its end for a comment, a `<script>`, `<pre>`, `<style>` or `<textarea>`
  element, a processing instruction, a declaration or CDATA. For those last kinds, the text after
  the end on its line is prose, as a browser shows it: `<!-- marker --> CI runs on node builder`
  reads `CI runs on node builder`. `<!-->` and `<!--->` are whole comments. Tags and comments in
  that text follow the rule above.
- Fenced and indented code blocks are left out, and so is front matter: a block that starts the
  file (after a UTF-8 byte order mark, if any) with a line of `---` and ends at a line of `---` or
  `...`, or starts and ends with `+++`. Without its closing line it is no front matter.
- With `code: true`, code blocks, HTML blocks and front matter are in, each line as the block
  holds it, without its fences or the markers and indent of the blocks around it.
- A backslash escape and an entity such as `&amp;` stay as written.
- CRLF line endings read as LF.
- errata runs no Markdown extensions, so a GitHub table is a paragraph whose rows are joined with
  spaces, pipes and delimiter row included: `runs on \| (\w+)` reads a cell.

The value is the prose's text of group 1: `node *builder*` wrapped over two lines gives
`node builder`. Its line is the line of the file where the match starts.

goldmark, the parser, is quadratic in the file on some shapes of input, such as an unclosed HTML
comment on every line, and nothing outside it can stop it. errata looks at the clock, and counts
what the parse has built, on every call goldmark makes into its parsers, and refuses a file past
any of the bounds under Limits. Each refusal makes the rule unknown and names the bound.

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
the CR line ends of classic Mac OS reports the lines an editor shows. Every other kind numbers
lines at LF alone, even the INI dialects, which end a line at a lone CR too.

errata reads no DTD. A `<!DOCTYPE>` is skipped, its external parts are never fetched, and an
entity it declares is never expanded: a reference to one, internal or external, makes the rule
unknown. That keeps out both an external entity that would read a file or a URL (XXE) and entities
that expand to gigabytes (billion laughs). The five predefined entities and character references
are read.

A file in UTF-8 is read, with or without a byte order mark. A file that declares US-ASCII,
ISO-8859-1, ISO-8859-15 or windows-1252 (or the labels `ascii` and `latin1`) is read when every
byte is ASCII, since it then reads the same as UTF-8. With any other byte, and in any other
encoding, UTF-16 too, the rule is unknown.

#### INI files

`ini` reads a file as the tool that reads it does. `dialect` names the tool; left out, the
file's name chooses:

| Dialect | Chosen for | Reads as |
|---|---|---|
| `git` | `.gitconfig`, `.gitmodules`, `gitconfig`, a name ending in `.gitconfig`, `config` in a directory named `.git` or `git` | git's own parser (git 2.47) |
| `ansible` | `hosts`, `hosts.ini`, `inventory`, `inventory.ini`, any file under a directory named `inventory` or `inventories` | ansible-core's ini inventory plugin (2.21) |
| `tox` | `tox.ini` | tox 4 |
| `php` | `php.ini` and a name that starts with `php.ini`, such as `php.ini-production`, `php.ini-development` and `php.ini.dist`, but not a template of one such as `php.ini.j2` or `php.ini.tpl`; and a name ending in `.ini` under a directory named `php` | PHP 8.4, in the mode PHP reads php.ini in |
| `plain` | every other file: `setup.cfg`, `pytest.ini`, `mypy.ini`, `.flake8` | Python's configparser |

```yaml
extract:
  deps:
    ini: {file: tox.ini, section: testenv, key: deps}
  memory:
    ini: {file: php.ini, section: PHP, key: memory_limit}
  web1:
    ini: {file: inventory/hosts, section: web, host: web1, key: ansible_host}
```

A `dialect` that is not one of these five does not load. Neither does `subsection` outside git,
`host` outside ansible, or a spec without `key`, except in ansible, where the table below says
when `key` is left out. git and ansible also need `section`. In plain and php, leaving
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

- In php.ini: a boolean word such as `On` or `none`, a constant such as `E_ALL` (a word named as
  PHP's constants are; `UTC` is text), a `${}` reference and an expression.
- In tox.ini: a value with a factor condition (`py311: mock`) or a substitution (`{posargs}`,
  `{env:X}`, `[]`). In setup.cfg's `[tox:tox]`, `[testenv]` and `[testenv:name]`, which errata
  reads as plain configparser does, also a value with a comment or a joined line, which tox drops
  or joins.
- In setup.cfg: a value with a `%`, and one that starts with an `attr:` or `file:` directive.
- In mypy.ini, .mypy.ini and the `[mypy]` and `[mypy-*]` sections of setup.cfg: a path option,
  such as `mypy_path`, `files` or `cache_dir`, with an entry that starts with `~` or a `$`
  variable anywhere, and a `files` entry with a glob (`*`, `?` or `[`).
- In .coveragerc and the `[coverage:*]` sections of setup.cfg and tox.ini: a `$$`, a `$name` or a
  `${`.
- In an ansible inventory: a host with a range or a port (`web[01:20]`, `db1:2222`), a number
  Python writes otherwise (such as `0x10` or `1+2j`), a container such as `[80, 443]`, and a
  Jinja2 template (`{{`, `{%` or `{#`).
- In a template file of any dialect, and a properties template, named with `.j2`, `.jinja`,
  `.jinja2`, `.tmpl`, `.tpl`, `.tftpl` or `.erb` at the end: a value holding `{{`, `{%` or `{#`,
  `<%` in ERB, and `${` or `%{` in Terraform's `.tpl` and `.tftpl`.

git, properties and other plain files, such as pytest.ini and .flake8, return only literal values,
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
expression of `|`, `&`, `^`, `~`, `!` and parentheses. `key[] = v` is the key `key`. A key with no
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
a string: `8080`, `True` and `[1, 2]` are the text written. A number Python writes otherwise,
such as `0x10`, and a container, such as `[1, 2]`, are marked not literal, since ansible uses the
value and not the text. A `#` comment after a literal goes, as
Python drops it, so `x = 8080 # http` is `8080` and `x = [1, 2] # c` is `[1, 2]`. A string
literal is the text Python makes of it: `'x\ty'` has a tab, `'a' 'b'` is `ab`, `b'\x41'` is `A`,
and `r'a\tb'` keeps its backslash. A string errata cannot decode exactly, one with `\N{...}` or a
surrogate escape, makes the rule unknown. A value that is no literal is the text written, a `#`
in it too, as in `pool.org # c` and `f'x'`. A byte that is not UTF-8 is kept, and so is the value
that holds it. errata does not check that a group a vars or children section names is declared.

#### Java properties

`properties` reads a file as `java.util.Properties.load` does: `=`, `:` or white space between
key and value, `\` at the end of a line to continue it, `\uXXXX`, `\t`, `\n`, `\r` and `\f`
escapes, and `#` and `!` comments. A `\` before any other character is dropped. The value is at
the line where its key starts. `key` is the key after its escapes are read, matched as text, so
`server.port` is the key `server.port`. Java keeps the last value of a key set twice; errata
returns every one. A `\u` without four hex digits makes the rule unknown, as Java throws. The
file is read as UTF-8, as `load(Reader)` reads it, and a sequence that is not UTF-8 is U+FFFD,
as Java's decoder makes it. A surrogate pair written as two escapes is its one character; a
surrogate alone is U+FFFD. Lines are numbered at `\n`, as every kind but `xml` numbers them, so
a file whose lines end in a lone `\r` is one line long. Spring's `application-<profile>.properties`
files are plain files: name the one to read, errata does not merge profiles. errata reads the
value Java reads, so every value is literal, but in a template file, as [INI files](#ini-files)
says.

#### Limits

A file in a pull request can be large or hostile, and an extractor must not stall or kill the run
over it. errata enforces the limits below on what it reads: the file size, the nodes a file can
hold, the nodes YAML aliases add, the values, the depth, a pattern's capture groups, the regex time,
the work of a path's filters and an XML path's terms, what a Markdown parse may build and how long
it may run, and the work of an INI or properties read. It does not bound the memory a read takes.
Peak memory depends on the file's shape within those limits, and the figures below are the densest
shapes measured so far, as examples, not as a ceiling; a file that needs more memory than the runner
has kills the run, which fails it rather than passing it. Every pass errata makes over a file takes
time in proportion to its size, but for the regex search, the Markdown parse and a path's filters,
so the size cap bounds the cost of a read, and the other limits bound what a small file can make of
itself. A regex search costs the size of the file times the threads its pattern keeps alive, which a
counted repeat such as `{0,1000}` makes many, so the regex time limit bounds it. Each limit fails
closed: a file over it makes the rule unknown, with a message that names the limit, the file and
what to do. Times and memory below were measured on a loaded 4 core machine.

| Limit | Value | Why |
|---|---|---|
| file size | 32 MiB, or `max_bytes` | checked before reading. An ordinary file costs about 27 times its size in memory: an 8.2 MB OpenAPI document takes 0.95 s and 223 MB |
| nodes | `max_bytes` divided by 7 (YAML), 3 (TOML), 8 (JSON) or 3 (XML) | checked before parsing, since the YAML and TOML parsers build a whole document or expression before errata sees it. errata counts in one pass an upper bound on the nodes the parser can build: two for each byte after which a key, a value or an item can start, with YAML's NEL, LS and PS line breaks among them, and a UTF-16 YAML file counted as the UTF-8 the parser reads. A file of one-character items counts one for each byte, an ordinary file one for every 3 to 16: a 6.3 MB Helm index has 6 times room at the default, a 7.9 MB uv.lock 4.8 times, a 7.4 MB Google API document 8 times. The limit bounds nodes, not memory, since a node costs more or less by its shape. The densest files measured so far at the default peak at 2.0 GB in YAML (mappings of 62 anchored, tagged keys), 2.55 GB in TOML (inline tables of dotted keys of 500 parts) and 0.42 GB in JSON (objects nested 900 deep), and grow in proportion to `max_bytes`; 32 MiB of one-character items ran out of 4.7 GB in YAML without the limit. Raise `max_bytes` to read a denser file. For XML errata counts each element, attribute, namespace declaration and text node as it builds them from encoding/xml's tokens, since it builds the tree itself. The densest of 39 real XML files, MARC library records, holds a node for every 11.5 bytes, almost 4 times the room at the default. The densest file XML can write, text between empty elements (`x<a/>`), reaches the limit at 28 MB, which takes 1.0 GB; 32 MiB of empty elements takes 1.3 GB. An attribute costs more memory than an element, since encoding/xml builds a whole start tag before errata counts it: the densest XML measured so far, one start tag of 8.4 million attributes in 32 MiB, peaks at 1.94 GB before it is refused |
| values per extractor | 100000 | every value is kept in memory and printed; a regex `(\d*)` matches at every byte. A path that selects more nodes at any step stops there. The values of one extractor also hold at most `max_bytes` bytes in all, since YAML aliases can repeat a value, decoding can lengthen one, and an XML element's value holds the text of every element inside it: `//a` over 1000 nested elements around 30 MB would return 30 GB, and is refused. 100000 regex matches in 4.3 MB take 0.8 s and 45 MB |
| depth | 1000 | each key, each part of a dotted TOML key or table header, and each element is one level, and an array table's element one more. The deepest of 56 real files measured nests 25 |
| YAML aliases and merge keys | 1000000 nodes | each alias adds the nodes of its anchor, and so does each mapping a merge key names, counted for the whole file before anything is built from them. A key counts by its length too: each 64 bytes of a key that a merge key copies count as one node more, and an alias used as a key counts one node and one more for each 64 bytes of its anchor's text, so a file that merges or repeats long keys many times reaches the limit instead of taking minutes. errata keeps one copy of an anchor, so 5000 mappings that merge one of 99 keys, at the limit, take 0.15 s and 51 MB |
| regex time | 10 s for each 32 MiB of `max_bytes`, and never less than 10 s | the search looks at the clock every 256 characters it reads, so one long search is stopped too. A pattern whose optional tail scans the rest of the line, such as `(\d)(?:.*x)?`, searches the line again after every match: 40 KB of one line took 27 s. One search that finds nothing costs the file times the pattern's width: `([0-9a-f]{40})` over 32 MiB of hex words took 28 s, and `([ab]{0,1000}c)` over 1 MiB 62 s; each is refused at 10 s. The slowest ordinary pattern measured over 32 MiB, `([A-Za-z0-9+/]{64,}={0,2})`, takes 4.7 s, and a pattern that starts with a literal about 2 s |
| regex capture groups | 32 | a search tracks every group for every thread it keeps alive; 100000 matches of a pattern of 32 groups take 0.5 s and 29 MB |
| filter work | 4000000000 bytes for the filters of one path | for each member tested, each comparison and each step after `@` counts 256, plus the bytes of each key it looks up and of the literal it compares, counted before the members are tested. At the limit, with 64 comparisons on each member, the worst shapes measured take 0.36 s to 1.42 s |
| XML path terms | 32 steps and predicates | each step reads at most every node of the file once, and each predicate the nodes its step keeps, so a path costs the file's nodes times its terms. 32 `//` steps over 8.4 million elements take 2.4 s after the parse. A comparison is counted against the filter work: 256 for each child or attribute it looks at, and the literal's length for each value of the same length it compares |
| Markdown parse time | 30 s for each 32 MiB of `max_bytes`, and never less than 30 s | goldmark is quadratic on some shapes and cannot be stopped from outside, so errata looks at the clock on every call goldmark makes into its parsers. 32 MiB of express's changelog, repeated, parsed in 15 s on a loaded 4 core machine. Of 39 hostile shapes at 32 MiB, each was read or refused at most 0.61 s after the limit |
| Markdown nodes | `max_bytes` divided by 4 | counted as the parse builds them: three for each inline node, two for each line, one for each block and each emphasis. The aws-sdk-go-v2 changelog of 2.9 MB makes the most of 5962 real Markdown files, 321763, 4% of the limit at the default |
| Markdown open blocks from line to line | `max_bytes` divided by 4 | goldmark records each block other than a paragraph that goes on to the next line and makes no node for it: 32 MiB of lines of 100 block quotes made 33 million and took 3.3 GB without the limit. The most in a real file is 56921 |
| pieces of the prose | `max_bytes` divided by 4 | a run of the file's bytes, or a space or newline errata writes. A run of blanks rewritten as one space starts two, so 32 MiB of a letter and a tab took 3.3 GB without the limit. The most in a real file is 92147 |
| Markdown nesting | 1000 levels | the text of a paragraph in 998 block quotes is at the limit |
| runs of `*` and `_` in one paragraph | 5000 | goldmark matches each closing run against every run open before it, so a paragraph costs the square of its runs: 5000 take 0.5 s. The most in a real paragraph is 797 |
| link reference definitions | 10000000, the definitions at the start of a paragraph times its lines | goldmark copies the paragraph's lines once for each definition it takes out: 3162 definitions, at the limit, take 0.08 s. The most in a row in a real file is 343. Put blank lines between the definitions |
| a line of an HTML block | 1 MiB | goldmark matches such a line against up to seven regular expressions in calls errata cannot stop: one took 20 s over a line of 32 MiB, about 1 s at the limit. The longest in a real file is 1801 bytes |
| prose | twice the size of the file | the regex time limit, which scales with the file, then holds for the prose too |
| ini and properties work | 128 units for each byte of the file and of the spec's names, and 1048576 more | each read makes one pass over the file and keeps only the values it selects, so it needs no node limit. Every loop that reads a line, compares a name or matches an ansible host against a range charges the budget; a unit is 0.3 to 2.6 ns on the dense shapes measured, so a read that spends the whole budget at 32 MiB (about 4.3 billion units) takes 1.5 s to 11 s. Real files spend 2 to 8 units a byte. The slowest shape that reads at 32 MiB takes 3.8 s (tox, a key of each letter in each of a million sections). The worst shape found, lines of ten 17 digit ranges against a spec host of 1024 bytes, is refused at the budget in 5.9 s, where it would have run for about 350 s; a range bound of 4 million digits is refused in 0.1 s, and php fallbacks nested 131000 deep in under 0.01 s |
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

#### When a rule is unknown

A file that is missing, does not parse or is over a limit makes the rule unknown. So does a YAML or
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

An INI or properties file makes the rule unknown for what its tool refuses, as the sections on
them say. A Markdown file parses whatever it holds, so only a bound under Limits refuses one.

### The check protocol

| Exit code | Meaning |
|---|---|
| 0 | the claim is true |
| 1 | the claim is false |
| anything else, or a timeout | unknown: the check could not evaluate. Not an erratum, but it fails the run |

Stdout is the message. If stdout is a JSON object with a `message` string, errata uses that string.
The program runs with the repository root as its working directory and receives `ERRATA_ROOT`,
`ERRATA_CHECK_ID` and `ERRATA_SCOPE` in its environment.

### Scopes

| Scope | Question | Needs |
|---|---|---|
| `repo` | Is this claim true of the files? | nothing |
| `live` | Is this claim true of the running system? | read access (not built yet) |
| `applied` | Will this claim be true after an apply? | read access and a dry run (not built yet) |

errata never applies anything. Until a provider for a running system exists, `live` and `applied`
checks are skipped by name, and the coverage line gives the gap for each.

### Command line

```text
errata check [--root DIR] [--config FILE] [--format text|junit|sarif]
errata version
```

| Flag | Default | Meaning |
|---|---|---|
| `--root` | `.` | the repository root, and the checks' working directory |
| `--config`, `-c` | `<root>/errata.yaml` | the config file |
| `--format`, `-f` | `text` | `text`, `junit` or `sarif` |

| Exit code | Meaning |
|---|---|
| 0 | every check that ran found its claim true |
| 1 | at least one erratum (a claim shown false) or unknown (a check that could not evaluate) |
| 2 | bad flags, or a config that does not load |

The text output writes every control character that a file or a program's output holds, such as
ESC or a CR, as its escape (`\x1b`, `\r`), and every byte that is not UTF-8 as `\xNN`; a CR
before a newline is dropped. The direction controls (U+061C, U+200E, U+200F, U+202A to U+202E and
U+2066 to U+2069) and the line and paragraph separators U+2028 and U+2029 are written as escapes
too (`\u202e`). An error errata prints on stderr, which can quote the config file, goes through
the same escapes. No file in the repository can clear the terminal, overwrite a line, reorder one
or set a link in it. JUnit and SARIF, which also go to stdout, write DEL, the C1 controls and the
direction controls as character references (`&#x9B;`) and as JSON escapes (`\u009b`), and a
reader of either format gets the text back as it was.

### In CI

errata has no integration with any CI system, on purpose: it is one binary that exits non-zero and
writes JUnit or SARIF. GitLab CI:

```yaml
errata:
  image:
    name: fklement/errata:latest
    entrypoint: [""]
  script:
    - errata check --root "$CI_PROJECT_DIR" --format junit > errata.xml
  artifacts:
    when: always
    reports:
      junit: errata.xml
```

GitHub Actions:

```yaml
jobs:
  errata:
    runs-on: ubuntu-latest
    container: fklement/errata:latest
    steps:
      - uses: actions/checkout@v5
      - run: errata check --root "$GITHUB_WORKSPACE"
```

### SARIF for GitHub code scanning

`--format sarif` writes [SARIF 2.1.0](https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/sarif-v2.1.0-errata01-os-complete.html),
the format GitHub code scanning reads. Uploaded, each erratum becomes an alert on the line that
holds the value that disagrees. A pull request shows the alert inline only when that line is in its
diff; otherwise the alert is in the repository's code scanning list.

```yaml
jobs:
  errata:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      security-events: write
    steps:
      - uses: actions/checkout@v5
        with:
          persist-credentials: false
      - run: docker run --rm -v "$PWD:/repo" fklement/errata:latest check --root /repo --format sarif > errata.sarif
      - uses: github/codeql-action/upload-sarif@v4
        if: always()
        with:
          sarif_file: errata.sarif
          category: errata
```

The errata step still exits 1 on an erratum or an unknown, so the job fails; `if: always()` uploads
the file anyway. errata runs with `docker run` instead of as the job's `container:`, so
`upload-sarif`, a JavaScript action, runs on the runner and not in the errata image, which holds
only errata and bash. `persist-credentials: false` keeps the job's token, which can write code
scanning results, out of `.git/config`, where every program check in the mounted checkout could
read it.

What goes where in the file:

| errata | SARIF |
|---|---|
| a check | a rule in `tool.driver.rules`, with the check's id, and its reason as the full description and the help text |
| an erratum | a result, at the location of the first value that disagrees, with links `[value 1](1)`, `[value 2](2)` and so on to every other value, then to each value `skip_not_literal` left out |
| an unknown | an `error` notification in `invocations[0].toolExecutionNotifications`, with the message and the reason, and each value `skip_not_literal` left out as a location; `executionSuccessful` is then false |
| a skipped check | a `note` notification with its gap, so the coverage is in the file |
| a check whose claim holds | its rule, and no result |
| a check whose claim holds with values left out | its rule, no result, and a `note` notification that counts the values `skip_not_literal` left out and lists each as a location |
| errata itself | `tool.driver`, with errata's version as `version` |

Code scanning does not display notifications. An unknown or a skip shows on GitHub only as the
failed job; the file holds them, and `errata check` in the text format prints them. A clean code
scanning tab is therefore not a clean run: read the job's result.

For an agree rule, errata groups the values by their normalised text and takes the largest group
as the majority. The result points at the first value outside the majority and links every other
value as a related location. Values come in the order `agree:` names their extractors, so on a tie
the first name in `agree:` counts as right. With two values that differ, the alert is on the second
one. List the source of truth first. An assert rule has no majority, so its result points at the
first value it extracted. A program check reports no values, so its result points at the line where the check starts
in the config file, and so does a rule whose extractors found nothing; the message is what the
program printed.

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

## Local Development

With Go 1.27 or later:

```bash
make tools    # golangci-lint, gremlins, govulncheck and goreleaser into ./bin, at pinned versions
make build    # static binary at ./errata
./errata check
```

### Testing

Tests sit beside the code they test, as Go expects. `make check` is the whole gate, and every
layer in it catches a different kind of defect:

| Command | What it runs |
|---|---|
| `make test` | unit, property and API tests, with the race detector and shuffled order |
| `make cover` | the same, failing below 90% statement coverage |
| `make slow` | the tests too slow to run under the race detector or once per mutant, run once; it fails when one of them does not run and pass |
| `make fuzz` | the fuzz targets for the config parser, the check protocol, the JUnit and SARIF writers, assert, the path grammar and each extractor |
| `make mutation` | Gremlins: changes one operator at a time and fails if the tests still pass, each package judged alone against its floors in `scripts/mutation.sh`. `MUTATION_SCOPE="internal/config internal/cli"` mutates those packages only, and limits `make fuzz` to their targets |
| `make lint` | golangci-lint with the linters listed, and justified, in `.golangci.yml`, once for each of linux, windows and darwin |
| `make vuln` | govulncheck over the code paths errata actually calls |
| `make dogfood` | errata checks this repository |
| `make release-dry` | every release binary, built locally, uploaded nowhere |

The container image: `docker build -t errata .` builds it from the [Dockerfile](Dockerfile).
Releases build the same image with [ko](https://ko.build) from the `kos:` section of
`.goreleaser.yaml`, and attach an SPDX SBOM per binary to the GitHub release; CI checks that both
recipes use the same base images. `make release-dry` builds the release binaries without publishing.

The API tests in `internal/cli/testdata/script` run the real binary, so they pin the output and
the exit codes that CI depends on. [docs/architecture.md](docs/architecture.md) says what each
layer is for.

### Dependency security

Every tool is installed at a pinned version, and every base image is pinned by digest. govulncheck
runs in the gate. Every night the published image of the last release is scanned with Trivy, and a
HIGH or CRITICAL finding with a fix produces a patch release once the full gate passes on the bump.

## Architecture

Each package has one job, and the domain types import nothing else:

| Package | Job |
|---|---|
| `internal/check` | the domain: check, rule, extractor, value, location, assertion, scope, verdict, result, report |
| `internal/config` | reads `errata.yaml`, strictly, and builds each rule's extractors and assert |
| `internal/engine` | decides which checks run and which are skipped |
| `internal/protocol` | runs a check program and turns its exit code into a verdict |
| `internal/rule` | evaluates a rule: runs its extractors, compares their values or evaluates its assert in CEL |
| `internal/extract` | builds the extractors a rule names and reads their values from files, one reader package beneath it for each kind |
| `internal/provider` | the interface for running systems, and the reconciler model |
| `internal/report` | the text, JUnit and SARIF outputs |
| `internal/cli` | flags, wiring and exit codes |

[docs/architecture.md](docs/architecture.md) explains the seams between them and the choices
behind each one.

## Contributing

Bug reports, questions and patches are welcome. **For anything larger than a small fix, open an
issue first**, because the reason behind a surprising choice is usually written down, and reading
it is cheaper than rediscovering it.

Run `make check` before proposing a change. It needs no credentials and no network beyond the Go
module proxy.

A few conventions are load bearing, and a change that ignores them will be sent back:

- **Use the words in [GLOSSARY.md](GLOSSARY.md)**, in code, output and documents, and no synonyms
  for them.
- **Every check has a reason**, in errata's own suite as in any other.
- **A change to output or exit codes changes the API tests** in `internal/cli/testdata/script`.
  Those tests are the contract CI users rely on.
- **Comments say why, not what.** A comment that guards a trap says what breaks if it is removed.
- **No dashes as punctuation.** Use a colon, a comma or a full stop. errata's own suite enforces
  this, so a dash fails `make check` rather than a review.

## License

MIT, see [LICENSE](LICENSE).
