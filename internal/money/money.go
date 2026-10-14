// Package money is integer micro-USD arithmetic: ceil conversions from time,
// exact decimal parsing and formatting. It uses no floating point and imports
// only the standard library (M3 C68, D-M3-6).
package money

import (
	"errors"
	"math"
	"math/bits"
	"strconv"
	"strings"
)

// MicroUSD is an amount in millionths of a US dollar.
type MicroUSD int64

// PerUSD is one dollar in micro-USD.
const PerUSD MicroUSD = 1_000_000

const (
	msPerHour  = 3_600_000
	secPerHour = 3600
)

// mulDivCeil returns ceil(a*b/d) for a, b >= 0 and d > 0, panicking when the
// product or the result does not fit in an int64.
func mulDivCeil(a, b, d uint64) MicroUSD {
	hi, lo := bits.Mul64(a, b)
	if hi >= d { // quotient would not fit in 64 bits
		panic("money: overflow")
	}
	q, r := bits.Div64(hi, lo, d)
	if r != 0 {
		q++
		if q == 0 {
			panic("money: overflow")
		}
	}
	if q > math.MaxInt64 {
		panic("money: overflow")
	}
	return MicroUSD(q)
}

// ForSeconds is ceil(seconds * perHour / 3600). seconds < 0 or perHour < 0 panics.
func ForSeconds(seconds int64, perHour MicroUSD) MicroUSD {
	if seconds < 0 || perHour < 0 {
		panic("money: negative argument")
	}
	return mulDivCeil(uint64(seconds), uint64(perHour), secPerHour)
}

// ForMillis is ceil(ms * perHour / 3_600_000). ms < 0 or perHour < 0 panics.
func ForMillis(ms int64, perHour MicroUSD) MicroUSD {
	if ms < 0 || perHour < 0 {
		panic("money: negative argument")
	}
	return mulDivCeil(uint64(ms), uint64(perHour), msPerHour)
}

// WithMarginPPM is ceil(m * (1_000_000 + ppm) / 1_000_000) (D-M3-16). m and ppm must be >= 0.
func WithMarginPPM(m MicroUSD, ppm int64) MicroUSD {
	if m < 0 || ppm < 0 {
		panic("money: negative argument")
	}
	return mulDivCeil(uint64(m), uint64(1_000_000)+uint64(ppm), 1_000_000)
}

// ErrParse is returned for malformed or out-of-range amounts.
var ErrParse = errors.New("money: invalid amount")

// ParseUSD parses an exact non-negative decimal literal in dollars ("0.69",
// "1.5e-3", "12") into micro-USD. More than six decimals round up.
func ParseUSD(s string) (MicroUSD, error) {
	m, err := ParseUSDSigned(s)
	if err != nil {
		return 0, err
	}
	if m < 0 || strings.HasPrefix(strings.TrimSpace(s), "-") {
		return 0, ErrParse
	}
	return m, nil
}

// ParseUSDSigned is ParseUSD that also accepts a leading sign (billing
// adjustments). Fractions below one micro-USD round toward +infinity.
func ParseUSDSigned(s string) (MicroUSD, error) {
	s = strings.TrimSpace(s)
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	mant, exp := s, int64(0)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant = s[:i]
		e, err := strconv.ParseInt(s[i+1:], 10, 32)
		if err != nil {
			return 0, ErrParse
		}
		exp = e
	}
	intPart, frac, _ := strings.Cut(mant, ".")
	if intPart == "" && frac == "" {
		return 0, ErrParse
	}
	digits := intPart + frac
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, ErrParse
		}
	}
	digits = strings.TrimLeft(digits, "0")
	// value (micro-USD) = digits × 10^(exp − len(frac) + 6)
	pow := exp - int64(len(frac)) + 6
	if digits == "" {
		return 0, nil
	}
	var mag uint64
	if pow >= 0 {
		if len(digits) > 19 || pow > 19 {
			return 0, ErrParse
		}
		n, err := strconv.ParseUint(digits, 10, 64)
		if err != nil {
			return 0, ErrParse
		}
		p := pow10(int(pow))
		hi, lo := bits.Mul64(n, p)
		if hi != 0 {
			return 0, ErrParse
		}
		mag = lo
	} else {
		cut := int(-pow)
		if cut >= len(digits) {
			mag = 1 // nonzero fraction below one micro-USD
			if neg {
				mag = 0 // ceiling toward +infinity
			}
		} else {
			head, tail := digits[:len(digits)-cut], digits[len(digits)-cut:]
			n, err := strconv.ParseUint(head, 10, 64)
			if err != nil {
				return 0, ErrParse
			}
			if !neg && strings.Trim(tail, "0") != "" {
				n++
				if n == 0 {
					return 0, ErrParse
				}
			}
			mag = n
		}
	}
	if mag > math.MaxInt64 {
		return 0, ErrParse
	}
	if neg {
		return -MicroUSD(mag), nil
	}
	return MicroUSD(mag), nil
}

func pow10(n int) uint64 {
	p := uint64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

func abs(m MicroUSD) uint64 {
	if m < 0 {
		return uint64(-(m + 1)) + 1 //nolint:gosec // -(m+1) >= 0, so the conversion is exact (also for MinInt64)
	}
	return uint64(m)
}

// USD is the exact decimal dollar string: 326217 => "0.326217", 690000 => "0.69", 0 => "0".
func (m MicroUSD) USD() string {
	a := abs(m)
	whole, frac := a/1_000_000, a%1_000_000
	var b strings.Builder
	if m < 0 {
		b.WriteByte('-')
	}
	b.WriteString(strconv.FormatUint(whole, 10))
	if frac != 0 {
		f := strconv.FormatUint(frac+1_000_000, 10)[1:]
		b.WriteByte('.')
		b.WriteString(strings.TrimRight(f, "0"))
	}
	return b.String()
}

// Display is the UI/ntfy string: "$0.00" for zero, two decimals (half-up) when
// |m| >= 10_000 micro-USD, else four decimals (half-up).
func (m MicroUSD) Display() string { return m.display(false) }

// DisplayCeil is Display rounded up (magnitude), for "up to" amounts (D-M3-29).
func (m MicroUSD) DisplayCeil() string { return m.display(true) }

func (m MicroUSD) display(ceil bool) string {
	if m == 0 {
		return "$0.00"
	}
	a := abs(m)
	unit, dec := uint64(100), 4 // 4 decimals = units of 100 micro-USD
	if a >= 10_000 {
		unit, dec = 10_000, 2
	}
	add := unit / 2
	if ceil {
		add = unit - 1
	}
	n := (a + add) / unit
	s := strconv.FormatUint(n, 10)
	for len(s) <= dec {
		s = "0" + s
	}
	out := "$" + s[:len(s)-dec] + "." + s[len(s)-dec:]
	if m < 0 {
		out = "-" + out
	}
	return out
}
