# A regex pattern references another extractor's values

Some claims say that every name one file lists appears in another, where the second file is free
text: every release a bootstrap script pins with `--version` is named by a Renovate `matchString`,
every chart in a list is mentioned in a runbook. The pattern for the second file depends on the
first file's values, so no fixed pattern can say it. A `regex` extractor's pattern may now
reference another extractor of its rule, by name, as `(?{name})`. The rule runs that extractor
first, and the pattern is searched once for each of its values, with the value in the
reference's place, quoted as a literal:

```yaml
extract:
  releases:
    regex: {files: ['infra/**/bootstrap.sh'], pattern: '^\s*helm upgrade --install ([\w.-]+) \S+ .*--version'}
  tracked:
    regex: {file: renovate.json, pattern: '--install ((?{releases})) '}
allow_empty: [tracked]
assert: releases.all(r, r in tracked)
```

## The rules

- **The spelling.** A reference is `(?{`, the extractor's name, and `})`. RE2 refuses `(?{` as
  syntax, so no pattern that loaded before this change means anything else now. A pattern holds
  at most one reference, to one extractor. A `(?{` that is no well formed reference, a reference
  inside a character class or `\Q...\E`, where RE2 reads it as text, and a pattern that does not
  load with an empty value in the reference's place (one with no capture group, for instance) do
  not load. Only `regex` takes a reference.
- **One pattern per value.** The value goes into the pattern inside a group, `(?:value)`, so a
  repeat after the reference repeats the whole value. Each distinct value, in the order its
  extractor found it, makes one pattern, compiled and searched alone; the extractor returns group
  1 of every match of every pattern, in the order the matches start in the file, and two that
  start at one place in the order of the values. A value that two files gave is searched for
  once.
