package sudoers

import (
	"fmt"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// The outcomes of matching a user against a list, as a set: the list
// grants, denies, or says nothing, and a set of more than one outcome is
// one the host decides.
const (
	grants uint8 = 1 << iota
	denies
	silent
)

// resolver resolves the aliases of a tree, with the aliases it is inside,
// and counts what the records it builds hold.
type resolver struct {
	t      *tree
	meter  *core.Meter
	inside map[string]bool
	nodes  int
	bytes  int64
	x      *extractor
}

// grants are a record for each command the user specifications grant the
// user the spec names, in file order: definite, or not literal where the
// host decides whether the user is one the list names. With grants: true
// they are every specification's records, whatever its user list.
func (l *loader) grants() ([]check.Value, error) {
	r := &resolver{t: &l.tree, meter: l.meter, inside: map[string]bool{}, x: l.x}
	var out []check.Value
	for _, u := range l.tree.specs {
		v, err := r.selects(u.users)
		if err != nil || v&grants == 0 {
			if err != nil {
				return nil, err
			}
			continue
		}
		recs, err := r.records(u, v != grants)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// selects is the set of outcomes of the user list ms for the spec: every
// specification grants for grants: true, and for user: the list decides.
func (r *resolver) selects(ms []member) (uint8, error) {
	if r.x.all {
		return grants, nil
	}
	return r.verdict(ms)
}

// records are the records of u's commands, not literal when undecided is
// set or a part of one is.
func (r *resolver) records(u userSpec, undecided bool) ([]check.Value, error) {
	users, unl, err := r.list(u.users, userList)
	if err != nil {
		return nil, err
	}
	var out []check.Value
	for _, p := range u.privs {
		hosts, hnl, err := r.list(p.hosts, hostList)
		if err != nil {
			return nil, err
		}
		for _, cs := range p.cmnds {
			recs, err := r.commandRecords(cs, users, hosts, undecided || unl || hnl)
			if err != nil {
				return nil, err
			}
			out = append(out, recs...)
		}
	}
	return out, nil
}

// commandRecords are the records of cs, granted to users on hosts: one
// for each command its command alias, if it names one, holds.
func (r *resolver) commandRecords(cs cmndSpec, users, hosts *check.Data, notLiteral bool) ([]check.Value, error) {
	cmds, cnl, err := r.list([]member{cs.cmd}, cmndList)
	if err != nil {
		return nil, err
	}
	c, err := r.carriedData(cs)
	if err != nil {
		return nil, err
	}
	nl := notLiteral || cnl || c.notLiteral
	out := make([]check.Value, 0, len(cmds.Items))
	for _, cmd := range cmds.Items {
		rec := &check.Data{Kind: check.DataMap, At: cmd.At, Keys: Fields, Items: []*check.Data{cmd, hosts, c.options, c.runas, c.groups, c.tags, users}}
		if err := r.count(rec); err != nil {
			return nil, err
		}
		out = append(out, check.Value{Text: rec.Show(check.ShownBytes), At: rec.At, Data: rec, NotLiteral: nl})
	}
	return out, nil
}

// carriedFields are the fields of a record that a command carries over:
// its Runas lists, tags and options, and whether one is not literal.
type carriedFields struct {
	runas, groups, tags, options *check.Data
	notLiteral                   bool
}

// carriedData is what cs carries, as a record's fields, an empty list at
// the command's line.
func (r *resolver) carriedData(cs cmndSpec) (carriedFields, error) {
	var c carriedFields
	runas, rnl, err := r.list(cs.runas, runasList)
	if err != nil {
		return c, err
	}
	groups, gnl, err := r.list(cs.groups, runasList)
	if err != nil {
		return c, err
	}
	tags, tnl := settings(cs.tags[:], cs.cmd.at)
	options, onl := settings(cs.options[:], cs.cmd.at)
	runas.At, groups.At = orAt(runas.At, cs.cmd.at), orAt(groups.At, cs.cmd.at)
	return carriedFields{runas: runas, groups: groups, tags: tags, options: options, notLiteral: rnl || gnl || tnl || onl}, nil
}

// orAt is at, or where for an empty list, which has none.
func orAt(at, where check.Location) check.Location {
	if at.File == "" {
		return where
	}
	return at
}

// joinItems is the texts of a list's items joined by a comma and a space.
func joinItems(d *check.Data) string {
	texts := make([]string, len(d.Items))
	for i, it := range d.Items {
		texts[i] = it.Text
	}
	return strings.Join(texts, ", ")
}

// count holds the records to the extractor's values and bytes.
func (r *resolver) count(rec *check.Data) error {
	n, b := rec.Size()
	r.nodes, r.bytes = r.nodes+n, r.bytes+b
	switch {
	case r.nodes > r.x.maxValues():
		return core.TooManyValues(rec.At.File)
	case r.bytes > r.x.limit:
		return fmt.Errorf("%s: the records the extractor returns hold more than %d bytes in all, the extractor's max_bytes; the aliases they resolve hold more than the files", rec.At.File, r.x.limit)
	}
	return nil
}

// settings are the tags or options set, as a list of the slots written.
func settings(set []setting, at check.Location) (*check.Data, bool) {
	d := &check.Data{Kind: check.DataList, At: at}
	nl := false
	for _, s := range set {
		if s.text == "" {
			continue
		}
		if len(d.Items) == 0 {
			d.At = s.at
		}
		d.Items = append(d.Items, &check.Data{Text: s.text, At: s.at})
		nl = nl || s.notLiteral
	}
	return d, nl
}

// list is ms, a list of kind, with its aliases resolved: each entry at
// the line it is written on, a ! before a negated one. An alias negated
// whole is one entry, its entries in parentheses after the !, as sudo
// negates the alias's outcome and not each entry's.
func (r *resolver) list(ms []member, kind int) (*check.Data, bool, error) {
	return r.listNegated(ms, kind, false)
}

// listNegated is list, with every entry negated once more when neg is
// set: the entries of an alias of one entry, which a ! before it negates.
func (r *resolver) listNegated(ms []member, kind int, neg bool) (*check.Data, bool, error) {
	d := &check.Data{Kind: check.DataList}
	nl := false
	for _, m := range ms {
		items, mnl, err := r.entry(m, kind, neg)
		if err != nil {
			return nil, false, err
		}
		d.Items = append(d.Items, items...)
		nl = nl || mnl
	}
	if len(d.Items) > 0 {
		d.At = d.Items[0].At
	}
	return d, nl, nil
}

// entry is the items of one entry m of a list of kind, negated once more
// when outer is set. A negated alias of one entry negates that entry, so
// two negations cancel; of several, it is one item.
func (r *resolver) entry(m member, kind int, outer bool) ([]*check.Data, bool, error) {
	if err := r.meter.Charge(core.LineWork); err != nil {
		return nil, false, err
	}
	neg := outer != m.neg
	if !m.alias {
		return []*check.Data{{Text: negated(neg, m.text), At: m.at}}, m.notLiteral, nil
	}
	a, leave, err := r.enter(m, kind)
	if err != nil {
		return nil, false, err
	}
	defer leave()
	if !neg || len(a.members) == 1 {
		sub, nl, err := r.listNegated(a.members, kind, neg)
		return itemsOf(sub), nl, err
	}
	sub, nl, err := r.list(a.members, kind)
	if err != nil {
		return nil, false, err
	}
	return []*check.Data{{Text: "!(" + joinItems(sub) + ")", At: m.at}}, nl, nil
}

// itemsOf is d's items, or nil for a nil d.
func itemsOf(d *check.Data) []*check.Data {
	if d == nil {
		return nil
	}
	return d.Items
}

// negated is text with a ! before it when neg is set.
func negated(neg bool, text string) string {
	if neg {
		return "!" + text
	}
	return text
}

// enter is the alias m names, of kind, and the func that leaves it once
// resolved; an alias the files do not define, and one inside itself, are
// refused.
func (r *resolver) enter(m member, kind int) (*alias, func(), error) {
	a := r.t.aliases[kind][m.text]
	if a == nil {
		return nil, nil, fmt.Errorf("%s: the %s alias %s is not defined in the files errata reads; sudo matches nothing for it, and a file outside the repository may define it", m.at, strings.Fields(listNames[kind])[0], m.text)
	}
	key := fmt.Sprint(kind, m.text)
	if r.inside[key] {
		return nil, nil, fmt.Errorf("%s: the alias %s holds itself; visudo refuses it, and sudo matches nothing through it, so errata cannot tell what the list holds", m.at, m.text)
	}
	r.inside[key] = true
	return a, func() { delete(r.inside, key) }, nil
}

// verdict is the set of outcomes of the user list ms for the user the
// spec names: sudo takes the last entry that matches, and an alias's
// outcome, negated with it.
func (r *resolver) verdict(ms []member) (uint8, error) {
	state := silent
	for _, m := range ms {
		mm, err := r.memberVerdict(m)
		if err != nil {
			return 0, err
		}
		next := mm &^ silent
		if mm&silent != 0 {
			next |= state
		}
		state = next
	}
	return state, nil
}

// memberVerdict is the set of outcomes of the entry m alone. An entry a
// template renders may name any user, so it may match.
func (r *resolver) memberVerdict(m member) (uint8, error) {
	if err := r.meter.Charge(core.LineWork); err != nil {
		return 0, err
	}
	if m.alias {
		a, leave, err := r.enter(m, userList)
		if err != nil {
			return 0, err
		}
		defer leave()
		v, err := r.verdict(a.members)
		if m.neg {
			v = swap(v)
		}
		return v, err
	}
	outcome := grants
	if m.neg {
		outcome = denies
	}
	switch c := compareUser(m.text, r.x.user, r.t.folds()); {
	case c == mayMatch, m.notLiteral:
		return outcome | silent, nil
	case c == matches:
		return outcome, nil
	}
	return silent, nil
}

// swap is v with grants and denies exchanged.
func swap(v uint8) uint8 {
	out := v & silent
	if v&grants != 0 {
		out |= denies
	}
	if v&denies != 0 {
		out |= grants
	}
	return out
}

// How an entry of a user list compares with the user the spec names.
const (
	differs = iota
	matches
	mayMatch
)

// compareUser compares the entry with the user the spec names: user: ALL
// matches every user, and an entry matches itself. What else may match is
// the host's to decide: for a user, compareForUser says, and for a group,
// compareGroups. fold is, for users and for groups, whether names may
// match in another case.
func compareUser(entry, user string, fold [2]bool) int {
	switch {
	case entry == "ALL" || entry == user:
		return matches
	case isUserName(user):
		return compareForUser(entry, user, fold[0])
	case strings.HasPrefix(user, "%"):
		return compareGroups(entry, user, fold[1])
	}
	return differs
}

// compareForUser compares an entry with a user named by name or ID: a
// group or a netgroup, the other form of the user's name, and with fold
// the name in another case, may match, as the host's groups, netgroups,
// passwd and sudo's case_insensitive_user decide.
func compareForUser(entry, user string, fold bool) int {
	switch {
	case !isUserName(entry), strings.HasPrefix(entry, "#") != strings.HasPrefix(user, "#"):
		return mayMatch
	case fold && strings.EqualFold(entry, user):
		return mayMatch
	}
	return differs
}

// compareGroups compares an entry with a group: another group of the same
// name as a Unix or a non-Unix group (%ops and %:ops), one named by ID
// where the other is named by name, and with fold the name in another
// case, may match.
func compareGroups(entry, group string, fold bool) int {
	if !strings.HasPrefix(entry, "%") {
		return differs
	}
	a, b := groupName(entry), groupName(group)
	switch {
	case a == b, strings.HasPrefix(a, "#") != strings.HasPrefix(b, "#"):
		return mayMatch
	case fold && strings.EqualFold(a, b):
		return mayMatch
	}
	return differs
}

// groupName is a group entry without its % and the : of a non-Unix group.
func groupName(entry string) string {
	return strings.TrimPrefix(strings.TrimPrefix(entry, "%"), ":")
}
