// Package semver reads versions and version ranges as npm, Cargo, PEP 440,
// Go modules and RubyGems write them, orders versions, tests a version
// against a range, and tests whether ranges overlap. It is the one version
// library errata has: agree's compare: semver and assert's semver helpers
// both call it, so the two never give different answers for the same values.
//
// Every function reads a value of at most MaxLength bytes and makes passes
// linear in its length, so a value from a repository costs time in
// proportion to its size (ADR 0003).
package semver

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxLength is the longest value, in bytes, this package reads; a longer one
// is neither a version nor a range. It is the length the assert helpers
// read. The longest real versions are far shorter: a Go pseudo-version such
// as v0.0.0-20191109021931-daa7c04131f5 is 34 bytes.
const MaxLength = 1024

// num is a whole number written in decimal, with no leading zero but in "0".
// It is kept as its digits, so a number of any length orders correctly
// without overflow.
type num string

// parseNum reads s, which must be digits only.
func parseNum(s string) (num, bool) {
	if s == "" {
		return "", false
	}
	for i := range len(s) {
		if !isDigit(s[i]) {
			return "", false
		}
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		s = "0"
	}
	return num(s), true
}

func cmpNum(a, b num) int {
	if len(a) != len(b) {
		return cmpInt(len(a), len(b))
	}
	return strings.Compare(string(a), string(b))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// inc is n plus one.
func (n num) inc() num {
	d := []byte(n)
	for i := len(d) - 1; i >= 0; i-- {
		if d[i] < '9' {
			d[i]++
			return num(d)
		}
		d[i] = '0'
	}
	return num("1" + string(d))
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// ident is one identifier of a pre-release: a number, or text.
type ident struct {
	numeric bool
	text    string // the number's digits when numeric
}

func newIdent(s string) ident {
	if n, ok := parseNum(s); ok {
		return ident{numeric: true, text: string(n)}
	}
	return ident{text: s}
}

// cmpIdents orders two pre-releases as SemVer 2.0.0 orders them: a number
// below text, numbers by value, text by its bytes, and a list that is a
// prefix of another below it.
func cmpIdents(a, b []ident) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		x, y := a[i], b[i]
		switch {
		case x.numeric && y.numeric:
			if c := cmpNum(num(x.text), num(y.text)); c != 0 {
				return c
			}
		case x.numeric:
			return -1
		case y.numeric:
			return 1
		default:
			if c := strings.Compare(x.text, y.text); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(a), len(b))
}

// Version is one version, as any of the five ecosystems writes it.
type Version struct {
	epoch   num   // PEP 440's N!, 0 when not written
	release []num // the numbers before any suffix, as many as written
	pre     []ident
	post    num
	hasPost bool
	dev     num
	hasDev  bool
	// floor marks a bound below every version of its release, pre-releases
	// and dev releases included. A range's upper bound such as npm's
	// <2.0.0-0 is one; no value parses as one.
	floor bool
	build string // SemVer build metadata or a PEP 440 local version; never compared
}

// Partial reports whether v names its release at less than full precision:
// one or two numbers and no suffix, such as 20 or 20.11. It stands for every
// release that starts with those numbers. Any other version stands for
// itself.
func (v Version) Partial() bool {
	return !v.floor && len(v.release) < 3 && !v.suffixed()
}

// suffixed reports whether v has a pre-release, a post release or a dev
// release.
func (v Version) suffixed() bool { return v.pre != nil || v.hasPost || v.hasDev }

// prerelease reports whether v comes before the release its numbers name: a
// pre-release or a dev release.
func (v Version) prerelease() bool { return v.pre != nil || v.hasDev }

// String is v as this package reads it, in one spelling for every
// ecosystem: the epoch when it is not 0, the numbers, then a pre-release
// after - (after . beside a post or dev release), .postN, .devN and the
// build after +. It reads back as a version equal to v. A floor is written -0 after
// its numbers, as npm writes the bound below every pre-release.
func (v Version) String() string {
	var b strings.Builder
	if v.epoch != "0" && v.epoch != "" {
		b.WriteString(string(v.epoch) + "!")
	}
	for i, n := range v.release {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(string(n))
	}
	if v.floor {
		b.WriteString("-0")
	}
	if v.pre != nil {
		// A pre-release beside a post or dev release came from the PEP 440
		// form, and is written in it, so the string reads back the same.
		sep := byte('-')
		if v.hasPost || v.hasDev {
			sep = '.'
		}
		for _, id := range v.pre {
			b.WriteByte(sep)
			b.WriteString(id.text)
			sep = '.'
		}
	}
	if v.hasPost {
		b.WriteString(".post" + string(v.post))
	}
	if v.hasDev {
		b.WriteString(".dev" + string(v.dev))
	}
	if v.build != "" {
		b.WriteString("+" + v.build)
	}
	return b.String()
}

// Compare orders a and b: -1, 0 or 1. Missing numbers count as 0, so 20 and
// 20.0.0 are equal, and the build is not compared. Within one release, a dev
// release comes first, then pre-releases, the release, and post releases,
// as PEP 440 orders them; SemVer's pre-releases order by SemVer's rules.
func Compare(a, b Version) int {
	if c := cmpRelease(a, b); c != 0 {
		return c
	}
	switch {
	case a.floor && b.floor:
		return 0
	case a.floor:
		return -1
	case b.floor:
		return 1
	}
	if c := cmpPre(a, b); c != 0 {
		return c
	}
	if c := cmpOptional(a.hasPost, a.post, b.hasPost, b.post, -1); c != 0 {
		return c
	}
	return cmpOptional(a.hasDev, a.dev, b.hasDev, b.dev, 1)
}

// cmpRelease orders the epochs, then the numbers, a missing number as 0.
func cmpRelease(a, b Version) int {
	if c := cmpNum(orZero(a.epoch), orZero(b.epoch)); c != 0 {
		return c
	}
	for i := 0; i < len(a.release) || i < len(b.release); i++ {
		if c := cmpNum(component(a, i), component(b, i)); c != 0 {
			return c
		}
	}
	return 0
}

func orZero(n num) num {
	if n == "" {
		return "0"
	}
	return n
}

func component(v Version, i int) num {
	if i < len(v.release) {
		return v.release[i]
	}
	return "0"
}

// preRank places a version's pre-release part as PEP 440 does: a dev
// release with neither pre nor post release first, then a pre-release, then
// none.
func preRank(v Version) int {
	switch {
	case v.pre == nil && !v.hasPost && v.hasDev:
		return -1
	case v.pre == nil:
		return 1
	}
	return 0
}

func cmpPre(a, b Version) int {
	ra, rb := preRank(a), preRank(b)
	if ra != rb {
		return cmpInt(ra, rb)
	}
	if ra != 0 {
		return 0
	}
	return cmpIdents(a.pre, b.pre)
}

// cmpOptional orders two optional numbers; absent is below every number when
// absent is -1 and above every number when it is 1.
func cmpOptional(hasA bool, a num, hasB bool, b num, absent int) int {
	switch {
	case hasA && hasB:
		return cmpNum(a, b)
	case hasA:
		return -absent
	case hasB:
		return absent
	}
	return 0
}

// AgreeAt reports whether a and b agree at the precision each states. A
// partial version agrees with any version whose leading numbers are its own:
// 20 with 20.11.1 and with v20.11, but not with 18.19.0. Two versions that
// are not partial agree when they are equal by Compare.
func AgreeAt(a, b Version) bool {
	if !a.Partial() && !b.Partial() {
		return Compare(a, b) == 0
	}
	if cmpNum(orZero(a.epoch), orZero(b.epoch)) != 0 {
		return false
	}
	n := len(a.release)
	if b.Partial() && (!a.Partial() || len(b.release) < n) {
		n = len(b.release)
	}
	for i := range n {
		if cmpNum(component(a, i), component(b, i)) != 0 {
			return false
		}
	}
	return true
}

// errTooLong is the refusal of a value over MaxLength.
var errTooLong = fmt.Errorf("it is longer than %d bytes, the longest value errata reads as a version", MaxLength)

// ParseVersion reads s as one version. Surrounding white space is ignored.
// It reads:
//
//   - a v prefix (Go modules, and npm) or a go prefix (a Go toolchain such as
//     go1.22.1);
//   - a PEP 440 epoch, such as 1!2.0;
//   - one or more numbers separated by dots;
//   - a SemVer pre-release after -, such as -rc.1 or a Go pseudo-version's
//     -20191109021931-daa7c04131f5, ordered by SemVer's rules;
//   - or a pre-release, post or dev release written as PEP 440 and RubyGems
//     write them, such as rc1, a1, .post2, .dev0, .pre or .beta.2, split
//     into words and numbers;
//   - build metadata or a PEP 440 local version after +, such as
//     +incompatible, which is kept and never compared.
func ParseVersion(s string) (Version, error) {
	if len(s) > MaxLength {
		return Version{}, errTooLong
	}
	rest := strings.TrimSpace(s)
	if rest == "" {
		return Version{}, errors.New("it is empty")
	}
	switch {
	case strings.HasPrefix(rest, "go") && len(rest) > 2 && isDigit(rest[2]):
		rest = rest[2:]
	case strings.HasPrefix(rest, "v") && len(rest) > 1 && isDigit(rest[1]):
		rest = rest[1:]
	}
	v := Version{epoch: "0"}
	if i := strings.IndexByte(rest, '!'); i >= 0 {
		e, ok := parseNum(rest[:i])
		if !ok {
			return Version{}, errors.New("the epoch before ! is not a number")
		}
		v.epoch, rest = e, rest[i+1:]
	}
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		v.build, rest = rest[i+1:], rest[:i]
		if err := checkBuild(v.build); err != nil {
			return Version{}, err
		}
	}
	end := releaseEnd(rest)
	if end == 0 {
		return Version{}, errors.New("it does not start with a number")
	}
	for _, part := range strings.Split(rest[:end], ".") {
		n, _ := parseNum(part) // releaseEnd took digits and dots only
		v.release = append(v.release, n)
	}
	rest = rest[end:]
	switch {
	case rest == "":
		return v, nil
	case rest[0] == '-':
		pre, err := semverPre(rest[1:])
		v.pre = pre
		return v, err
	}
	return v, wordSuffix(&v, rest)
}

