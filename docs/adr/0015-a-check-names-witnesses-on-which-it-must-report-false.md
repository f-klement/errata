# A check names witnesses on which it must report false

A check that cannot fail passes on every repository. A regex that never matches under
`allow_empty`, an assert that is true of any list, a program that reads the directory it was
started in rather than the tree it was handed: each goes green, and a suite of them goes green on
nothing. CODING_STANDARDS.md asks that every guard of errata's own be seen failing on a planted
break, and Endpaper's testing doctrine asks the same of property tests. errata gave a config's
checks no way to do the same, so a check may now name witnesses:

```yaml
- id: go-toolchain-agrees
  scope: repo
  extract: {...}
  agree: [mod, image]
  witness: [toolchain-drifts]   # .errata/witness/toolchain-drifts/
```

A check may name witnesses, trees under `.errata/witness/` on which it must report false. Every
`errata check` runs them; a witness the check holds on is an erratum, and an unknown fails the
run. A rule takes the fixture as its root, with the real index cut to its prefix; a program runs
from the real root with `ERRATA_ROOT` at the fixture. Globs and walks of a real run skip the tree.

## The rules

- `witness` is a key of every check beside `id` and `scope`, so every kind of check takes it. It
  lists one to eight names, each lower-case words joined by single hyphens, as an id is, so a name
  is one segment with no dot, slash or space. A name listed twice, an empty list, a bare string and
  a ninth name do not load. It loads only on scope `repo`: files cannot stand in for a live system.
- A fixture is the directory `.errata/witness/<name>/` of the repository, which holds the files
  the check reads, laid out as in the repository. errata reads no `.git` in it. A fixture that is
  not a directory, or is reached through a symlink, wherever the symlink leads, makes the witness
  unknown: a symlink at `.errata`, at `.errata/witness` or at the fixture. A symlink that stays
  inside the repository would put the fixtures at a path the index lists under another name,
  which a real run's globs and walks do not pass over.
- Each witness runs after its check, whatever the check's verdict, and adds one result named
  `id [witness name]`: true, which the coverage counts as held, when the check reports false; an
  erratum, `id held on its witness name, so it could not be shown to fail`, when the check holds;
  unknown, with the check's message, when the check is unknown. Text, JUnit and SARIF carry the
  name; SARIF puts an erratum at the check in the config, since the planted break is no place of
  the repository a reader should fix. A witness is not a check: the coverage's count of checks
  that ran leaves it out and its own count, `N witnesses held`, follows. The exit codes do not
  change.
- A rule runs with the fixture as its root, so a `file:` reads the fixture's file and every bound
  of ADR 0003 holds as on the repository. A `files:` glob matches the entries of the repository's
  index under `.errata/witness/<name>/`, with that prefix cut off: the files the fixture commits,
  and never an untracked one, as in CI (ADR 0008). The index is read under its own bound, and
  what is left is a part of it, so the bounds of a glob hold too.
- A program check and a generated check's generator start in the repository root, as on any
  run, so `[python3, scripts/x.py]` finds its script, with `ERRATA_ROOT` at the fixture's
  absolute path, under the check's timeout, process group and empty stdin. A generated check
  reads `generated.file` from the fixture. A program that finds the tree from its working
  directory or its own path reads the repository, holds, and its witness is an erratum, which is
  what a witness is for.
- The real run passes over every directory `witness` in a directory `.errata`: a glob leaves
  their files out of the index, and a walk such as `tool_version`'s does not enter them, so a
  `**/*.yaml` never reads a planted break. A `file:` that names a path inside still reads it.

## Why every run, and not a subcommand or a flag

A witness that runs only when asked runs only in the CI job someone remembered to change, so a
check that cannot fail still passes everywhere else. That is the failure witnesses are for, so
they fail closed: a run with a witness runs it. A config that wants a run without them deletes the
key. Each fixture costs one more run of its check under that check's timeout, and a check that
names none costs nothing.

## Why an unknown, and no new exit code

A fixture that is gone, or one the check cannot read, did not show that the check can fail. ADR
0001 makes such a result an unknown, which fails the run without being an erratum. A held witness
is an erratum because the check's claim, that it can tell a broken tree from a sound one, was shown
false. Exit code 1 already means an erratum or an unknown, and a new code would split what a CI
job reads as one answer.

## Considered options

- **A subcommand, `errata witness`, or a flag, `--witness`.** Off unless CI asks, so it fails
  open.
- **A key on rules only.** Program checks fail silently most, by reading a directory other than
  the one they were handed, and a rule's key would leave them out.
- **A single string.** A list later would be a change of type; a check with three branches wants
  three plants.
- **Inline files in the YAML.** No tree, so a glob has no index to read and a program no
  directory to be handed.
- **A git ref holding the fixture.** errata reads git's index and never its objects (ADR 0008).
- **An overlay on the real tree.** A program would need a copy of the whole repository to read
  it from one directory.

## Consequences

A config's checks can carry their own planted breaks, and a check that cannot fail fails the run.
A program check that reads its working directory where it should read `ERRATA_ROOT` cannot take a
witness until it is fixed; the README says so in the check protocol. `errata list` gains a
`witness` key on every check, an empty list when it names none, which the listing's version 1
allows. The tree `.errata/witness/` is reserved: a repository that kept files there for another
purpose has them passed over by globs and walks.
