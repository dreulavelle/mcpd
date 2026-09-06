package backup

import (
	"testing"
	"time"
)

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatalf("America/Chicago will not load: %v. cmd/mcpd imports time/tzdata "+
			"so that this works in the container; see the blank import there", err)
	}
	return loc
}

// Sunday 04:00 stays Sunday 04:00 across both daylight saving weekends.
//
// This is the bug the calendar arithmetic exists to prevent. Advancing by
// 7*24 hours makes the interval 167 hours in spring and 169 in autumn, so the
// backup drifts an hour earlier every March and an hour later every November,
// and after a few years it is running in the middle of the working day.
//
// 2026: the clocks go forward on 8 March and back on 1 November, both Sundays,
// which is exactly the weekend a weekly Sunday schedule lands on.
func TestScheduleFiresAtFourOnBothDSTWeekends(t *testing.T) {
	loc := chicago(t)
	s := Schedule{
		Enabled: true, Cadence: CadenceWeekly, Weekday: time.Sunday,
		Hour: 4, Minute: 0, Location: loc,
	}

	cases := []struct {
		name  string
		after time.Time
		// wantWall is the wall-clock instant expected, read in Chicago.
		wantWall string
		// wantGap is the number of hours from `after` to the fire, which is what
		// a fixed 7*24 would get wrong.
		wantGap float64
	}{
		{
			name:     "spring forward: the Sunday the clocks go forward",
			after:    time.Date(2026, 3, 1, 4, 0, 0, 0, loc),
			wantWall: "2026-03-08 04:00:00 -0500 CDT",
			// 23-hour day in the middle, so 167 rather than 168.
			wantGap: 167,
		},
		{
			name:     "the week after it",
			after:    time.Date(2026, 3, 8, 4, 0, 0, 0, loc),
			wantWall: "2026-03-15 04:00:00 -0500 CDT",
			wantGap:  168,
		},
		{
			name:     "fall back: the Sunday the clocks go back",
			after:    time.Date(2026, 10, 25, 4, 0, 0, 0, loc),
			wantWall: "2026-11-01 04:00:00 -0600 CST",
			// 25-hour day in the middle, so 169.
			wantGap: 169,
		},
		{
			name:     "the week after that",
			after:    time.Date(2026, 11, 1, 4, 0, 0, 0, loc),
			wantWall: "2026-11-08 04:00:00 -0600 CST",
			wantGap:  168,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.Next(tc.after)
			if got.In(loc).String() != tc.wantWall {
				t.Errorf("next fire %s, want %s", got.In(loc), tc.wantWall)
			}
			if gap := got.Sub(tc.after).Hours(); gap != tc.wantGap {
				t.Errorf("the interval was %g hours, want %g -- adding a fixed week "+
					"is what this arithmetic exists to avoid", gap, tc.wantGap)
			}
			if got.Weekday() != time.Sunday {
				t.Errorf("fired on a %s", got.Weekday())
			}
			if got.In(loc).Hour() != 4 {
				t.Errorf("fired at %02d:00 rather than 04:00", got.In(loc).Hour())
			}
		})
	}
}

// A half-hour timezone that also observes daylight saving is the case where an
// hour-shaped assumption shows up. Lord Howe Island shifts by thirty minutes.
func TestScheduleHandlesAHalfHourTimezone(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Skipf("Australia/Lord_Howe will not load: %v", err)
	}
	s := Schedule{
		Enabled: true, Cadence: CadenceDaily,
		Hour: 4, Minute: 30, Location: loc,
	}
	// Lord Howe moves on the first Sunday in April and October.
	after := time.Date(2026, 4, 4, 4, 30, 0, 0, loc)
	got := s.Next(after)
	if got.In(loc).Hour() != 4 || got.In(loc).Minute() != 30 {
		t.Errorf("fired at %02d:%02d, want 04:30", got.In(loc).Hour(), got.In(loc).Minute())
	}
	if got.In(loc).Day() != 5 {
		t.Errorf("fired on the %d, want the 5th", got.In(loc).Day())
	}
}

