# renovate_match reads a pattern as Renovate does

Renovate finds the files a manager reads with `managerFilePatterns`, and an entry that matches no
file is no error: the manager reads nothing, every run stays green, and no update ever comes. The
k3s suite has two checks for that failure, `argocd_manager_covers_every_application` and
`renovate_scans_where_images_live`, and each needs Renovate's own reading of an entry. An entry is a
regex only between slashes; without them it is a glob, so `^infra/.*\.yaml$` with its slashes
dropped still loads, as a glob that matches no file. ADR 0010 runs a `matchString` over a file; this
is the other matcher, of a string against an entry. An `assert` can now call it:

```yaml
extract:
  patterns: {json: {file: renovate.json, path: 'argocd.managerFilePatterns[*]'}}
  apps: {regex: {files: ['infra/argocd/applications/*.yaml'], pattern: '\A([\s\S])'}}
allow_empty: [patterns]
assert: size(patterns) > 0 && by_file(apps).all(f, patterns.exists(p, renovate_match(f, p)))
```

## The rules

- **What it is.** `renovate_match(s, p)` is Renovate's `matchRegexOrGlob(s, p)`, from
  `lib/util/string-match.ts`: the function behind `managerFilePatterns`, `matchPackageNames`,
  `matchDepNames` and every other key Renovate's documentation calls string pattern matching.
  It reads one entry. The list form, where entries that start with `!` must all match, is written
  in CEL with `filter` and `all`.
- **`*`** matches every string.
- **A regex** is an entry that starts with `/` or `!/` and ends with `/` or `/i`, read as Renovate
  reads it: the text between, with the `i` flag after `/i`, searched anywhere in `s`, and the
  answer inverted after `!`. `^` and `$` match at the ends of `s`. Renovate compiles it with
  `regEx` (`lib/util/regex.ts`), which uses the `re2` module where that loads, as it does in
  Renovate's images, and JavaScript's `RegExp` otherwise; errata reads it with Go's RE2. Where
  Renovate cannot compile the regex it reads the whole entry as a glob, which matches next to
  nothing. errata makes the rule unknown instead, naming the entry: a lookahead, which RE2 lacks,
  would otherwise pass or fail by accident.
- **A glob** is any other entry, which Renovate hands to minimatch with `dot` and `nocase`, and
  errata to `internal/minimatch`, a port of minimatch 10.2.6 with brace-expansion 5.0.12 and
  balanced-match 4.0.4, the versions Renovate 44.198.2 pins. The port follows the JavaScript step for
  step, quirks included: a leading `!` negates and `#` makes a comment that matches nothing,
  braces and sequences expand, `+(a|b)` and the other extglobs nest and adopt as minimatch's
  tree does, a negation takes a copy of the rest of its segment, POSIX classes such as
  `[[:alpha:]]` read Unicode properties, runs of slashes collapse, `.` and `..` segments are kept
  or resolved as minimatch keeps them, and `*.yaml`, `???` and the other common segments use
  minimatch's fast tests in place of their regex, as minimatch does. minimatch turns each segment
  into a JavaScript regex with lookaheads, which RE2 lacks, so the port runs those regexes with a
  backtracking matcher of ECMAScript's grammar, with the `i` and `u` flags. Where JavaScript
  refuses the regex minimatch builds, as it does for `[[:alpha:]], x` under the `u` flag a POSIX
  class sets, minimatch throws, Renovate throws with it, and the rule is unknown, naming the
  glob. Case mapping, case folding and the `\p{..}` classes are those of Node 24.22.2 (ICU 78.3,
  Unicode 17), the Node that k3s's `renovate/renovate:44` image held on 2026-10-30 (Renovate
  44.198.2). Renovate asks only for Node `^24.11.0`, and the image builds in a Node it names, so
  the tables follow the image: Go's tables, with the code points where Node's differ generated
  into `unicode_node.go`.
