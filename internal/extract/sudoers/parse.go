package sudoers

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// The kinds of a list's entries, each with an alias of its own: a user
// list's, a Runas list's, a host list's and a command list's.
const (
	userList = iota
	runasList
	hostList
	cmndList
	listKinds
)

// listNames name the kinds of list in a message.
var listNames = [listKinds]string{"user", "runas user or group", "host", "command"}

// aliasKeywords are the keywords that define an alias, with its kind.
var aliasKeywords = map[string]int{"User_Alias": userList, "Runas_Alias": runasList, "Host_Alias": hostList, "Cmnd_Alias": cmndList, "Cmd_Alias": cmndList}

// tagPairs are the tags of a command, each with its opposite, in the order
// a record lists them: a later tag of a pair replaces an earlier one.
var tagPairs = [][2]string{
	{"PASSWD", "NOPASSWD"}, {"EXEC", "NOEXEC"}, {"SETENV", "NOSETENV"},
	{"LOG_INPUT", "NOLOG_INPUT"}, {"LOG_OUTPUT", "NOLOG_OUTPUT"}, {"MAIL", "NOMAIL"},
	{"FOLLOW", "NOFOLLOW"}, {"INTERCEPT", "NOINTERCEPT"},
}

// optionNames are the options a command may take as NAME=value, in the
// order a record lists them.
var optionNames = [...]string{"CWD", "CHROOT", "ROLE", "TYPE", "PRIVS", "LIMITPRIVS", "NOTBEFORE", "NOTAFTER", "TIMEOUT", "APPARMOR_PROFILE"}

// digests are the hash algorithms a command's digest may name.
var digests = []string{"sha224", "sha256", "sha384", "sha512"}

// member is one entry of a list: negated or not, its text with quotes and
// escapes taken off, where it is, and whether it names an alias.
type member struct {
	neg        bool
	text       string
	at         check.Location
	alias      bool
	notLiteral bool
}

// setting is a tag or an option a command takes, as written, and where.
type setting struct {
	text       string
	at         check.Location
	notLiteral bool
}

// param is a value a Defaults line gives a setting: its name, its text,
// the line's scope as written, and where.
type param struct {
	name       string
	text       string
	scope      string
	at         check.Location
	notLiteral bool
}

// alias is an alias's entries and where it is defined.
type alias struct {
	members []member
	at      check.Location
}

// userSpec is a user specification: its user list, and for each host
// list after it the commands it grants there.
type userSpec struct {
	users []member
	privs []priv
}

// priv is one host list of a user specification and its commands.
type priv struct {
	hosts []member
	cmnds []cmndSpec
}

// cmndSpec is one command and what it takes, with what a command before
// it in the same list carries over.
type cmndSpec struct {
	*carried
	cmd member
}

// carried is what a command takes and carries over to the commands after
// it in its list until one replaces it: the Runas lists, the tags and the
// options. Commands that change none of it share one.
type carried struct {
	runas, groups []member
	tags          [8]setting
	options       [len(optionNames)]setting
}

// cursor reads one statement of a file.
type cursor struct {
	s *stmt
	i int
	r *reading
}

// fail is a refusal of the statement at the cursor.
func (c *cursor) fail(format string, args ...any) error {
	return core.LineError(c.line(), "%s; visudo refuses this line, and sudo since 1.9.3 drops it, so errata cannot tell what the file grants without it", fmt.Sprintf(format, args...))
}

// line is the line the cursor is on.
func (c *cursor) line() int { return c.s.lineAt(c.i) }

// at is where the cursor is, in its file.
func (c *cursor) at(off int) check.Location {
	return check.Location{File: c.r.file, Line: c.s.lineAt(off)}
}

// eof reports whether the statement is read to its end.
func (c *cursor) eof() bool { return c.i >= len(c.s.text) }

// peek is the byte at the cursor, or 0 at the end.
func (c *cursor) peek() byte {
	if c.eof() {
		return 0
	}
	return c.s.text[c.i]
}

// blank moves past spaces and tabs.
func (c *cursor) blank() {
	for !c.eof() && isBlank(c.peek()) {
		c.i++
	}
}

// expect moves past b, after white space, or refuses the statement.
func (c *cursor) expect(b byte, what string) error {
	c.blank()
	if c.peek() != b {
		return c.fail("%s", what)
	}
	c.i++
	return nil
}

