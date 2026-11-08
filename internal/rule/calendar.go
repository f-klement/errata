package rule

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// A systemd calendar event, as OnCalendar= in a timer unit writes it, read
// as systemd 257's calendar_spec_from_string reads it
// (src/shared/calendarspec.c): weekdays, a date, a time and a time zone,
// each field a chain of components. The parser is the small steps below,
// each one of systemd's.

// calComponent is one member of a chain: start, then every repeat up to
// stop, or up to the field's end when stop is -1; start alone when repeat
// is 0.
type calComponent struct {
	start, stop, repeat int
}

// calSpec is a calendar event. A nil chain is *, every value of its field.
// weekdays is a bit for each day from Monday, -1 for every day. Seconds are
// in microseconds, as systemd keeps them.
type calSpec struct {
	weekdays                           int
	year, month, day, hour, minute, us []calComponent
	endOfMonth                         bool
}

const (
	usPerSec    = 1_000_000
	allWeekdays = 127
	// tooBig is a number past every field's end, which a longer run of
	// digits saturates at, so no sum of two overflows. systemd refuses a
	// number past INT_MAX, which is past every field's end too.
	tooBig = math.MaxInt32 + 1
	// noStop is the stop of a component that runs to its field's end.
	noStop = -1
	// lastUs is the last microsecond of a minute.
	lastUs = 60*usPerSec - 1
	// maxComponents is CALENDARSPEC_COMPONENTS_MAX: a chain is refused past
	// one more component than it.
	maxComponents = 240
)

var errCalendar = errors.New("is no calendar event systemd reads")

// calShorthands are the names systemd reads for an event, in any case,
// each as the event it stands for.
var calShorthands = map[string]string{
	"minutely": "*-*-* *:*:00", "hourly": "*-*-* *:00:00", "daily": "*-*-* 00:00:00",
	"monthly": "*-*-01 00:00:00", "weekly": "Mon *-*-* 00:00:00",
	"annually": "*-01-01 00:00:00", "yearly": "*-01-01 00:00:00", "anually": "*-01-01 00:00:00",
	"quarterly":  "*-01,04,07,10-01 00:00:00",
	"biannually": "*-01,07-01 00:00:00", "bi-annually": "*-01,07-01 00:00:00",
	"semiannually": "*-01,07-01 00:00:00", "semi-annually": "*-01,07-01 00:00:00",
}

// parseCalendar reads text as calendar_spec_from_string does.
func parseCalendar(text string) (calSpec, error) {
	p := withoutZone(text)
	if p == "" {
		return calSpec{}, errCalendar
	}
	if full, ok := calShorthands[strings.ToLower(p)]; ok {
		p = full
	}
	if strings.HasPrefix(p, "@") {
		return calSpec{}, errors.New("is one moment, @ and a time in seconds, which has no interval")
	}
	c := calSpec{}
	for _, part := range []func(string) (string, error){c.parseWeekdays, c.parseDate, c.parseTime} {
		var err error
		if p, err = part(p); err != nil {
			return calSpec{}, err
		}
	}
	if p != "" {
		return calSpec{}, errCalendar
	}
	c.normalize()
	if !c.valid() {
		return calSpec{}, errCalendar
	}
	return c, nil
}

// withoutZone is text without the time zone at its end: UTC in any case,
// or a name the IANA database holds. The interval is that of a clock
// without daylight saving changes, so the zone does not change it. systemd
// also reads the names of the machine's own zone; errata reads them only
// where the database holds them, as it holds CET.
func withoutZone(text string) string {
	i := strings.LastIndexByte(text, ' ')
	if i < 0 {
		return text
	}
	if _, err := zone(text[i+1:]); err != nil && !strings.EqualFold(text[i+1:], "UTC") {
		return text
	}
	return text[:i]
}

// dayNames are the weekdays systemd reads, the long name of each before
// its short one, so Monday is not read as Mon and day.
var dayNames = []string{"Monday", "Mon", "Tuesday", "Tue", "Wednesday", "Wed", "Thursday", "Thu", "Friday", "Fri", "Saturday", "Sat", "Sunday", "Sun"}

