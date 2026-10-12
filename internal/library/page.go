package library

import (
	"encoding/base64"
	"encoding/json"
)

// Cursors are opaque base64url of {"o":<offset>} (a keyset can replace them
// without an API change).
func encodeCursor(offset int) string {
	b, _ := json.Marshal(struct {
		O int `json:"o"`
	}{offset})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(c string) (int, error) {
	if c == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, invalid("cursor", "malformed")
	}
	var v struct {
		O int `json:"o"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.O < 0 {
		return 0, invalid("cursor", "malformed")
	}
	return v.O, nil
}

// pager accumulates items until the page would exceed the byte cap.
type pager struct {
	size int
	n    int
}

// add reports whether item (already marshalled size) still fits; the first
// item always fits.
func (p *pager) add(itemSize int) bool {
	if p.n > 0 && p.size+itemSize+1 > maxPageBytes {
		return false
	}
	p.size += itemSize + 1
	p.n++
	return true
}

func sizeOf(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

func clampLimit(n, def, hi int) int {
	if n <= 0 {
		return def
	}
	if n > hi {
		return hi
	}
	return n
}
