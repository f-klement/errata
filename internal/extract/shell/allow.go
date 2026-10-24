package shell

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// A reference $Y or ${Y} resolves only while every statement before it, in
// the whole file, is one errata knows to change no variable but as it reads
// it. The test is an allowlist: a shell has more ways to change a variable
// than any list of the ways would hold (declare -i, unset "Y", read with no
// name, a nameref, arithmetic in [[ ]], set -A, printf's %n, xtrace's PS4, a
// function), so anything not on the list ends resolution for the rest of
// the file. Two tests make the list. quiet is whether expanding a statement's words changes nothing,
// which lets a reference inside it resolve. keeps is whether running it
// leaves every variable as errata records it, which lets a reference after
// it resolve. The README gives the same list.

// keptCommands are the commands a statement that keeps the variables may
// run, by their name written as plain text. printf, set, export and
// readonly take further tests (keepsCommand).
var keptCommands = map[string]bool{
	"echo": true, "printf": true, "true": true, "false": true, ":": true, "cd": true, "mkdir": true,
	"set": true, "export": true, "readonly": true,
}

// setOptions are the letters set may take, and safeOptions the names set -o
// may take: none changes a variable. xtrace (-x) is not one: before each
// command it expands PS4, which may assign, as PS4='$((Y=5))' does.
const setOptions = "eu"

var safeOptions = map[string]bool{"errexit": true, "nounset": true, "pipefail": true}

// shellOwned are variables the shell sets itself, or keeps from an
// assignment: a value the script gave one is not what a later reference
// reads. They are the ones bash, dash and mksh document as set by the shell,
// read only, or given a new value on each read or command; every name that
// starts with BASH, KSH or COMP_ is one too.
var shellOwned = map[string]bool{
	"RANDOM": true, "SRANDOM": true, "SECONDS": true, "LINENO": true, "EPOCHSECONDS": true,
	"EPOCHREALTIME": true, "_": true, "PPID": true, "UID": true, "EUID": true, "SHELLOPTS": true,
	"GROUPS": true, "FUNCNAME": true, "HISTCMD": true, "DIRSTACK": true, "PIPESTATUS": true,
	"PWD": true, "OLDPWD": true, "COLUMNS": true, "LINES": true, "OPTIND": true, "OPTARG": true,
	"REPLY": true, "MAPFILE": true, "USER_ID": true, "PATHSEP": true, "ERRNO": true,
}

// owned reports whether the shell owns a variable (shellOwned).
func owned(name string) bool {
	return shellOwned[name] || strings.HasPrefix(name, "BASH") || strings.HasPrefix(name, "KSH") ||
		strings.HasPrefix(name, "COMP_")
}

// quiet reports whether expanding a statement's words changes no variable:
// it runs in the foreground, and its words hold only text, quotes, and
// parameters with no operator but a default (${Y:-v}, ${Y-v}, ${Y:+v},
// ${Y+v}). Its redirects hold no here-document, name no descriptor by a
// variable ({fd}>f), and stand on a command that assigns nothing, since
// the shell expands redirects before assignments and the walk reads them
// after.
func quiet(s *syntax.Stmt) bool {
	if s.Negated || s.Background || s.Coprocess || s.Disown {
		return false
	}
	for _, r := range s.Redirs {
		if !quietRedirect(r) {
			return false
		}
	}
	if c, ok := s.Cmd.(*syntax.CallExpr); ok {
		return quietCall(c, len(s.Redirs) == 0)
	}
	if d, ok := s.Cmd.(*syntax.DeclClause); ok {
		return len(s.Redirs) == 0 && quietAssigns(d.Args)
	}
	return false
}

// quietCall reports whether a simple command's words are quiet, and that
// it assigns nothing when it has redirects (bare is false).
func quietCall(c *syntax.CallExpr, bare bool) bool {
	if !bare && (len(c.Assigns) != 0 || posixDecl(c)) {
		return false
	}
	if !quietAssigns(c.Assigns) {
		return false
	}
	for _, a := range c.Args {
		if !quietParts(a.Parts) {
			return false
		}
	}
	return true
}

// posixDecl reports whether a command is one posix reads as a declaration.
func posixDecl(c *syntax.CallExpr) bool {
	if len(c.Args) == 0 {
		return false
	}
	cmd := c.Args[0].Lit()
	return cmd == "export" || cmd == "readonly" || cmd == "local"
}

func quietAssigns(as []*syntax.Assign) bool {
	for _, a := range as {
		if a.Index != nil || a.Array != nil || a.Value != nil && !quietParts(a.Value.Parts) {
			return false
		}
	}
	return true
}

func quietRedirect(r *syntax.Redirect) bool {
	if r.Op == syntax.Hdoc || r.Op == syntax.DashHdoc {
		return false
	}
	if r.N != nil && strings.Trim(r.N.Value, "0123456789") != "" {
		return false
	}
	return quietParts(r.Word.Parts)
}

func quietParts(parts []syntax.WordPart) bool {
	for _, part := range parts {
		if !quietPart(part) {
			return false
		}
	}
	return true
}

func quietPart(part syntax.WordPart) bool {
	if p, ok := part.(*syntax.DblQuoted); ok {
		return quietParts(p.Parts)
	}
	if p, ok := part.(*syntax.ParamExp); ok {
		return quietParam(p)
	}
	_, lit := part.(*syntax.Lit)
	_, sgl := part.(*syntax.SglQuoted)
	return lit || sgl
}

