# Larder

This directory is a small service that does not exist: Larder, a pantry tracker with a Python
API, a TypeScript web client and a Java SDK. Its files are the fixtures of
`errata.example.yaml`, which shows each feature of errata twice, each with a comment that says
the claim it checks and why someone would want it.

Larder is served at https://Larder.Example.com/ and runs on Python 3.12 or later.

## Install

### Requirements

- Node 20.11 or later, for the web client
- Python 3.12, for the API

Run the API in a container:

```bash
docker run -p 8080:8080 larder:1.4.0
```

## Run the example

From the top of a clone of errata:

```bash
errata check --root examples --config examples/errata.example.yaml
```

The example runs in place. `examples` sits below the top of the clone, so errata reads the git
index of the clone and keeps the entries below `examples`
([A root below the top](../docs/reference.md#a-root-below-the-top)).
Paths in its output are relative to `examples`.

Every check of scope `repo` holds. The checks of scope `live` and `applied` are skipped, since
errata cannot read a running system yet, and the coverage line names each one. The run counts
one check more than the file holds: errata adds `errata-unbound-markers` whenever a rule reads
markers, and it is false for a marker no rule reads.

[The reference](../docs/reference.md) explains every key the example uses. The comment above
each check names its sections, such as [`marker`](../docs/reference.md#marker) or
[Records](../docs/reference.md#records).
