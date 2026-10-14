// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package events

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed ScheduleExpression. Next returns the first fire time
// strictly after t, or the zero time when there is none.
type Schedule interface {
	Next(t time.Time) time.Time
}

var errBadSchedule = errors.New("Parameter ScheduleExpression is not valid.")

// ParseSchedule accepts rate(N unit) and AWS's six-field cron(...), both in
// UTC.
func ParseSchedule(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	switch {
	case strings.HasPrefix(expr, "rate(") && strings.HasSuffix(expr, ")"):
		return parseRate(expr[5 : len(expr)-1])
	case strings.HasPrefix(expr, "cron(") && strings.HasSuffix(expr, ")"):
		return parseCron(expr[5 : len(expr)-1])
	}
	return nil, errBadSchedule
}

type rateSchedule time.Duration

func (r rateSchedule) Next(t time.Time) time.Time { return t.Add(time.Duration(r)) }

func parseRate(s string) (Schedule, error) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return nil, errBadSchedule
	}
	n, err := strconv.Atoi(f[0])
	if err != nil || n <= 0 {
		return nil, errBadSchedule
	}
	unit := f[1]
	// AWS insists on "1 minute" but "5 minutes".
	if (n == 1) == strings.HasSuffix(unit, "s") {
		return nil, errBadSchedule
	}
	var d time.Duration
	switch strings.TrimSuffix(unit, "s") {
	case "minute":
		d = time.Minute
	case "hour":
		d = time.Hour
	case "day":
		d = 24 * time.Hour
	default:
		return nil, errBadSchedule
	}
	return rateSchedule(time.Duration(n) * d), nil
}

// cronSchedule is cron(minutes hours day-of-month month day-of-week year).
type cronSchedule struct {
	minutes, hours, months [64]bool
	years                  map[int]bool // nil means every year
	dom                    domSpec
	dow                    dowSpec
}

type domSpec struct {
	any     bool // "?" or "*"
	days    [32]bool
	last    bool // L
	lastWd  bool // LW
	weekday int  // nW, 0 when unused
}

type dowSpec struct {
	any     bool
	days    [8]bool // 1=SUN .. 7=SAT
	lastOf  int     // nL: last such weekday of the month
	nth     int     // d#n
	nthDay  int
	isQmark bool
}

var monthNames = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
var dayNames = map[string]int{"SUN": 1, "MON": 2, "TUE": 3, "WED": 4, "THU": 5, "FRI": 6, "SAT": 7}

func parseCron(s string) (Schedule, error) {
	f := strings.Fields(s)
	if len(f) != 6 {
		return nil, errBadSchedule
	}
	c := &cronSchedule{}
	if err := setField(c.minutes[:], f[0], 0, 59, nil); err != nil {
		return nil, err
	}
	if err := setField(c.hours[:], f[1], 0, 23, nil); err != nil {
		return nil, err
	}
	if err := setField(c.months[:], strings.ToUpper(f[3]), 1, 12, monthNames); err != nil {
		return nil, err
	}
	if f[5] != "*" {
		var years [2200 - 1970 + 1]bool
		if err := setOffsetField(years[:], f[5], 1970, 2199); err != nil {
			return nil, err
		}
		c.years = map[int]bool{}
		for i, ok := range years {
			if ok {
				c.years[1970+i] = true
			}
		}
	}
	domQ, dowQ := f[2] == "?", f[4] == "?"
	// Exactly one of day-of-month and day-of-week must be "?".
	if domQ == dowQ {
		return nil, errBadSchedule
	}
	if err := c.parseDOM(f[2]); err != nil {
		return nil, err
	}
	if err := c.parseDOW(strings.ToUpper(f[4])); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *cronSchedule) parseDOM(s string) error {
	switch {
	case s == "?" || s == "*":
		c.dom.any = true
	case s == "L":
		c.dom.last = true
	case s == "LW":
		c.dom.lastWd = true
	case strings.HasSuffix(s, "W"):
		n, err := strconv.Atoi(strings.TrimSuffix(s, "W"))
		if err != nil || n < 1 || n > 31 {
			return errBadSchedule
		}
		c.dom.weekday = n
	default:
		return setField(c.dom.days[:], s, 1, 31, nil)
	}
	return nil
}