// negations moves past the ! before an entry, and reports whether there
// is an odd number of them.
func (c *cursor) negations() bool {
	neg := false
	for c.blank(); c.peek() == '!'; c.blank() {
		neg = !neg
		c.i++
	}
	return neg
}

// word reads a word: up to white space or a byte of stops, with a \
// keeping the byte after it, a double quoted part taken whole without its
// quotes, and a template's {{ expression kept whole. colon, when set,
// says whether a : that would end the word, after the bytes read, is part
// of it.
func (c *cursor) word(stops string, colon func(w []byte) bool) string {
	var w []byte
	for !c.eof() {
		ch := c.peek()
		if ch == ':' && colon != nil && colon(w) {
			w = append(w, ch)
			c.i++
			continue
		}
		if isBlank(ch) || strings.IndexByte(stops, ch) >= 0 {
			break
		}
		w = c.wordByte(w)
	}
	return string(w)
}

// wordByte adds to w the byte at the cursor, or the escape, the double
// quoted part or the template expression it starts.
func (c *cursor) wordByte(w []byte) []byte {
	switch {
	case c.peek() == '\\' && c.i+1 < len(c.s.text):
		c.i += 2
		return append(w, c.s.text[c.i-1])
	case c.peek() == '"':
		return c.quoted(w)
	case bytes.HasPrefix(c.s.text[c.i:], []byte("{{")):
		return c.expression(w)
	}
	c.i++
	return append(w, c.s.text[c.i-1])
}

// quoted adds to w the double quoted part at the cursor without its
// quotes, a \ keeping the byte after it, to its closing quote or the end.
// The cursor stops past the closing quote, or at the end of the text when
// the quote is left open, never past it.
func (c *cursor) quoted(w []byte) []byte {
	for c.i++; !c.eof() && c.peek() != '"'; c.i++ {
		if c.peek() == '\\' && c.i+1 < len(c.s.text) {
			c.i++
		}
		w = append(w, c.peek())
	}
	c.i = min(c.i+1, len(c.s.text))
	return w
}

// expression adds to w the template expression at the cursor, to its }}
// or the end.
func (c *cursor) expression(w []byte) []byte {
	end := len(c.s.text)
	if j := bytes.Index(c.s.text[c.i+2:], []byte("}}")); j >= 0 {
		end = c.i + 2 + j + 2
	}
	w = append(w, c.s.text[c.i:end]...)
	c.i = end
	return w
}

// listStops end an entry of a user, Runas or host list.
const listStops = ",:=()!"

// userColon takes the : of %:group, a non-Unix group.
func userColon(w []byte) bool { return string(w) == "%" }

// hostColon takes the : of an IPv6 address: one after hex digits and
// colons alone, or none.
func hostColon(w []byte) bool {
	for _, b := range w {
		if strings.IndexByte("0123456789abcdefABCDEF:", b) < 0 {
			return false
		}
	}
	return true
}

// member reads one entry of a list of kind.
func (c *cursor) member(kind int) (member, error) {
	neg := c.negations()
	if kind == cmndList {
		return c.command(neg)
	}
	start := c.i
	quoted := c.peek() == '"'
	var colon func([]byte) bool
	switch kind {
	case userList, runasList:
		colon = userColon
	case hostList:
		colon = hostIPv6(c)
	}
	w := c.word(listStops, colon)
	if w == "" {
		return member{}, c.fail("expected a %s", listNames[kind])
	}
	return c.newMember(neg, w, start, !quoted && isAliasName(w))
}

// hostIPv6 is the test of a : in a host word at c: an IPv6 address's,
// when a hex digit or another : follows it.
func hostIPv6(c *cursor) func([]byte) bool {
	return func(w []byte) bool {
		next := byte(0)
		if c.i+1 < len(c.s.text) {
			next = c.s.text[c.i+1]
		}
		return hostColon(w) && hostColon([]byte{next}) && next != 0
	}
}

// newMember is the entry w read from start, with its literal test.
func (c *cursor) newMember(neg bool, w string, start int, isAlias bool) (member, error) {
	at := c.at(start)
	nl, err := c.r.notLiteral(w, at.Line)
	return member{neg: neg, text: w, at: at, alias: isAlias, notLiteral: nl}, err
}

// commandStops end a word of a command.
const commandStops = ",:="

