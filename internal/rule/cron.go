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
	spec := strings.TrimSpace(schedule)
	if strings.HasPrefix(spec, "@") {
		expanded, ok := cronMacros[spec]
		switch {
		case slices.Contains([]string{"@monthly", "@yearly", "@annually"}, spec):
			return 0, fmt.Errorf("%q has no fixed interval, since months and years differ in length", schedule)
		case !ok:
			return 0, fmt.Errorf("%q: cron_interval reads the macros @hourly, @daily, @midnight and @weekly", schedule)
		}
		spec = expanded
	}
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return 0, fmt.Errorf("%q has %d fields; cron_interval reads five: minute, hour, day of month, month, day of week", schedule, len(fields))
	}
	if fields[2] != "*" || fields[3] != "*" {
		return 0, fmt.Errorf("%q has no fixed interval: day of month and month must be *, since months differ in length", schedule)
	}
	minutes, err := minuteField.parse(fields[0])
	if err != nil {
		return 0, fmt.Errorf("%q: %w", schedule, err)
	}
	hours, err := hourField.parse(fields[1])
	if err != nil {
		return 0, fmt.Errorf("%q: %w", schedule, err)
	}
	days, err := weekdays.parse(fields[4])
	if err != nil {
		return 0, fmt.Errorf("%q: %w", schedule, err)
	}

	// Every run in one week, in minutes from Sunday 00:00, in order.
	var runs []int
	for _, d := range days {
		for _, h := range hours {
			for _, m := range minutes {
				runs = append(runs, (d*24+h)*60+m)
			}
		}
	}
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
	step := 1
	if stepped {
		n, err := strconv.Atoi(stepText)
		if err != nil || n < 1 {
			return fmt.Errorf("the step %q is not a whole number above 0", stepText)
		}
		step = n
	}
	var lo, hi int
	switch {
	case base == "*":
		lo, hi = 0, f.span-1
	case base == "H":
		if stepped && f.span%step != 0 {
			return fmt.Errorf("in H/%d, %d does not divide the field's %d values, so the runs depend on the job's name", step, step, f.span)
		}
		lo, hi = 0, 0
		if stepped {
			hi = f.span - 1
		}
	case strings.HasPrefix(base, "H("):
		return fmt.Errorf("%q: H with a range is not read", base)
	default:
		first, last, ranged := strings.Cut(base, "-")
		var err error
		if lo, err = f.value(first); err != nil {
			return err
		}
		hi = lo
		switch {
		case ranged:
			if hi, err = f.value(last); err != nil {
				return err
			}
			if hi < lo {
				return fmt.Errorf("the range %q ends before it starts", base)
			}
		case stepped:
			// 5/15 is 5-59/15, as cronie and Kubernetes read it.
			hi = f.max
		}
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