- **The cost.** Each search is one RE2 pass over the file, linear in its size, so the extractor
  costs the number of values times the file. ADR 0003's bounds hold for the extractor as a
  whole. Checking the values, compiling their patterns and every search share the one regex time
  limit of its `max_bytes` (10 s for each 32 MiB, for `files:` the set's): the limit starts
  before the first value is checked, and a value checked, or a search run, once it has passed
  makes the rule unknown. The matches of every pattern together are held to 100000, and their
  bytes to `max_bytes`, since two values can match the same text. The values a reference reads
  are at most what their own extractor may return: 100000, of `max_bytes` bytes.
- **A value's size.** A value holds at most 4096 bytes, `core.MaxReference`; a longer one makes
  the rule unknown, naming where it was found. A value's pattern is compiled once for each file it
  searches, and up to twice more when it looks back or starts with a literal, and Go's size check
  does not stop a long literal: one value of 1 MiB took 1.4 s and 824 MiB of allocation to
  compile, and one of 4 MiB 5.6 s and 3.8 GB, so a single line of a file could have run the
  runner out of memory. The bound is far above any real value (a Renovate `matchString` is under
  200 bytes) and holds a compile to about 3 MiB.
- **An empty value** makes the rule unknown, naming where it was found. In its place the pattern
  would match its context alone: `--install ((?{releases})) ` would find `--install  x` and
  return the empty text, so `releases.all(r, r in tracked)` would hold for an empty release
  without any file naming one. Leaving the value out instead would hide that the referenced
  extractor gave something no file can name.
- **An empty or failed extractor** (ADR 0001). A referenced extractor that could not be read
  leaves the pattern unable to run, so the rule is unknown, naming both. One that found no value
  leaves the pattern nothing to search for: that is an answer, as an empty glob is in ADR 0008,
  so the extractor returns no value, and the rule's `allow_empty` decides, as for any extractor
  that finds none. The message says why: `extractor "tracked" found no value: extractor
  "releases" gave no value to search for`. A rule that holds when the first list is empty lists
  both extractors in `allow_empty`.
- **Not literal values, and normalize.** A pattern searches only for the literal values, after
  `skip_not_literal`. A not literal value that `skip_not_literal` does not leave out already
  makes the rule unknown, and is not searched for. A value is searched for as its file holds it,
  before `normalize`: a value lowered for the comparison would miss the text it came from in
  the other file too. A pattern that should ignore case says so itself, with `(?i)`.
- **The order.** A rule runs each extractor after the extractors it references, and otherwise in
  the order it ran them before: by name for `assert`, in the order of `agree` for `agree`. The
  report lists them in the order they ran. The dependency is checked at load: a reference to an
  extractor the rule does not declare, and a cycle of any length, a pattern referencing its own
  extractor included, do not load, and `errata check` exits 2.
- **File sets.** With `files:`, every file of the set is searched for every value, and each value
  keeps the file it was found in, so `by_file` groups a referencing extractor's values by the
  files it searched, as for any regex. The referenced extractor may read a set too; its values
  are searched for wherever they came from.
- **A value as a pattern.** `values_as: pattern` puts each value in as RE2 syntax rather than
  quoted, for a rule that runs a regex another file holds, such as a Renovate `matchString`. The
  value goes in as `(?-m:value)`, so its `^` and `$` match at the start and end of the text, as in
  a JavaScript regex without the `m` flag, which is how Renovate compiles a `matchString`; the
  pattern around it keeps the `m` flag. What matches is still RE2's reading of the value, which is
  not JavaScript's in every case: `\s` matches the Unicode spaces in JavaScript and only the ASCII
  ones in RE2, and `.` stops at `\r`, U+2028 and U+2029 in JavaScript but only at `\n` in RE2. RE2
  also accepts syntax that JavaScript rejects or reads otherwise: an inline flag such as `(?m)`
  inside a value, `\p{..}` classes, `\Q...\E`, `\A` and `\z`, `\x{..}` and POSIX classes such as
  `[[:alpha:]]`. A `matchString` free of all of these, over files of ASCII text without CR,
  matches what a JavaScript `RegExp` matches. Renovate may compile a `matchString` with the `re2`
  module where it is installed rather than with `RegExp`; errata has not checked which, and reads
  every value as Go's RE2 either way. The key has refusals of its own. At load, a reference that
  stands before capture group 1 is refused, since a value's own groups would then come first and
  group 1 would be a value's. When the rule runs, a value that is no RE2 pattern by itself, which
  keeps it from closing the group that holds it, and one that ends inside `\Q`, which would run on
  into the pattern around it, make the rule unknown, naming the value and where it was found, and
  so does a value whose groups take the pattern past 32. A JavaScript pattern that RE2 lacks, such
  as a lookahead, is unknown in the same way, never searched as something else. `values_as` on a
  pattern with no reference, or with any value but `literal` or `pattern`, does not load.
  `literal` is the default, and `errata list` writes it out.

## Considered options

- **`{{name}}` or `${name}` in the pattern.** Both read well, and both are text a real pattern
  holds: Helm templates and shell scripts, two of the files a reference searches, are full of
  them, and `${name}` already loads as RE2. A spelling RE2 refuses can mean only the reference.
- **A key beside the pattern, `values: name`, with a placeholder.** The placeholder would need a
  spelling anyway, and two keys can disagree. The name in the reference says where the values go
  and whose they are.
- **One alternation of every value, `(?:v1|v2|...)`.** One pass over the file, but RE2 runs an
  alternation leftmost first, so where two values match at one place only the first is returned
  (`a` hides `ab` in `chart: (a|ab)`), and the program grows with every value's bytes, past what
  Go's regexp compiles for a long list. Its cost is also the values times the file, through the
  threads the alternation keeps alive, so it buys nothing against the bound.
- **No way to use a value as a pattern.** The request was for values quoted as literals unless
  stated otherwise, and the owner preferred not to offer the other unless it came behind a key
  with its own refusals. The k3s repository's `k3s_plans_agree` asserts that Renovate's own regex
  matches the version pins, which only running that regex can show; a rule over the regex's
  literal prefix would pass with its version pattern broken. So the key exists, it is never the
  default, and each way a value could reach out of its group is refused.
- **Several references, or several extractors, in one pattern.** Two references multiply their
  values, and no claim found needs it. A pattern references one extractor once; a rule that
  needs two writes two extractors.

## Consequences

`(?{name})` and `values_as` are part of the config's contract from v0.2.2. A rule's extractors no
longer always run in name order, so a rule that references runs in an order of its own, which its
report shows. An extractor that reads other extractors' values names them in its description
(`check.Description.Reads`, since v0.2.3), and a later kind, or a plugin, can read them in the
same way. The regex extractor keeps one search per pattern in
`core.Each`, so a change to what one search returns, such as a match with its context, is made
in `Search` and reaches a referencing pattern with it.
