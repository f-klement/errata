# Roadmap

What each release will contain, in the order they ship. The words are defined in
[GLOSSARY.md](GLOSSARY.md). Plans change; a release is what its tag contains.

Each release has one theme. The v0.2 line ships as point releases. Integrations built as plugins
release on their own tags, apart from this list; the section on plugins says which those are.

## v0.1.0: the check runner

A complete tool for running checks in CI, with nothing yet that needs a running system.

- Program checks in any language, run through the check protocol.
- A required reason on every check, printed beside every failure.
- Fail closed: a check that cannot evaluate is unknown, and an unknown fails the run.
- Coverage on every run, with the gap behind each skipped check.
- Text and JUnit output; binaries for six platforms and a container image.
- Every pinned dependency, tool and base image kept current by Renovate, and a nightly scan of the
  released image that cuts a patch release when a fixable vulnerability appears.

## v0.2.0: the rule layer, the CI readers and env:

The baseline a checker of claims is compared on, and the release that makes most repo-scope checks
need no program at all.

- Rules: name extractors and state how their values relate.
  - `agree:` every value is equal, or with `compare: semver` every version agrees at the precision
    each one states, so `20`, `20.11.1` and `>=20` agree.
  - `assert:` a CEL expression holds, for relations beyond equality, such as a threshold that must
    lie between one backup interval and two.
  - A value a reader cannot read as written is not literal, and makes the rule unknown unless the
    rule leaves that extractor's not literal values out.
- Extractors for YAML, JSON and TOML paths, regex captures, and prose in Markdown matched after
  line wraps, backticks and emphasis are normalised. Every extractor returns a list of values with
  their locations, and an extractor that finds nothing is unknown.
  - YAML, JSON and TOML share one path syntax, part of JSONPath, with filters by equality.
  - XML through a subset of XPath 1.0, INI files (Ansible inventories, gitconfig, setup.cfg,
    tox.ini, php.ini), Java properties, toolchain version files such as `.tool-versions` and
    `.nvmrc`, and the package's own version in package.json, pyproject.toml, Cargo.toml and
    `VERSION`.
  - Environment variables where they are set: env files, shell scripts, GitLab CI, GitHub Actions
    and Jenkinsfiles. The CI readers return the value a job or step actually sees, resolved from
    local files only; a value that needs the network is unknown. An `env:` extractor asks every
    reader for one variable, so one rule checks that every place setting it agrees.
- SARIF output, for inline annotations on GitHub, and `errata list`, the checks a config loads as
  JSON.
- A gate that cannot pass on less than it should: a config names the check ids that must run and
  fails on a missing or extra one, and a flag turns a skipped check into a failed run.
- Program check defaults: one `run` for every program check, with the check's id substituted.
- Worked examples: a document naming the wrong host, a derived number that drifted, a runbook step
  whose prerequisite is gone.
- First real user: an existing suite of 64 invariant checks from a GitOps repository, moved under
  errata, then converted to rules where they fit.

## v0.2.1: claims across many files

The checks a GitOps repository still writes as programs because one file is not enough.

- Globs for the regex, YAML and JSON extractors, with the values kept per file, so one rule
  covers every Application or every manifest under a directory.
- CEL string functions: split, join and replace.
- Filters that test membership, existence and nesting, such as the item whose list holds a name.
- Paths into a YAML or TOML document carried inside a string, such as a config in a Helm value.
- A file's existence and its size as values, so a rule can say a file must not come back.
- Config loading cleaned up first: one decode of errata.yaml, and defaults applied while a
  check's own keys are still known, so a later default can tell an unset key from a set one.
- A rule's empty `run` is refused, as its `timeout` already is, since neither key does anything
  on a rule. This is a breaking change: a config that loads today with one is turned away.
- Version files that start with a byte order mark parse, as npm, Cargo and the version managers
  accept them.
- `errata check --output junit=errata.xml` writes a machine format to a file, repeatable, while
  the text report still goes to stdout, so one run feeds the pipeline and the log.

## v0.2.2: records and derived patterns

Claims about how values sit together, not only what they are.

- Lists of objects handed to CEL as maps, in file order, for rules where a later entry overrides
  an earlier one.
- Fields of one element kept together: a regex match's named groups, or several fields of one
  list item, as one record.
