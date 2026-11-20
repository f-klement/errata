# errata runs a plugin only at the checksum its release published

A plugin is a program errata runs with the user's rights, on a developer's machine and in CI
(ADR 0020). This record says where errata fetches one from, what it checks and when, who may
publish a plugin errata installs by name, and what `errata plugin install` does. It is a design
for v0.5.0; no release of errata installs a plugin yet. Status: accepted. The owner settled its
choices on 2026-11-19, and each is marked where it is made.

The design rests on one chain. A plugin's release publishes a checksums file. `errata plugin
install` checks the download against that file and copies the sha256 of every platform's
executable into `errata.lock`. Every run checks the executable it is about to start against the
lock. So the bytes a run starts are the bytes the release published, wherever they were fetched
from and wherever they are kept.

## What a plugin release publishes

A plugin is built and released from this repository, on tags of its own (ADR 0020), by the release
pipeline that builds errata: GoReleaser, run on a tag from the public source at that tag, uploading
to the GitHub releases of `f-klement/errata` through the REST API.

- The tag is `errata-ext-<name>/v<version>`, such as `errata-ext-csv/v0.1.0` (owner, 2026-11-19),
  as kustomize tags `kustomize/v5.4.1` in a repository that releases several programs. errata's
  own releases keep `vX.Y.Z`, so a plugin's tag never reads as a release of errata. A plugin's
  release is never marked as the repository's latest release (GoReleaser's `make_latest: false`),
  so the releases page and `releases/latest` keep pointing at errata.
- Each platform's asset is the executable itself, not an archive (owner, 2026-11-19):
  `errata-ext-<name>_<os>_<arch>`, with `.exe` on Windows, for the six platforms errata ships for.
- One checksums file, `errata-ext-<name>_checksums.txt`, holds a line `<sha256>  <asset>` for each
  asset, the format GoReleaser writes and `sha256sum -c` reads.
- A plugin that links code under another licence than errata's also publishes that code's notices
  as `errata-ext-<name>_NOTICES.txt`, which the checksums file lists as it lists the executables.

GoReleaser takes a release's version from its tag and refuses a tag that is not semantic
versioning: `errata-ext-csv/v0.1.0` fails its check (`failed to parse tag ... as semver`), and a
tag prefix is a feature of GoReleaser Pro alone. So plugins stay on errata's release pipeline, and
its release script cuts the prefix off the tag and passes the bare version, `0.1.0`, to GoReleaser
in an environment variable (owner, 2026-11-19). GoReleaser runs with `--skip=validate`, which lets
it past the tag; the ldflags that set the plugin's version and the release's name read the
variable, not `.Version`, which would be the whole tag. The validation skipped also checks that the
tree is clean, which holds anyway, since the release builds in a fresh clone of the mirror at the
tag. The release is still made on the whole tag, `GORELEASER_CURRENT_TAG` as errata's script sets
it today. Describe's `version` is that bare version, without the `v`.

An executable rather than an archive is what lets one sha256 mean one thing everywhere: the line in
the checksums file, the lock's pin, the file in the cache and the file in a vendored directory are
the same bytes. With an archive, the lock would pin the archive, and a vendored directory, which
holds the unpacked executable, could only be checked against a second sum errata computed itself.
A vendored directory is a copy of the plugin in another repository, whose LICENSE is not
errata's, and MIT asks that its notice go with every copy. So `errata plugin install --vendor`
writes errata's LICENSE, whose text errata carries in its binary, beside the executables it
vendors. That file covers errata's own code, and only that. A plugin that links code under another
licence, as one built on MPL-2.0 code such as HashiCorp's HCL library does, carries that code's
notices itself, in its release's notices file, and `--vendor` fetches, checks and copies that
file with it; errata's LICENSE stands for none of them. The lock does not pin the notices file:
its sum comes from the checksums file alone. That is enough, since the file is text that errata
never runs, and a mirror that served other notices could change what a reader is told but not
what a run starts.

## Where errata fetches a plugin from

The plugin source is a base URL under which errata finds `<tag>/<asset>`. By default it is
`https://github.com/f-klement/errata/releases/download`, the mirror's releases. A tag holds a `/`,
which errata escapes as `%2F` in the URL, as GitHub's own links to kustomize's assets do.