// dayName is the number of the day name p starts with, from Monday at 0,
// and the rest of p; ok is false when it starts with none.
func dayName(p string) (nr int, rest string, ok bool) {
	for i, name := range dayNames {
		if len(p) >= len(name) && strings.EqualFold(p[:len(name)], name) {
			return i / 2, p[len(name):], true
		}
	}
	return 0, p, false
}

// parseWeekdays is parse_weekdays: day names, any case, joined by , and
// ranges with .. or -, ending at a space or the end. Text that starts with
// no day name has none, and is the date.
func (c *calSpec) parseWeekdays(p string) (string, error) {
	last := -1
	for first := true; ; first = false {
		nr, rest, ok := dayName(p)
		switch {
		case !ok && first:
			return p, nil
		case !ok, rest != "" && !strings.ContainsRune("-., ", rune(rest[0])):
			return "", errCalendar
		}
		if err := c.addDays(last, nr); err != nil {
			return "", err
		}
		var done bool
		var err error
		if p, last, done, err = weekdaySeparator(rest, last, nr); done || err != nil {
			return p, err
		}
	}
}

// addDays marks day nr, and the days after last up to it when last starts
// a range.
func (c *calSpec) addDays(last, nr int) error {
	if last > nr {
		return errCalendar
	}
	from := nr
	if last >= 0 {
		from = last + 1
	}
	for d := from; d <= nr; d++ {
		c.weekdays |= 1 << d
	}
	return nil
}

// weekdaySeparator reads what follows day nr: the end, a space, .. or -
// to start a range at it, or a comma. done reports the end of the
// weekdays, and p is what follows them.
func weekdaySeparator(p string, last, nr int) (rest string, start int, done bool, err error) {
	if calEnd(p) {
		return strings.TrimLeft(p, " "), last, true, nil
	}
	last, p, ok := rangeOrList(p, last, nr)
	switch {
	case !ok:
		return "", 0, true, errCalendar
	case !calEnd(p):
		return p, last, false, nil
	case last >= 0:
		return "", 0, true, errCalendar // a range left open; a trailing comma is read
	}
	return strings.TrimLeft(p, " "), last, true, nil
}

// rangeOrList reads the .. or - that starts a range at day nr, which no
// range may be open for, or the comma that ends one; ok is false for any
// other.
func rangeOrList(p string, last, nr int) (int, string, bool) {
	switch {
	case strings.HasPrefix(p, "..") && last < 0:
		return nr, p[2:], true
	case p[0] == '-' && last < 0:
		return nr, p[1:], true
	case p[0] == ',':
		return -1, p[1:], true
	}
	return last, p, false
}

// calEnd reports whether p is at the end of a part of an event: the end
// of the text, or a space.
func calEnd(p string) bool { return p == "" || p[0] == ' ' }

// leadingDigits is the number the digits at the start of p write, at most
// tooBig, and the rest; ok is false when p starts with no digit.
func leadingDigits(p string) (v int, rest string, ok bool) {
	digits := strings.TrimLeft(p, "0123456789")
	for _, c := range p[:len(p)-len(digits)] {
		v = min(v*10+int(c-'0'), tooBig)
	}
	return v, digits, len(digits) < len(p)
}

// parseNumber is parse_component_decimal: digits, and for seconds a
// fraction, in microseconds (seconds).
func parseNumber(p string, usec bool) (int, string, error) {
	v, p, ok := leadingDigits(p)
	switch {
	case !ok:
		return 0, p, errCalendar
	case usec:
		return seconds(v, p)
	}
	return v, p, nil
}

// seconds is v seconds in microseconds, with the fraction after a point
// that starts no .. range (parseFraction).
func seconds(v int, p string) (int, string, error) {
	v *= usPerSec
	if !strings.HasPrefix(p, ".") || strings.HasPrefix(p, "..") {
		return v, p, nil
	}
	frac, p, ok := parseFraction(p[1:])
	if !ok {
		return 0, p, errCalendar
	}
	return v + frac, p, nil
}