- A regex built from another extractor's values, each quoted as a literal.
- A regex match with the lines around it, so a rule can require or refuse a context. Go's regex
  engine has no lookaround, and this keeps matching in linear time.
- A generated file is current: a committed file equals what its generator prints. This is the one
  check kind besides a program check that runs anything, and an ADR sets its limits.

## v0.2.3: consolidation before plugins

No new features. The places where a new extractor, check kind or reader is added today each hold
a case per kind; this release makes each of them one table, so the readers of the later v0.2
releases and the plugins of v0.3.0 are added in one place.

- Each kind of extractor is registered once, and a rule's extract entry finds its kind by its
  key, so a key no installed reader knows can be named in an unknown result.
- An extractor describes what it returns once (flat values, maps or records, which are not
  literal, which other extractors it reads), and config, rules and reports read that description.
- Where an extractor reads is one part every reader shares, and one read of a file serves every
  reader of it.
- The bounds on what an extractor returns hold where its values reach the rule, and extraction
  can be cancelled.
- One function starts every program errata runs, with its input, bounds, timeout and process
  group.
- A check's kind is decided once at load and dispatched from one table.
- The readers `env:` asks come from the registry of kinds.
- A file over its size limit is recognised by its error, not by the words of its message.
- CI's lists of readers and fuzz targets come from the tree, and mutation floors sit in one table.

## v0.2.4: docs claims

The claims documentation makes, read by its structure.

- Markdown by structure: HTML comments, the text under a heading, fenced code blocks by language,
  and table cells; the same for HTML, AsciiDoc and reStructuredText.
- Comments in source code, for the common language families, read by a lexer that knows each
  family's comment and string syntax.
- Markers: a claim tagged inline in a document or a comment, found anywhere in the repository and
  bound to the rule with the same id. A marker that no rule checks is an erratum.

## v0.2.5: literals and dependencies

Values written in code, and one version pinned in many places.

- Literal values in source code and in configuration written in a programming language, such as
  a version constant or a default port, read by one literal reader with a table row per
  language. errata reads configuration and never runs it, so no interpreter or runtime is added.
- Dependency versions across manifests and lockfiles (Go, npm, pnpm, Yarn, Python, Cargo, Ruby,
  Composer, Maven, Gradle, NuGet), so one rule checks that a package pinned in several places
  agrees.
- A package's version read from the git tag on HEAD, and unknown on a checkout with no tags.
- A TOML date written as a version is refused, as Cargo and the Python tools refuse it.

## v0.2.6: host config and formats

The files a host is configured with, and the formats the earlier readers left open.

- systemd unit files, with drop-ins and timers.
- Unix configuration files: sshd, sysctl, fstab, hosts, sudoers and the rest of `/etc`.
- JSONC and JSON5: JSON with comments and trailing commas.
- A `syntax:` key on `compare: semver` reads ranges as npm, Cargo, PEP 440, Go or RubyGems does.
- INI and properties values read closer to their tools: fewer values marked not literal that a
  tool reads as written, template blocks that decide whether a key exists, and values the
  consuming tool computes marked as not literal.

## v0.3.0: the plugin system

errata stays one small binary, and further integrations install on demand.

- A plugin is a separate executable, built and released from this repository, spoken to over
  stdin and stdout through a versioned protocol that an architecture decision record defines.
- `errata plugin install` fetches a plugin into an on-disk cache. `errata.lock` pins each plugin's
  version and checksum, so one commit always gives one verdict, and a vendored plugin directory
  lets CI run offline.
- Every plugin is checked against the checksums published with its release before it runs.
- A rule that needs a plugin that is not installed is unknown, naming the plugin.
- The CSV and HCL (Terraform, Packer, Nomad) extractors are the first two plugins.

## v0.4.0: container and manifest readers

The files that describe containers and clusters, read in core.

- Dockerfiles and Docker Compose (the effective service configuration).
- Kubernetes manifests read as objects: selected by kind, namespace and name across files, and
  containers by name. Kubernetes `env:` entries are one more reader for `env:`.
- Container image references parsed once for every place an image is named, with `compare: image`
  so a Dockerfile, a compose file, a manifest and Helm values agree on one image.
- Argo CD and Flux resources: sources, revisions and chart versions.
- `--base`, the diff range, detected in GitLab, GitHub Actions and Jenkins.

