package rule

import (
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/env"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/overloads"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/interpreter"
)

// Every extractor reaches CEL as list(string), so a relation over numbers,
// durations or ports goes through a typed helper and the checker knows every
// type at load. This file adds what CEL's standard library lacks:
//
//	int(s), uint(s), double(s)  CEL's conversions, with an error naming s
//	duration(s)         a Go duration (90s, 1h30m) or a Prometheus one (2d, 1w)
//	cron_interval(s)    the time between the runs of a cron schedule, as a duration
//	port_split(s)       "8080:80/tcp" as {ip, host, container, protocol}
//	same_set(a, b)      a and b hold the same members, ignoring order and repeats
//	subset(a, b)        every member of a is in b
//	duration * int      a duration scaled by a whole number, and int * duration
//	semver(s), semver_satisfies(v, r)  versions and ranges (semver.go)
//	renovate_match(s, p)  whether Renovate matches s with the pattern p (renovate.go)
//
// A helper that cannot read its argument returns an error, which makes the
// rule unknown with the error in the message: never false and never true.

// stdlib is CEL's standard library without the parts this file replaces and
// without the parts that read the machine.
//
// Its duration(string) reads Go durations only. Its multiplication is one
// binding for every overload, so a duration overload cannot be added beside
// it; the numeric overloads come back below with bindings of their own. Its
// int, uint and double conversions of a string do not name the string they
// failed on; they come back below with bindings that do.
//
// The timestamp accessors that take a time zone, such as getHours('Local'),
// read TZ and the zone files of the machine they run on for a named zone, so
// the same file could be true on a laptop and an error in the release image,
// which has no zone files. They come back below reading named zones from the
// database embedded in errata. They are the only part of the standard
// library that reads the machine: nothing else reads the clock, the
// environment or a file.
func stdlib() cel.EnvOption {
	exclude := []*env.Function{
		{Name: overloads.TypeConvertDuration, Overloads: []*env.Overload{{ID: overloads.StringToDuration}}},
		{Name: overloads.TypeConvertInt, Overloads: []*env.Overload{{ID: overloads.StringToInt}}},
		{Name: overloads.TypeConvertUint, Overloads: []*env.Overload{{ID: overloads.StringToUint}}},
		{Name: overloads.TypeConvertDouble, Overloads: []*env.Overload{{ID: overloads.StringToDouble}}},
		{Name: operators.Multiply},
	}
	for name, id := range timeZoneOverloads {
		exclude = append(exclude, &env.Function{Name: name, Overloads: []*env.Overload{{ID: id}}})
	}
	return cel.StdLib(cel.StdLibSubset(env.NewLibrarySubset().AddExcludedFunctions(exclude...)))
}

// timeZoneOverloads are the timestamp accessors that take a time zone, by
// function name.
var timeZoneOverloads = map[string]string{
	overloads.TimeGetFullYear:     overloads.TimestampToYearWithTz,
	overloads.TimeGetMonth:        overloads.TimestampToMonthWithTz,
	overloads.TimeGetDayOfYear:    overloads.TimestampToDayOfYearWithTz,
	overloads.TimeGetDayOfMonth:   overloads.TimestampToDayOfMonthZeroBasedWithTz,
	overloads.TimeGetDate:         overloads.TimestampToDayOfMonthOneBasedWithTz,
	overloads.TimeGetDayOfWeek:    overloads.TimestampToDayOfWeekWithTz,
	overloads.TimeGetHours:        overloads.TimestampToHoursWithTz,
	overloads.TimeGetMinutes:      overloads.TimestampToMinutesWithTz,
	overloads.TimeGetSeconds:      overloads.TimestampToSecondsWithTz,
	overloads.TimeGetMilliseconds: overloads.TimestampToMillisecondsWithTz,
}

