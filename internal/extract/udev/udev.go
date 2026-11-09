// Package udev is the udev extractor: every value a udev rules file
// assigns to one key, read as systemd-udevd reads the file, with the files
// of rules.d directories read in udev's order, and the rules a set of
// match keys selects.
package udev

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns every value a rules file assigns to Key, in the order udev
// applies them, each at its file and line, with the match keys of its rule
// as its condition. File names one file; Files names the files of rules.d
// directories, which errata reads in udev's order whatever the order
// written. Key is an assignment key as a rule writes it, such as MODE,
// SYMLINK, RUN{builtin} or ENV{ID_FS_TYPE}. Op keeps only the values
// assigned with one operator: =, +=, -= or :=.
//
// Tests keeps only the rules that test the match keys it names, such as
// SUBSYSTEM: usb and ATTR{idVendor}: 046d, each with == and the value as
// written, compared as text and never as udev's pattern: the rule's own
// key, or one a GOTO before it skips the rule by, as SUBSYSTEM!="usb",
// GOTO="end" does. KERNEL: sda does not keep a rule of KERNEL=="sd*".
type Spec struct {
	core.Where
	Key   string            `json:"key"`
	Op    string            `json:"op,omitempty"`
	Tests map[string]string `json:"tests,omitempty"`
}

// Kind is the udev kind, which a rule writes under the key udev.
var Kind = core.Kind[Spec]{Key: "udev", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is s with only what decides where it reads: the file or files as
// core.PlaceFiles writes them, no max_bytes, and RUN written RUN{program},
// which udev reads it as.
func Place(s Spec) Spec {
	s.Where = s.Place()
	if n, err := parseName(s.Key); err == nil {
		s.Key = n.String()
	}
	return s
}

// Resolve is s with its max_bytes written out, for errata list, sharing
// nothing with s.
func Resolve(s Spec) Spec {
	s.Where = s.Resolved()
	s.Tests = maps.Clone(s.Tests)
	return s
}

// Describe is what s's extractor returns: flat values under a label that
// names the file. A value may be not literal, where udev substitutes in it,
// whatever the file.
func Describe(s Spec) check.Description {
	file := s.File
	if file == "" && len(s.Files) > 0 {
		file = s.Files[0]
	}
	_, base := core.FileParts(file)
	return check.Description{Label: "udev, rules in " + base, NotLiteral: true}
}

// Build checks s and returns its extractor.
func Build(s Spec) (check.Extractor, error) {
	x := extractor{}
	if err := x.where(s); err != nil {
		return nil, err
	}
	if err := x.checkKey(s.Key, s.Op); err != nil {
		return nil, err
	}
	if s.Tests != nil {
		if err := x.checkTests(s.Tests); err != nil {
			return nil, err
		}
	}
	return x, nil
}

// where sets what x reads from s: one file, or the files of files:.
func (x *extractor) where(s Spec) error {
	var err error
	switch {
	case s.File != "" && s.Files != nil:
		return errors.New("udev: file and files are both set; write file for one file, or files for the files of rules.d directories")
	case s.Files != nil && len(s.Files) == 0:
		return errors.New("udev: files lists no entry; name a file or a glob, or write file: for one file")
	case s.Files != nil:
		x.files, err = core.CheckFiles(Kind.Key, s.Files, s.MaxBytes)
		x.limit = core.MaxBytes(s.MaxBytes)
		return err
	}
	x.Source, err = core.CheckSource(Kind.Key, s.File, s.MaxBytes)
	x.limit = x.Limit
	return err
}

// checkKey sets the key and the operator x selects, and refuses a key udev
// assigns no value to and an operator udev never reads it with.
func (x *extractor) checkKey(key, o string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("udev: key is required; name an assignment key, such as MODE or ENV{ID_FS_TYPE}")
	}
	n, err := parseName(key)
	if err != nil {
		return fmt.Errorf("udev: key: %w", err)
	}
	if !assignable(n.key) {
		return fmt.Errorf("udev: key %s is no key a rule assigns a value to; write one of %s", core.Quote(key), assignKeys())
	}
	x.key = n
	if o == "" {
		return nil
	}
	return x.checkOp(keyRules[n.key], o)
}

// checkOp sets the operator x selects, one udev reads the key with.
func (x *extractor) checkOp(k keyRule, o string) error {
	read := map[string]op{}
	for _, to := range k.assign {
		read[to.String()] = to
	}
	got, ok := read[o]
	if !ok {
		names := slices.Sorted(maps.Keys(read))
		return fmt.Errorf("udev: op %s is no operator udev reads %s with; write one of %s", core.Quote(o), x.key, strings.Join(names, ", "))
	}
	x.op = got
	return nil
}

// checkTests sets the match keys x selects rules by, and refuses one that
// is no key udev matches with.
func (x *extractor) checkTests(m map[string]string) error {
	if len(m) == 0 {
		return errors.New("udev: tests names no key; name match keys and values, such as SUBSYSTEM: usb, or leave tests out")
	}
	for _, written := range slices.Sorted(maps.Keys(m)) {
		n, err := parseName(written)
		if err != nil {
			return fmt.Errorf("udev: tests: %w", err)
		}
		if !keyRules[n.key].match {
			return fmt.Errorf("udev: tests names %s, which is no key udev matches with; write one of %s", core.Quote(written), matchKeys())
		}
		x.tests = append(x.tests, wanted{n, m[written]})
	}
	return nil
}

// name is a key with its attribute, as a spec names it.
type name struct {
	key, attr string
	hasAttr   bool
}

// String is n as a rule writes it.
func (n name) String() string {
	if n.hasAttr {
		return n.key + "{" + n.attr + "}"
	}
	return n.key
}

// parseName reads a key as a spec writes it, KEY or KEY{attr}, and refuses
// one udev does not read with that attribute. RUN is read as
// RUN{program}, as udev reads it.
func parseName(s string) (name, error) {
	n := name{key: s}
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if !strings.HasSuffix(s, "}") || strings.Count(s, "}") != 1 {
			return name{}, fmt.Errorf("%s has a { with no } after the attribute at its end", core.Quote(s))
		}
		n = name{key: s[:i], attr: s[i+1 : len(s)-1], hasAttr: true}
	}
	k, ok := keyRules[n.key]
	if !ok {
		return name{}, fmt.Errorf("%s is no key udev reads", core.Quote(n.key))
	}
	if !k.attr(n.attr, n.hasAttr) {
		return name{}, fmt.Errorf("%s is no attribute udev reads %s with", core.Quote(s), n.key)
	}
	return n.canonical(), nil
}

// canonical is n as udev reads it: RUN is RUN{program}.
func (n name) canonical() name {
	if n.key == "RUN" && !n.hasAttr {
		n.attr, n.hasAttr = "program", true
	}
	return n
}

// keysWhere names, sorted, the keys of keyRules for which test holds.
func keysWhere(test func(k string, r keyRule) bool) string {
	var out []string
	for k, r := range keyRules {
		if test(k, r) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

// assignable reports a key a rule assigns a value to: not GOTO and LABEL,
// which steer the rules, and not PROGRAM and IMPORT, which udev reads as
// match keys whatever their operator.
func assignable(k string) bool {
	return keyRules[k].assign != nil && !slices.Contains([]string{"GOTO", "LABEL", "PROGRAM", "IMPORT"}, k)
}

func assignKeys() string { return keysWhere(func(k string, _ keyRule) bool { return assignable(k) }) }

func matchKeys() string { return keysWhere(func(_ string, r keyRule) bool { return r.match }) }
