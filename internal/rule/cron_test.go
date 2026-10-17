package rule

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestCronInterval(t *testing.T) {
	day := 24 * time.Hour
	for schedule, want := range map[string]time.Duration{
		"*/5 * * * *":     5 * time.Minute,
		"*/15 * * * *":    15 * time.Minute,
		"* * * * *":       time.Minute,
		"0 * * * *":       time.Hour,
		"17 */6 * * *":    6 * time.Hour,
		"0 */1 * * *":     time.Hour,
		"45 1 * * *":      day,
		"0,30 * * * *":    30 * time.Minute,
		"0-59/20 * * * *": 20 * time.Minute,
		"5/15 * * * *":    15 * time.Minute,
		"0 0,12 * * *":    12 * time.Hour,
		"0 3 * * 0":       7 * day,
		"30 5 * * 7":      7 * day,
		"30 5 * * sun":    7 * day,
		"30 5 * * MON":    7 * day,
		// Jenkins: H is one value derived from the job's name.
		"H * * * *":     time.Hour,
		"H/15 * * * *":  15 * time.Minute,
		"H H * * *":     day,
		"H H/6 * * *":   6 * time.Hour,
		"H H * * H":     7 * day,
		"  0 * * * *  ": time.Hour,
		"@hourly":       time.Hour,
		"@daily":        day,
		"@midnight":     day,
		"@weekly":       7 * day,
		// A step past the field's range selects only the first value,
		// so the schedule runs once per hour, day or week.
		"*/90 * * * *":                     time.Hour,
		"5/60 * * * *":                     time.Hour,
		"0 */30 * * *":                     day,
		"0 0 * * */8":                      7 * day,
		"1/9223372036854775807 * * * *":    time.Hour,
		"1-59/9223372036854775807 * * * *": time.Hour,
	} {
		got, err := cronInterval(schedule)
		if err != nil || got != want {
			t.Errorf("cronInterval(%q) = %s, %v; want %s", schedule, got, err, want)
		}
	}
}

// Each case is a schedule without one fixed interval, or one cron_interval
// does not read, and the error it must give.
func TestCronIntervalRefuses(t *testing.T) {
	for schedule, want := range map[string]string{
		"*/7 * * * *":     `"*/7 * * * *" has no fixed interval: its runs are 4m0s or 7m0s apart`,
		"*/5 2 * * *":     `"*/5 2 * * *" has no fixed interval: its runs are 5m0s or 23h5m0s apart`,
		"30 5 * * 1-5":    `"30 5 * * 1-5" has no fixed interval: its runs are 24h0m0s or 72h0m0s apart`,
		"0 9-17 * * *":    `has no fixed interval: its runs are 1h0m0s or 16h0m0s apart`,
		"0 0 * * 0,3,6":   `has no fixed interval: its runs are 24h0m0s or 72h0m0s apart`,
		"0 0 * * 0,3-6/3": `has no fixed interval: its runs are 24h0m0s or 72h0m0s apart`,
		"0 0 1 * *":       `"0 0 1 * *" has no fixed interval: day of month and month must be *, since months differ in length`,
		"0 0 * 1 *":       `day of month and month must be *`,
		"H H H * *":       `day of month and month must be *`,
		"@monthly":        `"@monthly" has no fixed interval, since months and years differ in length`,
		"@yearly":         `has no fixed interval, since months`,
		"@annually":       `has no fixed interval, since months`,
		"@reboot":         `"@reboot": cron_interval reads the macros @hourly, @daily, @midnight and @weekly`,
		"@every 1h":       `"@every 1h": cron_interval reads the macros`,
		"0 * * *":         `"0 * * *" has 4 fields; cron_interval reads five: minute, hour, day of month, month, day of week`,
		"0 0 * * * *":     `has 6 fields`,
		"":                `"" has 0 fields`,
		"60 * * * *":      `"60 * * * *": minute field "60": "60" is not a number from 0 to 59`,
		"0 24 * * *":      `hour field "24": "24" is not a number from 0 to 23`,
		"0 0 * * 8":       `day of week field "8": "8" is not a number from 0 to 7 or a day name such as mon`,
		"0 0 * * fun":     `day of week field "fun": "fun" is not a number from 0 to 7 or a day name`,
		"*/0 * * * *":     `minute field "*/0": the step "0" is not a whole number above 0`,
		"*/x * * * *":     `the step "x" is not a whole number above 0`,
		// A step near the largest int overflowed the loop over the field.
		"* 1/9223372036854775807 * * *": `has no fixed interval: its runs are 1m0s or 23h1m0s apart`,
		"* * * * 1/9223372036854775807": `has no fixed interval: its runs are 1m0s or 144h1m0s apart`,
		"30-10 * * * *":                 `the range "30-10" ends before it starts`,
		"1,,2 * * * *":                  `"" is not a number from 0 to 59`,
		"-5 * * * *":                    `"" is not a number from 0 to 59`,
		"H/7 * * * *":                   `minute field "H/7": in H/7, 7 does not divide the field's 60 values, so the runs depend on the job's name`,
		"H H/5 * * *":                   `in H/5, 5 does not divide the field's 24 values`,
		"H(0-29)/10 * * * *":            `"H(0-29)": H with a range is not read`,
	} {
		if d, err := cronInterval(schedule); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("cronInterval(%q) = %s, %v; want an error containing %q", schedule, d, err, want)
		}
	}
}

// Every N minutes is a fixed interval exactly when N divides the hour, and
// every N hours exactly when N divides the day, whatever the fixed minute.
func TestPropertyCronSteps(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 59).Draw(t, "minutes")
		d, err := cronInterval(fmt.Sprintf("*/%d * * * *", n))
		if (60%n == 0) != (err == nil) {
			t.Fatalf("*/%d: %s, %v", n, d, err)
		}
		if err == nil && d != time.Duration(n)*time.Minute {
			t.Fatalf("*/%d: %s", n, d)
		}

		h := rapid.IntRange(1, 23).Draw(t, "hours")
		m := rapid.IntRange(0, 59).Draw(t, "minute")
		d, err = cronInterval(fmt.Sprintf("%d */%d * * *", m, h))
		if (24%h == 0) != (err == nil) {
			t.Fatalf("%d */%d: %s, %v", m, h, d, err)
		}
		if err == nil && d != time.Duration(h)*time.Hour {
			t.Fatalf("%d */%d: %s", m, h, d)
		}
	})
}

// cron_interval reaches CEL as a duration.
func TestCronIntervalInCEL(t *testing.T) {
	if ok, err := eval(t, "cron_interval(v[0]) == duration('6h')", "17 */6 * * *"); err != nil || !ok {
		t.Fatalf("%v, %v", ok, err)
	}
}