A mirror takes the default's place with `ERRATA_PLUGIN_SOURCE`, or `--source` on `errata plugin
install`. A mirror has one layout on disk: each release in nested directories named by its tag,
`errata-ext-csv/v0.1.0/`, holding the release's assets. A directory is written
`file:///srv/errata-plugins`, and errata decodes a `file://` URL's path, so `%2F` reads as the
nested `/`. An HTTP mirror must answer the URL with the encoded `%2F` that GitHub's links use; a
static server over the nested layout does, since it decodes the path the same way, and a
repository manager that refuses an encoded slash (Tomcat does by default) needs that allowed.
errata fetches the checksums file and the asset from the mirror and checks them exactly as it
checks the default's.

A mirror is transport only for a version the lock already pins: a mirror that serves other bytes
than the pin is refused. On a first pin, the mirror would serve both the checksums file and the
asset, so it would choose the bytes the lock then pins, and so would whoever sets
`ERRATA_PLUGIN_SOURCE` in a job's environment. So errata refuses a version's first pin through a
source other than the default, set by `ERRATA_PLUGIN_SOURCE` or `--source`, unless `--trust-source`
is given (owner, 2026-11-19). A first pin from GitHub needs no flag, and neither does an install
through a mirror of a version the lock already pins, which the pin checks. The usual way is to pin
each version once from GitHub, commit the lock, and let a mirror serve only versions the lock pins;
`--trust-source` is for a network that cannot reach GitHub at all, and says that its mirror is
trusted as GitHub would be.

A release's source is not written into `errata.lock`. A repository's lock is the same whether a
developer fetched from GitHub and CI from a mirror, so the lock records what the plugin is, not
where one machine found it. A plugin installed from a URL is the one entry that records where it
comes from, since nothing else names it (A plugin from elsewhere, below).

A vendored directory in the repository takes the cache's place. The plugin host's ticket sets its
layout, with one rule from this record: a vendored file's path carries its sum, as the cache's does
(The cache holds a file under its sum, below), and `--vendor` never renames over an existing file.
errata never fetches into it during a run: `errata plugin install --vendor` writes it, and
a person commits it. errata checks a vendored executable against the lock before every run, as it
checks the cache's. A vendored executable opens no new door: whoever can change it and the lock in
one commit can as well add a program check to `errata.yaml`, which runs the repository's own code,
so the vendored directory is held to the same review as the rest of the repository.

errata never looks a plugin up on `PATH` (ADR 0020), and a run never fetches (owner, 2026-11-19).
A run that needs a plugin the cache and the vendored directory do not hold makes every rule of its
kind unknown, naming the plugin and the command that installs it, as the plugin host's ticket
specifies. So a run does what the commit says with no network, and CI installs what the lock pins
with one command before it runs, `errata plugin install` with no name.

## What errata checks, and when

At install, for a plugin from a release:

1. The checksums file is read whole, up to 64 KiB, and parsed strictly: every line is 64 lower
   case hex digits, two spaces and an asset name, and a name that appears twice is refused, even
   with the same sum. That is stricter than `scripts/fetch-tool.sh`, which refuses a name only
   when it is given two different sums. A line is a platform's when its name is
   `errata-ext-<name>_<os>_<arch>`, with `.exe` for Windows, and `<os>_<arch>` one of the six
   platforms errata ships for; it is the notices line when its name is
   `errata-ext-<name>_NOTICES.txt`. Any other name refuses the file.
2. The file must list an asset for the platform being installed. Every other platform's line is
   kept too: the lock pins all of them, so a teammate on another system is checked against the
   same release without a fetch of its own deciding what the release was.
3. When the lock already pins this version, the file must list exactly the platforms the lock
   pins, each with the sum the lock pins. A difference in a sum or in the set of platforms is a
   release that changed after it was pinned, or a mirror that serves other bytes, and the install
   is refused, naming what differs. errata never rewrites a pin to match a release: a person
   deletes the lock entry, and the next install pins anew.
4. The asset is downloaded into a temporary file in the cache's directory, at most 256 MiB, as
   every download errata makes is, hashed as it is written, and refused when its sha256 is not the
   line's.
