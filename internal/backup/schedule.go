package backup

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// When a backup happens, expressed the way a person says it: every day, or
// every Sunday, at a time, in a place.
//
// No cron expression, and no cron dependency. Four fields describe every
// schedule this feature is for, they render as four ordinary form controls, and
// nobody has to be taught the difference between `0 4 * * 0` and `0 0 4 * *`.
//
// The timezone is stored explicitly rather than taken from the host. A
// container's clock is UTC, an operator's is not, and "four in the morning"
// asked for in a form has to mean four in the morning where they are -- across
// the two weekends a year when that is not a fixed offset.

// Cadence is how often a scheduled backup runs.
const (
	CadenceDaily  = "daily"
	CadenceWeekly = "weekly"
)

// Schedule is a resolved backup schedule.
type Schedule struct {
	Enabled bool
	Cadence string
	// Weekday matters only for the weekly cadence. Sunday is 0, which is what
	// time.Weekday says and what the form offers.
	Weekday time.Weekday
	Hour    int
	Minute  int
	// Location is where the hour is counted. Never nil once ParseSchedule has
	// returned; UTC when the stored name could not be loaded.
	Location *time.Location
	// Warning is empty unless something stored was not usable and a default was
	// taken instead. It is surfaced in the schedule summary rather than logged
	// once at startup, so that a timezone this build cannot load is visible on
	// the page that set it.
	Warning string
}

// ParseSchedule turns the stored settings into a schedule.
//
// Nothing here refuses: the values were validated when they were written, and a
// schedule that would not parse must still produce something the status can
// describe rather than an error that stops a worker. What could not be read is
// named in Warning.
func ParseSchedule(enabled bool, cadence string, weekday int, clock, timezone string) Schedule {
	s := Schedule{Enabled: enabled, Cadence: cadence, Location: time.UTC}
	if s.Cadence != CadenceDaily && s.Cadence != CadenceWeekly {
		s.Cadence = CadenceWeekly
	}
	if weekday >= 0 && weekday <= 6 {
		s.Weekday = time.Weekday(weekday)
	}

	hour, minute, err := ParseClock(clock)
	if err != nil {
		// 04:00 rather than midnight, and deliberately outside 01:00-03:00:
		// that window is where a daylight saving change puts an hour that does
		// not exist, and a backup that silently does not happen twice a year is
		// worse than one at an hour nobody asked for.
		hour, minute = 4, 0
		s.Warning = "The time on the schedule could not be read, so backups run at 04:00."
	}
	s.Hour, s.Minute = hour, minute

	name := strings.TrimSpace(timezone)
	if name == "" {
		name = "UTC"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		// This is what the tzdata import in cmd/mcpd exists to prevent: the
		// container image carries no zoneinfo, so without it every name but UTC
		// would land here and every schedule would silently be UTC.
		s.Warning = fmt.Sprintf(
			"This build could not load the time zone %q, so backups run on UTC.", name)
		loc = time.UTC
	}
	s.Location = loc
	return s
}

// ParseClock reads an HH:MM.
func ParseClock(v string) (hour, minute int, err error) {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, 0, fmt.Errorf("%q is not a time of day; write it as HH:MM, like 04:00", v)
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("%q is not a time of day; the hour is 00 to 23", v)
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("%q is not a time of day; the minutes are 00 to 59", v)
	}
	return hour, minute, nil
}

// Next returns the first firing instant strictly after `after`.
//
// Calendar arithmetic, never `last.Add(7 * 24 * time.Hour)`. The two weekends a
// year when a day is 23 or 25 hours long are exactly the weekends a fixed
// interval drifts through, and 04:00 becoming 03:00 every spring is the bug
// this shape exists to make impossible. time.Date normalises a date out of
// range, so day+1 crossing a month or a year needs no special case.
func (s Schedule) Next(after time.Time) time.Time {
	if !s.Enabled {
		return time.Time{}
	}
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	local := after.In(loc)
	year, month, day := local.Date()

	// Eight tries covers a week plus the day the loop starts on, which is more
	// than any cadence here can need. Bounded rather than `for {}` because a
	// timezone whose rules made a time unreachable would otherwise spin.
	for i := 0; i < 8; i++ {
		candidate := skipGap(time.Date(year, month, day+i, s.Hour, s.Minute, 0, 0, loc), s.Hour, s.Minute)
		if !candidate.After(after) {
			continue
		}
		if s.Cadence == CadenceWeekly && candidate.Weekday() != s.Weekday {
			continue
		}
		return candidate
	}
	return time.Time{}
}

// skipGap moves a time that does not exist forward to the first one that does.
//
// On the day the clocks go forward there is an hour -- half an hour in a few
// places -- that never happens. time.Date is documented to normalise such a
// time to *something*, and what it picks is the instant read against the offset
// before the change: ask for 02:30 on 8 March in Chicago and it hands back
// 01:30, an hour earlier than was asked for rather than later.
//
// Running early is a worse answer than running late: an operator picked a time
// because nothing else is happening then. The difference between what was asked
// and what came back is exactly the length of the gap, so adding it lands on
// the first instant that exists -- 03:30 in that example.
//
// Bounded to two hours so that a zone rule this does not anticipate leaves the
// time alone rather than moving it somewhere arbitrary. And a no-op on every
// other day of the year, including the day the clocks go back, where time.Date
// answers with the wall clock that was asked for.
func skipGap(candidate time.Time, hour, minute int) time.Time {
	asked := hour*60 + minute
	got := candidate.Hour()*60 + candidate.Minute()
	gap := asked - got
	if gap <= 0 || gap > 120 {
		return candidate
	}
	return candidate.Add(time.Duration(gap) * time.Minute)
}

// Previous returns the most recent firing instant at or before `at`.
//
// It is what catch-up compares against: a scheduled run that should have
// happened while the process was down is one whose instant is in the past and
// which no row records.
func (s Schedule) Previous(at time.Time) time.Time {
	if !s.Enabled {
		return time.Time{}
	}
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	local := at.In(loc)
	year, month, day := local.Date()

	for i := 0; i < 8; i++ {
		candidate := skipGap(time.Date(year, month, day-i, s.Hour, s.Minute, 0, 0, loc), s.Hour, s.Minute)
		if candidate.After(at) {
			continue
		}
		if s.Cadence == CadenceWeekly && candidate.Weekday() != s.Weekday {
			continue
		}
		return candidate
	}
	return time.Time{}
}
