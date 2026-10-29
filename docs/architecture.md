# Architecture

How the code is split, and why. The vocabulary is [GLOSSARY.md](../GLOSSARY.md).

## Packages

Dependencies point one way: `cli` imports the packages below it, and `check` imports nothing from
errata. `config` imports `extract` and `rule`, because loading a rule builds its extractors and
compiles its assert. `extract` imports one reader package for each kind of extractor, each reader
imports `extract/core`, and no reader imports another, so a reader can become a program of its own. `rule` owns CEL and imports only `check`: it evaluates over the values its
extractors return and never learns how a file is read. `check` holds the types that cross those
seams, `Value`, `Location`, `Extractor` and `Assertion`, so `rule` and `report` read a value's
place without importing `extract`. `report` takes errata's version as an argument and does not
import `cli`.

| Package | Owns | Knows nothing about |
|---|---|---|
| `cmd/errata` | `main`: signal handling and the exit code | anything else; it calls `cli.Execute` |
| `internal/cli` | flags, wiring, the exit code contract (0 clean, 1 errata, 2 usage) | how a check runs or how output looks |
| `internal/config` | the `errata.yaml` format, strict decoding, load errors, the line each check starts on; builds each rule's extractors and compiles its assert through `rule.Compile`, so a type error is a load error | running anything |
| `internal/engine` | which checks run, in what order, and which are skipped | the check protocol's details, output formats |
| `internal/protocol` | the check protocol: run a program, map its exit code to a verdict; run a generated check's generator and compare its stdout with the committed file | config, providers, output |
| `internal/rule` | evaluating a rule: run its extractors, compare their values or evaluate its assert, fail closed; the CEL environment, the assert helpers, the embedded time zone database and the limits | how an extractor reads a file, config, output |
| `internal/semver` | versions and ranges as npm, Cargo, PEP 440, Go modules and RubyGems write them: their order, precision, satisfaction and overlap, for agree's `compare: semver` and the assert helpers alike | files, rules, CEL |
| `internal/extract` | the kinds of extractor a rule names: the `Spec` a rule decodes into, the kinds table, `Build`, `Place`, `Resolved` and `Kind`, which see every kind and dispatch to its reader | how any kind reads a file; rules, config, output |
| `internal/extract/core` | what every reader shares: the source held inside the repository and to its size cap, the walk of the repository and the directories it passes over, the limits on nodes, values, depth and bytes, the tree and the path grammar with its filters, the line index, quoting, the clocked regex search, the entry extractor and work meter of the INI, properties and env file readers, and the files git's index tracks, read without git | any one format |
| `internal/extract/yaml`, `json`, `toml` | one reader each that parses its format into core's tree, for core's path to select from | each other |
| `internal/extract/regex` | the regex extractor, which is core's search over the file | the other readers |
| `internal/extract/text` | Markdown prose, searched with core's search | the other readers |
| `internal/extract/xml` | XML, with an XPath 1.0 subset of its own | the other readers |
| `internal/extract/ini`, `properties` | one reader each of entries for core's entry extractor: INI in the plain, git, ansible, tox and php dialects, and Java properties | each other |
| `internal/extract/envfile` | dotenv files as docker compose reads them, a reader of entries for core's entry extractor, with compose's substitutions resolved from the keys set before | the other readers |
| `internal/extract/shell` | the assignments and defaults of a variable in a shell script, parsed with mvdan.cc/sh/v3 under a guard on the depth of its stack, and the values the shell computes marked not literal | the other readers |
| `internal/extract/jenkins` | the value of an environment variable that a stage of a declarative Jenkinsfile sees, read with a lexer of Groovy's comments and string forms that keeps no statement, and the values Groovy computes marked not literal | the other readers; Groovy's evaluation |
| `internal/extract/toolversion` | the files that pin a tool's version, read by their names: its own line reader, golang.org/x/mod/modfile for go.mod, and the JSON and TOML parsers the facade hands it for the manifests and rust-toolchain.toml | the other readers, whose parsers it gets from the facade |
| `internal/extract/gitlabci` | the value one job of a GitLab CI file sees: local includes, extends, `!reference`, defaults and the variable precedence, read with the YAML parser the facade hands it | the other readers, whose parser it gets from the facade; the network |
| `internal/extract/githubactions` | the value one step or job of a GitHub Actions workflow sees: env: precedence, the matrix's jobs, and the values only GitHub evaluates marked or unknown, read with the YAML parser the facade hands it | the other readers, whose parser it gets from the facade; the network |
| `internal/extract/env` | one variable across the files a rule lists: the reader each file's name or shebang chooses, a glob over git's tracked files, and each value's origin, its reader and its job, step or stage | the readers, which the facade hands it as functions |
| `internal/extract/extracttest`, `coretest` | what the extractors' tests share; only test files import them, and Gremlins and the `make cover` total leave them alone | any reader: `coretest` imports `core` alone, and `extracttest` nothing of errata's but `check`, since `core`'s own tests import it |
| `internal/provider` | the provider interface, capabilities, the reconciler model, scope gating | any concrete live system (none yet) |
| `internal/report` | output formats: `text`, `junit` and `sarif` for a run, `text` and `json` for `errata list` | where the results came from |
| `internal/heapcap` | the heap watchdog that every package's tests link under the `mutation` tag only | errata's own code: only test files built with the tag import it |
| `internal/check` | the domain types: `Check`, `Rule`, `Extractor`, `Assertion`, `Value`, `Location`, `Extracted`, `Scope`, `Verdict`, `Result`, `Report` | every other package |

