package yue2

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Mode selects the rule set of CheckRequest.
type Mode int

const (
	// ModeYuE2 is exactly what `yue2 batch` (yue2-infer 0.1.6) accepts.
	ModeYuE2 Mode = iota
	// ModeApp is M1's ParseRequest rules mapped to issues.
	ModeApp
)

// Issue is one protocol or lint finding. Message never contains field values.
type Issue struct {
	Code     string `json:"code"`
	Field    string `json:"field,omitempty"`   // JSON key ("seed", "lyrics", ...)
	Severity string `json:"severity"`          // "error" | "warning"
	Message  string `json:"message"`           // never contains field values
	Line     int    `json:"line,omitempty"`    // lyrics line (1-based) or JSONL line
	Section  string `json:"section,omitempty"` // lyrics section label
	Fatal    bool   `json:"fatal,omitempty"`   // ModeYuE2: aborts the whole batch in 0.1.6
	// Details carries machine-readable numbers of a finding (the duration warnings: estimated_s, min_s, ...).
	Details map[string]any `json:"details,omitempty"`
}

// ABCResolver resolves an abc_path. nil means every abc_path is abc_path_unresolved.
type ABCResolver func(rel string) ([]byte, error)

const (
	sevError   = "error"
	sevWarning = "warning"
)

// errorField maps a ParseError code to the JSON key it concerns.
var errorField = map[string]string{
	"abc_conflict": "abc_path", "abc_path_unresolved": "abc_path", "abc_requires_cot": "abc",
	"bad_abc": "abc", "bad_cfg_scale": "cfg_scale", "bad_cot": "cot", "bad_id": "id",
	"empty_abc": "abc", "missing_id": "id", "missing_lyrics": "lyrics", "missing_style": "style",
	"prompt_mismatch": "prompt", "sampling_unsupported": "semantic_sampling",
	"seed_out_of_range": "seed", "style_tags_mismatch": "tags",
}

// warningField maps a Parsed.Warnings code to its JSON key.
var warningField = map[string]string{
	"tags_alias": "tags", "cot_defaulted": "cot", "seed_defaulted": "seed",
	"cfg_scale_default_dropped": "cfg_scale", "prompt_verified": "prompt",
}

var warningMsg = map[string]string{
	"bom":                       "leading byte order mark was removed",
	"crlf":                      "carriage returns in the input",
	"tags_alias":                "tags used as an alias of style",
	"cot_defaulted":             "cot defaulted to full",
	"seed_defaulted":            "seed defaulted",
	"cfg_scale_default_dropped": "cfg_scale equals the default and was dropped",
	"prompt_verified":           "prompt matches the native instruction text and was dropped",
}

// CheckRequest checks one request (a single JSON value) in the given mode. ModeApp is exactly
// ParseRequest mapped to issues, so the editor's lint and create_draft cannot disagree.
func CheckRequest(raw []byte, m Mode, abc ABCResolver) []Issue {
	if m == ModeApp {
		return checkApp(raw, abc)
	}
	return checkYuE2(raw, abc)
}

func checkApp(raw []byte, abc ABCResolver) []Issue {
	p, err := ParseRequest(raw, ParseOptions{ResolveABCPath: abc})
	var out []Issue
	if err != nil {
		var pe *ParseError
		if !errors.As(err, &pe) {
			pe = &ParseError{Code: "not_object"}
		}
		out = append(out, Issue{Code: pe.Code, Field: errorField[pe.Code], Severity: sevError, Message: pe.Detail})
		return out
	}
	for _, w := range p.Warnings {
		is := Issue{Code: w, Severity: sevWarning, Message: warningMsg[w]}
		if k, ok := strings.CutPrefix(w, "dropped_key:"); ok {
			is.Field = k
			is.Message = "key is not part of the canonical request and was dropped"
		} else {
			is.Field = warningField[w]
		}
		out = append(out, is)
	}
	return out
}

var yue2Keys = map[string]bool{
	"id": true, "style": true, "tags": true, "lyrics": true, "cot": true, "seed": true, "abc": true,
	"abc_path": true, "cfg_scale": true, "abc_sampling": true, "semantic_sampling": true,
	"lang": true, "eval_index": true, "clip_id": true, "prompt": true,
}

var intTokenRe = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

func isJSONObject(v json.RawMessage) bool {
	t := bytes.TrimSpace(v)
	return len(t) > 0 && t[0] == '{'
}

