// Package schedule decides whether a point in time falls inside office hours.
//
// It has no Kubernetes dependencies on purpose: the controller parses the
// OfficeHours spec into a Schedule once, and all time logic lives here, where
// it can be tested against any instant without a cluster or a real clock.
package schedule

import (
	"errors"
	"fmt"
	"time"
)

// Schedule is a parsed, validated set of office hours.
type Schedule struct {
	location *time.Location
	workdays map[time.Weekday]bool
	start    wallClock
	end      wallClock
}

// wallClock is a local time of day. It is kept as hour and minute, not as a
// duration since midnight, because days are not always 24 hours long: on a
// daylight saving change, "midnight + 8h" is not 08:00.
type wallClock struct {
	hour, minute int
}

var weekdays = map[string]time.Weekday{
	"Sunday": time.Sunday, "Monday": time.Monday, "Tuesday": time.Tuesday,
	"Wednesday": time.Wednesday, "Thursday": time.Thursday, "Friday": time.Friday,
	"Saturday": time.Saturday,
}

// Parse validates the user's input and builds a Schedule. The API server
// already enforces most of these rules; Parse checks them again so the package
// is safe on its own, and it is the only place that can check the time zone
// really exists.
func Parse(timeZone string, workdays []string, start, end string) (Schedule, error) {
	loc, err := time.LoadLocation(timeZone)
	if err != nil {
		return Schedule{}, fmt.Errorf("unknown time zone %q", timeZone)
	}
	if len(workdays) == 0 {
		return Schedule{}, errors.New("at least one workday is required")
	}
	days := make(map[time.Weekday]bool, len(workdays))
	for _, name := range workdays {
		d, ok := weekdays[name]
		if !ok {
			return Schedule{}, fmt.Errorf("unknown weekday %q", name)
		}
		days[d] = true
	}
	s, err := parseWallClock(start)
	if err != nil {
		return Schedule{}, fmt.Errorf("start: %w", err)
	}
	e, err := parseWallClock(end)
	if err != nil {
		return Schedule{}, fmt.Errorf("end: %w", err)
	}
	if s == e {
		return Schedule{}, errors.New("start and end must be different times")
	}
	return Schedule{location: loc, workdays: days, start: s, end: e}, nil
}

func parseWallClock(v string) (wallClock, error) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return wallClock{}, fmt.Errorf("%q is not a valid HH:MM time", v)
	}
	return wallClock{hour: t.Hour(), minute: t.Minute()}, nil
}

// Evaluate reports whether at falls inside office hours, and the next instant
// at which that answer changes. next is returned in the schedule's time zone.
//
// A window opens at start on each workday and closes at end. If end is earlier
// than start, the window crosses midnight and closes the next day. The start
// instant is inside the window; the end instant is outside it.
func (s Schedule) Evaluate(at time.Time) (awake bool, next time.Time) {
	now := at.In(s.location)

	// The only windows that can contain now are the ones that opened today or,
	// for a window that crosses midnight, yesterday.
	for offset := -1; offset <= 0; offset++ {
		opens, closes, ok := s.window(now, offset)
		if ok && !now.Before(opens) && now.Before(closes) {
			return true, closes
		}
	}

	// Asleep: find the next window that opens after now. At least one workday
	// exists, so one opens within the next 7 days.
	for offset := 0; offset <= 7; offset++ {
		opens, _, ok := s.window(now, offset)
		if ok && opens.After(now) {
			return false, opens
		}
	}
	panic("schedule: no workday found within a week; Parse should have prevented this")
}

// window returns the office-hours window that opens on the calendar day
// dayOffset days after the local date of ref. ok is false if that day is not
// a workday.
func (s Schedule) window(ref time.Time, dayOffset int) (opens, closes time.Time, ok bool) {
	y, m, d := ref.Date()
	day := time.Date(y, m, d+dayOffset, 12, 0, 0, 0, s.location) // noon avoids DST edge cases
	if !s.workdays[day.Weekday()] {
		return time.Time{}, time.Time{}, false
	}
	opens = s.at(day, 0, s.start)
	closeDay := 0
	if s.end.before(s.start) {
		closeDay = 1
	}
	closes = s.at(day, closeDay, s.end)
	return opens, closes, true
}

// at builds the instant for a wall-clock time on day + dayOffset, letting
// time.Date resolve it in the schedule's time zone (including DST changes).
func (s Schedule) at(day time.Time, dayOffset int, w wallClock) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d+dayOffset, w.hour, w.minute, 0, 0, s.location)
}

func (w wallClock) before(o wallClock) bool {
	return w.hour < o.hour || (w.hour == o.hour && w.minute < o.minute)
}