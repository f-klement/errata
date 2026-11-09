package udev

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// keyRule is how udev's parse_token takes one key: the attribute it takes,
// whether == and != make it a match key, the assignment operators it
// takes and the operator udev reads each as, and a test of an assigned
// value. A key, an attribute or an operator it does not take drops the
// rule, as udev drops it.
type keyRule struct {
	attr   func(attr string, has bool) bool
	match  bool
	assign map[op]op
	value  func(t *token) verdict
	noFold bool // the i prefix is refused, as on TEST, PROGRAM and IMPORT
}

// verdict is what udev does with an assignment's value.
type verdict uint8

const (
	keepValue   verdict = iota // the value is applied
	ignoreValue                // udev warns and passes over the key alone
	dropRule                   // udev drops the rule
)

// The assignment operators of each family of keys, and what udev reads
// each as, with the warning "assuming '='" where it changes one.
var (
	asMatch   = map[op]op{opAssign: opMatch, opAdd: opMatch, opFinal: opMatch} // PROGRAM, IMPORT
	allOps    = map[op]op{opAssign: opAssign, opAdd: opAdd, opRemove: opRemove, opFinal: opFinal}
	setOps    = map[op]op{opAssign: opAssign, opAdd: opAssign, opFinal: opFinal} // NAME, OWNER, GROUP, MODE, OPTIONS
	envOps    = map[op]op{opAssign: opAssign, opAdd: opAdd, opFinal: opAssign}
	tagOps    = map[op]op{opAssign: opAssign, opAdd: opAdd, opRemove: opRemove, opFinal: opAssign}
	attrOps   = map[op]op{opAssign: opAssign, opAdd: opAssign, opFinal: opAssign} // ATTR, SYSCTL
	labelOps  = map[op]op{opAssign: opAssign, opAdd: opAdd, opFinal: opAssign}    // SECLABEL
	runOps    = map[op]op{opAssign: opAssign, opAdd: opAdd, opFinal: opFinal}
	gotoOps   = map[op]op{opAssign: opAssign} // GOTO, LABEL
	matchOnly = keyRule{attr: noAttr, match: true}
)

// keyRules are the keys udev reads, as parse_token of systemd 257 takes
// them.
var keyRules = map[string]keyRule{
	"ACTION":     matchOnly,
	"DEVPATH":    matchOnly,
	"KERNEL":     matchOnly,
	"SUBSYSTEM":  matchOnly,
	"DRIVER":     matchOnly,
	"KERNELS":    matchOnly,
	"SUBSYSTEMS": matchOnly,
	"DRIVERS":    matchOnly,
	"TAGS":       matchOnly,
	"RESULT":     matchOnly,
	"ATTRS":      {attr: needAttr, match: true},
	"CONST":      {attr: oneOf("arch", "virt"), match: true},
	"TEST":       {attr: modeAttr, match: true, noFold: true},
	"SYMLINK":    {attr: noAttr, match: true, assign: allOps},
	"NAME":       {attr: noAttr, match: true, assign: setOps, value: nameValue},
	"ENV":        {attr: needAttr, match: true, assign: envOps, value: envValue},
	"TAG":        {attr: noAttr, match: true, assign: tagOps},
	"ATTR":       {attr: needAttr, match: true, assign: attrOps},
	"SYSCTL":     {attr: needAttr, match: true, assign: attrOps},
	"PROGRAM":    {attr: noAttr, match: true, assign: asMatch, noFold: true},
	"IMPORT":     {attr: oneOf("file", "program", "builtin", "db", "cmdline", "parent"), match: true, assign: asMatch, noFold: true},
	"OPTIONS":    {attr: noAttr, assign: setOps, value: optionsValue},
	"OWNER":      {attr: noAttr, assign: setOps},
	"GROUP":      {attr: noAttr, assign: setOps},
	"MODE":       {attr: noAttr, assign: setOps},
	"SECLABEL":   {attr: needAttr, assign: labelOps},
	"RUN":        {attr: runAttr, assign: runOps},
	"GOTO":       {attr: noAttr, assign: gotoOps},
	"LABEL":      {attr: noAttr, assign: gotoOps},
}

// refusesFold reports a key k udev refuses the i prefix on with operator
// o: any assignment, and TEST, PROGRAM and IMPORT whatever the operator.
func (k keyRule) refusesFold(o op) bool { return k.noFold || !o.isMatch() }

// noAttr takes a key written without braces.
func noAttr(_ string, has bool) bool { return !has }

// needAttr takes a key with an attribute that is not empty.
func needAttr(attr string, has bool) bool { return has && attr != "" }

