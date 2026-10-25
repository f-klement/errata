package core

import (
	"bytes"
	"errors"
	"fmt"
	slashpath "path"
	"path/filepath"
	"strings"

	"github.com/f-klement/errata/internal/check"
)

// ReadFunc reads a file into entries in file order, charging m for its work.
type ReadFunc func(data []byte, m *Meter, visit func(*Entry) error) error

// WorkPerByte is the work meter's budget for each byte of a file and of
// the spec's names. Each read of an ini or properties extractor has one
// budget, WorkPerByte for each byte of the file and of the spec's names,
// and every loop that touches a byte charges it: each line or token a
// reader reads, each entry it visits and the names compared for it, and
// for an ansible host the range walk, the match of a spec's host against
// a pattern and the address patterns. A read past the budget is refused,
// so no shape of file or spec costs more than its size times workPerByte,
// whatever loop a later change adds, as long as the loop charges.
//
// WorkPerByte was chosen from measurement. The fixed costs below and in
// the range walk make a unit 0.3 ns to 2.6 ns on the dense shapes of every
// reader measured at 32 MiB on a cluster node (TestReplayINIShapesAtFullSize's
// and the reviews'). Real files charge 2 to 8 units a byte: pytest's tox.ini
// 7.19 selecting minversion, git's own config 5.78, php.ini-production
// 4.16, flake8's setup.cfg 4.04, qemu's .gitmodules 3.96, an ansible
// example inventory 3.83, spring-petclinic's application.properties 3.61,
// chromium's .gitmodules 3.04, the JDK's java.security 2.52. The budget is
// 128 a byte, 18 times the densest real file, and at 32 MiB about 4.3
// billion units, 1.5 s to 11 s (the reviews' worst shape is refused at it in
// 5.9 s): a file errata refuses is one no tool writes.
// TestWorkMeterLeavesRoomForOrdinaryFiles holds the tests' files of each
// dialect to a quarter of it.
const WorkPerByte = 128

// workFloor is the budget every read has besides its bytes', about 1.5 ms:
// room for the address patterns of one host, 400 ns a byte, in a file of a
// few lines.
const workFloor = 1 << 20

// Meter is one read's work budget, workFloor and WorkPerByte for each byte
// of the file and the spec, and the last text a costly test decided (Decide),
// which a file of many lines of one ansible host would otherwise pay for on
// each. A nil meter has no budget and keeps nothing, for tests of a reader
// alone.
type Meter struct {
	Budget, Spent int64
	decided       []byte // the text Decide last decided, and its answer
	answer        bool
}

// NewMeter is the meter of a read of bytes bytes, the file's and the spec's
// names'.
func NewMeter(bytes int) *Meter {
	return &Meter{Budget: workFloor + WorkPerByte*int64(bytes)}
}

// Charge spends n units, and reports errWork once the budget is spent.
func (m *Meter) Charge(n int) error {
	if m == nil {
		return nil
	}
	if m.Spent += int64(n); m.Spent > m.Budget {
		return ErrWork
	}
	return nil
}

// ErrWork is the error of a charge past the budget.
var ErrWork = errors.New("work")

// Decide is decide's answer for text, charged cost, and kept: asked again for
// the same text, the meter gives the kept answer for len(text). It is how
// ansible's address patterns run once for the lines of one host.
func (m *Meter) Decide(text []byte, cost int, decide func(string) bool) (bool, error) {
	if m != nil && m.decided != nil && bytes.Equal(m.decided, text) {
		return m.answer, m.Charge(len(text))
	}
	if err := m.Charge(cost); err != nil {
		return false, err
	}
	answer := decide(string(text))
	if m != nil {
		m.decided, m.answer = append(m.decided[:0], text...), answer
	}
	return answer, nil
}

// The fixed work of the steps the readers share, in units of about 1.5 ns,
// besides one for each byte they read: measured on the densest shapes, a line
// of a few bytes and an entry each cost tens of nanoseconds whatever their
// length. The readers' own steps are beside them.
const (
	LineWork  = 64 // a line, or a properties logical line, or a git entry
	entryWork = 48 // an entry the matcher looks at
)

// tooMuchWork refuses a read that spent its budget.
func tooMuchWork(file string) error {
	return fmt.Errorf("%s: reading the file and matching the spec take more than %d units of work for each byte of the two and %d more, the limit; narrow the spec, or a regex extractor can still read the file", file, WorkPerByte, workFloor)
}

// FileParts is file with slashes, cleaned, as its directories and its name.
func FileParts(file string) (dirs []string, base string) {
	p := filepath.ToSlash(filepath.Clean(file))
	return strings.Split(slashpath.Dir(p), "/"), slashpath.Base(p)
}

