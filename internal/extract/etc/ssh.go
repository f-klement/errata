package etc

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// connection is what a spec's match: says of one connection: each Match
// criterion it gives, by name in lower case, and its value.
type connection map[string]string

// String is c as a message names it: each criterion and its value, by name.
func (c connection) String() string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(c)) {
		parts = append(parts, k+" "+c[k])
	}
	return strings.Join(parts, ", ")
}

// verdict is whether a block holds for the connection: yes, no, or a
// reason errata cannot tell.
type verdict struct {
	holds bool
	why   string // set when errata cannot tell
}

// applied are the hits OpenSSH applies to the spec's connection, in the
// order it obtains them: for a keyword that takes one value the first, and
// for one that takes several (dialect.many) every one. ssh reads its file
// in order, and a value outside any block always applies. sshd applies the
// values of the Match blocks the connection matches over those outside any
// block, so it takes those first and the others only when no Match block it
// matches sets the keyword. A block errata cannot decide that comes before
// the value it would return, or any such block for a keyword of several
// values, makes the read an error, since the block may set what OpenSSH
// applies.
func (x extractor) applied(hits []hit, setters []setter, m *core.Meter) ([]hit, error) {
	dec := decider{c: x.match, d: x.d, m: m, seen: map[string]verdict{}, setters: setters}
	if err := dec.prepare(); err != nil {
		return nil, err
	}
	many := x.d.many(x.key)
	if !x.d.matchFirst {
		return dec.obtain(hits, x.key, many)
	}
	var inBlock, outside []hit
	for _, h := range hits {
		if len(h.blocks) > 0 {
			inBlock = append(inBlock, h)
		} else {
			outside = append(outside, h)
		}
	}
	got, err := dec.obtain(inBlock, x.key, many)
	if err != nil || len(got) > 0 {
		return got, err
	}
	return dec.obtain(outside, x.key, many)
}

// decider decides the blocks of one connection, each block's text once
// for each set of criteria a line before it shadows.
type decider struct {
	c    connection
	d    *dialect
	m    *core.Meter
	seen map[string]verdict
	// setters are the lines that shadow a Match criterion (dialect.shadows),
	// in the order read, and first is, for each criterion, the first of
	// them that may apply to the connection (prepare). at is the seq of the
	// block being decided: the first setter before it shadows its
	// criterion there.
	setters []setter
	first   map[string]setter
	at      int
}

// prepare finds, for each criterion a setter shadows, the first setter
// that may apply to the connection: one whose blocks hold, or cannot be
// told. It decides each setter once, in the order read, and none after the
// first of its criterion, so its work is one decision a line; a setter's
// blocks come before it, so the setters before them are decided already.
func (dec *decider) prepare() error {
	dec.first = map[string]setter{}
	for _, s := range dec.setters {
		if _, found := dec.first[s.crit]; found {
			continue
		}
		v, err := dec.all(s.blocks)
		if err != nil {
			return err
		}
		if v.holds || v.why != "" {
			dec.first[s.crit] = s
		}
	}
	return nil
}

// obtain is the hits that apply, in order: the first, or every one when
// many is set.
func (dec *decider) obtain(hits []hit, key string, many bool) ([]hit, error) {
	var out []hit
	for _, h := range hits {
		v, err := dec.all(h.blocks)
		if err != nil {
			return nil, err
		}
		if v.why != "" {
			return nil, fmt.Errorf("%s sets %s under %s, which errata cannot decide for the connection match: names (%s): %s; give the criterion in match:, or leave match: out to read every value with its block", h.at, key, core.Quote(h.condition), dec.c, v.why)
		}
		if v.holds {
			out = append(out, h)
			if !many {
				return out, nil
			}
		}
	}
	return out, nil
}

// all is whether every block of blocks holds: no when one does not, and
// undecided when none says no and one cannot be told.
func (dec *decider) all(blocks []block) (verdict, error) {
	out := verdict{holds: true}
	for _, b := range blocks {
		v, err := dec.cached(b)
		if err != nil {
			return verdict{}, err
		}
		switch {
		case v.why == "" && !v.holds:
			return verdict{}, nil
		case v.why != "" && out.why == "":
			out = v
		}
	}
	return out, nil
}

// cached is the verdict of b, decided once for its text and the criteria
// a setter before it shadows, its lookup charged to the meter.
func (dec *decider) cached(b block) (verdict, error) {
	dec.at = b.seq
	if err := dec.m.Charge(entryWork + len(b.text)); err != nil {
		return verdict{}, err
	}
	key := b.text + "\x00" + dec.shadowed()
	if v, ok := dec.seen[key]; ok {
		return v, nil
	}
	v, err := dec.block(b.text)
	dec.seen[key] = v
	return v, err
}

// entryWork is the work of one block's lookup beside its bytes, as
// core's matcher charges an entry.
const entryWork = 48

