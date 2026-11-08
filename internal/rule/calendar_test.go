package rule

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Each event is one systemd 257 reads (systemd-analyze calendar prints its
// next runs), and its interval on a clock without daylight saving changes.
// Planted breaks: a coarser field not required whole; the gap from the
// last value to the first not checked; the shorthands' seconds left out;
// a fraction of a second read as whole.
func TestCalendarInterval(t *testing.T) {
	day, week := 24*time.Hour, 7*24*time.Hour
	for event, want := range map[string]time.Duration{
		"minutely": time.Minute, "hourly": time.Hour, "daily": day, "weekly": week, "DAILY": day,
		"*-*-* *:*:*":                     time.Second,
		"*:*:*":                           time.Second,
		"*:0/15":                          15 * time.Minute,
		"*:00/15:00":                      15 * time.Minute,
		"*-*-* *:0,15,30,45:00":           15 * time.Minute,
		"*:00..59/20":                     20 * time.Minute,
		"*:5/15":                          15 * time.Minute,
		"*-*-* 00/6:00":                   6 * time.Hour,
		"0/12:30":                         12 * time.Hour,
		"*-*-* 02:00:00":                  day,
		"02:00":                           day,
		"*-*-* 02:00 UTC":                 day,
		"*-*-* 02:00 utc":                 day,
		"03:15 Europe/Berlin":             day,
		"03:15 CET":                       day,
		"Mon":                             week,
		"Sun 03:00":                       week,
		"monday 03:00":                    week,
		"Mon..Sun 03:00":                  day,
		"Mon-Sun 03:00":                   day,
		"Mon,Tue,Wed,Thu,Fri,Sat,Sun 3:0": day,
		"Mon..Wed,Thu..Sun 03:00":         day,
		"Mon, *-*-* 1:00":                 week,
		"*:*:0/0.5":                       500 * time.Millisecond,
		"*:*:0/0.25":                      250 * time.Millisecond,
		"*:*:00/1.5":                      1500 * time.Millisecond,
		"9,21:00":                         12 * time.Hour,
		"*:*:0/30":                        30 * time.Second,
		"*:*:7":                           time.Minute,
		"*:*:00..59":                      time.Second,
		"*:00..59":                        time.Minute,
		"*:5..5":                          time.Hour,
		"Mon..Mon 03:00":                  week,
		"23:59:59":                        day,
		"*:*:59.999999":                   time.Minute,
		"*:*:0/0.001":                     time.Millisecond,
		"*:0/30 utc":                      30 * time.Minute,
	} {
		got, err := calendarInterval(event)
		if err != nil || got != want {
			t.Errorf("%q: %v, %v; want %v", event, got, err, want)
		}
	}
}

// What has no interval, and what systemd refuses, by message. Planted
// breaks: a day of the month read as every day; an open range of days
// read; a repeat that cannot fire read.
func TestCalendarIntervalRefuses(t *testing.T) {
	for event, want := range map[string]string{
		"monthly":                              "the year, the month and the day of the month must be *",
		"yearly":                               "the year, the month and the day of the month must be *",
		"quarterly":                            "the year, the month and the day of the month must be *",
		"semi-annually":                        "the year, the month and the day of the month must be *",
		"*-*-01 00:00":                         "the year, the month and the day of the month must be *",
		"*-02~03":                              "the year, the month and the day of the month must be *",
		"2026-*-* 00:00":                       "the year, the month and the day of the month must be *",
		"Mon..Fri 09:00":                       "its runs are not evenly spaced",
		"Mon,Wed 09:00":                        "its runs are not evenly spaced",
		"*:0,10":                               "its runs are not evenly spaced",
		"*:0/25":                               "its runs are not evenly spaced",
		"09..17:00":                            "its runs are not evenly spaced",
		"*:00:*":                               "its runs are not evenly spaced",
		"@1700000000":                          "is one moment",
		"":                                     "is no calendar event systemd reads",
		" UTC":                                 "is no calendar event systemd reads",
		"Mon..":                                "is no calendar event systemd reads",
		"Mon-":                                 "is no calendar event systemd reads",
		"Mon..Wed..Fri":                        "is no calendar event systemd reads",
		"Mon.Tue":                              "is no calendar event systemd reads",
		"Monx":                                 "is no calendar event systemd reads",
		"Fri..Mon":                             "is no calendar event systemd reads",
		"Mon,Foo":                              "is no calendar event systemd reads",
		"*:60":                                 "is no calendar event systemd reads",
		"24:00":                                "is no calendar event systemd reads",
		"*:0/60":                               "is no calendar event systemd reads",
		"*:0/0":                                "is no calendar event systemd reads",
		"*:*:61":                               "is no calendar event systemd reads",
		"*:*:0.":                               "is no calendar event systemd reads",
		"*:*:0/0.0000001":                      "is no calendar event systemd reads",
		"*:*:0..0.5":                           "is no calendar event systemd reads",
		"10":                                   "is no calendar event systemd reads",
		"10:":                                  "is no calendar event systemd reads",
		"10:00x":                               "is no calendar event systemd reads",
		"10:00:00:00":                          "is no calendar event systemd reads",
		"*-*-*x":                               "is no calendar event systemd reads",
		"*~01-01":                              "is no calendar event systemd reads",
		"*-*-*-*":                              "is no calendar event systemd reads",
		"*-*-32":                               "is no calendar event systemd reads",
		"*-*~29":                               "is no calendar event systemd reads",
		"*-13-01":                              "is no calendar event systemd reads",
		"9999999999:00":                        "is no calendar event systemd reads",
		"*:*:2148":                             "is no calendar event systemd reads",
		"*:*:00/1.0000005":                     "its runs are not evenly spaced",
		"*:*:0/0.001,0.0005":                   "take more than 60000 steps in all in one field",
		"Mon..Tue-Wed":                         "is no calendar event systemd reads",
		"*:*:0..1":                             "its runs are not evenly spaced",
		"*:*:0..0":                             "is no calendar event systemd reads",
		"*:*:60":                               "is no calendar event systemd reads",
		"0/23:00":                              "its runs are not evenly spaced",
		"*:5..0":                               "is no calendar event systemd reads",
		"*:0..1":                               "its runs are not evenly spaced",
		"*:10..9":                              "is no calendar event systemd reads",
		"*:50/20":                              "is no calendar event systemd reads",
		"*-*~01/1":                             "is no calendar event systemd reads",
		"*-*~02/1":                             "the year, the month and the day of the month must be *",
		"*-*-1/30 0:0":                         "the year, the month and the day of the month must be *",
		"100-*-* 0:0":                          "is no calendar event systemd reads",
		"*:" + strings.Repeat("1,", 241) + "1": "is no calendar event systemd reads",
	} {
		_, err := calendarInterval(event)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", event, err, want)
		}
	}
	// One fewer component than the refusal is read.
	if got, err := calendarInterval("*:" + strings.Repeat("1,", 240) + "1"); err != nil || got != time.Hour {
		t.Errorf("241 components: %v, %v", got, err)
	}
}

