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
  would otherwise pass or fail by accident. A regex whose RE2 program passes 4096 instructions is
  unknown as well, naming the count, and so is one of more than 256 bytes between its slashes,
  one that names more than 16 Unicode classes, and under case folding one whose ranges cover
  more than 4096 code points (The cost).
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
  answer. A class test's time grows with the class, so the port merges a class's ranges, asks each
  POSIX class's properties once, and counts a class test one step more for each 256 units of its
  work, a unit for each range it compares and 64 for each property it asks. `x`, 16 of `@(|)`
  and 119 of `[:word:]` took 2.15 s a match against `xⒶ` through the CI runner on the build node before;
  over the classes measured since, the step bound stops a match within 17.6 ms there. ast.js
  copies each node's regex source into its parent's and unescapes every node's body; the port
  writes a segment's source once, into one buffer, and unescapes only the root's. The node bound refuses as soon as the negations' copies are
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
- **The cost.** Each argument is at most 1024 bytes, as for every helper, and a call with a longer
  one compiles nothing and costs 1, its bytes and their product over 64. A call costs 1, its bytes
  and the product of its two lengths over 64, or for a glob the steps its match took over 8 where
  that is more. A glob costs 18 more for each of its bytes, for compiling it, and a regex 18 more
  for each of its bytes, more for each Unicode class it names and each code point it folds, one
  for each instruction and rune of its program, and more for the onepass pass Go makes of an
  anchored one, once errata has parsed it (amended 2026-11-01 and 2026-11-02, below). A regex that
  ran costs the string's bytes times the instructions of its RE2 program over 160, for matching
  it. The slowest glob match measured on a 4 core workstation, 1000 bytes against three classes of
  165 code points, ran 34 ms before the step bound stopped it, so the cost limit stops 63 calls of
  that kind, about 2.1 s, inside the five second timeout. Measured through the CI runner on a 4
  core node, the 1022 byte glob above compiles in 81 ms and costs about 19450 against a string of
  2 bytes, so the cost limit stops the 52nd call of such globs, each a different one (an
  evaluation compiles a glob once, below), after 4.1 s; a short glob compiles slowest for its
  length, 168 bytes of six brace sets, two negations and 32 nested extglobs in 15.9 ms, and the
  cost limit allows about 312 of those, 5 s of compiling. RE2 matches in time proportional to the
  string times the program, and a counted repetition makes the program up to a thousand times the
  regex's text: `(?:\pL?){999}` written 70 times, 911 bytes, is 139863 instructions, and one call
  of it against 1024 bytes ran 3.2 s. A regex whose program passes 4096 instructions is therefore
  unknown, naming the count, before it runs. The count is first estimated from the parsed regex by
  the size rules Go's own parser applies, and checked exactly after the compile: a regex of 1022
  bytes such as `(?:a*a*...){1000}` compiles to a million instructions, which took up to 390 ms a
  call, and 135 such calls refused only after compiling ran 21 s past the timeout. Refused from
  the estimate, the same 135 end in 0.1 s on the 4 core node. The parse that gives the tree is not
  linear either: under case folding Go's parser folds each code point of a class range one at a
  time, and it merges the ranges of every `\pL`, so `(?i)[` and `a-\x{10FFFF}` 84 times, 1017
  bytes, took 0.79 s to parse, and `[^\pL]` with 339 of them 57 ms. So the parse is bounded by the
  text before it runs: at most 256 bytes between the slashes, 16 Unicode classes, and under the
  `i` flag or `(?i)`, ranges over 4096 code points that Go folds one at a time. k3s's
  renovate.json holds regexes of at most 43 bytes between the slashes, errata's of at most 20, and
  neither names a Unicode class, quotes with `\Q` or writes `\x{`. The scan reads only what it
  reads as Go does: it refuses `\Q`, after which Go reads text as literals, and a `\x{` or `\p{`
  that Go would refuse (a `\p{` name may hold letters, digits, a leading `^`, and `_`, `-` and
  spaces, which Go ignores), so no token can swallow the text after it. It counts every `-`
  between two characters as a range, `[--H]` included, which Go reads as the range `-` to `H`.
  Over generated patterns of classes in context, with escapes, quotes, flags and several classes,
  a test and a fuzz target find it never counting fewer code points than Go's own parse shows Go
  folds for ranges; a single character, or a POSIX or Perl class such as `\w`, Go folds in at most
  63 code points, which the bound on the text holds. Within those bounds the slowest preparation
  measured, both parses (errata's, and the one inside `regexp.Compile`, which takes no parsed
  tree) and the compile, is 5.7 ms on the 4 core node, for `(?i)` and 16 `\pL` in a class, and 7.6
  ms on the 2 core node, for `^(?:\pL?){495}$`, so a regex call measured under 10 ms to prepare
  and about 120 ms to match. An evaluation keeps the regexes it compiled, bounded by their size
  (below): an anchored regex of under 1000 instructions gets a onepass program, which copies each
  instruction's runes, and `(?i)^(?:[^\pL\pN]){990}$` held 9.3 MiB. The slowest rate measured for
  a regex that runs was 28.7 ns for each byte times instruction, for `(?:\pL*){680}b`, so the cost
  limit pays for about 4.6 s of matching. On the 4 core node one call at the bound, `\pL?`
  repeated to 4083 instructions, matched 1024 bytes in 96 ms, and the cost limit stopped 150 of
  them in one step after 3.7 s. A Renovate entry such as `(^|/)values\.ya?ml$` is 20 instructions.

  The weights are in the cost because CEL looks at the timeout only between the steps of a
  comprehension, and checks the cost limit after every call: an expression of 4096 characters
  holds about 150 calls in one step, and before the weights they ran to their end after the
  rule was already unknown, about 14 s of one core for the 1022 byte glob and minutes for the
  911 byte regex. Those 150 glob calls, of 150 different globs, now stop on the cost limit after
  4.1 s on the 4 core node, before the timeout. A step that started just before the timeout,
  after work the cost does not count, ran on until the cost limit stopped it: 4.3 s past the
  timeout on the 4 core node for the 1022 byte glob, against 11.5 s without the weight. Each call
  now looks at the timeout itself (ADR 0016), and that step ends 1 ms past it on the build node;
  what still runs on is one regex match, at most about 120 ms.

  An ordinary glob of 38 bytes costs 684 more for compiling, which would leave a rule about 1300
  calls; the example above, 200 files against 10 entries, is 2000. So an evaluation compiles each
  entry once and charges its compile to the first call of it. One cache keeps the globs and the
  regexes an evaluation compiled, at most 256 of them and 65536 units of their size. A glob's
  units are one for each segment and each code unit of its literal and regex source, and for a
  glob the port refuses, each byte of the regex source its error keeps. A regex's are three for
  each instruction of its program, one for each rune the program holds, and one for each rune of
  the onepass copy Go makes of an anchored program under 1000 instructions. That copy gives each
  instruction a rune set: a capture, an empty width or a nop instruction copies the set of the
  instruction after it, and an alternation merges the sets of both its legs, so a chain of
  captures or carets before a class holds the class once for each link. Each instruction is
  counted the runes of the consuming instructions it reaches without consuming. It drops them when
  it ends, and an entry past either bound is compiled and charged at every call, as every glob was
  before. Measured through the CI runner, 256 small globs held 0.44 MiB, and 107 globs of 200
  segments of `/**/1`, the shape that holds the most for each unit, 61.7 bytes, held 3.8 MiB, so a
  full cache holds about 3.9 MiB at most. 256 small regexes held 0.58 MiB, and 56 anchored regexes
  of 124 empty captures each, the regex shape that holds the most for each unit, 46 bytes, held
  2.8 MiB, inside the same bound. An anchored class of 200 `\pL`, 1.8 MiB in its onepass copy, is
  never kept, and neither is 109 empty captures before a class of four Unicode categories, 3.1
  MiB, which a count of the rune instructions alone kept 19 of, 61 MiB. A glob of 56 bytes, ten
  negations then a class JavaScript refuses, keeps 26092 bytes of source in its error; before the
  size counted it, 256 of them held 6.7 MiB. The example's 2000 calls cost 171053 in all, where a
  compile charged at every call made them 1055807, past the limit. The cache reaches the call
  through the hidden log argument of ADR 0016, and the rewritten call costs what the call did,
  less the compile on an entry already compiled.

  **Amended 2026-11-01 and 2026-11-02: a regex's compile is charged.** Until then a regex compiled
  free, which was safe while a repeated entry stayed compiled. Once the one cache bounds regexes
  by their size, a regex too big for it compiles at every call, and 27000 calls of
  `/^(?:\pL){200}$/` (0.7 to 1.2 ms a compile) or `/(?i)^(?:[^\pL\pN]){990}$/` (12.4 ms) against
  the empty string, about 27 a call, ran to the timeout. A first charge by the entry's bytes alone
  needed 256 a byte to bound the slowest compile, and at that rate a config of 100 distinct
  regexes of 30 bytes spent 77% of the cost limit compiling, and 130 passed it, where each compile
  takes 20 to 50 us. So the call that compiles a regex pays for the work Go's compiler does: 18
  for each byte of the entry, as a glob pays; 256 for each Unicode class it names, which Go's
  parser merges into the class around it, and folds under case folding; under case folding, a
  quarter of the code points its ranges cover, which the parser folds one at a time; one for each
  instruction of its program and each rune the program holds; and for a program Go tries to make
  onepass (under 1000 instructions, anchored at the start of the text), its instructions times its
  instructions and runes over 256. The onepass pass merges the program's rune sets at each
  instruction, so its time grows with both, and the slowest compiles measured are anchored
  programs of about 1000 instructions over a large class, such as `^(?:\pL?){400}$` or an anchored
  class and its complement repeated 330 times, 20 bytes and 10 to 49 ms a compile. The
  instructions alone cover the fixed cost of a large program: about 6 ms for a onepass try of 1000
  instructions with no runes, and about 3 ms for an unanchored program of 2000. The parse terms
  cover what the program does not show: 16 of `\pL` in a class took up to 13.5 ms to parse, with
  case folding and without, and `(?i)[A-\x{1040}]`, 18 bytes, folds 4096 code points in 1.3 ms,
  which a first fit without them charged 382 and let the cost limit pay 4.16 s for. The form was
  fitted over 109 shapes, by the mean of compiles back to back as an evaluation runs them, through
  the CI runner on two nodes. On the slower node the cost limit pays for at most 2.30 s of
  compiling, for `^(?:\pL?){400}$`, and on the other for at most 0.86 s, for `^(?:\pL?){497}$`. On
  a node loaded three to four times over, the onepass shapes came to at most about 4.2 s, for
  `^(?:\pL*){495}$` at 51 ms a compile and 12113 a call. An ordinary entry of 30 bytes such as
  `/^apps/000/[^/]+/values\.yml$/` names no class, folds no range, and costs 595, so 100 of them
  spend 6% of the limit, and 200 leave a rule its verdict. A cache hit pays no compile, and a
  regex the scan of its text refuses pays none either, since no parse ran; one refused after its
  parse pays for its text, since an evaluation that cycles through more than 256 of them parses
  each at every call. The three regexes too big for the cache above, and 2000 distinct regexes of
  either slow parse, end on the cost limit, inside the timeout. The figures that change:
  `renovate_match` over `/a\.yaml$/` and the list of ADR 0016's test costs 223, 783 and 824 where
  it cost 28, 588 and 629, 195 more for the one compile; an ordinary entry such as
  `/(^|/)values\.ya?ml$/` costs 410 more once in an evaluation. An evaluation that the cost limit
  stops after an error that `&&`, `||` or `exists` absorbed now names both. The glob figures, and
  the example's 171053, do not change.

  With the compile charged once, a glob's match is the part of its cost that has to grow with the
  work. The product of the lengths does not: `x`, then 16 of `@(|)`, then a class of 450 code
  points spends all of minimatch's 131072 steps against `xy`, and charged by the lengths alone, 193
  calls of it in one step ran to their end, 10.27 s on the 4 core node, at a cost of 211200. So a
  glob's match costs its steps over 8 where that is more than the product, and a match that spends
  every step costs 16384, which the cost limit pays about 61 times. Since a class test counts its
  work in steps, a step takes about as long whatever the class. Through the CI runner on the build node,
  190 calls of one such glob in one step end on the cost limit after 0.47 to 1.15 s over the
  classes measured: 0.48 s for the 450 code points above, which the class merges into one range,
  0.65 s for 119 of `[:word:]` against `xⒶ`, which ran a full minute on the 4 core node before
  the work was counted, 0.72 s for 470 code points apart, which no merge shrinks, and 1.15 s, the
  slowest, for `[:print:]` and 60 code points apart. A compile charged at every call stopped the 450 code points after 2.22 s
  on the same node.

  The steps charge raises the cost of some ordinary globs: a match that runs a regex with an
  extglob, a class or a `*` can take more steps over 8 than the product over 64. Of 26 ordinary
  globs against 13 paths and strings, 57 of the 338 pairs cost more, by 1 to 8 against the paths
  of 205 bytes or less, and by up to 42 percent against strings of 1000 bytes: `**/!(*.md)`
  costs 35 against `.gitlab-ci.yml` where the product says 27, and `**/*([[:word:]]).yml` 1896
  against 1000 bytes where it says 1333. A glob whose segments minimatch tests without a regex,
  such as `**/*.yaml`, costs the product, and so does the example above. Counting a class test's
  work added a step to 13 of the pairs and changed the cost of none. A cache that outlived the
  evaluation would hold entries from every config errata reads, and a regex cache of 256 entries
  for the whole process once held 2.39 GiB.

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
