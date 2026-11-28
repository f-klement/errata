# The compose reader hands compose-go a copy of the files errata read

The `compose` extractor reads a Docker Compose project with compose-go, the loader docker compose
uses. compose-go opens files itself, with `os.ReadFile`, `os.Stat` and its own dotenv reader. For
an included file and an extends file it asks a resource loader first, but a resource loader can
only hand compose-go a path, which compose-go then opens. An included project's `.env` and an
include's `env_file:` it opens without asking any loader. So with
the repository's root as the project's directory, compose-go read every included or extended file
a second time, from the disk, past the checks errata made on its first read: within the
repository, through no link that leaves it, within `max_bytes`. In an included project compose-go
read an include's `env_file:` relative to the process's working directory.

So the reader stages the files. It reads each file of the project through core, as every extractor
reads, and writes a copy of each into a new private temporary directory (`os.MkdirTemp`, mode 0700,
files 0600, written through `os.Root`), at the file's path below the root. compose-go is handed that
directory in place of the root. Every file it opens is then a copy of bytes errata read, or no file
at all. The stage does not make errata's model of which file a path names right. A path errata did
not route finds nothing, and the rule is refused. A path errata routed to a file other than the one
docker compose would load reads that other file, from the stage. The routing below, its refusals,
and a test that compares each value with what compose-go gives loading the repository alone are what
keep that from happening. The stage's own path has its links resolved, so compose-go's link
resolution of a `develop.watch` path finds none in it, and a relative `TMPDIR` is made absolute.

## Routing paths

errata decides which file each `include:` and `extends:` path names, before compose-go loads
anything, since it must read and copy that file first. It resolves each path as compose-go does:
an include and a service's extends file against the directory of the project that loads the
naming file, and an extends file that an extends file names against the directory of the file
that names it. compose-go then asks the loader errata hands it, the guard, for each path, and
gives it two strings only: the path as written, interpolated, and the file whose load is running
(`consts.ComposeFileKey`). It never says which file of an extends chain wrote the path, since
`applyServiceExtends` recurses with the same context, and it leaves a base file's `extends.file`
as written, since the guard is no `localResourceLoader`. So errata keys each route by those two
strings, the file compose-go is loading and the path as compose-go hands it, and the guard gives
the file that key was routed to and refuses any other.

Two consequences follow. When one key would lead to two files, as when two projects of different
directories load one file that names a relative path, errata refuses the rule, naming the path and
both files, since the guard could not tell which one compose-go means; writing one path
differently, such as `./y.yaml`, reads. And a path whose text looks up a variable no file errata
read sets, through any default, alternative or required form, nested or not, is refused, naming
the path and the variable: docker compose takes that variable from its shell, which errata does
not read, and so may load another file. The walk follows the extends chain of every service
before compose-go loads the project, so such a path, or an extends service name that looks up
such a variable, refuses every rule over the project. A service's `env_file:` path that looks up
one is refused only where the rule reads the service's environment, and an item of a keyed list
whose name looks up one, with a value or without, only where the rule reads that list: the shell
would choose which variable, label or arg docker compose sets. A keyed list is one that
compose-go turns into a mapping by its items' names, such as `environment:`, `networks:` or
`build.additional_contexts`, or sorts by its items' text, as `extra_hosts:`, whose name is
its host. The reader's table of
them is checked against compose-go itself: a test walks compose-go's types for every field that
may hold a mapping, loads a list of strings there through compose-go's loader, and fails on a
keyed list the table lacks, so a compose-go release that adds one turns the suite red. A list a
`pre_start` hook inherits from its service is checked in the service too. A `label_file:` is not
read: compose-go would open it itself, so a read of the labels of a service or a hook that names
one is refused, and so is a read of a hook's environment where the hook names an `env_file:`.

A project built from more than one file or service is read coarsely (the owner, 2026-11-27). Six
review rounds each found another merge in which a value from the shell reached a value errata read
as literal: an override, an extends base, a default compose-go's normalize filled in for one file's
load and not another's, an included project's service merged under the including file's own, and an
alias merged under its key. Each fix checked one more merge, and the next round found one it did
not. So when a project has an override, an `include:`, or an `extends:` of any kind, and any file it
reads looks up a variable no file sets, every value read from it is not literal. errata still reads
the values, and refuses a key that holds nothing, since the shell may give it one. The `env` reader
gives nothing for a service that sets no such variable; it relies on its refusals of a variable
name, an `env_file:` or a profile the shell could choose instead. The taint does not ask which file
contributes to the value, so a merge errata does not know cannot escape it. Its cost is coarseness:
one `${TAG}` in an unrelated service of an override makes every value of the project not literal. A
refusal that asks whether the shell picks a name, a file or a profile checks every service of the
name in every file the project reads, with the services each extends, rather than the contributors
errata finds.