Four seams carry the design:

1. **`engine.Runner`.** The engine calls a function to run a check. Production passes
   `engine.Dispatch`, which sends a program check to `protocol.Run`, a generated check to
   `protocol.RunGenerated` and a rule to `rule.Run`, so one run holds all three and prints one
   coverage line; tests pass a fake. The two in `protocol` start their program through one
   function, so a generator gets a program check's timeout, environment and process group. A further runner, such as
   an in-process Kubernetes ownership check, plugs in here without the engine changing.
2. **`provider.Provider`.** It declares capabilities, so the engine can skip a `live` or `applied`
   check by name when nothing can reach it. The render, read and predict methods are left out on
   purpose: their signatures should come from the first real provider (Kubernetes), not from a
   guess.
3. **`report.Reporter`.** Text, JUnit and SARIF are three implementations over the same
   `check.Report`; an interactive view would be a fourth. `report.Formats` makes each with
   errata's version, which SARIF writes as the driver's version.
4. **`check.Extractor`.** `extract.Build` turns a spec into one, and `rule` calls it with the
   repository root. A new file format is a new reader, a package under `internal/extract` with a
   `Spec` and a `Build` that import `core` and no other reader, and one row of the kinds table;
   `rule` does not change.

## Decisions taken in the code

**cobra for the CLI, not Bubble Tea.** Bubble Tea is a framework for interactive terminal
programs. errata runs in CI first, where there is no terminal, and its contract is an exit code and
a file. cobra is the standard Go command and flag library (kubectl, helm and gh use it). An
interactive view of a report can come later as a `report.Reporter` built on Bubble Tea, without
touching the engine.

**Strict YAML decoding.** `sigs.k8s.io/yaml` with `UnmarshalStrict` rejects an unknown key. A
misspelt `reson:` that the decoder ignored would load a check without its reason, and a misspelt
`scope:` would change what the check needs. Both would fail silently. That library is also the one
`client-go` uses, so the Kubernetes provider adds no second YAML stack. It decodes through
`encoding/json`, which matches `Agree:` to `agree` regardless of case and keeps one of the two when
both are present, so `config` captures the JSON before that decode and refuses a key that differs
from a known one only in case. The library also reads only the first YAML document, so `config`
counts the documents with `go.yaml.in/yaml/v3`'s decoder and refuses a file with a second one
rather than drop the checks after it. A decoder, not a scan of the lines, because only a decoder
knows every line break YAML has and an encoding such as UTF-16; FuzzParse holds the count to what
`go.yaml.in/yaml/v2`, the library's own decoder, reads. The same decoder gives the line each check
starts on, which a SARIF result of a program check points at. yaml/v2 reads YAML 1.1, and the
library turns a bool, a number or null back into a string where the field takes one, so `id: y`
loaded as `true`. `config` walks the yaml/v3 nodes against the types the file decodes into and
decodes each plain scalar in a string field on its own; one that loads as a string other than its
text, such as `y` as `true` or `007` as `7`, is refused.

**A check that cannot evaluate is unknown, and an unknown fails the run.** A crashing program, a
timeout or a missing binary gives `VerdictUnknown`. It is kept apart from the errata, which hold
only claims shown false, and it fails the run all the same. A skip is only for a scope that no provider can
reach, and the coverage line names each one with its gap. A skip passes the run unless
`--fail-on-skip` sets `check.Report.SkipsFail`; `Report.Failed` then counts it, and each reporter
writes it as a failure, so the output agrees with the exit code.

**A rule's extractors are `check.Extractor` values.** `config` decodes `extract:` into
`extract.Spec` and calls `extract.Build`, so `check` keeps importing nothing from errata and
`rule` evaluates a rule without knowing what file format an extractor reads. Load errors list a
rule's extractors in name order and its verdict messages in the order `agree:` names them, so the
same file gives the same output on every run.

