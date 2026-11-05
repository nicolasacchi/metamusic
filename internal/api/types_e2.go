package api

// Types of the E2 operations (contracts C148-C161, C166). E2-F2 froze these shapes: openapi.json and schema.d.ts change no
// more. Seconds are JSON numbers with 0.1 s resolution (a value that is not a multiple of 0.1 is rounded half away from zero
// and stored as integer deciseconds, D-E2-28). Money keeps the M3 rule: integer micro-USD plus the exact decimal string.

// ---- items (C149.2) ----

// EditIn holds the inputs of the edit modes of an item: repaint (from_s, to_s), extend (extend_s), complete (tracks, 1..4) and
// stems (tracks, 1..6 names, one job per name).
type EditIn struct {
	FromS   *float64 `json:"from_s,omitempty"`
	ToS     *float64 `json:"to_s,omitempty"`
	ExtendS *float64 `json:"extend_s,omitempty"`
	Tracks  []string `json:"tracks,omitempty" nullable:"false" maxItems:"6" enum:"backing_vocals,bass,brass,drums,fx,guitar,keyboard,percussion,strings,synth,vocals,woodwinds"`
}

// RefIn is the reference input of a compose_new or reference item: a window of a take whose timbre the render follows. The
// window is 2..30 s (default the last 20 s) and is cut and tiled to a 30 s clip by the server.
type RefIn struct {
	TrackID string   `json:"track_id" format:"uuid"`
	FromS   *float64 `json:"from_s,omitempty"`
	ToS     *float64 `json:"to_s,omitempty"`
}

// EstimateOptions are the optional settings of estimate_generation (C151.3, D-E2-15).
type EstimateOptions struct {
	Backend string `json:"backend,omitempty"`
	TrialID string `json:"trial_id,omitempty"`
}

// ---- per-take plan additions (C149.2) ----

// TakeEdit echoes what a take edits, in seconds.
type TakeEdit struct {
	FromS   *float64 `json:"from_s"`
	ToS     *float64 `json:"to_s"`
	ExtendS *float64 `json:"extend_s"`
	Tracks  []string `json:"tracks" nullable:"false"`
	Stem    *string  `json:"stem"`
}

// TakeReference echoes the pinned reference clip of a take.
type TakeReference struct {
	TrackID    string  `json:"track_id"`
	FromS      float64 `json:"from_s"`
	ToS        float64 `json:"to_s"`
	ClipSHA256 string  `json:"clip_sha256"`
}

// ---- list_engines (C160) ----

// EngineCaps is engine.Caps v2 as the API shows it: the effective capabilities (the verified-features file intersected with
// what the pinned engine supports, D-E2-7). Zero means "no" or "not applicable".
type EngineCaps struct {
	Text2Song       bool     `json:"text2song"`
	PlanScore       bool     `json:"plan_score"`
	RenderFromScore bool     `json:"render_from_score"`
	RepaintInterval bool     `json:"repaint_interval" doc:"Repaint a time range of a take"`
	Extend          bool     `json:"extend"`
	Complete        bool     `json:"complete"`
	Stems           bool     `json:"stems"`
	ReferenceAudio  bool     `json:"reference_audio"`
	LRC             bool     `json:"lrc"`
	Tempo           bool     `json:"tempo" doc:"Accepts bpm and time signature"`
	Key             bool     `json:"key" doc:"Accepts a key and scale"`
	LoRA            bool     `json:"lora"`
	MinSeconds      int      `json:"min_seconds"`
	MaxSeconds      int      `json:"max_seconds" doc:"The effective cap: the minimum of the verified maximum over the endpoint's pools"`
	SampleRate      int      `json:"sample_rate"`
	RepaintMinS     int      `json:"repaint_min_s"`
	RepaintMaxS     int      `json:"repaint_max_s"`
	ExtendMinS      int      `json:"extend_min_s"`
	ExtendMaxS      int      `json:"extend_max_s"`
	ReferenceMaxS   int      `json:"reference_max_s" doc:"Longest reference window, seconds"`
	StemNames       []string `json:"stem_names" nullable:"false"`
	Tasks           []string `json:"tasks" nullable:"false" doc:"The generation modes the engine offers now, sorted"`
}

// EnginePool is the verified duration cap of one GPU pool.
type EnginePool struct {
	Pool     string `json:"pool"`
	MaxS     int    `json:"max_s"`
	Verified bool   `json:"verified" doc:"A passing canary names this maximum; false is the documented default"`
}

// EngineFeatures is the verified-features file (features.verified.json) as the API shows it.
type EngineFeatures struct {
	VerifiedAt            string       `json:"verified_at" doc:"1970-01-01 until a canary ran"`
	Tasks                 []string     `json:"tasks" nullable:"false" doc:"Engine tasks a canary proved"`
	Unverified            []string     `json:"unverified" nullable:"false" doc:"Capabilities the engine supports but no canary proved yet (shown as not yet verified)"`
	ReferenceReproducible bool         `json:"reference_reproducible"`
	SeedReproducible      bool         `json:"seed_reproducible"`
	ModelsMode            *string      `json:"models_mode" enum:"bundle_only,bundle_cached_base_baked,two_cached"`
	Pools                 []EnginePool `json:"pools" nullable:"false"`
}

