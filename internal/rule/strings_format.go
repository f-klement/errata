package rule

import (
	"fmt"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

// numberSize is more bytes than any clause writes for a number, a duration
// or a timestamp: the longest is %f of the largest double at the extension's
// greatest precision, 100, which is 410 bytes, and %s of the smallest double,
// 327.
const numberSize = 512

// formatGuard refuses a format whose result could be more than b.built
// bytes. The size is an upper bound, worked out without formatting.
func formatGuard(b bounds, format, list ref.Val) error {
	f, ok1 := format.(types.String)
	args, ok2 := list.(traits.Lister)
	if !ok1 || !ok2 {
		return nil
	}
	if size := formattedSize(string(f), args, b.built); size > b.built {
		return &formatTooLong{b: b, counted: size}
	}
	return nil
}

// formatTooLong is formatGuard's refusal, with what its count reached, which
// the refused call is charged for (stringCosts).
type formatTooLong struct {
	b       bounds
	counted int64
}

func (e *formatTooLong) Error() string {
	return fmt.Sprintf("the result could be more than the %d bytes one call may build, since a longer string exceeds the cost limit of %d by itself", e.b.built, e.b.cost)
}

// formattedSize is at least the length in bytes of format.format(args), or
// more than limit, where it stops counting. Each clause takes the next
// argument, as cel-go's formatter does, and %% takes none. The format
// string's own bytes count whole, clauses included.
func formattedSize(format string, args traits.Lister, limit int64) int64 {
	size := int64(len(format))
	count, ok := args.Size().(types.Int)
	if !ok {
		return size
	}
	next := types.Int(0)
	for i := 0; i < len(format) && size <= limit; i++ {
		if format[i] != '%' {
			continue
		}
		if i+1 < len(format) && format[i+1] == '%' {
			i++
			continue
		}
		if next >= count {
			// cel-go refuses a clause with no argument, and stops.
			break
		}
		j, verb := clauseVerb(format, i+1)
		size += clauseSize(verb, args.Get(next), limit-size)
		next++
		i = j
	}
	return size
}

// clauseVerb is the offset and the byte of the verb of the clause whose
// precision, such as .2 in %.2f, starts at off: the verb comes after it.
// The verb is 0 when the format ends first.
func clauseVerb(format string, off int) (int, byte) {
	j := off
	for j < len(format) && (format[j] == '.' || '0' <= format[j] && format[j] <= '9') {
		j++
	}
	if j < len(format) {
		return j, format[j]
	}
	return j, 0
}

// clauseSize is at least the length of what the clause of verb writes for v,
// or more than limit. %x and %X write two bytes for each byte of a string,
// %s writes v as valueSize counts it, and every other verb writes a number.
func clauseSize(verb byte, v ref.Val, limit int64) int64 {
	switch verb {
	case 'x', 'X':
		switch v := v.(type) {
		case types.String:
			return 2 * int64(len(v))
		case types.Bytes:
			return 2 * int64(len(v))
		}
	case 's':
		return valueSize(v, limit)
	}
	return numberSize
}

// valueSize is at least the length of what %s writes for v, or more than
// limit, where it stops counting: a list is written [a, b], a map {k: v, l: w}.
// A value of a type %s refuses counts as a number.
func valueSize(v ref.Val, limit int64) int64 {
	if size, ok := scalarSize(v); ok {
		return size
	}
	switch v := v.(type) {
	case traits.Mapper:
		return mapSize(v, limit)
	case traits.Lister:
		return listSize(v, limit)
	}
	return numberSize
}

// scalarSize is at least the length of what %s writes for the scalar v,
// and false when v is no scalar it knows.
func scalarSize(v ref.Val) (int64, bool) {
	switch v := v.(type) {
	case types.String:
		return int64(len(v)), true
	case types.Bytes:
		return int64(len(v)), true
	case types.Bool:
		return int64(len("false")), true
	case types.Null:
		return int64(len("null")), true
	case types.Int, types.Uint:
		return int64(len("-9223372036854775808")), true
	case *types.Type:
		return int64(len(v.TypeName())), true
	}
	return 0, false
}

// mapSize is valueSize of a map, written {k: v, l: w}.
func mapSize(v traits.Mapper, limit int64) int64 {
	size := int64(len("{}"))
	for it := v.Iterator(); it.HasNext() == types.True && size <= limit; {
		key := it.Next()
		// A key is a scalar, which no limit cuts short.
		size += valueSize(key, limit) + int64(len(": , "))
		if val, found := v.Find(key); found {
			size += valueSize(val, limit-size)
		}
	}
	return size
}

// listSize is valueSize of a list, written [a, b].
func listSize(v traits.Lister, limit int64) int64 {
	size := int64(len("[]"))
	for it := v.Iterator(); it.HasNext() == types.True && size <= limit; {
		size += valueSize(it.Next(), limit-size) + int64(len(", "))
	}
	return size
}
