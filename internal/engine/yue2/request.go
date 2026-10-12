// Package yue2 is the pure domain model of a YuE2 generation request: validation and canonical
// hashing (mirroring yue2-infer protocol.py), lyric and ABC score section parsing, style terms, slugs
// and id grouping. It does no I/O.
package yue2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const DefaultSeed = 831001
const MaxSeed = 1<<53 - 1 // D-M1-7

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,179}$`) // not "." or ".."
var seedRe = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// Request is YuE2's native request (the recipe).
type Request struct {
	ID       string   `json:"id"`
	Style    string   `json:"style"`
	Lyrics   string   `json:"lyrics"`
	Cot      string   `json:"cot"` // full|melody|off
	Seed     int64    `json:"seed"`
	ABC      *string  `json:"abc,omitempty"`
	CFGScale *float64 `json:"cfg_scale,omitempty"`
}

type ParseOptions struct {
	// ResolveABCPath returns the bytes of an abc_path target. nil means any abc_path is the error
	// abc_path_unresolved.
	ResolveABCPath func(rel string) ([]byte, error)
	// AllowTitle: "title" is app metadata (Parsed.Title), not a dropped key.
	AllowTitle bool
}

type Parsed struct {
	Request   Request
	Canonical []byte   // CanonicalJSON(Request)
	Hash      string   // hex sha256(Canonical)
	CoreHash  string   // hex sha256 of the canonical {cot,id,lyrics,seed,style} only
	Title     string   // AllowTitle only
	ABCPath   string   // the abc_path value when abc was inlined from it
	Dropped   []string // keys present but not part of the canonical request
	Warnings  []string // codes
}

// ParseError carries a stable code. Detail never contains field values.
type ParseError struct{ Code, Detail string }

func (e *ParseError) Error() string {
	if e.Detail == "" {
		return "yue2: " + e.Code
	}
	return "yue2: " + e.Code + ": " + e.Detail
}

func perr(code, format string, a ...any) *ParseError {
	return &ParseError{Code: code, Detail: fmt.Sprintf(format, a...)}
}

var knownKeys = map[string]bool{
	"id": true, "style": true, "tags": true, "lyrics": true, "cot": true, "seed": true,
	"abc": true, "abc_path": true, "cfg_scale": true, "prompt": true, "title": true,
	"abc_sampling": true, "semantic_sampling": true,
}

func isNull(r json.RawMessage) bool { return string(bytes.TrimSpace(r)) == "null" }

// present returns the value of key k when it exists and is not JSON null.
func present(m map[string]json.RawMessage, k string) (json.RawMessage, bool) {
	v, ok := m[k]
	if !ok || isNull(v) {
		return nil, false
	}
	return v, true
}

// ParseRequest validates raw (one JSON object) and returns the canonical request.
func ParseRequest(raw []byte, o ParseOptions) (Parsed, error) {
	var p Parsed
	if !utf8.Valid(raw) {
		return p, perr("bad_utf8", "input is not valid UTF-8")
	}
	if bytes.HasPrefix(raw, []byte("\xef\xbb\xbf")) {
		raw = raw[3:]
		p.Warnings = append(p.Warnings, "bom")
	}
	if bytes.IndexByte(raw, '\r') >= 0 {
		p.Warnings = append(p.Warnings, "crlf")
	}
	raw = bytes.TrimSpace(raw)
	if badSurrogate(raw) {
		return p, perr("bad_utf8", "string contains an unpaired surrogate escape")
	}
	if len(raw) == 0 || raw[0] != '{' {
		return p, perr("not_object", "input is not a JSON object")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return p, perr("not_object", "input is not a single JSON object")
	}

	var r Request

	// id
	idRaw, ok := present(m, "id")
	if !ok {
		return p, perr("missing_id", "id is required")
	}
	if err := json.Unmarshal(idRaw, &r.ID); err != nil || !idRe.MatchString(r.ID) || r.ID == "." || r.ID == ".." {
		return p, perr("bad_id", "id must match %s", idRe.String())
	}

	// style / tags
	var style, tags *string
	for _, f := range []struct {
		k string
		d **string
	}{{"style", &style}, {"tags", &tags}} {
		if v, ok := present(m, f.k); ok {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return p, perr("missing_style", "%s must be a string", f.k)
			}
			*f.d = &s
		}
	}
	switch {
	case style != nil && tags != nil && *style != *tags:
		return p, perr("style_tags_mismatch", "style and tags differ")
	case style != nil:
		r.Style = *style
	case tags != nil:
		r.Style = *tags
		p.Warnings = append(p.Warnings, "tags_alias")
	default:
		return p, perr("missing_style", "style is required")
	}

	// lyrics
	lv, ok := present(m, "lyrics")
	if !ok {
		return p, perr("missing_lyrics", "lyrics is required")
	}
	if err := json.Unmarshal(lv, &r.Lyrics); err != nil {
		return p, perr("missing_lyrics", "lyrics must be a string")
	}

	// cot
	if v, ok := m["cot"]; ok { // null is not "absent" for cot: YuE2 rejects it
		if err := json.Unmarshal(v, &r.Cot); err != nil {
			return p, perr("bad_cot", "cot must be full, melody or off")
		}
		if _, ok := Instructions[r.Cot]; !ok {
			return p, perr("bad_cot", "cot must be full, melody or off")
		}
	} else {
		r.Cot = "full"
		p.Warnings = append(p.Warnings, "cot_defaulted")
	}

	// seed
	if v, ok := m["seed"]; ok {
		tok := string(bytes.TrimSpace(v))
		n, err := strconv.ParseInt(tok, 10, 64)
		if !seedRe.MatchString(tok) || err != nil || n < 0 || n > MaxSeed {
			return p, perr("seed_out_of_range", "seed must be an integer in 0..%d", int64(MaxSeed))
		}
		r.Seed = n
	} else {
		r.Seed = DefaultSeed
		p.Warnings = append(p.Warnings, "seed_defaulted")
	}

	// abc / abc_path
	abcV, hasABC := present(m, "abc")
	pathV, hasPath := present(m, "abc_path")
	if hasABC && hasPath {
		return p, perr("abc_conflict", "abc and abc_path are mutually exclusive")
	}
	var abc string
	switch {
	case hasABC:
		if err := json.Unmarshal(abcV, &abc); err != nil {
			return p, perr("bad_abc", "abc must be a string")
		}
	case hasPath:
		var rel string
		if err := json.Unmarshal(pathV, &rel); err != nil || o.ResolveABCPath == nil {
			return p, perr("abc_path_unresolved", "abc_path cannot be resolved")
		}
		b, err := o.ResolveABCPath(rel)
		if err != nil {
			return p, perr("abc_path_unresolved", "abc_path cannot be resolved")
		}
		if !utf8.Valid(b) {
			return p, perr("bad_utf8", "abc_path target is not valid UTF-8")
		}
		abc = string(b)
		p.ABCPath = rel
	}
	if hasABC || hasPath {
		if strings.TrimSpace(abc) == "" {
			return p, perr("empty_abc", "abc is empty")
		}
		if r.Cot == "off" {
			return p, perr("abc_requires_cot", "abc requires cot melody or full")
		}
		r.ABC = &abc
	}

	// cfg_scale
	if v, ok := present(m, "cfg_scale"); ok {
		var f float64
		if err := json.Unmarshal(v, &f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 20 {
			return p, perr("bad_cfg_scale", "cfg_scale must be a number in [0,20]")
		}
		if f == 0 {
			f = 0 // normalise -0
		}
		def := 1.0
		if r.Cot == "off" {
			def = 1.01
		}
		if f == def {
			p.Warnings = append(p.Warnings, "cfg_scale_default_dropped")
		} else {
			r.CFGScale = &f
		}
	}

	// prompt
	if v, ok := present(m, "prompt"); ok {
		var s string
		want := Instructions[r.Cot] + "\n[Tags]\n" + r.Style + "\n[Lyrics]\n" + r.Lyrics + "\n"
		if err := json.Unmarshal(v, &s); err != nil || s != want {
			return p, perr("prompt_mismatch", "prompt does not match the native instruction text")
		}
		p.Warnings = append(p.Warnings, "prompt_verified")
		p.Dropped = append(p.Dropped, "prompt")
	}

	// sampling
	for _, k := range []string{"abc_sampling", "semantic_sampling"} {
		if _, ok := present(m, k); ok {
			return p, perr("sampling_unsupported", "%s changes the render and is not supported", k)
		}
	}

	// dropped keys, deterministic order
	var extra []string
	for k := range m {
		if k == "title" {
			if o.AllowTitle {
				if v, ok := present(m, k); ok {
					var s string
					if json.Unmarshal(v, &s) == nil {
						p.Title = s
					}
				}
				continue
			}
			extra = append(extra, k)
			continue
		}
		if !knownKeys[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		p.Dropped = append(p.Dropped, k)
		p.Warnings = append(p.Warnings, "dropped_key:"+safeKey(k))
	}

	p.Request = r
	p.Canonical = CanonicalJSON(r)
	p.Hash = hashHex(p.Canonical)
	core := r
	core.ABC, core.CFGScale = nil, nil
	p.CoreHash = hashHex(CanonicalJSON(core))
	return p, nil
}

// safeKey bounds and sanitises a key name for use inside a warning code.
func safeKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		if !unicode.IsPrint(r) {
			r = '?'
		}
		if b.Len()+utf8.RuneLen(r) > 64 {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// badSurrogate reports a \uD800-\uDFFF escape inside a JSON string that is not part of a valid
// high+low pair (encoding/json would silently turn it into U+FFFD).
func badSurrogate(raw []byte) bool {
	hex4 := func(i int) (int, bool) {
		if i+6 > len(raw) || raw[i] != '\\' || raw[i+1] != 'u' {
			return 0, false
		}
		n, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 32)
		return int(n), err == nil
	}
	inStr := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case !inStr:
			inStr = c == '"'
		case c == '"':
			inStr = false
		case c == '\\':
			if n, ok := hex4(i); ok && n >= 0xD800 && n <= 0xDFFF {
				if n >= 0xDC00 {
					return true
				}
				lo, ok := hex4(i + 6)
				if !ok || lo < 0xDC00 || lo > 0xDFFF {
					return true
				}
				i += 11
				continue
			}
			i++
		}
	}
	return false
}