// shadowed names the criteria a setter before the block being decided
// shadows, in a fixed order.
func (dec *decider) shadowed() string {
	var names []string
	for _, c := range slices.Sorted(maps.Keys(dec.first)) {
		if dec.first[c].seq < dec.at {
			names = append(names, c)
		}
	}
	return strings.Join(names, " ")
}

// shadow is why name cannot be decided for the block being decided: the
// first line that sets name's keyword and may apply to the connection
// comes before it; "" when it does not.
func (dec *decider) shadow(name string) string {
	s, ok := dec.first[name]
	if !ok || s.seq >= dec.at {
		return ""
	}
	return s.at.String() + " sets " + s.key + " before the Match line, and ssh matches " + name + " against what it sets"
}

// block decides one block's line, a Host or a Match line as
// foldOpenSSHBlock writes it.
func (dec *decider) block(line string) (verdict, error) {
	words := strings.Fields(line)
	if words[0] == "Host" {
		return dec.host(words[1:])
	}
	return dec.matchLine(words[1:])
}

// host decides an ssh Host line as readconf.c does: the host, in lower
// case, matches a pattern and no negated one.
func (dec *decider) host(patterns []string) (verdict, error) {
	name, ok := dec.c["host"]
	if !ok {
		return verdict{why: "a Host line matches the host, which match: does not give"}, nil
	}
	name = strings.ToLower(name)
	holds := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		ok, err := wild(name, strings.TrimPrefix(p, "!"), dec.m)
		if err != nil {
			return verdict{}, err
		}
		if ok && neg {
			return verdict{}, nil
		}
		holds = holds || ok
	}
	return verdict{holds: holds}, nil
}

// matchLine decides the criteria of a Match line, which all must hold, as
// match_cfg_line in readconf.c and servconf.c reads them: no when one does
// not hold, else undecided when one cannot be told.
func (dec *decider) matchLine(words []string) (verdict, error) {
	out := verdict{holds: true}
	for i := 0; i < len(words); {
		v, n, err := dec.criterionAt(words, i)
		if err != nil {
			return verdict{}, err
		}
		i += n
		switch {
		case v.why != "" && out.why == "":
			out.why = v.why
		case v.why == "" && !v.holds:
			return verdict{}, nil
		}
	}
	return out, nil
}

// criterionAt decides the criterion at words[i], and how many words it
// takes: one for a criterion that takes no argument (alone) and for one
// written name=argument, two for any other.
func (dec *decider) criterionAt(words []string, i int) (verdict, int, error) {
	name, arg, joined := strings.Cut(words[i], "=")
	name, neg := dec.criterionName(name)
	if v, ok := alone(name); ok {
		return negated(v, neg), 1, nil
	}
	n := 1
	if !joined {
		if i+1 == len(words) {
			return verdict{why: "Match " + core.Quote(name) + " has no argument, which OpenSSH refuses"}, 1, nil
		}
		arg, n = words[i+1], 2
	}
	v, err := dec.decide(name, arg)
	return negated(v, neg), n, err
}

// alone is the verdict of a criterion that takes no argument: all holds;
// canonical and final hold on a pass of ssh's reading errata does not
// model; sshd's invalid-user holds for a user the host does not know.
func alone(name string) (verdict, bool) {
	switch name {
	case "all":
		return verdict{holds: true}, true
	case "canonical", "final":
		return verdict{why: "Match " + name + " holds on a pass of ssh's reading that errata does not model"}, true
	case "invalid-user":
		return verdict{why: "Match invalid-user holds for a user the host does not know, which the file does not say"}, true
	}
	return verdict{}, false
}

// negated is v negated when neg is set and v is decided.
func negated(v verdict, neg bool) verdict {
	if v.why == "" && neg {
		v.holds = !v.holds
	}
	return v
}

// criterionName is a criterion's name in lower case, and whether a ! before
// it negates it, which ssh reads and sshd does not.
func (dec *decider) criterionName(w string) (string, bool) {
	name := strings.ToLower(w)
	if dec.d == sshDialect && strings.HasPrefix(name, "!") {
		return name[1:], true
	}
	return name, false
}

// decide decides the criterion name with its argument arg.
func (dec *decider) decide(name, arg string) (verdict, error) {
	value, given := dec.c[name]
	why := dec.shadow(name)
	switch {
	case why != "":
		return verdict{why: why}, nil
	case name == "exec":
		return verdict{why: "Match exec runs a command, and errata never runs one"}, nil
	case !slices.Contains(dec.d.criteria, name):
		return verdict{why: "errata does not read the criterion " + core.Quote(name)}, nil
	case !given:
		return verdict{why: "Match " + name + " needs the connection's " + name + ", which match: does not give"}, nil
	}
	holds, why, err := dec.criterion(name, value, arg)
	return verdict{holds: holds, why: why}, err
}