A single file with no `include:` and no `extends:` keeps exact marks, value by value. compose-go
merges within one file too, so the rules below for folds, hooks and keys built from others still
apply there. A value is literal only where errata can show that every
source that could have given it, or been folded into it, names no variable the files leave
unset. compose-go folds several sources into one value at a list, whose items it keeps one per
key (`EnforceUnicity`), and, where a `pre_start` hook merges with its service by the override
rules, at any list and at `logging`, which it merges only when both name the same driver. A value
below such a fold is not literal when the YAML at the fold names an unset variable, and a key
below one that holds nothing is refused. Every list is a fold but those an override replaces
whole (`command`, `entrypoint`, `healthcheck.test`). A test derives that set, and the mappings
compose-go does not merge key by key, from compose-go's loader, so a release that changes either
fails the suite. A value a `pre_start` hook takes from its service reads as the service's, at the
service's line, and the service's fold counts for it too. compose-go also builds a key from
another within one file: `develop` from `x-develop`, its alias, and `depends_on` from `links`,
`volumes_from` and the namespaces such as `network_mode: service:db`. A value below either key is
not literal when the other names an unset variable, and a key below it that holds nothing is
refused. A service whose `profiles:` names an unset variable is refused, since the shell decides
whether it is active.

## The files docker compose loads

docker compose run without `-f` loads more than the file it finds by its name: the first override
beside it (`compose.override.yml`, `compose.override.yaml`, `docker-compose.override.yml`,
`docker-compose.override.yaml`), or, when the `.env` file sets `COMPOSE_FILE`, the files it names in
its place (compose-go's `cli.WithConfigFileEnv` and `WithDefaultConfigPath`). With `-f` it loads
only the files named. The `env` reader reads a file docker compose finds by its name, so it loads
the override with it, and the project is then one of more than one file, read coarsely as above. A
`compose` rule names its files as `-f` does, so one with no `overrides:` key is refused where docker
compose run without `-f` would load more: it does not say which of the two runs it means. The
refusal names both ways out. Listing the override reads the two merged. `overrides: []`, an empty
list, says the rule means `-f` with the file alone (the owner, 2026-11-27): it loads no override
beside the file and ignores `COMPOSE_FILE`, as `-f` does. The spec keeps an absent key (nil) apart
from an empty list, and so do its place and the listing `errata list` prints. The `env` reader and a
`compose` rule with no `overrides:` key both refuse `COMPOSE_FILE` rather than follow it, since its
separator differs by platform and its files may leave the main file out. A test checks errata's two
lists of names against compose-go's.

## Bounds

A stage holds at most what one rule reads: `max_bytes` (32 MiB by default, 64 MiB at most), since
every file the walk reads draws on one budget, and at most 150 files. Directories add little: a
tree that reaches deep paths through links put 1,778 directories on a stage before the 150 file
limit refused it. Each rule has a stage of its own, so at most the engine's parallel width (the
CPUs) times `max_bytes` sits in `TMPDIR` at once. Where `TMPDIR` is a tmpfs that is RAM outside the
Go heap, which no heap limit counts.

## Removal

The rule removes its stage when it ends, on every exit: a value, a refusal, a compose-go error,
the file limit, a cancelled context or a passed deadline, and a panic in the parser, since the
removal is deferred. A process that is killed leaves its stage in `TMPDIR`, named
`errata-compose-*`. A `TMPDIR` that cannot be written makes the rule unknown, with a message that
says errata needed a directory to hand compose-go the files it read.

## Considered options

- **Intercept compose-go's opens.** A seccomp filter or `ptrace` would catch every open, but needs
  privileges the CI pods do not grant, and works only on Linux.
- **Hand compose-go each file's bytes and no path.** compose-go takes the bytes of the files it
  is given (`ConfigFile.Content`), but opens includes, extends files and env files by path, with no
  hook for their bytes.
- **Apply include and extends in errata.** errata would then have its own model of the merge, and
  every difference from compose-go would be a wrong value. errata models only which file each path
  names (Routing paths), and refuses a path it cannot tell apart; compose-go does the merge.
- **An in-memory file system.** compose-go calls the `os` package directly, so it cannot be given
  one.

## Consequences

This is the first extractor that writes at run time; a program check's copy of the tree ([ADR
0019](0019-a-program-check-runs-in-a-copy-of-the-tree.md)) is the other place errata writes one.
[ADR 0002](0002-errata-reads-configuration-and-never-runs-it.md) stands: the copies are data
compose-go parses, and nothing runs. A rule reading a Compose project needs a writable `TMPDIR`, and
holds up to `max_bytes` there while it runs.