// EntryKind is what an entry is.
type EntryKind uint8

const (
	// EntryKey is a key and its value.
	EntryKey EntryKind = iota
	// EntryName is a name a line holds with no key: an ansible host line's
	// host, or a group a children section lists. Its value is the name.
	EntryName
)

// Entry is one value a file sets, with the names it is set under, as its
// dialect compares them. The slices are valid only while the visit runs,
// since readers reuse their buffers.
type Entry struct {
	Section []byte
	Host    []byte // ansible: the host of a host line
	Kind    EntryKind
	Key     []byte
	Value   []byte
	Line    int  // where the key, or the name, starts
	NoValue bool // git: a key with no =, which git reads as true
	// sectionSeq counts the headers read so far, and hostSeq the ansible
	// host lines: an entry with the seq of the entry before has its
	// section, or its host, so a matcher compares each name once.
	SectionSeq int
	HostSeq    int
	// computed marks a value its reader found the tool computes: a php
	// boolean word, constant, operator or ${} reference, and an ansible
	// literal whose value Python writes otherwise, such as 0x10 or [1, 2].
	Computed bool
	// Origin is the part of the file the value holds for, which the value
	// carries as its check.Value.Origin: a Jenkinsfile's stage "deploy".
	Origin string
}

// Query is what an extractor selects, in the form of the entries.
type Query struct {
	Section []byte
	Host    []byte
	HasHost bool // false: the entries of every host
	// MatchHost reports whether a spec's host selects an entry's host, for a
	// query that has one: ansible's hostMatches.
	MatchHost func(host, want []byte, m *Meter) (bool, error)
	Key       []byte
	HasKey    bool // false: the entries of kind EntryName
}

// Wants reports whether the query selects e: the reference the fuzz targets
// hold the matcher to.
func (q *Query) Wants(e *Entry) bool {
	switch {
	case !bytes.Equal(e.Section, q.Section):
		return false
	case q.HasHost && !q.hostOK(e.Host):
		return false
	case q.HasKey:
		return e.Kind == EntryKey && bytes.Equal(e.Key, q.Key)
	default:
		return e.Kind == EntryName
	}
}

// Matcher is Query.Wants for entries in file order: it compares the
// section once for each header and the host once for each host line, so a
// long name costs its length once, not once for each entry under it.
type Matcher struct {
	q                   *Query
	m                   *Meter
	sectionSeq, hostSeq int
	sectionOK, hostOK   bool
	Err                 error // a host the matcher refused to match, which ends the read
}

// NewMatcher is the matcher of q, charging m.
func NewMatcher(q *Query, m *Meter) *Matcher {
	return &Matcher{q: q, m: m, sectionSeq: -1, hostSeq: -1}
}

// Wants is Query.Wants of e, charged; it sets Err when it refuses to match
// e's host or the budget is spent.
func (m *Matcher) Wants(e *Entry) bool {
	if m.Err = m.m.Charge(entryWork + len(m.q.Key)); m.Err != nil {
		return false
	}
	if e.SectionSeq != m.sectionSeq {
		if m.Err = m.m.Charge(len(m.q.Section)); m.Err != nil {
			return false
		}
		m.sectionSeq, m.sectionOK = e.SectionSeq, bytes.Equal(e.Section, m.q.Section)
	}
	if !m.sectionOK {
		return false
	}
	if m.q.HasHost {
		if e.HostSeq != m.hostSeq {
			m.hostSeq = e.HostSeq
			if m.hostOK, m.Err = m.q.MatchHost(e.Host, m.q.Host, m.m); m.Err != nil {
				return false
			}
		}
		if !m.hostOK {
			return false
		}
	}
	if m.q.HasKey {
		return e.Kind == EntryKey && bytes.Equal(e.Key, m.q.Key)
	}
	return e.Kind == EntryName
}

// hostOK is matchHost with no budget and a refusal read as no match, for
// the reference the fuzz target holds the Matcher to.
func (q *Query) hostOK(host []byte) bool {
	ok, _ := q.MatchHost(host, q.Host, nil)
	return ok
}

// EntryExtractor returns the values of the entries its query wants from the
// file read reads. The ini and properties extractors are both one.
type EntryExtractor struct {
	Source
	Query Query
	Read  ReadFunc
	// notLiteral is the dialect's test for the file, nil when every value
	// is literal.
	NotLiteral func(*Entry) bool
	// Keyed sets each value's Key to its entry's key: the envfile reader's
	// values carry the key they are set under.
	Keyed bool
	// valueLimit is the most values the extractor returns, 0 for maxValues,
	// which every extractor errata builds has: a test sets it to witness the
	// limit with a small file. The refusal still names maxValues.
	ValueLimit int
}

