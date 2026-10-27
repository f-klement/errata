# Filters test membership with RFC 9535's existence test

A rule often needs the member of a list whose own list holds a value: the Renovate rule whose
`matchDatasources` holds `docker`, the egress rule whose `ports` hold 6443. errata's path language
is a subset of RFC 9535 JSONPath, and RFC 9535 says this with two constructs it already has. A path
from `@` with no comparison after it is a test that the path selects a node, and that path may hold
a filter of its own. So `[?@.tags[?@ == 'docker']]` keeps the members whose `tags` hold `docker`,
and `[?!@.key]` keeps those without `key`. errata takes both, with `[*]` in such a path, and adds no
operator of its own. Every path errata reads that RFC 9535 also parses means the same in any RFC
9535 implementation, which the differential test against github.com/theory/jsonpath checks. RFC
9535 does not parse two of errata's paths: one that leaves out the leading `$`, and one with a key
after a dot that holds `-`. These are errata's two extensions.

## Considered options

- **An `in` operator, `'docker' in @.tags`**, as Jayway JsonPath has it. (JSONPath Plus runs a
  filter as JavaScript, where `in` tests a property name, and spells membership
  `@.tags.includes('docker')`.) It reads well, but it is not RFC 9535, so a path would mean one
  thing to errata and fail to parse in other tools. It also needs its own rules for a mapping and for a missing list, which the
  existence test already has.
- **A `contains()` function.** RFC 9535 functions are typed, and the five it defines do not test
  membership. A function of errata's own is the same departure as `in`, behind a different
  spelling.
- **Leave membership to CEL**, extracting every list whole. A value is a scalar, so a list of
  objects reaches CEL only as parallel lists of their fields, and which `docker` belongs to which
  rule is lost.

## Consequences

The bounds of ADR 0003 apply to nested filters. Filters nest at most 4 deep, since parsing a filter
and testing a member recurse once for each level. The work of a test that a path exists depends on
the file, since `[*]` and an inner filter fan out, so it is charged step by step as it runs,
against the same 4000000000 byte budget as the filter around it, where a comparison's cost is
known before any member is tested. `||`, parentheses, functions and comparisons other than `==`
stay refused. RFC 9535 has each, so adding one later keeps the language a subset.
