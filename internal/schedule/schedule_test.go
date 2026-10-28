package schedule

import (
	"testing"
	"time"
)

var israelWeek = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday"}

func mustParse(t *testing.T, workdays []string, start, end string) Schedule {
	t.Helper()
	s, err := Parse("Asia/Jerusalem", workdays, start, end)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

func TestEvaluate(t *testing.T) {
	jlm, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		t.Fatal(err)
	}
	local := func(y int, m time.Month, d, hh, mm int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, jlm)
	}

	day := mustParse(t, israelWeek, "08:00", "19:00")
	night := mustParse(t, []string{"Sunday"}, "22:00", "06:00")

	// November 2026: Sunday the 1st, Thursday the 5th, Friday the 6th, Sunday the 8th.
	tests := []struct {
		name      string
		schedule  Schedule
		at        time.Time
		wantAwake bool
		wantNext  time.Time
	}{
		{"before start", day, local(2026, 11, 1, 7, 59), false, local(2026, 11, 1, 8, 0)},
		{"exactly at start is awake", day, local(2026, 11, 1, 8, 0), true, local(2026, 11, 1, 19, 0)},
		{"one minute before end", day, local(2026, 11, 1, 18, 59), true, local(2026, 11, 1, 19, 0)},
		{"exactly at end is asleep", day, local(2026, 11, 1, 19, 0), false, local(2026, 11, 2, 8, 0)},
		{"thursday evening sleeps through the weekend", day, local(2026, 11, 5, 19, 0), false, local(2026, 11, 8, 8, 0)},
		{"friday noon", day, local(2026, 11, 6, 12, 0), false, local(2026, 11, 8, 8, 0)},
		{"input in UTC is converted", day, time.Date(2026, 11, 1, 5, 59, 0, 0, time.UTC), false, local(2026, 11, 1, 8, 0)},

		{"overnight: evening of the workday", night, local(2026, 11, 1, 23, 0), true, local(2026, 11, 2, 6, 0)},
		{"overnight: early next morning still awake", night, local(2026, 11, 2, 5, 59), true, local(2026, 11, 2, 6, 0)},
		{"overnight: window closes", night, local(2026, 11, 2, 6, 0), false, local(2026, 11, 8, 22, 0)},
		{"overnight: non-workday evening", night, local(2026, 11, 2, 23, 0), false, local(2026, 11, 8, 22, 0)},

		// DST ends Sunday 2026-10-25 at 02:00 (UTC+3 -> UTC+2). Saturday 20:00 is
		// UTC+3, Sunday 08:00 is UTC+2: the gap is 13 hours, not 12.
		{"across end of DST", day, local(2026, 10, 24, 20, 0), false, local(2026, 10, 25, 8, 0)},
		// DST starts Friday 2026-03-27 at 02:00 (UTC+2 -> UTC+3).
		{"across start of DST", day, local(2026, 3, 26, 19, 0), false, local(2026, 3, 29, 8, 0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			awake, next := tt.schedule.Evaluate(tt.at)
			if awake != tt.wantAwake {
				t.Errorf("awake = %v, want %v", awake, tt.wantAwake)
			}
			if !next.Equal(tt.wantNext) {
				t.Errorf("next = %v, want %v", next, tt.wantNext)
			}
		})
	}
}

func TestEvaluateDSTUsesWallClock(t *testing.T) {
	s := mustParse(t, israelWeek, "08:00", "19:00")
	jlm, _ := time.LoadLocation("Asia/Jerusalem")

	_, next := s.Evaluate(time.Date(2026, 10, 24, 20, 0, 0, 0, jlm))
	if got := next.UTC(); got.Hour() != 6 {
		t.Errorf("Sunday 08:00 after DST ends should be 06:00 UTC, got %v", got)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name       string
		tz         string
		days       []string
		start, end string
	}{
		{"unknown time zone", "Asia/Jerusalim", israelWeek, "08:00", "19:00"},
		{"no workdays", "Asia/Jerusalem", nil, "08:00", "19:00"},
		{"unknown weekday", "Asia/Jerusalem", []string{"Funday"}, "08:00", "19:00"},
		{"bad start", "Asia/Jerusalem", israelWeek, "8am", "19:00"},
		{"end out of range", "Asia/Jerusalem", israelWeek, "08:00", "24:00"},
		{"start equals end", "Asia/Jerusalem", israelWeek, "08:00", "08:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.tz, tt.days, tt.start, tt.end); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}