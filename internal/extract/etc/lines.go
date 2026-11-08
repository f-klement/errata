package etc

import (
	"bytes"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// entry is one keyword and value a file sets, with the block it is written
// under. The slices are valid only while the visit runs.
type entry struct {
	key   []byte // the keyword, in the form the dialect compares it
	value []byte
	block []byte // the header of the block, as the file writes it; nil outside one
	kind  []byte // limits: the type, soft, hard or -
	line  int    // where the keyword is
	// header marks the line that opens a block, an OpenSSH Match or Host
	// line, whose block is the entry's block: the loader notes where it is.
	header bool
}

// eachLine calls f with each line of data, without its line end, and its
// number, charging m for each. Lines end where core says they do: at a LF,
// a CRLF and a lone CR.
func eachLine(data []byte, m *core.Meter, f func(n int, line []byte) error) error {
	for off, n := 0, 1; off < len(data); n++ {
		next := core.NextLine(data, off)
		if err := m.Charge(core.LineWork + next - off); err != nil {
			return err
		}
		line := data[off:next]
		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if err := f(n, line); err != nil {
			return err
		}
		off = next
	}
	return nil
}

// trim is b without the spaces and tabs around it.
func trim(b []byte) []byte { return bytes.Trim(b, " \t") }

// isSpace reports a space or a tab, the white space between the words of
// every dialect's line.
func isSpace(c byte) bool { return c == ' ' || c == '\t' }

// word splits b, which starts with no white space, at its first space or
// tab: the word, and the rest with the white space before it taken off.
func word(b []byte) (w, rest []byte) {
	i := bytes.IndexAny(b, " \t")
	if i < 0 {
		return b, nil
	}
	return b[:i], trim(b[i:])
}

// cutAt is b up to the first c, or b.
func cutAt(b []byte, c byte) []byte {
	if i := bytes.IndexByte(b, c); i >= 0 {
		return b[:i]
	}
	return b
}

// lower is b in lower case, ASCII only, in buf.
func lower(buf, b []byte) []byte {
	buf = append(buf[:0], b...)
	for i, c := range buf {
		if 'A' <= c && c <= 'Z' {
			buf[i] = c + 'a' - 'A'
		}
	}
	return buf
}

// The OpenSSH dialects, sshd for sshd_config and ssh for ssh_config, as
// readconf.c and servconf.c read a line: white space around it is taken
// off, an empty line and one that starts with # are skipped, the keyword
// runs to white space or =, and an = with white space around it may stand
// between keyword and arguments. A # that starts a word outside double
// quotes ends the line. Keywords are matched in any case. Match, and for ssh
// Host, open a block that runs to the next one or to the end of the file.

var sshdDialect = &dialect{
	name:      "sshd",
	names:     func(dirs []string, base string) bool { return base == "sshd_config" || inDir(dirs, "sshd_config.d") },
	read:      readOpenSSH("match"),
	foldKey:   strings.ToLower,
	checkKey:  oneWord,
	wants:     sameKey,
	foldBlock: foldOpenSSHBlock,
	includes:  map[string]includeKind{"include": includeGlob},
	homes:     homeOf("/etc/ssh"),
	criteria:  []string{"address", "group", "host", "localaddress", "localport", "rdomain", "user"},
	// The keywords whose lines append rather than the first winning, as
	// servconf.c and, for ssh below, readconf.c read them in
	// openssh-portable master of 2026-11-07, which the wave's review
	// checked them against.
	many:       manyOf("port", "listenaddress", "hostkey", "hostcertificate", "acceptenv", "subsystem", "allowusers", "denyusers", "allowgroups", "denygroups"),
	matchFirst: true,
	expands:    true,
	notLiteral: expandsOpenSSH,
}

var sshDialect = &dialect{
	name: "ssh",
	names: func(dirs []string, base string) bool {
		return base == "ssh_config" || inDir(dirs, "ssh_config.d") || base == "config" && inDir(dirs, ".ssh")
	},
	read:      readOpenSSH("match", "host"),
	foldKey:   strings.ToLower,
	checkKey:  oneWord,
	wants:     sameKey,
	foldBlock: foldOpenSSHBlock,
	includes:  map[string]includeKind{"include": includeGlob},
	homes:     sshHomes,
	criteria:  []string{"host", "localuser", "originalhost", "tagged", "user"},
	// readconf.c's match_cfg_line matches user against the User, host
	// against the Hostname and tagged against the Tag a line before the
	// Match line set, rather than the connection's own.
	shadows:    map[string]string{"user": "user", "hostname": "host", "tag": "tagged"},
	many:       manyOf("identityfile", "certificatefile", "localforward", "remoteforward", "dynamicforward", "sendenv"),
	expands:    true,
	notLiteral: expandsOpenSSH,
}

// manyOf is the test of a keyword among keys.
func manyOf(keys ...string) func(string) bool {
	return func(k string) bool { return slices.Contains(keys, k) }
}

// foldOpenSSHBlock is a Match or Host line in the form a block: compares
// it: its words joined by one space, the keyword written Match or Host.
func foldOpenSSHBlock(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	switch strings.ToLower(f[0]) {
	case "match":
		f[0] = "Match"
	case "host":
		f[0] = "Host"
	}
	return strings.Join(f, " ")
}

// expandsOpenSSH reports a value OpenSSH expands before it uses it: a %
// token other than %%, a ${} variable, or a ~ for the home directory at
// its start.
func expandsOpenSSH(e *entry) bool {
	v := e.value
	if bytes.HasPrefix(v, []byte("~")) || bytes.Contains(v, []byte("${")) {
		return true
	}
	for i := 0; i < len(v)-1; i++ {
		if v[i] == '%' {
			if v[i+1] != '%' {
				return true
			}
			i++
		}
	}
	return false
}

// readOpenSSH is the reader of an OpenSSH file whose blocks the keywords
// in blocks open.
func readOpenSSH(blocks ...string) func([]byte, *core.Meter, func(*entry) error) error {
	return func(data []byte, m *core.Meter, visit func(*entry) error) error {
		var e entry
		var block, key []byte
		return eachLine(data, m, func(n int, line []byte) error {
			s := bytes.Trim(line, " \t\f")
			if len(s) == 0 || s[0] == '#' {
				return nil
			}
			kw, rest := s, []byte(nil)
			if i := bytes.IndexAny(s, " \t="); i >= 0 {
				kw, rest = s[:i], trim(s[i:])
				if len(rest) > 0 && rest[0] == '=' {
					rest = trim(rest[1:])
				}
			}
			value := cutOpenSSHComment(rest)
			key = lower(key, kw)
			if slices.Contains(blocks, string(key)) {
				block = append(append(append(block[:0], kw...), ' '), value...)
				e = entry{key: key, block: block, line: n, header: true}
				return visit(&e)
			}
			e = entry{key: key, value: value, line: n}
			if block != nil {
				e.block = block
			}
			return visit(&e)
		})
	}
}

// cutOpenSSHComment is the arguments b up to a # that starts a word
// outside quotes, as argv_split ends a line, with the white space before it
// taken off.
func cutOpenSSHComment(b []byte) []byte {
	var q argv
	for i := 0; i < len(b); i++ {
		if b[i] == '#' && q.quote == 0 && !q.inWord {
			return trim(b[:i])
		}
		next := -1
		if i+1 < len(b) {
			next = int(b[i+1])
		}
		i += q.step(b[i], next)
	}
	return b
}

// argv is where argv_split is in a line: inside a quote, " or ', or not,
// and inside a word or between words.
type argv struct {
	quote  byte
	inWord bool
}

// step reads c, which next follows, and returns how many bytes after it it
// takes too: one for a \ that escapes next, as argv_split drops a \ only
// before a quote, a \ and, outside quotes, a space. Before any other byte
// the \ is a byte of the word.
func (q *argv) step(c byte, next int) int {
	switch {
	case c == '\\' && q.escapes(next):
		q.inWord = true
		return 1
	case q.quote != 0:
		if c == q.quote {
			q.quote = 0
		}
	case c == '"' || c == '\'':
		q.quote, q.inWord = c, true
	default:
		q.inWord = !isSpace(c)
	}
	return 0
}

// escapes reports whether a \ before next, -1 at the end of the line,
// escapes it.
func (q *argv) escapes(next int) bool {
	return next == '"' || next == '\'' || next == '\\' || next == ' ' && q.quote == 0
}

// The sysctl dialect, for sysctl.conf and the files of sysctl.d, as
// procps's sysctl --system and systemd-sysctl read them: a line that
// starts with # or ; is a comment, the key runs to the =, a - before it
// only asks to ignore an error, and white space around key and value is
// taken off. A key whose first separator is / is read with every / and .
// swapped, so net/ipv4/ip_forward is net.ipv4.ip_forward.
var sysctlDialect = &dialect{
	name:  "sysctl",
	names: func(dirs []string, base string) bool { return base == "sysctl.conf" || inDir(dirs, "sysctl.d") },
	read:  readSysctl,
	foldKey: func(s string) string {
		return string(foldSysctl(nil, []byte(strings.TrimPrefix(strings.TrimSpace(s), "-"))))
	},
	checkKey: oneWord,
	wants:    sameKey,
	set:      true,
	order:    sysctlOrder,
}

func readSysctl(data []byte, m *core.Meter, visit func(*entry) error) error {
	var e entry
	var key []byte
	return eachLine(data, m, func(n int, line []byte) error {
		s := trim(line)
		if len(s) == 0 || s[0] == '#' || s[0] == ';' {
			return nil
		}
		i := bytes.IndexByte(s, '=')
		if i < 0 {
			return nil // sysctl warns and passes over a line with no =
		}
		k := trim(s[:i])
		if len(k) > 0 && k[0] == '-' {
			k = trim(k[1:])
		}
		key = foldSysctl(key, k)
		e = entry{key: key, value: trim(s[i+1:]), line: n}
		return visit(&e)
	})
}

// foldSysctl is k, in buf, with / and . swapped when / comes first.
func foldSysctl(buf, k []byte) []byte {
	buf = append(buf[:0], k...)
	if i := bytes.IndexAny(buf, "./"); i < 0 || buf[i] == '.' {
		return buf
	}
	for i, c := range buf {
		switch c {
		case '/':
			buf[i] = '.'
		case '.':
			buf[i] = '/'
		}
	}
	return buf
}

// sysctlOrder is the order sysctl --system reads files in: the drop-ins
// (dropIns), then sysctl.conf.
func sysctlOrder(files []string) []string {
	out := dropIns(files)
	if i := slices.IndexFunc(out, func(f string) bool { return baseName(f) == "sysctl.conf" }); i >= 0 {
		out = append(append(out[:i:i], out[i+1:]...), out[i])
	}
	return out
}

// dropIns is files in the order sysctl, kmod and pam_limits read their
// drop-in directories: only the .conf files of a directory whose name ends
// in .d, by base name, a name two files share read once, from the
// directory the tool ranks first (dirRank), so a file in /etc masks the one
// of its name in /usr/lib whatever the order files: lists them. Two files
// of one name in directories the tool does not rank are taken in the order
// listed.
func dropIns(files []string) []string {
	kept := slices.DeleteFunc(slices.Clone(files), notConf)
	slices.SortStableFunc(kept, func(a, b string) int { return dirRank(a) - dirRank(b) })
	return core.FirstOfEachName(kept)
}

// notConf reports a file of a directory whose name ends in .d that the
// tools pass over: one that is no .conf file, or whose name starts with a
// ., as kmod, procps and pam_limits pass over hidden files.
func notConf(f string) bool {
	dirs, base := core.FileParts(core.Untemplated(f))
	return strings.HasSuffix(dirs[len(dirs)-1], ".d") && (!strings.HasSuffix(base, ".conf") || strings.HasPrefix(base, "."))
}

// dirRanks are the directories, by the path above the drop-in directory
// (and above security for pam_limits), in the order the tools rank them:
// /etc, /run, /usr/local/lib, /usr/lib, /lib, then pam_limits' vendor
// /usr/etc. The longer of two that end alike is tried first.
var dirRanks = []struct {
	dir  string
	rank int
}{{"usr/local/lib", 2}, {"usr/lib", 3}, {"usr/etc", 5}, {"etc", 0}, {"run", 1}, {"lib", 4}}

// dirRank is the rank of the directory f is in, len(dirRanks) for one the
// tools do not rank.
func dirRank(f string) int {
	dirs, _ := core.FileParts(f)
	above := strings.TrimSuffix(path.Dir(strings.Join(dirs, "/")), "/security")
	for _, r := range dirRanks {
		if above == r.dir || strings.HasSuffix(above, "/"+r.dir) {
			return r.rank
		}
	}
	return len(dirRanks)
}

func baseName(f string) string { _, b := core.FileParts(f); return b }

// The resolv dialect, for resolv.conf, as glibc's resolver reads it: a
// line that starts with ; or # is a comment, and a keyword counts only at
// the start of a line with a space or a tab after it. A nameserver is its
// first word, cut at ; or # too, and a domain its first word; search,
// options and sortlist are the rest of the line.
var resolvDialect = &dialect{
	name:    "resolv",
	names:   func(_ []string, base string) bool { return base == "resolv.conf" },
	read:    readResolv,
	foldKey: exact,
	checkKey: func(k string) error {
		if !slices.Contains(resolvKeys, k) {
			return fmt.Errorf("key %s is no keyword glibc reads; write one of %s", core.Quote(k), strings.Join(resolvKeys, ", "))
		}
		return nil
	},
	wants: sameKey,
}

var resolvKeys = []string{"nameserver", "domain", "search", "options", "sortlist"}

func readResolv(data []byte, m *core.Meter, visit func(*entry) error) error {
	var e entry
	return eachLine(data, m, func(n int, line []byte) error {
		i := bytes.IndexAny(line, " \t")
		if i <= 0 || !slices.Contains(resolvKeys, string(line[:i])) {
			return nil
		}
		key, v := line[:i], trim(line[i:])
		switch string(key) {
		case "nameserver":
			if j := bytes.IndexAny(v, ";# \t"); j >= 0 {
				v = v[:j]
			}
		case "domain":
			v, _ = word(v)
		}
		e = entry{key: key, value: v, line: n}
		return visit(&e)
	})
}

// The limits dialect, for limits.conf and the files of limits.d, as
// pam_limits reads them: a # ends the line, and a line of four words or
// more is a domain, a type, an item and a value, the rest of the line. The
// type, the item and the value are read in lower case, as pam_limits
// lowers them. The entry's key is the item, its block the domain.
var limitsDialect = &dialect{
	name:    "limits",
	names:   func(dirs []string, base string) bool { return base == "limits.conf" || inDir(dirs, "limits.d") },
	read:    readLimits,
	foldKey: func(s string) string { return strings.ToLower(fields(s)) },
	checkKey: func(k string) error {
		f := strings.Fields(k)
		if len(f) > 2 || len(f) == 2 && f[0] != "soft" && f[0] != "hard" && f[0] != "-" {
			return fmt.Errorf("key %s is no item, and no type of soft, hard or - and an item", core.Quote(k))
		}
		return nil
	},
	wants: func(e *entry, key string) bool {
		typ, item, two := strings.Cut(key, " ")
		if !two {
			return string(e.key) == key
		}
		return string(e.key) == item && (string(e.kind) == typ || typ != "-" && string(e.kind) == "-")
	},
	foldBlock: exact,
	set:       true,
	order:     limitsOrder,
}

func readLimits(data []byte, m *core.Meter, visit func(*entry) error) error {
	var e entry
	var low []byte
	return eachLine(data, m, func(n int, line []byte) error {
		domain, rest := word(trim(cutAt(line, '#')))
		low = lower(low, rest)
		typ, rest := word(low)
		item, value := word(rest)
		if len(value) == 0 {
			return nil
		}
		e = entry{block: domain, kind: typ, key: item, value: value, line: n}
		return visit(&e)
	})
}

// limitsOrder is the order pam_limits reads files in: limits.conf, then
// the drop-ins (dropIns).
func limitsOrder(files []string) []string {
	isMain := func(f string) bool { return baseName(f) == "limits.conf" }
	main := slices.DeleteFunc(slices.Clone(files), func(f string) bool { return !isMain(f) })
	return append(main, dropIns(slices.DeleteFunc(slices.Clone(files), isMain))...)
}

// The logrotate dialect, for logrotate.conf and the files of logrotate.d,
// as logrotate reads them: a line that starts with # is a comment, a line
// that ends with { opens a block for the paths before it and a } closes it,
// the lines of a script from prerotate, postrotate, firstaction,
// lastaction or preremove to endscript are no directives, and include
// reads a file, or every file of a directory, in place.
var logrotateDialect = &dialect{
	name:      "logrotate",
	names:     func(dirs []string, base string) bool { return base == "logrotate.conf" || inDir(dirs, "logrotate.d") },
	read:      readLogrotate,
	foldKey:   exact,
	checkKey:  oneWord,
	wants:     sameKey,
	foldBlock: fields,
	includes:  map[string]includeKind{"include": includePath},
	homes:     homeOf("/etc"),
}

var logrotateScripts = []string{"prerotate", "postrotate", "firstaction", "lastaction", "preremove"}

func readLogrotate(data []byte, m *core.Meter, visit func(*entry) error) error {
	var e entry
	var block []byte
	script := false
	return eachLine(data, m, func(n int, line []byte) error {
		s := trim(line)
		switch {
		case script:
			script = string(s) != "endscript"
			return nil
		case len(s) == 0 || s[0] == '#':
			return nil
		case s[len(s)-1] == '{':
			block = append(block[:0], trim(s[:len(s)-1])...)
			return nil
		case string(s) == "}":
			block = nil
			return nil
		}
		kw, rest := word(s)
		if slices.Contains(logrotateScripts, string(kw)) {
			script = true
			return nil
		}
		e = entry{key: kw, value: rest, block: block, line: n}
		return visit(&e)
	})
}

// The nsswitch dialect, for nsswitch.conf, as glibc reads it: a # ends the
// line, and a database's name runs to the :, its sources and actions after.
var nsswitchDialect = &dialect{
	name:     "nsswitch",
	names:    func(_ []string, base string) bool { return base == "nsswitch.conf" },
	read:     readNsswitch,
	foldKey:  exact,
	checkKey: oneWord,
	wants:    sameKey,
}

func readNsswitch(data []byte, m *core.Meter, visit func(*entry) error) error {
	var e entry
	return eachLine(data, m, func(n int, line []byte) error {
		s := trim(cutAt(line, '#'))
		i := bytes.IndexByte(s, ':')
		if i <= 0 {
			return nil
		}
		e = entry{key: trim(s[:i]), value: trim(s[i+1:]), line: n}
		return visit(&e)
	})
}

// The modprobe dialect, for the files of modprobe.d, as kmod reads them: a
// \ at the end of a line joins the next to it, a line that starts with # is
// a comment, and the words are split at spaces and tabs. options, install,
// remove, softdep and weakdep name a module, the entry's block, with every
// - read as _, and their value is the rest of the line; the value of
// blacklist and mask is its module, and of alias the rest of the line.
var modprobeDialect = &dialect{
	name:    "modprobe",
	names:   func(dirs []string, base string) bool { return inDir(dirs, "modprobe.d") || base == "modprobe.conf" },
	read:    readModprobe,
	foldKey: exact,
	checkKey: func(k string) error {
		if !slices.Contains(modprobeKeys, k) {
			return fmt.Errorf("key %s is no command kmod reads; write one of %s", core.Quote(k), strings.Join(modprobeKeys, ", "))
		}
		return nil
	},
	wants:     sameKey,
	foldBlock: func(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "-", "_") },
	set:       true,
	order:     dropIns,
}