**An assert is CEL, checked at load and bounded twice.** Every extractor is a `list(string)`
variable, so the checker knows each type when the file loads, and helpers turn strings into
numbers, durations and ports. The parser refuses an expression deeper than 32 or longer than 4096
code points, because type checking runs before any limit on evaluation. Evaluation runs under a
CEL cost of 1,000,000 and a 5 second timeout, which CEL checks after every step of a
comprehension. Any error during evaluation makes the rule unknown, even one that `&&` or `||`
would decide around: each of their operands is rewritten into a hidden note that records an error
for that evaluation, and `exists` and `all` run every member, so the verdict does not depend on
the order of the values. A helper reads at most 1024 bytes. Named time zones come from an IANA
database embedded from the Go toolchain errata is built with, never from the machine, so a
verdict is the same on every runner. depguard holds `internal/rule` and `internal/semver`, which it reads versions with, to an allow list of imports.
Two packages on it can read a file, `time` with `LoadLocation` and `archive/zip` with
`OpenReader`, and forbidigo, not depguard, refuses those two calls.

**An extractor bounds time and size, and lets memory fail closed.** A file in a pull request is
input errata does not control. Every pass over a file is linear in its size or has a bound of its
own, and errata enforces named bounds: the file size (`max_bytes`), a node count scaled from it,
the nodes YAML aliases add, the values per extractor, the depth, the capture groups, the regex
time, the work of a path's filters, the Markdown parse's time and counts, the work of an INI,
properties or env file read, the lines and tokens of a go.mod, and the entries and depth of a walk of
the repository. A file or a walk past a bound makes the rule unknown. Peak memory is not promised, since it depends on a file's shape; a
file that exhausts it ends the process, which fails the job. ADR
[0003](adr/0003-extractors-bound-time-and-size-and-let-memory-fail-closed.md) has the reasons.

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
| Fuzz | `go test -fuzz` | `Fuzz*` | a panic or a broken invariant on arbitrary bytes from a config, a check's stdout, an extractor's file or path, an assert, or a message embedded in XML or SARIF |
| Mutation | [Gremlins](https://gremlins.dev) | `.gremlins.yaml` | a test that runs code but asserts nothing about it |
| Lint | golangci-lint v2 | `.golangci.yml` | defect classes listed in that file, one comment per linter |
| Vulnerability | govulncheck | `make vuln` | a known CVE in a function errata actually calls |

Tests live beside the code as `_test.go` files, which is the Go convention and gives a test access
to unexported helpers such as `protocol.message`. The API tests in `testscript` run the real
binary.

A property test states a rule about all inputs, for example "the JUnit output is valid XML and its
counts agree with the report, whatever strings a check prints". When rapid finds a counterexample,
copy the shrunk input into an ordinary unit test so it stays fixed.

Gremlins builds the tests with the `mutation` tag and reruns a package's tests once for every
mutant, so a test that takes seconds costs seconds per mutant. That is why each reader is a package
of its own: a mutant of one reader reruns that reader's tests, not every reader's. A test belongs to
the package of the code it checks, so a mutant there is killed where it is: the tests that read
files through every kind to check a bound of `core`, such as the source checks or the path's
filters, are `core`'s external tests (`package core_test`), which import the readers and reach what
only they need through `export_test.go`. Such a test skips under the tag:
`make test` runs it, or `make slow` when it skips under `-race` too. The replay of measured inputs
keeps its cases that stop on the regex clock under the tag, since the clock's counter is a mutant
only they kill.

A mutant that breaks a bound can make a test allocate without limit faster than Gremlins' timeout
fires, and the kernel then kills the whole run. So under the tag each package's tests link
`internal/heapcap`, whose watchdog exits a test binary whose Go heap and stacks pass its cap, and
which bounds each goroutine's stack; Gremlins counts that failure as the mutant killed.
`TestEveryTestedPackageLinksTheWatchdog` holds every package with tests to the link, and
`.gremlins.yaml` leaves the watchdog unmutated.

`make mutation` judges each package alone, the facade, `core` and every reader each in a pass of
its own, so a mutant of a reader counts as killed only when that reader's own tests fail. The
thresholds are 85% efficacy and 85% mutant coverage. The coverage figure is not 100% because Go's
coverage profile starts a `switch` case's block after its colon, so Gremlins reports a mutant in a
case expression as not covered even when a test runs that arm. Where such mutants cap a package,
`core`, `ini`, `properties` and `xml` among them, its floor is the figure it reaches;
`ci/README.md` (Floors per package) has the table.
