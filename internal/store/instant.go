package store

import (
	"fmt"
	"time"
)

// ParseInstant parses an RFC 3339 timestamp of any fractional width (D-L1-34). Pre-M3 columns are 20 characters
// ("2026-03-01T10:00:05Z"), M3/L1 columns 24 ("2026-03-01T10:00:05.250Z"); a string comparison across the two widths
// is a bug because '.' sorts before 'Z'. Every reader that mixes widths compares the parsed instants instead.
func ParseInstant(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: parse instant %q: %w", s, err)
	}
	return t.UTC(), nil
}

// NormaliseInstant renders FormatTime(ParseInstant(s)): the 24-character form every L1 column stores.
func NormaliseInstant(s string) (string, error) {
	t, err := ParseInstant(s)
	if err != nil {
		return "", err
	}
	return FormatTime(t), nil
}