// systemd's own steps, which the interval reads through: two digit years,
// a stop cut to whole repeats, a fraction rounded at its seventh digit,
// and days of a range.
func TestCalendarParse(t *testing.T) {
	for _, c := range []struct{ event, want string }{
		{"25-*-* 0:0", "year [{2025 -1 0}]"},
		{"70..99-*-* 0:0", "year [{1970 1999 1}]"},
		{"2030-*-* 0:0", "year [{2030 -1 0}]"},
		{"00-*-* 0:0", "year [{2000 -1 0}]"},
		{"69-*-* 0:0", "year [{2069 -1 0}]"},
		{"99-*-* 0:0", "year [{1999 -1 0}]"},
		{"*:*:1.000001", "us [{1000001 -1 0}]"},
		{"*:*:1.0000014", "us [{1000001 -1 0}]"},
		{"*:0..50/20", "minute [{0 40 20}]"},
		{"*:0..10/20", "minute [{0 -1 0}]"},
		{"*:*:0.0000005", "us [{1 -1 0}]"},
		{"*:*:0.0000004", "us [{0 -1 0}]"},
		{"*:*:1.5", "us [{1500000 -1 0}]"},
		{"Tue..Thu", "weekdays 14"},
		{"Mon,Sun", "weekdays 65"},
		{"Mon..Sun", "weekdays -1"},
		{"01~03", "endOfMonth true"},
	} {
		s, err := parseCalendar(c.event)
		if err != nil {
			t.Errorf("%q: %v", c.event, err)
			continue
		}
		got := map[string]string{
			"year": fmt.Sprint(s.year), "minute": fmt.Sprint(s.minute), "us": fmt.Sprint(s.us),
			"weekdays": strconv.Itoa(s.weekdays), "endOfMonth": strconv.FormatBool(s.endOfMonth),
		}
		field, want, _ := strings.Cut(c.want, " ")
		if got[field] != want {
			t.Errorf("%q: %s %s, want %s", c.event, field, got[field], want)
		}
	}
}