// helpers declares the functions above, with their bindings.
func helpers() []cel.EnvOption {
	list := func(t *cel.Type) *cel.Type { return cel.ListType(t) }
	opts := []cel.EnvOption{
		cel.Function(overloads.TypeConvertDuration,
			cel.Overload("errata_string_to_duration", []*cel.Type{cel.StringType}, cel.DurationType,
				cel.UnaryBinding(stringFunc("duration", func(s string) (ref.Val, error) {
					d, err := parseDuration(s)
					return types.Duration{Duration: d}, err
				})))),
		cel.Function(overloads.TypeConvertInt,
			cel.Overload("errata_string_to_int", []*cel.Type{cel.StringType}, cel.IntType,
				cel.UnaryBinding(convert("int", types.IntType)))),
		cel.Function(overloads.TypeConvertUint,
			cel.Overload("errata_string_to_uint", []*cel.Type{cel.StringType}, cel.UintType,
				cel.UnaryBinding(convert("uint", types.UintType)))),
		cel.Function(overloads.TypeConvertDouble,
			cel.Overload("errata_string_to_double", []*cel.Type{cel.StringType}, cel.DoubleType,
				cel.UnaryBinding(convert("double", types.DoubleType)))),
		cel.Function("cron_interval",
			cel.Overload("errata_cron_interval_string", []*cel.Type{cel.StringType}, cel.DurationType,
				cel.UnaryBinding(stringFunc("cron_interval", func(s string) (ref.Val, error) {
					d, err := cronInterval(s)
					return types.Duration{Duration: d}, err
				})))),
		cel.Function("port_split",
			cel.Overload("errata_port_split_string", []*cel.Type{cel.StringType}, cel.MapType(cel.StringType, cel.StringType),
				cel.UnaryBinding(stringFunc("port_split", func(s string) (ref.Val, error) {
					m, err := splitPort(s)
					return types.NewStringStringMap(types.DefaultTypeAdapter, m), err
				})))),
		cel.Function(operators.Multiply,
			cel.Overload(overloads.MultiplyInt64, []*cel.Type{cel.IntType, cel.IntType}, cel.IntType, cel.BinaryBinding(multiply)),
			cel.Overload(overloads.MultiplyUint64, []*cel.Type{cel.UintType, cel.UintType}, cel.UintType, cel.BinaryBinding(multiply)),
			cel.Overload(overloads.MultiplyDouble, []*cel.Type{cel.DoubleType, cel.DoubleType}, cel.DoubleType, cel.BinaryBinding(multiply)),
			cel.Overload("errata_multiply_duration_int", []*cel.Type{cel.DurationType, cel.IntType}, cel.DurationType,
				cel.BinaryBinding(scale)),
			cel.Overload("errata_multiply_int_duration", []*cel.Type{cel.IntType, cel.DurationType}, cel.DurationType,
				cel.BinaryBinding(func(n, d ref.Val) ref.Val { return scale(d, n) }))),
	}
	var same, sub []cel.FunctionOpt
	for _, t := range setElements {
		same = append(same, cel.Overload(setOverload("same_set", t), []*cel.Type{list(t), list(t)}, cel.BoolType,
			cel.BinaryBinding(setFunc("same_set", func(a, b map[any]bool) bool { return len(a) == len(b) && contains(b, a) }))))
		sub = append(sub, cel.Overload(setOverload("subset", t), []*cel.Type{list(t), list(t)}, cel.BoolType,
			cel.BinaryBinding(setFunc("subset", func(a, b map[any]bool) bool { return contains(b, a) }))))
	}
	opts = append(opts, cel.Function("same_set", same...), cel.Function("subset", sub...))
	opts = append(opts, semverHelpers()...)
	opts = append(opts, renovateHelpers()...)
	for name, id := range timeZoneOverloads {
		opts = append(opts, cel.Function(name, cel.MemberOverload(id, []*cel.Type{cel.TimestampType, cel.StringType}, cel.IntType,
			cel.BinaryBinding(inZone(name)))))
	}
	return opts
}

// inZone is the timestamp accessor name with a time zone argument. 'UTC' and
// a fixed offset such as '+01:00' or '-05:30' go to CEL, which reads them
// without a zone file. A named zone such as 'Europe/Berlin' comes from the
// database embedded in errata, never from the machine.
func inZone(name string) func(ts, zone ref.Val) ref.Val {
	return func(ts, tz ref.Val) ref.Val {
		z, ok := tz.(types.String)
		if !ok {
			return types.MaybeNoSuchOverloadErr(tz)
		}
		t, ok := ts.(types.Timestamp)
		if !ok {
			return types.MaybeNoSuchOverloadErr(ts)
		}
		if z == "UTC" || strings.Contains(string(z), ":") {
			return t.Receive(name, "", []ref.Val{tz})
		}
		loc, err := zone(string(z))
		if err != nil {
			return types.NewErr("%s: %v", name, err)
		}
		return types.Timestamp{Time: t.In(loc)}.Receive(name, "", nil)
	}
}

// setElements are the element types same_set and subset compare: the ones
// whose CEL equality is Go's ==.
var setElements = []*cel.Type{cel.StringType, cel.IntType, cel.UintType, cel.DoubleType, cel.BoolType}

func setOverload(fn string, t *cel.Type) string { return "errata_" + fn + "_list_" + t.String() }

