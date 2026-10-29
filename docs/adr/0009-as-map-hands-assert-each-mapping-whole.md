# as: map hands assert each mapping whole

Some claims are about a list of objects read in order. Renovate applies its `packageRules` one
after another and a later rule's `automerge` overrides an earlier one's, so whether a chart
automerges is the verdict of the last rule that names it and sets the key. A path selects scalars,
and a rule over `packageRules[*].automerge` and `packageRules[*].matchPackageNames[*]` gets two
flat lists that no longer say which name belongs to which rule. ADR 0004's filters keep the
members whose own list holds a value, which answers "is there such a rule", not "which rule
decides". So a `yaml`, `json` or `toml` extractor takes `as: map`, and hands assert each mapping
its path selects whole:

```yaml
extract:
  rules: {json: {file: renovate.json, path: "packageRules[*]", as: map}}
assert: |
  rules.exists(b, has(b.prPriority) && b.prPriority == '5' && has(b.automerge) && b.automerge == 'false' && !has(b.matchUpdateTypes) && has(b.matchPackageNames) && size(b.matchPackageNames) > 0) &&
  rules.filter(b, has(b.prPriority) && b.prPriority == '5' && has(b.automerge) && b.automerge == 'false' && !has(b.matchUpdateTypes) && has(b.matchPackageNames)).all(b,
    b.matchPackageNames.all(n,
      rules.map(r, has(r.automerge) && has(r.matchPackageNames) && n in r.matchPackageNames, r.automerge)[
        size(rules.filter(r, has(r.automerge) && has(r.matchPackageNames) && n in r.matchPackageNames)) - 1] != 'true'))
```

The `exists` holds the rule to the never-automerge rule itself, since `all` over no member holds.

## The rules

- `as` is `map` or left out. Any other word does not load, so a later form, such as `as: list`
  for sequences whole, can be added without changing what a config of today means. With `as:
  map`, every node the path selects, or the last `embedded` step selects, must be a mapping; a
  scalar or a sequence makes the rule unknown, naming its line, as a mapping does for a flat value.
- In `assert`, such an extractor is a `list(map(string, dyn))`, one map for each mapping, in file
  order. A value in a map is a map again for a nested mapping, a `list` for a sequence, and a
  `string` for a scalar: the text a flat value would have (README, Values), so `automerge: false`
  is `'false'`, `prPriority: 5` is `'5'`, and `int()` converts it. JSON `false` and `"false"` are
  one value, and so are `null` and `"null"`, `'null'`. A key set to null is set, so `has()` is true
  for it. A key is its text, so YAML's `1` and `0x1` are two keys. A map's keys come in the order
  the file wrote them, and the keys a YAML merge key adds after the mapping's own, wherever `<<`
  stands, so a comprehension over it, and the error it reports, is the same on every run.
  `has(m.key)` tests a key, and reading a key the map lacks is an error, which makes the rule
  unknown (ADR 0001).
- A value of a map is `dyn` to the checker, which accepts it beside any type, and CEL's equality
  makes a string unequal to a bool or a number without an error. `r.automerge != true` would then
  hold on every input. So `==`, `!=` and `in` between a value the checker types `dyn`, or a map or
  list of such values, and an operand whose checked type is bool, int, uint, double, null, bytes,
  duration or timestamp, or a list or map with members, keys or values of such a type, do not load.
  The pass over the checked expression goes by type, not spelling, so `size(l)`, `int('5')`, a
  ternary and `v.map(x, int(x))` are refused as a literal is, and it looks at each element, key and
  value of a list or map literal, so an empty one loads. It follows a `cel.bind` name to the value
  it binds, and a comprehension variable to the list literal it runs over, and looks into those
  literals the same way: a literal that mixes types is a `map(string, dyn)`, which no type check
  refuses. Any other operand is checked by its type alone. It refuses at the operand, or the
  member, and says to write the string, such as `'true'`, or to convert with `int()`. A value the
  expression writes as `dyn()` itself is left alone, since its author chose the type. The pass
  cannot tell a map's `dyn` from another, such as a member of `[1, 'a']`, so it refuses that too,
  in any rule, and the message names `dyn()` as the way out. It visits each node at most twice,
  once for `==` and once for `in`, so its visits stay linear in the expression however often a
  followed name is read. Its work is not: each read of a comprehension variable follows it to every
  element of its range, so the work grows as the reads times the range's length, about 0.1 s at the
  4096 byte limit on an expression.
