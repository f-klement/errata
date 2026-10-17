# errata

[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![release](https://img.shields.io/github/v/tag/f-klement/errata?label=release)](https://github.com/f-klement/errata/releases)
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
- **Output for people and for CI.** Readable text, or JUnit XML that GitLab, GitHub Actions and
  Jenkins render per check.
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
- **Rules in YAML** that bind values from several files and assert they agree.
- **Attested facts:** a value no API can read, recorded with a source and a date, checked for both.

The words above have one meaning each, fixed in [GLOSSARY.md](GLOSSARY.md).

## What belongs in errata

Ask where the value a check compares comes from.

- **From a file read as text, from YAML, JSON or TOML, from git, or from a live system:** it is an
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

Every check in `errata.yaml` has these keys:

| Key | Required | Meaning |
|---|---|---|
| `id` | yes | lower-case words joined by single hyphens, unique in the file |
| `reason` | yes | why the check exists and what breaks without it |
| `scope` | yes | `repo`, `live` or `applied` |
| `run` | yes | the command, as a list of arguments, run from the repository root |
| `timeout` | no | a Go duration such as `10s` or `2m`; default `30s` |

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
errata check [--root DIR] [--config FILE] [--format text|junit]
errata version
```

| Flag | Default | Meaning |
|---|---|---|
| `--root` | `.` | the repository root, and the checks' working directory |
| `--config`, `-c` | `<root>/errata.yaml` | the config file |
| `--format`, `-f` | `text` | `text` or `junit` |

| Exit code | Meaning |
|---|---|
| 0 | every check that ran found its claim true |
| 1 | at least one erratum (a claim shown false) or unknown (a check that could not evaluate) |
| 2 | bad flags, or a config that does not load |

### In CI

errata has no integration with any CI system, on purpose: it is one binary that exits non-zero and
writes JUnit. GitLab CI:

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
| `make fuzz` | the fuzz targets for the config parser, the check protocol and the JUnit writer |
| `make mutation` | Gremlins: changes one operator at a time and fails if the tests still pass |
| `make lint` | golangci-lint with the linters listed, and justified, in `.golangci.yml` |
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
| `internal/check` | the domain: check, scope, verdict, result, report |
| `internal/config` | reads `errata.yaml`, strictly |
| `internal/engine` | decides which checks run and which are skipped |
| `internal/protocol` | runs a check program and turns its exit code into a verdict |
| `internal/provider` | the interface for running systems, and the reconciler model |
| `internal/report` | the text and JUnit outputs |
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
