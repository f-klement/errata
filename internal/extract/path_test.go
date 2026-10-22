package extract

import (
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func name(s string) selector { return selector{kind: selName, name: s} }
func index(n int) selector   { return selector{kind: selIndex, index: n} }

var wild = selector{kind: selWild}

func TestParsePath(t *testing.T) {
	for in, want := range map[string][]selector{
		"$":                  nil,
		"a":                  {name("a")},
		"$.a":                {name("a")},
		".a":                 {name("a")},
		"spec.replicas":      {name("spec"), name("replicas")},
		"$.spec.max-pods":    {name("spec"), name("max-pods")},
		"agent.node-label":   {name("agent"), name("node-label")},
		"a._x9":              {name("a"), name("_x9")},
		"['0b']":             {name("0b")},
		`['say "hi"']`:       {name(`say "hi"`)},
		`["it's"]`:           {name("it's")},
		`['a\nb\t\r\b\f\/']`: {name("a\nb\t\r\b\f/")},
		`["\u00e9\u00E9"]`:   {name("éé")},
		`["\ud83d\ude00"]`:   {name("😀")},
		"['\x7f\ufffd']":     {name("\x7f\ufffd")},
		"métadonnées.clé":    {name("métadonnées"), name("clé")},
		"$['a.b']":           {name("a.b")},
		`$["a b"]`:           {name("a b")},
		`['it\'s']`:          {name("it's")},
		`["say \"hi\""]`:     {name(`say "hi"`)},
		`['back\\slash']`:    {name(`back\slash`)},
		"['']":               {name("")},
		"[0]":                {index(0)},
		"a[12]":              {name("a"), index(12)},
		"a[-1]":              {name("a"), index(-1)},
		"a[*].b":             {name("a"), wild, name("b")},
		"$[*][*]":            {wild, wild},
		"metadata.labels['app.kubernetes.io/name']": {name("metadata"), name("labels"), name("app.kubernetes.io/name")},
	} {
		got, err := parsePath(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if !reflect.DeepEqual(got.sels, want) {
			t.Errorf("%s: got %+v, want %+v", in, got.sels, want)
		}
	}
}

// Each path is a planted break of the grammar; the refusal names the problem
// and the column where it starts. The rows from a.0b on are what an earlier
// grammar accepted and this one refuses.
func TestParsePathRefuses(t *testing.T) {
	for in, want := range map[string]string{
		"":                        `path "": column 1: path is empty`,
		"a.":                      `path "a.": column 3: expected a key name but the path ends`,
		"a..b":                    `path "a..b": column 3: recursive descent (..) is not supported`,
		"a.*":                     `path "a.*": column 3: write [*] for every member, not .*`,
		"a.b c":                   `path "a.b c": column 4: expected . or [ but found ' '`,
		"a.b/c":                   `path "a.b/c": column 4: expected . or [ but found '/'`,
		"a.bé/":                   `path "a.bé/": column 5: expected . or [ but found '/'`,
		"a./":                     `path "a./": column 3: expected a key name but found '/'; write ['...'] for a key with that character`,
		"/a":                      `path "/a": column 1: expected a key name but found '/'`,
		"$a":                      `path "$a": column 2: expected . or [ but found 'a'`,
		"a[":                      `path "a[": column 3: expected a quoted key, an index or * after [ but the path ends`,
		"a[b]":                    `path "a[b]": column 3: expected a quoted key, an index or * after [ but found 'b'`,
		"a[1:2]":                  `path "a[1:2]": column 4: slices are not supported`,
		"a[1":                     `path "a[1": column 4: expected ] but the path ends`,
		"a[*x":                    `path "a[*x": column 4: expected ] but found 'x'`,
		"a['b'":                   `path "a['b'": column 6: expected ] but the path ends`,
		"a['b":                    `path "a['b": column 3: unterminated quoted key`,
		`a['b\`:                   `path "a['b\\": column 3: unterminated quoted key`,
		`a['\q']`:                 `path "a['\\q']": column 4: unsupported escape \q in a single-quoted key`,
		"a[-]":                    `path "a[-]": column 3: expected digits after -`,
		"a[01]":                   `path "a[01]": column 3: an index has no leading zero`,
		"a[-0]":                   `path "a[-0]": column 3: -0 is not an index; write 0`,
		"a[99999999999999999999]": `path "a[99999999999999999999]": column 3: index 99999999999999999999 is out of range`,
		"é.":                      `path "é.": column 3: expected a key name but the path ends`,
		`["\u12"]`:                `path "[\"\\u12\"]": column 3: \u takes four hex digits`,
		`["\u12g4"]`:              `path "[\"\\u12g4\"]": column 3: \u takes four hex digits`,
		`["\udc00"]`:              `path "[\"\\udc00\"]": column 3: \uDC00 is a low surrogate without a high one`,
		`["\ud800"]`:              `path "[\"\\ud800\"]": column 3: \uD800 is a high surrogate without a low one after it`,
		`["\ud800\u0041"]`:        `path "[\"\\ud800\\u0041\"]": column 3: \uD800 is a high surrogate without a low one after it`,
		"a.0b":                    `path "a.0b": column 3: a key after . cannot start with a digit; write [n] for an index, or ['0b'] for a key`,
		"spec.containers.0.image": `path "spec.containers.0.image": column 17: a key after . cannot start with a digit; write [n] for an index, or ['0'] for a key`,
		"0":                       `path "0": column 1: a key cannot start with a digit; write [n] for an index, or ['0'] for a key`,
		"200.x":                   `path "200.x": column 1: a key cannot start with a digit; write [n] for an index, or ['200'] for a key`,
		"-x":                      `path "-x": column 1: a key cannot start with -; write ['-x']`,
		"*":                       `path "*": column 1: write [*] for every member, not *`,
		"$a.b":                    `path "$a.b": column 2: expected . or [ but found 'a'`,
		"a.-x":                    `path "a.-x": column 3: a key after . cannot start with -; write ['-x']`,
		`['say \"hi\"']`:          `path "['say \\\"hi\\\"']": column 7: unsupported escape \" in a single-quoted key`,
		`["it\'s"]`:               `path "[\"it\\'s\"]": column 5: unsupported escape \' in a double-quoted key`,
		"a\tb":                    `path "a\tb": column 2: control character U+0009 is not allowed in a path`,
		"['a\nb']":                `path "['a\nb']": column 4: control character U+000A is not allowed in a path`,
		"['a\x00']":               `path "['a\x00']": column 4: control character U+0000 is not allowed in a path`,
		"a.\xff":                  `path "a.\xff": column 3: the path is not valid UTF-8`,
		"['\xffa']":               `path "['\xffa']": column 3: the path is not valid UTF-8`,
	} {
		_, err := parsePath(in)
		if err == nil {
			t.Errorf("%q parsed, want %s", in, want)
			continue
		}
		if !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: got %q, want it to start with %q", in, err, want)
		}
	}
}

func TestBuildRefusesPathThatDoesNotParse(t *testing.T) {
	for kind, s := range map[string]Spec{
		"yaml": {YAML: &YAML{File: "f", Path: "a..b"}},
		"json": {JSON: &JSON{File: "f", Path: "a..b"}},
		"toml": {TOML: &TOML{File: "f", Path: "a..b"}},
	} {
		_, err := Build(s)
		wantError(t, err, kind+`: path "a..b": column 3: recursive descent`)
	}
}

func scalarNode(s string) *node { return &node{text: s, line: 1} }

func TestEval(t *testing.T) {
	inner := newMapping(1)
	inner.set("x", scalarNode("1"))
	inner.set("y", scalarNode("2"))
	list := &node{shape: sequence, items: []*node{scalarNode("a"), scalarNode("b"), inner}}
	root := newMapping(1)
	root.set("list", list)
	root.set("m", inner)
	root.set("s", scalarNode("v"))
	for in, want := range map[string][]string{
		"s":          {"v"},
		"list[0]":    {"a"},
		"list[-1].x": {"1"},
		"list[-3]":   {"a"},
		"list[3]":    nil,
		"list[-4]":   nil,
		"list[*].x":  {"1"},
		"m[*]":       {"1", "2"},
		"absent":     nil,
		"s.x":        nil, // a key on a scalar
		"s[0]":       nil, // an index on a scalar
		"m[0]":       nil, // an index on a mapping
		"list.x":     nil, // a key on a sequence
		"s[*]":       nil,
	} {
		p, err := parsePath(in)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		nodes, err := p.eval(root)
		if err != nil {
			t.Fatalf("%s: over the limit", in)
		}
		for _, n := range nodes {
			got = append(got, n.text)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

// filter is a filter selector of comparisons.
func filter(cs ...comparison) selector {
	sel := selector{kind: selFilter, filter: cs}
	for _, c := range cs {
		sel.cost += c.cost()
	}
	return sel
}

// eq is the comparison of the path from @ with lit.
func eq(lit literal, current ...selector) comparison {
	return comparison{current: current, lit: lit}
}

func str(s string) literal { return literal{typ: typeString, str: s} }

func num(s string) literal {
	d, ok := parseDecimal(s)
	if !ok {
		panic(s)
	}
	return literal{typ: typeNumber, num: d}
}

var (
	litTrue  = literal{typ: typeBool, b: true}
	litFalse = literal{typ: typeBool}
	litNull  = literal{typ: typeNull}
)

func TestParseFilter(t *testing.T) {
	for in, want := range map[string][]selector{
		"a[?@.key == 'value']":                    {name("a"), filter(eq(str("value"), name("key")))},
		`a[?@.key == "value"]`:                    {name("a"), filter(eq(str("value"), name("key")))},
		"a[?@.a.b == 'v']":                        {name("a"), filter(eq(str("v"), name("a"), name("b")))},
		"a[?@['k.x'] == 'v']":                     {name("a"), filter(eq(str("v"), name("k.x")))},
		"a[?@.args[0] == 'x']":                    {name("a"), filter(eq(str("x"), name("args"), index(0)))},
		"a[?@[-1] == 'x']":                        {name("a"), filter(eq(str("x"), index(-1)))},
		"a[?@ == 80]":                             {name("a"), filter(eq(num("80")))},
		"a[?'api' == @.name]":                     {name("a"), filter(eq(str("api"), name("name")))},
		"a[?@.name-x == 'v']":                     {name("a"), filter(eq(str("v"), name("name-x")))},
		"a[?@.a == 'x' && @.b == 1]":              {name("a"), filter(eq(str("x"), name("a")), eq(num("1"), name("b")))},
		"a[?@.a=='x'&&@.b==1]":                    {name("a"), filter(eq(str("x"), name("a")), eq(num("1"), name("b")))},
		"a[?  @.a  ==  'x'  ]":                    {name("a"), filter(eq(str("x"), name("a")))},
		"a[?@.n == -1.5e+3]":                      {name("a"), filter(eq(num("-1500"), name("n")))},
		"a[?@.n == -0]":                           {name("a"), filter(eq(num("0"), name("n")))},
		"a[?@.n == 0.0E-2]":                       {name("a"), filter(eq(num("0"), name("n")))},
		"a[?@.b == true]":                         {name("a"), filter(eq(litTrue, name("b")))},
		"a[?@.b == false]":                        {name("a"), filter(eq(litFalse, name("b")))},
		"a[?@.b == null]":                         {name("a"), filter(eq(litNull, name("b")))},
		"a[?null == @.b]":                         {name("a"), filter(eq(litNull, name("b")))},
		`a[?@.s == 'it\'s é']`:                    {name("a"), filter(eq(str("it's é"), name("s")))},
		"a[?@.s == '']":                           {name("a"), filter(eq(str(""), name("s")))},
		"a[?@.s == ']'].b":                        {name("a"), filter(eq(str("]"), name("s"))), name("b")},
		"$[?@.x == 1][?@.y == 2]":                 {filter(eq(num("1"), name("x"))), filter(eq(num("2"), name("y")))},
		"a[?@.n == 1e999999999999]":               {name("a"), filter(eq(num("1e999999999999"), name("n")))},
		"a[?@.n == 1e-1000000000000]":             {name("a"), filter(eq(num("1e-1000000000000"), name("n")))},
		"a[?@.n == 1e1000000000000]":              {name("a"), filter(eq(num("1e1000000000000"), name("n")))},
		"a[?@.n == 0.1e-999999999999]":            {name("a"), filter(eq(num("1e-1000000000000"), name("n")))},
		"a[?@ .b == 1]":                           {name("a"), filter(eq(num("1"), name("b")))},
		"a[?@.a ['b'] [0] == 1]":                  {name("a"), filter(eq(num("1"), name("a"), name("b"), index(0)))},
		"a[ ?@.b == 1 ]":                          {name("a"), filter(eq(num("1"), name("b")))},
		"a[ 0 ]":                                  {name("a"), index(0)},
		"a[ 'k' ][ * ]":                           {name("a"), name("k"), wild},
		"$.a[?@.b == 'kilo']":                     {name("a"), filter(eq(str("kilo"), name("b")))},
		"spec.containers[?@.name == 'api'].image": {name("spec"), name("containers"), filter(eq(str("api"), name("name"))), name("image")},
	} {
		got, err := parsePath(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if !reflect.DeepEqual(got.sels, want) {
			t.Errorf("%s: got %+v, want %+v", in, got.sels, want)
		}
	}
}

// Each path is a planted break of the filter grammar: a construct of RFC
// 9535's filters outside the equality subset, or a malformed literal. The
// refusal names the construct and the column where it starts.
func TestParseFilterRefuses(t *testing.T) {
	for in, want := range map[string]string{
		"a[?@.b]":                      `path "a[?@.b]": column 7: a test that a path exists is not supported in a filter; compare it with ==`,
		"a[?@.b == 1 && @.c]":          `column 19: a test that a path exists is not supported in a filter`,
		"a[?@.b && @.c == 1]":          `column 8: a test that a path exists is not supported in a filter`,
		"a[?@.b || @.c == 1]":          `column 8: a test that a path exists is not supported in a filter`,
		"a[?@.b == False]":             `column 11: write false in lower case`,
		"a[?@.b == true1]":             `column 11: expected @ or a literal in the filter but found true1; quote a string`,
		"a[?@.b":                       `column 7: a test that a path exists is not supported in a filter`,
		"a[?@.b == 'x' || @.c == 'y']": `column 15: || is not supported in a filter; only && joins comparisons`,
		"a[?!@.b]":                     `column 4: ! is not supported in a filter`,
		"a[?(@.b == 'kilo')]":          `column 4: parentheses are not supported in a filter; join comparisons with &&`,
		"a[?@.b != 'x']":               `column 8: operator != is not supported in a filter; only == is`,
		"a[?@.b < 1]":                  `column 8: operator < is not supported in a filter; only == is`,
		"a[?@.b <= 1]":                 `column 8: operator <= is not supported in a filter; only == is`,
		"a[?@.b > 1]":                  `column 8: operator > is not supported in a filter; only == is`,
		"a[?@.b >= 1]":                 `column 8: operator >= is not supported in a filter; only == is`,
		"a[?@.b = 1]":                  `column 8: write == to compare, not =`,
		"a[?@.b 'x']":                  `column 8: expected == but found '\''`,
		"a[?'x']":                      `column 7: expected == but found ']'`,
		"a[?'x'":                       `column 7: expected == but the path ends`,
		"a[?@.b == $.x]":               `column 11: $ is not supported in a filter; a comparison starts its path from the member, @`,
		"a[?$.x == 1]":                 `column 4: $ is not supported in a filter`,
		"a[?length(@.b) == 1]":         `column 4: function length() is not supported in a filter`,
		"a[?@.b == match(@.c)]":        `column 11: function match() is not supported in a filter`,
		"a[?@.b == @.c]":               `column 11: a filter compares a path from @ with a literal, not with another path`,
		"a[?@ == @]":                   `column 9: a filter compares a path from @ with a literal, not with another path`,
		"a[?1 == 1]":                   `column 9: a filter compares a path from @ with a literal, not two literals`,
		"a[?@[*] == 1]":                `column 6: [*] in a filter selects many nodes, and a comparison takes one; after @ write only .key, ['key'] or [n]`,
		"a[?@.b[?@.c == 1] == 1]":      `column 8: a filter inside a filter is not supported`,
		"a[?@..b == 1]":                `column 6: recursive descent (..) is not supported`,
		"a[?@.* == 1]":                 `column 6: write [*] for every member, not .*`,
		"a[?@.b[1:2] == 1]":            `column 9: slices are not supported`,
		"a[?@.b == True]":              `column 11: write true in lower case`,
		"a[?@.b == NULL]":              `column 11: write null in lower case`,
		"a[?@.b == api]":               `column 11: expected @ or a literal in the filter but found api; quote a string`,
		"a[?@.b == +1]":                `column 11: expected @ or a literal in the filter but found '+'`,
		"a[?@.b == .5]":                `column 11: expected @ or a literal in the filter but found '.'`,
		"a[?@.b == 01]":                `column 11: a number has no leading zero`,
		"a[?@.b == -01]":               `column 11: a number has no leading zero`,
		"a[?@.b == -]":                 `column 11: expected digits after -`,
		"a[?@.b == 1.]":                `column 13: expected digits after the . of a number`,
		"a[?@.b == 1.e3]":              `column 13: expected digits after the . of a number`,
		"a[?@.b == 1e]":                `column 13: expected digits in the exponent of a number`,
		"a[?@.b == 1e+]":               `column 14: expected digits in the exponent of a number`,
		"a[?@.b == 'x":                 `column 11: unterminated quoted string`,
		`a[?@.b == '\q']`:              `column 12: unsupported escape \q in a single-quoted string`,
		`a[?@.b == "\'"]`:              `column 12: unsupported escape \' in a double-quoted string`,
		"a[?@.b == 'x'":                `column 14: expected ] but the path ends`,
		"a[?@.b == 'x' @.c]":           `column 15: expected && or ] after a comparison but found '@'`,
		"a[?@.b == 1, ?@.c == 2]":      `column 12: expected && or ] after a comparison but found ','`,
		"a[?@.b == 1 &&]":              `column 15: expected @ or a literal in the filter but found ']'`,
		"a[?]":                         `column 4: expected @ or a literal in the filter but found ']'`,
		"a[?":                          `column 4: expected @ or a literal in the filter but the path ends`,
		"a[?@.b ==":                    `column 10: expected @ or a literal in the filter but the path ends`,
		"a[?@.b == 1]x":                `column 13: expected . or [ but found 'x'`,
		"a[?@.b == 1 && @.c == 'x'":    `column 26: expected ] but the path ends`,
		"containers[?@.name != 'api']": `path "containers[?@.name != 'api']": column 20: operator != is not supported in a filter; only == is`,
	} {
		_, err := parsePath(in)
		if err == nil {
			t.Errorf("%q parsed, want %s", in, want)
			continue
		}
		prefix := `path ` + strconv.Quote(in) + `: `
		if !strings.HasPrefix(err.Error(), prefix) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %q, want %q", in, err, want)
		}
	}
}

func TestParseDecimal(t *testing.T) {
	for in, want := range map[string]decimal{
		"0":       {},
		"-0":      {},
		"0.000":   {},
		"0e99":    {},
		"1":       {digits: "1", exp: "1"},
		"10":      {digits: "1", exp: "2"},
		"1000":    {digits: "1", exp: "4"},
		"1e3":     {digits: "1", exp: "4"},
		"1E+3":    {digits: "1", exp: "4"},
		"1e003":   {digits: "1", exp: "4"},
		"10e2":    {digits: "1", exp: "4"},
		"1000.00": {digits: "1", exp: "4"},
		"0.1":     {digits: "1", exp: "0"},
		"1e-1":    {digits: "1", exp: "0"},
		".1":      {digits: "1", exp: "0"},
		"0.001":   {digits: "1", exp: "-2"},
		"123.45":  {digits: "12345", exp: "3"},
		"-1.5":    {neg: true, digits: "15", exp: "1"},
		"+1.5":    {digits: "15", exp: "1"},
		"007.5":   {digits: "75", exp: "1"},
		"1.":      {digits: "1", exp: "1"},
		// A zero is zero whatever its exponent, and any other exponent is
		// kept exactly, however long.
		"0e1000000000000000":            {},
		"-0.0E-99999999999999999999":    {},
		"1e999999999999999":             {digits: "1", exp: "1000000000000000"},
		"1e999999999999999999":          {digits: "1", exp: "1000000000000000000"},
		"10e999999999999999999":         {digits: "1", exp: "1000000000000000001"},
		"0.001e1000000000000000000":     {digits: "1", exp: "999999999999999998"},
		"0.001e1000000000000000002":     {digits: "1", exp: "1000000000000000000"},
		"1e-1000000000000000000":        {digits: "1", exp: "-999999999999999999"},
		"0.01e-999999999999999999":      {digits: "1", exp: "-1000000000000000000"},
		"99e9999999999999999999999999":  {digits: "99", exp: "10000000000000000000000001"},
		"-5e-9999999999999999999999999": {neg: true, digits: "5", exp: "-9999999999999999999999998"},
	} {
		got, ok := parseDecimal(in)
		if !ok || got != want {
			t.Errorf("%s: got %+v %v, want %+v", in, got, ok, want)
		}
	}
	// The planted breaks: text that is no decimal.
	for _, in := range []string{"", "-", ".", "e3", "1e", "1e+", "1x", "1.2.3", "inf", "nan", "0x1F", "1_000"} {
		if got, ok := parseDecimal(in); ok {
			t.Errorf("%q: got %+v, want no decimal", in, got)
		}
	}
}

// A number scalar's value, read as its format reads it.
func TestNumberOf(t *testing.T) {
	for in, want := range map[string]string{
		"8080":                    "8080",
		"8_080":                   "8080", // TOML and YAML separators
		"0x1F90":                  "8080", // TOML and YAML hexadecimal
		"0o17":                    "15",   // TOML and YAML octal
		"017":                     "15",   // YAML 1.1 octal, as yaml.v3 reads it
		"0b101":                   "5",    // TOML and YAML binary
		"-0x1F":                   "-31",  // YAML only
		"+12":                     "12",   // TOML and YAML
		"8.08e3":                  "8080", // every format
		"8080.0":                  "8080",
		"-0":                      "0",
		"18446744073709551615":    "18446744073709551615",
		"18446744073709551616":    "18446744073709551616",
		"9223372036854775807":     "9223372036854775807",
		"-9223372036854775808":    "-9223372036854775808",
		"12345678901234567890123": "12345678901234567890123",
		"0b+101":                  "5", // yaml.v3 parses what follows 0b with its sign
		"0b-101":                  "-5",
		"0o+17":                   "15",
		"0o-17":                   "-15",
		"0xFFFFFFFFFFFFFFFF":      "18446744073709551615", // YAML: above int64, through ParseUint
	} {
		got, ok := numberOf(in)
		w, _ := parseDecimal(want)
		if !ok || got != w {
			t.Errorf("%s: got %+v %v, want %s", in, got, ok, want)
		}
	}
	for _, in := range []string{"inf", "+inf", "-nan", ".inf", "-.Inf", ".NaN", "-0o-17", "0b2"} {
		if got, ok := numberOf(in); ok {
			t.Errorf("%s: got %+v, want no value", in, got)
		}
	}
}

// RFC 9535's examples that fall in the subset, through the JSON reader: the
// member value comparison of Table 12, the comparisons with null of Table
// 17, and Table 11's type mismatches as filters. A filter selects a
// container here, so each path takes a scalar after it.
func TestFilterRFC9535Examples(t *testing.T) {
	root := repo(t, map[string]string{
		"t12.json": `{
  "a": [3, 5, 1, 2, 4, 6,
        {"b": "j"},
        {"b": "k"},
        {"b": {}},
        {"b": "kilo"}
       ],
  "o": {"p": 1, "q": 2, "r": 3, "s": 5, "t": {"u": 6}},
  "e": "f"
}`,
		"t17.json": `{"a": null, "b": [null], "c": [{}], "null": 1}`,
		"t11.json": `{"obj": {"x": "y"}, "arr": [2, 3]}`,
	})
	for _, c := range []struct{ file, path, want string }{
		{"t12.json", "$.a[?@.b == 'kilo'].b", "kilo@6"},
		{"t12.json", "$.a[?@.b == 'k'].b", "k@4"},
		{"t12.json", "$.a[?@.b == ''].b", ""}, // {"b": {}} holds a mapping, which equals no string
		{"t12.json", "$.a[?@ == 4]", "4@2"},
		{"t12.json", "$.o[?@ == 5]", "5@8"},
		{"t12.json", "$.o[?@.u == 6].u", "6@8"},
		{"t12.json", "$[?@ == 'f']", "f@9"},
		{"t17.json", "$.b[?@==null]", "null@1"},
		{"t17.json", "$.c[?@.d==null]", ""},
		{"t17.json", "$[?@ == 1]", "1@1"},
		{"t17.json", "$[?@ == null]", "null@1"},
		{"t11.json", "$.arr[?@ == '2']", ""}, // 13 == '13' is false
		{"t11.json", "$.arr[?@ == 2]", "2@1"},
		{"t11.json", "$[?@ == 17]", ""},         // $.obj == 17 is false
		{"t11.json", "$[?@.absent == 'g']", ""}, // $.absent == 'g' is false
		{"t11.json", "$[?@.x == 'y'].x", "y@1"},
	} {
		got := texts(mustRun(t, Spec{JSON: &JSON{File: c.file, Path: c.path}}, root))
		if got != c.want {
			t.Errorf("%s %s: got %q, want %q", c.file, c.path, got, c.want)
		}
	}
}

// A filter compares by the type the file's format gives a scalar. The
// planted break each row guards: a string that spells a number, a number
// spelled another way, a boolean that is a string, and a missing key that is
// not null.
func TestFilterComparesTypedValues(t *testing.T) {
	root := repo(t, map[string]string{
		"f.yaml": `ports:
  - {name: plain, port: 8080}
  - {name: quoted, port: "8080"}
  - {name: str, port: !!str 8080}
  - {name: hex, port: 0x1F90}
  - {name: float, port: 8080.0}
  - {name: exp, port: 8.08e3}
  - {name: under, port: 8_080}
  - {name: other, port: 8081}
flags:
  - {enabled: true, id: a}
  - {enabled: True, id: b}
  - {enabled: "true", id: c}
  - {enabled: ~, id: d}
  - {enabled: null, id: e}
  - {enabled: , id: f}
  - {id: g}
  - {enabled: false, id: h}
dates:
  - {d: 2026-10-17, id: a}
  - {d: "2026-10-17", id: b}
  - {d: 2026-10-17T00:00:00Z, id: c}
refs:
  - {v: !Ref api, id: a}
  - {v: !!binary aGk=, id: b}
`,
		"f.toml": `[[dep]]
name = "plain"
version = 8080
[[dep]]
name = "quoted"
version = "8080"
[[dep]]
name = "hex"
version = 0x1F90
[[dep]]
name = "float"
version = 8_080.0
[[dep]]
name = "when"
at = 1979-05-27T07:32:00Z
day = 1979-05-27
enabled = true
`,
		"f.json": `{"n": [
  {"id": "a", "v": 1000},
  {"id": "b", "v": 1e3},
  {"id": "c", "v": 1000.0},
  {"id": "d", "v": "1000"},
  {"id": "e", "v": -0},
  {"id": "f", "v": 12345678901234567890123},
  {"id": "g", "v": 9007199254740993},
  {"id": "h", "v": true},
  {"id": "i", "v": null},
  {"id": "z1", "v": 0e1000000000000000},
  {"id": "z2", "v": -0.0E-99999999999999999999},
  {"id": "z3", "v": 1e1000000000000000},
  {"id": "z4", "v": 10e999999999999999},
  {"id": "z5", "v": 0.001e-99999999999999999999999999999}
]}`,
		"z.yaml": "a: [{p: 0e1000000000000000, n: z1}, {p: -0.0E-99999999999999999999, n: z2}, {p: 0, n: z3}]\n",
		"z.toml": "a = [{p = 0e1000000000000000, n = 'z1'}, {p = -0.0E-99999999999999999999, n = 'z2'}, {p = 0.0, n = 'z3'}]\n",
	})
	for _, c := range []struct{ file, path, want string }{
		{"f.yaml", "ports[?@.port == 8080].name", "plain@2 hex@5 float@6 exp@7 under@8"},
		{"f.yaml", "ports[?@.port == 8.08e3].name", "plain@2 hex@5 float@6 exp@7 under@8"},
		{"f.yaml", "ports[?@.port == '8080'].name", "quoted@3 str@4"},
		{"f.yaml", "flags[?@.enabled == true].id", "a@11 b@12"},
		{"f.yaml", "flags[?@.enabled == 'true'].id", "c@13"},
		{"f.yaml", "flags[?@.enabled == null].id", "d@14 e@15 f@16"},
		{"f.yaml", "flags[?@.enabled == false].id", "h@18"},
		{"f.yaml", "dates[?@.d == '2026-10-17'].id", "a@20 b@21"},
		{"f.yaml", "dates[?@.d == '2026-10-17T00:00:00Z'].id", "c@22"},
		{"f.yaml", "refs[?@.v == 'api'].id", "a@24"},
		{"f.yaml", "refs[?@.v == 'aGk='].id", "b@25"},
		{"f.toml", "dep[?@.version == 8080].name", "plain@2 hex@8 float@11"},
		{"f.toml", "dep[?@.version == '8080'].name", "quoted@5"},
		{"f.toml", "dep[?@.at == '1979-05-27T07:32:00Z'].name", "when@14"},
		{"f.toml", "dep[?@.day == '1979-05-27'].name", "when@14"},
		{"f.toml", "dep[?@.enabled == true].name", "when@14"},
		{"f.toml", "dep[?@.enabled == 'true'].name", ""},
		{"f.json", "n[?@.v == 1000].id", "a@2 b@3 c@4"},
		{"f.json", "n[?@.v == '1000'].id", "d@5"},
		{"f.json", "n[?@.v == 12345678901234567890123].id", "f@7"},
		{"f.json", "n[?@.v == 12345678901234567890124].id", ""},
		{"f.json", "n[?@.v == 9007199254740993].id", "g@8"},
		{"f.json", "n[?@.v == 9007199254740992].id", ""},
		{"f.json", "n[?@.v == true].id", "h@9"},
		{"f.json", "n[?@.v == 'true'].id", ""},
		{"f.json", "n[?@.v == null].id", "i@10"},
		{"f.json", "n[?@.v == 'null'].id", ""},
		{"f.json", "n[?@.v == 0].id", "e@6 z1@11 z2@12"},
		{"f.json", "n[?@.v == 1e1000000000000000].id", "z3@13 z4@14"},
		{"f.json", "n[?@.v == 1e1000000000000001].id", ""},
		{"f.json", "n[?@.v == 0.1e1000000000000001].id", "z3@13 z4@14"},
		{"f.json", "n[?@.v == 1e-100000000000000000000000000002].id", "z5@15"},
		{"f.json", "n[?@.v == 1e-100000000000000000000000000001].id", ""},
		{"z.yaml", "a[?@.p == 0].n", "z1@1 z2@1 z3@1"},
		{"z.toml", "a[?@.p == 0].n", "z1@1 z2@1 z3@1"},
	} {
		var spec Spec
		switch filepath.Ext(c.file) {
		case ".yaml":
			spec.YAML = &YAML{File: c.file, Path: c.path}
		case ".json":
			spec.JSON = &JSON{File: c.file, Path: c.path}
		default:
			spec.TOML = &TOML{File: c.file, Path: c.path}
		}
		if got := texts(mustRun(t, spec, root)); got != c.want {
			t.Errorf("%s %s: got %q, want %q", c.file, c.path, got, c.want)
		}
	}
}

// A filter selects members of a sequence and values of a mapping in the
// file's order, and only where every comparison holds.
func TestFilterSelects(t *testing.T) {
	root := repo(t, map[string]string{"f.yaml": `servers:
  zeta: {listen: 80, server_name: z.example}
  alpha: {listen: 443, server_name: a.example}
  mid: {listen: 80, server_name: m.example}
spec:
  template:
    spec:
      containers:
        - name: api
          env: prod
          image: api:2
          args: [--port, "8080"]
          labels: {app.kubernetes.io/name: api}
        - name: worker
          env: prod
          image: worker:2
          args: [--queue]
        - name: api
          env: dev
          image: api:3
          args: [--port, "9090"]
ports: [80, 443, 80]
name: x
`})
	for path, want := range map[string]string{
		"servers[?@.listen == 80].server_name":                                              "z.example@2 m.example@4",
		"servers[?@.listen == 80 && @.server_name == 'm.example'].listen":                   "80@4",
		"spec.template.spec.containers[?@.name == 'api'].image":                             "api:2@11 api:3@20",
		"spec.template.spec.containers[?@.name == 'api' && @.env == 'dev'].image":           "api:3@20",
		"spec.template.spec.containers[?'prod' == @.env].name":                              "api@9 worker@14",
		"spec.template.spec.containers[?@.args[0] == '--port'].image":                       "api:2@11 api:3@20",
		"spec.template.spec.containers[?@.args[-1] == '--queue'].image":                     "worker:2@16",
		"spec.template.spec.containers[?@.args[1] == 8080].image":                           "",
		"spec.template.spec.containers[?@.labels['app.kubernetes.io/name'] == 'api'].image": "api:2@11",
		"spec.template.spec.containers[?@.missing == 'x'].image":                            "",
		"spec.template.spec.containers[?@.args == '--port'].image":                          "",
		"spec.template.spec.containers[?@.name == 'api'][?@ == 'prod']":                     "prod@10",
		"ports[?@ == 80]": "80@22 80@22",
		"name[?@ == 'x']": "",
	} {
		if got := texts(mustRun(t, yamlSpec(path), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// A filter compares the node an alias names, with its type.
func TestFilterThroughAliases(t *testing.T) {
	root := repo(t, map[string]string{"f.yaml": `base: &b {name: api, port: 8080}
list:
  - *b
  - {<<: *b, name: other}
  - {name: api, port: "8080"}
`})
	if got := texts(mustRun(t, yamlSpec("list[?@.port == 8080].name"), root)); got != "api@1 other@4" {
		t.Errorf("got %q", got)
	}
}

func TestBuildRefusesFilterThatDoesNotParse(t *testing.T) {
	for kind, s := range map[string]Spec{
		"yaml": {YAML: &YAML{File: "f", Path: "a[?@.b != 1]"}},
		"json": {JSON: &JSON{File: "f", Path: "a[?@.b != 1]"}},
		"toml": {TOML: &TOML{File: "f", Path: "a[?@.b != 1]"}},
	} {
		_, err := Build(s)
		wantError(t, err, kind+`: path "a[?@.b != 1]": column 8: operator != is not supported in a filter; only == is`)
	}
}

// A number scalar is read once, however many aliases name it: the planted
// break is reading it again on every visit, which cost 10000 aliases of a
// 1 MB number 10 GB of allocation and 80 s. Here 200 aliases of a number of
// 64 KiB, which the break reads 200 times.
func TestFilterReadsANumberOnce(t *testing.T) {
	filterReadsANumberOnce(t, 1<<16)
}

// The same with a number of 1 MiB, which -race slows past make test's time.
func TestFilterReadsALargeNumberOnce(t *testing.T) {
	slowOnly(t)
	filterReadsANumberOnce(t, 1<<20)
}

// filterReadsANumberOnce filters 200 aliases of a number of n digits, and
// allows the allocation of reading it about once: 8 times n.
func filterReadsANumberOnce(t *testing.T, n int) {
	t.Helper()
	data := "b: &b 1." + strings.Repeat("0", n) + "1\nrefs: [" + strings.Repeat("*b, ", 199) + "*b]\n"
	docs, err := parseYAML([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	p, err := parsePath("refs[?@ == 2]")
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := p.eval(docs...)
	runtime.ReadMemStats(&after)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d nodes, %v", len(got), err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8*uint64(n) {
		t.Errorf("200 aliases of a number of %d digits allocated %d bytes; it is read once, about %d", n, grew, n)
	}
}

// The planted breaks: a string and a number one byte past maxLiteral. An
// escape counts as the byte it decodes to.
func TestFilterLiteralLimit(t *testing.T) {
	for _, c := range []struct{ lit, want string }{
		{"'" + strings.Repeat("x", maxLiteral) + "'", ""},
		{"'" + strings.Repeat(`\n`, maxLiteral) + "'", ""},
		{"'" + strings.Repeat("x", maxLiteral+1) + "'", "column 11: a string in a filter holds more than 2048 bytes, the limit"},
		{`"` + strings.Repeat("é", maxLiteral/2) + `x"`, "column 11: a string in a filter holds more than 2048 bytes, the limit"},
		{"1" + strings.Repeat("0", maxLiteral-1), ""},
		{"1" + strings.Repeat("0", maxLiteral), "column 11: a number in a filter is written in more than 2048 bytes, the limit"},
		{"-0." + strings.Repeat("0", maxLiteral-4) + "1", ""},
		{"-0." + strings.Repeat("0", maxLiteral-3) + "1", "column 11: a number in a filter is written in more than 2048 bytes, the limit"},
	} {
		_, err := parsePath("a[?@.b == " + c.lit + "]")
		if c.want == "" {
			if err != nil {
				t.Errorf("a literal of %d bytes: %v", len(c.lit), err)
			}
			continue
		}
		wantError(t, err, c.want)
	}
}

// The planted breaks: one comparison past maxComparisons, and a filter over
// more members than maxFilterBytes allows. Each comparison looks a key of
// maxLiteral bytes up and compares a literal of 565, so testing a member
// costs 200000 bytes and the limit comes at 20000 members, which cost
// maxFilterBytes exactly: the planted break >= refuses them. Dear
// comparisons keep the members few; TestFilterLimitsAtTheCheapestCost
// reaches the limit with the cheapest.
func TestFilterLimits(t *testing.T) {
	if _, err := parsePath(comparisons(maxComparisons)); err != nil {
		t.Errorf("%d comparisons: %v", maxComparisons, err)
	}
	_, err := parsePath(comparisons(maxComparisons + 1))
	wantError(t, err, fmt.Sprintf("column %d: a filter holds more than 64 comparisons, the limit", 4+10*maxComparisons))

	dear := "@['" + strings.Repeat("k", maxLiteral) + "'] == '" + strings.Repeat("v", 565) + "'"
	filterAtTheLimit(t, "a[?"+strings.Repeat(dear+" && ", maxComparisons-1)+dear+"]", 20000)
}

// At the limit with the cheapest comparisons, @ == 1 on every member, the
// filter tests the most members, 64 times each: 0.8 s plain, and over 60 s
// under -race, so make slow runs it.
func TestFilterLimitsAtTheCheapestCost(t *testing.T) {
	slowOnly(t)
	filterAtTheLimit(t, comparisons(maxComparisons), 242248)
}

// comparisons is a filter of n comparisons, @ == 1 and then @ == 2, which
// every member of 1 tests in full.
func comparisons(n int) string {
	return "a[?" + strings.Repeat("@ == 1 && ", n-1) + "@ == 2]"
}

// filterAtTheLimit evaluates the filter of path over a list of as many
// members as maxFilterBytes allows, want of them, and refuses one more.
func filterAtTheLimit(t *testing.T, path string, want int) {
	t.Helper()
	p, err := parsePath(path)
	if err != nil {
		t.Fatal(err)
	}
	one := &node{typ: typeNumber, text: "1", line: 1}
	list := &node{shape: sequence}
	for range maxFilterBytes / p.sels[1].cost {
		list.items = append(list.items, one)
	}
	if len(list.items) != want {
		t.Fatalf("%d members at the limit, want %d", len(list.items), want)
	}
	root := newMapping(1)
	root.set("a", list)
	if got, err := p.eval(root); err != nil || len(got) != 0 {
		t.Fatalf("at the limit: %d nodes, %v", len(got), err)
	}
	list.items = append(list.items, one)
	_, err = source{file: "f", limit: defaultMaxBytes}.values(p, root)
	msg := "f: path " + quote(p.text) + ": its filters would hash and compare more than 4000000000 bytes, the limit; narrow the path before the filter, or compare fewer or shorter keys and literals"
	if err == nil || err.Error() != msg {
		t.Errorf("error of %d bytes %.300q, want %q", len(fmt.Sprint(err)), fmt.Sprint(err), msg)
	}
}

// The filter work refusal quotes the first maxQuote bytes of the path, which
// can be as long as the configuration: the path of TestFilterLimits is
// 168 KB. The planted break is the message quoting all of it.
func TestFilterWorkMessageQuotesAShortPartOfThePath(t *testing.T) {
	dear := "@['" + strings.Repeat("k", maxLiteral) + "'] == '" + strings.Repeat("v", 565) + "'"
	p, err := parsePath("a[?" + strings.Repeat(dear+" && ", maxComparisons-1) + dear + "]")
	if err != nil {
		t.Fatal(err)
	}
	list := &node{shape: sequence}
	one := &node{typ: typeNumber, text: "1", line: 1}
	for range maxFilterBytes/p.sels[1].cost + 1 {
		list.items = append(list.items, one)
	}
	root := newMapping(1)
	root.set("a", list)
	_, err = source{file: "f", limit: defaultMaxBytes}.values(p, root)
	want := `f: path "a[?@['` + strings.Repeat("k", maxQuote-6) + `"...: its filters would hash and compare more than 4000000000 bytes`
	if err == nil || !strings.HasPrefix(err.Error(), want) || len(err.Error()) > 4*maxQuote+len(errFilterWork.Error()) {
		t.Errorf("error of %d bytes %.300q, want one that starts %q", len(fmt.Sprint(err)), fmt.Sprint(err), want)
	}
}

// A filter is charged, for each member it tests, the bytes its keys hash
// and its literals compare, and stepBytes for each comparison and each step
// after @, which is what the work of one costs beside its bytes.
func TestFilterCost(t *testing.T) {
	long := strings.Repeat("k", maxLiteral)
	for in, want := range map[string]int64{
		"a[?@ == 1]":                           stepBytes + 2,
		"a[?@ == 1.25e3]":                      stepBytes + 4,
		"a[?@.ab == 'xyz']":                    stepBytes + 2 + stepBytes + 3,
		"a[?@[0] == true]":                     2 * stepBytes,
		"a[?@['" + long + "'] == null]":        2*stepBytes + maxLiteral,
		"a[?@.a.b == '' && @[-1] == 'x']":      3*stepBytes + 2 + 2*stepBytes + 1,
		"a[?'" + long + "' == @." + long + "]": 2*stepBytes + 2*maxLiteral,
	} {
		p, err := parsePath(in)
		if err != nil {
			t.Fatalf("%.40s: %v", in, err)
		}
		if got := p.sels[1].cost; got != want {
			t.Errorf("%.40s: cost %d, want %d", in, got, want)
		}
	}
}

// The planted breaks: a key after @ one byte past maxLiteral, written after
// a dot and in brackets. A key is hashed on every member, as a literal is
// compared.
func TestFilterKeyLimit(t *testing.T) {
	for _, c := range []struct{ key, want string }{
		{"." + strings.Repeat("k", maxLiteral), ""},
		{"['" + strings.Repeat("k", maxLiteral) + "']", ""},
		{"." + strings.Repeat("k", maxLiteral+1), "column 5: a key in a filter holds more than 2048 bytes, the limit"},
		{"['" + strings.Repeat("k", maxLiteral+1) + "']", "column 5: a key in a filter holds more than 2048 bytes, the limit"},
		{".a['" + strings.Repeat("é", maxLiteral/2) + "x']", "column 7: a key in a filter holds more than 2048 bytes, the limit"},
	} {
		_, err := parsePath("a[?@" + c.key + " == 1]")
		if c.want == "" {
			if err != nil {
				t.Errorf("a key of %d bytes: %v", len(c.key), err)
			}
			continue
		}
		wantError(t, err, c.want)
	}
}

// [*] and a filter take a mapping's values from its ordered list, never by
// looking a key up: a lookup hashes the whole key, and aliases of a mapping
// with long keys would make every visit cost their length. The planted
// break is a lookup, which finds nothing once the hash map is gone.
func TestMembersAreNotLookedUp(t *testing.T) {
	for format, data := range map[string]string{
		"yaml": "m: {b: 1, a: 2, c: 1}\n",
		"json": `{"m": {"b": 1, "a": 2, "c": 1}}`,
		"toml": "m = {b = 1, a = 2, c = 1}\n",
	} {
		var root *node
		var err error
		switch format {
		case "yaml":
			var docs []*node
			docs, err = parseYAML([]byte(data))
			if err == nil {
				root = docs[0]
			}
		case "json":
			root, err = parseJSON([]byte(data))
		default:
			root, err = parseTOML([]byte(data))
		}
		if err != nil {
			t.Fatal(err)
		}
		m := root.field["m"]
		want := []*node{m.field["b"], m.field["a"], m.field["c"]}
		m.field = nil
		for path, want := range map[string][]*node{"m[*]": want, "m[?@ == 1]": {want[0], want[2]}} {
			p, err := parsePath(path)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := p.eval(root); err != nil || !slices.Equal(got, want) {
				t.Errorf("%s %s: got %v, %v", format, path, got, err)
			}
		}
	}
}