// criterion decides one criterion name, given value, against arg.
func (dec *decider) criterion(name, value, arg string) (bool, string, error) {
	switch name {
	case "host", "originalhost":
		r, err := patternList(strings.ToLower(value), arg, true, dec.m)
		return r == 1, "", err
	case "group":
		holds, err := groupMatch(value, arg, dec.m)
		return holds, "", err
	case "address", "localaddress":
		r, err := addrList(value, arg, dec.m)
		if errors.Is(err, errNetwork) {
			return false, "Match " + name + " " + core.Quote(arg) + " holds " + errNetwork.Error(), nil
		}
		return r == 1, "", err
	case "localport":
		return localPort(value, arg)
	}
	r, err := patternList(value, arg, false, dec.m)
	return r == 1, "", err
}

// groupMatch is sshd's Match Group: one of the groups, a list split at
// commas, matches the pattern list, and none matches a negated pattern.
func groupMatch(groups, list string, m *core.Meter) (bool, error) {
	found := false
	for g := range strings.SplitSeq(groups, ",") {
		r, err := patternList(g, list, false, m)
		if err != nil || r == -1 {
			return false, err
		}
		found = found || r == 1
	}
	return found, nil
}

// localPort is sshd's Match LocalPort: arg is the port, given as value.
func localPort(value, arg string) (bool, string, error) {
	p, ok := port(arg)
	if !ok {
		return false, "Match localport " + core.Quote(arg) + " is no port", nil
	}
	want, _ := strconv.Atoi(value) // checked at Build
	return p == want, "", nil
}

// patternList is OpenSSH's match_pattern_list: s against a list of patterns
// split at commas, each negated by a ! before it, the patterns in lower
// case when lower is set. It is 1 when a pattern matches and no negated
// one, -1 when a negated one matches, and 0 otherwise.
func patternList(s, list string, lower bool, m *core.Meter) (int, error) {
	got := 0
	for p := range strings.SplitSeq(list, ",") {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		if lower {
			p = strings.ToLower(p)
		}
		ok, err := wild(s, p, m)
		if err != nil {
			return 0, err
		}
		if ok {
			if neg {
				return -1, nil
			}
			got = 1
		}
	}
	return got, nil
}

// addrList is OpenSSH's addr_match_list: the address addr against a list
// split at commas of networks, written address/length, and patterns, each
// negated by a ! before it. It is 1 when one matches and no negated one,
// -1 when a negated one matches, and 0 when none does; a network sshd
// refuses is errNetwork, and a read past the work budget core.ErrWork.
func addrList(addr, list string, m *core.Meter) (int, error) {
	a, _ := netip.ParseAddr(addr) // checked at Build
	a = a.Unmap()
	got := 0
	for p := range strings.SplitSeq(list, ",") {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		ok, err := addrOne(a, addr, p, m)
		if err != nil {
			return 0, err
		}
		if ok {
			if neg {
				return -1, nil
			}
			got = 1
		}
	}
	return got, nil
}

// errNetwork refuses a network sshd refuses: one that does not parse, or
// sets bits past its length.
var errNetwork = errors.New("a network sshd refuses")

// addrOne reports whether a, written addr, is in the network p, written
// address/length, or matches the pattern p.
func addrOne(a netip.Addr, addr, p string, m *core.Meter) (bool, error) {
	if !strings.Contains(p, "/") {
		return wild(addr, p, m)
	}
	n, err := netip.ParsePrefix(p)
	if err != nil || n.Masked() != n {
		return false, errNetwork
	}
	return n.Contains(a), nil
}

// wild is OpenSSH's match_pattern: s against p, where * matches any run of
// characters and ? any one. It runs in time of the product of the lengths
// at worst, which it charges first, rather than the recursion OpenSSH runs.
func wild(s, p string, m *core.Meter) (bool, error) {
	if err := m.Charge((len(s) + 1) * (len(p) + 1)); err != nil {
		return false, err
	}
	return wildRun(s, p), nil
}

// wildRun is wild's match, with the last * met kept to fall back to.
func wildRun(s, p string) bool {
	si, pi := 0, 0
	star, back := -1, 0
	for si < len(s) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, back = pi, si
			pi++
		case matchesOne(p, pi, s[si]):
			si, pi = si+1, pi+1
		case star < 0:
			return false
		default:
			back++
			si, pi = back, star+1
		}
	}
	return strings.Trim(p[pi:], "*") == ""
}

// matchesOne reports whether p's character at pi matches c: c itself or ?.
func matchesOne(p string, pi int, c byte) bool {
	return pi < len(p) && (p[pi] == '?' || p[pi] == c)
}

// port is arg read as a number, and whether it is one.
func port(arg string) (int, bool) {
	p, err := strconv.Atoi(arg)
	return p, err == nil
}
