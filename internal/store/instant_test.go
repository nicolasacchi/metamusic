package store_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestParseInstant_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, in string
		ms       int64
		bad      bool
	}{
		{"20 chars", "2026-03-01T10:00:05Z", 1772359205000, false},
		{"24 chars", "2026-03-01T10:00:05.000Z", 1772359205000, false},
		{"fraction 250", "2026-03-01T10:00:05.250Z", 1772359205250, false},
		{"one digit", "2026-03-01T10:00:05.5Z", 1772359205500, false},
		{"six digits", "2026-03-01T10:00:05.123456Z", 1772359205123, false},
		{"offset", "2026-03-01T11:00:05+01:00", 1772359205000, false},
		{"invalid", "yesterday", 0, true},
		{"empty", "", 0, true},
	} {
		got, err := store.ParseInstant(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("%s: want error", tc.name)
			}
			continue
		}
		if err != nil || got.UnixMilli() != tc.ms {
			t.Errorf("%s: got %v %v, want %d", tc.name, got.UnixMilli(), err, tc.ms)
		}
	}
	a, _ := store.ParseInstant("2026-03-01T10:00:05Z")
	b, _ := store.ParseInstant("2026-03-01T10:00:05.250Z")
	if !a.Before(b) {
		t.Errorf("same-second 20-char value must be earlier than .250Z")
	}
	if "2026-03-01T10:00:05Z" < "2026-03-01T10:00:05.250Z" {
		t.Errorf("premise: the string compare is wrong across widths")
	}
	n, err := store.NormaliseInstant("2026-03-01T10:00:05Z")
	if err != nil || n != "2026-03-01T10:00:05.000Z" || len(n) != 24 {
		t.Errorf("NormaliseInstant = %q %v", n, err)
	}
}