// command reads a command: a digest, then the command and its
// arguments, each word as sudo reads it, joined by one space.
func (c *cursor) command(neg bool) (member, error) {
	c.blank()
	start := c.i
	digest := c.digest()
	if digest != "" {
		if neg {
			return member{}, c.fail("a ! before a digest; sudo reads the ! after it, as sha256:... !/bin/su")
		}
		neg = c.negations()
	}
	words, err := c.commandWords()
	if err != nil {
		return member{}, err
	}
	if len(words) == 0 {
		return member{}, c.fail("expected a command")
	}
	text := strings.Join(words, " ")
	if digest != "" {
		text = digest + " " + text
	}
	return c.newMember(neg, text, start, digest == "" && len(words) == 1 && isAliasName(text))
}

// commandWords reads the words of a command, to a , or a : or the end. A
// command or a first argument that starts with ^ is refused: it is a
// regular expression, which sudo 1.9.10 and later match to its $, past
// blanks, commas and colons, and errata does not read.
func (c *cursor) commandWords() ([]string, error) {
	var words []string
	for c.blank(); !c.eof() && strings.IndexByte(commandStops, c.peek()) < 0; c.blank() {
		if len(words) < 2 && c.peek() == '^' {
			return nil, core.LineError(c.line(), "the command or its first argument starts with ^, a regular expression sudo 1.9.10 and later match, which errata does not read")
		}
		w, err := c.commandWord()
		if err != nil {
			return nil, err
		}
		words = append(words, w)
	}
	return words, nil
}

// globEscapes are the bytes a \ keeps its \ before in a command, as sudo's
// lexer passes them to the glob match, and dropEscapes those it drops the
// \ before; it has no rule for a \ before any other byte.
const (
	globEscapes = "*?[]!^"
	dropEscapes = ":\\,= \t#"
)

// commandWord reads one word of a command as sudo's lexer reads it: a
// double quote is a byte of the word, and a template's {{ expression is
// kept whole.
func (c *cursor) commandWord() (string, error) {
	var w []byte
	for !c.eof() && !isBlank(c.peek()) && strings.IndexByte(commandStops, c.peek()) < 0 {
		var err error
		if w, err = c.commandByte(w); err != nil {
			return "", err
		}
	}
	return string(w), nil
}

// commandByte adds to w the byte at the cursor, or the escape or the
// template expression it starts. A # is refused: the comment scan kept it
// inside double quotes or before a digit, where sudo's lexer ends the
// command.
func (c *cursor) commandByte(w []byte) ([]byte, error) {
	switch {
	case c.peek() == '\\' && c.i+1 < len(c.s.text):
		return c.commandEscape(w)
	case bytes.HasPrefix(c.s.text[c.i:], []byte("{{")):
		return c.expression(w), nil
	case c.peek() == '#':
		return nil, c.fail("a # in a command, inside double quotes or before a digit, where sudo's lexer ends the command")
	}
	c.i++
	return append(w, c.s.text[c.i-1]), nil
}

// commandEscape adds to w the escape at the cursor: the \ and its byte
// before a glob byte, the byte alone before one of dropEscapes. Before
// any other byte it is refused, as sudo's lexer has no rule for it.
func (c *cursor) commandEscape(w []byte) ([]byte, error) {
	next := c.s.text[c.i+1]
	switch {
	case strings.IndexByte(globEscapes, next) >= 0:
		w = append(w, '\\')
	case strings.IndexByte(dropEscapes, next) < 0:
		return nil, c.fail("\\%c in a command, a \\ before a byte sudo's lexer has no rule for", next)
	}
	c.i += 2
	return append(w, next), nil
}

// digest reads a command's digest, such as sha256:abc, or nothing.
func (c *cursor) digest() string {
	save := c.i
	w := c.word(commandStops, nil)
	for _, d := range digests {
		if w == d && c.peek() == ':' {
			c.i++
			c.blank()
			return w + ":" + c.word(",:", nil)
		}
	}
	c.i = save
	return ""
}

// list reads a list of kind: entries joined by commas.
func (c *cursor) list(kind int) ([]member, error) {
	var out []member
	for {
		m, err := c.member(kind)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
		if c.blank(); c.peek() != ',' {
			return out, nil
		}
		c.i++
	}
}

// statement reads s into t: a Defaults line, an alias definition or a
// user specification.
func (t *tree) statement(r *reading, s *stmt) error {
	c := &cursor{s: s, r: r}
	c.blank()
	first := c.word(listStops+"@>", nil)
	if first == "Defaults" {
		return t.defaultsLine(c)
	}
	if kind, ok := aliasKeywords[first]; ok {
		return t.aliasLine(c, kind)
	}
	c.i = 0
	return t.userSpecLine(c)
}

