# errata reads configuration and never runs it

Configuration lives in data formats and in programming languages: a Django `settings.py`, a
Laravel `config/*.php`, a `vite.config.ts`, a Gradle build, a `Package.swift`. errata reads every
one of them without an interpreter, a runtime or a build tool. A data format has a Go parser
compiled into the binary. A file written in a programming language goes through one literal
reader: a grammar of literal data (strings, numbers, booleans, lists, maps, `name = value`, and a
call with literal arguments) shared by every language, and one table row per language giving its
comments, string forms, assignment and argument spelling, and brackets. Whatever is not a literal
(a call to compute a value, an import, an expression) comes back marked as not literal, and a rule
never compares it as if it were the text. Jsonnet and CUE are the one exception: they are evaluated
by design, and their reference implementations are Go libraries, so errata evaluates them in
process, with no network, inside the repository, and with a time and memory limit.

## Considered options

- **Run the language.** Shelling out to `python`, `php`, `ruby`, `node` or `gradle` would read
  computed values too. It would also make every CI image carry every runtime, execute code from the
  repository being checked, and give a check a different answer on a machine with a different
  interpreter version. A checker that runs the code it checks is no longer a checker of claims.
- **A parser per language.** Correct, but most languages have no pure Go parser, a cgo parser
  breaks the six platform static build, and forty parsers are forty dependencies to keep current.
- **Regular expressions only.** The regex extractor reads one line well, and it stays. It cannot
  tell a value in a string or a comment from a value in code, so it cannot be the reader for a
  whole file.

## Consequences

Adding a language is a table row and its tests, not a dependency. A value a framework computes at
start up is out of reach, and the documentation of each reader says which forms it reads. If a pure
Go parsing runtime with real grammars proves sound (a pure Go tree-sitter runtime exists, and is
young), it can replace the hand written lexers behind the same literal reader without changing a
rule. The binary stays one static file with no runtime beside it.