// EngineBackend is the backend that renders an engine, without endpoint ids or keys.
type EngineBackend struct {
	Name           string  `json:"name" doc:"fake, fake-acestep, runpod-serverless or runpod-serverless-acestep"`
	Enabled        bool    `json:"enabled" doc:"Generation can work at all (keys, quote, endpoint)"`
	DisabledReason *string `json:"disabled_reason" doc:"backend_none, keys_absent, endpoint_inactive, endpoint_config, quote_stale, catalog_unreadable or sweep_unreadable"`
	Armed          bool    `json:"armed"`
	Tripped        bool    `json:"tripped"`
	TripReason     *string `json:"trip_reason"`
	ColdStartS     int     `json:"cold_start_s"`
	MaxRuntimeS    int     `json:"max_runtime_s"`
}

// EngineAdoption is the adoption record of an engine (D-E2-15): the latest row of the append-only log.
type EngineAdoption struct {
	State     string  `json:"state" enum:"adopted,not_adopted,none"`
	Via       *string `json:"via" enum:"seed,cli,trial"`
	ChangedBy *string `json:"changed_by"`
	ChangedAt *string `json:"changed_at" doc:"RFC 3339 UTC"`
	Reason    *string `json:"reason"`
	TrialID   *string `json:"trial_id" doc:"The deciding trial (E2c); null for seed and cli rows"`
}

// EngineView is one engine of list_engines.
type EngineView struct {
	Name        string          `json:"name" doc:"yue2 or acestep15"`
	Label       string          `json:"label" example:"ACE-Step 1.5"`
	Enabled     bool            `json:"enabled" doc:"Registered in this process (MM_ACESTEP for acestep15)"`
	Adoption    EngineAdoption  `json:"adoption"`
	Backend     *EngineBackend  `json:"backend,omitempty" doc:"null when no backend is configured for the engine"`
	Caps        EngineCaps      `json:"caps"`
	TaskClasses []string        `json:"task_classes" nullable:"false" doc:"turbo and/or base: the model families the endpoint is provisioned for"`
	Features    *EngineFeatures `json:"features,omitempty" doc:"null for an engine without a verified-features file (yue2)"`
	Warnings    []string        `json:"warnings" nullable:"false" doc:"Operator-facing reasons the engine is limited"`
	// Selectable is what the engine picker needs: whether a generation of this engine can be estimated right now, and why not.
	Selectable        bool    `json:"selectable"`
	UnavailableReason *string `json:"unavailable_reason" enum:"not_enabled,not_adopted,backend_absent,backend_disabled,disarmed,tripped" doc:"Why selectable is false; the first failing check in this order"`
	AdoptHint         *string `json:"adopt_hint" doc:"How to adopt a not adopted engine (the CLI line, or the trial from E2c)"`
	OpenTrialID       *string `json:"open_trial_id" doc:"The open engine trial (E2c); always null before"`
}

// EngineList is the result of list_engines.
type EngineList struct {
	Engines       []EngineView `json:"engines" nullable:"false" doc:"yue2 first, then the other engines by name"`
	DefaultEngine string       `json:"default_engine" example:"yue2"`
}

type enginesOut struct{ Body EngineList }

// ---- provider status per backend (C151.3, C151.5) ----

// GlobalBound is the global worker bound across the endpoints (D-E2-30): G is the burn rate if every endpoint runs flat out,
// X the worst simultaneous exposure.
type GlobalBound struct {
	GMicroPerHour int64    `json:"g_micro_per_hour"`
	GUSDPerHour   string   `json:"g_usd_per_hour"`
	XMicro        int64    `json:"x_micro"`
	XUSD          string   `json:"x_usd"`
	Backends      []string `json:"backends" nullable:"false" doc:"The backends with a live workers.max above zero"`
}

// BackendStatus is one backend's provider status. The fields of ProviderStatusBody apply per backend; Engine says which engine it
// renders.
type BackendStatus struct {
	ProviderStatusBody
	Engine string `json:"engine"`
}

// ---- stems and LRC links (C157.1, C166) ----

// StemLink is a signed link to one stem artifact of a take (audio/flac); valid until the response's expires_at.
type StemLink struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// LRCLink is a signed link to the lyrics.lrc artifact of a take (text/plain, UTF-8).
type LRCLink struct {
	URL    string `json:"url"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// ---- error codes (C149.2, C151, C153, C158) ----

// E2 error codes. Each is a stable UPPER_SNAKE problem code; details are named in errors_e2.go.
const (
	CodeEngineCapability      = "ENGINE_CAPABILITY"       // 422 {engine, mode?, capability}
	CodeEngineNotAdopted      = "ENGINE_NOT_ADOPTED"      // 409 {engine, hint}
	CodeBackendUnavailable    = "BACKEND_UNAVAILABLE"     // 422 {engine, backend}
	CodeEngineParamsStale     = "ENGINE_PARAMS_STALE"     // 422 {engine_params_version, current}
	CodeConcurrencyExceedsCap = "CONCURRENCY_EXCEEDS_CAP" // 422 {backends, g_micro_per_hour, x_micro}
	CodeInvalidItem           = "INVALID_ITEM"            // 422 {item, field, rule}
	CodeEstimateStale         = "ESTIMATE_STALE"          // 409 {reason: reference_changed, ...}
)

// ItemRules are the INVALID_ITEM rule names of E2 items (C149.2); the engine's validation issue codes may add more.
var ItemRules = []string{"range_order", "range_outside_source", "range_too_short", "range_too_long", "extend_too_long", "total_over_600",
	"tracks_required", "tracks_duplicate", "unknown_track_name", "reference_window", "instrumental_lyrics", "exceeds_max_runtime",
	"trial_items"}

// Capabilities that can fail ENGINE_CAPABILITY (details.capability): "mode", "max_seconds", "src_format", "src_too_long",
// "stems", "reference", "lrc", "task:<name>" and the repaint/extend/complete flags.
