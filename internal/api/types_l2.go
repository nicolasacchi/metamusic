package api

import (
	"encoding/json"
	"fmt"

	"github.com/danielgtaylor/huma/v2"
)

// Types of the L2 (Library: Curation) operations (contract C129), frozen by L2-F2. Domain shapes live in records, recordview,
// crates, tape and curation; this file holds the request inputs, the envelopes and the wire shapes. No Muse text appears
// anywhere in it except RecordLiner (the app-audience liner notes: get_record only, C127.4). Principals are rendered by the
// operations through RefFor and KeyFor, never built by hand. The tool-facing inputs carry few `doc` tags on purpose: their
// schemas count against the tools/list ceiling (C129).

// ---- inputs shared by several operations ----

// OptSide is the nullable side label of a slot operation: the key absent, JSON null, or "A" to "D". Huma validates the
// enum before the value is decoded, so any other value is a 422 with field side.
type OptSide struct {
	Set   bool
	Null  bool
	Value string
}

// Schema implements huma.SchemaProvider: a nullable enum of the four sides.
func (OptSide) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Nullable: true, Enum: []any{"A", "B", "C", "D"}}
}

// UnmarshalJSON records presence, null and the letter.
func (o *OptSide) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null, o.Value = true, ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	switch s {
	case "A", "B", "C", "D":
		o.Null, o.Value = false, s
		return nil
	}
	return fmt.Errorf("side must be one of A, B, C, D or null")
}

// MarshalJSON writes null for an unset or null side and the letter otherwise.
func (o OptSide) MarshalJSON() ([]byte, error) {
	if !o.Set || o.Null {
		return []byte("null"), nil
	}
	return json.Marshal(o.Value)
}

// ---- Records: wire shapes ----

// RecordProposal is the pending proposal of a Record.
type RecordProposal struct {
	Note string       `json:"note"`
	By   PrincipalRef `json:"by"`
	At   string       `json:"at" doc:"RFC 3339 UTC"`
}

// RecordProposal is only ever null or an object, so its schema is the inline nullable one (Huma refuses nullable on a $ref object).
type recordProposalShape RecordProposal

// Schema implements huma.SchemaProvider.
func (RecordProposal) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[recordProposalShape](r)
}

// RecordPublication is the publication of a published Record.
type RecordPublication struct {
	By      PrincipalRef `json:"by"`
	At      string       `json:"at"`
	FirstAt string       `json:"first_at" doc:"The first time this Record was ever published"`
}

// RecordPublication is only ever null or an object, so its schema is the inline nullable one (Huma refuses nullable on a $ref object).
type recordPublicationShape RecordPublication

// Schema implements huma.SchemaProvider.
func (RecordPublication) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[recordPublicationShape](r)
}

// Record is one Record row (no slots, no liner).
type Record struct {
	ID           string             `json:"id"`
	Title        string             `json:"title"`
	Subtitle     string             `json:"subtitle"`
	LinerNote    string             `json:"liner_note" doc:"The operator's own note, prepended to the generated liner notes"`
	Status       string             `json:"status" enum:"draft,proposed,published"`
	Revision     int                `json:"revision"`
	CoverRerolls int                `json:"cover_rerolls"`
	Hidden       bool               `json:"hidden"`
	CreatedBy    PrincipalRef       `json:"created_by"`
	CreatedAt    string             `json:"created_at"`
	UpdatedAt    string             `json:"updated_at"`
	Proposal     *RecordProposal    `json:"proposal"`
	Published    *RecordPublication `json:"published"`
}

// RecordSummary is one list row.
type RecordSummary struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Subtitle  string     `json:"subtitle"`
	Status    string     `json:"status" enum:"draft,proposed,published"`
	Slots     int        `json:"slots"`
	Playable  int        `json:"playable"`
	DurationS float64    `json:"duration_s"`
	Revision  int        `json:"revision"`
	UpdatedAt string     `json:"updated_at"`
	Hidden    bool       `json:"hidden"`
	Warnings  int        `json:"warnings"`
	Cover     CoverLinks `json:"cover"`
}

// SlotSong is a slot's Song. No Muse text.
type SlotSong struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Angle       *string `json:"angle" nullable:"true"`
	State       string  `json:"state"`
	Retired     bool    `json:"retired"`
	RetiredInto *string `json:"retired_into" nullable:"true"`
}

