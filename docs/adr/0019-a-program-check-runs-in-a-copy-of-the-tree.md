# A program check runs in a copy of the tree

Since rules first ran on a pool, a program check and a generated check have run alone: every check
before one ends first, and none after it starts until it ends, because its program may write a
file a rule reads (ADR 0011). In a config whose time goes to test suites that is the whole run. The
GitOps repository's invariant suite on the build node, two CPUs: about 67 s, of which its 22
program and generated checks take 65 s one after another and its 76 rules 2.3 s.

So when it pays, each program check, each generated check and each of their witnesses runs on the
pool in a copy of the repository of its own, and nothing it writes reaches the repository or
another check. The report keeps the config's order, and every verdict, message and value is the one
the check gives in place.

## The copy

The copy is a git repository of its own in the user's cache (`internal/treecopy`). Its
`.git/objects/info/alternates` names the repository's objects, so it holds no object, and it takes
a copy of the repository's `config`, `HEAD`, `refs`, `packed-refs`, `shallow`, `info/exclude`,
`info/attributes` and the stash's log. `git read-tree --reset -u HEAD` then checks out `HEAD`, and
what the disk holds and `HEAD` does not is laid on top:

- each path `git status` names, staged or not, and each file git assumes unchanged or skips,
  copied with its mode, a symlink as a symlink, and a deleted one removed; untracked files git
  does not ignore are among them;
- git's index, when one of those paths is tracked or flagged, refreshed in the copy, so git in
  the copy lists what git in the repository lists and `git diff-files` names what it names there;
- each entry `git ls-files --others --ignored --directory` names, copied.

A check must judge what is on disk, not what was committed, so a checkout of `HEAD` alone would
be wrong on every local run with uncommitted work. The copy keeps the branch name, and a branch, a
stash or a key a check sets in it is the copy's own. A first pass used `git worktree add --detach`,
which costs the same: it left `HEAD` detached, shared the refs, the stash and the config with the
repository, and wrote an entry under the repository's `.git/worktrees`.

The ignored entries are copied, up to 32 MiB in all. Left out, a test suite that needs
`node_modules`, a virtual environment or a `.env` would fail in the copy and pass in place. The
first pass linked them, at the cost of a link an entry, but a check that wrote into an ignored
file wrote the repository's, so the copy did not isolate where it claimed to. Copied, a copy costs
what the ignored files weigh, so a tree that ignores more than the budget runs every check in
place, as before copies. The budget is 32 MiB because the trees that gain from copies sit far
below it and the ones above it sit far above: the GitOps repository as CI checks it out ignores
1.9 MiB (a Django project it clones into `config/landingpage`), errata's CI checkout none, while a
developer's GitOps tree ignores 2.9 GiB (nested clones) and errata's own 341 MiB (`bin/`). At the
budget a copy writes about twice what its tracked tree weighs (10.1 and 17.4 MiB a copy, below). The walk that weighs the
ignored files stops at the budget, so a large `node_modules` costs the walk to 32 MiB.

The pool's worker *n* makes its copies in `copy-n` of the repository's directory of the cache,
one after another, the directory emptied before each and removed after it, and holds a lock on it
while it does. So a check runs at the same path from one run to the next, and a cache keyed by
the directory, as Go's build cache keys a package by its path, stays warm. A second run of errata
on the same repository at once finds the locks taken and makes its copies in `spare-0`,
`spare-1` and so on, each under a lock of its own, so a spare a killed run left is emptied when a
run next takes it, and each run, when it opens the tree, removes the spares no run holds. On
Windows, where errata takes no lock, every copy is in a new directory of its own, at a path that
changes from run to run, and one a killed run left stays. git's status and the listings of flagged and ignored files are read once, when errata opens
the tree and again after a check in place, not once for each copy.

Measured on the build node on 2026-11-19, median of 15 (first pass):

| Tree | Method | Make | Remove | Disk per copy |
|---|---|---|---|---|
| GitOps repository, 906 files | `git worktree add --detach` | 0.100 s | 0.051 s | 10.1 MiB |
| GitOps repository | `cp -a` | 0.189 s | 0.075 s | 26.0 MiB |
| errata, 1115 files | `git worktree add --detach` | 0.139 s | 0.034 s | 17.4 MiB |
| errata | `cp -a` | 0.643 s | 0.190 s | 90.6 MiB |

`cp -a` scales with `.git`, a worktree with the tracked tree, and the review measured a repository
with alternates at the cost of a worktree. A tar of the tree without `.git` was cheaper still, but
three of the GitOps repository's checks call `git ls-files` and fail in it. The CI pod allows
neither a reflink copy nor an overlay mount.

## When it pays

A copy costs about 0.15 s to make and remove, and running side by side saves only what overlaps.
So a check runs in a copy only when its last run took a second or more, or errata has no record
of it, and only when two or more such checks would run alone and the pool has two workers or
more, so they can overlap. Otherwise every check runs in the repository as before, at no cost.
errata's own config, three short shell programs, ran 0.34 s in place against about 0.55 s in
copies on two CPUs in the first pass, which copied every program; now its first run copies them,
records that each took well under a second, and every later run is in place.

A check left in place still runs alone, but alone means only that no check before it reads the
repository any more: every rule before it has ended, and every check in a copy before it has
made its copy, which may still be running, since nothing the check in place writes reaches a copy
already made. No check after it starts until it ends, and the disk is read again for the copies
after it. Had it waited for the copies before it to end, as the first pass waited for a whole
stretch, every short check between two long ones would keep them apart: on the GitOps repository,
whose six checks of a second or more sit between short ones, the run took as long as before
copies.

