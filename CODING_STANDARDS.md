# Coding standards

Rules a reviewer applies to a change. They are judgement calls: anything a tool can check is a
check in `make check` instead, and is not repeated here.

## Tests run code the way production runs it

A test of a script calls it exactly as its caller does: the same working directory, the same
relative or absolute paths, the same environment variables. For a CI script that caller is
`.gitlab-ci.yml`, so read the job before reading the test. A test that passes an absolute path
where CI passes a relative one tests a different program: that is how `ci/autopatch.sh` passed
every test and stopped on its first night.

## Every guard has a witness

A check, a refusal or a test that guards a rule is shown failing on a planted break of that rule
before it is trusted: change the input it should refuse, watch it refuse, restore the input. Name
the planted break in the commit or the test. A guard nobody has seen fail may never fail, and a
suite of such guards goes green on nothing.

A refusal is tested for its refusal, not for any failure: assert the message (`REFUSED`, the named
reason), so a crash for an unrelated reason does not pass as the guard working.

## A fuzz oracle reads the line as the reader does

A fuzz oracle that holds a reader's text to the line it came from compares it with the line as
the reader reads it: `extracttest.RawLine`, which keeps the blanks at both ends, or that line
put through the same decoding the reader applies. `core.LineText` trims the line, so a correct
read of a value that keeps a trailing tab or space fails against it. The table and udev oracles
each compared with `core.LineText`, and in v0.4.0 each cost a red gate and a fix round.

## A function scores CRAP below 10

The CRAP of a function is `comp^2 * (1-cov)^3 + comp`, where comp is its cyclomatic complexity
and cov the share of its statements the tests run. Every function scores below 10. At full
coverage that is a complexity of 9 at most; at complexity 9 the tests run more than 76.9% of it.
`make cover` scores each function of `internal/` and `scripts/` from the profile `make test`
writes (`scripts/crap`, which counts as unclebob/crapper counts Go: 1, plus each `if`, `for`,
`range`, each `case` and `default` clause, and each `&&` and `||`, a closure's counted in the
function that holds it). `cmd/errata` has no tests of its own, so no profile covers it; a
reviewer holds its functions to the rule.

A parser is an aggregate of small composite helpers, kept in the same file as the parser they
make up: its complexity is broken up and still in one place. A helper moved to another file or
package to score better is not simpler, and a reader of the parser then has to find it.

`scripts/crap-baseline.txt` lists the functions that scored 10 or more when the rule came in
(b4bcbad6), each with its score. A function with no row scores below 10, a row's score may not
rise, and the change that takes a listed function below 10, or removes it, removes its row. No
row is ever added: every row is a row of the file's first version, at that score or lower
(`ci/tests/crap.sh`).