// TakeBrief is a Take inside a Record view.
type TakeBrief struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Status      string  `json:"status"`
	DurationS   float64 `json:"duration_s"`
	Lossy       bool    `json:"lossy"`
	Stars       *int    `json:"stars" nullable:"true" doc:"Human stars"`
	RecipeKnown bool    `json:"recipe_known"`
}

// TakeBrief is only ever null or an object, so its schema is the inline nullable one (Huma refuses nullable on a $ref object).
type takeBriefShape TakeBrief

// Schema implements huma.SchemaProvider.
func (TakeBrief) Schema(r huma.Registry) *huma.Schema { return nullableSchema[takeBriefShape](r) }

// SlotPick is the Song's current Pick row.
type SlotPick struct {
	TakeID *string `json:"take_id" nullable:"true"`
	Source string  `json:"source"`
}

// SlotPick is only ever null or an object, so its schema is the inline nullable one (Huma refuses nullable on a $ref object).
type slotPickShape SlotPick

// Schema implements huma.SchemaProvider.
func (SlotPick) Schema(r huma.Registry) *huma.Schema { return nullableSchema[slotPickShape](r) }

// SlotView is one slot of a Record view.
type SlotView struct {
	ID         string     `json:"id"`
	Position   int        `json:"position"`
	Side       *string    `json:"side" nullable:"true" enum:"A,B,C,D"`
	Mode       string     `json:"mode" enum:"floating,pinned"`
	State      string     `json:"state" enum:"ok,empty,pin_ineligible"`
	Song       SlotSong   `json:"song"`
	Take       *TakeBrief `json:"take"`
	Pick       *SlotPick  `json:"pick"`
	PinDiffers bool       `json:"pin_differs"`
	GainDB     *float64   `json:"gain_db" nullable:"true" doc:"The plan gain with the automatic target; null when unmeasured"`
	Why        *string    `json:"why" nullable:"true" doc:"Why the Song's current Pick is what it is"`
	Warnings   []string   `json:"warnings" nullable:"false"`
}

// RecordTotals are the counts of a Record view.
type RecordTotals struct {
	Slots     int     `json:"slots"`
	Playable  int     `json:"playable"`
	DurationS float64 `json:"duration_s"`
	Lossy     int     `json:"lossy"`
	Warnings  int     `json:"warnings"`
	Undecided int     `json:"undecided"`
}

// RecordWarning is a warning of a Record; slot_id is null for a Record-level one.
type RecordWarning struct {
	SlotID *string `json:"slot_id" nullable:"true"`
	Code   string  `json:"code"`
}

// RecordLiner is the generated app-audience liner notes (Muse text: only get_record returns it).
type RecordLiner struct {
	Markdown string         `json:"markdown"`
	Summary  map[string]any `json:"summary"`
}

// EnergySlot is one slot's energy curve.
type EnergySlot struct {
	SlotID    string    `json:"slot_id"`
	DurationS float64   `json:"duration_s"`
	GainDB    *float64  `json:"gain_db" nullable:"true"`
	MeanLUFS  *float64  `json:"mean_lufs" nullable:"true"`
	BPM       *float64  `json:"bpm" nullable:"true"`
	LUFS      []float64 `json:"lufs" nullable:"false" doc:"Short-term loudness after the plan gain, at most 120 points; empty without an analysis"`
}

// Energy is the energy-curve preview (include=energy).
type Energy struct {
	TargetLUFS     float64      `json:"target_lufs"`
	TotalDurationS float64      `json:"total_duration_s"`
	Slots          []EnergySlot `json:"slots" nullable:"false"`
}

// TakeLink names a Take in a changelog row; the title is read at request time, never stored.
type TakeLink struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// TakeLink is only ever null or an object, so its schema is the inline nullable one (Huma refuses nullable on a $ref object).
type takeLinkShape TakeLink

// Schema implements huma.SchemaProvider.
func (TakeLink) Schema(r huma.Registry) *huma.Schema { return nullableSchema[takeLinkShape](r) }

// ChangeView is one changelog row.
type ChangeView struct {
	Seq      int            `json:"seq"`
	At       string         `json:"at"`
	Kind     string         `json:"kind"`
	Text     string         `json:"text"`
	SlotID   *string        `json:"slot_id" nullable:"true"`
	Position *int           `json:"position" nullable:"true"`
	From     *TakeLink      `json:"from"`
	To       *TakeLink      `json:"to"`
	Reason   *string        `json:"reason" nullable:"true"`
	Evidence map[string]any `json:"evidence" nullable:"true"`
	Actor    PrincipalRef   `json:"actor"`
}