// oneOf takes a key whose attribute is one of names.
func oneOf(names ...string) func(string, bool) bool {
	return func(attr string, has bool) bool { return has && slices.Contains(names, attr) }
}

// modeAttr takes TEST without braces or with empty ones, or with a file
// mode in octal.
func modeAttr(attr string, has bool) bool {
	if !has || attr == "" {
		return true
	}
	m, err := strconv.ParseUint(attr, 8, 32)
	return err == nil && m <= 0o7777
}

// runAttr takes RUN without braces, or with program or builtin.
func runAttr(attr string, has bool) bool { return !has || attr == "program" || attr == "builtin" }

// nameValue drops a rule that assigns NAME %k, which takes no effect, or
// the empty name.
func nameValue(t *token) verdict {
	if t.value == "%k" || t.value == "" {
		return dropRule
	}
	return keepValue
}

// readOnlyEnv are the properties a rule may not set: udev drops a rule
// that assigns one.
var readOnlyEnv = []string{"ACTION", "DEVLINKS", "DEVNAME", "DEVPATH", "DEVTYPE", "DRIVER", "IFINDEX", "MAJOR", "MINOR", "SEQNUM", "SUBSYSTEM", "TAGS"}

func envValue(t *token) verdict {
	if slices.Contains(readOnlyEnv, t.attr) {
		return dropRule
	}
	return keepValue
}

// logLevels are the names log_level_from_string reads, at their numbers.
var logLevels = []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

// optionsValue is how udev takes an OPTIONS value: one it knows is kept, a
// link_priority that is no number and a log_level that is no level drop
// the rule, and any other is passed over.
func optionsValue(t *token) verdict {
	v := t.value
	switch {
	case slices.Contains([]string{"string_escape=none", "string_escape=replace", "db_persist", "watch", "nowatch"}, v), strings.HasPrefix(v, "static_node="):
		return keepValue
	case strings.HasPrefix(v, "link_priority="):
		_, err := strconv.Atoi(strings.TrimPrefix(v, "link_priority="))
		return keepIf(err == nil)
	case strings.HasPrefix(v, "log_level="):
		return keepIf(isLogLevel(strings.TrimPrefix(v, "log_level=")))
	}
	return ignoreValue
}

func keepIf(ok bool) verdict {
	if ok {
		return keepValue
	}
	return dropRule
}

// isLogLevel reports reset, or a level log_level_from_string reads: a name
// or a number from 0 to 7.
func isLogLevel(s string) bool {
	n, err := strconv.Atoi(s)
	return s == "reset" || slices.Contains(logLevels, s) || err == nil && n >= 0 && n <= 7
}

// rule is one rule udev keeps: its match keys, among them PROGRAM and
// IMPORT, and its assignments, each in the order written, its GOTO and
// its LABEL.
type rule struct {
	l        *logical
	matches  []*token
	assigns  []*token
	gotoTo   string
	gotoLine int
	hasGoto  bool
	label    string
	hasLabel bool
}

// readRule reads the rule l, and returns nil for a rule udev drops.
func readRule(l *logical, m *core.Meter) (*rule, error) {
	r := &rule{l: l}
	err := eachToken(l, m, r.add)
	if errors.Is(err, errDrop) {
		return nil, nil
	}
	return r, err
}

// add takes the key and value t into r, as parse_token does.
func (r *rule) add(t *token) error {
	k, ok := keyRules[t.key]
	switch {
	case !ok, !k.attr(t.attr, t.hasAttr):
		return errDrop
	case t.fold && k.refusesFold(t.op):
		return errDrop
	case t.op.isMatch() && !k.match:
		return errDrop
	case t.op.isMatch():
		r.matches = append(r.matches, t)
		return nil
	}
	o, ok := k.assign[t.op]
	if !ok {
		return errDrop
	}
	t.op = o
	return r.assign(k, t)
}

// assign takes the assignment t of a key k takes.
func (r *rule) assign(k keyRule, t *token) error {
	v := keepValue
	if k.value != nil {
		v = k.value(t)
	}
	switch {
	case v == dropRule:
		return errDrop
	case v == ignoreValue:
		return nil
	case t.op.isMatch():
		r.matches = append(r.matches, t)
	case t.key == "GOTO":
		r.setGoto(t)
	case t.key == "LABEL":
		r.label, r.hasLabel = t.value, true
	default:
		r.assigns = append(r.assigns, t)
	}
	return nil
}

// setGoto takes the first GOTO of a rule; udev warns and passes over the
// others.
func (r *rule) setGoto(t *token) {
	if !r.hasGoto {
		r.gotoTo, r.gotoLine, r.hasGoto = t.value, r.l.lineOf(t.off), true
	}
}
