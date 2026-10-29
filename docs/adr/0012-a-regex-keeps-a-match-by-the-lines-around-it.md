# A regex keeps a match by the lines around it

Some claims hold of a match only in a context. The k3s check provisioning-writes-git-not-the-cluster
says that node-provisioning.sh runs no `kubectl apply`, `delete` or `annotate`, except an annotate
that asks Argo CD to refresh, an apply of the k3s upgrade Plans, and anything in a comment. Each
exception is text on the same line as the match, which a regex could say only with lookaround.
Go's regexp is RE2, which has none, since lookaround is what costs a backtracking engine its
linear time. A `regex` extractor may now take a `context`:

```yaml
extract:
  changes:
    regex:
      file: scripts/node-provisioning.sh
      pattern: '\b(kubectl[^\S\n]+(?:-n[^\S\n]+\S+[^\S\n]+)?(?:apply|annotate|delete)|helm[^\S\n]+(?:upgrade|install))\b'
      context: {without: '^\s*#|^(?:[^;&|`$<>]|[$<>][^(;&|`$<>])*(?:infra/k3s-upgrade/|argocd\.argoproj\.io/refresh)(?:[^;&|`$<>]|[$<>][^(;&|`$<>])*$'}
allow_empty: [changes]
assert: size(changes) == 0
```

`[^\S\n]` is a space that is no newline, so a command never starts on one line and ends on the
next, which would make both lines its context. A context of 0 lines is the whole line, the text
before the match as well as after it, so an exemption anywhere on the line exempts every command on
it; the `without` above exempts only a line with no `;`, `&`, `|`, backtick, `$(`, `<(` or `>(`, so
a command chained after an exempt one, or run in a command or process substitution, is still found.
A `$`, `<` or `>` is allowed only before a character that is none of those, so a redirect such as
`2>&1` or `>>` on an exempt line is refused too, and so is a line that ends in one of them, since
nothing follows it. Both fail closed. The owner decided on 2026-10-27 that this is built into the
regex extractor, not left to a program check.

## The rules

- **The lines.** A match's context is the lines from the one it starts on to the one it ends on,
  `before` lines before them and `after` lines after them, cut to the lines of the file. A match
  that ends with a newline ends on the line the newline ends, and a file's final newline ends its
  last line and starts none, so whether a file ends with one does not change what a context
  holds. `before` and `after` count lines
  and default to 0, so a context without them is the match's own lines. Neither may be below 0;
  either may be larger than the file.
- **Two patterns.** A match is kept when `with` matches in a line of its context and `without`
  matches in none of them. A context names one of the two or both; one with neither does not
  load. Each is RE2 with the `m` flag, as a regex pattern is, and neither may reference values.
  A pattern that matches the empty text at the start of every line, such as `x*`, `^` or
  `(foo)?`, or at the end of every line, such as `$`, does not load: as `with` it would hold
  everywhere, and as `without` it would drop every match, so a stray `?` in `^\s*#?` would turn a
  guard into one that always passes. errata tells them from its syntax tree. `^$`, which matches
  only an empty line, and `\b`, which needs a word character beside it, load.
- **Each line alone.** A context pattern is searched in each line as though the line were the
  whole text: nothing in it matches a newline, so `a\sb` does not match `a` at the end of one line
  and `b` at the start of the next, and `\A` and `\z` match at the start and end of each line, as
  `^` and `$` do. Its groups capture nothing, so a named group in it is no field of anything.
- **What is returned.** A kept match gives what it gives without a context, group 1, at the line
  it starts on. A dropped one gives nothing. The 100000 values and `max_bytes` bytes an
  extractor may return count the matches before the context drops any, so a pattern that matches
  too often is refused however few matches its context keeps.
- **References.** For a pattern that references another extractor's values (ADR 0010), the
  context applies to the matches of every value. Which lines have it does not depend on the value,
  so each context pattern is searched once for each file, not once for each value.
- **File sets.** With `files:`, each file's lines are its own: a match on the first line of one
  file has no line of the file before it in its context.
- **The cost.** Each context pattern is one search of the file. Once it finds a match in a line it
  searches on from the next line, so it reads each line about once whatever it matches: it costs
  the file times the threads the pattern keeps alive, as a regex does, and one call into RE2 for
  each line it matches in. errata keeps the lines it matched as one bit for each line, with the count of the bits
  before every 64, so whether a context holds is two lookups however wide it is: the whole is
  linear in the file and the matches, and does not depend on `before` or `after`. 32 MiB of empty
  lines take 8 MB of bits and counts. The context's searches share the time limit of the
  pattern's search, the regex time of `max_bytes` (ADR 0003), and for a pattern that references
  values the one limit every value's search shares. A context pattern that runs past it makes the
  rule unknown, naming the pattern. A file the pattern finds nothing in is not searched for a
  context at all. On the slowest CI node, over 32 MiB of short lines with 99274 matches, a
  context as wide as the file took 4.1 s against 2.2 s for the pattern alone; a context of 0
  lines whose `without` matched in 8 million lines took 5.9 s; 32 MiB of empty lines, each
  matched by `with: '^$'`, the most calls a file can cost, was refused at 10 s. The process
  peaked at 257 MiB.

## Considered options

- **A program check.** The claim stays expressible as a script, as it was. The owner chose to
  build the context into the regex extractor, since a claim that needs one is common in shell
  scripts and runbooks, and a program check gives up errata's bounds and its report of each value.
- **A context pattern searched over the context's text as a whole**, so it could match across
  lines. Each match would need a search of its own context: 100000 matches with a context as
  wide as a file of 32 MiB is 100000 searches of 32 MiB. One search of the whole file cannot stand
  in for them, since RE2 returns matches that do not overlap, and the one it returns at a place
  may run out of one match's context where a shorter one inside it would not. Searched in each
  line alone, a pattern's matches in the whole file are exactly its matches in each line, which a
  property test checks against a search of every line.
- **A lookahead after the match**, such as a `followed_by` pattern, to any distance or only to the
  end of the match's line. It costs a search from the end of each match, the matches times the
  text it reads, and needs a bound of its own. A context of 0 lines is not that lookahead: it is
  the whole line, before the match too. The k3s check needed the exemption after the command; its
  rule says instead that the line holds no other command, by refusing `;`, `&` and `|`, which is
  stricter and fails closed. A lookahead waits for a claim that a line's context cannot express.
- **Returning the context's lines as values.** That is another extractor's job: what the context
  decides is whether a match counts, and the value is still the match's group 1.

## Consequences

`context`, with `before`, `after`, `with` and `without`, is part of the config's contract from
v0.2.2. `errata list` writes `before` and `after` out. The context is part of where a regex reads,
so two regexes that differ only in it are two places, and `agree` compares them. The compiled
context lives in `core`, beside the search, so the `text` extractor or a later kind that searches
with a regex can take it with one key. A pattern with several named groups that returns one
record for each match keeps or drops each record with its match, the same way, since the context
decides which matches count and not what a match returns.