// checkYuE2 mirrors yue2-infer 0.1.6 src/yue2/protocol.py (SongRequest, request_kwargs) and the
// per-row handling in src/yue2/cli.py (batch). Each rule cites the construct it mirrors.
func checkYuE2(raw []byte, abc ABCResolver) []Issue {
	var out []Issue
	add := func(code, field, msg string) {
		out = append(out, Issue{Code: code, Field: field, Severity: sevError, Message: msg})
	}
	// Any string that is not valid UTF-8 (protocol.py: the JSONL is read as UTF-8 text).
	if !utf8.Valid(raw) || badSurrogate(raw) {
		add("bad_utf8", "", "input is not valid UTF-8")
		return out
	}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' {
		add("not_object", "", "input is not a JSON object")
		return out
	}
	var m map[string]json.RawMessage // duplicate keys: last wins, like Python's json.loads
	if err := json.Unmarshal(t, &m); err != nil {
		add("not_object", "", "input is not a single JSON object")
		return out
	}
	str := func(k string) (string, bool) {
		v, ok := present(m, k)
		if !ok {
			return "", false
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			return "", false
		}
		return s, true
	}

	// Rule 1: allowed keys; cli.py batch raises outside the per-row try, aborting the batch.
	var unknown []string
	for k := range m {
		if !yue2Keys[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		out = append(out, Issue{Code: "unknown_key", Field: safeKey(k), Severity: sevError,
			Message: "key is not accepted by yue2 batch", Fatal: true})
	}

	// Rule 2: SongRequest.id.
	id, hasID := str("id")
	switch {
	case !hasID && !hasNonNull(m, "id"):
		add("missing_id", "id", "id is required")
	case !hasID || !idRe.MatchString(id) || id == "." || id == "..":
		add("bad_id", "id", "id must match "+idRe.String())
	}

	// Rule 3: style / tags (request_kwargs accepts either).
	style, hasStyle := str("style")
	tags, hasTags := str("tags")
	switch {
	case hasStyle && hasTags:
		if style != tags {
			add("style_tags_mismatch", "tags", "style and tags differ")
		}
	case hasTags:
		style, hasStyle = tags, true
	case !hasStyle:
		add("missing_style", "style", "style or tags is required (a string)")
	}

	// Rule 4: lyrics.
	lyrics, hasLyrics := str("lyrics")
	if !hasLyrics {
		add("missing_lyrics", "lyrics", "lyrics is required (a string)")
	}

	// Rule 5: cot (null is not "absent": SongRequest rejects it).
	cot := "full"
	cotOK := true
	if v, ok := m["cot"]; ok {
		var c string
		cotOK = json.Unmarshal(v, &c) == nil && !isNull(v)
		if _, known := Instructions[c]; !known {
			cotOK = false
		}
		if cotOK {
			cot = c
		}
		if !cotOK {
			add("bad_cot", "cot", "cot must be full, melody or off")
		}
	}

	// Rule 6: seed is an integer token (no fraction, exponent or bool) in [0, 2^63).
	if v, ok := m["seed"]; ok {
		tok := string(bytes.TrimSpace(v))
		n, err := strconv.ParseInt(tok, 10, 64)
		if !intTokenRe.MatchString(tok) || err != nil || n < 0 {
			add("seed_out_of_range", "seed", "seed must be an integer in 0..2^63-1")
		}
	}

	// Rule 7: abc / abc_path.
	abcV, hasABC := present(m, "abc")
	pathV, hasPath := present(m, "abc_path")
	var abcText string
	haveText := false
	switch {
	case hasABC && hasPath:
		add("abc_conflict", "abc_path", "abc and abc_path are mutually exclusive")
	case hasABC:
		if json.Unmarshal(abcV, &abcText) != nil {
			add("bad_abc", "abc", "abc must be a string")
		} else {
			haveText = true
		}
	case hasPath:
		var rel string
		if json.Unmarshal(pathV, &rel) != nil || abc == nil {
			add("abc_path_unresolved", "abc_path", "abc_path cannot be resolved")
		} else if b, err := abc(rel); err != nil {
			add("abc_path_unresolved", "abc_path", "abc_path cannot be resolved")
		} else if !utf8.Valid(b) {
			add("bad_utf8", "abc_path", "abc_path target is not valid UTF-8")
		} else {
			abcText, haveText = string(b), true
		}
	}
	if haveText {
		if strings.TrimSpace(abcText) == "" {
			add("empty_abc", "abc", "abc is empty")
		}
		if cotOK && cot == "off" {
			add("abc_requires_cot", "abc", "abc requires cot melody or full")
		}
	}

	// Rule 8: cfg_scale, null ok; finite number in [0, 20].
	if v, ok := present(m, "cfg_scale"); ok {
		var f float64
		if err := json.Unmarshal(v, &f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 20 {
			add("bad_cfg_scale", "cfg_scale", "cfg_scale must be a number in [0,20]")
		}
	}

	// Rule 9: prompt must equal the native instruction text.
	if v, ok := present(m, "prompt"); ok && cotOK && hasStyle && hasLyrics {
		var s string
		want := Instructions[cot] + "\n[Tags]\n" + style + "\n[Lyrics]\n" + lyrics + "\n"
		if json.Unmarshal(v, &s) != nil || s != want {
			add("prompt_mismatch", "prompt", "prompt does not match the native instruction text")
		}
	}

	// Rule 10: sampling blocks must be objects; contents are not validated (documented gap).
	for _, k := range []string{"abc_sampling", "semantic_sampling"} {
		if v, ok := present(m, k); ok && !isJSONObject(v) {
			add("bad_sampling", k, k+" must be an object")
		}
	}
	return out
}

func hasNonNull(m map[string]json.RawMessage, k string) bool {
	_, ok := present(m, k)
	return ok
}
