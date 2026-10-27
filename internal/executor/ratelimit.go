package executor

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// recentResetWindow is how far in the past a calendar-day reset instant may be
// and still count as "the limit has just reset" rather than "resets tomorrow".
// The error text is written when Claude hits the wall, but the run finalizes
// minutes later (finalize window, recovery steps, worker loop), so a reset
// that passes in between must not roll to the next day (GH-5879).
const recentResetWindow = 2 * time.Hour

// RateLimitInfo contains parsed rate limit information from Claude Code errors
type RateLimitInfo struct {
	ResetTime time.Time
	Timezone  string
	RawError  string
	// AlreadyReset is true when ResetTime is in the past, within
	// recentResetWindow of the parse clock: the limit has already reset, so
	// callers retry after their buffer instead of waiting for ResetTime.
	AlreadyReset bool
}

// IsRateLimitError checks if an error message indicates a rate limit
func IsRateLimitError(errMsg string) bool {
	lowerMsg := strings.ToLower(errMsg)
	return strings.Contains(lowerMsg, "hit your limit") ||
		strings.Contains(lowerMsg, "rate limit") ||
		strings.Contains(lowerMsg, "resets")
}

// ParseRateLimitError extracts rate limit information from an error message.
// Handles formats like:
//   - "You've hit your limit · resets 6am (Europe/Podgorica)"
//   - "You've hit your limit · resets 2:30pm (UTC)"
//   - "You've hit your limit · resets 12pm (America/New_York)"
func ParseRateLimitError(errMsg string) (*RateLimitInfo, bool) {
	return parseRateLimitErrorAt(errMsg, time.Now())
}

// parseRateLimitErrorAt is ParseRateLimitError against an explicit clock, so
// callers with an injected clock (the dispatcher's usage-limit pause) resolve
// "resets 6am" relative to the same instant they schedule from.
func parseRateLimitErrorAt(errMsg string, clockNow time.Time) (*RateLimitInfo, bool) {
	if !IsRateLimitError(errMsg) {
		return nil, false
	}

	// Pattern variations for reset time
	patterns := []*regexp.Regexp{
		// "resets 6am (Europe/Podgorica)" - hour only
		regexp.MustCompile(`resets\s+(\d{1,2})(am|pm)\s+\(([^)]+)\)`),
		// "resets 2:30pm (UTC)" - hour:minute
		regexp.MustCompile(`resets\s+(\d{1,2}):(\d{2})(am|pm)\s+\(([^)]+)\)`),
		// "resets 14:30 (UTC)" - 24-hour format
		regexp.MustCompile(`resets\s+(\d{1,2}):(\d{2})\s+\(([^)]+)\)`),
	}

	for i, pattern := range patterns {
		matches := pattern.FindStringSubmatch(errMsg)
		if matches == nil {
			continue
		}

		var hour, minute int
		var ampm, tz string

		switch i {
		case 0: // hour only with am/pm
			hour, _ = strconv.Atoi(matches[1])
			ampm = strings.ToLower(matches[2])
			tz = matches[3]
		case 1: // hour:minute with am/pm
			hour, _ = strconv.Atoi(matches[1])
			minute, _ = strconv.Atoi(matches[2])
			ampm = strings.ToLower(matches[3])
			tz = matches[4]
		case 2: // 24-hour format
			hour, _ = strconv.Atoi(matches[1])
			minute, _ = strconv.Atoi(matches[2])
			tz = matches[3]
		}

		// Convert 12-hour to 24-hour if needed
		if ampm == "pm" && hour != 12 {
			hour += 12
		} else if ampm == "am" && hour == 12 {
			hour = 0
		}

		// Parse timezone
		loc, err := time.LoadLocation(tz)
		if err != nil {
			// Fallback to local timezone if parsing fails
			loc = time.Local
		}

		// Calculate reset time
		now := clockNow.In(loc)
		resetTime := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)

		// A reset just behind us has already happened; only an older one is
		// tomorrow's.
		alreadyReset := false
		if resetTime.Before(now) {
			if now.Sub(resetTime) <= recentResetWindow {
				alreadyReset = true
			} else {
				resetTime = resetTime.Add(24 * time.Hour)
			}
		}

		return &RateLimitInfo{
			ResetTime:    resetTime,
			Timezone:     tz,
			RawError:     errMsg,
			AlreadyReset: alreadyReset,
		}, true
	}

	return nil, false
}

// TimeUntilReset returns the duration until the rate limit resets
func (r *RateLimitInfo) TimeUntilReset() time.Duration {
	return time.Until(r.ResetTime)
}

// HumanReadableReset returns a human-readable string for when the limit resets
func (r *RateLimitInfo) HumanReadableReset() string {
	dur := r.TimeUntilReset()
	if dur < 0 {
		return "now"
	}

	hours := int(dur.Hours())
	minutes := int(dur.Minutes()) % 60

	if hours > 0 {
		return strconv.Itoa(hours) + "h " + strconv.Itoa(minutes) + "m"
	}
	return strconv.Itoa(minutes) + "m"
}

// ResetTimeFormatted returns the reset time in a human-readable format
func (r *RateLimitInfo) ResetTimeFormatted() string {
	return r.ResetTime.Format("3:04 PM MST")
}