5. The temporary file is synced to disk and made executable and read only.
6. errata sends the temporary file the describe request, under the rules a run's requests have
   (the plugin protocol): an environment of the Go runtime's variables alone, an empty working
   directory, and the 10 s bound of a describe. The name it gives must be the one installed, its
   version the tag's without the `v`, and one of its protocol versions one errata speaks;
   otherwise the file is removed and the install refused. This runs the plugin, after its bytes
   were checked.
7. Only then is the file renamed into its place in the cache (below). When that place exists
   already, errata hashes the file there. If it matches, the temporary file is removed. If it does
   not, the file there is damaged, and the checked temporary file is renamed over it: the one
   rename over an existing file errata makes, and a safe one, since a run that hashed the damaged
   file refused it and never started it.
8. The lock is written last, to a temporary file renamed over `errata.lock`: name, version, and
   the sha256 of every platform's asset.

At every run, before the plugin's first request of the run, errata finds the executable from the
lock's pin, in the vendored directory or in the cache. It resolves the path's links, and the
result must stay under the directory it was found in, as a reader refuses a link out of the
repository; `Lstat` must then find a regular file there (a link, a directory or a device is
refused). errata hashes that file and compares
the sum with the lock's pin for the platform it runs on. A difference makes every rule of the
plugin's kind unknown, naming both sums and the path. A plugin the lock does not pin is not run,
even when an executable of its name is in the cache: the lock decides what a commit runs. Hashing
costs about 0.2 s for a 33 MiB executable (errata's own binary at the base of this change,
`sha256sum` on the developer workstation), and a plugin is hashed once a run, not once a request.

## The cache holds a file under its sum

The cache is content addressed: a plugin's file is at
`<cache>/plugins/<name>/<version>/<os>_<arch>/<sha256>/errata-ext-<name>`, with `.exe` on Windows.
That is still keyed by name, version and platform, as the plugin host's ticket asks, and the sum
in the path means two pins of one name and version, such as a release and an author's own build
that calls itself the same version, never share a path. errata writes a file there once, by
renaming a checked temporary file into place, and never rewrites one or replaces one that matches
its sum, so a run that checked a path starts the same bytes at every request, whatever an install
does meanwhile. A run looks up the path the lock's pin names, so a file the lock does not pin is
never started. The vendored directory follows the same rule: its paths carry the sum, and
`--vendor` never renames over an existing file. A vendored file at its sum's path that does not
match is refused, naming the path, since git restores it.

A name and a sum have strict forms already. A version reaches a path too, from describe, from the
command line, and from `errata.lock`, which a pull request can change, so errata refuses a version
that is not semantic versioning (`MAJOR.MINOR.PATCH`, with an optional prerelease and build part,
and no `v`) before it builds any path from it.

Since no file is ever written in place, an install takes no lock, on any platform: two installs at
once write their files by rename into paths their sums name, and each writes `errata.lock` by a
temporary file and a rename, so neither file is ever torn. Two installs into one repository at
once may each write a lock without the other's entry, and the last rename wins; the next run then
says which plugin is not pinned. Two `--vendor` installs at once lose an entry the same way, and
both sets of vendored files stay whole, each under its own sum.

A run checks a path and then starts it many times, so a process with the user's rights that
writes the file on purpose between the two runs its own bytes. errata itself never does, and that
process could as well replace errata's binary or the user's shell profile, so the check is against
corruption and stale caches, not against a local attacker, and errata makes no more of it. The
resolved path keeps the file the user's: a link, in any part of the path, to a place outside the
vendored directory or the cache, such as one another user can write, is refused.

Not checked: a signature or provenance over the checksums file (owner, 2026-11-19). What vouches for
a release is the HTTPS download from the mirror, the lock's pin and GitHub's immutable releases,
below. errata's own releases sign nothing today; a user trusts the bytes on the mirror's release
page because they came over HTTPS from `github.com/f-klement/errata`. A plugin errata installs by
name reaches the same trust from the same place, so a checksums file fetched beside the plugin is
the coarsest check that keeps the boundary errata's own download has: whoever can publish a release
on the mirror can publish errata as well as a plugin, so a signature on plugins alone stops no one.
What the plugin adds that errata's download lacks is the window after the first pin: a release
replaced later, or a mirror that changes its bytes, is refused by the lock. Considered options,
below, say what a signature would buy and what each way to make one costs. The files are named so
that a later `errata-ext-<name>_checksums.txt.sigstore.json` or `.sig` beside the checksums file
adds a check without changing the lock or the cache.

