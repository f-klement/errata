# A root below the top reads the index of the repository above

errata reads git's index for every glob, for the marker walk and for every witness (ADR 0008,
ADR 0015, ADR 0017). Up to v0.2.5 it read the index only at `--root` itself, so a root below the
top of its repository was refused, and each of those features was unknown there. That rules out
running errata on one directory of a repository: a subproject of a monorepo, or errata's own
`examples/`, whose test had to copy the directory into a repository of its own to run it. The
owner decided on 2026-11-04 (#174) that errata reads the index of the repository above such a
root and sees only its entries below the root.

```sh
errata check --root examples --config examples/errata.example.yaml
```

## The rules

- **The repository is the one git would find.** errata looks for a `.git` in the root, then in
  each directory above it, at most 64 up, as git's discovery does: a `.git` directory git would
  not take for one (no `HEAD`, `objects` or `refs`, as an empty one) is passed over, a `.git` file
  stops the look, and the look stops where the file system changes, on Linux and macOS. It is the
  look ADR 0006 already made for the sparse checkout of a missing file, now shared by every read
  of the index. The root's symlinks are resolved first, so a root named through a link outside
  the repository finds the repository that holds its target.
- **No owner test and no ceiling.** git refuses a repository another user owns
  (`safe.directory`) and stops at `GIT_CEILING_DIRECTORIES`. errata does neither: a `.git` above
  the root is read whoever owns it. Every read still goes through the root, opened as an
  `os.Root`, so an index planted above the root can choose only which files below the root a glob
  lists, or make a rule unknown; it cannot make errata read above the root. A test of the owner
  would refuse the common CI layout, a checkout owned by the runner's user and read by a container
  as another, the reason GitLab runners set `safe.directory '*'`. The orchestrator chose this on
  2026-11-04, for low upkeep.
- **The nearest repository wins.** A repository nested in another, and a submodule, which holds a
  `.git` file, hold the roots inside them: errata reads their index and none above. A submodule
  that is not checked out holds no `.git`, so the look goes on to the repository above, whose
  index lists the submodule as a commit and no file, and the root is refused as one that git
  tracks no file below (below).
- **A worktree** has a `.git` file, which errata follows to its git directory as at the top.
  **A bare repository** has no working tree and no `.git` in it, so no root holds its index: a
  root inside one is refused as one no repository holds, or reads the repository above it, as git
  would find it. errata reads no `GIT_DIR` or `GIT_WORK_TREE`.
- **A root no repository holds** is refused, as before: every rule that needs the index is
  unknown, and the message names the look.
- **Paths are relative to the root.** errata cuts the root's path from each entry of the index,
  so every glob, listing, lookup, message and report names a file as it is named below the root,
  and output for a root at the top does not change. A sparse checkout's message names the file or
  directory it leaves out below the root too. A directory it leaves out at the root or above it
  has no name below the root, so the message names it from the repository's top and says so;
  that is also the path `git sparse-checkout add` takes from the top.
- **Nothing above the root is read or matched.** The cut index holds only entries below the
  root, so `**` matches nothing above it, and a `files:` entry that leaves the root, such as
  `../x.yaml` or `../*.yaml`, does not load, as at the top. Every read still goes through the
  root opened as an `os.Root`, so a tracked symlink below the root that leads above it is refused
  as one that leads out, whether a glob matched it or a spec names it; the marker walk passes over
  such a link, which a glob matched, as it passes over every tracked link whose target git does
  not track below the root. The messages say "outside the repository", since errata calls the
  tree at `--root` the repository throughout.
- **Witnesses are found below the root.** A fixture is `.errata/witness/<name>/` below the root,
  and its index is the root's index below it. The repository's own fixtures, wherever they sit,
  are passed over by the root's globs and walks as at the top. A root in a fixture, or in
  `.errata/witness`, is refused for every read: a glob, the marker walk, a witness, a `file:`,
  `exists`, `size` and a walk. Its files are a planted break, read only on its witness's run. A
  root that is a directory `.errata`, in any case, is refused the same way: there the directory
  `witness` has no parent named `.errata`, so neither a glob nor a walk could tell that the
  files below it are fixtures. Such a root serves nothing a run at the top or in another directory cannot.
- **The sparse checkout rule of #187 holds below the root.** What a sparse checkout leaves out
  below the root is cut with the rest, so a glob that may match it is unknown and one that stays
  in the cone reads as in a full checkout, as ADR 0008 has it. A root inside a directory a sparse
  index lists as one entry is refused for every glob, since the working tree lacks every file a
  clone holds there. A file named outright is looked up in the whole index, as ADR 0006 already
  did.
- **A root git tracks no file below is refused.** A clone holds no such directory, so a glob there
  would match nothing on a tree CI never sees, and under `allow_empty` a rule would pass on no
  file read. A plain `file:` there still reads the disk, as at the top.
- **A name spelt in another case is refused.** An entry below the root only in another case, such
  as `Examples/x` for a root `examples`, is refused on every OS, as below a witness's fixture:
  macOS and Windows open both as one directory, so the root would hold files the cut leaves out.

## Why read the repository above, and not refuse

The refusal protected one thing: a glob that reads the index must read the files a clone holds,
and a root that holds no `.git` seemed to have no index. It has one, the repository's above it,
and git itself reads that index for any command run below the top. Cut to the root, it lists the
files a clone holds below the root, which is the set ADR 0008 asks for. The refusal made every
glob, marker and witness unknown there; the cut index makes them read what CI's clone reads.

## Considered options

- **Keep the refusal.** The example config, a monorepo's subprojects and every test that runs a
  config on a directory would each need a repository of their own, as `git init` in a copy. A
  copy is not what CI checks out, so the run reads another tree than the one it guards.
- **Read the working tree below the root.** It would read untracked and ignored files, which ADR
  0008 ruled out for globs.
- **Paths relative to the repository's top.** Every message would name `examples/conf/a.yaml`
  where `file:` wrote `conf/a.yaml`, and output for a root at the top would be the only one that
  matches what the config says.
- **Let `files:` reach above the root**, as `../shared/*.yaml`. The root bounds every read today,
  through `os.Root`, and a rule could then read files that a run at the root's own repository
  would not have. A config that needs them runs at a root that holds them.
- **Read the index of the top and refuse an empty cut silently, as an empty set.** A root git
  tracks nothing below is a directory a clone does not hold, so the set is no true answer, as for
  a witness's fixture.

## Consequences

`errata check --root <dir>` runs every feature on a directory below the top of its repository.
The root's index is read through one function, `rootIndex` in `internal/extract/core`, and cut by
`gitindex.Index.Within`, so every reader of the index sees the cut. A run at a root below the top
reads the whole index of the repository and keeps a part, so it pays the bounds of ADR 0003 on the
whole index, as git does. The refusal "run errata at the top of a git repository" is gone from the
README, ADR 0008 and the message, which now names the look up.