// releaseEnd is the length of the numbers at the start of s: digits, each
// dot followed by a digit.
func releaseEnd(s string) int {
	i := 0
	for {
		j := i
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		if j == i {
			return max(0, i-1) // the dot before was not followed by a digit
		}
		if j == len(s) || s[j] != '.' {
			return j
		}
		i = j + 1
	}
}

// checkBuild accepts build metadata: letters, digits, dots, hyphens and
// underscores, not empty.
func checkBuild(b string) error {
	if b == "" {
		return errors.New("nothing follows the +")
	}
	for i := range len(b) {
		if c := b[i]; !isDigit(c) && !isLetter(c) && c != '.' && c != '-' && c != '_' {
			return fmt.Errorf("the build after + holds %q, which a version does not", c)
		}
	}
	return nil
}

// semverPre reads a SemVer pre-release, identifiers of letters, digits and
// hyphens separated by dots.
func semverPre(s string) ([]ident, error) {
	var ids []ident
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return nil, errors.New("the pre-release after - has an empty part")
		}
		for i := range len(part) {
			if c := part[i]; !isDigit(c) && !isLetter(c) && c != '-' {
				return nil, fmt.Errorf("the pre-release after - holds %q, which a version does not", c)
			}
		}
		ids = append(ids, newIdent(part))
	}
	return ids, nil
}

