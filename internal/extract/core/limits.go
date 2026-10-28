package core

import (
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// The limits that keep a hostile or merely large file from stalling or
// killing a run. errata enforces bounds on what it reads, not on the memory
// a read takes: the file size, the nodes a file can hold, the nodes YAML
// aliases add, the values an extractor returns, the depth, a regex
// pattern's capture groups, and the regex time. Peak memory depends on the
// file's shape within those bounds; the densest shapes measured are under
// format, as examples, not as a ceiling. Every pass errata makes over a file
// but the regex search is linear in its size; the regex search costs the
// file times the threads its pattern keeps alive, and the time limit stops
// it. Each limit fails closed: a file over a limit makes the rule unknown,
// with a message that names the limit, the file, and what to do. The
// measurements are in the README's extractor section.
const (
	// DefaultMaxBytes is the largest file an extractor reads unless it sets
	// max_bytes:, checked before reading. One cap serves every kind. An
	// ordinary file costs about 27 times its size in memory: an OpenAPI
	// document of 8.2 MB takes 0.95 s and 223 MB. A denser file meets the
	// node limit, which scales with max_bytes: see format.
	DefaultMaxBytes = 32 << 20

	// MaxMaxBytes is the largest max_bytes that loads. The node limit
	// scales with max_bytes, and so does the memory a dense file can take:
	// the densest file measured at the default peaks at 2.55 GB, so at
	// 64 MiB one may take about 5 GB, under the 7 GB of a hosted CI runner.
	// A shell script's parser holds one statement whole, so the shell reader
	// refuses a statement over an eighth of max_bytes, and releases each
	// statement's syntax once it has read it: the densest script measured at
	// the default, eight groups of a;a;a each at the limit, peaks at 1.43 GB,
	// and one pipeline of a|a|a is refused at the limit with 0.88 GB. At
	// 64 MiB each statement may be twice as large.
	// GitHub warns about a pushed file over 50 MiB. With the ceiling, no sum
	// of a limit and a byte count can overflow.
	MaxMaxBytes = 64 << 20

	// MaxValues is the most values one extractor returns, and the most nodes
	// one step of a path may select. The values also hold at most the
	// extractor's max_bytes in all (source.values), which only YAML aliases
	// and merge keys, or decoding that lengthens a value, could pass. Every value is kept and may be
	// printed; 100000 regex matches in 4.3 MB take 0.8 s and 45 MB.
	MaxValues = 100_000

	// MaxDepth is how deeply a file may nest: each key, including each part
	// of a dotted TOML key or table header, and each element counts one
	// level. The deepest of 56 real files measured, Helm indexes, Kubernetes
	// manifests, Google API documents, uv.lock and Cargo.lock files, nests
	// 25 levels.
	MaxDepth = 1000

	// MaxExpanded is the most nodes YAML aliases and merge keys may add to
	// a file, counted as they expand: each alias, a merge key's too, adds
	// the nodes of its anchor. A merge key also counts each key it merges,
	// and an alias used as a key counts one node and its anchor's text, a
	// node for every 64 bytes (mergeKeyBytes), since the mapping hashes
	// them. At the limit, 5000 mappings that merge one of 99 keys take
	// 0.15 s and 51 MB, and a chain of 816 mappings that each merge the one
	// before 0.15 s and 45 MB.
	MaxExpanded = 1_000_000

	// regexTimeLimit bounds the matching of one regex extractor whose file
	// may be up to the default size; a larger max_bytes raises it in
	// proportion (regexTime). The search reads the file through a clock,
	// so one search is stopped too. A pattern whose optional tail scans the
	// rest of the line searches again after every match, which is
	// quadratic in the line: 40 KB of one line took 27 s. One search costs
	// the file times the threads the pattern keeps alive: ([0-9a-f]{40})
	// over 32 MiB of hex words took 28 s, and ([ab]{0,1000}c) over 1 MiB
	// of b 62 s; both are refused at 10 s now. The slowest ordinary pattern
	// measured over 32 MiB, ([A-Za-z0-9+/]{64,}={0,2}), takes 4.7 s.
	regexTimeLimit = 10 * time.Second

	// maxFileName is the longest file an extractor may name, in bytes:
	// PATH_MAX on Linux, the longest path its kernel opens. Every message
	// about a file names it whole, since it says where the problem is, and
	// the limit bounds those messages.
	maxFileName = 4096

	// MaxGroups is the most capture groups a regex pattern may have. The
	// extractor returns group 1, and a search tracks every group for every
	// thread it keeps alive. 100000 matches of a pattern of 32 groups take
	// 0.5 s and 29 MB.
	MaxGroups = 32
)

// regexTime is the time limit of a regex extractor that reads at most
// limit bytes: regexTimeLimit up to the default size, and in proportion to
// the limit above it, since a search costs time in proportion to the file.
func regexTime(limit int64) time.Duration {
	if limit <= DefaultMaxBytes {
		return regexTimeLimit
	}
	return regexTimeLimit * time.Duration(limit) / DefaultMaxBytes
}

// OtherWay is what a message says to do about a file the yaml, json and toml
// extractors will not read.
const OtherWay = "a regex extractor can still read the file"

// TooManyValues refuses an extractor of file that selects more than
// MaxValues values.
func TooManyValues(file string) error {
	return fmt.Errorf("%s: the extractor selects more than %d values, the limit; narrow the path or the pattern so it selects fewer", file, MaxValues)
}

// TooDeep is the depth refusal of every format, at the line that crosses it.
func TooDeep(line int) error {
	return fmt.Errorf("line %d: nested more than %d levels deep, the limit; %s", line, MaxDepth, OtherWay)
}

func tooLarge(file string, size, limit int64) error {
	return tooLargeError(fmt.Sprintf("read %s: the file is %d bytes, over the limit of %d bytes; set max_bytes: on the extractor to read a larger file", file, size, limit))
}

// ErrTooLarge is what a file over its limit is, by errors.Is: a reader
// of several files names the limit they share.
var ErrTooLarge = errors.New("the file is over its limit")

// tooLargeError is a refusal of a file over its limit, which is ErrTooLarge.
type tooLargeError string

func (e tooLargeError) Error() string        { return string(e) }
func (e tooLargeError) Is(target error) bool { return target == ErrTooLarge }

// Format is what the node limit knows of a file format: the bytes after
// which its parser can start a node, a key, a value, an item or a
// collection, and how many bytes of max_bytes allow one node.
type Format struct {
	starts       [256]bool // always
	beforeSpace  [256]bool // only before white space or the end of the file
	BytesPerNode int64
	// yaml is set for YAML, whose parser reads UTF-16 after a byte order
	// mark and breaks lines on NEL, LS and PS as on \n.
	yaml bool
}

func newFormat(bytesPerNode int64, starts, beforeSpace string) *Format {
	f := &Format{BytesPerNode: bytesPerNode}
	for i := range len(starts) {
		f.starts[starts[i]] = true
	}
	for i := range len(beforeSpace) {
		f.beforeSpace[beforeSpace[i]] = true
	}
	return f
}

// The node limit is max_bytes divided by bytesPerNode, and checked before
// the parser runs, since yaml.v3 builds a whole document and go-toml a whole
// expression before errata sees a node. A file of nothing but one-character
// items counts one node for each byte, and an ordinary file one for every 3
// to 16 bytes: the densest measured is a uv.lock of 7.9 MB, 3.4 bytes a node
// in TOML; a generated Helm index of 6.2 MB, 5.9 in YAML (a real one of
// 6.3 MB, 7.9); and a Google API document of 7.4 MB, 14.4 in JSON. Each
// value allows those files at the default 4 times the nodes they can hold.
// The limit bounds the nodes, not the memory: a node costs more or less
// by its shape, so peak memory depends on the file. The densest files
// measured so far at the default, each just under the limit, are examples
// and not a ceiling: 2.0 GB and 10 s in YAML, mappings of 62 anchored and
// tagged keys; 2.55 GB and 9.5 s in TOML, inline tables of dotted keys of
// 500 parts; and 0.42 GB in JSON, objects nested 900 deep. They grow in
// proportion to max_bytes. Without the limit, 32 MiB of one-character items
// ran out of 4.7 GB in YAML, and took 3.3 GB and 22 s in TOML and 2.0 GB and
// 7 s in JSON.
var (
	JSONFormat = newFormat(8, ",:[{", "")
	TOMLFormat = newFormat(3, "\n=,[{.", "")
	YAMLFormat = func() *Format {
		f := newFormat(7, "\n\r,:[{", "-?")
		f.yaml = true
		return f
	}()
)

// CheckNodes refuses data when f's parser could build more nodes from it
// than s's limit allows.
func (s Source) CheckNodes(data []byte, f *Format) error {
	limit := s.Limit / f.BytesPerNode
	if n := f.CountNodes(data); int64(n) > limit {
		return tooDenseError(fmt.Sprintf("%s: the file can hold up to %d nodes, over the limit of %d, one for every %d bytes of max_bytes; set max_bytes: higher to read a file this dense",
			s.File, n, limit, f.BytesPerNode))
	}
	return nil
}

// ErrTooDense is what a file over the node limit is, by errors.Is: a set
// names the bytes its earlier files left, which set the limit.
var ErrTooDense = errors.New("the file holds more nodes than its limit")

// tooDenseError is a refusal of a file over the node limit, which is
// ErrTooDense.
type tooDenseError string

func (e tooDenseError) Error() string        { return string(e) }
func (e tooDenseError) Is(target error) bool { return target == ErrTooDense }

// CountNodes is an upper bound on the nodes f's parser builds from data,
// with errata's tree, found in one pass before the parser runs: four for the
// start of the file, where the root starts too, and two for each byte after
// which a node can start. At most two nodes start at one place, a collection
// and its first key or item, so the bound holds wherever a byte of the set
// appears, inside a string too; there it only counts too many. For YAML, a
// NEL, LS or PS counts as \n does and as white space, and UTF-16 is counted
// as the UTF-8 the parser turns it into. The fuzz targets check the bound
// against what yaml.v3, go-toml and the tree hold.
func (f *Format) CountNodes(data []byte) int {
	if f.yaml {
		data = fromUTF16(data)
	}
	n := 2
	for i, b := range data {
		if f.starts[b] || (f.beforeSpace[b] && f.spaceAt(data, i+1)) || (f.yaml && breakEndsAt(data, i)) {
			n++
		}
	}
	return 2 * n
}

// spaceAt reports whether data has white space or its end at i.
func (f *Format) spaceAt(data []byte, i int) bool {
	if i == len(data) || isSpace(data[i]) {
		return true
	}
	if !f.yaml || i+1 == len(data) {
		return false
	}
	switch data[i] {
	case 0xc2: // NEL
		return data[i+1] == 0x85
	case 0xe2: // LS and PS
		return i+2 < len(data) && data[i+1] == 0x80 && (data[i+2] == 0xa8 || data[i+2] == 0xa9)
	default:
		return false
	}
}

// breakEndsAt reports whether a NEL (C2 85), an LS (E2 80 A8) or a PS
// (E2 80 A9) ends at data[i]. The byte A9 alone also ends é.
func breakEndsAt(data []byte, i int) bool {
	switch data[i] {
	case 0x85:
		return i >= 1 && data[i-1] == 0xc2
	case 0xa8, 0xa9:
		return i >= 2 && data[i-2] == 0xe2 && data[i-1] == 0x80
	default:
		return false
	}
}

// fromUTF16 is data in UTF-8 when it starts with a UTF-16 byte order mark,
// as yaml.v3 decodes it, and data itself otherwise.
func fromUTF16(data []byte) []byte {
	if len(data) < 2 {
		return data
	}
	var unit func(b []byte) uint16
	switch {
	case data[0] == 0xff && data[1] == 0xfe:
		unit = func(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }
	case data[0] == 0xfe && data[1] == 0xff:
		unit = func(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
	default:
		return data
	}
	out := make([]byte, 0, len(data)/2*3)
	for i := 2; i+1 < len(data); i += 2 {
		r := rune(unit(data[i:]))
		if utf16.IsSurrogate(r) && i+3 < len(data) {
			if pair := utf16.DecodeRune(r, rune(unit(data[i+2:]))); pair != utf8.RuneError {
				r = pair
				i += 2
			}
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// maxQuote is the most bytes of file text one message quotes. A key, an
// anchor or a number can be as long as the file, and a message goes into a
// CI log and a JUnit report.
const maxQuote = 64

// Clip is file text for a message: s, or its first maxQuote bytes and ...
// when it is longer.
func Clip(s string) string { return cut(s, maxQuote) }

// cut is s, or its first n bytes and ... when it is longer. The cut moves
// back to the start of a rune, at most three bytes, so it splits none.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for back := 0; back < utf8.UTFMax-1 && !utf8.RuneStart(s[n]); back++ {
		n--
	}
	return s[:n] + "..."
}

// Quote is clip in Go's quotes, as %q writes them, with the ... after the
// closing quote.
func Quote(s string) string {
	if c := Clip(s); c != s {
		return strconv.Quote(c[:len(c)-3]) + "..."
	}
	return strconv.Quote(s)
}

// ClipError cuts the message of a library's error, which may quote file
// text, to four times maxQuote bytes, room for any message that quotes none.
func ClipError(err error) error {
	if msg := err.Error(); len(msg) > 4*maxQuote {
		return errors.New(cut(msg, 4*maxQuote))
	}
	return err
}
