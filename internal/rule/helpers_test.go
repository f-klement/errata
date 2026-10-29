package rule

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"pgregory.net/rapid"
)

// eval compiles expr over the extractor v and evaluates it with values.
func eval(t *testing.T, expr string, values ...string) (bool, error) {
	t.Helper()
	a, err := Compile(expr, []string{"v"}, Lists{})
	if err != nil {
		t.Fatal(err)
	}
	return a.Eval(context.Background(), map[string][]string{"v": values}, nil, nil)
}

func TestParseDuration(t *testing.T) {
	day := 24 * time.Hour
	for in, want := range map[string]time.Duration{
		// Go durations, as Kubernetes and Helm values write them.
		"90s": 90 * time.Second, "1h30m": 90 * time.Minute, "1.5h": 90 * time.Minute, "300ms": 300 * time.Millisecond,
		"720h0m0s": 30 * day, "-1h": -time.Hour, "0": 0,
		// Prometheus durations.
		"2d": 2 * day, "1w": 7 * day, "1y": 365 * day, "1w3d": 10 * day, "1d12h": 36 * time.Hour,
		"1y2w3d4h5m6s7ms": 365*day + 14*day + 3*day + 4*time.Hour + 5*time.Minute + 6*time.Second + 7*time.Millisecond,
		// The most of one unit a duration holds, one under the refused 106752d.
		"106751d": 106751 * day, "15250w": 15250 * 7 * day,
	} {
		got, err := parseDuration(in)
		if err != nil || got != want {
			t.Errorf("parseDuration(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"", "2 days", "1d1.5h", "3h2d", "1D", "d", "1x", "106752d", "9223372036854775808ms", "293y"} {
		if d, err := parseDuration(in); err == nil {
			t.Errorf("parseDuration(%q) = %s, want an error", in, d)
		}
	}
	_, err := parseDuration("2 days")
	if err == nil || err.Error() != `"2 days" is not a Go duration such as 90s or 1h30m, nor a Prometheus duration such as 2d or 1w` {
		t.Errorf("error = %v", err)
	}
	if _, err := parseDuration("106752d"); err == nil || err.Error() != `"106752d" is longer than a duration can hold` {
		t.Errorf("overflow error = %v", err)
	}
}

// Whatever duration Go prints, duration() reads back.
func TestPropertyDurationReadsGo(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := time.Duration(rapid.Int64().Draw(t, "d"))
		got, err := parseDuration(d.String())
		if err != nil || got != d {
			t.Fatalf("parseDuration(%q) = %s, %v", d.String(), got, err)
		}
	})
}

// A Prometheus duration is the sum of its parts.
func TestPropertyDurationReadsPrometheus(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var (
			b    strings.Builder
			want time.Duration
		)
		for i, unit := range []string{"y", "w", "d", "h", "m", "s", "ms"} {
			if n := rapid.IntRange(-1, 99).Draw(t, unit); n >= 0 {
				fmt.Fprintf(&b, "%d%s", n, unit)
				want += time.Duration(n) * prometheusUnits[i]
			}
		}
		if b.Len() == 0 {
			return
		}
		got, err := parseDuration(b.String())
		if err != nil || got != want {
			t.Fatalf("parseDuration(%q) = %s, %v; want %s", b.String(), got, err, want)
		}
	})
}