// An event whose finest field of more than one value is evenly spaced
// over its span, and whose coarser fields are whole, has that spacing;
// written a value at a time, as a list, it reads the same as a repeat.
func TestPropertyCalendarIntervalOfAnEvenList(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		divisors := []int{1, 2, 3, 4, 5, 6, 10, 12, 15, 20, 30}
		step := rapid.SampledFrom(divisors).Draw(t, "step")
		start := rapid.IntRange(0, step-1).Draw(t, "start")
		var list []string
		for m := start; m < 60; m += step {
			list = append(list, strconv.Itoa(m))
		}
		want := time.Duration(step) * time.Minute
		for _, event := range []string{"*:" + strings.Join(list, ","), fmt.Sprintf("*:%d/%d", start, step)} {
			if got, err := calendarInterval(event); err != nil || got != want {
				t.Fatalf("%q: %v, %v; want %v", event, got, err, want)
			}
		}
	})
}

// calendar_interval's work is bounded by the steps its components take,
// a value two components repeat counted twice, so the review's event of
// 240 components over every thousandth of a minute is refused before a
// step is taken; 60000 steps in all are read. Planted break: the bound
// counting distinct values.
func TestCalendarIntervalBoundsItsSteps(t *testing.T) {
	heavy := "*:*:" + strings.TrimSuffix(strings.Repeat("0..59.999/0.001,", 240), ",")
	if _, err := calendarInterval(heavy); err == nil || !strings.Contains(err.Error(), "take more than 60000 steps in all in one field") {
		t.Errorf("240 components of 60000 steps: %v", err)
	}
	if got, err := calendarInterval("*:*:0/0.002,0.001/0.002"); err != nil || got != time.Millisecond {
		t.Errorf("60000 steps over two components: %v, %v", got, err)
	}
	if _, err := calendarInterval("*:*:0/0.002,0/0.002,0.001/0.002"); err == nil {
		t.Error("90000 steps over three components, 60000 values, read")
	}
}

// A calendar_interval call costs its event's bytes and its steps, so a
// comprehension over many heavy events reaches the cost limit, and one
// over light ones does not. Planted break: the cost tracker left out.
func TestCalendarIntervalHasACost(t *testing.T) {
	if got := calendarSteps("*:0/15"); got != 1+4+24 {
		t.Errorf("*:0/15 takes %d steps, want 29", got)
	}
	if got := calendarSteps("x"); got != 0 {
		t.Errorf("an event errata refuses takes %d steps", got)
	}
	expr := "v.all(x, calendar_interval(x) > duration('0s'))"
	light := slices.Repeat([]string{"*:0/15"}, 20)
	if _, err := eval(t, expr, light...); err != nil {
		t.Errorf("20 light events: %v", err)
	}
	heavy := slices.Repeat([]string{"*:*:0/0.001"}, 20)
	if _, err := eval(t, expr, heavy...); err == nil || !strings.Contains(err.Error(), "cost limit") {
		t.Errorf("20 events of 60000 steps: %v, want the cost limit", err)
	}
}
