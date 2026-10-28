# An extractor reads a file set from git's index, and CEL groups its values by file

Most claims a repository makes about its manifests are about every file of a kind: every Argo CD
Application, every CronJob, every Markdown file. 36 of the k3s suite's program checks stayed
program checks only because an extractor read one named file, and a rule that listed today's files
would miss the file added tomorrow, which is the failure most of those checks were written for.
So the `yaml`, `json`, `toml` and `regex` extractors take `files:` in place of `file:`, with the
meaning `env`'s `files:` already had, and an `assert` groups an extractor's values by file with
`by_file(x)`.

```yaml
extract:
  ignores:
    yaml:
      files: [infra/argocd/applications/*.yaml]
      kind: Application
      path: "spec.ignoreDifferences[?@.kind == 'Secret' && @.jsonPointers[?@ == '/data']].kind"
  options:
    yaml: {files: [infra/argocd/applications/*.yaml], kind: Application, path: "spec.syncPolicy.syncOptions[*]"}
assert: by_file(ignores).all(f, f in by_file(options) && 'RespectIgnoreDifferences=true' in by_file(options)[f])
```

## The rules

- `files:` lists up to 64 entries. An entry with `*`, `?` or `[` is a glob; any other entry names one
  file, which must exist. `file:` and `files:` together do not load. One matcher serves every
  `files:` key, `env`'s too, in `internal/extract/core`, and it is Go's `path.Match` applied a path
  segment at a time, so `*` never crosses a `/`, plus one addition: a segment that is exactly `**`
  matches zero or more whole segments, so `infra/**/*.yaml` matches `infra/a.yaml` and
  `infra/a/b/c.yaml`, and `a/**` matches `a` and everything below it. A `**` inside a segment,
  such as `a**`, is a `*`, as `.gitignore` reads it.
- A glob matches the files git's index lists, read from `.git/index` without running git: the files
  a clone holds, never an untracked or ignored one, a submodule, or a file a sparse checkout leaves
  out. A glob needs the root to be the top of a git repository, or the rule is unknown.
- The order is fixed: the entries in the order `files:` lists them, and a glob's files in the order
  of git's index, which sorts paths byte by byte. A file two entries name, or two names that
  resolve to one file, such as a tracked symlink and its target, is read once, at its first name.
  The values of each file come in the order its reader finds them.
- Each file a glob matches is read as `file:` would read it: through the same check that refuses a
  symlink leading out of the repository, a device, a FIFO and a directory. A tracked symlink that
  leads out makes the rule unknown, as it does for `file:`. An entry that is absolute or whose `..`
  leaves the root does not load. A tracked file missing from the working tree is unknown.
- A glob that matches no file adds no value. The extractor then returns an empty list, and the
  rule's own rule decides: an extractor that found no value makes the rule unknown unless
  `allow_empty` lists it.
- Each file is parsed alone, and a file its reader refuses (one that does not parse, a YAML file of
  several documents without `kind` or `name`, one past a node limit) makes the whole extractor
  unknown. The one exception is a `kind` or `name` that selects no document of a file: in a set the
  file adds no value, where for `file:` it is refused, since a glob over every manifest selects the
  kind in some of them.
- ADR 0003's bounds hold for the set as one extractor. `max_bytes` (32 MiB by default, at most
  64 MiB) is what the files hold in all, each read to what the files before it left, and a file's
  node limit is scaled from those bytes. The values of all the files are held to 100000 and to
  `max_bytes` bytes. The budget on what YAML aliases and merge keys add (1000000 nodes) and the
  budget on a path's filter work (4000000000 bytes) are spent by the files together, each file's
  read starting at what the files before it spent: a file of merges just under the alias budget
  reads in 0.14 s, and 613 of them, each given the whole budget, took 82 s before the budget was
  shared. The documents `embedded` parses out of strings
  ([ADR 0007](0007-embedded-reads-a-document-a-string-holds.md)) count over every file of the set
  together, as one more file against the set's `max_bytes`, as one file's strings count against
  its own. A regex extractor's time limit, 10 seconds for each 32 MiB of `max_bytes`, is shared by
  the searches of every file. One extractor
  reads at most 10000 files, empty ones too, since each costs an open. The globs are matched
  against at most 256 MiB of path: the bytes of every tracked path times the sum of the globs'
  weights, where a glob without `**` weighs 1 and a glob with `**` weighs its other segments,
  since the matcher retries what follows the last `**` at each later segment of a path. That
  weight is multiplied by one for each 8 bytes of the glob's longest segment that holds a `*`,
  since `path.Match` tries what follows a `*` at each byte where the `*` could end: a segment of
  a `*` and 251 more bytes took 655 ns a byte of path at a weight of 1, and takes 28 ns a weighed
  byte on a 2 core worker node at its weight of 32. Each byte of a character class, its brackets
  included, counts as 3 bytes of the segment, since `path.Match` reads a class again at each byte
  it tests: twenty classes of 13 bytes after a `*` took 72 ns a weighed byte with each byte counted
  once, and take 21 ns with 3 cores at their weight of 98. The bound assumes 33 ns. A spec over a
  bound makes the rule unknown, naming the bound.