// parseFraction is parse_fractional_part_u with six digits: the first six,
// padded with zeros, rounded up by a seventh of 5 to 9, the rest skipped.
func parseFraction(p string) (int, string, bool) {
	n := len(p) - len(strings.TrimLeft(p, "0123456789"))
	if n == 0 {
		return 0, p, false
	}
	frac, _, _ := leadingDigits((p[:n] + "000000")[:6])
	if n > 6 && p[6] >= '5' {
		frac++
	}
	return frac, p[n:], true
}

// parseChain is parse_chain: * or components joined by ,. For seconds, *
// is every second.
func parseChain(p string, usec bool) ([]calComponent, string, error) {
	if rest, ok := strings.CutPrefix(p, "*"); ok {
		if usec {
			return []calComponent{{0, noStop, usPerSec}}, rest, nil
		}
		return nil, rest, nil
	}
	var out []calComponent
	for {
		k, rest, err := parseComponent(p, usec)
		if err != nil {
			return nil, p, err
		}
		if out = append(out, k); len(out) > maxComponents+1 {
			return nil, p, errCalendar
		}
		var more bool
		if p, more = strings.CutPrefix(rest, ","); !more {
			return out, rest, nil
		}
	}
}

// parseComponent is one of prepend_component: a start, an optional
// ..stop, an optional /repeat, and then a separator.
func parseComponent(p string, usec bool) (calComponent, string, error) {
	k := calComponent{stop: noStop}
	var err error
	if k.start, p, err = parseNumber(p, usec); err != nil {
		return k, p, err
	}
	if rest, ok := strings.CutPrefix(p, ".."); ok {
		if k.stop, p, err = parseNumber(rest, usec); err != nil {
			return k, p, err
		}
		k.repeat = 1
		if usec {
			k.repeat = usPerSec
		}
	}
	if p, err = k.parseRepeat(p, usec); err != nil {
		return k, p, err
	}
	if p != "" && !strings.ContainsRune(" ,-~:", rune(p[0])) {
		return k, p, errCalendar
	}
	return k, p, nil
}

// parseRepeat reads a /repeat, which may not be 0; without one, a range
// of seconds shorter than a second is refused.
func (k *calComponent) parseRepeat(p string, usec bool) (string, error) {
	rest, ok := strings.CutPrefix(p, "/")
	if !ok {
		if usec && k.stop != noStop && k.start+k.repeat > k.stop {
			return p, errCalendar
		}
		return p, nil
	}
	var err error
	if k.repeat, rest, err = parseNumber(rest, usec); err == nil && k.repeat == 0 {
		err = errCalendar
	}
	return rest, err
}

// parseDate is parse_date: year-month-day or month-day, ~ for a day
// counted from the end of the month, or nothing when what follows is a
// time.
func (c *calSpec) parseDate(p string) (string, error) {
	if p == "" {
		return p, nil
	}
	first, t, err := parseChain(p, false)
	if err != nil {
		return "", err
	}
	if t == "" || t[0] == ':' {
		return p, nil // a time
	}
	second, t, err := c.dateField(t)
	switch {
	case err != nil:
		return "", err
	case calEnd(t):
		c.month, c.day = first, second
		return strings.TrimLeft(t, " "), nil
	case c.endOfMonth:
		return "", errCalendar
	}
	c.year, c.month = first, second
	return c.lastDateField(t)
}

// lastDateField reads the day of a date of three fields, which ends the
// date.
func (c *calSpec) lastDateField(t string) (string, error) {
	day, t, err := c.dateField(t)
	if err != nil || !calEnd(t) {
		return "", errCalendar
	}
	c.day = day
	return strings.TrimLeft(t, " "), nil
}

// dateField reads the - or ~ before a field of a date, and the field.
func (c *calSpec) dateField(t string) ([]calComponent, string, error) {
	switch t[0] {
	case '~':
		c.endOfMonth = true
	case '-':
	default:
		return nil, t, errCalendar
	}
	return parseChain(t[1:], false)
}

