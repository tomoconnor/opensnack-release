// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package events

import (
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestScheduleNext(t *testing.T) {
	cases := []struct {
		expr, from, want string
	}{
		{"rate(1 minute)", "2026-12-01 10:00", "2026-12-01 10:01"},
		{"rate(5 minutes)", "2026-12-01 10:00", "2026-12-01 10:05"},
		{"rate(2 hours)", "2026-12-01 10:00", "2026-12-01 12:00"},
		{"rate(1 day)", "2026-12-01 10:00", "2026-12-02 10:00"},
		{"cron(0 6 * * ? *)", "2026-12-13 05:00", "2026-12-13 06:00"},
		{"cron(0 6 * * ? *)", "2026-12-13 06:00", "2026-12-14 06:00"},
		{"cron(0/15 * * * ? *)", "2026-12-13 06:07", "2026-12-13 06:15"},
		{"cron(5,35 14 * * ? *)", "2026-12-13 14:06", "2026-12-13 14:35"},
		{"cron(0 18 ? * MON-FRI *)", "2026-12-12 19:00", "2026-12-14 18:00"}, // Sat -> Mon
		{"cron(0 10 ? * 6L 2026)", "2026-12-01 00:00", "2026-12-25 10:00"},   // last Friday
		{"cron(0 10 ? * 2#1 *)", "2026-12-01 00:00", "2026-12-07 10:00"},     // first Monday
		{"cron(0 10 ? * MON#3 *)", "2026-12-01 00:00", "2026-12-21 10:00"},   // third Monday
		{"cron(0 0 L * ? *)", "2026-02-02 00:00", "2026-02-28 00:00"},
		{"cron(0 0 L * ? *)", "2028-02-02 00:00", "2028-02-29 00:00"},
		{"cron(0 0 LW * ? *)", "2026-05-01 00:00", "2026-05-29 00:00"},  // May 31 2026 is Sunday
		{"cron(0 0 15W * ? *)", "2026-08-01 00:00", "2026-08-14 00:00"}, // Aug 15 2026 is Saturday
		{"cron(0 0 1W * ? *)", "2026-08-01 00:00", "2026-08-03 00:00"},  // Aug 1 is Saturday -> Mon 3rd
		{"cron(0 0 1 JAN,JUL ? *)", "2026-02-01 00:00", "2026-07-01 00:00"},
		{"cron(0 0 24 12 ? 2027-2030)", "2026-01-01 00:00", "2027-12-24 00:00"},
		{"cron(0 6 ? * L *)", "2026-12-13 07:00", "2026-12-19 06:00"}, // L alone is Saturday
	}
	for _, c := range cases {
		s, err := ParseSchedule(c.expr)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if got := s.Next(ts(c.from)); !got.Equal(ts(c.want)) {
			t.Errorf("%s from %s: got %s, want %s", c.expr, c.from, got.Format("2006-01-02 15:04 Mon"), c.want)
		}
	}
}

func TestScheduleInvalid(t *testing.T) {
	for _, e := range []string{
		"rate(5 minute)", "rate(1 minutes)", "rate(0 minutes)", "rate(5 weeks)", "rate(5)",
		"cron(0 6 * * *)", "cron(0 6 * * * *)", "cron(0 6 ? * ? *)", "cron(60 6 * * ? *)",
		"cron(0 6 ? * 8 *)", "cron(0 6 ? * 2#6 *)", "every day", "cron(0 0 32 * ? *)",
	} {
		if _, err := ParseSchedule(e); err == nil {
			t.Errorf("%s: want error", e)
		}
	}
}
