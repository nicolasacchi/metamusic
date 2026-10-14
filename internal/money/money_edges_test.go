package money

import (
	"errors"
	"testing"
)

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", name)
		}
	}()
	f()
}

func TestMoney_ForMillisAndMargin(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		got, want MicroUSD
	}{
		{"an hour of milliseconds", ForMillis(3_600_000, 690_000), 690_000},
		{"one millisecond rounds up", ForMillis(1, 1), 1},
		{"zero is zero", ForMillis(0, 690_000), 0},
		{"ten percent margin", WithMarginPPM(1_000_000, 100_000), 1_100_000},
		{"the margin rounds up", WithMarginPPM(3, 1), 4},
		{"no margin", WithMarginPPM(12345, 0), 12345},
	} {
		if c.got != c.want {
			t.Errorf("%s: %d, want %d", c.name, c.got, c.want)
		}
	}
	mustPanic(t, "ForMillis(-1)", func() { ForMillis(-1, 1) })
	mustPanic(t, "ForMillis(perHour<0)", func() { ForMillis(1, -1) })
	mustPanic(t, "WithMarginPPM(m<0)", func() { WithMarginPPM(-1, 0) })
	mustPanic(t, "WithMarginPPM(ppm<0)", func() { WithMarginPPM(1, -1) })
	mustPanic(t, "ForSeconds(-1)", func() { ForSeconds(-1, 1) })
}

func TestMoney_ParseEdges(t *testing.T) {
	t.Parallel()
	bad := []string{
		"9999999999999999999e5",                       // the product does not fit in 64 bits
		"184467440737095516151e-7",                    // rounding a fraction up wraps past the largest uint64
		"9223372036854775808e-6",                      // one micro-USD past the largest int64
		"12345678901234567890123456789012345.1234567", // the integer head does not fit
		"1e", // an exponent without digits
		".",  // nothing at all
	}
	for _, s := range bad {
		if _, err := ParseUSDSigned(s); !errors.Is(err, ErrParse) {
			t.Errorf("ParseUSDSigned(%q) = %v, want ErrParse", s, err)
		}
	}
	// a negative fraction below one micro-USD rounds toward +infinity: to zero
	if got, err := ParseUSDSigned("-0.0000001"); err != nil || got != 0 {
		t.Errorf("ParseUSDSigned(-0.0000001) = %d, %v", got, err)
	}
	if got, err := ParseUSDSigned("0.0000001"); err != nil || got != 1 {
		t.Errorf("ParseUSDSigned(0.0000001) = %d, %v", got, err)
	}
}
