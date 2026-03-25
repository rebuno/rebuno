package policy

import (
	"testing"
	"time"
)

func TestParseSchedule_WeekdayRange(t *testing.T) {
	s, err := parseSchedule("Mon-Fri 09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("parseSchedule: %v", err)
	}
	if s.startHour != 9 || s.startMin != 0 {
		t.Errorf("expected start 09:00, got %02d:%02d", s.startHour, s.startMin)
	}
	if s.endHour != 17 || s.endMin != 0 {
		t.Errorf("expected end 17:00, got %02d:%02d", s.endHour, s.endMin)
	}
}

func TestParseSchedule_CommaSeparatedDays(t *testing.T) {
	s, err := parseSchedule("Mon,Wed,Fri 08:00-12:00 America/New_York")
	if err != nil {
		t.Fatalf("parseSchedule: %v", err)
	}
	if !s.days[time.Monday] || !s.days[time.Wednesday] || !s.days[time.Friday] {
		t.Error("expected Mon, Wed, Fri to be set")
	}
	if s.days[time.Tuesday] || s.days[time.Thursday] {
		t.Error("expected Tue, Thu to not be set")
	}
}

func TestParseSchedule_Invalid(t *testing.T) {
	cases := []string{
		"",
		"Mon-Fri",
		"Mon-Fri 09:00-17:00",     // missing timezone
		"Mon-Fri 25:00-17:00 UTC", // invalid hour
		"Xyz-Fri 09:00-17:00 UTC", // invalid day
	}
	for _, c := range cases {
		_, err := parseSchedule(c)
		if err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestMatchSchedule_WithinWindow(t *testing.T) {
	s, _ := parseSchedule("Mon-Fri 09:00-17:00 UTC")
	// Wednesday 12:00 UTC
	at := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	if !matchSchedule(s, at) {
		t.Error("expected match for Wed 12:00 UTC within Mon-Fri 09:00-17:00")
	}
}

func TestMatchSchedule_OutsideWindow(t *testing.T) {
	s, _ := parseSchedule("Mon-Fri 09:00-17:00 UTC")
	// Wednesday 20:00 UTC
	at := time.Date(2026, 3, 25, 20, 0, 0, 0, time.UTC)
	if matchSchedule(s, at) {
		t.Error("expected no match for Wed 20:00 UTC outside Mon-Fri 09:00-17:00")
	}
}

func TestMatchSchedule_WrongDay(t *testing.T) {
	s, _ := parseSchedule("Mon-Fri 09:00-17:00 UTC")
	// Saturday 12:00 UTC
	at := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	if matchSchedule(s, at) {
		t.Error("expected no match for Sat 12:00 UTC outside Mon-Fri")
	}
}

func TestMatchSchedule_TimezoneConversion(t *testing.T) {
	s, _ := parseSchedule("Mon-Fri 09:00-17:00 America/New_York")
	// Wed 14:00 UTC = Wed 10:00 ET (within window, EDT in March 2026)
	at := time.Date(2026, 3, 25, 14, 0, 0, 0, time.UTC)
	if !matchSchedule(s, at) {
		t.Error("expected match: 14:00 UTC = 10:00 ET, within 09:00-17:00 ET")
	}
	// Wed 05:00 UTC = Wed 01:00 ET (outside window)
	at = time.Date(2026, 3, 25, 5, 0, 0, 0, time.UTC)
	if matchSchedule(s, at) {
		t.Error("expected no match: 05:00 UTC = 01:00 ET, outside 09:00-17:00 ET")
	}
}