- Each map and each node under it keeps its file and line in errata, through the lines of a
  document in a string as ADR 0007 gives them. The message of a false or unknown assert shows each
  map at its own file and line, shown close to JSON and cut at `check.ShownBytes`, 256 bytes, so
  an erratum names every member the rule read, the one that decided among them; a report points
  at each and names it the same way. CEL reads no location.
- `by_file(x)` over such an extractor is a `map(string, list(map(string, dyn)))`, from each file
  of a `files:` set to its maps (ADR 0008). Each file is read as one, and the maps keep their
  files.
- `agree` compares flat values and does not load with an extractor that has `as: map`.
  `normalize` applies to every scalar of a map, as to a flat value, and to no key, since the
  expression names the keys. `allow_empty` decides an extractor that found no mapping, as for any
  other.

## Bounds

ADR 0003's bounds hold, counted over what the maps hold. Every node of every map counts as one
of the extractor's 100000 values, the map itself included, and the bytes of its keys and scalars
count against `max_bytes`, in one file and over the files of a set together. YAML aliases can
select one mapping many times without the file growing, so each selection counts again, and the
copy stops at the first node past a bound. A value's text, which messages and reports print, is
the mapping shown and cut at the same 256 bytes, so a large mapping does not double the memory it
takes.

In CEL, a map costs what CEL charges for reading it: each key read and each member of a
comprehension. The maps are built once for each evaluation, in time linear in their nodes:
100000 nodes, the most an extractor returns, are handed over and read by index in well under a
second. A comprehension inside another over a long list reaches the cost limit, which makes the
rule unknown. cel-go's cost tracker makes a comprehension take time quadratic in the members it
runs over, a list's or a map's keys, whatever the members are: an `all` over 16000 maps takes
1.5 s, as one over 16000 strings does, and one over 50000 maps, or over the keys of one mapping of
99999 keys, meets the 5 second timeout and is unknown, so a rule over a list that long narrows its
path first.

## A record is a map with a fixed set of keys

A later kind of value, several paths of one element kept together, is the same type: a
`check.Data` mapping whose keys are the names the rule gives its paths, each value read by its
own path. assert reads it as a map, by_file groups it, and the bounds and messages above apply
unchanged. So a rule written over maps reads records without a second type to learn. ADR 0013
gives the rules of records.

## Considered options

- **Every value a map, `{text, file, line}`.** ADR 0008 refused it: every rule written so far
  would stop loading.
- **Typed scalars**, a number as an `int` or `double` and `true` as a `bool`. CEL would compare
  `r.automerge == false` directly. But a flat value is the text the file wrote, and the same file
  read with and without `as: map` would give `'5'` in one rule and `5` in the other. YAML 1.1 and
  1.2 type `yes` and `0o17` differently, TOML's dates have no CEL type, and a JSON number of
  thirty digits has no exact `int` or `double`. errata's helpers already convert a string, so the
  scalar stays one.
- **A macro, `maps(x)`, beside a flat extractor**, as `by_file` is. The extractor would then have
  to keep both forms of every value, and its path would have to select the mappings anyway.
- **The location of a field in CEL**, such as `line(m)`. A value of a map is a CEL string, which
  carries no location, and a function of a map would let an expression depend on the layout of a
  file rather than on its content. The message prints the location instead.
- **Maps built as CEL reads them**, member by member. It would avoid building maps an expression
  never reads, but would rebuild a sequence each time a key is read; a whole build is linear and
  bounded by the 100000 nodes.

## Consequences

`as` is part of the config's contract from v0.2.2, and its one value, `map`, the type above. A
rule that reads a list of objects in order states its own fold in CEL, which has no index
variable in a comprehension: the last write is the last of the matching values, `l[size(l)-1]`.
assert declares cel-go's bindings library, version 0, the `cel.bind(name, value, expr)` macro
alone, so a rule binds the matching values once instead of writing the filter twice. The macro
is a comprehension over no member, which the cost limit sees as any other; the refusal beside
a map's value follows its name to the value it binds. A message over a long list of maps prints
every map, so it is as long as a flat rule's over the same members.