- **The bounds.** minimatch has none that would stop a glob in a config from stalling a run: x
  followed by 18 of `!(a)` took it 2.5 s, since each negation doubles the tree, and `*(a|aa)`
  repeated backtracks for a time exponential in its length on a string of a's that ends in b. The port refuses such a glob,
  and the rule is unknown, naming the bound: braces that expand past 64 globs or 16384
  characters, a segment tree past 4096 nodes, more than 32768 characters of regex source over
  the glob's segments (x and 10 of `!(a)` pass these two, and 11 do not), and a match past 131072
  steps. Every alternative a regex tries costs a step, an empty one included, so `x@(|)@(|)...`
  meets the step bound too. minimatch's tree also asks a parent `isEnd` twice, which makes
  `a@(a@(...))` compile in time exponential in its depth; the port asks once, with the same
  answer. Measured on a 4 core workstation with arguments of at most 1024 bytes, the step
  bound stops a match within 34 ms. ast.js copies each node's regex source into its parent's
  and unescapes every node's body; the port writes a segment's source once, into one buffer,
  and unescapes only the root's. The node bound refuses as soon as the negations' copies are
  made, before any source is written, and the source bound as soon as the buffer passes what
  is left of the 32768 characters. A negation followed by a tail of 500 `b*`, which took
  1.85 s and 4 GiB before, is refused in 5 ms. The slowest compile measured is 91 ms: four
  brace sets expanding to 16 globs, each a negation and 249 nested extglobs, until the source
  bound refused them. A real entry is far inside them: `infra/**/*.yaml` against a
  path of 32 bytes takes 29 µs.
- **The evidence.** `internal/rule/testdata/renovate-cases.json` holds 6868 cases, 101 entries of
  every form against 68 strings, each with the answer of Renovate's own function, `true`, `false`
  or `throw`. `renovate-oracle.mjs` beside it writes and checks them. `internal/minimatch`'s
  `testdata/cases.json` holds 14501 more, random globs against random paths and a sample of code
  points against globs that fold case or name a POSIX class, checked by `oracle.mjs` against
  minimatch itself. Before they were cut to those samples, 150000 random globs and 3.7 million
  cases over every code point to U+2FFFF agreed with minimatch, apart from the globs past a bound.
- **The cost.** Each argument is at most 1024 bytes, as for every helper. A call costs 1, its
  bytes, and the product of its two lengths over 64. The slowest match measured on a 4 core
  workstation, 1000 bytes against three classes of 165 code points, ran 34 ms before the step bound stopped
  it, so the cost limit stops 63 calls of that kind, about 2.1 s, inside the five second
  timeout. Compiling is charged only by the glob's bytes, though: the 91 ms glob above, against
  a string of 2 bytes, costs about 1050, so the five second timeout, not the cost limit, stops a
  rule that makes about 55 such calls. CEL looks at the timeout only between the steps of a
  comprehension, so the calls inside one step run to their end after the rule is unknown: an
  expression of 4096 characters holds about 150 calls, about 14 s of one core.

## Considered options

- **A key on an extractor**, such as a `matching:` list of entries on a file set. It would read
  only paths, where Renovate matches package names and dependency names the same way, and it
  would put the claim, which files a manager must cover, into what is extracted. A helper keeps
  the extractors reading values and the claim in `assert`, as `semver_satisfies` does for
  versions.
- **`values_as: pattern`** (ADR 0010) runs a regex over a file's text, not over a path, and
  Renovate's slashes, its `i` and its `!` would each need a key. It reads a `matchString`;
  `renovate_match` reads an entry.
- **Part of minimatch, refusing the rest.** The first version read only ASCII globs of `*`, `?`,
  `**` and plain classes, and made braces, extglobs, negation, comments and POSIX classes unknown,
  since each has edge cases no page of rules states. The owner chose on 2026-10-30 to port all of
  minimatch instead, so an entry Renovate reads is one errata reads, and to keep unknown only
  what cannot be ported (a regex RE2 lacks) and what has to be bounded.
- **A JavaScript regex library**, such as dlclark/regexp2, for the regexes minimatch builds. It
  backtracks as the port's matcher does, but reads .NET's syntax and case rules rather than
  ECMAScript's, and the owner's rule is that a feature relies as little as possible on what is
  outside the binary (ADR 0008). The matcher reads the grammar of ECMAScript patterns, minus
  backreferences and octal escapes, so that it refuses what JavaScript refuses.
- **errata's own glob**, the matcher of `files:` (ADR 0008). It is case sensitive, reads `[!a]`
  as a class holding `!`, and matches `a/**` with `a`, which minimatch does not. A rule over
  Renovate needs Renovate's reading, so the two stay apart.

## Consequences

`renovate_match` is part of the config's contract from v0.2.3. The two manager coverage checks of
the k3s suite convert with it and with file sets; `pinned_charts_are_renovate_tracked` and
`every_tracked_manifest_is_deployable` still compare match offsets and a manager's ignore list,
and stay program checks. `internal/minimatch` is pinned to minimatch 10.2.6 and the Unicode tables
of Node 24.22.2, from Renovate 44.198.2's image. `renovate-oracle.mjs` copies that version's
`string-match.ts` and `regex.ts`. When Renovate's image moves to a new minimatch or a Node with
another ICU, both oracles run again under it,
`testdata/unicode/gen.go` and `gen.mjs` regenerate the tables, and a case whose answer changed is
ported.