func TestSplitPort(t *testing.T) {
	cases := map[string]map[string]string{
		"8080:80/tcp":           {"ip": "", "host": "8080", "container": "80", "protocol": "tcp"},
		"8080:80":               {"ip": "", "host": "8080", "container": "80", "protocol": "tcp"},
		"80":                    {"ip": "", "host": "", "container": "80", "protocol": "tcp"},
		"53:53/udp":             {"ip": "", "host": "53", "container": "53", "protocol": "udp"},
		"127.0.0.1:8001:8001":   {"ip": "127.0.0.1", "host": "8001", "container": "8001", "protocol": "tcp"},
		"127.0.0.1::5000":       {"ip": "127.0.0.1", "host": "", "container": "5000", "protocol": "tcp"},
		"::1:6000:6000":         {"ip": "::1", "host": "6000", "container": "6000", "protocol": "tcp"},
		"[::1]:6001:6001/sctp":  {"ip": "::1", "host": "6001", "container": "6001", "protocol": "sctp"},
		"8000-8010:8000-8010":   {"ip": "", "host": "8000-8010", "container": "8000-8010", "protocol": "tcp"},
		"9090-9091:8080-8081":   {"ip": "", "host": "9090-9091", "container": "8080-8081", "protocol": "tcp"},
		"0.0.0.0:65535:1/udp":   {"ip": "0.0.0.0", "host": "65535", "container": "1", "protocol": "udp"},
		"6060-6061:6060/tcp":    {"ip": "", "host": "6060-6061", "container": "6060", "protocol": "tcp"},
		"203.0.113.5:443:8443":  {"ip": "203.0.113.5", "host": "443", "container": "8443", "protocol": "tcp"},
		"[fe80::1]:22:22/tcp":   {"ip": "fe80::1", "host": "22", "container": "22", "protocol": "tcp"},
		"[2001:db8::1]:8080:80": {"ip": "2001:db8::1", "host": "8080", "container": "80", "protocol": "tcp"},
	}
	for in, want := range cases {
		got, err := splitPort(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("splitPort(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for in, want := range map[string]string{
		"":              `"": the container port "" is not a number or a range such as 8000-8010`,
		"8080:":         `"8080:": the container port "" is not a number`,
		":80":           `":80": the host port before the colon is empty`,
		"8080:80/http":  `"8080:80/http": the protocol "http" is not one of tcp, udp, sctp`,
		"8080:80/":      `the protocol "" is not one of tcp, udp, sctp`,
		"70000:80":      `"70000:80": the host port "70000" is above 65535`,
		"8010-8000:80":  `the host port "8010-8000" is a range that ends before it starts`,
		"host:8080:80":  `"host:8080:80": "host" is not an IP address`,
		":8080:80":      `":8080:80": "" is not an IP address`,
		"${PORT}:80":    `the host port "${PORT}" is not a number`,
		"1:2:3":         `"1" is not an IP address`,
		"8080:80/tcp/x": `the protocol "tcp/x" is not one of`,
	} {
		if got, err := splitPort(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("splitPort(%q) = %v, %v; want an error containing %q", in, got, err, want)
		}
	}
}

// port_split reaches CEL as a map, read by field or by key.
func TestPortSplitInCEL(t *testing.T) {
	ok, err := eval(t, "port_split(v[0]).host == '8080' && port_split(v[0])['container'] == '80' && port_split(v[0]).protocol == 'tcp' && port_split(v[0]).ip == ''", "8080:80")
	if err != nil || !ok {
		t.Fatalf("got %v, %v", ok, err)
	}
}

func TestSets(t *testing.T) {
	cases := []struct {
		expr   string
		values []string
		want   bool
	}{
		{"same_set(v, ['b', 'a'])", []string{"a", "b", "a"}, true},
		{"same_set(v, ['a'])", []string{"a", "b"}, false},
		{"same_set(v, ['a', 'b', 'c'])", []string{"a", "b"}, false},
		{"same_set(v, [])", nil, true},
		{"subset(v, ['a', 'b', 'c'])", []string{"c", "a"}, true},
		{"subset(v, ['a'])", []string{"a", "b"}, false},
		{"subset([], v)", []string{"a"}, true},
		{"same_set(v.map(x, int(x)), [80, 443])", []string{"443", "80", "080"}, true},
		{"subset(v.map(x, double(x)), [0.5, 1.0])", []string{"1", "0.50"}, true},
		{"same_set(v.map(x, uint(x)), [1u])", []string{"1", "01"}, true},
		{"same_set(v.map(x, x == 'y'), [true, false])", []string{"y", "n"}, true},
		{"same_set(v.map(x, x == 'y'), [true])", []string{"y", "n"}, false},
		// NaN equals nothing in CEL, itself included, and so is in no set.
		{"subset(v.map(x, double(x)), [double('NaN')])", []string{"NaN"}, false},
	}
	for _, tc := range cases {
		got, err := eval(t, tc.expr, tc.values...)
		if err != nil || got != tc.want {
			t.Errorf("%s over %q = %v, %v; want %v", tc.expr, tc.values, got, err, tc.want)
		}
	}
}

// The set functions count toward the cost limit by the size of their lists,
// so a large set comparison cannot pass as one step.
func TestSetsCountTowardTheCostLimit(t *testing.T) {
	a, err := compile("same_set(v, v)", []string{"v"}, limits{cost: 100, timeout: time.Second, grace: stopGrace})
	if err != nil {
		t.Fatal(err)
	}
	values := make([]string, 60)
	for i := range values {
		values[i] = strconv.Itoa(i)
	}
	if _, err := a.Eval(context.Background(), map[string][]string{"v": values}, nil, nil); err == nil || err.Error() != "the evaluation exceeded the cost limit of 100" {
		t.Fatalf("120 members under a limit of 100: error = %v", err)
	}
	if ok, err := a.Eval(context.Background(), map[string][]string{"v": values[:40]}, nil, nil); err != nil || !ok {
		t.Fatalf("80 members under a limit of 100: %v, %v", ok, err)
	}
}

// For any two lists, same_set holds exactly when each is a subset of the
// other, and subset agrees with a membership test written out.
func TestPropertySets(t *testing.T) {
	a, err := Compile("same_set(v, w) == (subset(v, w) && subset(w, v))", []string{"v", "w"}, Lists{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := Compile("subset(v, w) == v.all(x, x in w)", []string{"v", "w"}, Lists{})
	if err != nil {
		t.Fatal(err)
	}
	rapid.Check(t, func(t *rapid.T) {
		member := rapid.SampledFrom([]string{"a", "b", "c", "d"})
		values := map[string][]string{"v": rapid.SliceOf(member).Draw(t, "v"), "w": rapid.SliceOf(member).Draw(t, "w")}
		for _, x := range []*assertion{a.(*assertion), sub.(*assertion)} {
			if ok, err := x.Eval(context.Background(), values, nil, nil); err != nil || !ok {
				t.Fatalf("%v: %v, %v", values, ok, err)
			}
		}
	})
}

// Replacing CEL's multiplication keeps it for numbers, with its overflow
// check, and adds a duration scaled by a whole number.
func TestMultiply(t *testing.T) {
	for _, expr := range []string{
		"3 * 4 == 12 && v[0] != ''", "3u * 4u == 12u && v[0] != ''", "1.5 * 2.0 == 3.0 && v[0] != ''", "-2 * 6 == -12 && v[0] != ''",
		"duration(v[0]) * 2 == duration('2h')", "2 * duration(v[0]) == duration('2h')",
		"duration(v[0]) * 0 == duration('0s')", "duration(v[0]) * -1 == duration('-1h')",
	} {
		if ok, err := eval(t, expr, "1h"); err != nil || !ok {
			t.Errorf("%s: %v, %v", expr, ok, err)
		}
	}
	for expr, want := range map[string]string{
		"9223372036854775807 * size(v) * 2 > 0":                        "integer overflow",
		"duration(v[0]) * 9223372036854775807 > duration('0s')":        "duration overflow: 1h0m0s * 9223372036854775807",
		"duration(v[0]) * -9223372036854775807 * -2 > duration('0s')":  "duration overflow",
		"-1 * (duration(v[0]) * 9223372036854775807) > duration('0s')": "duration overflow",
	} {
		if _, err := eval(t, expr, "1h"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want %q", expr, err, want)
		}
	}
	// The one product that overflows and still divides back.
	if v := scale(durationVal(time.Duration(-1<<63)), intVal(-1)); !strings.Contains(fmt.Sprint(v), "duration overflow") {
		t.Errorf("min duration * -1 = %v, want an overflow", v)
	}
}

// int and double are CEL's own conversions, and a failed parse is an error.
func TestNumberConversions(t *testing.T) {
	if ok, err := eval(t, "int(v[0]) + 1 == 43 && double(v[1]) == 2.5", "42", "2.5"); err != nil || !ok {
		t.Fatalf("%v, %v", ok, err)
	}
	if _, err := eval(t, "int(v[0]) > 0", "4.2"); err == nil {
		t.Fatal("int('4.2') did not fail")
	}
}

func durationVal(d time.Duration) ref.Val { return types.Duration{Duration: d} }
func intVal(n int64) ref.Val              { return types.Int(n) }
