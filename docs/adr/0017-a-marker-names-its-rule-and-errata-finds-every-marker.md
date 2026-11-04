# A marker names its rule, and errata finds every marker

A rule names the files it reads, so a claim the rule does not name goes unchecked: a port in a new
runbook, a version in a comment someone added last week. The owner decided on 2026-10-17 that a
claim may name its rule instead. A document tags the claim inline, errata finds every tag in the
repository, and the rule with that id reads it:

```markdown
The API listens on port <!-- errata: api-port -->8080<!-- /errata -->.
```

```yaml
- id: api-port
  extract:
    doc:  {marker: {}}
    conf: {yaml: {file: deploy/api.yaml, path: 'spec.ports[0].port'}}
  agree: [doc, conf]
```

## The rules

- A marker opens with the HTML comment `<!-- errata: ID -->` and closes with `<!-- /errata -->`.
  Its value is the text between, with the blanks at either end left out and each run of blanks,
  a line break among them, read as one space. Markers do not nest. A comment whose text starts
  with `errata:` or `/errata` and that is no opener or closer does not parse.
- In Markdown a marker is a comment the CommonMark parse reads as HTML, inline or a comment block,
  and its value is the prose between, in one block and one table cell. In a file the comment lexer reads (README,
  Code comments), a marker is that HTML comment text written inside a comment of the language, and a value
  runs on only through comments with blanks between them. In HTML and XML a marker is a comment of
  the file. A marker in a code block, a code span or a string literal is no marker; one in any
  other file, reStructuredText and AsciiDoc among them, or in Markdown's other HTML blocks, such
  as a `<div>`, makes the walk unknown, since errata reads no marker there.
- The `marker` extractor's spec takes `files`, the walk, and `max_bytes`; config hands it the id of
  its rule through a hook on the kind (`core.Kind.ForRule`, applied by `extract.Spec.ForRule`),
  since an extractor is built from its spec alone and the registry names no reader. Its values
  are the markers that name its rule, each at its file and the line where the value starts.
- The walk reads git's index as a glob does (ADR 0008): the files a clone holds, unknown in a
  sparse checkout where the checkout leaves a file out, the fixtures of witnesses only on a
  witness's run. It passes over a symlink git tracks that a glob matched, whose target it reads
  under the target's own name when git tracks it, and is unknown for a link `files` names
  outright, since read under the link's name a marker would be bound to a file the built-in check
  reads under the target's. It parses only a file that holds the bytes `errata`. A run reads and parses each file
  once, whichever walks take it in (`check.Shared`, a result the engine's run holds), and each
  walk filters that result.
- A marker it cannot read whole makes the walk fail, and every rule whose walk reads the file is
  unknown (ADR 0001): an unclosed marker, a stray closer, a marker inside another, an id of the
  wrong form, a `<!--` that reads like a marker and is no comment the reader takes for one.
- Whenever a rule reads markers, config adds the check `errata-unbound-markers` after the config's
  own. It walks the files the rules' `marker` extractors walk, together, and is false for each
  marker there whose file no `marker` extractor of the rule it names reads. The owner chose a
  built-in check with its own id on 2026-11-02, and on 2026-11-03 that it walks only those files:
  a marker outside every rule's `files` is not its to report, so a repository whose test fixtures
  hold marker text, as errata's own does, narrows `files` and runs clean. It is a rule, one extractor `unbound` under `size(unbound) == 0`, so the engine,
  the report, errata list and witnesses take it as they take any rule. `required` does not list
  it, and a config check may not take its id while it is added.
- The bounds of ADR 0003: a walk opens at most 100000 files, reads 2 GiB of them, and finds
  100000 markers; a file over `max_bytes` is read in pieces of 1 MiB for the bytes `errata` alone
  and is unknown when it holds them.

## Considered options

- **The rest of the line as the value**, as `# errata: port` above a line would read. A value
  that a line wrap splits, or two values on one line, would be cut wrong without a word. A closing
  marker is unambiguous and survives the wrap.
- **A capture pattern on the rule.** It moves the delimiting back into the config, where the file
  it applies to is not named, and a pattern that misses reads nothing.
- **A syntax of its own for code comments**, such as `errata(api-port)[8080]`. One syntax in every
  file is one thing to learn, and the HTML comment is already invisible in rendered Markdown.
- **Walk the working tree, or parse `.gitignore`.** A run would then read build output and
  untracked files, so a rule could pass in CI and fail on a laptop. git's index is what ADR 0008
  reads for globs already.
- **Skip a file no reader reads.** A marker in it would be a watch silently off, which is what
  the built-in check exists to catch. errata refuses such a file only when it holds a marker.
- **A check kind of its own for the built-in check.** It would need a row in config's kinds, in the
  engine's dispatch table and in errata list, for what a rule with one extractor already says.
- **The built-in check over every file git tracks**, with a top level key to exclude some. One
  file of marker text anywhere, such as a test fixture, would fail every run with no way out but
  the new key. The owner chose the union of the rules' `files` on 2026-11-03: no new key, and the
  rules' own `files` are the way out.
- **Pass over every symlink git tracks**, a link `files` names outright too. Such an entry is a
  request for what the link leads to, so the rule would read the marker under the link's name
  while the built-in check read it under the target's and reported it unbound.
- **Refuse every symlink git tracks.** One tracked link anywhere, such as `AGENTS.md` to
  `CLAUDE.md`, would make every rule over the default walk unknown, and `files` has no way to
  leave a file out.
- **One walk for each rule that reads markers**, and one more for the built-in check. Over 2 GiB
  of Markdown each walk takes about 48 s on the builder node, so ten such rules would take some
  nine minutes.
- **A rule's markers unknown only for a broken marker that names it.** An unclosed marker or a
  stray closer may hide a marker of any rule after it, so the walk cannot say what the file holds
  for any rule. `files` narrows a rule's walk away from a file it does not need.

## Consequences

A claim gets a check from the commit that tags it, and the tag is checked from both sides: a rule
that reads markers and finds none is unknown unless `allow_empty` lists it, and a marker no rule
reads is an erratum. A run reads each file once for all its marker rules, and each rule lists git's
index once against the run's one read of it. The marker syntax is a format a user's
documents carry, and changing it later is a breaking change to every tagged claim.
