# renovate_match stops at the timeout through a hidden argument

An assert runs under a cost limit and a timeout (ADR 0003). CEL looks at the timeout only between
the steps of a comprehension, and an expression of 4096 characters holds about 150 calls of
`renovate_match` in one step. ADR 0014 charges a glob's compile and a regex's match to the cost
limit, so the calls of one step stop on that limit. A step that started just before the timeout,
after work the cost does not count, still ran until the cost limit stopped it: 4.3 s past the
timeout on the 4 core node for the slowest glob ADR 0014 names. Each call now looks at the timeout
itself:

- When the rule loads, after the notes that keep an error `&&` or `||` absorbed, errata rewrites every call
  `renovate_match(s, p)` as `@errata_renovate_match(s, p, @errata_log)`. A name that starts with
  `@` cannot be written in an expression. `@errata_log` is the hidden variable the notes already
  read, one per evaluation, and it now carries the evaluation's context as well, and its glob
  cache (ADR 0014). The hidden function's first two parameters are dyn, so a value typed dyn that
  is not a string reaches its binding, which refuses it with the message plain CEL gives,
  `no such overload: renovate_match(int, string)`, and not one naming the hidden function.
- The hidden call looks at the context before it starts and before a regex runs. A glob is
  compiled by `minimatch.CompileContext`, which looks at each brace set it expands, before it
  parses each segment, and at each node of a segment's tree as it builds the regex source, and
  matched by `(*Glob).MatchSteps`, which looks every 1024 steps under the context of the call that
  runs it, so a glob the cache compiled under an earlier call still stops. A call whose context
  is done returns an error, and the rule is unknown on the timeout, as for any other stop. A
  compile that was stopped is not cached.
- A call that ended after the timeout is charged only the bytes it reads, so the calls that
  stop at once do not reach the cost limit first, and the message names the timeout.

## What it measured

Through the CI runner on the build node, with the 5 s timeout, a step of 4.9 s of a probe
helper the cost does not count, then one `||` chain of calls. The same program with a log that
carries no context gives the figure from before the change. The globs of the first two rows are
distinct, of the shape named, since the cache compiles a repeated glob once.

| calls after the uncharged step | before | after |
|---|---|---|
| 140 of the 1022 byte glob shape of ADR 0014, 42 ms each | 2.13 s past the timeout, on the cost limit | 1.0 ms past |
| 140 of the 168 byte glob shape, 7.4 ms each | 1.02 s past | 1.0 ms past |
| 140 of `(?:\pL*){680}b` against 1024 bytes, 18 ms each | 1.77 s past, on the cost limit | 12 ms past |
| 150 of a regex of 4043 instructions against 1024 bytes, 59 to 68 ms each | 2.35 s past, on the cost limit | 17 ms past |
| 140 of one cached glob that spends every match step against `xy`, 41 ms each | 2.22 s past, on the cost limit | 0.8 ms past |

After the change each of them is unknown on the timeout. A regex call ends past the timeout by
what is left of the match it was in, so the regex rows end anywhere up to one call past it.

A deadline set at a random point of one call, 50 times for each shape, was followed by the call's
return, with an error or with a verdict, within 3.1 ms for the 1022 byte glob, 4.3 ms for one
segment of three negations and 251 nested `a+(` against 200 bytes, 2.7 ms for 200 brace sets,
1.5 ms for the 168 byte glob, and 1.4 ms for the glob that spends every step. With a probe that
recorded the time of each look, the longest stretch of one call without a look was 0.4 to 4.8 ms
over those globs, against 16.4 ms for the one segment glob before the source build looked at each
node, and 3.2 ms for the braces before brace expansion looked at each set.

## What still runs past the timeout

Go's `regexp` takes no context, so a regex that has started runs to its end. ADR 0014 bounds its
program to 4096 instructions, and a call at that bound against 1024 bytes runs about 60 ms on
that node and 96 to 120 ms on the nodes ADR 0014 measured. So an evaluation ends at most one
regex match past its timeout. A glob runs past a look by at most the work between two looks: one
brace set, one node of the source build, the preparation of a segment's tree before the build,
or 1024 steps of the match. A class test counts its work in steps (ADR 0014), so 1024 steps take
at most about 0.14 ms on the build node over the classes measured, where 119 of `[:word:]` took 17 ms
before. Through the CI runner on that node, a deadline at a random point of one match, 30 times
for each of 11 such globs, was followed by the call's return within 3.1 ms; 5 of the 330 calls
returned a verdict after the deadline, at most 1.1 ms after it. Under a timeout of 200 ms, 190
calls of each glob in one step ended 0.5 to 1.0 ms past it, on the timeout. A helper other than
`renovate_match` looks at no context; the bounds on what each reads (MaxArgument, MaxBuilt) keep
its calls short.

## Why not cel-go's AsyncBinding

cel-go 0.32 has `cel.AsyncBinding`, which hands a function the evaluation's context. A program
with such a function cannot run through `ContextEval`; it needs `ConcurrentEval`, which evaluates
the expression in passes. Each pass leaves every async call it meets as an unknown, starts it in
a goroutine of its own, and evaluates the whole expression again once a result comes. For every
rule that calls `renovate_match`, that would change:

- the notes: the log would see each pass, with unknowns where the calls' results were not in yet,
  and `&&` and `||` decide on an unknown otherwise than on a bool;
- the cost: each pass is charged again, so a rule's cost would depend on how many passes its
  calls took;
- short circuiting: an operand that a later pass skips may have run in an earlier one, and by
  default 150 calls of one step would run at once, up to 100 goroutines, where the cost limit
  assumes one core. `cel.AsyncMaxConcurrency(1)` bounds that to one at a time, but the passes,
  their cost and their notes stay as above.

The rewrite changes no operand and no order of evaluation, so the notes, the cost and short
circuiting of every rule stay as they were. The tests of each, which did not change, hold it, and
a test holds that a rule with `renovate_match` costs what it did in plain CEL. One call costs
less: when an argument is an error, cel-go returns before it reads the log, and the hidden call
is not charged at all, where renovate_match was charged 1 and the bytes of its strings. So
`renovate_match(v[0], v[9])` over a list of two values costs 4 rather than 5 and the bytes of
`v[0]`: 4 rather than 6 for a value of one byte, and 4 rather than 17 for one of 12. No work stands behind such a
call, since its binding never runs.

## Considered options

- **cel-go's AsyncBinding.** Above.
- **A program per evaluation**, whose binding closes over that evaluation's context. Through the
  CI runner on the build node, compiling and planning a rule of one `renovate_match` call took
  0.24 ms against 0.045 ms for its evaluation, and ADR 0014's example rule 0.48 ms against
  8.5 ms. For a small rule the plan would cost five times its evaluation, and the hidden
  variable already reaches every call.
- **A higher weight on a glob's compile.** It shortens the overrun in proportion and cuts as much
  from the calls an ordinary rule may make, and it does nothing for work the cost does not count.
- **Accepting the overrun.** The owner chose the context on 2026-10-31.

## Consequences

The hidden log is the one value each evaluation hands its helpers: the context, and the glob
cache of ADR 0014. A helper that has to stop at the timeout takes the same rewrite.
`internal/minimatch` gains `CompileContext` and `(*Glob).MatchSteps`, which take the context;
`Match` is the two under a context that is never done. ADR 0014's statement of the overrun points
here.
