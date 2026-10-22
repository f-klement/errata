# Extractors bound time and size, and let memory fail closed

A repository's files are input errata does not control: a pull request from a fork can add any
file a rule reads. Every pass an extractor makes over a file is linear in its size, and errata
enforces bounds that each have a plain meaning: a file size cap (32 MiB by default, `max_bytes:`
on an extractor raises or lowers it, never above 64 MiB), a node count per format scaled from that
cap, a budget on what YAML aliases expand to, a cap on values per extractor and on regex capture
groups, and a time limit on a regex search that scales with the size cap. A file past a bound makes
its rule unknown, which fails the run. errata does not promise a ceiling on peak memory. It depends
on the shape of the file as well as its size, and a file that exhausts memory ends the process,
which fails the CI job: the run fails closed, it never passes.

## Considered options

- **Model each parser's cost and refuse what the model predicts is expensive.** This was built
  first. Three review rounds each found inputs the model priced wrongly, in both directions: a
  merge key chain that used gigabytes before the model saw it, and ordinary lock files the model
  refused. A model of another library's cost is wrong wherever that library changes.
- **State a measured memory ceiling per format.** Two further rounds each measured a denser shape
  past the figure stated in the round before (a YAML ceiling stated as 0.76 GB, then 1.47 GB, then
  measured at 2.0 GB). A number that the next reviewer beats is a claim errata cannot keep.
- **Cap the process's memory.** Go's memory limit is a garbage collection target, not a hard
  limit, so it does not stop an allocation; a hard limit belongs to the CI runner, where the
  operator can set it.

## Consequences

The bounds are what the documentation states, each with the measurement that chose it. The peak
memory figures measured so far are given as examples, not as a guarantee. An operator who needs a
hard memory ceiling sets it on the runner. A new extractor gets the same treatment: linear passes,
named bounds, and an unknown past any of them.
