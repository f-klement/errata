package rule

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// cronField is one of the three cron fields cron_interval reads.
type cronField struct {
	name string
	// span is how many values the field has: 60 minutes, 24 hours, 7 days.
	span int
	// max is the largest value it accepts. Day of week takes 7 for Sunday
	// beside 0, as cron does.
	max   int
	names map[string]int
}

var (
	minuteField = cronField{name: "minute", span: 60, max: 59}
	hourField   = cronField{name: "hour", span: 24, max: 23}
	weekdays    = cronField{name: "day of week", span: 7, max: 7, names: map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}}
)

// cronMacros are the shorthands Kubernetes CronJobs and Vixie cron accept
// that have a fixed interval. A month or a year has no fixed length, so
// @monthly and @yearly are refused.
var cronMacros = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
}

const minutesPerWeek = 7 * 24 * 60

// cronInterval returns the time between consecutive runs of a five field cron
// schedule, and an error for a schedule whose runs are not evenly spaced.
//
// The minute, hour and day of week fields take numbers, *, ranges (1-5),
// lists (0,30), steps (*/15, 0-30/10, 5/15) and day names (mon). They also
// take Jenkins' H, a value Jenkins derives from the job's name: H stands for
// one value and H/N for every Nth value from one start. That start never
// changes the spacing, so H reads as the field's first value; H/N where N
// does not divide the field is refused, because there the runs depend on the
// job's name. Day of month and month must be *, since months differ in
// length. The interval is that of a clock without daylight saving changes.
func cronInterval(schedule string) (time.Duration, error) {
	fields, err := cronFields(schedule)
	if err != nil {
		return 0, err
	}
	runs, err := weekRuns(schedule, fields)
	if err != nil {
		return 0, err
	}
	return fixedGap(schedule, runs)
}

// cronFields is the five fields of schedule, a macro expanded, refused
// unless day of month and month are *.
func cronFields(schedule string) ([]string, error) {
	spec, err := expandMacro(schedule)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return nil, fmt.Errorf("%q has %d fields; cron_interval reads five: minute, hour, day of month, month, day of week", schedule, len(fields))
	}
	if fields[2] != "*" || fields[3] != "*" {
		return nil, fmt.Errorf("%q has no fixed interval: day of month and month must be *, since months differ in length", schedule)
	}
	return fields, nil
}

// expandMacro is schedule trimmed, with a macro such as @daily expanded.
// A macro of no fixed interval, or one cron_interval does not read, is
// refused.
func expandMacro(schedule string) (string, error) {
	spec := strings.TrimSpace(schedule)
	if !strings.HasPrefix(spec, "@") {
		return spec, nil
	}
	expanded, ok := cronMacros[spec]
	switch {
	case slices.Contains([]string{"@monthly", "@yearly", "@annually"}, spec):
		return "", fmt.Errorf("%q has no fixed interval, since months and years differ in length", schedule)
	case !ok:
		return "", fmt.Errorf("%q: cron_interval reads the macros @hourly, @daily, @midnight and @weekly", schedule)
	}
	return expanded, nil
}

// weekRuns is every run of schedule's fields in one week, in minutes from
// Sunday 00:00, in order.
func weekRuns(schedule string, fields []string) ([]int, error) {
	minutes, err := minuteField.parse(fields[0])
	if err != nil {
		return nil, fmt.Errorf("%q: %w", schedule, err)
	}
	hours, err := hourField.parse(fields[1])
	if err != nil {
		return nil, fmt.Errorf("%q: %w", schedule, err)
	}
	days, err := weekdays.parse(fields[4])
	if err != nil {
		return nil, fmt.Errorf("%q: %w", schedule, err)
	}
	var runs []int
	for _, d := range days {
		for _, h := range hours {
			for _, m := range minutes {
				runs = append(runs, (d*24+h)*60+m)
			}
		}
	}
	return runs, nil
}

