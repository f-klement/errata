# Worked examples

Four claims a repository commonly gets wrong, each written as errata checks. Each example shows
the files, the checks, what `errata check` prints while the claim is false, and the fix that makes
it hold. Every block on this page is a section of a test: each example is an API test in
`internal/cli/testdata/script`, which runs the checks on the files shown here and then on the fixed
ones, and errata's own suite fails when a block on this page differs from the test's section.

## A document names the wrong port

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
[Comparing versions](../README.md#comparing-versions)), and a path.

## A derived number drifted from what derives it

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

## A procedure's prerequisite is no longer declared

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

A file the runbook names is the case a rule cannot state today. An extractor reads values out of
a file, and no extractor says whether a file exists: a rule over a file that is gone is unknown,
which fails the run without being an erratum, and the message says the file could not be read.
Until errata has a kind for it, a program check states the claim:

```yaml
checks:
  - id: runbook-sysctl-file-exists
    reason: >
      The runbook has the operator check the kernel parameters that the node role's sysctl file
      sets. If the file is gone, the role sets none of them, and every step that checks one fails.
    scope: repo
    run: [test, -f, ansible/roles/node/files/99-node.conf]
```

It holds while the file is there and is an erratum once it is gone. A program check is code:
[Checks](../README.md#checks) says what it runs with, and what that means on a ref you do not
trust. `test -f` is not held to the repository as an extractor's `file` is: it takes an absolute
path and a `..` that leaves the root, and follows a symlink wherever it points. Name a path inside
the repository.

## An attested fact is older than its lifetime

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
