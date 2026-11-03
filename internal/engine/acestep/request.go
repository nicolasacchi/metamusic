// Package acestep is the pure domain model of the ACE-Step 1.5 engine (E2 C148): the engine-native request
// (the recipe), strict parsing and canonical bytes, validation against the effective caps, hashes, expected
// audio seconds, duration and runtime limits, the pinned weights, the verified-features file, the pinned engine
// parameters, the translation from a YuE2 recipe, and the engine.Engine, RecipeCodec and IngestPolicy
// implementations. It does no I/O and imports engine, canonjson and (for FromYuE2 only) yue2.
//
// Validation findings are engine.Issue values. Code is a stable rule name (bad_id, range_order,
// exceeds_max_runtime, ...) except for the two upper-case codes callers map to HTTP errors: ENGINE_CAPABILITY
// (Message "capability=<name>") and ENGINE_PARAMS_STALE.
package acestep

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/canonjson"
	"github.com/nicolasacchi/metamusic/internal/engine"
)

// Name is the registry key and recipes.engine value.
const Name = "acestep15"

// TurboSteps is the fixed inference step count of the turbo class.
const TurboSteps = 8

// Base-class defaults (C148.3).
const (
	DefaultBaseSteps     = 50
	DefaultGuidanceMilli = 7000
)

// Task classes (D-E2-3).
const (
	ClassTurbo = "turbo" // text2music, repaint, extend
	ClassBase  = "base"  // complete, extract
)

// Tasks of a request.
const (
	TaskText2Music = "text2music"
	TaskRepaint    = "repaint"
	TaskExtend     = "extend"
	TaskComplete   = "complete"
	TaskExtract    = "extract"
)

var allTasks = []string{TaskText2Music, TaskRepaint, TaskExtend, TaskComplete, TaskExtract}

// Structural limits (C148.4).
const (
	MaxSeed        = 1<<53 - 1
	MaxCaptionLen  = 1000
	MaxLyricsBytes = 64 << 10
	maxDurationDS  = 10 * AbsoluteMaxS
	segMinDS       = 10 * SegmentMinS
	segMaxDS       = 10 * SegmentMaxS
	refMinDS       = 200
	refMaxDS       = 300
)

// Request is the canonical engine-native request (recipes.request_json). Durations are integer deciseconds
// (D-E2-28); no float appears in it.
type Request struct {
	ID            string   `json:"id"`
	Task          string   `json:"task"`
	Caption       string   `json:"caption"`
	Lyrics        string   `json:"lyrics"`
	Instrumental  bool     `json:"instrumental"`
	VocalLanguage string   `json:"vocal_language"`
	BPM           *int     `json:"bpm,omitempty"`
	KeyScale      string   `json:"keyscale,omitempty"`
	TimeSignature string   `json:"timesignature,omitempty"`
	DurationDS    int      `json:"duration_ds,omitempty"`
	Seed          int64    `json:"seed"`
	Thinking      bool     `json:"thinking"`
	LRC           bool     `json:"lrc,omitempty"`
	ParamsVersion string   `json:"engine_params_version"`
	Steps         int      `json:"inference_steps"`
	GuidanceMilli *int     `json:"guidance_milli,omitempty"`
	Src           *SrcRef  `json:"src,omitempty"`
	Ref           *RefRef  `json:"ref,omitempty"`
	RepaintFromDS *int     `json:"repaint_from_ds,omitempty"`
	RepaintToDS   *int     `json:"repaint_to_ds,omitempty"`
	ExtendDS      *int     `json:"extend_ds,omitempty"`
	Tracks        []string `json:"tracks,omitempty"`
}

// SrcRef pins the source audio of an edit by track and master digest.
type SrcRef struct {
	TrackID      string `json:"track_id"`
	MasterSHA256 string `json:"master_sha256"`
	DurationDS   int    `json:"duration_ds"`
}

// RefRef pins reference audio; the clip is always exactly 30.000 s (a 2..30 s window tiled, D-E2-9).
type RefRef struct {
	TrackID    string `json:"track_id"`
	ClipSHA256 string `json:"clip_sha256"`
	FromDS     int    `json:"from_ds"`
	ToDS       int    `json:"to_ds"`
}

// StemNames are the twelve track names of ACE-Step's lego/extract/complete documentation, sorted.
var StemNames = []string{"backing_vocals", "bass", "brass", "drums", "fx", "guitar", "keyboard", "percussion", "strings", "synth", "vocals", "woodwinds"}