var modprobeKeys = []string{"alias", "blacklist", "mask", "options", "install", "remove", "softdep", "weakdep"}

func readModprobe(data []byte, m *core.Meter, visit func(*entry) error) error {
	r := modprobeReader{visit: visit}
	err := eachLine(data, m, r.line)
	if err == nil && r.pending {
		err = r.directive() // a \ on the last line joins nothing
	}
	return err
}

// modprobeReader joins the lines a \ continues, and visits the directive
// they make.
type modprobeReader struct {
	visit         func(*entry) error
	e             entry
	joined, block []byte
	start         int
	pending       bool // the line before ended with a \
}

// line takes line n: it joins it to a line a \ continued, and visits the
// directive once no \ continues it.
func (r *modprobeReader) line(n int, line []byte) error {
	if !r.pending {
		r.start, r.joined = n, r.joined[:0]
	}
	r.joined = append(r.joined, line...)
	if len(r.joined) > 0 && r.joined[len(r.joined)-1] == '\\' {
		r.joined, r.pending = r.joined[:len(r.joined)-1], true
		return nil
	}
	return r.directive()
}

// directive visits the directive the joined lines make, if they make one.
func (r *modprobeReader) directive() error {
	r.pending = false
	s := trim(r.joined)
	if len(s) == 0 || s[0] == '#' {
		return nil
	}
	kw, rest := word(s)
	r.e = entry{key: kw, value: rest, line: r.start}
	switch string(kw) {
	case "options", "install", "remove", "softdep", "weakdep":
		mod, v := word(rest)
		r.block = append(r.block[:0], mod...)
		for i, c := range r.block {
			if c == '-' {
				r.block[i] = '_'
			}
		}
		r.e.block, r.e.value = r.block, v
	case "blacklist", "mask":
		r.e.value, _ = word(rest)
	}
	return r.visit(&r.e)
}

