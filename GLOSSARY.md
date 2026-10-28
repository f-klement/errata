# Glossary

The words this project uses, each with one meaning. Use these words in code, in documents and in
output. Do not introduce a synonym for a word that is already here.

## Core

**Claim.** A statement the repository makes. It can be true or false. Examples: a sentence in a
README that names a host, a threshold in one file that depends on a schedule in another, a field in
git that is supposed to be absent from the live system.

**Check.** One test of one claim. A check has an identifier, a reason, a scope, and a result.

**Reason.** The text that says why a check exists and what breaks without it. Every check must have
one. A check without a reason does not load. Its author writes it. Why a check was skipped is a
gap, not a reason.

**Verdict.** The result of a check that ran: true, false or unknown. A verdict says whether the
claim holds. It is not the same as an observation, which only says that a difference exists.

**Unknown.** The verdict of a check that could not evaluate. A program check is unknown when its
program crashed, did not finish in time, or could not be found. A rule is unknown when an extractor
could not read its file (a file that is not there is no such case for `exists`, whose value it
is: `false`), reached a limit such as the file size or the regex time, or found no value
while `allow_empty` does not list it. A rule is also unknown when an extractor returned a not
literal value while `skip_not_literal` does not list it. An agree rule is also unknown when it has
no value at all, or one value while no extractor that `allow_empty` lists came back empty. An assert
rule is also unknown when its expression could not decide: an error anywhere in the expression while
it ran, or its cost or time limit reached. The claim was not shown false, so an unknown is not an
erratum. It still fails the run, because a suite that passes on what it could not test passes on a
subset. Do not call it an error.

**Required list.** The ids a config lists under its top-level `required` key: the checks it must
hold. A config whose checks are not exactly those ids does not load, so a check deleted from the
file stops the run before anything runs. It says which checks the config holds, not which ones
ran: a skipped check is still on it.

**Errata.** The output of a run: the claims its checks showed to be false. One of them is an
erratum. A run that ends with an unknown and no errata still fails.

## Scopes

A scope says what a check needs in order to run. Each check declares exactly one.

**Repo scope.** The check reads only files. It needs no access to any running system.

**Live scope.** The check reads the running system and compares it to the files.

**Applied scope.** The check asks what the running system would contain after an apply, and
compares that to the files. It never applies anything.

## Systems

**Live system.** The running system a repository describes. A Kubernetes cluster, a set of Docker
containers, a host.

**Provider.** The part of errata that talks to one kind of live system. A provider implements up to
three operations and declares which ones it has.

**Render.** The provider operation that turns the files into the objects they describe.

**Read.** The provider operation that fetches the current state of the live system.

**Predict.** The provider operation that reports what an apply would produce, without applying it.

**Attested fact.** A value that no interface can read, recorded by hand with a source and a date. A
check on an attested fact tests both the value and its age. Use this for a setting that exists only
in a vendor's web interface.

## Reconciliation

**Reconciler.** A program that reads git and changes the live system to match it. Argo CD and Flux
are reconcilers.

**Reconciler model.** The four facts errata stores about a provider's reconciler: whether one
exists, how often it runs, whether it deletes objects absent from git, and whether it repairs a
change made by hand. These four facts turn an observation into a verdict.

**Drift.** The live system differs from the files. Drift alone is an observation. It becomes a
verdict only after the reconciler model is applied to it.

**Competing writer.** A second party that writes the same fact as git. Git can lose to it silently.
In Kubernetes it is another field manager. In Terraform it is a change made outside Terraform. On a
host it is the operating system vendor, whose update reverts a setting.

**Field manager.** The recorded name of the writer of one field on a Kubernetes object. Kubernetes
stores these names in `metadata.managedFields`. Two managers can own one field.

## Rules

**Rule.** A check the user writes in YAML. It names extractors and states how their values must
relate: all agree, as text or as versions, or a CEL expression that must hold.