Beside the HTTPS download and the lock's pin, the mirror has GitHub's immutable releases on
(owner, 2026-11-19). Once a release is published, its assets cannot be replaced or deleted and its
tag cannot move, even by a token with Contents write. A repository that has not pinned a version
yet then gets the bytes everyone else got, at the cost of one setting and no key. A new release
made with a stolen token stays possible, as it does for errata itself. Until the setting is on, a
release can still be replaced before a repository first pins it.

The setting covers errata's own releases too, and with it GoReleaser must upload into a draft and
publish the release last. errata's `.goreleaser.yaml` makes no draft today, so with the setting on
first, the next release of errata would be published and then fail to upload into it. Turning it
on is the owner's action on the mirror, since neither errata nor its release pipeline can set it
and the pipeline's token has no right to, and it comes in this order:

1. The release configuration uploads into a draft and publishes it last, and a release candidate
   of errata shows that the pinned GoReleaser does so, assets and all.
2. Then the owner turns the setting on (Settings, General, Releases), before the first plugin is
   released.

## Who may publish a plugin errata installs by name

Only this repository (owner, 2026-11-19). `errata plugin install csv` fetches `errata-ext-csv` from
the plugin source, whose default is the mirror's releases, and from no other place: there is no
index, no registry and no lookup by owner. A plugin errata installs by name is built from the public
source at a tag, by the same pipeline and with the same tests as a built in reader (ADR 0020), so a
person who can publish one is a person who can release errata. A name is the key of one kind (ADR
0020), so a registry of names from many publishers would let two of them claim `csv`; one publisher
cannot.

## A plugin from elsewhere

A plugin someone else builds is never installed by name. It is installed from a file the user
names, or from a URL together with the sha256 the user gives for it (owner, 2026-11-19). Either
way nothing vouches for it but the user, so its lock entry says where it came from, and a run
checks it against the lock as it checks any other.

### From a file

```bash
errata plugin install --file ./bin/errata-ext-nginx
```

errata refuses the path unless `Lstat` finds a regular file, then copies it into a temporary file
in the cache's directory and hashes the copy as it writes it, so the bytes it hashes, describes and
keeps are one read of a file an author may be rebuilding. It sends the copy describe, as step 6 of
a release does (the name it gives must be the file's name after `errata-ext-`, and its protocol one
errata speaks), renames it into the cache under its sum, and writes a lock entry with `source:
file`, its version as describe gave it, and the sha256 of this platform only.
There is nothing to fetch it from again, so a teammate whose cache lacks it, on this platform or
another, is told the plugin is not installed and that its entry came from a file. This is the form
for an author testing a build.

### From a URL with a sum

```bash
errata plugin install --url https://plugins.example.com/nginx/0.3.0/errata-ext-nginx_linux_amd64 \
  --sha256 9b1c...e4
errata plugin install nginx --platform darwin/arm64 \
  --url https://plugins.example.com/nginx/0.3.0/errata-ext-nginx_darwin_arm64 --sha256 77d0...a1
```

`--url` and `--sha256` come together: one without the other, a URL that is not `https://`, or a
sum that is not 64 hex digits is a usage error, given before errata opens a connection. The sum is
what errata checks, so the transport cannot change what runs; `https://` is still required, since a
lock is committed and every CI run would fetch from that URL, and a plain HTTP answer that any proxy
can rewrite would turn those runs into refusals.

The given sum is checked before anything is written where errata keeps plugins:

1. The download goes into a temporary file in the cache's directory, at most 256 MiB, and is hashed
   as it is written.
2. When its sha256 is not the one given, the temporary file is removed and the install refused,
   naming both sums. The cache, the vendored directory and the lock are as they were.
3. Only then is the temporary file synced, made executable and read only, and sent describe, with
   the checks a file gets above. A failed describe removes it.
4. It is renamed into the cache under its sum, and the lock entry is written last, with `source:
   url`, the version describe gave, and for the platform its URL and the sum.