// defaultsLine reads the Defaults line at c: its scope, then its
// settings.
func (t *tree) defaultsLine(c *cursor) error {
	scope := c.scope()
	for {
		p, err := c.param(scope)
		if err != nil {
			return err
		}
		t.noteCase(p)
		if t.keepsParam(p) {
			t.params = append(t.params, p)
		}
		if c.blank(); c.eof() {
			return nil
		}
		if err := c.expect(',', "expected , between the settings of a Defaults line"); err != nil {
			return err
		}
	}
}

// scope reads the scope of a Defaults line: its binding character and its
// list, whose entries are written as the line writes them, joined by
// commas without the white space sudo allows around them and after the
// binding character. It is "" for a line with none.
func (c *cursor) scope() string {
	if strings.IndexByte("@:!>", c.peek()) < 0 {
		return ""
	}
	scope := "Defaults" + string(c.peek())
	for c.i++; ; c.i++ {
		c.blank()
		start := c.i
		c.word(",", nil)
		scope += string(c.s.text[start:c.i])
		if c.blank(); c.peek() != ',' {
			return scope
		}
		scope += ","
	}
}

// param reads one setting of a Defaults line: a flag, true, or with a ! before
// it false; or a value after =, or after += or -= with the operator before
// it.
func (c *cursor) param(scope string) (param, error) {
	neg := c.negations()
	start := c.i
	for !c.eof() && isNameByte(c.peek()) {
		c.i++
	}
	name := string(c.s.text[start:c.i])
	if name == "" {
		return param{}, c.fail("expected a Defaults setting")
	}
	p := param{name: name, text: "true", scope: scope, at: c.at(start)}
	if neg {
		p.text = "false"
	}
	c.blank()
	op := c.operator()
	if op != "" {
		if neg {
			return param{}, c.fail("the setting %s takes a ! or a value, not both", name)
		}
		c.blank()
		p.at = c.at(c.i)
		p.text = strings.TrimPrefix(op, "=") + c.word(",", nil)
	}
	var err error
	p.notLiteral, err = c.r.notLiteral(p.text, p.at.Line)
	return p, err
}

// operator moves past =, += or -=, and returns it, or "".
func (c *cursor) operator() string {
	for _, op := range []string{"=", "+=", "-="} {
		if bytes.HasPrefix(c.s.text[c.i:], []byte(op)) {
			c.i += len(op)
			return op
		}
	}
	return ""
}

// aliasLine reads the definitions of an alias line of kind at c, one or
// more joined by colons.
func (t *tree) aliasLine(c *cursor, kind int) error {
	for {
		c.blank()
		start := c.i
		name := c.word(listStops, nil)
		if !isAliasName(name) {
			return c.fail("%s is no alias name: an upper case letter, then upper case letters, digits and _, and not ALL", core.Quote(name))
		}
		if err := c.expect('=', "expected = after the alias name"); err != nil {
			return err
		}
		members, err := c.list(kind)
		if err != nil {
			return err
		}
		if err := t.define(kind, name, alias{members: members, at: c.at(start)}); err != nil {
			return err
		}
		if c.blank(); c.eof() {
			return nil
		}
		if err := c.expect(':', "expected : between the definitions of an alias line"); err != nil {
			return err
		}
	}
}

// userSpecLine reads the user specification at c: a user list, then one
// or more host lists, each with = and its commands, joined by colons.
func (t *tree) userSpecLine(c *cursor) error {
	users, err := c.list(userList)
	if err != nil {
		return err
	}
	u := userSpec{users: users}
	for {
		p, err := c.priv()
		if err != nil {
			return err
		}
		u.privs = append(u.privs, p)
		if c.blank(); c.eof() {
			break
		}
		if err := c.expect(':', "expected , or : after a command"); err != nil {
			return err
		}
	}
	if t.keepsSpecs {
		t.specs = append(t.specs, u)
	}
	return nil
}