// TapeBrief is a tape's line in a Record view.
type TapeBrief struct {
	ID            string  `json:"id"`
	State         string  `json:"state" enum:"queued,running,ready,failed,expired"`
	CreatedAt     string  `json:"created_at"`
	FinishedAt    *string `json:"finished_at" nullable:"true"`
	ContainsLossy bool    `json:"contains_lossy"`
	Bytes         *int64  `json:"bytes" nullable:"true"`
	ExpiresAt     *string `json:"expires_at" nullable:"true"`
}

// PreviewChapter is a chapter name as the tape would carry it.
type PreviewChapter struct {
	Position int    `json:"position"`
	Name     string `json:"name"`
}

// PreviewTag is a tag as the tape would carry it.
type PreviewTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// TapeWarning names a slot the tape snapshot flags: code is pin_ineligible or undecided_pick.
type TapeWarning struct {
	SlotID string `json:"slot_id"`
	Code   string `json:"code" enum:"pin_ineligible,undecided_pick"`
}

// RecordPreview is exactly what a tape export would carry (include=preview; the publish page shows it).
type RecordPreview struct {
	Chapters    []PreviewChapter `json:"chapters" nullable:"false"`
	Tags        []PreviewTag     `json:"tags" nullable:"false"`
	Description string           `json:"description"`
	Warnings    []TapeWarning    `json:"warnings" nullable:"false"`
}

// RecordView is a Record with its slots; the optional parts are present only when asked for. Only get_record returns liner.
type RecordView struct {
	Record   Record          `json:"record"`
	Slots    []SlotView      `json:"slots" nullable:"false"`
	Totals   RecordTotals    `json:"totals"`
	Warnings []RecordWarning `json:"warnings" nullable:"false"`
	Cover    CoverLinks      `json:"cover"`
	Liner    *RecordLiner    `json:"liner,omitempty" doc:"get_record only (Muse text)"`
	Energy   *Energy         `json:"energy,omitempty"`
	Changes  []ChangeView    `json:"changes,omitempty"`
	Tapes    []TapeBrief     `json:"tapes,omitempty"`
	Preview  *RecordPreview  `json:"preview,omitempty"`
	Pending  bool            `json:"pending" doc:"The changelog may lag by one sync pass; the resolved Takes are never stale"`
}

// ---- Records: inputs and envelopes ----

