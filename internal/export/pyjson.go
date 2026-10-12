// Package export renders requests the way the original Python tooling wrote them (songs.jsonl).
package export

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// Style is the textual style of one JSONL line, matching CPython json.dumps parameters.
type Style struct {
	KeyOrder    []string // nil => sorted keys; else this order, unknown keys appended in sorted order
	ItemSep     string   // ", " (Python default) | ","
	KeySep      string   // ": " (Python default) | ":"
	EnsureASCII bool
}

const hexdigits = "0123456789abcdef"

// EncodeRequest renders r as one line, byte for byte what CPython json.dumps(obj, ensure_ascii=...,
// separators=...) produces, without a trailing newline.
func EncodeRequest(r yue2.Request, s Style) []byte {
	vals := map[string]string{
		"id": quote(r.ID, s.EnsureASCII), "style": quote(r.Style, s.EnsureASCII),
		"lyrics": quote(r.Lyrics, s.EnsureASCII), "cot": quote(r.Cot, s.EnsureASCII),
		"seed": strconv.FormatInt(r.Seed, 10),
	}
	if r.ABC != nil {
		vals["abc"] = quote(*r.ABC, s.EnsureASCII)
	}
	if r.CFGScale != nil {
		vals["cfg_scale"] = PyFloat(*r.CFGScale)
	}
	var keys []string
	used := map[string]bool{}
	for _, k := range s.KeyOrder {
		if _, ok := vals[k]; ok && !used[k] {
			keys = append(keys, k)
			used[k] = true
		}
	}
	var rest []string
	for k := range vals {
		if !used[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)

	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteString(s.ItemSep)
		}
		b.WriteString(quote(k, s.EnsureASCII))
		b.WriteString(s.KeySep)
		b.WriteString(vals[k])
	}
	b.WriteByte('}')
	return []byte(b.String())
}

func u4(b *strings.Builder, c rune) {
	b.WriteString(`\u`)
	for sh := 12; sh >= 0; sh -= 4 {
		b.WriteByte(hexdigits[(c>>uint(sh))&0xf])
	}
}

// quote renders a JSON string like CPython's encoder.
func quote(s string, ascii bool) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\b':
			b.WriteString(`\b`)
		case c == '\f':
			b.WriteString(`\f`)
		case c < 0x20:
			u4(&b, c)
		case ascii && (c > 0x7e):
			if c > 0xffff {
				c -= 0x10000
				u4(&b, 0xd800|(c>>10))
				u4(&b, 0xdc00|(c&0x3ff))
			} else {
				u4(&b, c)
			}
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// PyFloat renders f like Python's repr(float): shortest round-trip digits, ".0" for integral values,
// exponent form (1e-05, 1e+16) when the decimal exponent is < -4 or >= 16.
func PyFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde+XX
	mant, exps, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(exps)
	if exp < -4 || exp >= 16 {
		sign := "+"
		if exp < 0 {
			sign = "-"
			exp = -exp
		}
		return mant + "e" + sign + pad2(exp)
	}
	out := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(out, ".") {
		out += ".0"
	}
	return out
}

func pad2(n int) string {
	s := strconv.Itoa(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}
