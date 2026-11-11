// Package schedule parses standard five-field cron expressions and answers
// "when is the next run" and "which run is due". It depends on the standard
// library only, and all times are UTC.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed cron expression: minute hour day-of-month month day-of-week.
type Cron struct {
	minute, hour, dom, month, dow bits
	// domStar and dowStar record that a day field started with '*'. When both
	// day fields are restricted, a day matches if either does (classic cron).
	domStar, dowStar bool
}

type bits uint64

func (b bits) has(n int) bool { return b&(1<<uint(n)) != 0 }

var macros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
var dayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Parse reads a five-field expression such as "*/15 9-17 * * mon-fri" or one
// of the macros @hourly, @daily, @weekly, @monthly and @yearly. Each field
// accepts '*', numbers, ranges (a-b), steps (*/n, a-b/n, a/n) and comma lists;
// months and weekdays also accept three-letter English names. Sunday is 0 or 7.
func Parse(expr string) (Cron, error) {
	expr = strings.TrimSpace(expr)
	if m, ok := macros[strings.ToLower(expr)]; ok {
		expr = m
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Cron{}, fmt.Errorf("cron expression %q needs 5 fields (minute hour day-of-month month day-of-week), got %d", expr, len(fields))
	}
	var c Cron
	var err error
	if c.minute, _, err = parseField(fields[0], 0, 59, nil, "minute"); err != nil {
		return Cron{}, err
	}
	if c.hour, _, err = parseField(fields[1], 0, 23, nil, "hour"); err != nil {
		return Cron{}, err
	}
	if c.dom, c.domStar, err = parseField(fields[2], 1, 31, nil, "day-of-month"); err != nil {
		return Cron{}, err
	}
	if c.month, _, err = parseField(fields[3], 1, 12, monthNames, "month"); err != nil {
		return Cron{}, err
	}
	if c.dow, c.dowStar, err = parseField(fields[4], 0, 7, dayNames, "day-of-week"); err != nil {
		return Cron{}, err
	}
	if c.dow.has(7) { // 7 is Sunday too
		c.dow |= 1
	}
	return c, nil
}

func parseField(s string, lo, hi int, names []string, what string) (bits, bool, error) {
	star := strings.HasPrefix(s, "*")
	var out bits
	for _, part := range strings.Split(s, ",") {
		if part == "" {
			return 0, false, fmt.Errorf("%s field %q has an empty list item", what, s)
		}
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil || n < 1 {
				return 0, false, fmt.Errorf("%s field %q has an invalid step %q", what, s, stepPart)
			}
			step = n
		}

		var from, to int
		switch {
		case rangePart == "*":
			from, to = lo, hi
			if what == "day-of-week" {
				to = 6 // '*' means 0-6; 7 would only repeat Sunday
			}
		case strings.Contains(rangePart, "-"):
			a, b, _ := strings.Cut(rangePart, "-")
			var err error
			if from, err = value(a, lo, hi, names, what, s); err != nil {
				return 0, false, err
			}
			if to, err = value(b, lo, hi, names, what, s); err != nil {
				return 0, false, err
			}
			if from > to {
				return 0, false, fmt.Errorf("%s field %q has a range that runs backwards", what, s)
			}
		default:
			var err error
			if from, err = value(rangePart, lo, hi, names, what, s); err != nil {
				return 0, false, err
			}
			to = from
			if hasStep { // "a/n" means a through the maximum
				to = hi
			}
		}
		for n := from; n <= to; n += step {
			out |= 1 << uint(n)
		}
	}
	return out, star, nil
}

func value(tok string, lo, hi int, names []string, what, field string) (int, error) {
	if names != nil {
		for i, n := range names {
			if strings.EqualFold(tok, n) {
				return i + lo, nil
			}
		}
	}
	n, err := strconv.Atoi(tok)
	if err != nil {
		return 0, fmt.Errorf("%s field %q: %q is not a number or name", what, field, tok)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("%s field %q: %d is outside %d-%d", what, field, n, lo, hi)
	}
	return n, nil
}

func (c Cron) dayMatches(t time.Time) bool {
	domOK := c.dom.has(t.Day())
	dowOK := c.dow.has(int(t.Weekday()))
	if !c.domStar && !c.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// Next returns the first run strictly after the given time, to the minute, or
// the zero time if there is none within five years (for example "0 0 31 2 *").
func (c Cron) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case !c.month.has(int(t.Month())):
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case !c.hour.has(t.Hour()):
			t = t.Truncate(time.Hour).Add(time.Hour)
		case !c.minute.has(t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// maxMissed bounds how many missed runs Due will count.
const maxMissed = 10000

// Due finds the most recent run in (after, now]. missed is how many runs fell
// in that window (so missed-1 were skipped). ok is false when none is due.
func (c Cron) Due(after, now time.Time) (latest time.Time, missed int, ok bool) {
	t := c.Next(after)
	for !t.IsZero() && !t.After(now) && missed < maxMissed {
		latest = t
		missed++
		t = c.Next(t)
	}
	return latest, missed, missed > 0
}