- Each value keeps its own file in its location, so every message and report names the file and
  line where the value was found, as for one file. In `assert`, each extractor is still a
  `list(string)`, and `by_file(x)` is a `map(string, list(string))` from each file that gave `x` a
  value to its values, in order. Its keys come in the order of each file's first value, so a
  comprehension over them, and the error it reports, is the same on every run; a Go map
  would hand CEL its keys in an order that changes from run to run. A file that gave no value is
  no key. `by_file` takes the name of one of the rule's extractors and nothing else, which the
  load checks.

## Why an empty set and not unknown

ADR 0001 makes a check that could not evaluate unknown, so the run fails without an erratum. A
glob that matches nothing has evaluated: errata read the index and found no file, which is a true
answer. Making it unknown would also refuse a claim of the form "every X, if there is one", such
as "every Application that ignores a Secret's data syncs with RespectIgnoreDifferences" in a
repository where none does yet. The case ADR 0001 guards against, a glob gone wrong that silently
reads nothing, is already caught one level up: an extractor that finds no value makes its rule
unknown unless the author lists it in `allow_empty`, which says in the config that none is fine.
A rule that needs the set itself to be non empty pairs the glob with an extractor that
`allow_empty` does not list.

## Considered options

- **Walk the working tree**, as `tool_version` does without `files:`. It would read untracked and
  ignored files, `node_modules` among them, so a rule could pass in CI and fail on a laptop with a
  build directory, or the other way round. `env` already matched against the index, and two
  meanings for one key would be worse than either.
- **A glob library**, such as bmatcuk/doublestar. The owner's rule is that a feature is core and
  relies as little as possible on what is outside the binary; the matcher is a page of Go over
  `path.Match`, and its cost is bounded by the same product `env` already charged.
- **A glob in `file:`**, read as one whenever it holds `*`, `?` or `[`. A file named `a[1].yaml`
  that a v0.2.0 config reads would then mean another file. `files:` was already the key for several
  files in `env`, `tool_version` and `package_version`.
- **Pass over a file that does not parse.** A glob over `**/*.yaml` meets Helm templates, which are
  not YAML. Skipping them would let a rule pass without reading the file a claim is about, which is
  what ADR 0001 rules out; the author narrows the glob, or reads such files with `regex`.
- **A value as a map in CEL, `{text, file, line}`.** It would change the type of every extractor,
  so every rule written so far would stop loading, and comparing values would need `.text`
  everywhere. `by_file` adds the file only where a rule asks for it.
- **A variable beside each extractor**, such as `files.x` or `x_files`, parallel to its values. A
  name of errata's own could clash with an extractor's name, and a parallel list needs index
  arithmetic CEL lacks to group by. `by_file` is a macro, so it reads a hidden variable no
  expression can name.

## Consequences

A rule over a set reads files that the merge request adding them names nowhere else, so a new
manifest is held to the claim from its first commit. `env` gains `**` with the shared matcher: a
glob of v0.2.0 that held `**` as a whole segment, where it matched one segment, now matches any
number. The order of a set's values follows git's index, so a rule that indexes them, such as
`x[0]`, reads the first file by path, and `by_file(x)`'s keys follow the same order. Adding a
reader to file sets is a builder of one file for `core.BuildOneOrSet` and a `files:` field, which
`TestPlaceOfFiles` holds to the same place rules as the others.