// wordSuffix reads the suffix PEP 440 and RubyGems write after the numbers:
// words and numbers, separated by ., _ or -, or by nothing where a word
// meets a number. A word other than post, rev, r or dev, and the numbers
// after it, are the pre-release (rc1, .beta.2, .pre); post, rev or r and a
// number is a post release, and dev and a number a dev release, each 0 when
// no number follows.
func wordSuffix(v *Version, s string) error {
	tokens, err := splitWords(s)
	if err != nil {
		return err
	}
	i := 0
	for i < len(tokens) && !isPostWord(tokens[i]) && !isDevWord(tokens[i]) {
		if t := tokens[i]; t == "x" || t == "X" {
			return fmt.Errorf("%s stands for a number, as in a range, so this is not one version", t)
		}
		if isDigit(tokens[i][0]) && v.pre == nil {
			return fmt.Errorf("%s follows the numbers, which a version does not allow", quoteToken(tokens[i]))
		}
		if v.pre == nil && !slices.Contains(preWords, strings.ToLower(tokens[i])) {
			// So 10ms, 1st, 1e10 and 20H2 are not versions.
			return fmt.Errorf("%s after the numbers is not a pre-release, post or dev word (%s)", quoteToken(tokens[i]), strings.Join(append(slices.Clip(preWords), "post", "rev", "r", "dev"), ", "))
		}
		v.pre = append(v.pre, newIdent(tokens[i]))
		i++
	}
	if i < len(tokens) && isPostWord(tokens[i]) {
		v.hasPost, v.post, i = true, "0", i+1
		if i < len(tokens) && isDigit(tokens[i][0]) {
			v.post, _ = parseNum(tokens[i])
			i++
		}
	}
	if i < len(tokens) && isDevWord(tokens[i]) {
		v.hasDev, v.dev, i = true, "0", i+1
		if i < len(tokens) && isDigit(tokens[i][0]) {
			v.dev, _ = parseNum(tokens[i])
			i++
		}
	}
	if i < len(tokens) {
		return fmt.Errorf("%s comes after the post or dev release, which a version does not allow", quoteToken(tokens[i]))
	}
	return nil
}

