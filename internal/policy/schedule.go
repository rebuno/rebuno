package policy

import (
	"fmt"
	"strings"
	"time"
)

type schedule struct {
	days      map[time.Weekday]bool
	startHour int
	startMin  int
	endHour   int
	endMin    int
	loc       *time.Location
}

var dayNames = map[string]time.Weekday{
	"Sun": time.Sunday, "Mon": time.Monday, "Tue": time.Tuesday,
	"Wed": time.Wednesday, "Thu": time.Thursday, "Fri": time.Friday,
	"Sat": time.Saturday,
}

var dayOrder = []time.Weekday{
	time.Sunday, time.Monday, time.Tuesday, time.Wednesday,
	time.Thursday, time.Friday, time.Saturday,
}

func parseSchedule(s string) (*schedule, error) {
	parts := strings.Fields(s)
	if len(parts) != 3 {
		return nil, fmt.Errorf("schedule must have 3 parts: days time timezone, got %q", s)
	}

	days, err := parseDays(parts[0])
	if err != nil {
		return nil, err
	}

	startH, startM, endH, endM, err := parseTimeRange(parts[1])
	if err != nil {
		return nil, err
	}

	loc, err := time.LoadLocation(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", parts[2], err)
	}

	return &schedule{
		days:      days,
		startHour: startH, startMin: startM,
		endHour: endH, endMin: endM,
		loc: loc,
	}, nil
}

func parseDays(s string) (map[time.Weekday]bool, error) {
	days := make(map[time.Weekday]bool)

	if strings.Contains(s, "-") {
		parts := strings.SplitN(s, "-", 2)
		start, ok1 := dayNames[parts[0]]
		end, ok2 := dayNames[parts[1]]
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("invalid day range %q", s)
		}
		if start <= end {
			for i := int(start); i <= int(end); i++ {
				days[dayOrder[i]] = true
			}
		} else {
			// Wrap-around (e.g., Fri-Mon)
			for i := int(start); i < 7; i++ {
				days[dayOrder[i]] = true
			}
			for i := 0; i <= int(end); i++ {
				days[dayOrder[i]] = true
			}
		}
	} else {
		for _, d := range strings.Split(s, ",") {
			day, ok := dayNames[d]
			if !ok {
				return nil, fmt.Errorf("invalid day name %q", d)
			}
			days[day] = true
		}
	}

	return days, nil
}

func parseTimeRange(s string) (int, int, int, int, error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0, 0, 0, fmt.Errorf("invalid time range %q", s)
	}
	sh, sm, err := parseHourMinute(parts[0])
	if err != nil {
		return 0, 0, 0, 0, err
	}
	eh, em, err := parseHourMinute(parts[1])
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return sh, sm, eh, em, nil
}

func parseHourMinute(s string) (int, int, error) {
	var h, m int
	_, err := fmt.Sscanf(s, "%d:%d", &h, &m)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid time %q: %w", s, err)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("time out of range: %q", s)
	}
	return h, m, nil
}

func matchSchedule(s *schedule, at time.Time) bool {
	t := at.In(s.loc)
	if !s.days[t.Weekday()] {
		return false
	}
	minutes := t.Hour()*60 + t.Minute()
	start := s.startHour*60 + s.startMin
	end := s.endHour*60 + s.endMin
	return minutes >= start && minutes < end
}