// setCosts charges same_set and subset the size of both lists, so the cost
// limit counts their work as it counts a comprehension's.
func setCosts() []interpreter.CostTrackerOption {
	var opts []interpreter.CostTrackerOption
	for _, t := range setElements {
		for _, fn := range []string{"same_set", "subset"} {
			opts = append(opts, interpreter.OverloadCostTracker(setOverload(fn, t), func(args []ref.Val, _ ref.Val) *uint64 {
				cost := uint64(1)
				for _, a := range args {
					// An argument that is an error has no size; the call
					// returns that error, which then names the cause.
					if s, ok := a.(traits.Sizer); ok {
						if n, ok := s.Size().(types.Int); ok && n > 0 {
							cost += uint64(n)
						}
					}
				}
				return &cost
			}))
		}
	}
	return opts
}

// convert is CEL's own conversion of a string to t, with an error that names
// the string, so the message says which value did not convert.
func convert(name string, t *types.Type) func(ref.Val) ref.Val {
	return stringFunc(name, func(s string) (ref.Val, error) {
		v := types.String(s).ConvertToType(t)
		if err, ok := v.(*types.Err); ok {
			return nil, fmt.Errorf("%q: %s", s, err.Error())
		}
		return v, nil
	})
}

// MaxArgument is the longest string, in bytes, a helper reads. The longest
// real arguments are far shorter: a Go duration is at most 24 bytes
// (2562047h47m16.854775807s, the largest), an int 20 (-9223372036854775808),
// a Compose port 59 ([2001:db8:85a3::8a2e:370:7334]:65535-65535:65535-65535/sctp),
// and a cron schedule that lists every minute, every hour and every day by
// name about 270. A double written out in full has up to 767 significant
// digits. The bound keeps one call short, so an evaluation sees its timeout
// soon after it passes.
const MaxArgument = 1024

// stringFunc adapts f to a CEL binding and names the helper in its error.
func stringFunc(name string, f func(string) (ref.Val, error)) func(ref.Val) ref.Val {
	return func(arg ref.Val) ref.Val {
		s, ok := arg.(types.String)
		if !ok {
			return types.MaybeNoSuchOverloadErr(arg)
		}
		if len(s) > MaxArgument {
			return types.NewErr("%s: the argument is %d bytes; a helper reads at most %d", name, len(s), MaxArgument)
		}
		v, err := f(string(s))
		if err != nil {
			return types.NewErr("%s: %v", name, err)
		}
		return v
	}
}

func multiply(a, b ref.Val) ref.Val {
	m, ok := a.(traits.Multiplier)
	if !ok {
		return types.MaybeNoSuchOverloadErr(a)
	}
	return m.Multiply(b)
}

// scale multiplies a duration by a whole number, and refuses a product that
// time.Duration cannot hold rather than wrapping around.
func scale(d, n ref.Val) ref.Val {
	dur, ok := d.(types.Duration)
	if !ok {
		return types.MaybeNoSuchOverloadErr(d)
	}
	k, ok := n.(types.Int)
	if !ok {
		return types.MaybeNoSuchOverloadErr(n)
	}
	product := int64(dur.Duration) * int64(k)
	if int64(k) != 0 && (product/int64(k) != int64(dur.Duration) || (int64(k) == -1 && dur.Duration == math.MinInt64)) {
		return types.NewErr("duration overflow: %s * %d", dur.Duration, k)
	}
	return types.Duration{Duration: time.Duration(product)}
}

// setFunc adapts a comparison of two member sets to a CEL binding.
func setFunc(name string, f func(a, b map[any]bool) bool) func(ref.Val, ref.Val) ref.Val {
	return func(a, b ref.Val) ref.Val {
		ma, err := members(a)
		if err != nil {
			return types.NewErr("%s: %v", name, err)
		}
		mb, err := members(b)
		if err != nil {
			return types.NewErr("%s: %v", name, err)
		}
		return types.Bool(f(ma, mb))
	}
}

// members returns the distinct members of a CEL list, keyed by their Go
// value. A double NaN never equals itself, in CEL and as a Go map key alike.
func members(l ref.Val) (map[any]bool, error) {
	lister, ok := l.(traits.Lister)
	if !ok {
		return nil, fmt.Errorf("%s is not a list", l.Type().TypeName())
	}
	set := map[any]bool{}
	for it := lister.Iterator(); it.HasNext() == types.True; {
		v := it.Next()
		switch v.(type) {
		case types.String, types.Int, types.Uint, types.Double, types.Bool:
			set[v.Value()] = true
		default:
			return nil, fmt.Errorf("a list member of type %s is not compared as a set member", v.Type().TypeName())
		}
	}
	return set, nil
}

