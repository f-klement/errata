# errata handbook

How to use errata, in the order a new user needs it: install it, write a first check, choose
between a program and a rule, decide what is worth checking, wire it into CI, and make the gate
hold. The [reference](reference.md) has every key, extractor and limit for looking up, and
[cli.md](cli.md) every command, flag and exit code; this handbook links into both rather than
repeating them. [GLOSSARY.md](../GLOSSARY.md) fixes the meaning of each word, and the
[README](../README.md) says why errata exists.

- [Install](#install)
- [The first check](#the-first-check)
- [Program checks and rules](#program-checks-and-rules)
- [A claim worth checking](#a-claim-worth-checking)
- [The extractors by task](#the-extractors-by-task)
- [CI](#ci)
- [A gate that cannot pass on less](#a-gate-that-cannot-pass-on-less)
- [Reading a failure and an unknown](#reading-a-failure-and-an-unknown)
- [Moving a suite of scripts to rules](#moving-a-suite-of-scripts-to-rules)
- [Worked examples](#worked-examples)
- [The example config](#the-example-config)

## Install

errata is one static binary, built for the systems the README's [Features](../README.md#features)
list. Each release on the [releases page](https://github.com/f-klement/errata/releases) has a
`tar.gz` archive for each of them (a `zip` for Windows), a `checksums.txt` and an SPDX SBOM for
each archive. On Linux, check the archive you downloaded against the checksums before unpacking:

```bash
sha256sum --ignore-missing -c checksums.txt
```

With Go installed, `go install` builds it from source instead:

```bash
go install github.com/f-klement/errata/cmd/errata@latest
```

`errata version` prints the version you have.

The image `fklement/errata` holds the same binary on Alpine with bash, so a program check can be a
shell script. Mount the repository and make it the working directory:

```bash
docker run --rm -v "$PWD:/repo" -w /repo fklement/errata check
```

[DOCKERHUB.md](../DOCKERHUB.md) lists its tags, what is in it, and how to add the tools a check
needs.

## The first check

A check tests one claim. This repository's README tells a new developer which Node to install:

```markdown
# Shop

## Development

Install Node 20.11, then run `npm ci`.
```

and `.nvmrc`, which nvm and the CI job read, pins the Node they install:

```text
20.11.1
```

The two say the same thing in two places, and nothing keeps them in step. Put an `errata.yaml` at
the root of the repository with a rule that holds them to each other:

```yaml
checks:
  - id: readme-names-the-node-version
    reason: >
      The README tells a new developer which Node to install, and .nvmrc pins the Node that nvm
      and CI install. A README that names another version sets up a machine that differs from CI.
    scope: repo
    extract:
      readme: {markdown: {file: README.md, target: section, section: Development, pattern: 'Install Node ([0-9.]+)'}}
      pinned: {tool_version: {tool: node}}
    agree: [readme, pinned]
    compare: semver
```

Every check has an `id`, a `reason` and a `scope`. The reason is what errata prints beside the
check when it fails, so write it for the person who reads that failure: what the claim is and what
breaks when it is false. A check without one does not load. The scope `repo` says that the check
reads only files ([Scopes](reference.md#scopes)).

The rule names two extractors under `extract`. `readme` reads the section of the README under the
heading Development and keeps what the pattern's group captures; `pinned` reads every file that
pins Node, here `.nvmrc`. `agree` holds when their values are the same. With `compare: semver` it
compares them as versions, so the README's `20.11` agrees with any `20.11` release
([`compare`](reference.md#compare)).

`errata list` shows the check as errata loads it, with the defaults it fills in, and runs nothing:

```text
readme-names-the-node-version (repo) rule
  why: The README tells a new developer which Node to install, and .nvmrc pins the Node that nvm and CI install. A README that names another version sets up a machine that differs from CI.
  extract: pinned: tool_version tool node, max_bytes 33554432
           readme: markdown file README.md, max_bytes 33554432, target section, section Development, pattern `Install Node ([0-9.]+)`
  agree: readme, pinned (compare semver)

1 check: 1 rule, 0 program checks.
```

`errata check` runs it. A check whose claim holds prints nothing, so a run where every claim holds
prints only the coverage line, and exits 0:

```text
0 errata, 0 unknown. Coverage: ran 1, skipped 0.
```

[Reading a failure and an unknown](#reading-a-failure-and-an-unknown) takes the same check on to
the day the claim stops holding.

## Program checks and rules

errata has three kinds of check, and an entry's keys decide which one it is
([Checks](reference.md#checks)):

- A **rule** has `extract`. Its extractors read values out of files, inside errata, and `agree`
  or `assert` states how they relate. A rule starts no program.
- A **program check** has `run`: a program, in any language, that exits 0 when the claim is true
  and 1 when it is false, and prints its message
  ([the check protocol](reference.md#the-check-protocol)).
- A **generated check** has `generated`: it runs a generator and holds when what it prints is the
  committed file, byte for byte ([`generated`](reference.md#generated)).

Write a rule whenever an extractor can read the values. A rule names every value it compared with
the file and the line it came from, so a failure says where to look. Its extractors fail closed:
a file that is gone, a pattern that matches nothing or a value the file only computes makes the
rule unknown, where a script's `grep` that matches nothing passes unless the script guards it.
It reads files and runs nothing. And `errata list --format json` describes it, so other tools can
read what it checks.

Write a program check when the claim needs something no extractor reads: the current date, as in
[the attested fact](#an-attested-fact-is-older-than-its-lifetime), a network call, or a program
the repository already has. A program check is code that runs with the job's environment, secrets
included. On a ref you do not trust, such as a merge request from a fork, the config is that
ref's too and can add a `run`, so run errata there in a job without secrets, or with a config of
rules only from a ref you trust, named with `--config` ([Checks](reference.md#checks)).

Write a generated check when a committed file is the output of a program, such as generated
documentation or a public copy of a private file, and must be what the program prints today.

## A claim worth checking

The README's [What belongs in errata](../README.md#what-belongs-in-errata) draws the line between
errata and a language's own tools. Inside that line, a claim is worth a check when these hold:

- **It is stated in a place other than its source.** A port in a document and in the config the
  service reads; a version in a README and in the file the build pins it in; a job name in a
  runbook and in the pipeline. The check holds the copy to the source, and the reason says which
  is which, so whoever fixes a failure knows which side to change.
- **Being wrong costs something.** A sentence an agent acts on, a step an operator follows, a
  threshold an alert depends on. A claim nobody reads costs nothing when it rots.
- **The relation, not the value.** A check that compares a derived number with its current value
  holds until the day the number it derives from changes, and then still holds. State how the
  number derives, as [the backup example](#a-derived-number-drifted-from-what-derives-it) does.
- **One relation per check.** A host and a port are two checks, so a failure names the one that
  broke. `agree` or `assert` holds one relation; two relations do not fit in one rule.
- **It can fail.** A pattern that can never match under `allow_empty`, or an assertion that is
  true of any list, passes on every repository. A [witness](reference.md#witness) shows that the
  check reports false on a planted break.

## The extractors by task

Pick the extractor by what the value is, not by the file's format alone. The
[table of kinds](reference.md#extractors) gives the fields of each, and each kind has a section of
its own in the reference.

| Task | Kinds |
|---|---|
| a value in a sentence of a document, or of a code comment | [`text`](reference.md#text), [`comment`](reference.md#comment) |
| a value in a document's structure: a section, a table cell, a code block, an HTML comment | [`markdown`](reference.md#markdown), [`asciidoc`](reference.md#asciidoc), [`rst`](reference.md#rst), [`html`](reference.md#html) |
| a claim a document tags with the id of the rule that checks it | [`marker`](reference.md#marker) |
| a value at a path in a structured file | [`yaml`](reference.md#yaml), [`json`](reference.md#json), [`toml`](reference.md#toml), [`xml`](reference.md#xml) |
| a key in a flat config file | [`ini`](reference.md#ini), [`properties`](reference.md#properties), [`envfile`](reference.md#envfile) |
| the version of a tool, or of the package itself | [`tool_version`](reference.md#tool_version), [`package_version`](reference.md#package_version) |
| a variable a CI job or a script sets | [`gitlab-ci`](reference.md#gitlab-ci), [`github-actions`](reference.md#github-actions), [`jenkins`](reference.md#jenkins), [`shell`](reference.md#shell) |
| one variable in every file that sets it | [`env`](reference.md#env) |
| whether a file is there, and its size | [`exists`](reference.md#exists-and-size), [`size`](reference.md#exists-and-size) |
| anything else a regular expression finds | [`regex`](reference.md#regex) |

A few choices come up often:

- **`text` before `regex` for prose.** `text` reads a Markdown file as a browser shows it, so a
  line break, a code span or a link inside the sentence does not hide the value. A `regex` over the
  raw file misses the value the day someone wraps the line.
- **A section or a table before the whole file.** `markdown` with `section:` reads only the part
  of the document the claim lives in, so the same words elsewhere in the file are not read as the
  claim.
- **`marker` when the document should say what it claims.** A marker puts the rule's id beside the
  value, so whoever edits the sentence sees that a check reads it, and errata reports a marker no
  rule reads.
- **The CI kinds before `regex` for a CI variable.** `gitlab-ci`, `github-actions` and `jenkins`
  give the value a job, a step or a stage sees, one for each case the file chooses between; a
  regex finds a line that may not be the one that applies.
- **`files:` for every file of a kind.** `yaml`, `json`, `toml` and `regex` take a glob, so the
  file added tomorrow is read too ([A set of files](reference.md#a-set-of-files)).

Then the relation. `agree` holds when every value is the same, as text or, with `compare: semver`,
as a version ([`compare`](reference.md#compare)). Anything else is an `assert`: a
[CEL](https://cel.dev) expression over the values, with functions for durations, cron schedules,
ports and sets ([`assert`](reference.md#assert)).

A value no tool can read, such as a setting in a router's web interface, is an
[attested fact](#an-attested-fact-is-older-than-its-lifetime): a file that records it, which a rule
reads and a program check ages.

## CI

errata has no integration with any CI system, on purpose: it is one binary that exits non zero and
writes JUnit or SARIF ([Output formats](reference.md#output-formats)). In this GitLab CI job the
log shows the text report and the pipeline's test report reads the JUnit file:

```yaml
errata:
  image:
    name: fklement/errata:latest
    entrypoint: [""]
  script:
    - errata check --root "$CI_PROJECT_DIR" --output junit=errata.xml
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

GitHub code scanning reads the [SARIF](reference.md#sarif) that `--format sarif` writes. Uploaded,
each erratum becomes an alert on the line that holds the value that disagrees. A pull request
shows the alert inline only when that line is in its diff; otherwise the alert is in the
repository's code scanning list.

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
      - run: docker run --rm -v "$PWD:/repo" fklement/errata:latest check --root /repo --output sarif=/repo/errata.sarif
      - uses: github/codeql-action/upload-sarif@v4
        if: always()
        with:
          sarif_file: errata.sarif
          category: errata
```

The errata step still fails the job when the run fails ([exit codes](cli.md#errata-check));
`if: always()` uploads the file anyway. errata runs with `docker run` instead of as the job's
`container:`, so `upload-sarif`, a JavaScript action, runs on the runner and not in the errata
image, which holds only errata and bash. `persist-credentials: false` keeps the job's token, which
can write code scanning results, out of `.git/config`, where every program check in the mounted
checkout could read it.

The [reference](reference.md#sarif) says what goes where in the file, which value a result blames,
and how errata keeps the file inside GitHub's limits.

## A gate that cannot pass on less

A CI job that runs errata is a gate: it should fail whenever the suite tested less than it says.
errata fails closed by default. An unknown fails the run, and a check that does not load stops it
with exit code 2 before anything runs. Three settings close the ways a suite can still shrink
without a failure:

- **A deleted check.** Delete an entry from `errata.yaml`, and the run passes without it. The
  top level [`required`](reference.md#required) list names every check the config holds, and a
  config whose checks are not exactly those ids does not load. Write it one id a line, so the diff
  that deletes a check deletes a line under `required` too, where a reviewer sees it. The
  reference says how to keep the list where the author's change cannot reach it.
- **A skipped check.** A check whose scope the run cannot reach, such as a `live` check in a job
  with no cluster, is skipped, and a skip passes. [`--fail-on-skip`](cli.md#errata-check) makes a
  skip fail the run as an unknown does.
- **A check that cannot fail.** A [`witness`](reference.md#witness) names a fixture with a break
  planted in it, and every run runs the check on it too. A check that holds on its witness is an
  erratum.

[`defaults`](reference.md#defaults) does not stop a deleted check itself. It makes `required`
cheap to write beside many program checks that run one program with their own id, as a suite of
scripts often does: the program is written once, as the default `run`, and each check is an id, a
reason and a scope, so each id is written twice in one file:

```yaml
defaults:
  run: [python3, scripts/checks.py, --only, <errata.id_underscored>]
required:
  - no-latest-tags
  - backups-cover-the-retention-window
checks:
  - id: no-latest-tags
    reason: An image tagged latest changes under a pinned deploy.
    scope: repo
  - id: backups-cover-the-retention-window
    ...
```

The CI job then runs `errata check --fail-on-skip`, with the output it needs for its test report.

## Reading a failure and an unknown

The exit code says whether the run passed, and [`errata check`](cli.md#errata-check) lists each
code. The text report prints a block for each check that did not hold, then the coverage line
([The text report](reference.md#the-text-report)).

Take the [first check](#the-first-check) on the day CI moves to Node 22, and `.nvmrc` says
`22.12.0` while the README still says 20.11:

```text
FALSE readme-names-the-node-version (repo)
  why:  The README tells a new developer which Node to install, and .nvmrc pins the Node that nvm and CI install. A README that names another version sets up a machine that differs from CI.
  said: agree: the versions "20.11" and "22.12.0" differ at the precision both state
          "20.11" (version 20.11) at README.md:5 (readme)
          "22.12.0" (version 22.12.0) at .nvmrc:1 (pinned)

1 erratum, 0 unknown. Coverage: ran 1, skipped 0.
```

`FALSE` is an erratum: the check showed the claim false. `why:` is the reason, and `said:` is the
check's account: for a rule, each value with the file, the line and the extractor it came from.
The reason says which side is the source, here `.nvmrc`, so the fix is to the README, and the
check holds again. A fix to the source instead would undo the change the erratum caught.

Now someone rewords the README so that it no longer names a version:

```markdown
# Shop

## Development

Install the Node version in `.nvmrc`, then run `npm ci`.
```

The pattern finds nothing, and the check cannot compare:

```text
UNKNOWN readme-names-the-node-version (repo)
  why:  The README tells a new developer which Node to install, and .nvmrc pins the Node that nvm and CI install. A README that names another version sets up a machine that differs from CI.
  said: extractor "readme" found no value

0 errata, 1 unknown. Coverage: ran 1, skipped 0.
```

`UNKNOWN` is not an erratum. The claim was not shown false: the check could not evaluate. It
still fails the run, because a suite that passes on what it could not test passes on a subset.
The `said:` line names the extractor and what went wrong. Here the README no longer makes the
claim at all, so the check has nothing left to hold, and the change that reworded the README
removes the check with it. Had the README moved the sentence under another heading, the fix would
be the rule's `section`.

Every cause of an unknown, for a rule, is in
[When a rule is unknown](reference.md#when-a-rule-is-unknown): a file that is missing, does not
parse or is over a [limit](reference.md#limits), a pattern that finds nothing, and a value the
file only computes. A program check is unknown when it exits with anything but 0 or 1, runs
past its `timeout`, or cannot be started ([the check protocol](reference.md#the-check-protocol)).
Two keys accept an empty or a computed value on purpose, and are not ways to silence an unknown:

- [`allow_empty`](reference.md#checks) lists an extractor that may find no value, when an absent
  value is part of the claim, such as an optional setting.
- [`skip_not_literal`](reference.md#values-a-file-only-computes) lists an extractor whose values
  the file only computes, such as `$CI_COMMIT_SHA`, and leaves those out. The report still names
  each value it left out.

Under the coverage line, a `skipped` line names each check the run could not reach and the gap
behind it. A witness adds a result of its own, which [`witness`](reference.md#witness) and
[The text report](reference.md#the-text-report) describe.

## Moving a suite of scripts to rules

Many repositories already have a directory of check scripts. errata runs them as they are, then
lets you replace them one at a time.

1. **Run the scripts as program checks.** Give each script a check with its id and a reason, and
   a `run` that starts it. A script that exits 0 or 1 already follows the
   [check protocol](reference.md#the-check-protocol); one that exits 1 for an error it hit, rather
   than a false claim, should exit 2 instead, so errata reports it as unknown. When one program
   runs every check by name, write it once as the [default run](reference.md#defaults).
2. **Close the gate.** Add [`required`](reference.md#required) with every id, and run errata in CI
   as [A gate that cannot pass on less](#a-gate-that-cannot-pass-on-less) says. The suite now
   fails on a deleted check, a skipped one and an unknown.
3. **Turn each script into a rule where an extractor reads its values.** A script that greps a
   document and a config for the same value is an `agree` rule; one that computes a relation is an
   `assert`. Use [the extractors by task](#the-extractors-by-task) to pick the kinds. Delete the
   script in the same change, and keep the check's id, so `required` does not change.
4. **Keep a program check where the claim needs a program.** The current date, a network call or
   the repository's own tooling stays a program check. A script that regenerates a file and runs
   `git diff --exit-code` becomes a [generated check](reference.md#generated), which compares what
   the generator prints without rewriting the working tree.
5. **Plant a break for each rule.** Commit a [witness](reference.md#witness) fixture with the break
   the old script was written to catch. The rule must report false on it, on every run.

A script often reads the working directory; a program check should read the tree from
`ERRATA_ROOT`, which is the repository root on a real run and the fixture on a witness.

## Worked examples

Four claims a repository commonly gets wrong, each written as errata checks. Each example shows
the files, the checks, what `errata check` prints while the claim is false, and the fix that makes
it hold. Every block in this section, [The first check](#the-first-check) and
[Reading a failure and an unknown](#reading-a-failure-and-an-unknown) is a section of a test: each
example is an API test in `internal/cli/testdata/script`, which runs the checks on the files shown
here and then on the fixed ones, and errata's own suite fails when a block here differs from the
test's section.

### A document names the wrong port

A file an agent reads before it acts, such as `AGENTS.md` or `CLAUDE.md`, names hosts, ports,
versions and paths. When the configuration moves on and the file does not, the agent acts on the
old value, and nothing reports it.

Here the staging database moved behind a connection pooler on port 6432. `AGENTS.md` still says
5432:

```markdown
# Working on this service

The staging database is `db.staging.example.com` on port
`5432`. Run the migrations against it before a release.
```

`config/staging.toml`, which the application reads:

```toml
[database]
host = "db.staging.example.com"
port = 6432
```

The `text` extractor reads `AGENTS.md` as a browser shows it, so the backticks and the line
break between `port` and its number do not hide the value. `agree` holds when the document and
the config give the same value. One relation is one check, so the host and the port are two:

```yaml
checks:
  - id: agents-name-the-staging-db-host
    reason: >
      AGENTS.md tells an agent which host the staging database is on. An agent that reads
      another host connects to the wrong database or to none.
    scope: repo
    extract:
      doc:    {text: {file: AGENTS.md, pattern: 'staging database is (\S+) on port'}}
      config: {toml: {file: config/staging.toml, path: database.host}}
    agree: [doc, config]

  - id: agents-name-the-staging-db-port
    reason: >
      AGENTS.md tells an agent which port the staging database listens on. The config is what
      the application reads, so a port the document names and the config does not sends the
      agent to a closed port.
    scope: repo
    extract:
      doc:    {text: {file: AGENTS.md, pattern: 'staging database is \S+ on port (\d+)'}}
      config: {toml: {file: config/staging.toml, path: database.port}}
    agree: [doc, config]
```

The host agrees and the port does not:

```text
FALSE agents-name-the-staging-db-port (repo)
  why:  AGENTS.md tells an agent which port the staging database listens on. The config is what the application reads, so a port the document names and the config does not sends the agent to a closed port.
  said: agree: the values differ
          "5432" at AGENTS.md:3 (doc)
          "6432" at config/staging.toml:3 (config)

1 erratum, 0 unknown. Coverage: ran 2, skipped 0.
```

The fix is to the document, since the config is what runs: `AGENTS.md` says `6432`, and both
checks hold. The same pair of extractors fits a version, with `compare: semver` when the two
places state it at different precision (see
[`compare`](reference.md#compare)), and a path.

### A derived number drifted from what derives it

Some numbers are computed by hand from others: a retention count from a schedule, an alert's
threshold from an interval, a replica count from a quorum. When the number they come from
changes, the derived one stays where it was.

Here the backup policy keeps a restore point from any time in the last 7 days. The backup ran
every 6 hours, and 28 backups covered the week. The schedule changed to every hour:

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: backup
spec:
  schedule: "0 * * * *"
```

`backup.toml` still keeps 28, which now covers 28 hours:

```toml
[retention]
keep_last = 28
window = "7d"
```

`assert` states the relation as a [CEL](https://cel.dev) expression. `cron_interval` reads the
time between runs of the schedule, `int` and `duration` read the TOML values, and a duration
times a whole number is a duration:

```yaml
checks:
  - id: backups-cover-the-retention-window
    reason: >
      The policy promises a restore point from any time in the retention window. The tool
      deletes all but the newest keep_last backups, so keep_last runs of the schedule must
      span the window, or the oldest restore points are gone before the window ends.
    scope: repo
    extract:
      schedule: {yaml: {file: deploy/backup-cronjob.yaml, path: spec.schedule}}
      keep:     {toml: {file: backup.toml, path: retention.keep_last}}
      window:   {toml: {file: backup.toml, path: retention.window}}
    assert: cron_interval(schedule[0]) * int(keep[0]) >= duration(window[0])
```

The message gives every value the expression saw, with its place:

```text
FALSE backups-cover-the-retention-window (repo)
  why:  The policy promises a restore point from any time in the retention window. The tool deletes all but the newest keep_last backups, so keep_last runs of the schedule must span the window, or the oldest restore points are gone before the window ends.
  said: assert: the expression is false
          expression: cron_interval(schedule[0]) * int(keep[0]) >= duration(window[0])
          keep[0] = "28" at backup.toml:2
          schedule[0] = "0 * * * *" at deploy/backup-cronjob.yaml:6
          window[0] = "7d" at backup.toml:3

1 erratum, 0 unknown. Coverage: ran 1, skipped 0.
```

With `keep_last = 168` the rule holds. Had the rule compared `keep_last` with the literal 28, it
would have held before and after the schedule changed; stating how the number derives is what
makes the check notice the change.

### A procedure's prerequisite is no longer declared

A runbook step names what it needs: a package, a kernel parameter, a file. The repository
declares those somewhere else, in an Ansible role, a Dockerfile or a sysctl file. When the
declaration is removed, the runbook keeps asking for it.

Here the runbook for adding a node, `docs/runbook.md`:

```markdown
# Adding a node

1. Install the package `nfs-common` with apt; the volumes mount over NFS.
2. Install the package `jq` with apt.
3. Check that the kernel parameter `net.ipv4.ip_forward` is 1.
4. Check that the kernel parameter `vm.max_map_count` is at least 262144, or search does not
   start.
```

The node role installs the packages, in `ansible/roles/node/tasks/main.yml`:

```yaml
- name: Install the node packages
  ansible.builtin.apt:
    name:
      - jq
      - open-iscsi

- name: Install the sysctl settings
  ansible.builtin.copy:
    src: 99-node.conf
    dest: /etc/sysctl.d/99-node.conf
```

and sets the kernel parameters, in `ansible/roles/node/files/99-node.conf`:

```ini
net.ipv4.ip_forward = 1
fs.inotify.max_user_watches = 524288
```

Storage moved from NFS to iSCSI and search moved off the nodes, so the role dropped `nfs-common`
and `vm.max_map_count`. The runbook did not. `subset` is true when every value the runbook names
is one the role declares:

```yaml
checks:
  - id: runbook-packages-are-installed
    reason: >
      The runbook for adding a node names the packages the node needs, and the node role is what
      installs them. A package the runbook names and the role does not install is a step that
      either fails or installs something nothing else keeps up to date.
    scope: repo
    extract:
      runbook:  {text: {file: docs/runbook.md, pattern: 'the package (\S+) '}}
      declared: {yaml: {file: ansible/roles/node/tasks/main.yml, path: "$[*]['ansible.builtin.apt'].name[*]"}}
    assert: subset(runbook, declared)

  - id: runbook-kernel-parameters-are-set
    reason: >
      The runbook for adding a node has the operator check kernel parameters that the node role's
      sysctl file sets. A parameter the runbook names and the file does not set fails the check
      on every new node, and sends the operator to set by hand what the repository dropped.
    scope: repo
    extract:
      runbook:  {text: {file: docs/runbook.md, pattern: 'the kernel parameter (\S+) '}}
      declared: {regex: {file: ansible/roles/node/files/99-node.conf, pattern: '^([a-z0-9_.]+)\s*='}}
    assert: subset(runbook, declared)
```

Both checks are errata:

```text
FALSE runbook-packages-are-installed (repo)
  why:  The runbook for adding a node names the packages the node needs, and the node role is what installs them. A package the runbook names and the role does not install is a step that either fails or installs something nothing else keeps up to date.
  said: assert: the expression is false
          expression: subset(runbook, declared)
          declared[0] = "jq" at ansible/roles/node/tasks/main.yml:4
          declared[1] = "open-iscsi" at ansible/roles/node/tasks/main.yml:5
          runbook[0] = "nfs-common" at docs/runbook.md:3
          runbook[1] = "jq" at docs/runbook.md:4

FALSE runbook-kernel-parameters-are-set (repo)
  why:  The runbook for adding a node has the operator check kernel parameters that the node role's sysctl file sets. A parameter the runbook names and the file does not set fails the check on every new node, and sends the operator to set by hand what the repository dropped.
  said: assert: the expression is false
          expression: subset(runbook, declared)
          declared[0] = "net.ipv4.ip_forward" at ansible/roles/node/files/99-node.conf:1
          declared[1] = "fs.inotify.max_user_watches" at ansible/roles/node/files/99-node.conf:2
          runbook[0] = "net.ipv4.ip_forward" at docs/runbook.md:5
          runbook[1] = "vm.max_map_count" at docs/runbook.md:6

2 errata, 0 unknown. Coverage: ran 2, skipped 0.
```

The message lists the values of both extractors. A value under `runbook` that is missing under
`declared` is a stale step: `nfs-common` in the first check, `vm.max_map_count` in the second.
With step 1 rewritten for iSCSI and step 4 removed, both checks hold.

A file the runbook names is a claim too: the role's sysctl file must be there for step 3 to mean
anything. Every other extractor is unknown for a file that is gone, since it was asked for a value
inside it. [`exists`](reference.md#exists-and-size) is asked whether the file is there, and its
`false` is an answer, so the rule below is an erratum once the file is gone, not an unknown:

```yaml
checks:
  - id: runbook-sysctl-file-exists
    reason: >
      The runbook has the operator check the kernel parameters that the node role's sysctl file
      sets. If the file is gone, the role sets none of them, and every step that checks one fails.
    scope: repo
    extract:
      sysctl: {exists: {file: ansible/roles/node/files/99-node.conf}}
    assert: sysctl[0] == 'true'
```

It holds while the file is there and is an erratum once it is gone.

### An attested fact is older than its lifetime

Some values live where no tool can read them: a router's firewall rule, a setting in a NAS's web
interface, a switch's port assignment. A document that states one can only be checked against a
record of what someone saw. An attested fact is that record, written into the repository with
its source and the day it was read, and trusted for a stated number of days.

Here the router blocks SSH from the internet, and someone looked on 20 July. Each fact is one
file under `facts/`, such as `facts/router-wan-ssh.yaml`:

```yaml
value: blocked
source: router web interface, Firewall, WAN inbound rules, the rule named ssh
attested: 2026-07-20
lifetime_days: 90
```

| Key | Meaning |
|---|---|
| `value` | what was seen, as the text the documents state it in |
| `source` | where to look again: the interface, the page, the setting |
| `attested` | the day someone looked, written `YYYY-MM-DD` and unquoted |
| `lifetime_days` | how many days after `attested` the fact is trusted, a whole number |

Each fact is a file `facts/<name>.yaml`, directly in `facts/`. Anything else there, such as a
`.yml` file, a dot file or a directory, makes the check unknown, since the script would not read
it. Write each key once, on one line at the start of the line: the script reads `attested` and
`lifetime_days` line by line, not with a YAML parser, and a key written twice is unknown too. A
comment after a value is allowed.

`docs/network.md` states the value:

```markdown
# The home network

From the internet, port 22 is blocked at the router. Reach the hosts over the
VPN.
```

A rule tests the value, as any other: the fact file is YAML, and the `yaml` extractor reads it. A
rule cannot test the age, since its `assert` has no function that gives the current date, and it
gives the same verdict on every day. A program check can. `scripts/attested-age.sh` exits 1 when
a fact is older than its lifetime, prints each such fact, and exits 2, which errata reports as
unknown, when a fact has no date or lifetime it can read or `facts/` holds anything but facts:

```sh
# Exits 1 when a fact under facts/ is older than its lifetime, and 2 when a
# fact has no date or lifetime it can read, or facts/ holds anything but
# facts. ATTESTED_TODAY, as YYYY-MM-DD, stands in for the current date in UTC.
# The first line printed names the date used and where it came from.
if [ -n "${ATTESTED_TODAY:-}" ]; then
  today=$ATTESTED_TODAY from=ATTESTED_TODAY
else
  from="the clock, UTC"
  today=$(date -u +%Y-%m-%d) || { echo "date -u gave no current date"; exit 2; }
fi
echo "today is $today, from $from"
cd "$ERRATA_ROOT" || exit 2
for f in facts/* facts/.[!.]* facts/..?*; do
  [ -e "$f" ] || [ -L "$f" ] || continue
  case $f in
    facts/.*) ;;
    *.yaml) [ -f "$f" ] && continue ;;
  esac
  echo "$f: not a fact; each fact is a file facts/<name>.yaml"
  exit 2
done
exec awk -v today="$today" '
# The days since a fixed origin of a date written YYYY-MM-DD, or -1.
function day(s,   y, m, d, n) {
  if (s !~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$/) return -1
  y = substr(s, 1, 4) + 0; m = substr(s, 6, 2) + 0; d = substr(s, 9, 2) + 0
  if (m < 1 || m > 12) return -1
  n = substr("312831303130313130313031", 2 * m - 1, 2) + 0
  if (m == 2 && y % 4 == 0 && (y % 100 != 0 || y % 400 == 0)) n++
  if (d < 1 || d > n) return -1
  if (m <= 2) y--
  return 365 * y + int(y / 4) - int(y / 100) + int(y / 400) + int((153 * (m > 2 ? m - 3 : m + 9) + 2) / 5) + d
}
# The value of a line "key: value", without a trailing comment.
function field(s) {
  sub(/^[a-z_]+:[ \t]*/, "", s); sub(/[ \t]+#.*$/, "", s); sub(/[ \t]+$/, "", s)
  return s
}
BEGIN {
  now = day(today)
  if (now < 0) { print "today is not a date as YYYY-MM-DD"; exit 2 }
  status = 0
  for (i = 1; i < ARGC; i++) {
    file = ARGV[i]; at = ""; life = ""; ats = 0; lives = 0
    while ((r = (getline line < file)) > 0) {
      if (line ~ /^attested:/) { at = field(line); ats++ }
      if (line ~ /^lifetime_days:/) { life = field(line); lives++ }
    }
    if (r < 0) { print file ": cannot be read"; exit 2 }
    if (ats > 1 || lives > 1) { print file ": attested or lifetime_days is written twice"; exit 2 }
    close(file)
    then = day(at)
    if (then < 0) { print file ": no attested date as YYYY-MM-DD"; exit 2 }
    if (life !~ /^[0-9]+$/) { print file ": no lifetime_days as a whole number of days"; exit 2 }
    if (then > now) { print file ": attested " at ", after " today; exit 2 }
    if (now - then > life + 0) {
      print file ": attested " at ", " now - then " days old, past its lifetime of " life " days"
      status = 1
    }
  }
  exit status
}' facts/*.yaml
```

The script reads the current date in UTC, or the date in `ATTESTED_TODAY` when that is set. The
test behind this example sets it, so the example gives the same verdict on any day; leave it unset
in CI. errata hands a program check the environment of the job, so a variable left set in a shell
or a CI setting would move the date for good. The first line the script prints names the date and
where it came from, and errata shows it with every erratum and unknown; a check that holds has no
message to show. The script needs only `sh` and a POSIX `awk`. The two checks:

```yaml
checks:
  - id: network-doc-states-the-router-ssh-rule
    reason: >
      docs/network.md tells the reader whether the router lets SSH in from the internet. The
      router's firewall is set in its web interface, which no tool reads, so the attested fact is
      the record of what it is set to.
    scope: repo
    extract:
      doc:  {text: {file: docs/network.md, pattern: 'port 22 is (\S+) at the router'}}
      fact: {yaml: {file: facts/router-wan-ssh.yaml, path: value}}
    agree: [doc, fact]

  - id: attested-facts-are-current
    reason: >
      Each fact under facts/ records what someone saw where no tool can read, with the day they
      looked and how many days that look is trusted. A fact past its lifetime may no longer hold,
      and the rules that read its value would hold on a guess.
    scope: repo
    run: [sh, scripts/attested-age.sh]
```

On 3 November the fact is 106 days old, and its lifetime was 90:

```text
FALSE attested-facts-are-current (repo)
  why:  Each fact under facts/ records what someone saw where no tool can read, with the day they looked and how many days that look is trusted. A fact past its lifetime may no longer hold, and the rules that read its value would hold on a guess.
  said: today is 2026-11-03, from ATTESTED_TODAY
        facts/router-wan-ssh.yaml: attested 2026-07-20, 106 days old, past its lifetime of 90 days

1 erratum, 0 unknown. Coverage: ran 2, skipped 0.
```

The value still agrees, so only the age is an erratum. The fix is to look again: once the fact
says `attested: 2026-11-02`, both checks hold. A look that finds the port open makes the fact
current and the document wrong, and the rule on the value is then the erratum. The fact is trusted
up to and including its last day, here 18 October, and is an erratum from the next.

## The example config

[examples/errata.example.yaml](../examples/errata.example.yaml) shows every feature of errata
twice, over the files of a small made up service beside it. A comment above each check says the
claim it checks and the sections that describe it, and
[examples/README.md](../examples/README.md) describes the service. Run it in place, from a clone
of errata:

```bash
errata check --root examples --config examples/errata.example.yaml
```

Every check of scope `repo` holds. The `live` and `applied` checks are skipped, each with its gap
under the coverage line.