// ValuesAllowed is the extractor's value limit.
func (e EntryExtractor) ValuesAllowed() int {
	if e.ValueLimit == 0 {
		return MaxValues
	}
	return e.ValueLimit
}

// MayReturnNotLiteral reports whether the dialect's tool computes some
// values of the file, which skip_not_literal can then leave out.
func (e EntryExtractor) MayReturnNotLiteral() bool { return e.NotLiteral != nil }

// Extract reads the file under root and returns every value the query
// selects, in file order.
func (e EntryExtractor) Extract(root string) ([]check.Value, error) {
	data, err := e.Source.Read(root)
	if err != nil {
		return nil, err
	}
	return e.FromData(data)
}

// FromData returns the values of the entries the query wants, at most
// maxValues of them and at most the extractor's max_bytes in all, as every
// extractor's values. A value is mostly a part of the file, but git reads a
// key with no = as true, four bytes for a key of one, and a byte that is
// not UTF-8 is three in the U+FFFD Java reads for it.
func (e EntryExtractor) FromData(data []byte) ([]check.Value, error) {
	out := []check.Value{}
	var total int64
	var stop error
	work := NewMeter(len(data) + len(e.Query.Section) + len(e.Query.Host) + len(e.Query.Key))
	m := NewMatcher(&e.Query, work)
	err := e.Read(data, work, func(en *Entry) error {
		if !m.Wants(en) {
			switch {
			case errors.Is(m.Err, ErrWork):
				return ErrWork
			case m.Err != nil:
				return LineError(en.Line, "%v", m.Err)
			}
			return nil
		}
		if total += int64(len(en.Value)); total > e.Limit {
			stop = tooManyBytes(e.File, e.Limit)
			return errStop
		}
		if len(out) == e.ValuesAllowed() {
			stop = TooManyValues(e.File)
			return errStop
		}
		v := check.Value{Text: string(en.Value), At: check.Location{File: e.File, Line: en.Line}, NotLiteral: e.NotLiteral != nil && e.NotLiteral(en), Origin: en.Origin}
		if e.Keyed {
			v.Key, v.HasKey = string(en.Key), true
		}
		out = append(out, v)
		return nil
	})
	switch {
	case stop != nil:
		return nil, stop
	case errors.Is(err, ErrWork):
		return nil, tooMuchWork(e.File)
	case err != nil:
		return nil, fmt.Errorf("%s: %w", e.File, err)
	}
	return out, nil
}

// tooManyBytes refuses values that hold more bytes than limit in all.
func tooManyBytes(file string, limit int64) error {
	return fmt.Errorf("%s: the values the extractor selects hold more than %d bytes in all, the extractor's max_bytes; a key git reads as true and a byte Java reads as U+FFFD hold more than the file, so narrow the section or the key, or set max_bytes: higher", file, limit)
}

// errStop ends a read from inside a visit.
var errStop = errors.New("stopped")

// LineError names the line a reader stopped at.
func LineError(line int, format string, args ...any) error {
	return fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, args...))
}

// HasJinja reports whether v holds the start of a Jinja2 expression,
// statement or comment.
func HasJinja(v []byte) bool {
	return bytes.Contains(v, []byte("{{")) || bytes.Contains(v, []byte("{%")) || bytes.Contains(v, []byte("{#"))
}

// templateSuffixes end the names of template files, such as php.ini.j2,
// which an engine renders before any tool reads them. .tpl and .tftpl are
// also Terraform's templatefile.
var templateSuffixes = []string{".j2", ".jinja", ".jinja2", ".tmpl", ".tpl", ".tftpl", ".erb"}

// IsTemplate reports whether a file's name ends in a template suffix.
func IsTemplate(base string) bool {
	for _, s := range templateSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	return false
}

// TemplateNotLiteral is, for a template file, the test of a value the
// engine renders, or that test, the dialect's own, which may be nil,
// marks: one that holds {{, {% or {# (hasJinja), <% in ERB, and ${ or %{
// in Terraform's .tpl and .tftpl. For another file it is test.
func TemplateNotLiteral(file string, test func(*Entry) bool) func(*Entry) bool {
	_, base := FileParts(file)
	if !IsTemplate(base) {
		return test
	}
	erb := strings.HasSuffix(base, ".erb")
	terraform := strings.HasSuffix(base, ".tpl") || strings.HasSuffix(base, ".tftpl")
	return func(e *Entry) bool {
		v := e.Value
		return HasJinja(v) || erb && bytes.Contains(v, []byte("<%")) ||
			terraform && (bytes.Contains(v, []byte("${")) || bytes.Contains(v, []byte("%{"))) ||
			test != nil && test(e)
	}
}
