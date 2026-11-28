# A plugin is an executable errata speaks to over stdin and stdout

errata is one static binary, and every reader in it is a dependency the binary carries. The
readers still to come weigh more than the ones built so far: HCL, Jsonnet and CUE, and later a
Kubernetes provider that renders Helm and Kustomize. So an extractor kind that most repositories
do not need ships as a plugin: a program built and released from this repository, which errata
installs on demand and runs when a rule names its kind.

A plugin is an executable named `errata-ext-<name>`, as git finds `git-<name>`, kubectl finds
`kubectl-<name>` and Terraform finds `terraform-provider-<name>`. It provides the extractor kind
whose key is `<name>`, so an entry `csv:` under a rule's `extract:` needs `errata-ext-csv`, and a
message about a missing plugin names it from the config alone. errata starts it with no argument,
writes one JSON request to its stdin and closes it, and reads one JSON reply from its stdout. The
plugin reads nothing else: errata resolves `file:`, `files:` and `max_bytes:`, reads the files
with the checks a built in reader gets, and hands the plugin their bytes with the entry's
`max_bytes`, by which the plugin bounds its own work (owner, 2026-11-20). Every request is a
process of its own, which ends when the reply is written.

The [plugin protocol](../plugin-protocol.md) is the specification: the three requests, the values
a plugin returns with their file, line and not literal mark, the bounds it runs under, and how the
protocol is versioned. This record says why it has that form.

Protocol 1 is for offline extractor plugins: a plugin reads the files errata sends it and nothing
else. A live provider, such as the Kubernetes and bare metal providers of later releases, needs
the network and credentials to reach a cluster or a host. That grant comes with a later protocol
version, designed with the release that needs it; protocol 1 gives a plugin neither (owner,
2026-11-19). [ADR 0023](0023-a-provider-is-a-plugin-that-speaks-protocol-2.md) is that design.

## Why an executable

- **The static build stays.** errata ships for six platforms as static binaries with no cgo. A
  plugin is built the same way, by the release pipeline errata uses, for the same platforms, but
  released on tags of its own, and errata needs nothing beside it to run one.
- **A crash ends the plugin, not the run.** A plugin that panics, loops or runs out of memory is a
  process that exits, overruns its timeout or is killed. errata sees how it ended and makes the
  rule unknown (ADR 0001). In process, a panic in a reader's dependency would end errata, and a
  loop could only be stopped where the reader checks its context.
- **The seam exists.** `check.Extractor` is a function from a spec and files to values with
  locations, and `check.Description` says beforehand what those values are: not literal or not,
  flat, maps or records, and which other extractors' values they read. A plugin answers the same
  two questions in JSON. The host that speaks the protocol is one more implementation of
  `check.Extractor`, and `program.Run` already starts every program errata runs, with a timeout,
  a bound on output and a process group of its own.
- **Any language can write one.** The protocol is JSON over a pipe. A plugin written in Go is
  what this repository releases, but nothing in the protocol depends on it.
- **The kind is the name.** One plugin provides one kind, and the kind's key is the name after
  `errata-ext-`. errata never needs a list of which plugin provides which kind.

## What a plugin is held to

A plugin is a program errata runs with the user's rights, on the developer's machine and in CI.
The protocol takes from it every reason to touch anything but its request:

- Its input is the request. It gets the bytes of its files and the values of the extractors its
  description names, never a path to open. Its working directory is an empty directory errata
  makes for the request, so a relative path it opens finds nothing of the repository.