The first install of a URL plugin is for the platform errata runs on, since only that one can be
described, and it sets the entry's name and version. Each further `--platform <os>/<arch>` install
names the plugin, downloads and checks that platform's file the same way, and adds its URL and sum
to the entry without running it. `errata plugin install` with no name then fetches, on each
machine, the URL the lock pins for that machine's platform and checks it against the pinned sum,
as it does for a release; `--vendor` fetches every platform the entry pins. A plugin source set
with `ERRATA_PLUGIN_SOURCE` or `--source` does not apply to a URL entry, whose URL is its own.

The sum vouches for the bytes, not for who built them: errata runs exactly the file whose sum the
user gave, and a sum copied from the page that also serves the file is worth what that page is.

### Beside a release of the same name

A file or a URL may not take the place of a plugin the lock pins from a release unless the user
passes `--replace`, which a plugin author needs to test a build of an official plugin. The entry
then says `source: file` or `source: url`, and `errata plugin install` with no name installs no
release over it.

## What `errata plugin install` does

```text
errata plugin install [<name>[@<version>]...] [--source <url>] [--trust-source] [--vendor]
errata plugin install --file <path> [--replace]
errata plugin install [<name>] [--platform <os>/<arch>] --url <https url> --sha256 <hex> [--replace]
```

For the lock as a whole, and for a release by name (the file and URL forms are above):

1. With no name, it installs every plugin the lock pins, at the pinned version, and fails on the
   first that it cannot install: a release from the plugin source, a URL entry from its pinned URL,
   and a file entry not at all, which it names. With names, it installs each.
2. Each name follows the protocol's rules and is not the key of a built in kind.
3. The version is the one given; else the lock's, when the lock pins the plugin; else the newest
   release of the plugin (owner, 2026-11-19). The newest is read from GitHub's releases API for
   the mirror, which lists errata's releases and every plugin's, by date, 100 a page; errata reads
   every page until the list ends, keeps the releases whose tag starts `errata-ext-<name>/v` and
   which are neither drafts nor prereleases, and takes the highest of the versions after the
   prefix by semantic versioning's precedence. No such release refuses the install. The call is
   anonymous, or carries `GITHUB_TOKEN` when it is set, and that token goes to `api.github.com`
   alone, never to the plugin source, a mirror or a URL entry, whose URL a pull request can
   change; `scripts/fetch-tool.sh` likewise sends it to the attestation API alone. Through a
   mirror, which has no API, a version must be given or pinned.
4. When the source is not the default and the lock does not pin the version, it refuses unless
   `--trust-source` is given (Where errata fetches a plugin from, above).
5. It fetches the checksums file from `<source>/<tag>/`, then the asset for the platform it runs
   on, and checks them in the order "What errata checks" gives.
6. With `--vendor`, it also fetches and checks every platform's asset the checksums file lists
   and writes them into the vendored directory, so CI on any platform runs from the commit. When
   the repository has the vendored directory, where a run looks alone, an install without
   `--vendor` vendors as if it were given, and says so; this holds for the install with no name
   too (owner, 2026-11-20).
7. It writes the lock, and prints for each plugin its version, the platform installed and what was
   checked: `errata-ext-csv 0.1.0 linux/amd64: sha256 3f2a... matches errata-ext-csv_checksums.txt
   and errata.lock`.

The lock is written last and only when every plugin of the install succeeded. Files are added to
the cache and the vendored directory under their sums, by rename; in the cache never over a good
file, and in the vendored directory never over any file. So an install that fails leaves the lock
as it was, at most files in the cache no lock pins, and in the vendored directory whole files that
`git status` shows; a directory of the repository it made and left empty is removed. Installs take
no lock (The cache holds a file under its sum, above).

The lock is written in one form, a line for each build. The comment and blank lines at its top,
before the first build, are kept as written, and a comment between builds is dropped (owner,
2026-11-20).

## What the README will say beside it

The README's Quick Start gains `errata plugin install` and, beside it, these paragraphs; the
handbook's install section links to them rather than saying it twice:

