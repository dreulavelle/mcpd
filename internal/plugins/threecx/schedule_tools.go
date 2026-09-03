package threecx

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tool for "is the business open, and what does the phone system think".
//
// 3CX keeps office hours, holidays and the time zone per group -- what its
// console calls a department -- so "the company's hours" means the default
// group's, and a department can differ. A holiday there covers more than the
// name suggests: the same record is Christmas Day, the afternoon of Christmas
// Eve, and the Tuesday somebody is shutting at three.

func (p *Plugin) registerScheduleTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_schedule",
		Title: "Get office hours and holidays",
		Description: "One department's office hours, its holidays and early " +
			"closings, its time zone, and whether somebody has forced it open " +
			"or closed by hand -- which no amount of reading the schedule would " +
			"tell you, and is the first thing to check when calls go to the " +
			"closed greeting during the day. Leave department out for the " +
			"default one, which is everybody; the answer names the others.",
		Idempotent: true,
	}, p.getSchedule)
}

// groupRecord is a department as the phone system keeps it.
type groupRecord struct {
	ID                int    `json:"Id"`
	Name              string `json:"Name"`
	Number            string `json:"Number"`
	IsDefault         bool   `json:"IsDefault"`
	CurrentGroupHours string `json:"CurrentGroupHours"`
	TimeZoneID        string `json:"TimeZoneId"`
	OverrideExpiresAt string `json:"OverrideExpiresAt"`
	Hours             *struct {
		Type           string `json:"Type"`
		IgnoreHolidays bool   `json:"IgnoreHolidays"`
		Periods        []struct {
			DayOfWeek string `json:"DayOfWeek"`
			Start     string `json:"Start"`
			Stop      string `json:"Stop"`
		} `json:"Periods"`
	} `json:"Hours"`
}

const groupFields = "Id,Name,Number,IsDefault,CurrentGroupHours,TimeZoneId,OverrideExpiresAt,Hours"

func (p *Plugin) readGroups(ctx context.Context) ([]groupRecord, error) {
	q := url.Values{"$select": {groupFields}}
	got, err := list[groupRecord](ctx, p.client, "Groups", q, p.cfg.MaxItems)
	if err != nil {
		return nil, err
	}
	return got.Rows, nil
}

// findGroup resolves a department by name or number.
func findGroup(groups []groupRecord, asked string) (groupRecord, bool) {
	asked = strings.TrimSpace(asked)
	for _, g := range groups {
		if strings.EqualFold(g.Name, asked) || g.Number == asked {
			return g, true
		}
	}
	return groupRecord{}, false
}

func groupNames(groups []groupRecord) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.Name)
	}
	sort.Strings(out)
	return out
}

type scheduleArgs struct {
	Department string `json:"department,omitempty" jsonschema:"which department, by name or number; left out for the default one"`
}

// DayHours is one day the department is open.
type DayHours struct {
	Day  string `json:"day"`
	From string `json:"from"`
	To   string `json:"to"`
}

// HolidayRow is one closure.
type HolidayRow struct {
	Name     string `json:"name"`
	Starts   string `json:"starts"`
	Ends     string `json:"ends,omitempty"`
	FromTime string `json:"from_time,omitempty"`
	ToTime   string `json:"to_time,omitempty"`
	Repeats  bool   `json:"repeats"`
	Prompt   string `json:"prompt,omitempty"`
}

// Schedule is one department's hours and closures.
type Schedule struct {
	Department string `json:"department"`
	Number     string `json:"number,omitempty"`
	IsDefault  bool   `json:"is_default"`
	TimeZone   string `json:"time_zone,omitempty"`
	// Forced is set when somebody has overridden the schedule by hand: "forced
	// closed", "forced open". Empty when the schedule is doing its job.
	Forced      string `json:"forced,omitempty"`
	ForcedUntil string `json:"forced_until,omitempty"`

	HoursKind      string       `json:"hours_kind,omitempty"`
	OfficeHours    []DayHours   `json:"office_hours"`
	IgnoreHolidays bool         `json:"ignore_holidays"`
	Holidays       []HolidayRow `json:"holidays"`

	// Departments names the others, so a caller who asked about the wrong one
	// can ask again.
	Departments []string `json:"departments"`
}

