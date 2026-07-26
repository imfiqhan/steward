package steward

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronExpr is a parsed five-field cron expression:
// minute hour day-of-month month day-of-week.
// Supported syntax per field: * , - / and combinations ("*/15", "1-5",
// "1,3,5", "10-50/10"). Day-of-week: 0–6, 0 = Sunday (7 accepted as Sunday).
type cronExpr struct {
	minute, hour, dom, month, dow uint64 // bitmasks
}

type cronField struct {
	min, max int
}

var cronFields = [5]cronField{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day of month
	{1, 12}, // month
	{0, 6},  // day of week
}

// parseCron parses "m h dom mon dow".
func parseCron(spec string) (*cronExpr, error) {
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron %q: want 5 fields (minute hour dom month dow), got %d", spec, len(parts))
	}
	var masks [5]uint64
	for i, part := range parts {
		mask, err := parseCronField(part, cronFields[i].min, cronFields[i].max)
		if err != nil {
			return nil, fmt.Errorf("cron %q field %d: %w", spec, i+1, err)
		}
		masks[i] = mask
	}
	return &cronExpr{minute: masks[0], hour: masks[1], dom: masks[2], month: masks[3], dow: masks[4]}, nil
}

func parseCronField(expr string, min, max int) (uint64, error) {
	var mask uint64
	for _, part := range strings.Split(expr, ",") {
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step %q", stepPart)
			}
			step = n
		}
		lo, hi := min, max
		switch {
		case rangePart == "*":
			// full range
		case strings.Contains(rangePart, "-"):
			loS, hiS, _ := strings.Cut(rangePart, "-")
			var err1, err2 error
			lo, err1 = strconv.Atoi(loS)
			hi, err2 = strconv.Atoi(hiS)
			if err1 != nil || err2 != nil {
				return 0, fmt.Errorf("bad range %q", rangePart)
			}
		default:
			n, err := strconv.Atoi(rangePart)
			if err != nil {
				return 0, fmt.Errorf("bad value %q", rangePart)
			}
			lo, hi = n, n
			if hasStep { // "5/15" = every 15 starting at 5
				hi = max
			}
		}
		// Day-of-week 7 = Sunday.
		if max == 6 {
			if lo == 7 {
				lo = 0
			}
			if hi == 7 {
				hi = 0
			}
		}
		if lo < min || hi > max || lo > hi {
			return 0, fmt.Errorf("value out of range %d-%d in %q", min, max, part)
		}
		for v := lo; v <= hi; v += step {
			mask |= 1 << uint(v)
		}
	}
	if mask == 0 {
		return 0, fmt.Errorf("empty field %q", expr)
	}
	return mask, nil
}

func bit(mask uint64, v int) bool { return mask&(1<<uint(v)) != 0 }

// matches reports whether t (minute resolution) satisfies the expression.
// Standard cron semantics: when both dom and dow are restricted, either may
// match; when only one is restricted, it must match.
func (c *cronExpr) matches(t time.Time) bool {
	if !bit(c.minute, t.Minute()) || !bit(c.hour, t.Hour()) || !bit(c.month, int(t.Month())) {
		return false
	}
	full := func(mask uint64, f cronField) bool {
		for v := f.min; v <= f.max; v++ {
			if !bit(mask, v) {
				return false
			}
		}
		return true
	}
	domOK := bit(c.dom, t.Day())
	dowOK := bit(c.dow, int(t.Weekday()))
	domAll := full(c.dom, cronFields[2])
	dowAll := full(c.dow, cronFields[4])
	switch {
	case domAll && dowAll:
		return true
	case domAll:
		return dowOK
	case dowAll:
		return domOK
	default:
		return domOK || dowOK
	}
}