- Its environment is the Go runtime's variables and nothing else: `GOMAXPROCS`, `GOMEMLIMIT` and
  `GOGC` when errata has them, and `SYSTEMROOT` on Windows (owner, 2026-11-19). A program check
  is the repository's own code and gets errata's whole environment; a plugin is not, and in CI
  that environment holds the job's tokens. `program.Spec.Env` is a program's whole environment,
  and a nil `Env` is an empty one, so a host that forgets to set it starts a plugin with nothing
  rather than with the job's tokens; program checks and generators pass `os.Environ()` with
  their own variables added. The plugin host sets `Env` to the allowlisted names errata has. Its
  test (the plugin host's ticket) sets a token variable and checks that the plugin sees neither
  the token nor `PATH`.
- Where a value is, errata checks: its file must be one of the request's files, its line must
  be in that file, and the values are held to ADR 0003's bounds where they reach the rule, as
  every extractor's are since v0.2.3. What a value is, its text, key, condition and not literal
  mark, errata takes from the plugin as the plugin writes it. A defective plugin that returns
  `8080` for a cell holding `9090` passes every check, which is why the verification design,
  and building plugins in this repository with the tests of a built in reader, matter.
- It runs no program and reads no file or network. errata cannot enforce this, which is the cost
  of this choice set out below; the plugins errata installs by name are built from this
  repository, and errata checks each against its release's checksums before it runs
  ([ADR 0021](0021-errata-runs-a-plugin-only-at-the-checksum-its-release-published.md) says how).

This leaves ADR 0002 standing. A plugin reads configuration; it does not run the repository's
code. ADR 0002 lets errata evaluate Jsonnet and CUE in process, with no network, inside the
repository, and with a time and memory limit. As plugins they keep the time bound. No network
becomes the plugin's conduct, which errata does not enforce. Inside the repository holds by
construction: an evaluator reads its imports from the files the entry lists, since it is handed
no other. The memory limit is lost, as ADR 0003 accepts for every reader (Bounds, below).

## Bounds

ADR 0003 bounds time and size and lets memory fail closed. A plugin gets the same treatment, with
the bounds a process allows:

- **Time.** Each request has a timeout, which kills the plugin's process group when it passes.
  The rule is then unknown, and the message says the plugin did not finish in time.
- **Output.** errata holds at most a bound computed from `max_bytes`, the most values and the
  measured cost of JSON for each, of the plugin's stdout, and 1 MiB of its stderr, since a message
  uses only its last lines. The byte past it stops the plugin, as the timeout does, and the rule is
  unknown. A reply errata did not read whole is a reply it cannot decode, so the bound stops the
  plugin, as a generator's does (ADR 0011), and does not drain as a program check's does.
- **Values.** The rule holds the decoded values to `MaxValues` and to `max_bytes` in all, with the
  messages a built in reader's values get.
- **Memory.** errata sets no ceiling, for the reasons ADR 0003 gives: a soft limit such as Go's
  memory limit stops no allocation, and a hard one belongs to the runner. A plugin that exhausts
  memory is killed by the system or the runner, and errata sees a plugin killed by a signal, which
  makes the rule unknown; if the system kills errata instead, the job fails. Either way the run
  fails closed. A process of its own makes this better than it is in process, where a reader that
  exhausts memory always ends the run.

A run the caller cancels is not a plugin that timed out. When errata's run stops early, as an
interrupt stops it, the host kills the plugin, and the rule is unknown with a message that starts
`cancelled:` and names no timeout, as a program check's is. `program.Run` tells the two apart
(`Ended.Cancelled` beside `Ended.TimedOut`), so a cancelled run never reads as a plugin that ran
too long. The same change to `program` makes `Spec.Env` the whole environment, nil meaning an
empty one, which the environment rule above rests on.

## Considered options

- **Go's `plugin` package.** A shared object loaded into errata's process. It needs cgo, which
  breaks the static build; it works on Linux and macOS only, so four of the six platforms would
  have no plugins; and a plugin loads only into an errata built by the same Go release with the
  same version of every package both import, so each errata release would need a matching build
  of every plugin. A crash in a plugin would also end errata.
- **WebAssembly modules run by wazero.** wazero is pure Go, so errata would stay static, and one
  module would serve every platform. A module gets a hard memory cap, which ADR 0003 could not
  promise in process and this decision does not promise either, and a filesystem confined by
  construction, where an executable is confined only by its own conduct. Those are real losses.
  Against them: the heavy dependencies plugins exist to carry, HCL's and later Helm's and
  Kustomize's, would each have to compile to WebAssembly, and some compile poorly; Go compiled
  for WASI preview 1 (`GOOS=wasip1`) runs on one thread, and parsing in a module is slower than
  native code; and errata would carry a WebAssembly runtime and its compiler in the core binary,
  the weight plugins exist to keep out. Protocol 1 needs no network, but a module has no network
  sockets at all, so the live providers of later releases could never be modules, where an
  executable can be given the network by a later protocol version. The owner chose executables on
  2026-10-18.
- **One long running process per plugin, spoken to in many requests.** The owner chose a process
  per request on 2026-11-19. A long running process saves a process start per request. It also
  needs framing on the pipe, a protocol for concurrent requests or a queue in front of one
  process, and a rule for what a plugin may keep between requests, which would let one rule's
  files reach another's values. A process per request starts in milliseconds, ends with its
  reply, and cannot carry state from one rule to the next.
- **gRPC, as Terraform's providers use through go-plugin, or length prefixed binary frames.**
  gRPC is typed and versioned, but it brings a protobuf and gRPC stack into the core binary, a
  server in every plugin, and a handshake over a socket. Binary frames save base64's third on
  file bytes, at the cost of a codec written by hand on both sides. The owner chose JSON with
  file bytes in base64 on 2026-11-19: JSON is in Go's standard library and in every language a
  plugin might be written in.
- **A plugin that provides several kinds**, such as one HCL plugin with a key for Terraform, one
  for Packer and one for Nomad. errata would then need a list of which plugin provides which kind
  before it could name a missing one. The owner chose one kind per plugin on 2026-11-19, with a
  format's dialects as a field of its spec, as `ini` takes `dialect`.
- **The plugin reads the files itself.** It would then need the repository's path, could read
  any file in it and beyond, and would have to repeat the checks errata makes of a file: inside
  the repository, no link out of it, no device or FIFO, at most `max_bytes`. Handed bytes, a
  plugin is a function from its request to its reply.

## Consequences

Each plugin errata runs costs a process start per request: one to describe the plugin, one to build
the config's entries of its kind at load, and one per extractor entry for each root a run reads (the
repository and each witness). A plugin is trusted to read only its request, and the verification
design is what makes that trust reasonable for a plugin errata installs by name. The protocol is a
public interface: a change to it is a new protocol version, and errata keeps speaking the older ones
it lists, as the specification says. Dropping a version is a breaking change of errata. A value a
plugin returns must be valid UTF-8, since JSON carries text, so a reader whose values are not UTF-8
stays built in, and a rule whose plugin entry reads another extractor's value that is not UTF-8 is
unknown before the plugin starts. Whether a plugin reads the values of every extractor its
description names is its promise, which errata does not check; the specification says why.