The record is `durations.json` in the repository's directory of the user's cache: from each
program and generated check's name in the report, its witnesses included, to how long it last
ran, in milliseconds. errata writes it after each run that weighed copies, to a file beside it
that it renames over it, so a run that reads it while another writes reads one record whole, and
of two runs that write, the last stands. A record that is missing, cannot be read or does not
decode is no record, and every check copies. A CI job that starts with an empty cache therefore
always copies, which is what its suites of long checks want. The directory is the repository's
because a check's name means something only in one config; it is named by a hash of the
repository's path, so two clones of one repository keep a record each.

Measured on the build node on 2026-11-19, two CPUs (`taskset 0,1`, `GOMAXPROCS=2`), three rounds
interleaved with errata before copies, after a first run that wrote the record:

| Config | Before copies | With copies |
|---|---|---|
| GitOps repository, 98 checks | 79.5, 78.3, 66.1 s | 48.1, 51.1, 41.2 s |
| errata's own, 3 short programs | 0.36, 0.31, 0.30 s | 0.37, 0.31, 0.30 s |

The node ran other work (load 3.4 to 6.8), so the rounds spread; the reports were equal but for
the times in every round. errata's own first run copies its three programs, 0.86 s against 0.37
s, and each run after it is in place. A `go build ./...` and a `go vet ./...` of errata's tree,
forced into copies, took 2.43, 2.47 and 2.59 s against 2.25, 2.40 and 2.41 s in place: the copy's
path is the slot's from run to run, so Go's build cache stays warm. Left to the record, the same
two checks moved between copies and the repository as `go vet` fell under a second and rose over
it, and the first run on each set of paths built cold once (15 s), never again.

It runs in the repository, as before, whenever a copy could not be true to the tree: no `git`, a
root in no repository or one with no commit, submodules (a copy would hold them empty), a sparse
checkout (a copy would hold it whole), a tracked path with a `filter` attribute such as Git LFS
(its filter would run for every copy), a config that sets `core.worktree`, `core.splitIndex`,
`extensions.worktreeConfig` or `extensions.refStorage`, more than 32 MiB of ignored files,
`GIT_DIR` and its kin set as a hook sets them, or a user cache inside the repository or one that
cannot be made.

## A check that must write to the repository

A check is a claim, and a write to the tree is a side effect of checking it, one that made a
later rule's verdict depend on the order of the config. So copying is the default: a check that
writes by accident cannot change the repository or another check, and the common config needs no
new key. A check that must write for a check after it keeps the old way with `in_place: true`: it
runs in the repository, alone, and a rule after it reads what it wrote. The other default, in
place unless a key asks for a copy, would leave every config as slow as it was until someone added
a key to each check.

## What changes for a program

A program in a copy has the copy's path as its working directory and `ERRATA_ROOT`, one of the
pool's stable directories in the user's cache, so a path it prints in a message differs from the
repository's, and its files' modification time is the copy's. A rule or check after it no longer
reads what it wrote. Each of these is a reason to set `in_place: true`, and none changed a verdict
of the GitOps repository's suite.

## Considered options

- **The repository's `.git` copied whole (`cp -a`).** A true copy, but five times the cost of a
  worktree on errata's tree, and more with a larger history. A repository that borrows the objects
  is as true for the cost of a worktree.
- **A git worktree of `HEAD`.** The first pass: the same cost, but a detached `HEAD`, refs, stash
  and config shared with the repository, and an entry under its `.git/worktrees`.
- **Ignored entries linked rather than copied.** Cheap at any size, but a write into one reached
  the repository.
- **A copy in a new temporary directory each time.** Every path differs from run to run, so a
  build cache keyed by the path, such as Go's, rebuilt every package in every copy.
- **Copy every program check.** A short check pays more for its copy than it gains: errata's own
  config ran 60% slower in copies.
- **A worktree of `HEAD` when the tree is clean and `cp -a` when it is not.** A dirty tree is
  every local run, and `cp -a` of a work tree also copies what it ignores.
- **Copies only for a config that opts in.** No config gains anything until it is edited.
- **Run programs side by side in place.** Two test suites that write the same cache or output
  file would race, and a rule would read a file mid write.

## Consequences

This amends ADR 0011's "the repository root as the working directory": a program check and a
generator start in the root of a copy when the run uses copies. A program check that writes a file
a later check reads needs `in_place: true`. errata now starts `git` to make a copy, which it never
did before; with no `git` it runs as before. It writes in the user's cache: the copies while they
run, and the record of how long each check took. `in_place` is a new key of a program check and a
generated check, refused on a rule. An interrupt, `SIGTERM` or `SIGHUP` stops a run and removes its
copies; a run that is killed leaves its copy in the cache, and the next run that takes the same
directory empties it, or, for a spare, the next run removes it. When a check in place grows the
ignored files past the budget, the checks after it run in place, alone, as on a tree over the
budget. The copy's git config is the repository's in another directory: an `includeIf
"gitdir:..."` section matches the copy's path and a relative `include.path` resolves against the
copy's `.git`, so a value set only through one differs in the copy. errata copies such a config
rather than refusing it, since most includes set a user's identity, which a check seldom reads;
a check that does needs `in_place: true`.
