# An unknown fails the run without being an erratum

A check that could not evaluate (its program crashed, did not finish in time, or could not be found)
gets the verdict unknown. We keep unknowns out of the errata, because the errata are the claims a
check showed to be false and an unknown showed nothing, and we still make an unknown fail the run
with exit code 1, because a suite that passes on what it could not test goes green on a subset and
says nothing about it.

## Considered options

- **Count an unknown as an erratum.** This was the first implementation. It made the output assert
  that a claim was false when nobody knew, which is the kind of false claim errata exists to find.
- **Let an unknown pass, or give it an exit code of its own.** A pass is the fail-open behaviour the
  project is built against. A separate exit code would make every CI configuration choose whether
  to treat it as a failure, and most would get the choice wrong by not making it.

## Consequences

Exit code 1 means "at least one erratum or unknown", and the summary line counts the two apart
(`1 erratum, 1 unknown`). Once a release ships, changing either is a breaking change to every CI
job that runs errata.