var (
	idRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,179}$`)
	trackRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	shaRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	keyRe    = regexp.MustCompile(`^([A-Ga-g])([#b♯♭]?)\s*(major|minor|maj|min|m|M)?$`)
	keyCanon = regexp.MustCompile(`^[A-G][#b]? (Major|Minor)$`)
)

// Class is the model class that renders the request: base for complete and extract.
func (r Request) Class() string {
	if r.Task == TaskComplete || r.Task == TaskExtract {
		return ClassBase
	}
	return ClassTurbo
}

func issue(code, field, format string, a ...any) engine.Issue {
	return engine.Issue{Code: code, Field: field, Severity: "error", Message: fmt.Sprintf(format, a...)}
}

func warn(code, field, format string, a ...any) engine.Issue {
	i := issue(code, field, format, a...)
	i.Severity = "warning"
	return i
}

func capability(field, c string) engine.Issue {
	return issue("ENGINE_CAPABILITY", field, "capability=%s", c)
}

// HasErrors reports whether any issue is an error.
func HasErrors(is []engine.Issue) bool {
	return slices.ContainsFunc(is, func(i engine.Issue) bool { return i.Severity == "error" })
}

// Canonical returns the canonical bytes of r (keys sorted, compact, no HTML escaping).
func Canonical(r Request) ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return canonjson.Canonical(b)
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// Parse reads one request strictly: unknown keys, wrong types and duplicate keys are errors; absent
// vocal_language, engine_params_version, inference_steps and (base class) guidance_milli are filled with the
// pinned defaults and the keyscale and track list are normalised, so the returned canonical bytes parse to
// themselves. The canonical bytes are nil when any error is returned. Duration caps and capabilities are not
// checked here (see Check).
func Parse(raw json.RawMessage) (Request, []byte, []engine.Issue) {
	var r Request
	fatal := func(code, format string, a ...any) (Request, []byte, []engine.Issue) {
		i := issue(code, "", format, a...)
		i.Fatal = true
		return r, nil, []engine.Issue{i}
	}
	if !utf8.Valid(raw) {
		return fatal("bad_utf8", "input is not valid UTF-8")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return fatal("not_object", "input is not a JSON object")
	}
	if _, err := canonjson.Canonical(raw); err != nil {
		return fatal("bad_json", "%v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		var te *json.UnmarshalTypeError
		switch {
		case errors.As(err, &te):
			return fatal("bad_type", "field %s has the wrong type", te.Field)
		case strings.HasPrefix(err.Error(), "json: unknown field"):
			return fatal("unknown_key", "%s", strings.TrimPrefix(err.Error(), "json: "))
		}
		return fatal("bad_json", "%v", err)
	}
	var issues []engine.Issue
	if r.VocalLanguage == "" {
		r.VocalLanguage = "unknown"
	}
	if r.KeyScale != "" {
		if k, ok := NormalizeKey(r.KeyScale); ok {
			if k != r.KeyScale {
				issues = append(issues, warn("keyscale_normalised", "keyscale", "keyscale normalised"))
			}
			r.KeyScale = k
		}
	}
	slices.Sort(r.Tracks)
	if r.ParamsVersion == "" {
		r.ParamsVersion = CurrentParamsVersion()
	} else if _, ok := paramsFile().Versions[r.ParamsVersion]; !ok {
		issues = append(issues, issue("ENGINE_PARAMS_STALE", "engine_params_version", "unknown engine_params_version"))
	}
	if r.Steps == 0 {
		r.Steps = TurboSteps
		if r.Class() == ClassBase {
			r.Steps = DefaultBaseSteps
		}
	}
	if r.Class() == ClassBase && r.GuidanceMilli == nil {
		g := DefaultGuidanceMilli
		r.GuidanceMilli = &g
	}
	issues = append(issues, structural(r)...)
	if HasErrors(issues) {
		return r, nil, issues
	}
	c, err := Canonical(r)
	if err != nil {
		return r, nil, append(issues, issue("bad_json", "", "%v", err))
	}
	return r, c, issues
}

// NormalizeKey maps "Am", "c major", "F#m", "Bb" ... to "<A-G>[#|b] <Major|Minor>"; false when it is not a key.
func NormalizeKey(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if keyCanon.MatchString(s) {
		return s, true
	}
	m := keyRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	acc := strings.NewReplacer("♯", "#", "♭", "b").Replace(m[2])
	scale := "Major"
	switch {
	case m[3] == "m", strings.EqualFold(m[3], "minor"), strings.EqualFold(m[3], "min"):
		scale = "Minor"
	}
	return strings.ToUpper(m[1]) + acc + " " + scale, true
}

func rangeInt(v, lo, hi int) bool { return v >= lo && v <= hi }

// structural is the checks that do not depend on the engine's effective caps (C148.4).
func structural(r Request) []engine.Issue {
	var is []engine.Issue
	add := func(i engine.Issue) { is = append(is, i) }
	if r.ID == "" {
		add(issue("missing_id", "id", "id is required"))
	} else if !idRe.MatchString(r.ID) || r.ID == "." || r.ID == ".." {
		add(issue("bad_id", "id", "id must match %s", idRe))
	}
	if r.Task == "" {
		add(issue("missing_task", "task", "task is required"))
		return is
	} else if !slices.Contains(allTasks, r.Task) {
		add(capability("task", "task:"+r.Task))
		return is
	}
	if strings.TrimSpace(r.Caption) == "" || utf8.RuneCountInString(r.Caption) > MaxCaptionLen {
		add(issue("bad_caption", "caption", "caption must be 1..%d runes", MaxCaptionLen))
	}
	if len(r.Lyrics) > MaxLyricsBytes {
		add(issue("lyrics_too_long", "lyrics", "lyrics exceed %d bytes", MaxLyricsBytes))
	}
	lyricTask := r.Task == TaskText2Music || r.Task == TaskRepaint || r.Task == TaskExtend
	switch {
	case r.Instrumental && r.Lyrics != "":
		add(issue("instrumental_lyrics", "lyrics", "instrumental requests carry no lyrics"))
	case !r.Instrumental && r.Lyrics == "" && lyricTask:
		add(issue("lyrics_required", "lyrics", "lyrics are required unless instrumental"))
	}
	if !slices.Contains(languageList(), r.VocalLanguage) {
		add(issue("bad_vocal_language", "vocal_language", "vocal_language is not in the language list"))
	}
	if r.BPM != nil && !rangeInt(*r.BPM, 30, 300) {
		add(issue("bad_bpm", "bpm", "bpm must be 30..300"))
	}
	if r.KeyScale != "" && !keyCanon.MatchString(r.KeyScale) {
		add(issue("bad_keyscale", "keyscale", "keyscale must be <A-G>[#|b] <Major|Minor>"))
	}
	if r.TimeSignature != "" && !slices.Contains([]string{"2", "3", "4", "6"}, r.TimeSignature) {
		add(issue("bad_timesignature", "timesignature", "timesignature must be 2, 3, 4 or 6"))
	}
	if r.Seed < 0 || r.Seed > MaxSeed {
		add(issue("bad_seed", "seed", "seed must be 0..%d", MaxSeed))
	}
	if r.Thinking && r.Task != TaskText2Music {
		add(issue("thinking_ignored", "thinking", "the planner LM runs for text2music only"))
	}
	if r.LRC {
		if r.Task != TaskText2Music {
			add(issue("lrc_task", "lrc", "lrc is a text2music option"))
		}
		if r.Instrumental || r.Lyrics == "" {
			add(issue("lrc_requires_lyrics", "lrc", "lrc needs lyrics"))
		}
	}
	if r.Class() == ClassTurbo {
		if r.Steps != TurboSteps {
			add(issue("bad_steps", "inference_steps", "turbo steps are fixed at %d", TurboSteps))
		}
		if r.GuidanceMilli != nil {
			add(issue("guidance_forbidden", "guidance_milli", "guidance_milli is a base-class parameter"))
		}
	} else {
		if !rangeInt(r.Steps, 32, 64) {
			add(issue("bad_steps", "inference_steps", "base steps must be 32..64"))
		}
		if r.GuidanceMilli == nil || !rangeInt(*r.GuidanceMilli, 1000, 15000) {
			add(issue("bad_guidance", "guidance_milli", "guidance_milli must be 1000..15000"))
		}
	}
	is = append(is, structuralTask(r)...)
	return is
}

func structuralTask(r Request) []engine.Issue {
	var is []engine.Issue
	add := func(i engine.Issue) { is = append(is, i) }
	if r.Task == TaskText2Music {
		switch {
		case r.DurationDS == 0:
			add(issue("duration_required", "duration_ds", "text2music needs duration_ds"))
		case r.DurationDS < 10*MinS:
			add(issue("duration_too_short", "duration_ds", "duration_ds must be at least %d", 10*MinS))
		case r.DurationDS > maxDurationDS:
			add(issue("duration_too_long", "duration_ds", "duration_ds above the structural ceiling %d", maxDurationDS))
		}
	} else if r.DurationDS != 0 {
		add(issue("duration_forbidden", "duration_ds", "duration_ds is a text2music field"))
	}
	if r.Task == TaskText2Music {
		if r.Src != nil {
			add(issue("src_forbidden", "src", "text2music has no source"))
		}
	} else if r.Src == nil {
		add(issue("src_required", "src", "%s needs a source", r.Task))
	} else {
		is = append(is, structuralSrc(*r.Src)...)
	}
	if r.Ref != nil {
		if r.Task != TaskText2Music && r.Task != TaskRepaint {
			add(issue("ref_forbidden", "ref", "reference audio is for text2music and repaint"))
		}
		is = append(is, structuralRef(*r.Ref)...)
	}
	srcDS := 0
	if r.Src != nil {
		srcDS = r.Src.DurationDS
	}
	// repaint range
	if r.Task == TaskRepaint {
		is = append(is, structuralRange(r, srcDS)...)
	} else if r.RepaintFromDS != nil || r.RepaintToDS != nil {
		add(issue("range_forbidden", "repaint_from_ds", "a range is a repaint field"))
	}
	// extend
	switch {
	case r.Task == TaskExtend && r.ExtendDS == nil:
		add(issue("extend_required", "extend_ds", "extend needs extend_ds"))
	case r.Task == TaskExtend:
		switch e := *r.ExtendDS; {
		case e < segMinDS:
			add(issue("extend_too_short", "extend_ds", "extend_ds must be at least %d", segMinDS))
		case e > segMaxDS:
			add(issue("extend_too_long", "extend_ds", "extend_ds above %d: chain separate jobs", segMaxDS))
		}
		if srcDS+*r.ExtendDS > maxDurationDS {
			add(issue("total_over_600", "extend_ds", "source plus extension exceeds %d s", AbsoluteMaxS))
		}
	case r.ExtendDS != nil:
		add(issue("extend_forbidden", "extend_ds", "extend_ds is an extend field"))
	}
	is = append(is, structuralTracks(r)...)
	return is
}

func structuralSrc(s SrcRef) []engine.Issue {
	var is []engine.Issue
	if !trackRe.MatchString(s.TrackID) {
		is = append(is, issue("bad_src", "src.track_id", "src.track_id is not a track id"))
	}
	if !shaRe.MatchString(s.MasterSHA256) {
		is = append(is, issue("bad_src", "src.master_sha256", "src.master_sha256 must be 64 lowercase hex"))
	}
	switch {
	case s.DurationDS < 10*MinS:
		is = append(is, issue("src_too_short", "src.duration_ds", "source shorter than %d s", MinS))
	case s.DurationDS > maxDurationDS:
		is = append(is, issue("src_too_long", "src.duration_ds", "source longer than %d s", AbsoluteMaxS))
	}
	return is
}

func structuralRef(f RefRef) []engine.Issue {
	var is []engine.Issue
	if !trackRe.MatchString(f.TrackID) {
		is = append(is, issue("bad_ref", "ref.track_id", "ref.track_id is not a track id"))
	}
	if !shaRe.MatchString(f.ClipSHA256) {
		is = append(is, issue("bad_ref", "ref.clip_sha256", "ref.clip_sha256 must be 64 lowercase hex"))
	}
	if f.FromDS < 0 || f.ToDS <= f.FromDS || f.ToDS-f.FromDS < refMinDS || f.ToDS-f.FromDS > refMaxDS {
		is = append(is, issue("reference_window", "ref", "reference window must be 2..30 s"))
	}
	return is
}

func structuralRange(r Request, srcDS int) []engine.Issue {
	if r.RepaintFromDS == nil || r.RepaintToDS == nil {
		return []engine.Issue{issue("range_required", "repaint_from_ds", "repaint needs repaint_from_ds and repaint_to_ds")}
	}
	from, to := *r.RepaintFromDS, *r.RepaintToDS
	switch {
	case from < 0 || from >= to:
		return []engine.Issue{issue("range_order", "repaint_from_ds", "need 0 <= from < to")}
	case srcDS > 0 && to > srcDS:
		return []engine.Issue{issue("range_outside_source", "repaint_to_ds", "range ends after the source")}
	case to-from < segMinDS:
		return []engine.Issue{issue("range_too_short", "repaint_to_ds", "range shorter than %d s", SegmentMinS)}
	case to-from > segMaxDS:
		return []engine.Issue{issue("range_too_long", "repaint_to_ds", "range longer than %d s", SegmentMaxS)}
	}
	return nil
}

func structuralTracks(r Request) []engine.Issue {
	var is []engine.Issue
	add := func(i engine.Issue) { is = append(is, i) }
	if r.Task != TaskComplete && r.Task != TaskExtract {
		if len(r.Tracks) > 0 {
			add(issue("tracks_forbidden", "tracks", "tracks are a complete/extract field"))
		}
		return is
	}
	maxN := 4
	if r.Task == TaskExtract {
		maxN = 1
	}
	if len(r.Tracks) == 0 {
		add(issue("tracks_required", "tracks", "%s needs tracks", r.Task))
	}
	if len(r.Tracks) > maxN {
		add(issue("tracks_too_many", "tracks", "at most %d tracks", maxN))
	}
	for i, t := range r.Tracks {
		if !slices.Contains(StemNames, t) {
			add(issue("unknown_track_name", "tracks", "unknown track name"))
		}
		if i > 0 && r.Tracks[i-1] == t {
			add(issue("tracks_duplicate", "tracks", "duplicate track name"))
		}
	}
	return is
}