type listRecordsIn struct {
	Status        []string `query:"status,explode" nullable:"false" enum:"draft,proposed,published"`
	Q             string   `query:"q" maxLength:"200"`
	SongID        string   `query:"song_id"`
	IncludeHidden bool     `query:"include_hidden"`
	Limit         int      `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor        string   `query:"cursor"`
}
type recordsOut struct {
	Body struct {
		Items      []RecordSummary `json:"items" nullable:"false"`
		NextCursor *string         `json:"next_cursor" nullable:"true"`
		Total      int             `json:"total"`
	}
}

type getRecordIn struct {
	ID           string   `path:"id"`
	Include      []string `query:"include,explode" nullable:"false" enum:"liner,energy,tapes,changes,preview" doc:"Optional parts; default liner,tapes,changes"`
	ChangesLimit int      `query:"changes_limit" minimum:"0" maximum:"50" default:"10"`
}
type recordViewOut struct{ Body RecordView }

type createRecordIn struct {
	IdemHeader
	Body struct {
		Title     string   `json:"title" minLength:"1" maxLength:"120"`
		Subtitle  string   `json:"subtitle,omitempty" maxLength:"200"`
		LinerNote string   `json:"liner_note,omitempty" maxLength:"4000"`
		SongIDs   []string `json:"song_ids,omitempty" maxItems:"60" doc:"Songs, in order; each becomes a floating slot"`
	}
}

type updateRecordIn struct {
	ID   string `path:"id"`
	Body struct {
		Revision    int     `json:"revision" minimum:"1"`
		Title       *string `json:"title,omitempty" minLength:"1" maxLength:"120"`
		Subtitle    *string `json:"subtitle,omitempty" maxLength:"200"`
		LinerNote   *string `json:"liner_note,omitempty" maxLength:"4000"`
		CoverReroll *bool   `json:"cover_reroll,omitempty" doc:"Draw a new cover layout; the palette stays"`
		Hidden      *bool   `json:"hidden,omitempty"`
	}
}

// SlotOp is one operation of edit_record_slots (C121.3).
type SlotOp struct {
	Op       string   `json:"op" enum:"add,remove,move,reorder,pin,unpin,set_side,pin_all,unpin_all"`
	SongID   string   `json:"song_id,omitempty" doc:"add"`
	SlotID   string   `json:"slot_id,omitempty" doc:"remove, move, pin, unpin, set_side"`
	TakeID   string   `json:"take_id,omitempty" doc:"pin; add with a Take pins the new slot"`
	Position *int     `json:"position,omitempty" doc:"add (optional, default end) and move; 1-based"`
	Side     OptSide  `json:"side,omitempty" doc:"set_side (required: a letter or null) and add (optional)"`
	SlotIDs  []string `json:"slot_ids,omitempty" maxItems:"60" doc:"reorder: the complete new order"`
}
type editRecordSlotsIn struct {
	ID   string `path:"id"`
	Body struct {
		Revision int      `json:"revision" minimum:"1"`
		Ops      []SlotOp `json:"ops" minItems:"1" maxItems:"50"`
	}
}

type getRecordChangesIn struct {
	ID     string   `path:"id"`
	Order  string   `query:"order" enum:"desc,asc" default:"desc"`
	Cursor int      `query:"cursor" minimum:"0" doc:"The seq of the previous page's last item"`
	Kind   []string `query:"kind,explode" nullable:"false"`
	Limit  int      `query:"limit" minimum:"1" maximum:"200" default:"50"`
}
type recordChangesOut struct {
	Body struct {
		Items      []ChangeView `json:"items" nullable:"false"`
		NextCursor *int         `json:"next_cursor" nullable:"true"`
	}
}

type proposeRecordIn struct {
	ID   string `path:"id"`
	Body struct {
		Revision  int    `json:"revision" minimum:"1"`
		Rationale string `json:"rationale" minLength:"1" maxLength:"500"`
	}
}

type publishRecordIn struct {
	ID   string `path:"id"`
	Body struct {
		Revision  int  `json:"revision" minimum:"1"`
		Published bool `json:"published" doc:"true publishes; false sends a proposal back or unpublishes"`
	}
}

// ---- Tapes ----

// TapeProgress is the phase of a running tape.
type TapeProgress struct {
	Phase string `json:"phase" enum:"decode,encode,rewrite,verify"`
	Item  int    `json:"item"`
	Items int    `json:"items"`
}

// TapeProgress is only ever null or an object, so its schema is the inline nullable one (Huma refuses nullable on a $ref object).
type tapeProgressShape TapeProgress

// Schema implements huma.SchemaProvider.
func (TapeProgress) Schema(r huma.Registry) *huma.Schema { return nullableSchema[tapeProgressShape](r) }

// TapeSpread is the loudness spread of the unclamped items, in LU.
type TapeSpread struct {
	Before float64 `json:"before"`
	After  float64 `json:"after"`
}

// TapeSpread is only ever null or an object, so its schema is the inline nullable one (Huma refuses nullable on a $ref object).
type tapeSpreadShape TapeSpread

// Schema implements huma.SchemaProvider.
func (TapeSpread) Schema(r huma.Registry) *huma.Schema { return nullableSchema[tapeSpreadShape](r) }

// TapeItem is the planned gain of one item.
type TapeItem struct {
	SlotID        string   `json:"slot_id"`
	TakeID        string   `json:"take_id"`
	Position      int      `json:"position"`
	GainDB        float64  `json:"gain_db"`
	Status        string   `json:"status" enum:"matched,cut_clamped,boost_clamped,peak_limited,silent,unmeasured"`
	PredictedLUFS *float64 `json:"predicted_lufs" nullable:"true"`
	Lossy         bool     `json:"lossy"`
	Resampled     bool     `json:"resampled"`
	Upmixed       bool     `json:"upmixed"`
	Downmixed     bool     `json:"downmixed"`
}

// TapeChapter is a chapter of a ready tape.
type TapeChapter struct {
	Index   int    `json:"index"`
	SlotID  string `json:"slot_id"`
	StartMS int64  `json:"start_ms"`
	Samples int64  `json:"samples"`
}

// TapeDownload is the signed link of a ready tape.
type TapeDownload struct {
	URL       string  `json:"url"`
	Filename  string  `json:"filename"`
	Bytes     int64   `json:"bytes"`
	ExpiresAt string  `json:"expires_at"`
	CueURL    *string `json:"cue_url,omitempty" doc:"Only when a sidecar cue sheet exists"`
}

// TapeDownload is only ever null or an object, so its schema is the inline nullable one (Huma refuses nullable on a $ref object).
type tapeDownloadShape TapeDownload

// Schema implements huma.SchemaProvider.
func (TapeDownload) Schema(r huma.Registry) *huma.Schema { return nullableSchema[tapeDownloadShape](r) }

// TapeError is the failure of a failed tape.
type TapeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// TapeError is only ever null or an object, so its schema is the inline nullable one (Huma refuses nullable on a $ref object).
type tapeErrorShape TapeError

// Schema implements huma.SchemaProvider.
func (TapeError) Schema(r huma.Registry) *huma.Schema { return nullableSchema[tapeErrorShape](r) }

// Tape is one lossless export of a published Record.
type Tape struct {
	ID               string        `json:"id"`
	RecordID         string        `json:"record_id"`
	RecordRevision   int           `json:"record_revision"`
	State            string        `json:"state" enum:"queued,running,ready,failed,expired"`
	Reused           bool          `json:"reused" doc:"An identical export was already live and was returned instead of a new one"`
	Progress         *TapeProgress `json:"progress"`
	TargetLUFS       float64       `json:"target_lufs"`
	SpreadLU         *TapeSpread   `json:"spread_lu"`
	Items            []TapeItem    `json:"items" nullable:"false"`
	Chapters         []TapeChapter `json:"chapters" nullable:"true"`
	Warnings         []TapeWarning `json:"warnings" nullable:"false"`
	AcceptedWarnings bool          `json:"accepted_warnings"`
	ContainsLossy    bool          `json:"contains_lossy"`
	LossySources     int           `json:"lossy_sources"`
	TotalMS          *int64        `json:"total_ms" nullable:"true"`
	Bytes            *int64        `json:"bytes" nullable:"true"`
	SHA256           *string       `json:"sha256" nullable:"true"`
	Download         *TapeDownload `json:"download"`
	Error            *TapeError    `json:"error"`
	CreatedAt        string        `json:"created_at"`
	StartedAt        *string       `json:"started_at" nullable:"true"`
	FinishedAt       *string       `json:"finished_at" nullable:"true"`
}
type tapeOut struct{ Body Tape }

type exportRecordTapeIn struct {
	IdemHeader
	ID   string `path:"id"`
	Body struct {
		Revision       *int     `json:"revision,omitempty" minimum:"1" doc:"Refuse (409) unless the Record is still at this revision"`
		TargetLUFS     *float64 `json:"target_lufs,omitempty" minimum:"-30" maximum:"-9" doc:"Rounded to 0.1 LU; default: the automatic target"`
		IncludeLiner   *bool    `json:"include_liner,omitempty" doc:"Default true"`
		AcceptWarnings *bool    `json:"accept_warnings,omitempty" doc:"Humans only: accept pin_ineligible and undecided_pick slots"`
	}
}

type getTapeIn struct {
	ID string `path:"id"`
}

// ---- Crates ----

// Crate is one Crate row.
type Crate struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Kind        string         `json:"kind" enum:"manual,smart"`
	Subject     string         `json:"subject" enum:"song,take"`
	Filter      map[string]any `json:"filter" nullable:"true" doc:"A smart Crate's saved filter (mm.crate-filter/1); null for a manual one"`
	Revision    int            `json:"revision"`
	CreatedBy   PrincipalRef   `json:"created_by"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
	Hidden      bool           `json:"hidden"`
}

