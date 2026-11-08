package rule

import (
	"cel.dev/cel-go/common/cost"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
)

// calendarCost charges a calendar_interval call for its work: one, a unit
// for each byte of the event it parses, and one for each step its fields'
// components take (calendarSteps), so a comprehension over many events
// pays for each, as the cost limit holds it.
func calendarCost() cost.TrackerOption {
	return cost.OverloadTracker("errata_calendar_interval_string", func(args []ref.Val, _ ref.Val) *uint64 {
		n := uint64(1)
		if s, ok := args[0].(types.String); ok {
			n += uint64(len(s)) + uint64(calendarSteps(string(s))) //nolint:gosec // G115: a count, never below 0
		}
		return &n
	})
}

// calendarSteps is the steps the fields of the event text take, at most
// maxCalValues for each that calendar_interval reads, and 0 for an event
// it refuses to parse.
func calendarSteps(text string) int {
	c, err := parseCalendar(text)
	if err != nil {
		return 0
	}
	n := 0
	for i, ch := range [][]calComponent{c.us, c.minute, c.hour} {
		if ch == nil {
			ch = []calComponent{{0, noStop, 1}}
		}
		n += min(chainSteps(ch, calFields[i].span), maxCalValues+1)
	}
	return n
}
