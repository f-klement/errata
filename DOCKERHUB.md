# errata

[![license](https://img.shields.io/github/license/f-klement/errata)](https://github.com/f-klement/errata/blob/main/LICENSE)
[![release](https://img.shields.io/github/v/tag/f-klement/errata?label=release)](https://github.com/f-klement/errata/releases)
[![docker hub](https://img.shields.io/github/v/release/f-klement/errata?label=docker%20hub&logo=docker)](https://hub.docker.com/r/fklement/errata)
[![docker pulls](https://img.shields.io/docker/pulls/fklement/errata)](https://hub.docker.com/r/fklement/errata)
[![Ko-fi](https://img.shields.io/badge/Ko--fi-buy%20me%20a%20coffee-FF5E5B?logo=kofi&logoColor=white)](https://ko-fi.com/fklement)

Like errata or find it useful? Offer me a coffee. All features are free either way.

A checker for the claims a repository makes about itself: a document that names the wrong host, a
command a runbook offers that no longer exists, a threshold that no longer matches the schedule it
watches, a version pinned in two files that disagree. Built first for GitOps repositories, and
useful in any repository whose claims can be read from its files, its git history or the system it
runs.

Each check is a program. errata runs it, prints every claim shown false with the reason the check
was written, and says how many checks ran, how many it skipped, and the gap behind each skip.

**Source, issues and the full documentation:**
[github.com/f-klement/errata](https://github.com/f-klement/errata). This page covers running the
image.

## Run it

Mount the repository and make it the working directory:

```bash
docker run --rm -v "$PWD:/repo" -w /repo fklement/errata check
```

It reads `errata.yaml` from the repository root. A minimal one:

```yaml
checks:
  - id: readme-names-the-current-port
    reason: The service moved to port 8443. A README that still says 8080 sends people to nothing.
    scope: repo
    run: [bash, -c, '! grep -n "8080" README.md']
```

A check exits 0 when its claim is true and 1 when it is false. Anything else, a crash or a timeout,
is unknown: not an erratum, since the claim was not shown false, but it fails the run.

## In CI

GitLab CI:

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

## Configuration

| Flag | Default | Meaning |
|---|---|---|
| `--root` | `.` | the repository root, and the checks' working directory |
| `--config`, `-c` | `<root>/errata.yaml` | the config file |
| `--format`, `-f` | `text` | `text`, or `junit` for CI |

| Exit code | Meaning |
|---|---|
| 0 | every check that ran found its claim true |
| 1 | at least one erratum (a claim shown false) or unknown (a check that could not evaluate) |
| 2 | bad flags, or a config that does not load |

### What is in the image

- `errata`, a static Go binary, on `PATH`.
- Alpine Linux with bash 5.3, so a check can be a bash or POSIX shell script using `grep`, `sed`,
  `awk` and `find`.
- It runs as root, so a CI job can write its report into the workspace. Pass `--user` to
  `docker run` to run it as yourself.

### How it is built

Releases are built with [ko](https://ko.build) from the `kos:` section of
[`.goreleaser.yaml`](https://github.com/f-klement/errata/blob/main/.goreleaser.yaml): errata
compiled into a static binary on top of `bash:5.3-alpine3.24`, pinned by digest, for `linux/amd64`
and `linux/arm64`, with an SPDX SBOM per platform. Each release builds it from the public source at
its tag.

The [`Dockerfile`](https://github.com/f-klement/errata/blob/main/Dockerfile) builds the same image
with `docker build -t errata .`: the same base images, binary path, user and entrypoint, without the
SBOM. CI builds it on every change and refuses a base image that differs from the release's.

### Adding tools

A check that needs another program needs an image that has it:

```dockerfile
FROM fklement/errata:latest
RUN apk add --no-cache git python3 jq
```

## Upgrading

```bash
docker pull fklement/errata:latest
```

Pin a version rather than tracking `latest` if you would rather choose when that happens. A version
tag never changes after it is pushed. Patch releases ship automatically when a fixed HIGH or
CRITICAL vulnerability turns up in the last release.

## Tags

| Tag | Meaning |
|---|---|
| `latest` | the newest release |
| `X.Y.Z` | one release |

## Notes

- **errata never applies anything.** Checks against a running system are designed to be read only,
  and the planned dry run scope will use dry runs only.
- **It is not a language linter.** Rules that need a language's parser or types belong in that
  language's linter. errata checks what files, git and running systems say.
- Images are published for `linux/amd64` and `linux/arm64`, each with an SPDX SBOM.

## License

MIT.
