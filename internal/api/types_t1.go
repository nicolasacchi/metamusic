package api

import (
	"encoding/json"
	"errors"

	"github.com/danielgtaylor/huma/v2"
)

// Wire types of the seven transform operations (C212, C219-C223). JSON is snake_case. Frozen by T1-F1 in one piece, including
// the hi-fi/3D kinds (master, widen, binaural) and the capability fields listening and dataset, so openapi.json and
// schema.d.ts are regenerated once: a lane fills handler bodies, never these shapes. Every optional field is a pointer
// that also accepts null ("null = the default"); the resolved values come back in TransformEstimate.resolved.

// bodyTransform is the request body cap of create and estimate: a score of 256 KiB plus its JSON frame.
const bodyTransform = 300 << 10

// ---- request ----

// TransformAt is a position in a morph input: a bar, or a section like chorus#1 (only sections that carry times resolve).
type TransformAt struct {
	Bar     *int    `json:"bar,omitempty" nullable:"true" minimum:"0"`
	Section *string `json:"section,omitempty" nullable:"true" pattern:"^[a-z_]+#[0-9]+$" doc:"kind#ordinal, e.g. chorus#1"`
}

// DownbeatSpec is a downbeat position in seconds, or the string "auto" (opt in to the heuristic).
type DownbeatSpec struct {
	Seconds *float64
	Auto    bool
}

// Schema implements huma.SchemaProvider: a number of seconds, "auto", or null. The null is its own branch: Nullable beside OneOf
// makes the generated TypeScript an intersection (Record<string, never> | null) & (number | "auto") that no value satisfies.
func (DownbeatSpec) Schema(huma.Registry) *huma.Schema {
	zero := 0.0
	return &huma.Schema{OneOf: []*huma.Schema{
		{Type: huma.TypeNumber, Minimum: &zero},
		{Type: huma.TypeString, Enum: []any{"auto"}},
		{Type: "null", Enum: []any{nil}}, // huma has no null type: without the enum this branch accepts every value and oneOf fails
	}}
}