**Program check.** A check the user writes as a program, run through the check protocol. Every check
is either a rule or a program check. Do not call it a script, an exec check or a command check.

**Default run.** The `run` a program check without one of its own takes, from the top level
`defaults` key, with the check's id put in for its placeholders. It is a program errata starts, as
any `run` is.

**Extractor.** The part of a rule that gets values from one place. A YAML path, an XPath into an
XML file, a key in an INI, properties or env file, a regular expression capture, a sentence of
Markdown prose, the versions of a tool in the files that pin it, the version a package gives
itself in each of its manifests, one variable in every file a rule lists, whether a file is there and its size in bytes, an image
digest, a field on a live object.

**Embedded document.** A document a string of another file holds, such as a runner's
`config.toml` in a Helm value or a JSON list in an env var's value. A `yaml`, `json` or `toml`
extractor reads one through its `embedded` steps, each naming the string's format and a path in
it.

**File set.** The files an extractor's `files:` names: each entry a file, or a glob matched against
the files git tracks, where `**` matches any number of path segments. The extractor reads each file
as it would read one, and each value keeps its own file, which `by_file` groups by in an assert.

**Pin.** A file's statement of the version of a tool the repository uses, such as `20.11.1` in
`.nvmrc` or `nodejs 20.11.1` in `.tool-versions`. The `tool_version` extractor reads every pin of
one tool. A range a tool must satisfy, such as `"node": ">=20"` under `engines` in package.json, is
not a pin.

**Range.** A statement of the versions a tool accepts, such as `>=20` or `^20.1` under `engines`
in package.json, or `~> 3.2` in a Gemfile. An agree rule with `compare: semver` reads each value
as a version or a range: every version among the values must satisfy every range, and ranges
with no version beside them agree when one release satisfies them all.

**Precision.** How many numbers a version states. A version of one or two numbers, such as `20` or
`20.11`, stands for every release that starts with them, so under `compare: semver` it agrees with
any version that starts with the same numbers. A version of three numbers or more, or with a
pre-release, stands for itself.

**Not literal.** A value a file only computes, such as `E_ALL` in php.ini, which PHP reads as a
number, or `{posargs}` in tox.ini. Its text is what the file wrote, not what the file means, so a
rule never compares it. A rule that lists the extractor in `skip_not_literal` leaves such values
out; any other rule with one is unknown. The `ini` extractor marks them, `properties` in a
template file, `tool_version` a version such as `lts/*` or `system`, which a version manager
resolves, `package_version` a version the build computes, such as `dynamic = ["version"]` in
pyproject.toml, `envfile` a value whose `${OTHER}` names a key the file does not set before it,
`gitlab-ci` a variable that names another, such as `$CI_COMMIT_SHA`, or a number GitLab's YAML
loader retypes, such as `3.10`, `shell` a value the shell computes and errata does not
resolve, such as `$(git describe)` or `"$HOME/bin"`, `github-actions` a `${{ }}` expression
other than a matrix variable, or a number YAML retypes, `jenkins` a value Groovy computes,
such as `credentials('id')` or `"${BUILD_NUMBER}"`, and `env` any of these that the reader of its
file marks.

**Condition.** What must hold for a value to be the one a reader names, for a reader that returns
one value for each case a file chooses between: a GitLab CI rule's `if $CI_COMMIT_TAG`, a
`parallel:matrix` instance such as `build: [a]`, or a GitHub Actions matrix job such as
`test (ubuntu-latest, 18)`. A rule compares the value as any other; a message
names its condition after its place.

**Check protocol.** The way errata runs a program check. errata runs the
program and reads its result. The program can be in any language.

**Gap.** What a run lacks to reach a check's scope: a provider operation that no available provider
has, for example read for a live check. A check whose scope the run cannot reach is skipped, and its
gap is printed in the coverage. A skip does not fail the run unless `errata check` is given
`--fail-on-skip`.

**Coverage.** The count of checks that ran and the count that were skipped, with the gap for each
skip. errata prints coverage on every run.
