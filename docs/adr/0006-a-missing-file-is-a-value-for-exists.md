# A missing file is a value for exists

Some claims are about a file being there at all: a vendored kustomization's generators stay removed
while an Argo Application exists, a values file that an Application names is there, a CLAUDE.md
stays under a byte ceiling. Every extractor is unknown for a file that is not there, so no rule
could say "this file stays removed". errata has two extractor kinds that look at a file and read
none of it, each with one field, `file`:

```yaml
extract:
  app:   {exists: {file: infra/argocd/applications/wazuh.yaml}}
  notes: {size: {file: CLAUDE.md}}
```

`exists` gives `true` when a regular file is at the name once its symlinks are followed, and
`false` when nothing is there. `size` gives that file's size in bytes, in decimal. Both get what
they report from one `lstat` after the links are followed, through `core.Source.Stat`. Each value
is at the file as `file` wrote it, at no line.

## A missing file is false, not unknown

This is the one place where a missing file is not an unknown, and ADR 0001 is the reason it is
allowed to be. ADR 0001 makes a check unknown when it could not evaluate: its claim was not shown
false, so the run fails without an erratum. A `yaml` extractor over a missing file could not
evaluate. It was asked for the value at a path inside the file, and with no file there is no
such value and nothing to say about one. `exists` was asked whether the file is there. Finding
nothing at the name is that question answered, by the same `lstat` that would have found the
file. Calling it unknown would make the one question `exists` asks unanswerable in the case it
exists for: a rule that a file stays removed could only ever be true or unknown, never pass.

What ADR 0001 protects still holds, because `exists` is unknown wherever it truly cannot tell:

- a symlink that leads outside the repository, whether or not its target exists, since errata
  looks at nothing outside it (README, Extractors);
- a directory on the way that errata cannot open, to read it and search it, where the file may
  be there or not: the `lstat` goes through Go's `os.Root`, which opens each directory on the
  way, so a directory that may be searched and not read (mode 0111) is unknown too;
- a directory at the name (below);
- a loop of links, and a root that is not there or is no directory. The root is opened before
  any name is looked up, since a file as the root would otherwise read as a name below a file,
  which is `false`;
- a FIFO, a socket or a device at the name, which git never checks out and every reader refuses.

Only "nothing at the name" (`ENOENT`) and "a name on the way is a file, not a directory"
(`ENOTDIR`, as for `f/x` where `f` is a file) are `false`. A link that dangles inside the
repository is `false`: following it as the operating system does finds nothing, as `test -e`
does. A regular file that errata may not read is `true`, since the `lstat` of the file itself
needs no read permission on it, and neither kind reads it.

## A directory at the name is unknown

Three answers were weighed for a directory where a file was named:

- **`true`**, as `test -e` reads it. A rule that a values file is there would hold for a
  directory Argo cannot read as one.
- **`false`**, as Python's `is_file()` reads it, and as `package_version` and `tool_version`
  pass over a directory at a manifest's name (`core.ErrDirectory`). Safe for a rule that a file
  is there. Unsafe for a rule that a file stays removed whenever what reads the name reads a
  directory too: kustomize reads a directory in `resources:` and `generators:`, and an Argo
  directory source with `recurse: true` reads the manifests below a directory named `wazuh.yaml`.
- **unknown**, as `size` and every reader already are for a directory. It fails the run in both
  directions, and the author of the rule finds out that something unexpected is at the name.

The owner chose unknown on 2026-10-27. Each of the other two passes one kind of rule on a
tree it should refuse, and a directory where a file was named is rare enough that failing the run
over it costs little. A claim about a directory has no kind yet.

`size` keeps every reader's rule: no file at the name, a directory and anything else that is no
regular file make it unknown. A size is a regular file's, and a rule that a file stays small
wants the file there.

## Size is still worth having

k3s holds `claude_md_files_stay_small` today with `size(bytes(root_md[0])) <= 7000` over a regex
capture of the whole file. That works for its two files, and counts something else:

- the regex reads every CRLF as LF, so a file of CRLF lines counts one byte fewer for each line;
- a file over `max_bytes`, 32 MiB by default and 64 MiB at most, is unknown, where its size is
  plain;
- it reads the whole file into memory and hands it to CEL to count it.

`size` gives the byte count `wc -c` gives, of a file of any size, without reading it, and costs
nothing beside `exists`: the same `lstat` reports both. So both are built.

## A glob does not load

A `files` entry with `*`, `?` or `[` is a glob, in `env` and, from v0.2.1, in the `files:` of
`yaml`, `json`, `toml` and `regex`. Read as a name, `legacy/*.conf` is no file, so `exists` would
say `false` and a rule that the files stay removed would hold whatever the glob matched. So a
`file` with any of the three does not load, for `exists` and `size` alike. File sets came as
`files:` and left `file` a single name, and `exists` and `size` take no `files:`, so what
existence over a glob means (any match, every match, a count) is still undecided; the refusal
and its test in `internal/extract/filestat` are where that decision starts. The cost is that a
file whose name holds one of the characters, such as `app/[slug]/page.tsx`, cannot be named yet.

## Considered options

- **One kind, `file: {file: x}`, with two values.** A value is one string, and a rule would index
  `x[0]` and `x[1]` by convention. Two kinds of one value each read as what they give.
- **One kind with a field, `stat: {file: x, of: size}`.** One key fewer, and a field to misspell
  that a load error would catch. `exists:` and `size:` say the same in fewer words.
- **`file_exists` and `file_size`.** Self-describing, but every kind already reads a file, and
  `file_exists: {file: x}` says it twice.
- **A missing file as no value, through `check.NoValue`**, so that `allow_empty` decides. That
  keeps "missing is unknown" as the default, and makes every rule that a file stays removed list
  its extractor in `allow_empty` and test `size(x) == 0`. A missing file would then be told apart
  from a directory, a dangling link and a file only by the rule's author remembering the key.
- **Size of a missing file as `0`.** An empty file and no file would be one value, so a rule that
  a file stays small would pass on its deletion.

## Consequences

`exists` and `size` are the seventeenth and eighteenth keys a rule's extractor may set, and a
missing file is `false` from v0.2.1; changing that later is a breaking change to every rule that
reads it. A directory at the name is unknown for both kinds. On a file system that ignores case, as macOS
and Windows set theirs up by default, `exists: {file: Values.yaml}` is `true` when `values.yaml`
is there, where Linux CI finds nothing, as every reader would read it. Neither takes
`max_bytes`, since neither reads a byte, and a config that sets it does not
load. A location now prints without a line when it has none, so a message names `legacy/old.conf`,
not `legacy/old.conf:0`.
