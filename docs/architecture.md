# Architecture

How the code is split, and why. The vocabulary is [GLOSSARY.md](../GLOSSARY.md).

## Packages

Dependencies point one way: `cli` imports the packages below it, and `check` imports nothing from
errata.

| Package | Owns | Knows nothing about |
|---|---|---|
| `cmd/errata` | `main`: signal handling and the exit code | anything else; it calls `cli.Execute` |
| `internal/cli` | flags, wiring, the exit code contract (0 clean, 1 errata, 2 usage) | how a check runs or how output looks |
| `internal/config` | the `errata.yaml` format, strict decoding, load errors | running anything |
| `internal/engine` | which checks run, in what order, and which are skipped | the check protocol's details, output formats |
| `internal/protocol` | the check protocol: run a program, map its exit code to a verdict | config, providers, output |
| `internal/provider` | the provider interface, capabilities, the reconciler model, scope gating | any concrete live system (none yet) |
| `internal/report` | output formats: `text` and `junit` | where the results came from |
| `internal/check` | the domain types: `Check`, `Scope`, `Verdict`, `Result`, `Report` | every other package |

Three seams carry the design:

1. **`engine.Runner`.** The engine calls a function to run a check. Production passes
   `protocol.Run`; tests pass a fake. A second runner, such as an in-process Kubernetes ownership
   check, plugs in here without the engine changing.
2. **`provider.Provider`.** It declares capabilities, so the engine can skip a `live` or `applied`
   check by name when nothing can reach it. The render, read and predict methods are left out on
   purpose: their signatures should come from the first real provider (Kubernetes), not from a
   guess.
3. **`report.Reporter`.** Two implementations exist from the start, which proves the seam is real.
   SARIF and an interactive view are further reporters over the same `check.Report`.

## Decisions taken in the code

**cobra for the CLI, not Bubble Tea.** Bubble Tea is a framework for interactive terminal
programs. errata runs in CI first, where there is no terminal, and its contract is an exit code and
a file. cobra is the standard Go command and flag library (kubectl, helm and gh use it). An
interactive view of a report can come later as a `report.Reporter` built on Bubble Tea, without
touching the engine.

**Strict YAML decoding.** `sigs.k8s.io/yaml` with `UnmarshalStrict` rejects an unknown key. A
misspelt `reson:` that the decoder ignored would load a check without its reason, and a misspelt
`scope:` would change what the check needs. Both would fail silently. That library is also the one
`client-go` uses, so the Kubernetes provider adds no second YAML stack.

**A check that cannot evaluate is unknown, and an unknown fails the run.** A crashing program, a
timeout or a missing binary gives `VerdictUnknown`. It is kept apart from the errata, which hold
only claims shown false, and it fails the run all the same. A skip is only for a scope that no provider can
reach, and the coverage line names each one with its gap.

**The check protocol is exit codes first.** Exit 0 is true, exit 1 is false, anything else is an
error. Stdout is the message, or the `message` field when stdout is a JSON object. A shell script
can implement this, and so can an existing test suite.

## Testing

`make check` is the gate. Each layer catches a different class of defect.

| Layer | Tool | Where | What it catches |
|---|---|---|---|
| Unit | `go test` | `*_test.go` beside the code | a function that returns the wrong value |
| Property | [rapid](https://pkg.go.dev/pgregory.net/rapid) | `TestProperty*` | an invariant that fails on an input nobody wrote down; rapid shrinks it to the smallest case |
| API | [testscript](https://pkg.go.dev/github.com/rogpeppe/go-internal/testscript) | `internal/cli/testdata/script/*.txtar` | a change to output or exit codes, which are what CI depends on |
| Fuzz | `go test -fuzz` | `Fuzz*` | a panic or a broken invariant on arbitrary bytes from a config, a check's stdout, or a message embedded in XML |
| Mutation | [Gremlins](https://gremlins.dev) | `.gremlins.yaml` | a test that runs code but asserts nothing about it |
| Lint | golangci-lint v2 | `.golangci.yml` | defect classes listed in that file, one comment per linter |
| Vulnerability | govulncheck | `make vuln` | a known CVE in a function errata actually calls |

Tests live beside the code as `_test.go` files, which is the Go convention and gives a test access
to unexported helpers such as `protocol.message`. The API tests in `testscript` run the real
binary.

A property test states a rule about all inputs, for example "the JUnit output is valid XML and its
counts agree with the report, whatever strings a check prints". When rapid finds a counterexample,
copy the shrunk input into an ordinary unit test so it stays fixed.

The mutation thresholds are 85% efficacy and 85% mutant coverage. The coverage figure is not 100%
because Go's coverage profile starts a `switch` case's block after its colon, so Gremlins reports a
mutant in a case expression as not covered even when a test runs that arm.
