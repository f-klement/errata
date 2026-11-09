package sudoers

import (
	"strings"
	"testing"
)

// Commands errata does not read as sudo does make the rule unknown: a
// command or a first argument that starts with ^, a regular expression
// sudo 1.9.10 and later match, which errata does not read; a \ before a
// byte sudo's lexer has no rule for, which sudo drops the line over; and
// a # inside double quotes, where sudo's lexer ends the command. A \ and
// a ^ later in the arguments read as before. Planted breaks: the ^ test
// left out; a \ before any byte taken; a # in a command word taken.
func TestCommandsNotRead(t *testing.T) {
	runRefusals(t, []refuseCase{
		{"regex argument", map[string]string{"sudoers": "bob ALL = /usr/bin/systemctl ^restart [a-z]+\\.service$\n"}, user("sudoers", "bob"), "starts with ^, a regular expression"},
		{"regex argument with a comma", map[string]string{"sudoers": "bob ALL = /usr/bin/x ^a,b$\n"}, user("sudoers", "bob"), "starts with ^, a regular expression"},
		{"regex argument with a colon", map[string]string{"sudoers": "bob ALL = /usr/bin/x ^a:b$\n"}, user("sudoers", "bob"), "starts with ^, a regular expression"},
		{"regex command", map[string]string{"sudoers": "bob ALL = ^/usr/bin/(ls|cat)$\n"}, user("sudoers", "bob"), "starts with ^, a regular expression"},
		{"backslash before another byte", map[string]string{"sudoers": "bob ALL = /bin/echo \\x\n"}, user("sudoers", "bob"), "\\x in a command"},
		{"# in quotes", map[string]string{"sudoers": "bob ALL = /bin/echo \"a#b\"\n"}, user("sudoers", "bob"), "a # in a command, inside double quotes"},
	})
	runReads(t, []readCase{
		{"later ^ and escapes", map[string]string{"sudoers": "bob ALL = /bin/x a ^b \\: \\#\n"}, user("sudoers", "bob"), "sudoers:1={command=/bin/x a ^b : # host=ALL user=bob}"},
		{"# in quotes elsewhere", map[string]string{"sudoers": "Defaults passprompt=\"a#b\"\n"}, defaults("sudoers", "passprompt"), "sudoers:1=a#b"},
	})
}

// A double quote left open at the end of a statement ends the word there,
// in a scope, a value and a user list, without reading past the text. A
// statement of 32 bytes fills its slice, so a read one byte past it
// panics; a shorter one would read a byte of spare capacity.
// FuzzSudoers found the scope's on the gate of 6274edd8 (seed
// testdata/fuzz/FuzzSudoers/scope-open-quote). Planted break: quoted
// moving past the end of an open quote.
func TestOpenQuoteAtTheEnd(t *testing.T) {
	runRefusals(t, []refuseCase{
		{"in a scope", map[string]string{"sudoers": "Defaults:\"" + strings.Repeat("a", 22)}, defaults("sudoers", "x"), "expected a Defaults setting"},
		{"in a user list", map[string]string{"sudoers": "\"" + strings.Repeat("a", 31)}, user("sudoers", "a"), "expected a host"},
	})
	runReads(t, []readCase{
		{"in a value", map[string]string{"sudoers": "Defaults x=\"" + strings.Repeat("a", 20)}, defaults("sudoers", "x"), "sudoers:1=" + strings.Repeat("a", 20)},
	})
}
