# Roadmap

What each release will contain, in the order they ship. The words are defined in
[GLOSSARY.md](GLOSSARY.md). Plans change; a release is what its tag contains.

## v0.1.0: the check runner

A complete tool for running checks in CI, with nothing yet that needs a running system.

- Program checks in any language, run through the check protocol.
- A required reason on every check, printed beside every failure.
- Fail closed: a check that cannot evaluate is unknown, and an unknown fails the run.
- Coverage on every run, with the gap behind each skipped check.
- Text and JUnit output; binaries for six platforms and a container image.

## v0.2.0: rules in YAML

The baseline a checker of claims is compared on, and the release that makes most repo-scope checks
need no program at all.

- First: every pinned dependency, tool and base image kept current by Renovate, so the CI image
  and the release image pick up security fixes without anyone editing a version by hand.
- Rules: name extractors and state how their values relate.
  - `agree:` every value is equal.
  - `assert:` a CEL expression holds, for relations beyond equality, such as a threshold that must
    lie between one backup interval and two.
- Extractors for YAML, JSON and TOML paths, regex captures, and prose in Markdown matched after
  line wraps, backticks and emphasis are normalised. Every extractor returns a list of values with
  their locations, and an extractor that finds nothing is unknown.
- SARIF output, for inline annotations on GitHub.
- Worked examples: a document naming the wrong host, a derived number that drifted, a runbook step
  whose prerequisite is gone.
- First real user: an existing suite of 64 invariant checks from a GitOps repository, moved under
  errata, then converted to rules where they fit.

## v0.3.0: Kubernetes, and fields git cannot remove

The feature no other tool ships.

- A Kubernetes provider that renders (Helm and Kustomize, in process), reads live objects, and
  predicts an apply with a server-side dry run. errata never applies anything.
- The `live` and `applied` scopes.
- **Fields git cannot remove:** every field another field manager keeps alive after git deleted it,
  with that manager named.
- Applies that cannot succeed: immutable field changes and admission rejections, before the push.
- A running image that is not the pinned image.
- `--base`, the diff range, detected in GitLab, GitHub Actions and Jenkins.

## v0.4.0: verdicts on drift, and a second provider

- The reconciler model: whether a reconciler runs, how often, whether it prunes and whether it
  repairs a hand edit. It turns drift into a verdict, so a difference Argo CD repairs in minutes is
  not reported and a field nobody can remove is.
- A bare metal and systemd provider: files over SSH, `systemctl show`, and the operating system
  vendor as a competing writer.
- Checks in Starlark, if the program boundary proves too clumsy in practice.

## v0.5.0: refactor and hardening

No new features. Deepen what four releases have bent, and harden what CI users depend on.

## Later

- Attested facts: a value no API can read, recorded with its source and date, checked for both.
- Whether every check must prove it can fail, with a fixture on which it reports false.
