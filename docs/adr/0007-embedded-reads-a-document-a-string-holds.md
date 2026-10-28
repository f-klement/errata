# embedded reads a document a string holds

A repository often carries a config file inside a string of another file: Gatus's `config.yaml`
in a ConfigMap's `data`, a GitLab runner's `config.toml` in a Helm value, a JSON list in an env
var's `value`. A path stops at the string, so a rule could read such a config only by a regular
expression over its text, and the k3s repository kept a hand written block scalar reader for it.
A `yaml`, `json` or `toml` extractor takes `embedded`, a list of steps. Each step names the
format its string is parsed in and a path read in that document:

```yaml
region:
  yaml:
    file: infra/observability/15-garage.yaml
    kind: ConfigMap
    path: "data['garage.toml']"
    embedded: [{format: toml, path: s3_api.s3_region}]
```

## The rules

- `format` is `yaml`, `json` or `toml`, and `path` is the path grammar of the three, with its
  filters. A step reads every string the path or the step before it selected, in file order, and
  the values are what the last step selects.
- `embedded` takes at most 4 steps. A step past the fourth, a format left out or one errata does
  not read, and a path left out or that does not parse do not load.
- The string is parsed by the parser a file of its format goes through, with the same
  refusals: a repeated key, a depth past the limit, a tag that does not fit its scalar. An
  embedded YAML string holds at most one document, since a step has no `kind` or `name` to pick
  one of several. An empty YAML string holds none, so its step selects nothing, which
  `allow_empty` decides.
- A value a step reads that is not a string (a number, a bool, null, a mapping or a sequence), and
  a string that does not parse, make the rule unknown (ADR 0001). The message names the file,
  every path of the chain and the line of the string: `values.yaml: path "runners.config" then
  toml path "runners[*].cache.s3.BucketName": the toml document in the string at line 4: toml:
  line 3: ...`.
- The bounds of ADR 0003 count every document an extractor parses out of strings together, as
  one more file. Their bytes may reach `max_bytes` in all, and the nodes they can hold, each
  counted at its format's bytes per node, may too. The nodes YAML aliases and merge keys add
  carry over from the file into its YAML strings, against the one limit. The filters of every
  path share one budget, and the values of every document count against the limit on values and
  on their bytes together. A string an alias repeats counts each time it is read.
- A value is reported at a line of the outer file. Where every string on its way is a YAML
  literal block scalar (`|`), whose lines are the file's lines, that is the line the value stands
  at: the line of the `|` plus the value's line in the document. errata finds the `|` in the
  file's bytes from where yaml.v3 says the scalar starts, past an anchor, a tag, a comment or a
  blank line between them, since yaml.v3 gives a scalar the line of its anchor or tag, which may
  stand on the line above; an alias reports its anchor's lines. Two kinds of block scalar are
  reported as other strings are: one that holds an LS or a PS, since yaml.v3 breaks the file's
  lines there and a JSON or TOML document does not, and one in a UTF-16 file, whose bytes are
  not the text yaml.v3 reads. Any other string folds or escapes its lines, so every value in it
  is reported at the line where the outermost such string starts. No line passes the file's
  last, counted by its `\n`: yaml.v3 also breaks lines on CR, NEL, LS and PS, so in a file with
  such breaks before a block its lines run ahead of errata's count, and a value past the end is
  reported at the last line.

## Considered options

- **A kind of its own, `embedded: {outer: ..., inner: ...}`.** It would leave the three readers
  alone, but it would restate their fields, add a row to the kinds table with every method that
  switches on it, and make one rule spell the same file two ways depending on whether it reads a
  string.
- **Steps nested inside steps**, `embedded: {format: toml, path: x, embedded: {...}}`. It reads
  like the data, but the config's types would hold themselves, and the config decoder's walk of
  YAML 1.1 strings ends because no type of the file does.
- **No limit on the steps.** The bounds already cap the work, but a chain of more than a few
  steps gives a message no one can follow; the deepest real nesting found is two.
- **The inner line alone, or the outer line alone, always.** The inner line points nowhere a
  reader can open. The outer line alone hides which key of a long `config.toml` a rule read. The
  sum is cheap and correct only for a literal block scalar, so only there is it given.

## Consequences

`embedded` is part of the config's contract from v0.2.1. The reader of a format that a string
can hold now receives the parsers table from the facade, as `tool_version` and
`package_version` do, since no reader imports another. A document in a string can make a run
read up to twice `max_bytes`: the file and its strings.