// parseTime is parse_calendar_time: hour:minute or hour:minute:second, and
// 00:00:00 when there is no time.
func (c *calSpec) parseTime(p string) (string, error) {
	zero := []calComponent{{0, noStop, 0}}
	c.hour, c.minute, c.us = zero, zero, zero
	if p == "" {
		return p, nil
	}
	var err error
	if c.hour, p, err = parseChain(p, false); err != nil || !strings.HasPrefix(p, ":") {
		return "", errCalendar
	}
	if c.minute, p, err = parseChain(p[1:], false); err != nil {
		return "", err
	}
	if p == "" {
		return p, nil
	}
	if p[0] != ':' {
		return "", errCalendar
	}
	c.us, p, err = parseChain(p[1:], true)
	return p, err
}

// normalize is calendar_spec_normalize: a weekday set of none or all is
// every day, two digit years are this century's or the last, and each
// component's stop is cut to a whole number of repeats.
func (c *calSpec) normalize() {
	if c.weekdays <= 0 || c.weekdays >= allWeekdays {
		c.weekdays = -1
	}
	c.endOfMonth = c.endOfMonth && c.day != nil
	for i := range c.year {
		c.year[i].start, c.year[i].stop = fullYear(c.year[i].start), fullYear(c.year[i].stop)
	}
	for _, ch := range []*[]calComponent{&c.year, &c.month, &c.day, &c.hour, &c.minute, &c.us} {
		for i := range *ch {
			(*ch)[i].normalize()
		}
	}
}

// fullYear is fix_year: 0 to 69 are 2000 to 2069, 70 to 99 are 1970 to
// 1999.
func fullYear(y int) int {
	switch {
	case y >= 0 && y < 70:
		return y + 2000
	case y >= 70 && y < 100:
		return y + 1900
	}
	return y
}

// normalize is normalize_chain for one component: stop cut to a whole
// number of repeats, and a repeat that never fires, or a stop at the
// start, dropped.
func (k *calComponent) normalize() {
	ranged := k.stop > k.start && k.repeat > 0
	if ranged {
		k.stop -= (k.stop - k.start) % k.repeat
	}
	if ranged && k.start+k.repeat > k.stop || k.start == k.stop {
		k.repeat, k.stop = 0, noStop
	}
}

// valid is calendar_spec_valid.
func (c *calSpec) valid() bool {
	return chainValid(c.year, 1970, 2199, false) && chainValid(c.month, 1, 12, false) &&
		chainValid(c.day, 1, 31, c.endOfMonth) && chainValid(c.hour, 0, 23, false) &&
		chainValid(c.minute, 0, 59, false) && chainValid(c.us, 0, lastUs, false)
}

// chainValid is chain_valid: each component within from and to, a day
// counted from the end of the month within 28 days of it, and each repeat
// able to fire once.
func chainValid(ch []calComponent, from, to int, endOfMonth bool) bool {
	if endOfMonth {
		to -= 3
	}
	return !slices.ContainsFunc(ch, func(k calComponent) bool { return !k.valid(from, to, endOfMonth) })
}

func (k calComponent) valid(from, to int, endOfMonth bool) bool {
	switch {
	case k.start < from || k.start > to || k.repeat > to-from:
		return false
	case k.stop != noStop:
		return k.stop >= from && k.stop <= to && k.start+k.repeat <= k.stop
	case endOfMonth:
		return k.start-k.repeat >= from
	}
	return k.start+k.repeat <= to
}

// maxCalValues is the most steps the components of one field of an event
// may take for calendar_interval, a value repeated by two components
// counted twice: a second split into thousandths, sixty times.
const maxCalValues = 60_000