// quietParam reports whether a parameter expansion changes nothing: $Y,
// ${Y}, $1, $@ and the like, alone or with a default that is quiet itself.
// An index or a slice is arithmetic, which can assign.
func quietParam(p *syntax.ParamExp) bool {
	if p.Param == nil || p.Flags != nil || p.Excl || p.Length || p.Width || p.IsSet || p.NestedParam != nil ||
		p.Index != nil || len(p.Modifiers) != 0 || p.Slice != nil || p.Repl != nil || p.Names != 0 {
		return false
	}
	if p.Exp == nil {
		return true
	}
	op := p.Exp.Op
	if op != syntax.DefaultUnset && op != syntax.DefaultUnsetOrNull && op != syntax.AlternateUnset && op != syntax.AlternateUnsetOrNull {
		return false
	}
	return p.Exp.Word == nil || quietParts(p.Exp.Word.Parts)
}

// keeps reports whether a quiet statement leaves every variable as the walk
// records it: assignments alone (Y=v), export or readonly with names and
// values and no option, or a command from keptCommands with no assignment
// before it.
func keeps(s *syntax.Stmt) bool {
	if d, ok := s.Cmd.(*syntax.DeclClause); ok {
		v := d.Variant.Value
		if v != "export" && v != "readonly" {
			return false
		}
		for _, a := range d.Args {
			if a.Name == nil {
				return false
			}
		}
		return true
	}
	c, ok := s.Cmd.(*syntax.CallExpr)
	if !ok {
		return false
	}
	if len(c.Args) == 0 {
		return true
	}
	if len(c.Assigns) != 0 || len(c.Args[0].Parts) != 1 {
		return false
	}
	name, ok := c.Args[0].Parts[0].(*syntax.Lit)
	return ok && keptCommands[name.Value] && keepsCommand(name.Value, c.Args[1:])
}

// keepsCommand tests the arguments of a command from keptCommands: printf
// keeps the variables when its format is plain text that does not start
// with -, so it is no -v, and holds no %, since bash's %n assigns; set when each argument is plain text, options of
// setOptions or -o with a name of safeOptions; export and readonly, which
// posix reads as commands, when each argument is NAME or starts NAME=.
func keepsCommand(cmd string, args []*syntax.Word) bool {
	if cmd == "printf" {
		format, ok := fixedText(args)
		return ok && !strings.HasPrefix(format, "-") && !strings.Contains(format, "%")
	}
	if cmd == "set" {
		return keepsSet(args)
	}
	if cmd == "export" || cmd == "readonly" {
		for _, a := range args {
			if !nameArgument(a) {
				return false
			}
		}
	}
	return true
}

// keepsSet reports whether set's arguments are only safe options.
func keepsSet(args []*syntax.Word) bool {
	opts := make([]string, len(args))
	for i := range args {
		opt, ok := fixedText(args[i:])
		if !ok {
			return false
		}
		opts[i] = opt
	}
	return keepsOptions(opts)
}

// keepsOptions reports whether opts are only options of setOptions after -
// or +, and -o or +o with a name of safeOptions.
func keepsOptions(opts []string) bool {
	for len(opts) != 0 {
		opt := opts[0]
		opts = opts[1:]
		if len(opt) < 2 || !strings.ContainsRune("-+", rune(opt[0])) {
			return false
		}
		for _, c := range opt[1:] {
			if c == 'o' {
				if len(opts) == 0 || !safeOptions[opts[0]] {
					return false
				}
				opts = opts[1:]
				continue
			}
			if !strings.ContainsRune(setOptions, c) {
				return false
			}
		}
	}
	return true
}

// shebangKeeps reports whether the options a #! first line passes the shell
// keep the variables, as set's must (keepsOptions): #!/bin/bash -x turns on
// xtrace for the whole file. A first argument that is no option is the
// program env or busybox runs, and is passed over.
func shebangKeeps(data []byte) bool {
	fields, _ := shebangFields(data)
	if len(fields) < 2 {
		return true
	}
	args := fields[1:]
	if !strings.ContainsRune("-+", rune(args[0][0])) {
		args = args[1:]
	}
	return keepsOptions(args)
}

// fixedText is the text of the first word of words when the shell gives it
// as written: plain text with no backslash, and quotes that hold such text.
func fixedText(words []*syntax.Word) (string, bool) {
	if len(words) == 0 {
		return "", false
	}
	var b strings.Builder
	if !fixedParts(&b, words[0].Parts) {
		return "", false
	}
	return b.String(), true
}

func fixedParts(b *strings.Builder, parts []syntax.WordPart) bool {
	for _, part := range parts {
		if p, ok := part.(*syntax.DblQuoted); ok && !p.Dollar {
			if !fixedParts(b, p.Parts) {
				return false
			}
			continue
		}
		if p, ok := part.(*syntax.SglQuoted); ok && !p.Dollar {
			b.WriteString(p.Value)
			continue
		}
		p, ok := part.(*syntax.Lit)
		if !ok || strings.Contains(p.Value, `\`) {
			return false
		}
		b.WriteString(p.Value)
	}
	return true
}

// nameArgument reports whether an argument of posix export or readonly
// starts with a plain name: NAME alone, or a first part NAME=.
func nameArgument(word *syntax.Word) bool {
	first, ok := word.Parts[0].(*syntax.Lit)
	if !ok {
		return false
	}
	name, _, found := strings.Cut(first.Value, "=")
	return isName(name) && (found || len(word.Parts) == 1)
}