// A time that does not exist on the day the clocks go forward is normalised
// forward rather than skipped.
//
// 02:30 does not happen on 8 March 2026 in Chicago. The run has to happen; a
// backup that quietly does not run twice a year is worse than one at 03:30.
// The default is 04:00 precisely so nobody meets this, and the help text says
// why -- but somebody will type 02:30, and this is what happens then.
func TestScheduleRunsAtTheNextRealInstantWhenTheClockSkipsTheOneAsked(t *testing.T) {
	loc := chicago(t)
	s := Schedule{
		Enabled: true, Cadence: CadenceDaily,
		Hour: 2, Minute: 30, Location: loc,
	}
	after := time.Date(2026, 3, 7, 2, 30, 0, 0, loc)
	got := s.Next(after).In(loc)
	if got.Day() != 8 {
		t.Fatalf("fired on the %d, want the 8th", got.Day())
	}
	if got.Hour() == 2 {
		t.Fatalf("fired at 02:%02d, an hour that does not exist that day", got.Minute())
	}
	if got.Hour() != 3 || got.Minute() != 30 {
		t.Errorf("fired at %02d:%02d, want 03:30", got.Hour(), got.Minute())
	}
}

// A gap that starts at midnight moves forward like any other.
//
// Havana and Santiago put their spring-forward change at 00:00, so midnight
// itself does not exist and time.Date normalises it to 23:00 the *previous
// day*. A plain subtraction of that difference is negative, which the first
// version of skipGap left alone -- and a backup asked for at midnight would
// have run an hour before the day it was asked for, once a year.
func TestScheduleHandlesAGapThatStartsAtMidnight(t *testing.T) {
	loc, err := time.LoadLocation("America/Havana")
	if err != nil {
		t.Skipf("America/Havana will not load: %v", err)
	}
	s := Schedule{Enabled: true, Cadence: CadenceDaily, Hour: 0, Minute: 0, Location: loc}

	// Cuba moves the clocks forward at midnight on the second Sunday in March.
	after := time.Date(2026, 3, 7, 12, 0, 0, 0, loc)
	got := s.Next(after).In(loc)

	if got.Day() != 8 {
		t.Fatalf("fired on the %d of %s, want the 8th", got.Day(), got.Month())
	}
	if got.Hour() == 23 {
		t.Fatalf("fired at 23:00, an hour before the day it was asked for")
	}
	if !got.After(after) {
		t.Errorf("fired at %s, which is not after %s", got, after)
	}
}

// Daily and weekly, and a schedule that is off.
func TestScheduleNext(t *testing.T) {
	loc := chicago(t)
	daily := Schedule{Enabled: true, Cadence: CadenceDaily, Hour: 4, Location: loc}
	weekly := Schedule{
		Enabled: true, Cadence: CadenceWeekly, Weekday: time.Wednesday,
		Hour: 4, Location: loc,
	}

	cases := []struct {
		name  string
		s     Schedule
		after time.Time
		want  time.Time
	}{
		{
			name:  "daily, before today's time, fires today",
			s:     daily,
			after: time.Date(2026, 6, 10, 1, 0, 0, 0, loc),
			want:  time.Date(2026, 6, 10, 4, 0, 0, 0, loc),
		},
		{
			name:  "daily, after today's time, fires tomorrow",
			s:     daily,
			after: time.Date(2026, 6, 10, 5, 0, 0, 0, loc),
			want:  time.Date(2026, 6, 11, 4, 0, 0, 0, loc),
		},
		{
			name:  "daily, exactly on the instant, fires the next day",
			s:     daily,
			after: time.Date(2026, 6, 10, 4, 0, 0, 0, loc),
			want:  time.Date(2026, 6, 11, 4, 0, 0, 0, loc),
		},
		{
			name:  "daily across the end of a month",
			s:     daily,
			after: time.Date(2026, 6, 30, 5, 0, 0, 0, loc),
			want:  time.Date(2026, 7, 1, 4, 0, 0, 0, loc),
		},
		{
			name:  "daily across the end of a year",
			s:     daily,
			after: time.Date(2026, 12, 31, 5, 0, 0, 0, loc),
			want:  time.Date(2027, 1, 1, 4, 0, 0, 0, loc),
		},
		{
			name:  "weekly finds the named day",
			s:     weekly,
			after: time.Date(2026, 6, 10, 5, 0, 0, 0, loc), // a Wednesday, past 04:00
			want:  time.Date(2026, 6, 17, 4, 0, 0, 0, loc),
		},
		{
			name:  "weekly, earlier on the named day, fires today",
			s:     weekly,
			after: time.Date(2026, 6, 10, 1, 0, 0, 0, loc),
			want:  time.Date(2026, 6, 10, 4, 0, 0, 0, loc),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.s.Next(tc.after)
			if !got.Equal(tc.want) {
				t.Errorf("next fire %s, want %s", got.In(loc), tc.want.In(loc))
			}
		})
	}

	off := daily
	off.Enabled = false
	if got := off.Next(time.Now()); !got.IsZero() {
		t.Errorf("a schedule that is off named a next run at %s", got)
	}
}