// preWords are the words that may start a pre-release written without a
// -: PEP 440's spellings, and RubyGems' pre and preview. A pre-release
// after - is SemVer's and may hold any word.
var preWords = []string{"a", "alpha", "b", "beta", "c", "rc", "pre", "preview"}

// maxToken is the most bytes of a value an error quotes. A longer part is
// quoted to maxToken bytes and its length named, so an error about a value
// of MaxLength bytes stays short.
const maxToken = 64

// quoteToken is s quoted for an error, clipped to maxToken bytes.
func quoteToken(s string) string {
	if len(s) <= maxToken {
		return strconv.Quote(s)
	}
	end := maxToken
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return fmt.Sprintf("%s... (%d bytes)", strconv.Quote(s[:end]), len(s))
}

// The words are matched without regard to case, as PEP 440 reads them; a
// pre-release keeps its word as written, so it orders as written.
func isPostWord(t string) bool {
	t = strings.ToLower(t)
	return t == "post" || t == "rev" || t == "r"
}

func isDevWord(t string) bool { return strings.EqualFold(t, "dev") }

// splitWords splits s into runs of letters and runs of digits. A separator
// stands between two runs, and nothing else may: none may be doubled, lead a
// run after another separator, or end s.
func splitWords(s string) ([]string, error) {
	var tokens []string
	i := 0
	for i < len(s) {
		if c := s[i]; c == '.' || c == '_' || c == '-' {
			if i+1 == len(s) || !isDigit(s[i+1]) && !isLetter(s[i+1]) {
				return nil, fmt.Errorf("the separator %q after the numbers is not followed by a word or a number", c)
			}
			i++
		}
		j := i
		switch {
		case isDigit(s[i]):
			for j < len(s) && isDigit(s[j]) {
				j++
			}
		case isLetter(s[i]):
			for j < len(s) && isLetter(s[j]) {
				j++
			}
		default:
			return nil, fmt.Errorf("it holds %q, which a version does not", s[i])
		}
		tokens = append(tokens, s[i:j])
		i = j
	}
	return tokens, nil
}

// bump is v's first i+1 numbers with the last of them one higher, and no
// suffix: the first release after every release that starts with v's first
// i+1 numbers.
func bump(v Version, i int) Version {
	release := make([]num, i+1)
	for k := range release {
		release[k] = component(v, k)
	}
	release[i] = release[i].inc()
	return Version{epoch: v.epoch, release: release}
}

// releaseOf is v's epoch and numbers, without a suffix or a build.
func releaseOf(v Version) Version {
	return Version{epoch: v.epoch, release: v.release}
}

// atFloor is v as a floor: below every version of its release.
func atFloor(v Version) Version {
	f := releaseOf(v)
	f.floor = true
	return f
}
