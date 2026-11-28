# The plugin protocol

This is versions 1 and 2 of the protocol errata speaks with a plugin: an executable that provides
one extractor kind, the provider operations of one kind of live system, or both, released on tags
of its own. v0.5.0 builds the host that speaks it; no
release of errata runs a plugin yet. [ADR 0020](adr/0020-a-plugin-is-an-executable-errata-speaks-to-over-stdin-and-stdout.md)
says why a plugin is an executable spoken to over stdin and stdout, and not a Go plugin or a
WebAssembly module.

Protocol 1 is for offline extractor plugins, which read only the files errata sends them. It
gives a plugin no network and no credentials. Protocol 2 adds the provider operations render, read
and predict beside extract, and gives read and predict, and nothing else, the environment
variables the plugin names, such as `KUBECONFIG`, so they can reach a cluster or a host
([ADR 0023](adr/0023-a-provider-is-a-plugin-that-speaks-protocol-2.md)). Everything this document
says of protocol 1 holds in protocol 2 unless [Protocol 2](#protocol-2) says otherwise.

## Contents

- [The plugin](#the-plugin)
- [How errata runs a plugin](#how-errata-runs-a-plugin)
- [The encoding](#the-encoding)
- [describe](#describe)
- [build](#build)
- [extract](#extract)
- [Values](#values)
- [What errata checks of a reply](#what-errata-checks-of-a-reply)
- [Bounds](#bounds)
- [Protocol 2](#protocol-2)
- [Versions](#versions)

## The plugin

A plugin is an executable named `errata-ext-<name>`, with `.exe` after it on Windows. It
provides the extractor kind whose key is `<name>`: a rule's entry `csv:` under `extract:` is read
by `errata-ext-csv`. A name is 1 to 64 bytes of lower case ASCII letters, digits, `_` and `-`,
starting with a letter, and is not the key of a kind errata has built in; a plugin of such a name
is refused, since the built in kind would read its entries.

errata runs a plugin from its own plugin cache or from the repository's vendored plugin
directory, at the version and checksum the lock file pins, and never looks one up on `PATH`.
[Plugins](reference.md#plugins) in the reference says where those are.

An entry of a plugin's kind is written as any other extractor:

```yaml
extract:
  ports:
    csv:
      file: deploy/services.csv
      column: port
      where: {name: api}
```

`file`, `files` and `max_bytes` are errata's: it resolves them, reads the files and sends their
bytes. Every other key is the plugin's, and the plugin declares each one (describe, below), so
errata decodes the entry as strictly as a built in kind's: a key the plugin does not declare, a key
that differs from a declared one only in case, and a value YAML 1.1 would read as another type do
not load.

## How errata runs a plugin

Each request is one run of the plugin, started through the function that starts every program
errata runs:

- **argv.** The executable alone, with no argument. The request says what to do.
- **stdin.** One JSON request, then end of file.
- **stdout.** One JSON reply. Nothing may follow it but white space.
- **stderr.** For people. errata reads it only for a message: when the plugin fails, the message
  ends with stderr's last lines, cut at 256 bytes.
- **Exit status.** 0 when the plugin wrote a reply. Any other status, or death by a signal, makes
  the request fail, whatever stdout holds.
- **Working directory.** An empty directory errata makes for the request and removes after it.
  The plugin is handed the bytes of its files and has no reason to open one.
- **Environment.** Only the variables errata passes, and no other of errata's environment:
  `GOMAXPROCS`, `GOMEMLIMIT` and `GOGC` when errata has them, so an operator can tune a plugin
  written in Go, and on Windows `SYSTEMROOT`, which the system's libraries need. A CI job's
  tokens, `HOME` and `PATH` do not reach a plugin.
- **Process group.** On Linux and macOS the plugin runs in a process group of its own, which errata
  kills on a timeout, on the output bound, on a cancelled run, and once the plugin exits, so a
  child it started does not outlive the request, unless the child leaves the group, as `setsid`
  does, which errata cannot stop. On Windows errata starts no process group.

A plugin reads its request and writes its reply. It does not open files, start programs or use
the network, and it keeps nothing from one request to the next. errata cannot enforce this on an
executable; a plugin that does any of it is a defect of that plugin.

## The encoding

Requests and replies are JSON objects (RFC 8259) in UTF-8. Bytes, such as a file's content, are
a JSON string of standard base64 with padding (RFC 4648, section 4), which is how Go's
`encoding/json` writes a `[]byte`. Text, such as a value, is a JSON string, and must be valid
UTF-8: a plugin cannot return text that is not, and errata refuses a reply that holds it. A file's
bytes may be anything, since they travel in base64. A value errata sends in an extract request's
`values` that is not valid UTF-8, as a built in reader may return one (the ansible inventory
reader keeps such a byte), makes the rule unknown before the plugin starts, with a message that
names the value's place: JSON would carry it only with each bad byte replaced, and the plugin would
read text the file does not hold.

Each request names the protocol version it is written in, as `"protocol": 1`, except a describe
request, which comes before a version is chosen. errata sends only the members this document
defines for that version, and refuses a reply that holds a member it does not define, or one of
the wrong JSON type, since a member errata skipped might change what a value means. A plugin
should refuse a request that holds a member it does not know in the same way. A member is named
as this document writes it, in the same case, and written once: errata refuses a reply that names
one in another case, such as `Values`, or writes one twice. A member this document marks as
optional may be left out; `null` is no value for any member.

## describe

errata asks a plugin to describe itself once per run, before it reads the config's entries of
the plugin's kind:

```json
{"op": "describe", "protocols": [1, 2]}
```

`protocols` lists the versions of this protocol that errata speaks, lowest first. The plugin
chooses one of them, the highest it speaks, and replies:

```json
{
  "protocol": 1,
  "name": "csv",
  "version": "0.1.0",
  "where": "files",
  "fields": [
    {"name": "column", "type": "string", "required": true},
    {"name": "where", "type": "map"},
    {"name": "header", "type": "bool"},
    {"name": "dialect", "type": "string"},
    {"name": "delimiter", "type": "string"}
  ]
}
```

| Member | What it is |
|---|---|
| `protocol` | the version the plugin chose, one of `protocols` |
| `name` | the plugin's name, which must be the `<name>` of the executable errata ran |
| `version` | the plugin's release, as semantic versioning writes it; errata names it in messages |
| `where` | `file` for a kind that reads one file, which takes `file` and `max_bytes`; `files` for a kind that also reads a file set, which takes `files` too |
| `fields` | the keys an entry of the kind takes besides errata's, in the order `errata list` prints them |

Each field has a `name`, of lower case ASCII letters, digits and `_`, starting with a letter and
none of `file`, `files` and `max_bytes`; a `type`; and an optional `required`, false when left out.
The types are:

| Type | In the config | In a request |
|---|---|---|
| `string` | a string | a JSON string |
| `int` | an integer | a JSON number with no fraction or exponent, within 64 bits |
| `bool` | `true` or `false` | `true` or `false` |
| `strings` | a list of strings | a JSON array of strings |
| `map` | a mapping of strings to strings | a JSON array of `{"key": k, "value": v}` objects, in the config's order |

Five members are the same in every version of the protocol, so that a plugin and an errata of
different versions can always find out which, if any, they share: `op` and `protocols` of the
describe request, and `protocol`, `name` and `version` of its reply. Every other member of the
reply, and the values each takes, such as the types of a field, are the chosen version's, so a
later version may add a member to describe. A plugin that speaks none of the versions offered
replies with the versions it does speak instead:

```json
{"protocols": [3, 4], "name": "csv", "version": "0.4.0"}
```

## build

errata sends every entry of the plugin's kind in the config in one build request when it loads
the config, after it has decoded each against the declared fields:

```json
{
  "protocol": 1,
  "op": "build",
  "entries": [
    {"rule": "api-port-matches", "spec": {"column": "port", "where": [{"key": "name", "value": "api"}]}}
  ]
}
```

`rule` is the id of the rule the entry belongs to. `spec` holds the plugin's fields as the entry
writes them, without errata's `file`, `files` and `max_bytes`, and without a field the entry
leaves out. The plugin checks each spec, as a built in kind's `Build` does, and replies with one
result for each entry, in the same order:

```json
{
  "built": [
    {
      "spec": {"column": "port", "where": [{"key": "name", "value": "api"}], "header": true, "dialect": "csv", "delimiter": ","},
      "description": {"label": "csv", "not_literal": false, "shape": "flat", "fields": [], "reads": []}
    }
  ]
}
```

A reply with a result for fewer or more entries than the request sent is refused, and every rule
with an entry of the kind is unknown. `spec` is the spec with every default the plugin fills in
written out, which `errata list` prints with errata's own keys beside it. It is also the entry's
place: two extractors of a rule whose `spec` is equal, with errata's `file` or `files` cleaned and
`max_bytes` left out, read one place, and the rule does not load, as for two built in extractors
of one place. So `{column: port}` and `{column: port, header: true}` are one place when `header`
defaults to true. `description` is what the entry's extractor returns, as
`check.Description` holds it for a built in kind:

| Member | What it is |
|---|---|
| `label` | the kind as a message names it, such as `csv`, or `csv, tsv dialect` |
| `not_literal` | whether a value may be marked not literal; a rule's `skip_not_literal` may list the extractor only when it is true |
| `shape` | `flat`: each value is text; `map`: each value is a mapping handed to `assert` whole; `record`: each value is a mapping of the fields `fields` names |
| `fields` | for `record`, the keys of each record in order; empty for any other shape |
| `reads` | the names of the other extractors of the rule whose values the extractor reads (extract, below) |

A spec the plugin refuses is a result of its own, `{"error": "the message"}`, and the config does
not load, with the message after the entry's place: a spec a plugin refuses is a config error, as
one a built in kind refuses is.

### What `reads` promises

errata runs the extractors `reads` names before this one, refuses a cycle of them, and sends their
values in the extract request. It sends no other values, so a plugin cannot read an extractor its
description did not name. The other direction is the plugin's promise: an extractor named in
`reads` counts as used, so the refusal of an extractor no part of the rule uses passes it over,
and errata does not check that the plugin reads its values. It could not: errata sees the request
and the reply, not what the plugin did between them, and running the plugin again without the
values would double its cost and still prove nothing, since a plugin may read values and return
the same ones. A broken promise leaves an unused extractor in a config without a load error; it
changes no verdict, since the rule compares what the plugin returns either way. A plugin keeps
the promise by deriving `reads` and the reading from one parse of its spec, as the built in
`regex` kind does.

## extract

errata sends one extract request for each entry each time a rule reads it: once for the
repository and once for each witness the rule's check names.

```json
{
  "protocol": 1,
  "op": "extract",
  "spec": {"column": "port", "where": [{"key": "name", "value": "api"}], "header": true, "dialect": "csv", "delimiter": ","},
  "files": [
    {"path": "deploy/services.csv", "data": "bmFtZSxwb3J0CmFwaSw4MDgwCg=="}
  ],
  "values": {},
  "max_bytes": 33554432
}
```

| Member | What it is |
|---|---|
| `spec` | the spec as the build reply wrote it out |
| `files` | the files the entry reads, in the order a built in reader reads them: its `file`, or each file of its `files` in path order. Each has its `path`, relative to the root with `/` between names, as messages print it, and its bytes in `data` |
| `values` | for each name in the description's `reads`, that extractor's literal values, after `skip_not_literal`, in the order they were found, each as the Values section below writes it |
| `max_bytes` | the entry's `max_bytes`, with its default of 32 MiB written out when the entry sets none: the bound errata holds the files to together. Always 1 or more; a request with less is refused |

A plugin bounds its work by `max_bytes`, as a built in reader does (owner, 2026-11-20). Before it
builds a whole document from the files, it counts in one pass over their bytes an upper bound on
what its parser can build, and refuses the files with an error when the count passes `max_bytes`
divided by a constant the plugin measures and names: the Markdown reader allows one node for every
4 bytes of `max_bytes`, the go.mod reader one token for every 64. The bound then scales with what
the config author allows, and a denser file than the constant permits needs a larger `max_bytes`,
which the error names. errata itself sets no memory bound on a plugin (Bounds, below), so a plugin
that skips this can be killed for its memory, or have the system kill errata.

errata reads the files before the plugin starts, with every check a built in reader's file gets:
inside the repository, no link out of it, not a directory, a device or a FIFO, and at most
`max_bytes`. A file that fails one, or a file that is not there, makes the rule unknown with the
message a built in reader gives, and the plugin does not run. A `files` entry whose globs match no
file returns no values without starting the plugin, as a built in reader's glob that matches no
file adds no value; the rule's `allow_empty` then decides.

The plugin replies with its values:

```json
{"values": [{"text": "8080", "file": "deploy/services.csv", "line": 2}]}
```

or with an error, when it could not read a file, such as one it cannot parse:

```json
{"error": "deploy/services.csv:4: a row of 3 cells under a header of 2"}
```

which makes the rule unknown with the plugin's message, or, when it found no value and can say
where the value may be instead:

```json
{"no_value": "the row api sets no port; the port may come from the chart's default"}
```

which the rule treats as an empty list, so `allow_empty` decides, and names the reason when the
rule is unknown for it. An empty `values` list is not an error either: the rule decides whether
finding nothing is unknown. A reply holds exactly one of `values`, `error` and `no_value`; one
that holds two, or none, is refused.

## Values

Each value of a reply, and of a request's `values`, is an object:

| Member | What it is |
|---|---|
| `text` | the value's text. Left out for a value of shape `map` or `record`, whose text errata writes from `data` |
| `file` | the file the value is in, as the request's `path` wrote it |
| `line` | the line it is on, counted from 1, as errata counts lines (below); 0 for a value that holds for the file as a whole |
| `not_literal` | optional, false when left out: `true` marks a value the file only computes, such as a reference or a function call, whose `text` is what the file wrote and not what it evaluates to |
| `key` | optional: the name the file sets the value under, such as `port`; a report names it beside the place. `""` is a key, the empty one; left out is no key |
| `condition` | optional: what must hold for the value to be the one the file means, such as a block's selector; a report names it beside the place |
| `data` | for shape `map` and `record` only: the value whole, as a node |

A node of `data` is one of:

```json
{"line": 3, "scalar": "8080"}
{"line": 2, "map": [{"key": "port", "value": {"line": 3, "scalar": "8080"}}]}
{"line": 5, "list": [{"line": 5, "scalar": "a"}, {"line": 6, "scalar": "b"}]}
```

A scalar is a string, as every value is. A mapping is a list of keys and values in the file's
order, and holds each key once. Each node has its own line in the value's file. A record's mapping
has exactly the keys of the description's `fields`, in that order.

A line ends at a LF, a CRLF or a lone CR, as every built in reader counts lines, so a value's line
is the one an editor shows. A plugin built from this repository counts lines with
`internal/extract/core/lineend`, the package the built in readers count them with. The not
literal mark is the one a built in reader sets: a rule never compares a value marked not literal
as text. It is unknown, or, when its `skip_not_literal` lists the extractor, it leaves the value
out.

## What errata checks of a reply

errata decodes the reply strictly and checks each value before the rule sees it. Any of these
makes the rule unknown, with a message that names the plugin, its version and what was wrong:

- the reply is not one JSON object, holds a member this version does not define, or a member of
  the wrong type;
- a value's `file` is not the `path` of one of the request's `files`, or its `line`, or a node's,
  is below 0 or past the file's last line;
- a value is marked not literal while the description says `not_literal: false`;
- a value's shape is not the description's: `text` without `data` for `map` or `record`, `data`
  for `flat`, or a record whose keys are not `fields`;
- the values pass `MaxValues` or `max_bytes` in all, which the rule holds every extractor's values
  to (each node of `data` counts one value, and its keys' and scalars' bytes count). For a
  plugin's values errata also counts each value's `key` and `condition` to `max_bytes`, which the
  output bound below relies on.

A plugin's `error` and `no_value` text, and stderr's last lines, are text errata did not write:
a plugin may quote a cell a pull request wrote, newline and all. errata quotes them as it quotes a
file's text in a message, so a newline or a direction control prints escaped and never as a line
of the report of its own. A describe reply whose `name` or `version`, or a build reply whose
`label`, holds a control character, a direction control or a line or paragraph separator is
refused, as errata refuses one in a version it reads from a file.

A build reply whose description names in `reads` an extractor the rule does not declare, or a
`shape` of `record` with no `fields`, does not load, as the same description of a built in kind
does not.

## Bounds

ADR [0003](adr/0003-extractors-bound-time-and-size-and-let-memory-fail-closed.md) bounds what a
built in reader reads and returns, and lets memory fail closed. A plugin runs under these:

| Bound | Value | What errata reports past it |
|---|---|---|
| time of a describe or a build request | 10 s | every rule with an entry of the kind is unknown: the plugin did not answer in time |
| stdout of a describe request | 1 MiB | the plugin is stopped at the byte past it, and every rule with an entry of the kind is unknown, naming the stream and the bound |
| stdout of a build request | 32 MiB | as for describe |
| time of an extract request | 30 s | the rule is unknown: the plugin did not finish within 30 s. errata kills its process group |
| the files of one extract request | together at most `max_bytes` | the rule is unknown, naming the file or the set and the bound, and the plugin does not run |
| stdout of an extract request | 6 times `max_bytes`, plus 160 bytes and 6 times the longest `path` of the request for each of `MaxValues` values, plus 64 KiB: about 230 MiB at the default 32 MiB with paths of 40 bytes | the plugin is stopped at the byte past it, as on a timeout, and the rule is unknown, naming the stream and the bound |
| stderr of any request | 1 MiB, of which a message uses the last 256 bytes | the plugin is stopped at the byte past it, as on a timeout, and the rule is unknown, naming the stream and the bound |
| the values a reply returns | `MaxValues` (100000, `wire.MaxValues` to a plugin built from this repository) nodes and `max_bytes` bytes in all | the rule is unknown, with the message a built in reader's values get past the same bound |
| memory | none set by errata; the plugin bounds its own work by the request's `max_bytes` (extract, above) | a plugin the system or the runner kills for its memory ended by a signal: the rule is unknown, naming the signal. If the system kills errata instead, the CI job fails |

The output bound stops the plugin rather than dropping what passes it, since errata cannot decode
a reply it did not read whole. It is the largest compact reply whose values pass the values
bound, so such a reply refused at the output bound would have been refused at the values bound
too. Measured with Go's `encoding/json` on 2026-11-19:

- A string costs at most 6 bytes for one byte of its text: a control character is `\u0000`, and
  Go writes `<`, `>` and `&` as `\u003c` and the like. U+2028 costs 6 bytes for 3, a quote or a
  newline 2 for 1. The strings of the values, their text, keys, conditions, map keys and
  scalars, are at most `max_bytes` in all, hence 6 times `max_bytes`.
- With every member, empty strings and lines of 19 digits, and its comma, a flat value costs 92
  bytes besides its strings and its `file`, and a value of `data` with its first node 130. A
  further node costs at most 100: 40 for a scalar node and 60 for its key's pair in a map, or 41
  as an item of a list. A value and each further node count one of `MaxValues`, hence 160 bytes
  for each.
- Each value repeats its `file`, which costs at most 6 times its length.
- The reply's own members and white space between them are left 64 KiB, so a plugin writes its
  reply without indentation, as `json.Marshal` does. An indented reply near the most values can
  pass the output bound, and its rule is then unknown.

The figure is large because it is a ceiling: a real reply is a small part of it. errata holds
what the plugin writes, so the bound is also the most memory a reply takes.

A plugin that exits with a status other than 0, is killed by a signal, or writes no reply makes
the rule unknown; the message names the status or the signal and ends with stderr's last lines.
For a describe or a build request, every rule with an entry of the kind is unknown in the same way,
as it is when the plugin is not installed.

A cancelled run is not a timeout. When errata's run stops before the plugin finishes, as an
interrupt, `SIGTERM` or `SIGHUP` stops it, errata kills the plugin's process group, and the rule is
unknown with a message that starts `cancelled:`, as a program check's is: it says errata's run
stopped, and names no timeout and no fault of the plugin. The function that starts every program
errata runs tells a run whose caller cancelled it apart from one that passed its own timeout.

## Protocol 2

Protocol 2 is protocol 1 with the provider operations of [GLOSSARY.md](../GLOSSARY.md): render
turns files into the objects they describe, read fetches the current state of the live system, and
predict reports what an apply would produce without applying it. A plugin that provides one of them
is a provider, and a live or applied check names it with `provider:` (see
[Scopes](reference.md#scopes)). The plugin's name is still the key of its kind when it provides
extract too, so `errata-ext-kubernetes` is the provider `kubernetes` and, if it provides extract,
the extractor kind `kubernetes:`.

### describe in protocol 2

The describe request is the same, with `"protocols": [1, 2]`. A reply that chooses 2 adds two
members:

```json
{
  "protocol": 2,
  "name": "kubernetes",
  "version": "0.1.0",
  "operations": ["render", "read", "predict"],
  "env": ["KUBECONFIG", "HOME"]
}
```

| Member | What it is |
|---|---|
| `operations` | the operations the plugin provides, each once, from `extract`, `render`, `read` and `predict`; at least one |
| `env` | optional: the names of the environment variables the plugin's read and predict requests need, each once, each of ASCII letters, digits and `_`, not starting with a digit, at most 64 bytes, and at most 32 names. None of them is a variable errata itself reads, in any case: `GITHUB_TOKEN` and `ERRATA_PLUGIN_SOURCE` |
| `where`, `fields` | as in protocol 1, required when `operations` holds `extract`, and refused when it does not |

`env` is refused in a reply whose `operations` holds neither read nor predict, and so is a name of
a variable errata itself reads, compared in any case, as Windows looks variables up:
`GITHUB_TOKEN`, the token `errata plugin install` sends to GitHub's releases API, which a CI job
may hold with write scope, and `ERRATA_PLUGIN_SOURCE`, the mirror it installs from. errata's own
credential is for errata, and no provider needs it to reach a live system. A plugin that
chooses protocol 1 provides extract alone, as before.

A provider plugin may not run as an extractor kind, and an extractor plugin may not serve a check's
`provider:`: an entry of a kind whose plugin's `operations` lacks `extract` is unknown, as an entry of
a plugin that does not describe itself is, and a check whose provider lacks the operation its scope
needs is skipped, with a gap that names the operation and the plugin.

The build and extract requests of a plugin that chose 2 are protocol 1's with `"protocol": 2`.

### The environment of read and predict

A read or a predict request runs with protocol 1's environment and, beside it, each variable `env`
names that errata's environment holds, with errata's value. A variable errata's environment lacks
is left out, as a Go runtime variable is. `errata plugin install` prints the names and records them
in `errata.lock` ([reference](reference.md#erratalock)), and a run passes only the names both
`env` and the lock hold. The lock records the names for each platform's build, so a plugin may
name other variables on another OS, such as `USERPROFILE` where it names `HOME` elsewhere. A
plugin whose `env` names one the lock does not record for its platform does not run:
each check that names it is unknown, with a message that names the variable and the install that
records it, so a release that asks for another variable reaches a CI job only through a change to
the lock. Describe, build, extract and render requests get protocol
1's environment alone, whatever `env` says: render reads the files errata sends it, as extract does,
and needs no network and no credentials. So in CI a job's tokens reach only the read and predict
requests of a plugin that names them, and only those it names.

The working directory is still an empty directory made for the request, so a variable that holds a
relative path, such as `KUBECONFIG=deploy/kubeconfig`, finds nothing; it must be absolute. Unlike
the other operations, read and predict reach the network: that is their purpose, and errata cannot
see which hosts they reach.

### render, read and predict

errata sends each operation one request:

```json
{
  "protocol": 2,
  "op": "render",
  "spec": {"chart": "deploy/chart", "values": ["deploy/values.yaml"]},
  "files": [
    {"path": "deploy/chart/Chart.yaml", "data": "YXBpVmVyc2lvbjogdjIK"}
  ],
  "max_bytes": 33554432
}
```

| Member | What it is |
|---|---|
| `op` | `render`, `read` or `predict` |
| `spec` | a JSON object that says what to render, read or predict, in the plugin's own members. This version of the protocol does not declare them; the plugin refuses a spec it does not read with an error |
| `files` | the files the request reads, as an extract request sends them: render's sources, or for predict the objects an apply would send. A read request sends `[]` |
| `max_bytes` | the bound on the reply's files, their paths and bytes together, and errata's default of 32 MiB when the caller sets none. Always 1 or more |

The plugin replies with files, the objects the operation found, each a document under a path the
plugin chooses:

```json
{"files": [{"path": "apps/v1/Deployment/api/api.yaml", "data": "a2luZDogRGVwbG95bWVudAo="}]}
```

or with an error, which makes what asked for the operation unknown:

```json
{"error": "deploy/chart/templates/a.yaml:3: function \"lookup\" is not defined"}
```

or, for read and predict alone, with a gap, when the live system cannot be reached at all, such as
no cluster configured or no permission to read it:

```json
{"gap": "no cluster: KUBECONFIG names no context"}
```

A gap skips the check that asked, as a check whose provider is not installed is skipped: the run
lacks the system, and the check's claim was not tested. A render reply with `gap` is refused. A
reply holds exactly one of `files`, `error` and `gap`; one that holds two, or none, is refused.

errata checks each file of a reply: its `path` is one or more names joined by `/`, none of them
empty, `.` or `..`, with no `\` and no control character, and at most 4096 bytes; no path appears
twice; and the paths and bytes of all the files together are at most the request's `max_bytes`. A
reply that fails any of these is refused, and what asked for the operation is unknown, naming the
plugin, its version and what was wrong.

| Bound | Value | What errata reports past it |
|---|---|---|
| time of a render, read or predict request | 60 s | what asked is unknown: the plugin did not finish within 60 s. errata kills its process group |
| stdout of a render, read or predict request | 6 times `max_bytes`, plus 22 bytes for each of 100000 files, plus 12: about 194 MiB at the default 32 MiB | the plugin is stopped at the byte past it, and what asked is unknown, naming the stream and the bound |
| stderr | 1 MiB, as for every request | as for every request |
| the files of a reply | 100000 files, and `max_bytes` for their paths and bytes together | what asked is unknown, naming the bound |

The stdout bound is the longest reply whose files pass the files bound, written as Go's
`encoding/json` writes it, without indentation, and a newline after it:

- A byte of a path costs at most 6: Go writes `<`, `>` and `&` as `\u003c` and the like, and a
  path holds no control character. A byte of data costs at most 6 too: base64 writes 4 bytes for
  3, and 4 for the last 1 or 2. The paths and data together are at most `max_bytes`, hence 6
  times `max_bytes`.
- A file costs 22 bytes besides its path and data, `{"path":"","data":""}` and its comma, and a
  reply holds at most 100000 files. An empty file's data is `""`: `null` is no value for any
  member, and a reply that holds it is refused. Go writes a nil `[]byte` as `null`, so a plugin
  in Go writes an empty file's data as an empty slice, as errata's `wire.File` does.
- The reply's own member, `{"files":[]}`, with no comma after its last file, and a newline after
  it, cost 12.

A reply of 100000 files whose paths are all `<`, `>` and `&` reaches the bound exactly, and a byte
more is cut; errata's tests send both. A plugin that indents its reply can pass the bound with
files errata would admit. A cancelled run is not a timeout, as for every request.

## Versions

The protocol has one integer version, 2 in this document, and errata speaks 1 and 2. Every change to a request or a reply
is a new version: a member added, removed or given another meaning, a new type of field, a new
shape. Within one version neither side meets a member it does not know.

A plugin declares the version it speaks in its describe reply, choosing from the versions errata
offers, and may speak several. errata speaks every version from 1 to its newest, and offers them
all, `[1, 2]` today. An extractor plugin needs nothing protocol 2 adds and may stay on 1. A
release that stops speaking a version is a breaking change of errata, which its release notes
name. The five members of describe that choose the version never change (describe, above), so
the choice works between any two versions.

When the plugin speaks none of the versions errata offers, its reply lists the versions it does
speak, and every rule with an entry of the plugin's kind is unknown, with a message that names
both sides and what to do:

```text
plugin errata-ext-csv 0.4.0 speaks protocol 3, 4, and this errata speaks 1, 2: install a release
of errata-ext-csv for protocol 1, 2, or upgrade errata
```

The run fails, as it does for a plugin that is not installed, and no rule of another kind is
affected. A plugin that replies to describe with a version errata did not offer is refused the
same way.
