// Package npy is a minimal, hardened reader for NumPy .npy integer arrays
// (C49.2). It is pure Go, never allocates more than the caller's byte budget
// (checked before allocation) and is fuzzed.
package npy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	// ErrUnsupported is returned for well-formed files this reader does not read.
	ErrUnsupported = errors.New("npy: unsupported array")
	// ErrCorrupt is returned for malformed files.
	ErrCorrupt = errors.New("npy: corrupt file")
	// ErrTooLarge is returned when the declared array exceeds the byte budget.
	ErrTooLarge = errors.New("npy: array exceeds size limit")
)

const (
	maxHeader = 64 << 10
	magic     = "\x93NUMPY"
	maxNDim   = 3
)

// Array holds a C-order integer array widened to int64.
type Array struct {
	DType string
	Shape []int
	Data  []int64
}

type kind struct {
	size   int
	signed bool
}

var dtypes = map[string]kind{
	"<i1": {1, true}, "|i1": {1, true}, "<i2": {2, true}, "<i4": {4, true}, "<i8": {8, true},
	"<u1": {1, false}, "|u1": {1, false}, "<u2": {2, false}, "<u4": {4, false}, "<u8": {8, false},
}

var (
	reDescr   = regexp.MustCompile(`'descr'\s*:\s*'([^']*)'`)
	reFortran = regexp.MustCompile(`'fortran_order'\s*:\s*(True|False)`)
	reShape   = regexp.MustCompile(`'shape'\s*:\s*\(([^)]*)\)`)
)

// Read parses a .npy stream. maxBytes bounds both the declared payload and the
// widened int64 result; it is checked before anything is allocated.
func Read(r io.Reader, maxBytes int64) (Array, error) {
	var pre [10]byte
	if _, err := io.ReadFull(r, pre[:8]); err != nil || string(pre[:6]) != magic {
		return Array{}, fmt.Errorf("%w: bad magic", ErrCorrupt)
	}
	var hlen int
	switch major := pre[6]; major {
	case 1:
		if _, err := io.ReadFull(r, pre[8:10]); err != nil {
			return Array{}, fmt.Errorf("%w: short header", ErrCorrupt)
		}
		hlen = int(binary.LittleEndian.Uint16(pre[8:10]))
	case 2, 3:
		var l [4]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return Array{}, fmt.Errorf("%w: short header", ErrCorrupt)
		}
		n := binary.LittleEndian.Uint32(l[:])
		if n > maxHeader {
			return Array{}, fmt.Errorf("%w: header too large", ErrCorrupt)
		}
		hlen = int(n)
	default:
		return Array{}, fmt.Errorf("%w: format version %d", ErrUnsupported, major)
	}
	if pre[7] != 0 {
		return Array{}, fmt.Errorf("%w: format version %d.%d", ErrUnsupported, pre[6], pre[7])
	}
	if hlen > maxHeader {
		return Array{}, fmt.Errorf("%w: header too large", ErrCorrupt)
	}
	hb := make([]byte, hlen)
	if _, err := io.ReadFull(r, hb); err != nil {
		return Array{}, fmt.Errorf("%w: short header", ErrCorrupt)
	}
	h := string(hb)
	dm, fm, sm := reDescr.FindStringSubmatch(h), reFortran.FindStringSubmatch(h), reShape.FindStringSubmatch(h)
	if dm == nil || fm == nil || sm == nil || !strings.HasPrefix(strings.TrimSpace(h), "{") {
		return Array{}, fmt.Errorf("%w: unparseable header", ErrCorrupt)
	}
	k, ok := dtypes[dm[1]]
	if !ok {
		return Array{}, fmt.Errorf("%w: dtype %q", ErrUnsupported, dm[1])
	}
	if fm[1] == "True" {
		return Array{}, fmt.Errorf("%w: fortran order", ErrUnsupported)
	}
	shape, err := parseShape(sm[1])
	if err != nil {
		return Array{}, err
	}
	n := int64(1)
	for _, d := range shape {
		if d != 0 && n > math.MaxInt64/int64(d) {
			return Array{}, ErrTooLarge
		}
		n *= int64(d)
	}
	if n > maxBytes/8 || n*int64(k.size) > maxBytes {
		return Array{}, fmt.Errorf("%w: %d elements", ErrTooLarge, n)
	}
	want := n * int64(k.size)
	payload := make([]byte, want)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Array{}, fmt.Errorf("%w: data shorter than the declared shape", ErrCorrupt)
	}
	var extra [1]byte
	if m, _ := io.ReadFull(r, extra[:]); m != 0 {
		return Array{}, fmt.Errorf("%w: data longer than the declared shape", ErrCorrupt)
	}
	out := make([]int64, n)
	for i := range out {
		p := payload[i*k.size:]
		switch {
		case k.size == 1 && k.signed:
			out[i] = int64(int8(p[0])) // #nosec G115 -- two-s complement reinterpretation
		case k.size == 1:
			out[i] = int64(p[0])
		case k.size == 2 && k.signed:
			out[i] = int64(int16(binary.LittleEndian.Uint16(p))) // #nosec G115 -- two-s complement reinterpretation
		case k.size == 2:
			out[i] = int64(binary.LittleEndian.Uint16(p))
		case k.size == 4 && k.signed:
			out[i] = int64(int32(binary.LittleEndian.Uint32(p))) // #nosec G115 -- two-s complement reinterpretation
		case k.size == 4:
			out[i] = int64(binary.LittleEndian.Uint32(p))
		case k.signed:
			out[i] = int64(binary.LittleEndian.Uint64(p)) // #nosec G115 -- two-s complement reinterpretation
		default:
			u := binary.LittleEndian.Uint64(p)
			if u > math.MaxInt64 {
				return Array{}, fmt.Errorf("%w: uint64 value above MaxInt64", ErrUnsupported)
			}
			out[i] = int64(u)
		}
	}
	return Array{DType: dm[1], Shape: shape, Data: out}, nil
}

func parseShape(s string) ([]int, error) {
	var shape []int
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		d, err := strconv.ParseInt(f, 10, 32)
		if err != nil || d < 0 {
			return nil, fmt.Errorf("%w: bad shape", ErrCorrupt)
		}
		shape = append(shape, int(d))
	}
	if len(shape) == 0 || len(shape) > maxNDim {
		return nil, fmt.Errorf("%w: %d dimensions", ErrUnsupported, len(shape))
	}
	return shape, nil
}