> errata installs a plugin from errata's own GitHub releases, and from nowhere else by name.
> `errata plugin install csv` downloads `errata-ext-csv` for your system, checks it against the
> checksums file published with its release, and writes its version and the sha256 of every
> platform's build into `errata.lock`. Commit the lock. Every run checks the plugin's file against
> the lock before it starts it, and a run never downloads anything: in CI, run `errata plugin
> install` first, or commit the plugins with `errata plugin install --vendor`.
>
> The checksums file comes from the same release page as errata itself, so it proves what that
> page published, as `sha256sum -c checksums.txt` does for errata's download. errata's releases
> carry no signature. Once a version is in the lock, a release or a mirror that later serves other
> bytes is refused.
>
> Set `ERRATA_PLUGIN_SOURCE` to install from a mirror of the releases. A mirror installs any
> version the lock already pins, and the pin checks what it serves. A version the lock does not pin
> yet is refused through a mirror, since the mirror would also serve the checksums file and so
> choose what is pinned: pin each version once from GitHub and commit the lock, or, on a network
> that cannot reach GitHub, pass `--trust-source` to trust the mirror as GitHub would be.
>
> A plugin from elsewhere is installed from a file, `errata plugin install --file
> ./errata-ext-name`, or from a URL with the sha256 you give, `errata plugin install --url <url>
> --sha256 <sum>`, which errata checks before it keeps the file. Its lock entry says where it came
> from; errata vouches only that the bytes match your sum.

## Considered options

- **A Sigstore signature from the release pipeline, with no key.** The CI system issues the
  release job an OIDC token, Sigstore's Fulcio certifies it, and the checksums file is signed with
  the short lived key; nothing is stored or rotated. It would split who can sign (a protected tag
  in the private source repository) from who can upload (the mirror's token), so a stolen GitHub
  token could publish a release no errata would install. Against it: the certificate, the bundle
  published beside the checksums and Rekor's public log all carry that repository's path, which
  the public mirror is kept free of, and errata's source would have to pin that identity, or the
  repository's numeric id; and verifying in errata brings sigstore-go, its TUF client and a trust
  root into the core binary, the weight plugins exist to keep out. errata's own
  releases would be signed the same way, or the split would protect a plugin and not the errata
  that installs it. Not chosen (owner, 2026-11-19).
- **One ed25519 key that signs the checksums file**, the public key compiled into errata and the
  private key in one CI variable, entered once, beside the mirror's token. Go's standard library
  verifies it, so the core binary gains nothing, and no rotation is scheduled. Since the private
  key sits beside the token, it splits a compromise on GitHub's side from a release, not one of
  the CI system, and a leaked key can only be replaced by a release of errata with a new public
  key, while every errata already installed trusts the old one. Not chosen (owner, 2026-11-19).
- **GitHub's own release attestation.** With immutable releases, GitHub attests each release.
  Checking it costs the binary what the Sigstore option costs (sigstore-go, and GitHub's trust
  root), and it attests whatever the token uploaded, so it splits nothing from the HTTPS download.
- **Provenance from GitHub Actions on the mirror.** The pipeline uses no GitHub Actions at all,
  and a workflow that signs what a release holds signs whatever the mirror's token uploaded, so
  it would stop no one unless it also built the plugin, a second release pipeline.
- **The plugins' sums in errata's own release**, a table compiled into each errata. The trust would
  be errata's, but a plugin would ship only with a release of errata, which ADR 0020's own tags
  rule out.
- **An archive per platform, as errata ships.** The lock would pin a file the run never sees; see
  above. Not chosen (owner, 2026-11-19).
- **A run that fetches what the lock pins.** It would save CI one command, at the price of a run
  that reaches the network and whose time depends on it. One commit gives one verdict either way,
  since the pin is checked. Not chosen (owner, 2026-11-19).
- **A plugin from elsewhere from a file only**, or none in v0.5.0. A URL with the user's sum adds a
  second fetch path, but lets a team's CI install its own plugins from the lock alone, where a file
  entry leaves every machine to find the file itself. The owner chose both forms on 2026-11-19.

## Consequences

The plugin host gains an HTTP client: errata's core imports no `net/http` today, outside a test. The
build ticket for the first plugin adds the tag form to the release pipeline's tag rules, which
accept `vX.Y.Z` and `vX.Y.Z-rc.N` alone today, the step in the release script that cuts the prefix
off, and a GoReleaser configuration per plugin with the `binary` archive format, `make_latest:
false`, and its version from the variable. A signature added later goes beside the checksums
file, and errata's own release gains the same one in the same change.
