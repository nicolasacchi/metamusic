package store

import "time"

// TimeLayout is the fixed-width UTC layout of every M3 timestamp column that
// SQL compares (D-M3-23): lexicographic order equals time order.
const TimeLayout = "2006-01-02T15:04:05.000Z"

// FormatTime renders t in TimeLayout (UTC, millisecond precision, 24 bytes).
func FormatTime(t time.Time) string { return t.UTC().Format(TimeLayout) }

// ParseTime is the inverse of FormatTime.
func ParseTime(s string) (time.Time, error) { return time.Parse(TimeLayout, s) }
