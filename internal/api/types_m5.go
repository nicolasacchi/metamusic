package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/attention"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/songpr"
)

// Wire types of the nine M5 operations (C260). JSON is snake_case. Frozen by M5-F2: a lane fills handler bodies, never
// these shapes. Lyric and comment text appears only where a contract says so (a patch's own ops and diff, a callback's
// comment body) and never in audit rows, job events or notifications.

// ---- Song PRs (C253) ----

// PatchOp is one operation of a patch: replace_line (section_idx, line_idx, text) or set_style (style, at most once).
// section_idx and line_idx index the lyrics sections and the non-empty lines of the base take.
type PatchOp struct {
	Op         string  `json:"op" enum:"replace_line,set_style"`
	SectionIdx *int    `json:"section_idx,omitempty" minimum:"0" maximum:"99" doc:"replace_line"`
	LineIdx    *int    `json:"line_idx,omitempty" minimum:"0" maximum:"999" doc:"replace_line"`
	Text       *string `json:"text,omitempty" minLength:"1" maxLength:"200" doc:"replace_line: one lyric line, no tags, no newline"`
	Style      *string `json:"style,omitempty" minLength:"1" maxLength:"1000" doc:"set_style: the complete new style string"`
}

// PatchBase names the take a patch applies to.
type PatchBase struct {
	TrackID    string `json:"track_id"`
	Title      string `json:"title"`
	RecipeHash string `json:"recipe_hash"`
}

// PatchAddress is a comment a patch addresses, as it was when the patch was proposed.
type PatchAddress struct {
	CommentID   string `json:"comment_id"`
	TrackID     string `json:"track_id"`
	Kind        string `json:"kind" enum:"note,fix"`
	State       string `json:"state" enum:"open,addressed,resolved,regressed"`
	BodyExcerpt string `json:"body_excerpt" doc:"At most 200 characters"`
}

// PatchRender is one render of a patch (one batch, up to four takes of one Song).
type PatchRender struct {
	BatchID      string  `json:"batch_id"`
	JobID        string  `json:"job_id"`
	State        string  `json:"state"`
	Seed         int64   `json:"seed"`
	ChildTrackID *string `json:"child_track_id" nullable:"true"`
}

// PatchCallbackTally counts the callbacks of a patch by answer.
type PatchCallbackTally struct {
	Total    int `json:"total"`
	Answered int `json:"answered"`
	Fixed    int `json:"fixed"`
	NotYet   int `json:"not_yet"`
	Worse    int `json:"worse"`
	Kept     int `json:"kept"`
	Lost     int `json:"lost"`
}

// PatchCompiled is the M3 patch the ops compile to: only the changed fields are set.
type PatchCompiled struct {
	Lyrics *string `json:"lyrics,omitempty"`
	Style  *string `json:"style,omitempty"`
}

// SongPRSummary is a Song PR without its ops, diff and compiled patch (list rows).
type SongPRSummary struct {
	ID        string             `json:"id"`
	Base      PatchBase          `json:"base"`
	Operator  string             `json:"operator" enum:"lyric_edit,style_edit,multi_edit"`
	Title     *string            `json:"title" nullable:"true"`
	Rationale string             `json:"rationale"`
	Addresses []PatchAddress     `json:"addresses" nullable:"false"`
	Renders   []PatchRender      `json:"renders" nullable:"false"`
	Callbacks PatchCallbackTally `json:"callbacks"`
	Author    PrincipalRef       `json:"author"`
	CreatedAt string             `json:"created_at"`
}

// SongPR is a stored, immutable Song PR (the plan's Patch; renamed because Patch is M3's generation patch): bounded lyric and style edits that name the comments they address.
type SongPR struct {
	SongPRSummary
	Ops      []PatchOp     `json:"ops" nullable:"false"`
	Diff     songpr.Diff   `json:"diff"`
	Bounds   songpr.Scope  `json:"bounds" doc:"The scoped sections and what the patch uses of each bound"`
	Compiled PatchCompiled `json:"compiled"`
}

type proposePatchIn struct {
	IdemHeader
	Body struct {
		BaseTrackID         string    `json:"base_track_id" minLength:"1"`
		AddressesCommentIDs []string  `json:"addresses_comment_ids" minItems:"1" maxItems:"10" uniqueItems:"true" doc:"Non-hidden fix or note comments, open or regressed, on the base take or an ancestor"`
		Rationale           string    `json:"rationale" minLength:"1" maxLength:"2000"`
		Title               *string   `json:"title,omitempty" minLength:"1" maxLength:"120"`
		ExtraSections       []int     `json:"extra_sections,omitempty" nullable:"false" maxItems:"2" doc:"Neighbour lyrics sections to put in scope"`
		Ops                 []PatchOp `json:"ops" minItems:"1" maxItems:"20"`
	}
}
type patchOut struct{ Body SongPR }

type getPatchIn struct {
	ID string `path:"id" doc:"Patch id"`
}