func (p *Plugin) getSchedule(ctx context.Context, args scheduleArgs) (Schedule, error) {
	if err := p.ready(); err != nil {
		return Schedule{}, err
	}
	groups, err := p.readGroups(ctx)
	if err != nil {
		return Schedule{}, p.call(err)
	}
	if len(groups) == 0 {
		return Schedule{}, fmt.Errorf("this phone system reports no departments")
	}

	var chosen groupRecord
	if asked := strings.TrimSpace(args.Department); asked != "" {
		g, ok := findGroup(groups, asked)
		if !ok {
			return Schedule{}, fmt.Errorf("there is no department %q; the phone system has %s",
				asked, strings.Join(groupNames(groups), ", "))
		}
		chosen = g
	} else {
		chosen = groups[0]
		for _, g := range groups {
			if g.IsDefault {
				chosen = g
				break
			}
		}
	}

	out := Schedule{
		Department: chosen.Name, Number: chosen.Number, IsDefault: chosen.IsDefault,
		Forced: forcedAs(chosen.CurrentGroupHours), OfficeHours: []DayHours{},
		Holidays: []HolidayRow{}, Departments: []string{},
	}
	if out.Forced != "" {
		out.ForcedUntil = chosen.OverrideExpiresAt
	}
	for _, g := range groups {
		if g.ID != chosen.ID {
			out.Departments = append(out.Departments, g.Name)
		}
	}
	sort.Strings(out.Departments)
	if chosen.Hours != nil {
		out.HoursKind = chosen.Hours.Type
		out.IgnoreHolidays = chosen.Hours.IgnoreHolidays
		for _, per := range chosen.Hours.Periods {
			out.OfficeHours = append(out.OfficeHours, DayHours{Day: per.DayOfWeek, From: hhmm(per.Start), To: hhmm(per.Stop)})
		}
	}

	// The holidays hang off the group as a navigation property, which is the
	// phone system saying plainly that they belong to it.
	type holiday struct {
		Name            string `json:"Name"`
		Day             int    `json:"Day"`
		Month           int    `json:"Month"`
		Year            int    `json:"Year"`
		DayEnd          int    `json:"DayEnd"`
		MonthEnd        int    `json:"MonthEnd"`
		YearEnd         int    `json:"YearEnd"`
		TimeOfStartDate string `json:"TimeOfStartDate"`
		TimeOfEndDate   string `json:"TimeOfEndDate"`
		IsRecurrent     bool   `json:"IsRecurrent"`
		HolidayPrompt   string `json:"HolidayPrompt"`
	}
	var g struct {
		OfficeHolidays []holiday `json:"OfficeHolidays"`
	}
	hq := url.Values{
		"$select": {"Id"},
		"$expand": {"OfficeHolidays($select=Id,Name,Day,Month,Year,DayEnd,MonthEnd,YearEnd," +
			"TimeOfStartDate,TimeOfEndDate,IsRecurrent,HolidayPrompt)"},
	}
	if err := p.client.get(ctx, fmt.Sprintf("Groups(%d)", chosen.ID), hq, &g); err != nil {
		return Schedule{}, p.call(err)
	}
	for _, h := range g.OfficeHolidays {
		row := HolidayRow{
			Name: h.Name, Starts: dateText(h.Year, h.Month, h.Day),
			FromTime: clock(h.TimeOfStartDate), ToTime: clock(h.TimeOfEndDate),
			Repeats: h.IsRecurrent, Prompt: h.HolidayPrompt,
		}
		if ends := dateText(h.YearEnd, h.MonthEnd, h.DayEnd); ends != row.Starts {
			row.Ends = ends
		}
		out.Holidays = append(out.Holidays, row)
	}
	// By when they happen, so the list reads as a year rather than as whatever
	// order they were added in. A repeating closure sorts as "--12-25", which
	// puts it among the months where it belongs.
	sort.SliceStable(out.Holidays, func(a, b int) bool {
		return monthDay(out.Holidays[a].Starts) < monthDay(out.Holidays[b].Starts)
	})

	out.TimeZone = p.timeZoneName(ctx, chosen.TimeZoneID)
	p.note(nil)
	return out, nil
}

// forcedAs reads the phone system's override mode as something worth showing.
// "Default" is the schedule doing its job and is reported as nothing at all;
// anything else is somebody having overridden it by hand.
func forcedAs(mode string) string {
	switch mode {
	case "", "Default":
		return ""
	case "ForceOpened":
		return "forced open"
	case "ForceClosed":
		return "forced closed"
	case "ForceBreak":
		return "forced onto break"
	case "ForceHoliday":
		return "forced onto holiday hours"
	case "ForceCustomOperator":
		return "forced to a custom operator"
	}
	return mode
}

// timeZoneName turns the phone system's own time zone id into a name a person
// has heard of. 3CX keeps zones by number, which is meaningless on a screen
// whose whole subject is what time things happen. The id comes back unchanged
// when the lookup fails, which is worse than a name and better than nothing.
func (p *Plugin) timeZoneName(ctx context.Context, id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	type zone struct {
		ID       string `json:"Id"`
		Name     string `json:"Name"`
		IanaName string `json:"IanaName"`
	}
	q := url.Values{"$select": {"Id,Name,IanaName"}}
	got, err := list[zone](ctx, p.client, "Defs/TimeZones", q, 500)
	if err != nil {
		return id
	}
	for _, z := range got.Rows {
		if z.ID == id {
			return firstNonBlank(z.IanaName, z.Name, id)
		}
	}
	return id
}

// monthDay is the part of a rendered date that places it within a year:
// "12-25" from either "2026-12-25" or "--12-25".
func monthDay(date string) string {
	if len(date) >= 5 {
		return date[len(date)-5:]
	}
	return date
}
