# A provider is a plugin that speaks protocol 2

The live and applied scopes belong to errata: the glossary defines them and every check declares a
scope. The providers that reach a live system, Kubernetes first, carry dependencies errata keeps
out of its binary (client-go, Helm, Kustomize), so a provider ships as a plugin
([ADR 0020](0020-a-plugin-is-an-executable-errata-speaks-to-over-stdin-and-stdout.md)), installed
and pinned as every plugin is
([ADR 0021](0021-errata-runs-a-plugin-only-at-the-checksum-its-release-published.md)). ADR 0020
left the grant a provider needs, the network and credentials, to "a later protocol version,
designed with the release that needs it". v0.6.1 is that release, and this record is that design.
The owner settled each choice below on 2026-11-27.

## Protocol 2 carries the provider operations

Protocol 2 is protocol 1 with three more requests, `render`, `read` and `predict`, beside
`extract`, and two more members of describe's reply: `operations`, the operations the plugin
provides, and `env`, the environment variables its read and predict requests need. errata offers
`[1, 2]`; an extractor plugin moves to 2 only when it wants to, since it needs nothing 2 adds. The
[plugin protocol](../plugin-protocol.md#protocol-2) is the specification.

One protocol keeps one describe, one version rule and one host. The kind is still the name
(ADR 0020): `errata-ext-kubernetes` is the provider `kubernetes`, and the extractor kind
`kubernetes:` too if it provides extract, so errata needs no list of which plugin provides what.

The three requests share one form: a spec that is the plugin's own JSON object, the files the
operation reads, and a bound; and one reply: files, an error or a gap. Render's objects and
predict's predicted objects are documents errata's readers can read as they read the repository's
files, so a rule over rendered, live or predicted objects reuses the readers and adds none.
The spec's members are not declared in protocol 2, as an extract entry's are: their first caller,
the Kubernetes provider, shapes them, and a later version can declare them when a second provider
shows what they share.

## Read and predict get the variables their plugin names, and nothing else does

A plugin declares in its describe reply the names of the environment variables its read and
predict requests need, such as `KUBECONFIG` and `HOME`. errata passes those, with errata's values,
to read and predict requests alone, beside the Go runtime's variables every request gets. Describe,
build, extract and render keep protocol 1's environment: render reads only the files errata sends,
as extract does, so it needs no credentials and runs in a CI job with no cluster.

This is the coarsest split that keeps the real boundary. An offline operation gets nothing. An
online one gets the names its plugin needs, so a CI job's other tokens stay out of it. The user
writes no list. The plugins errata installs by name are built in this repository and pinned by
checksum, so a plugin's list is reviewed where its code is. A plugin from elsewhere names its own
variables, and the user who installed it from a file or a URL vouched for it already.

That vouching needs the list in view (review of #138, 2026-11-28). `errata plugin install` prints
the names each provider's build declares, with those its last pin did not record, and records them
in `errata.lock` beside the build's checksum, so a release that widens its list changes a
committed file and its review shows the widening. A run passes a read or predict only the names
both the build declares and the lock records, and refuses a build that declares one the lock does
not record, naming the variable and the install that records it. The user still writes nothing:
the install writes the names, as it writes the checksum, and no key or token is added.

A plugin may not name a variable errata itself reads, `GITHUB_TOKEN`, which `errata plugin
install` sends to GitHub's releases API and which a CI job may hold with write scope, or
`ERRATA_PLUGIN_SOURCE`: errata's credential is errata's, and no provider needs it to reach a live
system. The names compare in any case, as Windows looks variables up.

errata cannot see which hosts read and predict reach: that is their purpose. What ADR 0020 says
of conduct holds for them: a provider reads and predicts, and never applies. errata never applies
anything, and a provider that did would be a defect of that provider.

## A check names its provider

A live or applied check names its provider: `provider: kubernetes`. The key is required on every
live and applied check and refused on a repo check. The config alone then says what a check needs,
as an extractor kind's key does, and the gap of a check the run cannot reach names the plugin and
the command that installs it: `no installed plugin provides read: errata-ext-kubernetes is not
installed; install it with errata plugin install kubernetes`.

A check needs one operation: read for a live check, predict for an applied one. When the named
plugin is installed and provides it, the check runs. When it is not installed, or provides other
operations, the check is skipped with that gap: the run lacks the system, and the check was not
tested. When the lock pins it and errata cannot run it, because the lock pins no build for this
platform, its build differs from the pin, it did not describe itself, or it names a variable the
lock does not record for it, the check is unknown, as a rule whose plugin errata cannot run is:
that is a fault, not a missing system. The gap's install command is the plugin host's, the one an
extractor kind's missing build gives: in a repository that vendors, the install that vendors.

## A program check's plugin makes its scope reachable

In v0.6.1 a live or applied check is a program check, which reaches the system itself, as the
example config's `kubectl` script does. The named plugin, installed and providing the operation,
makes the check's scope reachable; errata does not call the plugin for it. Rules over live and
predicted objects, through extractors that read the files a read or predict reply holds, are the
later target, and protocol 2 already carries what they need.

## Considered options

- **A separate provider protocol**, with its own describe, so extractor plugins never see a
  version change. It splits one public interface into two, each with its five fixed describe
  members, and a plugin that provides both would speak both. Not chosen.
- **Providers as extractor kinds under protocol 1**, such as a `kubernetes-live` kind whose extract
  request reads the cluster. Protocol 1 promises that a plugin reads only its request; this would
  break that promise for every extract request, with no environment to reach a cluster in. Not
  chosen.
- **The config names a provider's variables**, `providers: {kubernetes: {env: [KUBECONFIG]}}`. The
  user decides, but every repository repeats the same list, and a pull request can widen it as it
  can add a program check. Not chosen.
- **Read and predict get errata's whole environment**, as a program check does. Simplest, and in
  CI it hands every job token to a plugin. Not chosen.
- **Any pinned plugin that provides the operation serves every live check**, with no key on the
  check. With none pinned, the gap cannot name a plugin to install; and a repository with two
  providers could not say which a check needs. Not chosen.
- **A table in errata of the provider plugins it knows.** ADR 0020 rejected such a list for kinds.
- **errata hands a program check the plugin's read reply**, on stdin or in a file. A later step,
  if rules over the reply do not cover what program checks need.

## Consequences

errata speaks protocols 1 and 2, and dropping either is a breaking change. A provider's read and
predict requests can reach the network with the credentials its plugin names, which is a larger
trust than an extractor plugin's; the checksum chain of ADR 0021 is what lets a user give it. A
config with a live or applied check that names no provider no longer loads: until v0.6.1 such a
check could only be skipped, so the key costs a line and changes no verdict.