// The chrony dialect, for chrony.conf, as chronyd reads it: a line that
// starts with #, %, ! or ; is a comment, runs of white space are one space,
// keywords are matched in any case, include reads the files a glob
// matches in place, and confdir and sourcedir the .conf and .sources files
// of their directories, by name across them.
var chronyDialect = &dialect{
	name: "chrony",
	names: func(dirs []string, base string) bool {
		return base == "chrony.conf" || len(dirs) > 1 && dirs[len(dirs)-2] == "chrony" && (inDir(dirs, "conf.d") || inDir(dirs, "sources.d"))
	},
	read:     readChrony,
	foldKey:  strings.ToLower,
	checkKey: oneWord,
	wants:    sameKey,
	includes: map[string]includeKind{"include": includeGlob, "confdir": includeConfDir, "sourcedir": includeSourceDir},
	homes:    chronyHomes,
	// chronyd refuses to start when an include with no glob names a file
	// that is not there.
	mustInclude: true,
}

// chronySources are the directives a sourcedir's files may hold.
var chronySources = []string{"server", "pool", "peer"}

func readChrony(data []byte, m *core.Meter, visit func(*entry) error) error {
	var e entry
	var key, value []byte
	return eachLine(data, m, func(n int, line []byte) error {
		s := trim(line)
		if len(s) == 0 || bytes.IndexByte([]byte("#%!;"), s[0]) >= 0 {
			return nil
		}
		kw, rest := word(s)
		key = lower(key, kw)
		value = value[:0]
		for i, w := range bytes.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' }) {
			if i > 0 {
				value = append(value, ' ')
			}
			value = append(value, w...)
		}
		e = entry{key: key, value: value, line: n}
		return visit(&e)
	})
}

// The ntp dialect, for ntp.conf, as ntpd reads it: a # ends the line, the
// keyword is the first word, and includefile reads a file in place.
var ntpDialect = &dialect{
	name:     "ntp",
	names:    func(_ []string, base string) bool { return base == "ntp.conf" },
	read:     readNTP,
	foldKey:  exact,
	checkKey: oneWord,
	wants:    sameKey,
	includes: map[string]includeKind{"includefile": includePath},
	homes:    homeOf("/etc"),
}

func readNTP(data []byte, m *core.Meter, visit func(*entry) error) error {
	var e entry
	return eachLine(data, m, func(n int, line []byte) error {
		s := trim(cutAt(line, '#'))
		if len(s) == 0 {
			return nil
		}
		kw, rest := word(s)
		e = entry{key: kw, value: rest, line: n}
		return visit(&e)
	})
}