// contains reports whether every member of sub is in set.
func contains(set, sub map[any]bool) bool {
	for k := range sub {
		if !set[k] {
			return false
		}
	}
	return true
}

// prometheusDuration is the duration Prometheus writes in its configuration
// and rules, such as for: 2d or a retention of 15d: a whole number of each of
// y, w, d, h, m, s and ms, each at most once and in that order. Loki and
// Alertmanager read the same form.
var prometheusDuration = regexp.MustCompile(`^(?:(\d+)y)?(?:(\d+)w)?(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?(?:(\d+)ms)?$`)

// prometheusUnits are the units of prometheusDuration's groups, in order.
// Prometheus counts a day as 24 hours and a year as 365 days.
var prometheusUnits = []time.Duration{365 * 24 * time.Hour, 7 * 24 * time.Hour, 24 * time.Hour, time.Hour, time.Minute, time.Second, time.Millisecond}

// parseDuration reads a Go duration, the form Kubernetes and Helm values use
// (90s, 1h30m, 1.5h), or else a Prometheus duration (2d, 1w). Where both
// forms read a string, they read the same duration.
func parseDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	m := prometheusDuration.FindStringSubmatch(s)
	if s == "" || m == nil {
		return 0, fmt.Errorf("%q is not a Go duration such as 90s or 1h30m, nor a Prometheus duration such as 2d or 1w", s)
	}
	var total time.Duration
	for i, unit := range prometheusUnits {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.ParseInt(m[i+1], 10, 64)
		if err != nil || n > int64(math.MaxInt64-total)/int64(unit) {
			return 0, fmt.Errorf("%q is longer than a duration can hold", s)
		}
		total += time.Duration(n) * unit
	}
	return total, nil
}

// portProtocols are the protocols a Compose port names after its slash.
// Compose defines tcp and udp; Docker also accepts sctp.
var portProtocols = []string{"tcp", "udp", "sctp"}

var portNumber = regexp.MustCompile(`^(\d{1,5})(?:-(\d{1,5}))?$`)

// splitPort reads the Compose short syntax for a port,
// [[IP:]HOST:]CONTAINER[/PROTOCOL], where a port is a number or a range such
// as 8000-8010 and an IPv6 address may be in brackets. The protocol is tcp
// when the string names none, as in Compose. An address with no host port,
// such as 127.0.0.1::80, has an empty host.
func splitPort(s string) (map[string]string, error) {
	rest, protocol, named := strings.Cut(s, "/")
	if !named {
		protocol = "tcp"
	} else if !slices.Contains(portProtocols, protocol) {
		return nil, fmt.Errorf("%q: the protocol %q is not one of %s", s, protocol, strings.Join(portProtocols, ", "))
	}
	out := map[string]string{"ip": "", "host": "", "container": rest, "protocol": protocol}
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		out["container"], out["host"] = rest[i+1:], rest[:i]
		if j := strings.LastIndex(out["host"], ":"); j >= 0 {
			ip := strings.TrimSuffix(strings.TrimPrefix(out["host"][:j], "["), "]")
			if _, err := netip.ParseAddr(ip); err != nil {
				return nil, fmt.Errorf("%q: %q is not an IP address", s, out["host"][:j])
			}
			out["ip"], out["host"] = ip, out["host"][j+1:]
		}
		if out["host"] == "" && out["ip"] == "" {
			return nil, fmt.Errorf("%q: the host port before the colon is empty", s)
		}
	}
	for _, key := range []string{"host", "container"} {
		if key == "host" && out[key] == "" {
			continue
		}
		if err := checkPort(out[key]); err != nil {
			return nil, fmt.Errorf("%q: the %s port %w", s, key, err)
		}
	}
	return out, nil
}

// checkPort accepts a port number or an ascending range of them.
func checkPort(p string) error {
	m := portNumber.FindStringSubmatch(p)
	if m == nil {
		return fmt.Errorf("%q is not a number or a range such as 8000-8010", p)
	}
	lo, _ := strconv.Atoi(m[1]) // the pattern allows only digits
	hi := lo
	if m[2] != "" {
		hi, _ = strconv.Atoi(m[2])
	}
	switch {
	case hi > 65535:
		return fmt.Errorf("%q is above 65535", p)
	case hi < lo:
		return fmt.Errorf("%q is a range that ends before it starts", p)
	}
	return nil
}