// priv reads a host list, its =, and the commands after it.
func (c *cursor) priv() (priv, error) {
	c.blank()
	hosts, err := c.list(hostList)
	if err != nil {
		return priv{}, err
	}
	if err := c.expect('=', "expected = after the host list"); err != nil {
		return priv{}, err
	}
	p := priv{hosts: hosts}
	prev := cmndSpec{carried: &carried{}}
	for {
		cs, err := c.cmndSpec(prev)
		if err != nil {
			return priv{}, err
		}
		p.cmnds = append(p.cmnds, cs)
		prev = cs
		if c.blank(); c.peek() != ',' {
			return p, nil
		}
		c.i++
	}
}

// cmndSpec reads a command with its Runas lists, options and tags, with
// those prev, the command before it, carries over.
func (c *cursor) cmndSpec(prev cmndSpec) (cmndSpec, error) {
	cs := cmndSpec{carried: prev.carried}
	own := func() {
		if cs.carried == prev.carried {
			next := *prev.carried
			cs.carried = &next
		}
	}
	if c.blank(); c.peek() == '(' {
		users, groups, err := c.runas()
		if err != nil {
			return cmndSpec{}, err
		}
		own()
		cs.runas, cs.groups = users, groups
	}
	if err := c.settings(&cs, own); err != nil {
		return cmndSpec{}, err
	}
	m, err := c.command(c.negations())
	cs.cmd = m
	return cs, err
}

// invokingUser is the Runas user of () and (:groups), which run the
// command as the user who runs sudo.
const invokingUser = "(invoking user)"

// runas reads a Runas spec: (users), (users:groups), or () and (:groups),
// whose user is the invoking user.
func (c *cursor) runas() (users, groups []member, err error) {
	start := c.i
	c.i++
	if c.blank(); c.peek() == ':' || c.peek() == ')' {
		users = []member{{text: invokingUser, at: c.at(start)}}
	} else if users, err = c.list(runasList); err != nil {
		return nil, nil, err
	}
	if c.blank(); c.peek() == ':' {
		c.i++
		if c.blank(); c.peek() != ')' {
			if groups, err = c.list(runasList); err != nil {
				return nil, nil, err
			}
		}
	}
	return users, groups, c.expect(')', "expected ) after the Runas list")
}

// settings reads the options and the tags before a command into cs,
// calling own before it changes what cs carries.
func (c *cursor) settings(cs *cmndSpec, own func()) error {
	var set [len(optionNames)]bool
	for {
		slot, more, err := c.setting(cs, own)
		if err != nil || !more {
			pairOptions(cs, set)
			return err
		}
		if slot >= 0 {
			set[slot] = true
		}
	}
}

// setting reads one option or tag into cs, and returns the option's slot,
// or -1 for a tag, and whether it read one.
func (c *cursor) setting(cs *cmndSpec, own func()) (int, bool, error) {
	c.blank()
	start := c.i
	w := c.word(listStops, nil)
	c.blank()
	slot, set := -1, ""
	switch {
	case w == "":
	case c.peek() == ':':
		slot = tagSlot(w)
		set = w
	case c.peek() == '=':
		slot = optionSlot(w)
	}
	if slot < 0 {
		c.i = start
		return -1, false, nil
	}
	c.i++
	if set == "" {
		c.blank()
		set = w + "=" + c.word(",:", nil)
	}
	at := c.at(start)
	nl, err := c.r.notLiteral(set, at.Line)
	s := setting{text: set, at: at, notLiteral: nl}
	own()
	if set == w {
		cs.tags[slot] = s
		return -1, true, err
	}
	cs.options[slot] = s
	return slot, true, err
}

// optionPairs are the options sudo carries over as a pair: a command that
// sets either takes the other only from its own line.
var optionPairs = [][2]int{{2, 3}, {4, 5}} // ROLE and TYPE, PRIVS and LIMITPRIVS

// pairOptions clears, in cs, the option of a pair that cs carried over
// when its line set the other one.
func pairOptions(cs *cmndSpec, set [len(optionNames)]bool) {
	for _, p := range optionPairs {
		for i, slot := range p {
			if set[p[1-i]] && !set[slot] {
				cs.options[slot] = setting{}
			}
		}
	}
}

// tagSlot is the slot of the tag w in tagPairs, or -1.
func tagSlot(w string) int {
	for i, p := range tagPairs {
		if w == p[0] || w == p[1] {
			return i
		}
	}
	return -1
}

// optionSlot is the slot of the option w in optionNames, or -1.
func optionSlot(w string) int {
	for i, o := range optionNames {
		if w == o {
			return i
		}
	}
	return -1
}