// CrateSummary is a list row: the Crate and its count (manual always, smart only with counts=true).
type CrateSummary struct {
	Crate
	Count *int `json:"count" nullable:"true"`
}

// CrateDetail is a Crate with its live count and any warning (a saved filter naming ids that no longer exist).
type CrateDetail struct {
	Crate    Crate    `json:"crate"`
	Count    int      `json:"count"`
	Warnings []string `json:"warnings" nullable:"false"`
}
type crateDetailOut struct{ Body CrateDetail }

// CrateMember is one member of a manual Crate: exactly one of song_id and track_id, matching the subject.
type CrateMember struct {
	SongID  string `json:"song_id,omitempty"`
	TrackID string `json:"track_id,omitempty"`
}

// ItemsResult is the outcome of edit_crate_items.
type ItemsResult struct {
	Added     int `json:"added"`
	Already   int `json:"already"`
	Removed   int `json:"removed"`
	NotMember int `json:"not_member"`
	Count     int `json:"count"`
}
type itemsResultOut struct{ Body ItemsResult }

type listCratesIn struct {
	Kind          string `query:"kind" enum:"manual,smart"`
	Subject       string `query:"subject" enum:"song,take"`
	Q             string `query:"q" maxLength:"200"`
	IncludeHidden bool   `query:"include_hidden"`
	Counts        bool   `query:"counts" doc:"Compute the live count of smart Crates"`
	Limit         int    `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor        string `query:"cursor"`
}
type cratesOut struct {
	Body struct {
		Items      []CrateSummary `json:"items" nullable:"false"`
		NextCursor *string        `json:"next_cursor" nullable:"true"`
		Total      int            `json:"total"`
	}
}

type getCrateIn struct {
	ID string `path:"id"`
}

type createCrateIn struct {
	IdemHeader
	Body struct {
		Name        string         `json:"name" minLength:"1" maxLength:"80"`
		Description string         `json:"description,omitempty" maxLength:"500"`
		Kind        string         `json:"kind" enum:"manual,smart"`
		Subject     string         `json:"subject,omitempty" enum:"song,take" doc:"Default song; fixed at creation"`
		Filter      map[string]any `json:"filter,omitempty" doc:"Smart only: the keys of list_songs (song) or search_tracks (take)"`
		Items       []CrateMember  `json:"items,omitempty" maxItems:"200"`
	}
}

type updateCrateIn struct {
	ID   string `path:"id"`
	Body struct {
		Revision    int            `json:"revision" minimum:"1"`
		Name        *string        `json:"name,omitempty" minLength:"1" maxLength:"80"`
		Description *string        `json:"description,omitempty" maxLength:"500"`
		Filter      map[string]any `json:"filter,omitempty"`
		Hidden      *bool          `json:"hidden,omitempty"`
	}
}

type editCrateItemsIn struct {
	ID   string `path:"id"`
	Body struct {
		Add    []CrateMember `json:"add,omitempty" maxItems:"200"`
		Remove []CrateMember `json:"remove,omitempty" maxItems:"200"`
	}
}

// ---- Curation settings ----

// CurationSettings are the two privacy opt-ins (D-L2-16).
type CurationSettings struct {
	ExportMuseNames   bool          `json:"export_muse_names" doc:"Tape files carry the inspiring Muse's name"`
	SubsonicMuseNames bool          `json:"subsonic_muse_names" doc:"Automatic Subsonic groupings are named by their Muse"`
	UpdatedAt         *string       `json:"updated_at" nullable:"true"`
	UpdatedBy         *PrincipalRef `json:"updated_by"`
}
type curationSettingsOut struct{ Body CurationSettings }

type setCurationSettingsIn struct {
	Body struct {
		ExportMuseNames   *bool `json:"export_muse_names,omitempty"`
		SubsonicMuseNames *bool `json:"subsonic_muse_names,omitempty"`
	}
}

// ---- amendments to earlier responses (C130) ----

// SongRecordRef is a Record a Song is in (get_song.records, at most 10, newest first, non-hidden Records only).
type SongRecordRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status" enum:"draft,proposed,published"`
	Mode   string `json:"mode" enum:"floating,pinned"`
}

// SongCrateRef is a manual Crate that holds a Song (get_song.crates, at most 10).
type SongCrateRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MuseCatalog names a Muse's automatic Subsonic grouping (get_muse.catalog): its catalog id and the neutral name, never the
// opted-in one.
type MuseCatalog struct {
	ID          string `json:"id" doc:"The catalog id the Subsonic server lists the grouping under"`
	NeutralName string `json:"neutral_name"`
}

// MuseCatalog is only ever null or an object, so its schema is the inline nullable one (Huma refuses nullable on a $ref object).
type museCatalogShape MuseCatalog

// Schema implements huma.SchemaProvider.
func (MuseCatalog) Schema(r huma.Registry) *huma.Schema { return nullableSchema[museCatalogShape](r) }