// fixedGap is the one gap between each run and the next, around the week,
// or a refusal that names every gap there is.
func fixedGap(schedule string, runs []int) (time.Duration, error) {
	gaps := map[int]bool{}
	for i, r := range runs {
		next := runs[(i+1)%len(runs)]
		if next <= r {
			next += minutesPerWeek
		}
		gaps[next-r] = true
	}
	if len(gaps) == 1 {
		for g := range gaps {
			return time.Duration(g) * time.Minute, nil
		}
	}
	apart := make([]string, 0, len(gaps))
	for _, g := range slices.Sorted(maps.Keys(gaps)) {
		apart = append(apart, (time.Duration(g) * time.Minute).String())
	}
	return 0, fmt.Errorf("%q has no fixed interval: its runs are %s apart", schedule, strings.Join(apart, " or "))
}

// parse returns the values of one field, ascending and without repeats.
func (f cronField) parse(text string) ([]int, error) {
	set := make([]bool, f.span)
	for part := range strings.SplitSeq(text, ",") {
		if err := f.mark(set, part); err != nil {
			return nil, fmt.Errorf("%s field %q: %w", f.name, text, err)
		}
	}
	var values []int
	for v, on := range set {
		if on {
			values = append(values, v)
		}
	}
	return values, nil
}

// mark sets the values one comma separated part of a field selects.
func (f cronField) mark(set []bool, part string) error {
	base, stepText, stepped := strings.Cut(part, "/")
	step, err := cronStep(stepText, stepped)
	if err != nil {
		return err
	}
	lo, hi, err := f.bounds(base, step, stepped)
	if err != nil {
		return err
	}
	// A step past the field's range selects only the first value, so
	// */90 in the minute field runs once an hour, as cronie and Kubernetes
	// read it. The loop stops before v + step could overflow.
	for v := lo; ; v += step {
		set[v%f.span] = true
		if hi-v < step {
			break
		}
	}
	return nil
}

// cronStep is the step after a part's /, or 1 when it has none.
func cronStep(text string, stepped bool) (int, error) {
	if !stepped {
		return 1, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("the step %q is not a whole number above 0", text)
	}
	return n, nil
}

// bounds is the first and last value the part base, before its step,
// selects: the whole field for *, Jenkins' H, or a value or a range.
func (f cronField) bounds(base string, step int, stepped bool) (int, int, error) {
	switch {
	case base == "*":
		return 0, f.span - 1, nil
	case base == "H":
		hi, err := f.hashHigh(step, stepped)
		return 0, hi, err
	case strings.HasPrefix(base, "H("):
		return 0, 0, fmt.Errorf("%q: H with a range is not read", base)
	default:
		return f.rangeBounds(base, stepped)
	}
}

// hashHigh is the last value of H, read as the field's first value, or of
// H/step, which starts there too, refused where step does not divide the
// field.
func (f cronField) hashHigh(step int, stepped bool) (int, error) {
	if !stepped {
		return 0, nil
	}
	if f.span%step != 0 {
		return 0, fmt.Errorf("in H/%d, %d does not divide the field's %d values, so the runs depend on the job's name", step, step, f.span)
	}
	return f.span - 1, nil
}

// rangeBounds is the bounds of a value or a range, a-b, of the field. A
// value with a step runs to the field's end: 5/15 is 5-59/15, as cronie and
// Kubernetes read it.
func (f cronField) rangeBounds(base string, stepped bool) (int, int, error) {
	first, last, ranged := strings.Cut(base, "-")
	lo, err := f.value(first)
	if err != nil {
		return 0, 0, err
	}
	switch {
	case ranged:
		hi, err := f.value(last)
		if err != nil {
			return 0, 0, err
		}
		if hi < lo {
			return 0, 0, fmt.Errorf("the range %q ends before it starts", base)
		}
		return lo, hi, nil
	case stepped:
		return lo, f.max, nil
	}
	return lo, lo, nil
}

// value reads one number or day name of the field.
func (f cronField) value(text string) (int, error) {
	if n, ok := f.names[strings.ToLower(text)]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 0 || n > f.max {
		if f.names != nil {
			return 0, fmt.Errorf("%q is not a number from 0 to %d or a day name such as mon", text, f.max)
		}
		return 0, fmt.Errorf("%q is not a number from 0 to %d", text, f.max)
	}
	return n, nil
}
