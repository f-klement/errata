# A record is a map of the fields a rule names

A path or a pattern returns a flat list, so two fields of one element reach a rule as two lists
that no longer say which value belongs with which. Gatus's push keys are `<group>_<name>` of one
endpoint, and a query check's source is in its `url` while the words that must name it are in its
alert's `description`. ADR 0009 hands a mapping over whole, which keeps every field together but
also every field the rule does not read, and a regex match is no mapping at all. So an extractor
can return records: one mapping for each element or match, whose keys are the fields the rule
names, each with its own line.

```yaml
extract:
  gatus:
    yaml:
      file: infra/gatus/gatus.yaml
      kind: ConfigMap
      path: "data['config.yaml']"
      embedded: [{format: yaml, path: "external-endpoints[*]"}]
      as: record
      fields: {name: name, group: group}
  images: {regex: {file: deploy.yaml, pattern: 'image: (?P<repo>[^:\s]+):(?P<tag>\S+)', as: record}}
```

## The rules

- `as: record` on a `yaml`, `json` or `toml` extractor takes `fields`, a map from a field name to
  a path read from each node the extractor's path selects, after the last `embedded` step. `fields`
  without `as: record`, `as: record` without `fields`, and two fields over one path do not load:
  `agree` over two such fields would compare each node with itself, as `agree` over two
  extractors of one place would.
- `as: record` on a `regex` extractor reads the pattern's named groups as the fields of each
  match, in the order the pattern opens them. A group without a name is no field. A pattern with no
  named group, or one name given to two groups, does not load. `as` on a regex takes no other word.
- A field name is a letter or `_`, then letters, digits and `_`, the form CEL reads after a dot,
  and none of the words CEL's language definition reserves. It keeps `true`, `false`, `null` and
  `in` out of field names, so `r.in` does not parse, and reserves the other seventeen for the
  languages that embed CEL. cel-go reads those after a dot today, but the definition advises
  against them as field names, and refusing them now keeps a later cel-go from breaking a rule
  that loads.
- A field takes the node its path selects whole: a scalar is the string a flat value would have,
  a mapping a map and a sequence a list, as ADR 0009 gives them. A field whose path selects nothing,
  or a named group that took no part in the match, is left out of that record, so `has(r.f)` tests
  it. A field path that selects more than one node makes the rule unknown at the element's line,
  and so does a scalar or a sequence that holds none of the fields, such as a ConfigMap's string
  selected without its `embedded` step. A mapping that holds none is an empty record: a field may
  be optional, and a field that no record holds is decided by `allow_empty` below.
- A path record's keys are sorted by name, since errata.yaml's `fields` is a map whose order the
  loader does not keep. A regex record's keys follow the pattern.
- The record is at the line where its element or match starts, and each field at the line where
  its value starts, through the lines of a document in a string (ADR 0007).

## How records meet the rules for flat values

- **assert.** A record is the `check.Data` of kind `DataMap` that ADR 0009 announced: the same
  `list(map(string, dyn))`, the same strings, and the same refusal of a `dyn` compared with an
  operand whose checked type it never equals. errata adds no CEL type and no function for records.
- **agree.** An entry of `agree` names an extractor, or, when no extractor has that name and it is
  `x.f`, the field `f` of the records `x` returns. Each record's field is compared as a flat value
  at the field's own line, and the message names it under `x.f`. A record without the field adds
  nothing. A field holding a mapping or a sequence makes the rule unknown. Naming a record
  extractor whole, a field its records do not have, or a field of an extractor that returns no
  record does not load. The extractor runs once however many of its fields `agree` names, so two
  fields of one record can agree with each other. An extractor whose own name holds a dot keeps
  it, since the whole name is looked up first, but an entry that is both such an extractor and a
  field of a record extractor does not load, since the field could then not be named at all.
- **normalize** applies to every scalar of a record, and to no key, as for a map.
- **allow_empty** names the extractor. It decides an extractor that found no record, and a field
  that none of its records holds: under `assert` every field the config names, under `agree` the
  field `agree` names. The config names each field, so one that no record holds is a path that
  misses or a key the file renamed, and `!has(r.f) || ...` would hold on every record. A per field
  opt out was the other way; `allow_empty` already says "this may come back empty" for the
  extractor, and a second key would say it again for each field.
- **skip_not_literal.** The yaml, json, toml and regex readers never mark a value not literal, so a
  record never holds one, and the key stays refused for such an extractor.
- **by_file and file sets.** Over `files:`, each record is at its own file, and `by_file(x)` maps
  each file to its records (ADR 0008).
- **Embedded documents.** A path record reads its fields in the document the last step selects,
  and its lines are those of the outer file.
- **Two extractors over one place** compare their `as` and `fields` as part of the place, so a
  record extractor and a flat one over the same path are two places, as `as: map` and flat are.

## Bounds

ADR 0003's bounds hold, counted over what the records hold. Each record and each node of its
fields counts as one of the extractor's 100000 values, and the bytes of the field names, keys and
scalars count against `max_bytes`, in one file and over a set together. A regex search keeps its
time limit and its cap of 32 groups. A field's path runs under the same filter budget as the
extractor's path, so a filter in a field costs what it costs anywhere. A record's text, which
messages print, is cut at 256 bytes as a map's is.

## Considered options

- **Records from every pattern with named groups.** A regex extractor returns group 1 today, and a
  pattern that names its groups would change what it returns under rules already written. `as:
  record` asks for it.
- **A list of group names on a regex, as `fields`.** The names are already in the pattern, and a
  second list could disagree with it.
- **Every field present, an absent one null.** A null scalar is the string `'null'` in a map, so a
  missing field and a field the file sets to null would be one value. Leaving it out keeps them
  apart, and `has()` already tests a key.
- **A field selecting several nodes as a list.** The field's type would then hang on how many
  nodes one element holds, so a rule could load and break on the next element. One value, or
  unknown, keeps a field one type. A field whose path selects a sequence whole is a list.
- **A record type of its own in CEL**, with declared fields. It would check field names at load,
  but a rule over maps would not read records, and ADR 0009 kept one type for both.
- **agree over a field as a mapping**, `{extractor: x, field: f}`. Every `agree` today is a list
  of names; `x.f` keeps it one, and the lookup of the whole name first keeps every name that loads
  today meaning what it meant. No record extractor existed before v0.2.2, so refusing an entry that
  could mean either refuses no config that loaded before.

## Consequences

`as: record` and `fields` are part of the config's contract from v0.2.2. An entry of `agree` with
a dot can now name a field, and an entry that named no extractor still does not load. The
landingpage and Gatus checks of the GitOps repository read Gatus's config as records instead of
splitting it with regular expressions.
