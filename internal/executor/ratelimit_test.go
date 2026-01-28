package executor

import (
	"strings"
	"testing"
	"time"
)

func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		expected bool
	}{
		{
			name:     "hit your limit message",
			errMsg:   "You've hit your limit · resets 6am (Europe/Podgorica)",
			expected: true,
		},
		{
			name:     "rate limit lowercase",
			errMsg:   "rate limit exceeded",
			expected: true,
		},
		{
			name:     "resets keyword",
			errMsg:   "API resets at midnight",
			expected: true,
		},
		{
			name:     "not rate limit error",
			errMsg:   "connection timeout",
			expected: false,
		},
		{
			name:     "empty string",
			errMsg:   "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRateLimitError(tt.errMsg)
			if got != tt.expected {
				t.Errorf("IsRateLimitError(%q) = %v, want %v", tt.errMsg, got, tt.expected)
			}
		})
	}
}

func TestParseRateLimitError(t *testing.T) {
	// Use UTC for predictable test results
	utc, _ := time.LoadLocation("UTC")

	tests := []struct {
		name        string
		errMsg      string
		wantParsed  bool
		wantTZ      string
		checkHour   int // expected hour in 24h format
		checkMinute int
	}{
		{
			name:        "hour only am format",
			errMsg:      "You've hit your limit · resets 6am (UTC)",
			wantParsed:  true,
			wantTZ:      "UTC",
			checkHour:   6,
			checkMinute: 0,
		},
		{
			name:        "hour only pm format",
			errMsg:      "You've hit your limit · resets 2pm (UTC)",
			wantParsed:  true,
			wantTZ:      "UTC",
			checkHour:   14,
			checkMinute: 0,
		},
		{
			name:        "hour:minute am format",
			errMsg:      "You've hit your limit · resets 6:30am (UTC)",
			wantParsed:  true,
			wantTZ:      "UTC",
			checkHour:   6,
			checkMinute: 30,
		},
		{
			name:        "hour:minute pm format",
			errMsg:      "You've hit your limit · resets 2:30pm (UTC)",
			wantParsed:  true,
			wantTZ:      "UTC",
			checkHour:   14,
			checkMinute: 30,
		},
		{
			name:        "noon (12pm)",
			errMsg:      "You've hit your limit · resets 12pm (UTC)",
			wantParsed:  true,
			wantTZ:      "UTC",
			checkHour:   12,
			checkMinute: 0,
		},
		{
			name:        "midnight (12am)",
			errMsg:      "You've hit your limit · resets 12am (UTC)",
			wantParsed:  true,
			wantTZ:      "UTC",
			checkHour:   0,
			checkMinute: 0,
		},
		{
			name:        "with real timezone",
			errMsg:      "You've hit your limit · resets 6am (Europe/Podgorica)",
			wantParsed:  true,
			wantTZ:      "Europe/Podgorica",
			checkHour:   6,
			checkMinute: 0,
		},
		{
			name:        "24-hour format",
			errMsg:      "You've hit your limit · resets 14:30 (UTC)",
			wantParsed:  true,
			wantTZ:      "UTC",
			checkHour:   14,
			checkMinute: 30,
		},
		{
			name:       "not a rate limit error",
			errMsg:     "connection refused",
			wantParsed: false,
		},
		{
			name:       "rate limit without reset time",
			errMsg:     "rate limit exceeded",
			wantParsed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := ParseRateLimitError(tt.errMsg)

			if ok != tt.wantParsed {
				t.Errorf("ParseRateLimitError(%q) parsed = %v, want %v", tt.errMsg, ok, tt.wantParsed)
				return
			}

			if !tt.wantParsed {
				return
			}

			if info.Timezone != tt.wantTZ {
				t.Errorf("timezone = %q, want %q", info.Timezone, tt.wantTZ)
			}

			// Check the hour and minute in the appropriate timezone
			resetInTZ := info.ResetTime.In(utc)
			if tt.wantTZ == "UTC" {
				if resetInTZ.Hour() != tt.checkHour {
					t.Errorf("hour = %d, want %d", resetInTZ.Hour(), tt.checkHour)
				}
				if resetInTZ.Minute() != tt.checkMinute {
					t.Errorf("minute = %d, want %d", resetInTZ.Minute(), tt.checkMinute)
				}
			}

			// Reset time should be in the future
			if info.ResetTime.Before(time.Now()) {
				// Unless it's within a small margin (test timing issues)
				if time.Since(info.ResetTime) > time.Minute {
					t.Errorf("reset time %v is not in the future", info.ResetTime)
				}
			}
		})
	}
}

func TestRateLimitInfo_TimeUntilReset(t *testing.T) {
	info := &RateLimitInfo{
		ResetTime: time.Now().Add(2 * time.Hour),
		Timezone:  "UTC",
	}

	dur := info.TimeUntilReset()
	// Should be approximately 2 hours (allow 1 second margin)
	if dur < 2*time.Hour-time.Second || dur > 2*time.Hour+time.Second {
		t.Errorf("TimeUntilReset() = %v, want approximately 2h", dur)
	}
}

func TestRateLimitInfo_HumanReadableReset(t *testing.T) {
	tests := []struct {
		name          string
		offset        time.Duration
		expectContain string // Use contains check due to timing variance
	}{
		{
			name:          "hours and minutes",
			offset:        2*time.Hour + 30*time.Minute + 30*time.Second, // Add buffer
			expectContain: "2h",
		},
		{
			name:          "minutes only",
			offset:        45*time.Minute + 30*time.Second, // Add buffer
			expectContain: "m",
		},
		{
			name:          "past time",
			offset:        -10 * time.Minute,
			expectContain: "now",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &RateLimitInfo{
				ResetTime: time.Now().Add(tt.offset),
			}
			got := info.HumanReadableReset()
			if !strings.Contains(got, tt.expectContain) {
				t.Errorf("HumanReadableReset() = %q, want to contain %q", got, tt.expectContain)
			}
		})
	}
}
