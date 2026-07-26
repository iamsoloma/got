package utils

import (
	"fmt"
	"strconv"
)

func FormatTimezone(offset int) string {
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	hours := offset / 3600
	minutes := (offset % 3600) / 60
	return fmt.Sprintf("%s%02d%02d", sign, hours, minutes)
}

func ParseTimezone(tz string) (int, error) {
	if len(tz) != 5 {
		return 0, fmt.Errorf("invalid timezone format: expected 5 characters")
	}

	sign := tz[0]
	if sign != '+' && sign != '-' {
		return 0, fmt.Errorf("invalid sign: must be '+' or '-'")
	}

	hoursStr := tz[1:3]
	minutesStr := tz[3:5]

	hours, err := strconv.Atoi(hoursStr)
	if err != nil {
		return 0, fmt.Errorf("invalid hours: %w", err)
	}
	minutes, err := strconv.Atoi(minutesStr)
	if err != nil {
		return 0, fmt.Errorf("invalid minutes: %w", err)
	}

	// check values
	if hours > 23 || minutes > 59 {
		return 0, fmt.Errorf("invalid time values")
	}

	offsetSeconds := hours*3600 + minutes*60
	if sign == '-' {
		offsetSeconds = -offsetSeconds
	}

	return offsetSeconds, nil
}