func (c *cronSchedule) parseDOW(s string) error {
	switch {
	case s == "?":
		c.dow.any, c.dow.isQmark = true, true
	case s == "*":
		c.dow.any = true
	case s == "L":
		c.dow.days[7] = true
	case strings.HasSuffix(s, "L"):
		d, err := dayValue(strings.TrimSuffix(s, "L"))
		if err != nil {
			return err
		}
		c.dow.lastOf = d
	case strings.Contains(s, "#"):
		a, b, _ := strings.Cut(s, "#")
		d, err := dayValue(a)
		if err != nil {
			return err
		}
		n, err := strconv.Atoi(b)
		if err != nil || n < 1 || n > 5 {
			return errBadSchedule
		}
		c.dow.nthDay, c.dow.nth = d, n
	default:
		return setField(c.dow.days[:], s, 1, 7, dayNames)
	}
	return nil
}

func dayValue(s string) (int, error) {
	if d, ok := dayNames[s]; ok {
		return d, nil
	}
	d, err := strconv.Atoi(s)
	if err != nil || d < 1 || d > 7 {
		return 0, errBadSchedule
	}
	return d, nil
}

// setField parses a comma list of *, n, a-b, */n, a/n, a-b/n into bits.
func setField(bits []bool, s string, lo, hi int, names map[string]int) error {
	val := func(x string) (int, error) {
		if v, ok := names[x]; ok {
			return v, nil
		}
		v, err := strconv.Atoi(x)
		if err != nil || v < lo || v > hi {
			return 0, errBadSchedule
		}
		return v, nil
	}
	for _, part := range strings.Split(s, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return errBadSchedule
			}
			step = n
		}
		start, end := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if start, err = val(a); err != nil {
				return err
			}
			if end, err = val(b); err != nil {
				return err
			}
			if end < start {
				return errBadSchedule
			}
		default:
			v, err := val(rng)
			if err != nil {
				return err
			}
			start = v
			if !hasStep {
				end = v
			}
		}
		for i := start; i <= end; i += step {
			bits[i] = true
		}
	}
	return nil
}

// setOffsetField is setField for ranges that don't start near zero (years).
func setOffsetField(bits []bool, s string, lo, hi int) error {
	tmp := make([]bool, hi+1)
	if err := setField(tmp, s, lo, hi, nil); err != nil {
		return err
	}
	for i := lo; i <= hi; i++ {
		bits[i-lo] = tmp[i]
	}
	return nil
}

func (c *cronSchedule) Next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	for ; day.Year() <= 2199; day = day.AddDate(0, 0, 1) {
		if c.years != nil && !c.years[day.Year()] {
			// Skip to the next January 1st.
			day = time.Date(day.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
			continue
		}
		if !c.months[int(day.Month())] {
			day = time.Date(day.Year(), day.Month()+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
			continue
		}
		if !c.dayMatches(day) {
			continue
		}
		for h := 0; h < 24; h++ {
			if !c.hours[h] {
				continue
			}
			for m := 0; m < 60; m++ {
				if !c.minutes[m] {
					continue
				}
				at := day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
				if !at.Before(t) {
					return at
				}
			}
		}
	}
	return time.Time{}
}

func daysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func (c *cronSchedule) dayMatches(day time.Time) bool {
	y, m, d := day.Date()
	last := daysIn(y, m)
	wd := int(day.Weekday()) + 1 // 1=SUN

	// One side is always "?", so only the other decides.
	if !c.dow.isQmark {
		switch {
		case c.dow.any:
			return true
		case c.dow.lastOf != 0:
			return wd == c.dow.lastOf && d+7 > last
		case c.dow.nth != 0:
			return wd == c.dow.nthDay && (d-1)/7+1 == c.dow.nth
		default:
			return c.dow.days[wd]
		}
	}
	switch {
	case c.dom.any:
		return true
	case c.dom.last:
		return d == last
	case c.dom.lastWd:
		return d == nearestWeekday(y, m, last)
	case c.dom.weekday != 0:
		target := c.dom.weekday
		if target > last {
			return false
		}
		return d == nearestWeekday(y, m, target)
	default:
		return c.dom.days[d]
	}
}

// nearestWeekday is the weekday nearest to day d without leaving the month,
// as cron's W means.
func nearestWeekday(y int, m time.Month, d int) int {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	switch t.Weekday() {
	case time.Saturday:
		if d == 1 {
			return 3 // Monday the 3rd
		}
		return d - 1
	case time.Sunday:
		if d == daysIn(y, m) {
			return d - 2
		}
		return d + 1
	}
	return d
}