// UnmarshalJSON accepts a number or "auto".
func (d *DownbeatSpec) UnmarshalJSON(b []byte) error {
	*d = DownbeatSpec{}
	if string(b) == "null" {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s != "auto" {
			return errors.New(`downbeat must be a number of seconds or "auto"`)
		}
		d.Auto = true
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	d.Seconds = &f
	return nil
}

// MarshalJSON writes the number, "auto" or null.
func (d DownbeatSpec) MarshalJSON() ([]byte, error) {
	switch {
	case d.Auto:
		return []byte(`"auto"`), nil
	case d.Seconds != nil:
		return json.Marshal(*d.Seconds)
	}
	return []byte("null"), nil
}

// IsZero lets omitzero drop an unset value.
func (d DownbeatSpec) IsZero() bool { return !d.Auto && d.Seconds == nil }

// MorphParams (C202.3): a beat- and key-aligned transition of two Takes.
type MorphParams struct {
	TrackAID    string       `json:"track_a_id"`
	TrackBID    string       `json:"track_b_id"`
	Bars        *int         `json:"bars,omitempty" nullable:"true" minimum:"1" maximum:"32" doc:"Transition length in bars of the output grid (default 8)"`
	AAt         *TransformAt `json:"a_at,omitempty"`
	BAt         *TransformAt `json:"b_at,omitempty"`
	BPMA        *float64     `json:"bpm_a,omitempty" nullable:"true" minimum:"40" maximum:"240"`
	BPMB        *float64     `json:"bpm_b,omitempty" nullable:"true" minimum:"40" maximum:"240"`
	BeatsPerBar *int         `json:"beats_per_bar,omitempty" nullable:"true" minimum:"2" maximum:"7"`
	DownbeatAS  DownbeatSpec `json:"downbeat_a_s,omitzero"`
	DownbeatBS  DownbeatSpec `json:"downbeat_b_s,omitzero"`
	KeyPolicy   *string      `json:"key_policy,omitempty" nullable:"true" enum:"to_a,none"`
	SemitonesB  *float64     `json:"semitones_b,omitempty" nullable:"true" minimum:"-12" maximum:"12"`
}

// ShepardParams is an endlessly rising (or falling) construction from a loop.
type ShepardParams struct {
	SourceID         string   `json:"source_id"`
	Direction        *string  `json:"direction,omitempty" nullable:"true" enum:"up,down"`
	LoopBars         *int     `json:"loop_bars,omitempty" nullable:"true" minimum:"1" maximum:"8"`
	LoopS            *float64 `json:"loop_s,omitempty" nullable:"true" minimum:"1" maximum:"20"`
	StartS           *float64 `json:"start_s,omitempty" nullable:"true" minimum:"0"`
	Layers           *int     `json:"layers,omitempty" nullable:"true" minimum:"3" maximum:"7"`
	PeriodS          *float64 `json:"period_s,omitempty" nullable:"true" minimum:"8" maximum:"120"`
	DurationS        *float64 `json:"duration_s,omitempty" nullable:"true" minimum:"8" maximum:"3600"`
	BellSigmaOctaves *float64 `json:"bell_sigma_octaves,omitempty" nullable:"true" minimum:"0.4" maximum:"2"`
}

// GlacierParams is a 10-50x time-stretch into a long ambient piece.
type GlacierParams struct {
	SourceID string   `json:"source_id"`
	Factor   *float64 `json:"factor,omitempty" nullable:"true" minimum:"10" maximum:"50"`
	StartS   *float64 `json:"start_s,omitempty" nullable:"true" minimum:"0"`
	LengthS  *float64 `json:"length_s,omitempty" nullable:"true" exclusiveMinimum:"0"`
	WindowS  *float64 `json:"window_s,omitempty" nullable:"true" minimum:"0.1" maximum:"2"`
	FadeInS  *float64 `json:"fade_in_s,omitempty" nullable:"true" minimum:"0" maximum:"30"`
	FadeOutS *float64 `json:"fade_out_s,omitempty" nullable:"true" minimum:"0" maximum:"30"`
}

// PolytempoParams is two Takes at different tempos converging on a shared downbeat.
type PolytempoParams struct {
	TrackAID    string       `json:"track_a_id"`
	TrackBID    string       `json:"track_b_id"`
	BPMA        *float64     `json:"bpm_a,omitempty" nullable:"true" minimum:"40" maximum:"240"`
	BPMB        *float64     `json:"bpm_b,omitempty" nullable:"true" minimum:"40" maximum:"240"`
	BeatsPerBar *int         `json:"beats_per_bar,omitempty" nullable:"true" minimum:"2" maximum:"7"`
	DownbeatAS  DownbeatSpec `json:"downbeat_a_s,omitzero"`
	DownbeatBS  DownbeatSpec `json:"downbeat_b_s,omitzero"`
	ConvergeBar *int         `json:"converge_bar,omitempty" nullable:"true" minimum:"4"`
	UnisonBars  *int         `json:"unison_bars,omitempty" nullable:"true" minimum:"0" maximum:"64"`
	Pan         *string      `json:"pan,omitempty" nullable:"true" enum:"split,centre"`
}

// EnsembleParams is an octave-stacked, formant-shifted vocal in a generated space. dry_db null means minus infinity.
type EnsembleParams struct {
	SourceID string         `json:"source_id"`
	Space    *string        `json:"space,omitempty" nullable:"true" enum:"cathedral,cistern,hangar,canyon"`
	WetDB    *float64       `json:"wet_db,omitempty" nullable:"true" minimum:"-40" maximum:"0"`
	DryDB    NullableNumber `json:"dry_db,omitzero" doc:"A number sets it, null means minus infinity, absent keeps the default"`
	TailS    *float64       `json:"tail_s,omitempty" nullable:"true" minimum:"0" maximum:"40"`
}

// ScoreSource is exactly one of abc (text) or from_track_id (that Take's score.abc).
type ScoreSource struct {
	ABC         *string `json:"abc,omitempty" nullable:"true" maxLength:"262144"`
	FromTrackID *string `json:"from_track_id,omitempty" nullable:"true"`
}

// ScoreRenderParams (T1b): ABC to score-faithful audio with a soundfont chosen by name.
type ScoreRenderParams struct {
	Score     ScoreSource `json:"score"`
	Dialect   *string     `json:"dialect,omitempty" nullable:"true" enum:"standard,yue2"`
	Soundfont *string     `json:"soundfont,omitempty" nullable:"true" pattern:"^[a-z][a-z0-9_-]{0,31}$" doc:"A configured name; never a path"`
	BPM       *float64    `json:"bpm,omitempty" nullable:"true" minimum:"20" maximum:"300"`
	TailS     *float64    `json:"tail_s,omitempty" nullable:"true" minimum:"0" maximum:"10"`
	Gain      *float64    `json:"gain,omitempty" nullable:"true" minimum:"0.1" maximum:"1.5"`
}

// MasterParams (C219): gentle tonal polish, levelling and a true-peak finish of the full mix.
type MasterParams struct {
	SourceID    string   `json:"source_id"`
	Preset      *string  `json:"preset,omitempty" nullable:"true" enum:"neutral,warm,bright,punch"`
	TiltDB      *float64 `json:"tilt_db,omitempty" nullable:"true" minimum:"-3" maximum:"3" doc:"Step 0.1; overrides the preset"`
	CompAmount  *float64 `json:"comp_amount,omitempty" nullable:"true" minimum:"0" maximum:"1" doc:"Step 0.05; 0 removes the compressor"`
	LowCutHz    *int     `json:"low_cut_hz,omitempty" nullable:"true" minimum:"0" maximum:"40" doc:"0 (off) or 20..40"`
	TargetLUFS  *float64 `json:"target_lufs,omitempty" nullable:"true" minimum:"-30" maximum:"-12"`
	CeilingDBTP *float64 `json:"ceiling_dbtp,omitempty" nullable:"true" minimum:"-3" maximum:"-1" doc:"Step 0.1; never above -1.0"`
}

// WidenParams (C220): stereo widening whose mono fold-down is the original's.
type WidenParams struct {
	SourceID    string   `json:"source_id"`
	Width       *float64 `json:"width,omitempty" nullable:"true" minimum:"1" maximum:"2" doc:"Step 0.05; gain on the side signal"`
	Decorrelate *float64 `json:"decorrelate,omitempty" nullable:"true" minimum:"0" maximum:"0.5" doc:"Step 0.05"`
	BassMonoHz  *int     `json:"bass_mono_hz,omitempty" nullable:"true" minimum:"0" maximum:"250" doc:"0 (off) or 60..250"`
}

// BinauralParams (C221): the stereo pair placed in front of the head, for headphones.
type BinauralParams struct {
	SourceID     string   `json:"source_id"`
	Model        *string  `json:"model,omitempty" nullable:"true" enum:"hrtf,parametric"`
	Preset       *string  `json:"preset,omitempty" nullable:"true" enum:"studio,wide,near"`
	LeftAzDeg    *int     `json:"left_az_deg,omitempty" nullable:"true" minimum:"-90" maximum:"-10"`
	RightAzDeg   *int     `json:"right_az_deg,omitempty" nullable:"true" minimum:"10" maximum:"90"`
	ElevationDeg *int     `json:"elevation_deg,omitempty" nullable:"true" minimum:"0" maximum:"30"`
	Room         *string  `json:"room,omitempty" nullable:"true" enum:"none,studio,hall"`
	RoomMixDB    *int     `json:"room_mix_db,omitempty" nullable:"true" minimum:"-40" maximum:"-12"`
	TailS        *float64 `json:"tail_s,omitempty" nullable:"true" minimum:"0" maximum:"10"`
}

// TransformRequest is the body of estimate_transform and create_transform: exactly the member named by kind is present,
// else 422 INVALID_TRANSFORM {rule: kind_params_mismatch}.
type TransformRequest struct {
	Kind       string   `json:"kind" enum:"morph,shepard,glacier,polytempo,ensemble,score_render,master,widen,binaural"`
	Title      *string  `json:"title,omitempty" nullable:"true" minLength:"1" maxLength:"200"`
	Rationale  *string  `json:"rationale,omitempty" nullable:"true" minLength:"1" maxLength:"1000" doc:"Stored on the Operator Ledger row"`
	Seed       *int64   `json:"seed,omitempty" nullable:"true" minimum:"0" maximum:"9007199254740991"`
	TargetLUFS *float64 `json:"target_lufs,omitempty" nullable:"true" minimum:"-30" maximum:"-12"`

	Morph       *MorphParams       `json:"morph,omitempty"`
	Shepard     *ShepardParams     `json:"shepard,omitempty"`
	Glacier     *GlacierParams     `json:"glacier,omitempty"`
	Polytempo   *PolytempoParams   `json:"polytempo,omitempty"`
	Ensemble    *EnsembleParams    `json:"ensemble,omitempty"`
	ScoreRender *ScoreRenderParams `json:"score_render,omitempty"`
	Master      *MasterParams      `json:"master,omitempty"`
	Widen       *WidenParams       `json:"widen,omitempty"`
	Binaural    *BinauralParams    `json:"binaural,omitempty"`
}

type estimateTransformIn struct{ Body TransformRequest }

type createTransformIn struct {
	IdemHeader
	Body TransformRequest
}

// ---- estimate ----

// TransformCost is always zero: a transform spends CPU, never money.
type TransformCost struct {
	MicroUSD int64  `json:"micro_usd"`
	USD      string `json:"usd" doc:"Always 0.000000"`
}

// TransformCPU is the deterministic CPU model (C209).
type TransformCPU struct {
	ExpectedMS int64  `json:"expected_ms"`
	MaxMS      int64  `json:"max_ms"`
	BudgetMS   int64  `json:"budget_ms"`
	Model      string `json:"model" doc:"cpu/1"`
}

// TransformOutputEstimate is the expected output.
type TransformOutputEstimate struct {
	Seconds    float64 `json:"seconds"`
	SampleRate int     `json:"sample_rate"`
	Bits       int     `json:"bits"`
	Channels   int     `json:"channels"`
	BytesEst   int64   `json:"bytes_est"`
}

// TransformBPM is the resolved tempo of an input and where it came from.
type TransformBPM struct {
	Value      float64  `json:"value"`
	Source     string   `json:"source" doc:"explicit, plan, declared or measured"`
	OctaveFold int      `json:"octave_fold"`
	Confidence *float64 `json:"confidence,omitempty" nullable:"true"`
}

// TransformKey is the resolved key of an input.
type TransformKey struct {
	Camelot *string `json:"camelot,omitempty" nullable:"true"`
	PC      int     `json:"pc" doc:"Pitch class 0..11, C = 0"`
	Minor   bool    `json:"minor"`
	Source  string  `json:"source"`
}

// TransformEstimateInput is one input of an estimate with its alignment resolution.
type TransformEstimateInput struct {
	Role        string        `json:"role"`
	TrackID     string        `json:"track_id"`
	Title       string        `json:"title"`
	DurationS   float64       `json:"duration_s"`
	BPM         *TransformBPM `json:"bpm"`
	Key         *TransformKey `json:"key"`
	BeatsPerBar int           `json:"beats_per_bar"`
}

// TransformWarning is one coded note; field names the parameter it concerns (may be empty).
type TransformWarning struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// TransformQuota is the daily CPU pool of the caller's principal kind (a shared pool, D-T1-10).
type TransformQuota struct {
	PrincipalKind string `json:"principal_kind" doc:"human or agent: the pool is shared by every principal of that kind"`
	CapMS         int64  `json:"cap_ms"`
	UsedMS        int64  `json:"used_ms"`
	ReservedMS    int64  `json:"reserved_ms"`
	ResetsAt      string `json:"resets_at" doc:"RFC 3339"`
}

// TransformQueueEstimate is the queue as an estimate sees it.
type TransformQueueEstimate struct {
	Running           int `json:"running"`
	Queued            int `json:"queued"`
	PositionIfCreated int `json:"position_if_created"`
}

// TransformExisting names a live or done transform of the same spec (create would reuse it).
type TransformExisting struct {
	TransformID string  `json:"transform_id"`
	TakeID      *string `json:"take_id,omitempty" nullable:"true"`
	State       string  `json:"state"`
}

// TransformTools reports the tools a render needs.
type TransformTools struct {
	FFmpeg     bool    `json:"ffmpeg"`
	FLAC       bool    `json:"flac"`
	Fluidsynth bool    `json:"fluidsynth"`
	Soundfont  *string `json:"soundfont" nullable:"true"`
}

// TransformEstimate is the answer of estimate_transform: free, pure, deterministic for a database state, and it writes
// nothing (D-T1-6).
type TransformEstimate struct {
	Kind        string                   `json:"kind"`
	SpecHash    string                   `json:"spec_hash"`
	Title       string                   `json:"title"`
	Cost        TransformCost            `json:"cost"`
	CPU         TransformCPU             `json:"cpu"`
	WallExpectS float64                  `json:"wall_expected_s"`
	Output      TransformOutputEstimate  `json:"output"`
	MemoryMB    int                      `json:"memory_mb"`
	Inputs      []TransformEstimateInput `json:"inputs" nullable:"false"`
	Resolved    RawRequest               `json:"resolved" doc:"The parameters with every default written out"`
	Warnings    []TransformWarning       `json:"warnings" nullable:"false"`
	Quota       TransformQuota           `json:"quota"`
	Queue       TransformQueueEstimate   `json:"queue"`
	Existing    *TransformExisting       `json:"existing"`
	Tools       TransformTools           `json:"tools"`
}

type transformEstimateOut struct{ Body TransformEstimate }

// ---- view ----

// TransformInputRef is an input of a transform as the view lists it.
type TransformInputRef struct {
	Role    string `json:"role"`
	TrackID string `json:"track_id"`
	Title   string `json:"title"`
}

// TransformCPUUse is the CPU of a transform: expected, budget and (when finished) used.
type TransformCPUUse struct {
	ExpectedMS int64  `json:"expected_ms"`
	MaxMS      int64  `json:"max_ms"`
	UsedMS     *int64 `json:"used_ms" nullable:"true"`
}

// TransformError is the row error of a failed or cancelled transform (no paths, no argv).
type TransformError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// TransformView is a transform as create, get, list and wait return it.
type TransformView struct {
	ID                string              `json:"id"`
	TakeID            string              `json:"take_id" doc:"Equals id: the transform is its Take"`
	Kind              string              `json:"kind"`
	State             string              `json:"state" enum:"queued,running,done,failed,cancelled"`
	Phase             *string             `json:"phase" nullable:"true"`
	ProgressPermille  int                 `json:"progress_permille"`
	Title             string              `json:"title"`
	SpecHash          string              `json:"spec_hash"`
	Reused            bool                `json:"reused"`
	Restored          bool                `json:"restored" doc:"A discarded Take of this spec was restored to draft instead of rendering again"`
	Principal         PrincipalRef        `json:"principal"`
	Rationale         *string             `json:"rationale" nullable:"true"`
	Inputs            []TransformInputRef `json:"inputs" nullable:"false"`
	Cost              TransformCost       `json:"cost"`
	CPU               TransformCPUUse     `json:"cpu"`
	OutputSecondsEst  float64             `json:"output_seconds_est"`
	Warnings          []TransformWarning  `json:"warnings" nullable:"false" doc:"Render-time warnings; empty until done"`
	Error             *TransformError     `json:"error"`
	CancelRequestedAt *string             `json:"cancel_requested_at" nullable:"true"`
	CreatedAt         string              `json:"created_at"`
	StartedAt         *string             `json:"started_at" nullable:"true"`
	FinishedAt        *string             `json:"finished_at" nullable:"true"`
}

// createTransformOut answers 202 for a queued or running transform (also a reused live one) and 200 for a reused done one.
type createTransformOut struct {
	Status int
	Body   TransformView
}

type getTransformIn struct {
	ID string `path:"id" doc:"Transform id (equals its Take id)"`
}
type transformOut struct{ Body TransformView }

type listTransformsIn struct {
	State   string `query:"state" enum:"queued,running,done,failed,cancelled"`
	Kind    string `query:"kind" enum:"morph,shepard,glacier,polytempo,ensemble,score_render,master,widen,binaural"`
	TrackID string `query:"track_id" doc:"Only transforms that have this Take as an input"`
	Limit   int    `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor  string `query:"cursor"`
}

// TransformPage is list_transforms: newest first.
type TransformPage struct {
	Items      []TransformView `json:"items" nullable:"false"`
	NextCursor *string         `json:"next_cursor" nullable:"true"`
}
type transformPageOut struct{ Body TransformPage }

type waitForTransformIn struct {
	ID         string `path:"id" doc:"Transform id"`
	SinceState string `query:"since_state" enum:"queued,running,done,failed,cancelled" doc:"Return as soon as the state differs; default the state at call time"`
	TimeoutS   int    `query:"timeout_s" minimum:"0" maximum:"55" default:"25"`
}

// TransformWait is wait_for_transform.
type TransformWait struct {
	Transform TransformView `json:"transform"`
	Changed   bool          `json:"changed"`
	TimedOut  bool          `json:"timed_out"`
}
type transformWaitOut struct{ Body TransformWait }

type cancelTransformIn struct {
	ID string `path:"id" doc:"Transform id"`
}

// ---- capabilities ----

// TransformParamCap is one parameter in the capabilities table; the same Go table validates the request.
type TransformParamCap struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Default  any      `json:"default,omitempty" nullable:"true"`
	Min      *float64 `json:"min,omitempty" nullable:"true"`
	Max      *float64 `json:"max,omitempty" nullable:"true"`
	Enum     []string `json:"enum,omitempty" nullable:"false"`
	Required bool     `json:"required"`
}

// TransformKindCaps is the capabilities entry of one kind. Listening is "headphones" for binaural and absent otherwise;
// Dataset is the credit string of the HRTF dataset (binaural only).
type TransformKindCaps struct {
	Kind       string              `json:"kind"`
	Summary    string              `json:"summary"`
	Inputs     int                 `json:"inputs"`
	Params     []TransformParamCap `json:"params" nullable:"false"`
	BlindSpots []string            `json:"blind_spots" nullable:"false"`
	Example    RawRequest          `json:"example" doc:"A request body that passes validation"`
	Listening  *string             `json:"listening,omitempty" nullable:"true" doc:"headphones, for binaural"`
	Dataset    *string             `json:"dataset,omitempty" nullable:"true" doc:"Credit string of the HRTF dataset, binaural only"`
}

// TransformLimits are the limits of C200 and D-T1-10.
type TransformLimits struct {
	MaxInputs  int `json:"max_inputs"`
	MaxInputS  int `json:"max_input_s"`
	MaxOutputS int `json:"max_output_s"`
	MaxMemMB   int `json:"max_mem_mb"`
	MaxCPUS    int `json:"max_cpu_s"`
	Concurrent int `json:"concurrent_renders"`
}

// TransformQueueCaps is the queue as capabilities show it.
type TransformQueueCaps struct {
	Running int `json:"running"`
	Queued  int `json:"queued"`
	Max     int `json:"max"`
}

// TransformFluidsynth reports the optional synth tool (T1b).
type TransformFluidsynth struct {
	Available bool    `json:"available"`
	Version   *string `json:"version" nullable:"true"`
}

// TransformToolCaps reports the tools of the host.
type TransformToolCaps struct {
	FFmpeg     bool                `json:"ffmpeg"`
	FLAC       bool                `json:"flac"`
	Fluidsynth TransformFluidsynth `json:"fluidsynth"`
}

// TransformSoundfont is a configured soundfont as the API shows it: its name, a sha prefix and its size; never a path.
type TransformSoundfont struct {
	Name  string `json:"name"`
	SHA12 string `json:"sha256_12"`
	Bytes int64  `json:"bytes"`
}

// TransformCapabilities is get_transform_capabilities, generated from the same Go tables that validate.
type TransformCapabilities struct {
	Enabled    bool                 `json:"enabled"`
	Kinds      []TransformKindCaps  `json:"kinds" nullable:"false"`
	Limits     TransformLimits      `json:"limits"`
	Quota      TransformQuota       `json:"quota"`
	Queue      TransformQueueCaps   `json:"queue"`
	Tools      TransformToolCaps    `json:"tools"`
	Soundfonts []TransformSoundfont `json:"soundfonts" nullable:"false"`
	DSPVersion string               `json:"dsp_version"`
}
type transformCapabilitiesOut struct{ Body TransformCapabilities }

// Nullable object schemas: Huma refuses nullable on an object $ref (repo convention, see nullableSchema), so the types that
// appear as a pointer field implement huma.SchemaProvider and render inline with nullable set.

type transformAtShape TransformAt
type transformBPMShape TransformBPM
type transformKeyShape TransformKey
type transformExistingShape TransformExisting
type transformErrorShape TransformError

// Schema implements huma.SchemaProvider.
func (TransformAt) Schema(r huma.Registry) *huma.Schema { return nullableSchema[transformAtShape](r) }

// Schema implements huma.SchemaProvider.
func (TransformBPM) Schema(r huma.Registry) *huma.Schema { return nullableSchema[transformBPMShape](r) }

// Schema implements huma.SchemaProvider.
func (TransformKey) Schema(r huma.Registry) *huma.Schema { return nullableSchema[transformKeyShape](r) }

// Schema implements huma.SchemaProvider.
func (TransformExisting) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[transformExistingShape](r)
}

// Schema implements huma.SchemaProvider.
func (TransformError) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[transformErrorShape](r)
}
