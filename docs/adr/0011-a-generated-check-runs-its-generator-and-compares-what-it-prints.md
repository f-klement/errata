# A generated check runs its generator and compares what it prints

Some files in a repository are generated and committed: a public copy of a document with private
sections stripped, a lock file, an API client, a table in a README built from a schema. The
claim is that the committed file is what its generator prints today. Nothing a rule can read
decides that, because the generator's rules live in the generator. So errata has a third kind of
check beside rules and program checks, the generated check:

```yaml
- id: architecture-public-copy-is-current
  reason: architecture.md is handed to people and is generated from architecture-full.md.
  scope: repo
  generated:
    file: architecture.md
    run: [python3, scripts/build-architecture.py, --stdout]
```

errata reads the file, runs the generator, holds its stdout, and compares the two byte for byte.
The file is read first, so a generator that writes the file and prints it is compared with the
file as committed and not with what it just wrote.
The check holds when they are equal and is an erratum when they differ, with the first line and
column where they part, both sides quoted to the end of that line, and that line as the result's
place in SARIF.

## Why this does not break ADR 0002

ADR 0002 says errata reads configuration and never runs it: a rule's extractors read files with
parsers compiled into the binary, and a value a program would compute is out of reach. That
stays true. A generated check is no rule and has no extractor, and no rule gains a way to run
anything. What runs is the generator, a program the config names, as a program check's `run` is
one. errata has run the programs a config names since v0.1.0, through the check protocol. A
generated check adds one thing to the check protocol: errata decides the verdict by comparing the
output with a file, where a program check reports its own verdict through its exit code.

The limits are what keep that honest:

- **argv only, never a shell.** `generated.run` is a list of arguments, as `run` is. errata
  starts its first argument with the rest as they are written. It never joins them into a
  command line, so no argument is split, globbed or expanded. The placeholders a `run` takes
  (`<errata.id>`, `<errata.id_underscored>`) are replaced inside one argument, as there.
- **The rules of a program check, through the same code.** The generator and a program check
  start through one function in `internal/protocol`: the repository root as the working
  directory; errata's environment with `ERRATA_ROOT`, `ERRATA_CHECK_ID` and `ERRATA_SCOPE` added;
  the check's `timeout`, 30 seconds when it sets none; a process group of its own, which the
  timeout kills while the program runs. A child left running after the program exits runs on,
  for both kinds. Stdin is the null device for both, so a generator that reads stdin reads
  an empty file at once and does not wait for the timeout.
- **The output compared, never run.** errata does not execute, parse, evaluate or write
  what the generator prints. It compares the bytes with the file's, and quotes at most 64 bytes of
  each side in a message. A failing generator's message ends with its stderr's last lines, cut
  at 256 bytes.
- **A bound on what errata holds.** `generated.max_bytes`, 32 MiB when left out and at most
  64 MiB as for an extractor, bounds the committed file, the generator's stdout and its stderr,
  each on its own. The byte after the bound on either stream stops the generator, as a timeout
  does. A program check's output has no bound yet; that is a separate change.

## What each outcome is

- **Equal bytes: true.** Line ends count, and so does the last newline. A file of CRLF lines is an
  erratum against a generator that prints LF, and so is a file that lacks the newline the
  generator prints at its end. A generator that normalizes them can say so in its own output; a
  checker that normalized them would pass a file that differs from what the generator writes. The
  check this kind replaced in the GitOps repository read the file with Python's universal
  newlines and passed a CRLF line in architecture.md; this kind fails it.
- **Different bytes: false.** The message starts with the file and line, such as
  `architecture.md:15, column 7, differs from what its generator prints`, so the text and JUnit
  reports name the place in the message, and SARIF puts the result at that line of that file.
- **No file at the name: false.** The generator ran and printed a file the repository does not
  hold, so the claim that the committed file equals the output is shown false: there is no
  committed file. This is unlike a rule's extractor, which is unknown for a missing file because
  it cannot read the value it was asked for (ADR 0001). Here errata has both sides of the
  comparison: one is the output, and the other is the fact, found by the `lstat` that `exists`
  uses (ADR 0006), that nothing is there. That holds for an empty output too, since an empty file
  and no file are different trees. The result points at the check in the config, since it has
  no line.
- **A generator that exits with any status but 0: unknown.** Exit status 1 included: the status
  says whether the generator ran, and only the comparison says whether the file is current. A
  generator that fails half way may print part of the file, and a comparison of that would be an
  erratum about the generator, not the file. So would a generator that is not installed.
- **A timeout, or output past the bound: unknown.** Output past the bound cannot equal a file
  errata would read, but errata stopped the generator before it finished, so it does not know
  what the generator would have printed.
- **A file errata will not read: unknown**, as for an extractor: past `max_bytes`, a directory,
  a link that leads out of the repository, a device or a FIFO.

## The other keys

`scope` must be `repo`: the check compares a file of the repository with what a program prints
from the repository, and needs no provider. So `--fail-on-skip` never sees a generated check
skipped, and the coverage counts it as one that ran. `defaults.run` is a program check's and a
generated check does not take it; `run` beside `generated` does not load, nor does a rule key.
`required` lists a generated check by its id, as any other. `errata list` prints the generator's
argv with the placeholders replaced, under `generated.run` in JSON and `run:` in text, so a
reader sees what will run before it does.

## Considered options

- **Keep it a program check.** The GitOps repository's check ran `build-architecture.py --check`
  and read its exit code. Every generator then writes its own comparison, and the one it had read
  CRLF as LF. A failure said "out of step" and no more: no line, no place in SARIF. A
  reader of the config sees a program and cannot tell that it compares a file.
- **A `generated` extractor inside a rule.** A rule could then compare the output with a regex
  or assert over it. But every extractor would then be one that may run a program, and a reader
  of a rule could no longer tell from the kind of its extractors whether it runs anything. ADR
  0002's line between reading and running would go.
- **Normalize line ends, or trim the last newline.** This makes a check pass that a byte
  comparison fails, on a file that is not what the generator writes. `normalize` stays a rule's
  key.
- **Print a diff.** A whole diff of a large file in a CI log and a JUnit message costs more
  than it tells: the first difference and its place are enough to find it, and the generator
  rewrites the file anyway.

## Consequences

A config can run a third kind of program, and the README's warning about the programs a config
runs on a ref you do not trust covers generators too. `type` in `errata list --format json` has
a third value, `generated`, which the listing's version 1 allows. A generator must print the file to
stdout, so a generator that only writes a file needs a mode that prints it, as
`build-architecture.py` gained `--stdout`. Changing a verdict above, such as reading a missing file
as unknown, is a breaking change to every check of this kind.