## v0.4.1: fields git cannot remove

The feature no other tool ships.

- The `live` and `applied` scopes: errata runs a check that needs the running system through an
  installed provider plugin, and a check no installed plugin can run is skipped with its gap.
- **Fields git cannot remove:** every field another field manager keeps alive after git deleted it,
  with that manager named.
- Applies that cannot succeed: immutable field changes and admission rejections, before the push.
- A running image that is not the pinned image.
- Beside it, the errata-ext-kubernetes plugin v0.1.0: a Kubernetes provider that renders (Helm and
  Kustomize, in process), reads live objects, and predicts an apply with a server-side dry run,
  with extractors for Helm charts and values and for Kustomize files. errata never applies
  anything.

## v0.5.0: drift verdicts

- The reconciler model: whether a reconciler runs, how often, whether it prunes and whether it
  repairs a hand edit. It turns drift into a verdict, so a difference Argo CD repairs in minutes is
  not reported and a field nobody can remove is.
- Checks in Starlark, if the program boundary proves too clumsy in practice.
- Beside it, the errata-ext-baremetal plugin: a bare metal and systemd provider reading files over
  SSH and `systemctl show`, with the operating system vendor as a competing writer.

## v0.6.0: refactor and hardening

No new features. Deepen what the earlier releases have bent, and harden what CI users depend on.

## Plugins

The plugin protocol ships in v0.3.0. After that, each plugin is released on its own tag, apart
from the releases above, and a plugin that is not scheduled has no release yet.

A plugin is built when a real repository needs it: one errata already checks, or one a user names
in an issue. Otherwise the smallest plugin is built first.

These integrations are core and never need a plugin: YAML, JSON, TOML, regex, CEL (with globs,
records, string functions and the other features of v0.2.1 and v0.2.2), INI, XML, Java
properties, version files, package versions, Markdown prose, env files and `env:`, shell, the
GitLab CI, GitHub Actions and Jenkins readers, Markdown structure, code comments, markers, the
literal reader, source code literals, HTML, AsciiDoc and reStructuredText, dependency versions,
systemd units, Unix configuration files, JSONC and JSON5, Dockerfiles, Compose, Kubernetes
manifests and their `env:` entries, image references, GitOps resources, and Starlark.

Planned as plugins, with no release yet:

- Service and infrastructure configuration: nginx, Caddyfile and HAProxy; crontab schedules;
  OpenAPI documents; Protocol Buffers; SQL migrations; CODEOWNERS.
- Evaluated configuration: Jsonnet and CUE, evaluated without network access, and the literal
  values in Nix files with the pins in `flake.lock`.
- Build files: Makefiles, Justfile, Taskfile and Ninja, CMake and Meson, Gradle, Maven, MSBuild,
  Autotools, Bazel, and the literal values in sbt, SCons, Rakefile and setup.py.
- The configuration files of popular programming languages, read as the tool reads them:
  `tsconfig` and ESLint inheritance, Prettier and Babel, bundler and test runner configs written as
  code, Python tool settings in each tool's lookup order, Spring Boot profiles, EditorConfig, and
  the Go, Rust, Ruby, PHP, Swift and .NET toolchain files.
- The configuration of their most popular frameworks: Django, Flask, FastAPI and Celery; Next.js,
  Nuxt, Angular, SvelteKit, NestJS and Astro; Rails; Laravel and Symfony; ASP.NET Core; Play, Akka
  and Ktor through HOCON; Phoenix; Rocket and Tauri; Flutter, Android, iOS and React Native; the
  documentation site generators MkDocs, Docusaurus, Hugo, Jekyll and Sphinx; and vcpkg, Conan and
  qmake. A setting written in the framework's own language is read as a literal, never executed.
- Around Kubernetes: Helm template references (every value a template reads exists, and every
  value is read), Helmfile, Skaffold, Tilt and DevSpace, buildx bake files and `.dockerignore`,
  cluster configs (kind, k3d, minikube, k3s, kubeadm, Talos), Carvel ytt and OpenShift Templates,
  and the keys and recipients of SOPS files and Sealed Secrets, whose values are never decrypted.

## Later

- Attested facts: a value no API can read, recorded with its source and date, checked for both.
- Whether every check must prove it can fail, with a fixture on which it reports false.