// Previous is what catch-up compares against, so it has to be the mirror of
// Next rather than an approximation of it.
func TestSchedulePrevious(t *testing.T) {
	loc := chicago(t)
	weekly := Schedule{
		Enabled: true, Cadence: CadenceWeekly, Weekday: time.Sunday,
		Hour: 4, Location: loc,
	}

	// A Tuesday: the most recent Sunday 04:00 is two days back.
	at := time.Date(2026, 6, 16, 12, 0, 0, 0, loc)
	want := time.Date(2026, 6, 14, 4, 0, 0, 0, loc)
	if got := weekly.Previous(at); !got.Equal(want) {
		t.Errorf("previous fire %s, want %s", got.In(loc), want.In(loc))
	}

	// Standing exactly on the instant, that instant is the previous one.
	if got := weekly.Previous(want); !got.Equal(want) {
		t.Errorf("previous fire %s, want the instant itself %s", got.In(loc), want.In(loc))
	}

	// Sunday morning, before 04:00: the previous is a week back.
	early := time.Date(2026, 6, 14, 1, 0, 0, 0, loc)
	wantEarly := time.Date(2026, 6, 7, 4, 0, 0, 0, loc)
	if got := weekly.Previous(early); !got.Equal(wantEarly) {
		t.Errorf("previous fire %s, want %s", got.In(loc), wantEarly.In(loc))
	}
}

func TestParseClock(t *testing.T) {
	cases := []struct {
		in     string
		hour   int
		minute int
		bad    bool
	}{
		{in: "04:00", hour: 4},
		{in: "00:00"},
		{in: "23:59", hour: 23, minute: 59},
		{in: " 04:30 ", hour: 4, minute: 30},
		{in: "4:00", bad: true},
		{in: "24:00", bad: true},
		{in: "04:60", bad: true},
		{in: "0400", bad: true},
		{in: "", bad: true},
		{in: "aa:bb", bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			hour, minute, err := ParseClock(tc.in)
			if tc.bad {
				if err == nil {
					t.Errorf("%q was accepted as %02d:%02d", tc.in, hour, minute)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q was refused: %v", tc.in, err)
			}
			if hour != tc.hour || minute != tc.minute {
				t.Errorf("%q read as %02d:%02d, want %02d:%02d", tc.in, hour, minute, tc.hour, tc.minute)
			}
		})
	}
}

// A stored value that cannot be read must not stop the worker. It falls back
// and says so, because a schedule silently running on UTC when somebody asked
// for Chicago is an hour wrong for half the year on a host nobody watches.
func TestParseScheduleFallsBackAndSaysSo(t *testing.T) {
	s := ParseSchedule(true, "weekly", 0, "04:00", "Mars/Olympus_Mons")
	if s.Location != time.UTC {
		t.Errorf("location %s, want UTC after an unusable name", s.Location)
	}
	if s.Warning == "" {
		t.Error("the timezone was silently changed and nothing said so")
	}

	s = ParseSchedule(true, "weekly", 0, "half past four", "UTC")
	if s.Hour != 4 || s.Minute != 0 {
		t.Errorf("time %02d:%02d, want the 04:00 default", s.Hour, s.Minute)
	}
	if s.Warning == "" {
		t.Error("the time was silently changed and nothing said so")
	}

	// Nonsense cadences and weekdays fall back rather than producing a schedule
	// that never fires.
	s = ParseSchedule(true, "fortnightly", 99, "04:00", "UTC")
	if s.Cadence != CadenceWeekly {
		t.Errorf("cadence %q, want the weekly default", s.Cadence)
	}
	if s.Weekday != time.Sunday {
		t.Errorf("weekday %s, want Sunday", s.Weekday)
	}
}