type listPatchesIn struct {
	TrackID     string `query:"track_id" doc:"Patches whose base is this take or whose renders produced it"`
	CommentID   string `query:"comment_id" doc:"Patches that address this comment"`
	RootTrackID string `query:"root_track_id" doc:"Patches on this lineage"`
	Limit       int    `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor      string `query:"cursor"`
}

// PatchPage is the answer of list_patches, newest first.
type PatchPage struct {
	Items      []SongPRSummary `json:"items" nullable:"false"`
	NextCursor *string         `json:"next_cursor" nullable:"true"`
	Total      int             `json:"total"`
}
type patchPageOut struct{ Body PatchPage }

// ---- Callback Listening (C255) ----

// CallbackChild is the take a callback asks about.
type CallbackChild struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Seed  int64  `json:"seed"`
}

// CallbackComment is the comment a callback is about, with its own spot on the comment's track.
type CallbackComment struct {
	ID       string                `json:"id"`
	TrackID  string                `json:"track_id"`
	Kind     string                `json:"kind" enum:"note,fix,keep,marker"`
	State    string                `json:"state" enum:"open,addressed,resolved,regressed"`
	Body     string                `json:"body"`
	AtSecond *float64              `json:"at_second" nullable:"true"`
	Section  *judgments.SectionRef `json:"section" doc:"Section label; null when no timed section covers at_second"`
	Voice    *judgments.VoiceRef   `json:"voice,omitempty" doc:"Present for a voice note"`
}

// CallbackSpot is the same musical spot on the child and on the take the comment was written on, computed on read.
type CallbackSpot struct {
	ChildAtS   float64  `json:"child_at_s"`
	SourceAtS  *float64 `json:"source_at_s" nullable:"true"`
	Basis      string   `json:"basis" enum:"section,proportional"`
	Confidence float64  `json:"confidence"`
	Label      *string  `json:"label" nullable:"true"`
}

// CallbackLinks are relative SPA links.
type CallbackLinks struct {
	Child  string  `json:"child" doc:"/tracks/<child>?t=<child_at_s>"`
	Source *string `json:"source" nullable:"true" doc:"/tracks/<source>?t=<source_at_s>; null without a source time"`
}

// CallbackVerdict is one stored answer.
type CallbackVerdict struct {
	ID                  string   `json:"id"`
	Source              string   `json:"source" enum:"human,critic"`
	PrincipalKind       string   `json:"principal_kind" enum:"human,device,agent"`
	Verdict             string   `json:"verdict" enum:"fixed,not_yet,worse,kept,lost"`
	Note                *string  `json:"note" nullable:"true"`
	HeardAtS            *float64 `json:"heard_at_s" nullable:"true"`
	MapBasis            *string  `json:"map_basis" nullable:"true"`
	StateBefore         *string  `json:"state_before" nullable:"true" doc:"Comment state before a human verdict moved it"`
	StateAfter          *string  `json:"state_after" nullable:"true"`
	RegressionCommentID *string  `json:"regression_comment_id" nullable:"true"`
	CreatedAt           string   `json:"created_at"`
}

// CallbackVerdictOrNull is a CallbackVerdict that may be null (the repo's OrNull convention: Huma refuses nullable on an object $ref).
type CallbackVerdictOrNull CallbackVerdict

type callbackVerdictShape CallbackVerdict

// Schema implements huma.SchemaProvider.
func (CallbackVerdictOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[callbackVerdictShape](r)
}

// Callback asks the human whether a Song PR child fixed a comment ("fix") or kept what a "keep" comment praised.
type Callback struct {
	ID             string                 `json:"id"`
	Kind           string                 `json:"kind" enum:"fix,keep"`
	Child          CallbackChild          `json:"child"`
	PatchID        string                 `json:"patch_id"`
	JobID          string                 `json:"job_id"`
	Comment        CallbackComment        `json:"comment"`
	Relation       string                 `json:"relation" enum:"self,ancestor" doc:"Whether the comment sits on the patch's base take or on an ancestor"`
	Spot           CallbackSpot           `json:"spot"`
	Links          CallbackLinks          `json:"links"`
	State          string                 `json:"state" enum:"pending,answered" doc:"pending until a human verdict exists"`
	MyVerdict      *CallbackVerdictOrNull `json:"my_verdict"`
	HumanVerdict   *CallbackVerdictOrNull `json:"human_verdict"`
	CriticVerdicts []CallbackVerdict      `json:"critic_verdicts" nullable:"false" doc:"Visible only after the caller's own source has answered"`
	CreatedAt      string                 `json:"created_at"`
}

type listCallbacksIn struct {
	State        []string `query:"state,explode" nullable:"false" enum:"pending,answered"`
	Kind         []string `query:"kind,explode" nullable:"false" enum:"fix,keep"`
	ChildTrackID string   `query:"child_track_id"`
	TrackID      string   `query:"track_id" doc:"Callbacks whose comment sits on this take"`
	CommentID    string   `query:"comment_id"`
	Limit        int      `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor       string   `query:"cursor"`
}

