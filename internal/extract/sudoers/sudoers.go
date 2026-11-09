// Package sudoers is the sudoers extractor: the Defaults settings and the
// grants of a sudoers file and the files it includes inside the
// repository, read as sudo reads them, with the four kinds of alias
// resolved.
package sudoers

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns what a sudoers file sets, in the order sudo reads it, each
// value at its file and line. File names the file, which stands for
// /etc/sudoers; Files names drop-ins of a sudoers.d, read in sudo's order.
//
// Defaults names a Defaults setting, such as secure_path, and returns each
// value a Defaults line gives it, with the line's scope as its condition.
// User names an entry of a user list, a user, a %group, a +netgroup or
// ALL, and returns a record for each command a user specification grants
// it: who, on which hosts, as whom, with which tags and options. Grants
// returns those records for every user specification, whatever its user
// list. Exactly one of the three is set.
type Spec struct {
	core.Where
	Defaults string `json:"defaults,omitempty"`
	User     string `json:"user,omitempty"`
	Grants   bool   `json:"grants,omitempty"`
}

// Kind is the sudoers kind, which a rule writes under the key sudoers.
var Kind = core.Kind[Spec]{Key: "sudoers", Place: Place, Resolve: Resolve, Describe: Describe}

// Fields are the fields of each record a spec of user: returns, in the
// order of a record's keys.
var Fields = []string{"command", "host", "options", "runas", "runas_group", "tags", "user"}

// Place is s with only what decides where it reads: the file or files as
// core.PlaceFiles writes them, no max_bytes, and the setting and the user
// without the white space around them.
func Place(s Spec) Spec {
	s.Where = s.Place()
	s.Defaults = strings.TrimSpace(s.Defaults)
	s.User = strings.TrimSpace(s.User)
	return s
}

// Resolve is s with its max_bytes written out, for errata list, sharing
// nothing with s.
func Resolve(s Spec) Spec {
	s.Where = s.Resolved()
	return s
}

// Describe is what s's extractor returns: flat values for defaults:, a
// record of Fields for user:, under the kind's key. It may be not literal
// in a template file, and for a user or a group, whose grants through
// another entry that may name them the host decides.
func Describe(s Spec) check.Description {
	d := check.Description{Label: "sudoers", NotLiteral: templated(s.Where)}
	if s.User != "" || s.Grants {
		d.Shape, d.Fields = check.ShapeRecord, slices.Clone(Fields)
		u := strings.TrimSpace(s.User)
		d.NotLiteral = d.NotLiteral || isUserName(u) || strings.HasPrefix(u, "%")
	}
	return d
}

// templated reports whether a file w names is a template.
func templated(w core.Where) bool {
	for _, f := range append([]string{w.File}, w.Files...) {
		if _, base := core.FileParts(f); f != "" && core.IsTemplate(base) {
			return true
		}
	}
	return false
}

// Build checks s and returns its extractor.
func Build(s Spec) (check.Extractor, error) {
	x := extractor{defaults: strings.TrimSpace(s.Defaults), user: strings.TrimSpace(s.User), all: s.Grants}
	if err := x.where(s); err != nil {
		return nil, err
	}
	if err := x.checkSelector(s); err != nil {
		return nil, err
	}
	return x, nil
}

// where sets what x reads from s: one file, or the drop-ins of files:.
func (x *extractor) where(s Spec) error {
	var err error
	switch {
	case s.File != "" && s.Files != nil:
		return errors.New("sudoers: file and files are both set; write file for a sudoers file, or files for the drop-ins of a sudoers.d")
	case s.Files != nil && len(s.Files) == 0:
		return errors.New("sudoers: files lists no entry; name a file or a glob, or write file: for one file")
	case s.Files != nil:
		x.files, err = core.CheckFiles(Kind.Key, s.Files, s.MaxBytes)
		x.limit = core.MaxBytes(s.MaxBytes)
		return err
	}
	x.Source, err = core.CheckSource(Kind.Key, s.File, s.MaxBytes)
	x.limit = x.Limit
	return err
}

// checkSelector refuses a spec that sets none of defaults, user and
// grants, or more than one, a setting that is no name of one and a user
// that is no entry of a user list.
func (x *extractor) checkSelector(s Spec) error {
	set := 0
	for _, on := range []bool{x.defaults != "", x.user != "", x.all} {
		if on {
			set++
		}
	}
	switch {
	case set == 0:
		return errors.New("sudoers: write defaults: for a Defaults setting, such as secure_path, user: for the grants to a user or a %group, or grants: true for every grant")
	case set > 1:
		return errors.New("sudoers: set exactly one of defaults, user and grants; write each as an extractor of its own")
	case x.defaults != "":
		return checkSetting(s.Defaults)
	case x.all:
		return nil
	}
	return checkUser(s.User)
}

// checkSetting refuses a Defaults setting that is no name sudo reads: a
// setting is letters, digits and _, with no ! or operator.
func checkSetting(name string) error {
	n := strings.TrimSpace(name)
	for _, c := range []byte(n) {
		if !isNameByte(c) {
			return fmt.Errorf("sudoers: defaults is %s, which is no name of a setting: a setting is letters, digits and _, such as secure_path, written without its ! or its value", core.Quote(name))
		}
	}
	return nil
}

// checkUser refuses a user that is no entry of a user list: one with white
// space or a character sudo reads as syntax, one negated, and an alias,
// which errata resolves to its entries.
func checkUser(user string) error {
	u := strings.TrimSpace(user)
	switch {
	case strings.ContainsAny(strings.TrimPrefix(u, "%:"), "\t,:=()\"\\"):
		return fmt.Errorf("sudoers: user is %s, which holds a tab or one of , : = ( ) \" \\; name one user, %%group, %%:group, +netgroup or ALL as sudo compares it, without quotes or escapes", core.Quote(user))
	case strings.HasPrefix(u, "!"):
		return fmt.Errorf("sudoers: user is %s, which is negated; name the entry, and read a ! in the records' user field", core.Quote(user))
	case isAliasName(u):
		return fmt.Errorf("sudoers: user is %s, a User_Alias, which errata resolves to its entries; name a user or a %%group the alias holds", core.Quote(user))
	}
	return nil
}

// isUserName reports whether entry names one user, by name or #uid, rather
// than a group, a netgroup or ALL.
func isUserName(entry string) bool {
	return entry != "" && entry != "ALL" && !strings.HasPrefix(entry, "%") && !strings.HasPrefix(entry, "+")
}

// isNameByte reports a byte of a Defaults setting's name.
func isNameByte(c byte) bool {
	return strings.IndexByte(aliasBytes+"abcdefghijklmnopqrstuvwxyz", c) >= 0
}

// aliasBytes are the bytes of an alias's name: upper case letters, then
// digits and _, which may not start it.
const aliasBytes = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"

// isAliasName reports a name sudo reads as an alias: an upper case letter,
// then upper case letters, digits and _, and not ALL.
func isAliasName(s string) bool {
	if s == "" || s == "ALL" || strings.IndexByte(aliasBytes[:26], s[0]) < 0 {
		return false
	}
	for _, c := range []byte(s) {
		if strings.IndexByte(aliasBytes, c) < 0 {
			return false
		}
	}
	return true
}
