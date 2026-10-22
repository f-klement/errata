# Glossary

The words this project uses, each with one meaning. Use these words in code, in documents and in
output. Do not introduce a synonym for a word that is already here.

## Core

**Claim.** A statement the repository makes. It can be true or false. Examples: a sentence in a
README that names a host, a threshold in one file that depends on a schedule in another, a field in
git that is supposed to be absent from the live system.

**Check.** One test of one claim. A check has an identifier, a reason, a scope, and a result.

**Reason.** The text that says why a check exists and what breaks without it. Every check must have
one. A check without a reason does not load. Its author writes it. Why a check was skipped is a
gap, not a reason.

**Verdict.** The result of a check that ran: true, false or unknown. A verdict says whether the
claim holds. It is not the same as an observation, which only says that a difference exists.

**Unknown.** The verdict of a check that could not evaluate: its program crashed, did not finish in
time, or could not be found, or an extractor found no value. The claim was not shown false, so an unknown is not an erratum. It
still fails the run, because a suite that passes on what it could not test passes on a subset. Do
not call it an error.

**Errata.** The output of a run: the claims its checks showed to be false. One of them is an
erratum. A run that ends with an unknown and no errata still fails.

## Scopes

A scope says what a check needs in order to run. Each check declares exactly one.

**Repo scope.** The check reads only files. It needs no access to any running system.

**Live scope.** The check reads the running system and compares it to the files.

**Applied scope.** The check asks what the running system would contain after an apply, and
compares that to the files. It never applies anything.

## Systems

**Live system.** The running system a repository describes. A Kubernetes cluster, a set of Docker
containers, a host.

**Provider.** The part of errata that talks to one kind of live system. A provider implements up to
three operations and declares which ones it has.

**Render.** The provider operation that turns the files into the objects they describe.

**Read.** The provider operation that fetches the current state of the live system.

**Predict.** The provider operation that reports what an apply would produce, without applying it.

**Attested fact.** A value that no interface can read, recorded by hand with a source and a date. A
check on an attested fact tests both the value and its age. Use this for a setting that exists only
in a vendor's web interface.

## Reconciliation

**Reconciler.** A program that reads git and changes the live system to match it. Argo CD and Flux
are reconcilers.

**Reconciler model.** The four facts errata stores about a provider's reconciler: whether one
exists, how often it runs, whether it deletes objects absent from git, and whether it repairs a
change made by hand. These four facts turn an observation into a verdict.

**Drift.** The live system differs from the files. Drift alone is an observation. It becomes a
verdict only after the reconciler model is applied to it.

**Competing writer.** A second party that writes the same fact as git. Git can lose to it silently.
In Kubernetes it is another field manager. In Terraform it is a change made outside Terraform. On a
host it is the operating system vendor, whose update reverts a setting.

**Field manager.** The recorded name of the writer of one field on a Kubernetes object. Kubernetes
stores these names in `metadata.managedFields`. Two managers can own one field.

## Rules

**Rule.** A check the user writes in YAML. It names extractors and states how their values must
relate: all equal, or a CEL expression that must hold.

**Program check.** A check the user writes as a program, run through the check protocol. Every check
is either a rule or a program check. Do not call it a script, an exec check or a command check.

**Extractor.** The part of a rule that gets values from one place. A YAML path, a JSON path, a
regular expression capture, an image digest, a field on a live object.

**Check protocol.** The way errata runs a program check. errata runs the
program and reads its result. The program can be in any language.

**Gap.** What a run lacks to reach a check's scope: a provider operation that no available provider
has, for example read for a live check. A check whose scope the run cannot reach is skipped, and its
gap is printed in the coverage.

**Coverage.** The count of checks that ran and the count that were skipped, with the gap for each
skip. errata prints coverage on every run.