// values is the sorted values of a chain over a field of span values, nil
// standing for each. A chain whose components take more than maxCalValues
// steps in all is refused before any is taken.
func values(ch []calComponent, span int) ([]int, error) {
	if ch == nil {
		ch = []calComponent{{0, noStop, 1}}
	}
	if chainSteps(ch, span) > maxCalValues {
		return nil, fmt.Errorf("has components that take more than %d steps in all in one field, which calendar_interval does not read", maxCalValues)
	}
	set := map[int]bool{}
	for _, k := range ch {
		k.mark(set, span)
	}
	out := make([]int, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	slices.Sort(out)
	return out, nil
}

// chainSteps is the steps the components of ch take over a field of span
// values, each counted whether or not another took it.
func chainSteps(ch []calComponent, span int) int {
	n := 0
	for _, k := range ch {
		n += k.steps(span)
	}
	return n
}

// last is the last value k may take below span.
func (k calComponent) last(span int) int {
	switch {
	case k.repeat == 0:
		return k.start
	case k.stop == noStop:
		return span - 1
	}
	return k.stop
}

// steps is how many values k takes below span.
func (k calComponent) steps(span int) int {
	if k.repeat == 0 {
		return 1
	}
	return (k.last(span)-k.start)/k.repeat + 1
}

// mark adds the values of k below span to set.
func (k calComponent) mark(set map[int]bool, span int) {
	for v, end := k.start, k.last(span); v <= end; v += max(k.repeat, 1) {
		set[v] = true
	}
}

// calFields are the fields of an event's runs within a week, from the
// finest: microseconds of a minute, minutes, hours, and days of the week,
// each with its span and the time one of its values stands for.
var calFields = []struct {
	span int
	unit time.Duration
}{
	{60 * usPerSec, time.Microsecond}, {60, time.Minute}, {24, time.Hour}, {7, 24 * time.Hour},
}

// calendarInterval returns the time between consecutive runs of a systemd
// calendar event, such as OnCalendar=*:0/15 or OnCalendar=daily, and an
// error for one whose runs are not evenly spaced. The weekdays, hours,
// minutes and seconds may be anything systemd reads; the year, the month
// and the day of the month must be *, since months differ in length. The
// interval is that of a clock without daylight saving changes.
//
// The runs are evenly spaced when the finest field that holds more than
// one value holds values evenly spaced around its span, and every coarser
// field holds each of its values: the gap from that field's last value to
// its first is then the gap between any two. An event of one value in
// each field runs once a week.
func calendarInterval(text string) (time.Duration, error) {
	sets, err := calendarSets(text)
	if err != nil {
		return 0, err
	}
	i := 0
	for i < len(sets) && len(sets[i]) == 1 {
		i++
	}
	if i == len(sets) {
		return 7 * 24 * time.Hour, nil
	}
	gap, ok := evenGap(sets[i], calFields[i].span)
	for j := i + 1; ok && j < len(sets); j++ {
		ok = len(sets[j]) == calFields[j].span
	}
	if !ok {
		return 0, fmt.Errorf("%q has no fixed interval: its runs are not evenly spaced", text)
	}
	return time.Duration(gap) * calFields[i].unit, nil
}

// calendarSets are the values of each field of calFields that the event
// text runs at.
func calendarSets(text string) ([][]int, error) {
	c, err := parseCalendar(text)
	if err != nil {
		return nil, fmt.Errorf("%q %w", text, err)
	}
	if c.year != nil || c.month != nil || c.day != nil {
		return nil, fmt.Errorf("%q has no fixed interval: the year, the month and the day of the month must be *, since months differ in length", text)
	}
	var sets [][]int
	for i, ch := range [][]calComponent{c.us, c.minute, c.hour} {
		vs, err := values(ch, calFields[i].span)
		if err != nil {
			return nil, fmt.Errorf("%q %w", text, err)
		}
		sets = append(sets, vs)
	}
	var days []int
	for d := range 7 {
		if c.weekdays&(1<<d) != 0 {
			days = append(days, d)
		}
	}
	return append(sets, days), nil
}

// evenGap is the gap between the values vs around a circle of span, and
// whether every gap is that one.
func evenGap(vs []int, span int) (int, bool) {
	gap := span / len(vs)
	if span%len(vs) != 0 {
		return 0, false
	}
	for k := 1; k < len(vs); k++ {
		if vs[k]-vs[k-1] != gap {
			return 0, false
		}
	}
	return gap, true
}
