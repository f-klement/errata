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
- Every pinned dependency, tool and base image kept current by Renovate, and a nightly scan of the
  released image that cuts a patch release when a fixable vulnerability appears.

## v0.2.0: rules in YAML

The baseline a checker of claims is compared on, and the release that makes most repo-scope checks
need no program at all.

- Rules: name extractors and state how their values relate.
  - `agree:` every value is equal, or with `compare: semver` every version agrees at the precision
    each one states, so `20`, `20.11.1` and `>=20` agree.
  - `assert:` a CEL expression holds, for relations beyond equality, such as a threshold that must
    lie between one backup interval and two.
- Extractors for YAML, JSON and TOML paths, regex captures, and prose in Markdown matched after
  line wraps, backticks and emphasis are normalised. Every extractor returns a list of values with
  their locations, and an extractor that finds nothing is unknown.
  - YAML, JSON and TOML share one path syntax, part of JSONPath, with filters by equality.
  - XML through a subset of XPath 1.0, for the tools that still keep their configuration in XML.
  - Markdown by structure: HTML comments, the text under a heading, fenced code blocks by language,
    and table cells; the same for HTML, AsciiDoc and reStructuredText.
  - Comments in source code, for the common language families, read by a lexer that knows each
    family's comment and string syntax.
  - Literal values in source code and in configuration written in a programming language, such as
    a version constant or a default port, read by one literal reader with a table row per
    language. errata reads configuration and never runs it, so no interpreter or runtime is added.
  - Environment variables where they are set: env files, shell scripts, GitLab CI, GitHub Actions
    and Jenkinsfiles. The CI readers return the value a job or step actually sees, resolved from
    local files only; a value that needs the network is unknown. An `env:` extractor asks every
    reader for one variable, so one rule checks that every place setting it agrees.
  - Infrastructure and service configuration: HCL (Terraform, Packer, Nomad), INI files (Ansible
    inventories, gitconfig, setup.cfg, tox.ini, php.ini), Java properties, nginx, Caddyfile and
    HAProxy, crontab schedules, OpenAPI documents, Protocol Buffers, SQL migrations, CSV and
    CODEOWNERS.
  - Evaluated configuration: Jsonnet and CUE, evaluated without network access.
  - Build files: Makefiles, Justfile, Taskfile and Ninja, CMake and Meson, Gradle, Maven, MSBuild,
    Autotools, Bazel, and the literal values in sbt, SCons, Rakefile and setup.py.
  - Dependency versions across manifests and lockfiles (Go, npm, pnpm, Yarn, Python, Cargo, Ruby,
    Composer, Maven, Gradle, NuGet), and toolchain version files such as `.tool-versions` and
    `.nvmrc`, so one rule checks that a version pinned in several places agrees.
- Markers: a claim tagged inline in a document or a comment, found anywhere in the repository and
  bound to the rule with the same id. A marker that no rule checks is an erratum.
- SARIF output, for inline annotations on GitHub.
- Worked examples: a document naming the wrong host, a derived number that drifted, a runbook step
  whose prerequisite is gone.
- First real user: an existing suite of 64 invariant checks from a GitOps repository, moved under
  errata, then converted to rules where they fit.

## v0.3.0: Kubernetes, and fields git cannot remove

The feature no other tool ships.

- Plugins as separate executables, built and released from this repository and installed on
  demand by an errata command. The release starts by deciding, for every integration, those
  already shipped as well as those planned, which stay in the core binary and which become plugins.
- A Kubernetes provider that renders (Helm and Kustomize, in process), reads live objects, and
  predicts an apply with a server-side dry run. errata never applies anything.
- The `live` and `applied` scopes.
- **Fields git cannot remove:** every field another field manager keeps alive after git deleted it,
  with that manager named.
- Applies that cannot succeed: immutable field changes and admission rejections, before the push.
- A running image that is not the pinned image.
- `--base`, the diff range, detected in GitLab, GitHub Actions and Jenkins.
- Extractors for Dockerfiles, Docker Compose (the effective service configuration), Helm charts and
  values, Kustomize files, and Kubernetes `env:` entries as one more reader for `env:`.
- Kubernetes manifests read as objects: selected by kind, namespace and name across files, and
  containers by name. Argo CD and Flux resources, Helmfile, Skaffold, Tilt and DevSpace, buildx bake
  files and `.dockerignore`, cluster configs (kind, k3d, minikube, k3s, kubeadm, Talos), Carvel ytt
  and OpenShift Templates, and the keys and recipients of SOPS files and Sealed Secrets, whose values
  are never decrypted.
- Container image references parsed once for every place an image is named, with `compare: image`
  so a Dockerfile, a compose file, a manifest and Helm values agree on one image.
- Helm template references: every value a template reads exists, and every value is read.
- Nix: the literal values in flakes and Nix files, and the pins in `flake.lock`.

## v0.4.0: verdicts on drift, and a second provider

- The reconciler model: whether a reconciler runs, how often, whether it prunes and whether it
  repairs a hand edit. It turns drift into a verdict, so a difference Argo CD repairs in minutes is
  not reported and a field nobody can remove is.
- A bare metal and systemd provider: files over SSH, `systemctl show`, and the operating system
  vendor as a competing writer.
- systemd unit files as an extractor, with drop-ins and timers, so the unit in git is compared with
  what `systemctl show` reports.
- The configuration files of popular programming languages, read as the tool reads them: JSONC and
  JSON5, `tsconfig` and ESLint inheritance, Prettier and Babel, bundler and test runner configs
  written as code, Python tool settings in each tool's lookup order, Spring Boot profiles,
  EditorConfig, and the Go, Rust, Ruby, PHP, Swift and .NET toolchain files.
- The configuration of their most popular frameworks: Django, Flask, FastAPI and Celery; Next.js,
  Nuxt, Angular, SvelteKit, NestJS and Astro; Rails; Laravel and Symfony; ASP.NET Core; Play, Akka
  and Ktor through HOCON; Phoenix; Rocket and Tauri; Flutter, Android, iOS and React Native; the
  documentation site generators MkDocs, Docusaurus, Hugo, Jekyll and Sphinx; and vcpkg, Conan and
  qmake. A setting written in the framework's own language is read as a literal, never executed.
- Checks in Starlark, if the program boundary proves too clumsy in practice.

## v0.5.0: refactor and hardening

No new features. Deepen what four releases have bent, and harden what CI users depend on.

## Later

- Attested facts: a value no API can read, recorded with its source and date, checked for both.
- Whether every check must prove it can fail, with a fixture on which it reports false.