// CallbackPage is the answer of list_callbacks: pending first (oldest first), then answered (newest first).
type CallbackPage struct {
	Items      []Callback `json:"items" nullable:"false"`
	NextCursor *string    `json:"next_cursor" nullable:"true"`
	Total      int        `json:"total"`
}
type callbackPageOut struct{ Body CallbackPage }

type getCallbackIn struct {
	ID string `path:"id" doc:"Callback id"`
}
type callbackOut struct{ Body Callback }

// CallbackSubmitResult is the answer of submit_callback_verdict: 201 for a new verdict, 200 with replayed true for an identical
// repeat by the same source.
type CallbackSubmitResult struct {
	Replayed            bool            `json:"replayed"`
	Verdict             CallbackVerdict `json:"verdict"`
	CommentState        *string         `json:"comment_state" nullable:"true" doc:"The comment's state after a human fix verdict"`
	RegressionCommentID *string         `json:"regression_comment_id" nullable:"true"`
}

type submitCallbackVerdictIn struct {
	ID   string `path:"id" doc:"Callback id"`
	Body struct {
		Verdict  string   `json:"verdict" enum:"fixed,not_yet,worse,kept,lost" doc:"fixed, not_yet or worse for a fix callback; kept or lost for a keep callback"`
		Note     *string  `json:"note,omitempty" minLength:"1" maxLength:"500"`
		HeardAtS *float64 `json:"heard_at_s,omitempty" minimum:"0" doc:"Where in the child the human listened"`
	}
}
type callbackSubmitOut struct {
	Status int // 201 for a new verdict, 200 for a replay
	Body   CallbackSubmitResult
}

// ---- Attention (C256) ----

type getListenHeatIn struct {
	ID string `path:"id" doc:"Track id"`
}
type listenHeatOut struct{ Body attention.Heat }

// ---- Pair suggestions (C258) ----

// PairSuggestion is one suggested duel pair with the quantities behind its score.
type PairSuggestion struct {
	ATrackID     string  `json:"a_track_id"`
	BTrackID     string  `json:"b_track_id"`
	ATitle       string  `json:"a_title"`
	BTitle       string  `json:"b_title"`
	Score        float64 `json:"score" doc:"p(1-p)(sd_a^2 + sd_b^2): the expected information of one more verdict"`
	PAWinsB      float64 `json:"p_a_beats_b"`
	SDA          float64 `json:"sd_a"`
	SDB          float64 `json:"sd_b"`
	HumanVerdict int     `json:"human_verdicts"`
}

// PairSuggestionModel reports the ladder fit the suggestions rest on.
type PairSuggestionModel struct {
	Nu              float64 `json:"nu"`
	VerdictsCounted int     `json:"verdicts_counted"`
}

// PairSuggestions is the answer of suggest_duel_pairs.
type PairSuggestions struct {
	Scope       string              `json:"scope" enum:"lineage,global"`
	RootTrackID *string             `json:"root_track_id" nullable:"true"`
	Section     *string             `json:"section" nullable:"true" doc:"The section label both tracks are timed on, when one was asked for"`
	Model       PairSuggestionModel `json:"model"`
	Pairs       []PairSuggestion    `json:"pairs" nullable:"false"`
}

type suggestDuelPairsIn struct {
	Scope          string `query:"scope" enum:"lineage,global" default:"lineage" doc:"lineage needs root_track_id (the 40 most recent tracks); global takes the 60 most recent rated or dueled tracks"`
	RootTrackID    string `query:"root_track_id"`
	SectionLabel   string `query:"section_label" doc:"Only pairs whose tracks are both timed on this section"`
	SectionKind    string `query:"section_kind" doc:"With section_ordinal, instead of section_label"`
	SectionOrdinal int    `query:"section_ordinal" minimum:"1" maximum:"99"`
	K              int    `query:"k" minimum:"1" maximum:"15" default:"6"`
}
type pairSuggestionsOut struct{ Body PairSuggestions }

// ---- Voice notes (C259) ----

// bodyVoice is the request body cap of create_voice_note: 4 MiB of audio as base64 plus the JSON envelope.
const bodyVoice = 6 << 20

type createVoiceNoteIn struct {
	IdemHeader
	ID   string `path:"id" doc:"Track id"`
	Body struct {
		AudioBase64 string   `json:"audio_base64" minLength:"1" doc:"At most 4 MiB decoded and 120 s; webm or ogg with opus, or MP4 with aac or opus"`
		AtSecond    *float64 `json:"at_second,omitempty" nullable:"true" minimum:"0" doc:"Position in the track, rounded to 0.1 s"`
		Kind        string   `json:"kind,omitempty" enum:"note,fix,keep" doc:"Default note"`
		Body        *string  `json:"body,omitempty" minLength:"1" maxLength:"500" doc:"Default \"[voice note m:ss]\""`
	}
}
