package api

import (
	"encoding/json"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/muses"
)

// L1 wire types (C96). Frozen after L1-B2: openapi.json and schema.d.ts are derived from them. JSON is snake_case;
// principals go through RefFor/KeyFor; Muse text appears only in the objects marked "Muse text" (C99).

// OptString is a patch field that may be absent, an explicit null or a string (update_song angle and muse_id). The
// JSON decoder calls UnmarshalJSON only when the key is present.
type OptString struct {
	Set   bool
	Value *string
}

// Schema implements huma.SchemaProvider: a nullable string.
func (OptString) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Nullable: true}
}

// UnmarshalJSON records presence and null.
func (o *OptString) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	o.Value = &s
	return nil
}

// ---- Muses ----

// MuseRef is the form in which Muse text reaches readers (Muse text; read scope, D-L1-6).
type MuseRef = muses.MuseRef

// Muse is one Muse (Muse text).
type Muse struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind" enum:"reference,brief"`
	Title     string  `json:"title"`
	Artist    *string `json:"artist" nullable:"true"`
	Brief     *string `json:"brief" nullable:"true"`
	Source    string  `json:"source" enum:"recipe_refs,human,agent"`
	CreatedBy string  `json:"created_by"`
	UpdatedAt string  `json:"updated_at"`
	Revision  int     `json:"revision"`
	Hidden    bool    `json:"hidden"`
}

// MuseCoverage counts the live, non-hidden Songs of a Muse by state.
type MuseCoverage struct {
	Songs      int `json:"songs"`
	Draft      int `json:"draft"`
	Rendered   int `json:"rendered"`
	Judged     int `json:"judged"`
	Picked     int `json:"picked"`
	QueuedJobs int `json:"queued_jobs"`
}

// MuseSummary is a list row (Muse text).
type MuseSummary struct {
	Muse
	MuseCoverage
}

// MuseAngleCount is the Songs of a Muse per angle (null = none).
type MuseAngleCount struct {
	Angle *string `json:"angle" nullable:"true"`
	Songs int     `json:"songs"`
}

// MuseDetail is get_muse (Muse text).
type MuseDetail struct {
	Muse       Muse             `json:"muse"`
	Coverage   MuseCoverage     `json:"coverage"`
	Angles     []MuseAngleCount `json:"angles" nullable:"false"`
	Songs      []SongSummary    `json:"songs" nullable:"false"`
	SongsTotal int              `json:"songs_total"`
}

// AssignResult is the outcome of assign_muse_songs.
type AssignResult struct {
	Assigned  int `json:"assigned"`
	Unchanged int `json:"unchanged"`
}

type listMusesIn struct {
	Q             string `query:"q" maxLength:"200" doc:"Phrase-prefix search over Muse title, artist and brief"`
	Kind          string `query:"kind" enum:"reference,brief"`
	IncludeHidden bool   `query:"include_hidden"`
	Limit         int    `query:"limit" minimum:"1" maximum:"200" default:"100"`
	Cursor        string `query:"cursor"`
}
type musesOut struct {
	Body struct {
		Items      []MuseSummary `json:"items" nullable:"false"`
		NextCursor *string       `json:"next_cursor" nullable:"true"`
		Total      int           `json:"total"`
	}
}
type getMuseIn struct {
	ID         string `path:"id" doc:"Muse id"`
	SongsLimit int    `query:"songs_limit" minimum:"0" maximum:"200" default:"100"`
}
type museDetailOut struct{ Body MuseDetail }
type createMuseIn struct {
	IdemHeader
	Body struct {
		Kind   string  `json:"kind" enum:"reference,brief"`
		Title  string  `json:"title" minLength:"1" maxLength:"200"`
		Artist *string `json:"artist,omitempty" minLength:"1" maxLength:"200" doc:"Reference Muses only"`
		Brief  *string `json:"brief,omitempty" minLength:"1" maxLength:"2000" doc:"Brief Muses only"`
	}
}
type museOut struct{ Body Muse }
type updateMuseIn struct {
	ID   string `path:"id" doc:"Muse id"`
	Body struct {
		Revision int     `json:"revision" minimum:"1"`
		Title    *string `json:"title,omitempty" minLength:"1" maxLength:"200"`
		Artist   *string `json:"artist,omitempty" minLength:"1" maxLength:"200"`
		Brief    *string `json:"brief,omitempty" minLength:"1" maxLength:"2000"`
		Hidden   *bool   `json:"hidden,omitempty"`
	}
}
type assignMuseSongsIn struct {
	ID   string `path:"id" doc:"Muse id"`
	Body struct {
		SongIDs []string `json:"song_ids" minItems:"1" maxItems:"200"`
	}
}
type assignResultOut struct{ Body AssignResult }

// ---- Songs ----

// SongPickRef is the Pick of a Song as lists show it.
type SongPickRef struct {
	TakeID string `json:"take_id"`
	Source string `json:"source" enum:"pinned,ladder,stars,newest"`
}

// SongSummary is a list row and the core of a Board chip.
type SongSummary struct {
	ID            string       `json:"id"`
	Title         string       `json:"title"`
	Angle         *string      `json:"angle" nullable:"true"`
	MuseID        *string      `json:"muse_id" nullable:"true"`
	State         string       `json:"state" enum:"draft,rendered,judged,picked"`
	Takes         int          `json:"takes"`
	Pick          *SongPickRef `json:"pick"`
	DraftID       *string      `json:"draft_id" nullable:"true"`
	RootTrackID   *string      `json:"root_track_id" nullable:"true"`
	QueuedJobs    int          `json:"queued_jobs"`
	OpenProposals int          `json:"open_proposals"`
	Hidden        bool         `json:"hidden"`
	Archived      bool         `json:"archived"`
	UpdatedAt     string       `json:"updated_at"`
}

// SongCore is the identity block of get_song and the answer of update_song.
type SongCore struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Angle        *string  `json:"angle" nullable:"true"`
	AngleSource  *string  `json:"angle_source" nullable:"true" enum:"reference,request_id,manual,cleared"`
	Origin       string   `json:"origin" enum:"draft,root"`
	Revision     int      `json:"revision"`
	State        string   `json:"state" enum:"draft,rendered,judged,picked"`
	Hidden       bool     `json:"hidden"`
	Archived     bool     `json:"archived"`
	QueuedJobs   int      `json:"queued_jobs"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	RootTrackID  *string  `json:"root_track_id" nullable:"true"`
	RootTrackIDs []string `json:"root_track_ids" nullable:"false"`
	DraftID      *string  `json:"draft_id" nullable:"true"`
	Retired      bool     `json:"retired"`
	RetiredInto  *string  `json:"retired_into" nullable:"true"`
}

// SongDraft is the draft of an unrendered Song.
type SongDraft struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	Revision  int     `json:"revision"`
	Label     string  `json:"label"`
	RequestID string  `json:"request_id"`
	GroupKey  *string `json:"group_key" nullable:"true"`
}

// TakeLadder is a take's ladder strength.
type TakeLadder struct {
	U      float64 `json:"u"`
	SD     float64 `json:"sd"`
	NDuels int     `json:"n_duels"`
}

// TakeTempo is requested versus measured tempo of a take.
type TakeTempo struct {
	RequestedBPM *float64 `json:"requested_bpm" nullable:"true"`
	MeasuredBPM  *float64 `json:"measured_bpm" nullable:"true"`
}

// TakeCard is one take on the Song page.
type TakeCard struct {
	ID             string      `json:"id"`
	Title          string      `json:"title"`
	Status         string      `json:"status"`
	Generation     int         `json:"generation"`
	ParentTrackID  *string     `json:"parent_track_id" nullable:"true"`
	Origin         string      `json:"origin"`
	DurationS      float64     `json:"duration_s"`
	RecordedAt     string      `json:"recorded_at"`
	Lossy          bool        `json:"lossy"`
	RecipeKnown    bool        `json:"recipe_known"`
	Stars          *int        `json:"stars" nullable:"true" doc:"Human stars"`
	CriticStars    *int        `json:"critic_stars" nullable:"true"`
	IsPick         bool        `json:"is_pick"`
	Pinned         bool        `json:"pinned"`
	HasEars        bool        `json:"has_ears"`
	Flags          []string    `json:"flags" nullable:"false" doc:"Ears flag codes"`
	Ladder         *TakeLadder `json:"ladder"`
	Tempo          *TakeTempo  `json:"tempo"`
	CostDisplayUSD *string     `json:"cost_display_usd" nullable:"true"`
}

// PickView is the current Pick of a Song (C91.4).
type PickView struct {
	TakeID        *string       `json:"take_id" nullable:"true"`
	Source        string        `json:"source" enum:"none,pinned,ladder,stars,newest"`
	PinnedTakeID  *string       `json:"pinned_take_id" nullable:"true"`
	PinIneligible bool          `json:"pin_ineligible"`
	PinnedBy      *PrincipalRef `json:"pinned_by"`
	PinnedAt      *string       `json:"pinned_at" nullable:"true"`
	Evidence      RawRequest    `json:"evidence" doc:"mm.pick/1 evidence"`
	ChangedAt     string        `json:"changed_at"`
	LastEventID   string        `json:"last_event_id"`
	Seq           int           `json:"seq"`
	Pending       bool          `json:"pending"`
	OpenProposals int           `json:"open_proposals"`
}

// PickProposal is an agent's suggestion for a Song's Pick; the rationale is agent-authored text.
type PickProposal struct {
	ID         string       `json:"id"`
	SongID     string       `json:"song_id"`
	TakeID     string       `json:"take_id"`
	Rationale  string       `json:"rationale"`
	ProposedBy PrincipalRef `json:"proposed_by"`
	State      string       `json:"state" enum:"open,accepted,dismissed,superseded,expired"`
	CreatedAt  string       `json:"created_at"`
	DecidedAt  *string      `json:"decided_at" nullable:"true"`
	DecidedBy  *string      `json:"decided_by" nullable:"true"`
}

// PickEvent is one entry of the append-only Pick feed (C91.8).
type PickEvent struct {
	Cursor     int64        `json:"cursor"`
	ID         string       `json:"id"`
	SongID     string       `json:"song_id"`
	Seq        int          `json:"seq"`
	At         string       `json:"at"`
	FromTakeID *string      `json:"from_take_id" nullable:"true"`
	ToTakeID   *string      `json:"to_take_id" nullable:"true"`
	FromSource string       `json:"from_source" enum:"none,pinned,ladder,stars,newest"`
	ToSource   string       `json:"to_source" enum:"none,pinned,ladder,stars,newest"`
	Reason     string       `json:"reason" enum:"initial,pin,unpin,pin_ineligible,pin_restored,ladder,stars,newest,emptied"`
	Actor      PrincipalRef `json:"actor"`
	Evidence   RawRequest   `json:"evidence"`
}

// SoundFacetEntry is one facet value of a Song with its origin.
type SoundFacetEntry struct {
	Value  string `json:"value"`
	Origin string `json:"origin"`
}

// TempoDetail is the collapsed tempo of a Song (D-L1-17).
type TempoDetail struct {
	RequestedBPM *float64 `json:"requested_bpm" nullable:"true"`
	PlanBPM      *float64 `json:"plan_bpm" nullable:"true"`
	MeasuredBPM  *float64 `json:"measured_bpm" nullable:"true"`
	Basis        string   `json:"basis" enum:"measured,declared,plan"`
	Mismatch     bool     `json:"mismatch"`
	Octave       bool     `json:"octave"`
}

// SongSounds is the Sound block of get_song.
type SongSounds struct {
	TaxonomyVersion int                          `json:"taxonomy_version"`
	Facets          map[string][]SoundFacetEntry `json:"facets"`
	Tempo           *TempoDetail                 `json:"tempo"`
	Unmapped        []string                     `json:"unmapped" nullable:"false"`
}

// SongSession names a Session a Song belongs to (collection-derived names are Muse-adjacent text, C99).
type SongSession struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// SongLineage summarises the lineage of the primary root.
type SongLineage struct {
	RootTrackID   string `json:"root_track_id"`
	Generations   int    `json:"generations"`
	ChildrenTotal int    `json:"children_total"`
}

// SongView is get_song (Muse text in `muse` and `sessions`).
type SongView struct {
	Song       SongCore       `json:"song"`
	Muse       *MuseRef       `json:"muse"`
	Draft      *SongDraft     `json:"draft"`
	Takes      []TakeCard     `json:"takes" nullable:"false"`
	TakesTotal int            `json:"takes_total"`
	Pick       *PickView      `json:"pick"`
	Proposals  []PickProposal `json:"proposals" nullable:"false"`
	Events     []PickEvent    `json:"events" nullable:"false"`
	Sounds     SongSounds     `json:"sounds"`
	Sessions   []SongSession  `json:"sessions" nullable:"false"`
	Lineage    *SongLineage   `json:"lineage"`
	Pending    bool           `json:"pending"`
}

// PinResult is the outcome of pin_pick.
type PinResult struct {
	Pick     PickView      `json:"pick"`
	Proposal *PickProposal `json:"proposal"`
	Changed  bool          `json:"changed"`
}

// SongFilterIn (exported so Huma binds the embedded fields) are the filters shared by list_songs and search_sounds.
type SongFilterIn struct {
	MuseID           string         `query:"muse_id"`
	NoMuse           bool           `query:"no_muse"`
	Angle            string         `query:"angle"`
	State            []string       `query:"state,explode" nullable:"false" enum:"draft,rendered,judged,picked"`
	Queued           optParam[bool] `query:"queued"`
	SessionID        string         `query:"session_id"`
	Sound            []string       `query:"sound,explode" nullable:"false" maxItems:"30" doc:"facet:value, repeatable; OR inside a facet, AND across facets"`
	TempoMismatch    optParam[bool] `query:"tempo_mismatch"`
	HasPick          optParam[bool] `query:"has_pick"`
	DraftID          string         `query:"draft_id" doc:"The single Song of this draft"`
	TrackID          string         `query:"track_id" doc:"The single Song of this take"`
	HasOpenProposals optParam[bool] `query:"has_open_proposals"`
	Q                string         `query:"q" maxLength:"200" doc:"Case-insensitive substring of the Song title"`
	IncludeHidden    bool           `query:"include_hidden"`
	IncludeArchived  bool           `query:"include_archived"`
}
type listSongsIn struct {
	SongFilterIn
	Limit  int    `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor string `query:"cursor"`
}
type songsOut struct {
	Body struct {
		Items      []SongSummary `json:"items" nullable:"false"`
		NextCursor *string       `json:"next_cursor" nullable:"true"`
		Total      int           `json:"total"`
	}
}
type getSongIn struct {
	ID            string `path:"id" doc:"Song id; with by=draft a draft id, with by=track a take id"`
	By            string `query:"by" enum:"song,draft,track" default:"song"`
	TakeLimit     int    `query:"take_limit" minimum:"1" maximum:"100" default:"24"`
	IncludeEvents int    `query:"include_events" minimum:"0" maximum:"50" default:"10"`
}
type songViewOut struct{ Body SongView }
type updateSongIn struct {
	ID   string `path:"id" doc:"Song id"`
	Body struct {
		Revision int       `json:"revision" minimum:"1"`
		Title    *string   `json:"title,omitempty" minLength:"1" maxLength:"200"`
		Angle    OptString `json:"angle,omitempty" doc:"A slug, or null to clear it for good"`
		MuseID   OptString `json:"muse_id,omitempty" doc:"A Muse id, or null to unassign"`
		Hidden   *bool     `json:"hidden,omitempty"`
	}
}
type songCoreOut struct{ Body SongCore }

// ---- Picks ----

type proposePickIn struct {
	IdemHeader
	ID   string `path:"id" doc:"Song id"`
	Body struct {
		TakeID    string `json:"take_id"`
		Rationale string `json:"rationale" minLength:"1" maxLength:"500"`
	}
}
type pickProposalOut struct{ Body PickProposal }
type pinPickIn struct {
	ID   string `path:"id" doc:"Song id"`
	Body struct {
		TakeID     *string `json:"take_id" required:"true" nullable:"true" doc:"The take to pin, or null to unpin"`
		ProposalID *string `json:"proposal_id,omitempty" doc:"An open proposal for this take to accept"`
	}
}
type pinResultOut struct{ Body PinResult }
type dismissProposalIn struct {
	ID string `path:"id" doc:"Proposal id"`
}
type listPickEventsIn struct {
	SongID  string   `query:"song_id"`
	SongIDs []string `query:"song_ids,explode" nullable:"false" maxItems:"200"`
	After   int64    `query:"after" minimum:"0" doc:"The cursor of the previous page (exclusive)"`
	Limit   int      `query:"limit" minimum:"1" maximum:"200" default:"100"`
}
type pickEventsOut struct {
	Body struct {
		Items      []PickEvent `json:"items" nullable:"false"`
		NextCursor *int64      `json:"next_cursor" nullable:"true"`
	}
}

// ---- Sessions ----

// SessionSummary is a Session with its member counts (the name can embed an inspiration: Muse-adjacent text).
type SessionSummary struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind" enum:"import,upload,batch,night"`
	Name      string  `json:"name"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at" nullable:"true"`
	Hidden    bool    `json:"hidden"`
	Revision  int     `json:"revision"`
	Drafts    int     `json:"drafts"`
	Takes     int     `json:"takes"`
	Songs     int     `json:"songs"`
}
type listSessionsIn struct {
	Kind          []string `query:"kind,explode" nullable:"false" enum:"import,upload,batch,night"`
	Q             string   `query:"q" maxLength:"200"`
	IncludeHidden bool     `query:"include_hidden"`
	Limit         int      `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor        string   `query:"cursor"`
}
type sessionsOut struct {
	Body struct {
		Items      []SessionSummary `json:"items" nullable:"false"`
		NextCursor *string          `json:"next_cursor" nullable:"true"`
		Total      int              `json:"total"`
	}
}
type updateSessionIn struct {
	ID   string `path:"id" doc:"Session id"`
	Body struct {
		Revision int     `json:"revision" minimum:"1"`
		Name     *string `json:"name,omitempty" minLength:"1" maxLength:"120"`
		Hidden   *bool   `json:"hidden,omitempty"`
	}
}
type sessionOut struct{ Body SessionSummary }

// ---- Sounds ----

// FacetValueCount is one value of a facet with its Song count.
type FacetValueCount struct {
	Value      string `json:"value"`
	Count      int    `json:"count"`
	Mismatches *int   `json:"mismatches,omitempty" doc:"tempo_band only: Songs whose requested and measured tempo disagree"`
}

// SoundSearch is search_sounds.
type SoundSearch struct {
	TaxonomyVersion int                          `json:"taxonomy_version"`
	Selected        map[string][]string          `json:"selected"`
	Facets          map[string][]FacetValueCount `json:"facets"`
	Songs           []SongSummary                `json:"songs" nullable:"false"`
	Total           int                          `json:"total"`
	NextCursor      *string                      `json:"next_cursor" nullable:"true"`
}
type searchSoundsIn struct {
	SongFilterIn
	Limit  int    `query:"limit" minimum:"0" maximum:"100" default:"50" doc:"0 returns facet counts only"`
	Cursor string `query:"cursor"`
}
type soundSearchOut struct{ Body SoundSearch }

// TaxonomyItem is one effective term mapping (agent-authored terms may embed Muse text, C99).
type TaxonomyItem struct {
	Term         string `json:"term"`
	Facet        string `json:"facet"`
	Value        string `json:"value"`
	SinceVersion int    `json:"since_version"`
}

// TaxonomyVersion is one taxonomy version.
type TaxonomyVersion struct {
	Version   int          `json:"version"`
	Kind      string       `json:"kind" enum:"seed,extension"`
	SeedID    *string      `json:"seed_id" nullable:"true"`
	CreatedBy PrincipalRef `json:"created_by"`
	CreatedAt string       `json:"created_at"`
	Note      *string      `json:"note" nullable:"true"`
}

// UnmappedTerm is a style part no taxonomy phrase matched.
type UnmappedTerm struct {
	Term  string `json:"term"`
	Songs int    `json:"songs"`
}

// TaxonomyPage is get_sound_taxonomy.
type TaxonomyPage struct {
	CurrentVersion int               `json:"current_version"`
	Versions       []TaxonomyVersion `json:"versions" nullable:"false"`
	Items          []TaxonomyItem    `json:"items" nullable:"false"`
	Unmapped       []UnmappedTerm    `json:"unmapped" nullable:"false"`
	NextCursor     *string           `json:"next_cursor" nullable:"true"`
}
type taxonomyIn struct {
	Facet    string `query:"facet" enum:"genre_family,era,vocal,mood,language"`
	Q        string `query:"q" maxLength:"100"`
	Unmapped bool   `query:"unmapped" doc:"Return the top unmapped style terms instead of the mappings"`
	Limit    int    `query:"limit" minimum:"1" maximum:"200" default:"100"`
	Cursor   string `query:"cursor"`
}
type taxonomyOut struct{ Body TaxonomyPage }

// MapEntry is one requested mapping change.
type MapEntry struct {
	Term  string `json:"term" minLength:"1" maxLength:"60"`
	Facet string `json:"facet" enum:"genre_family,era,vocal,mood,language"`
	Value string `json:"value" minLength:"1" maxLength:"40"`
	Op    string `json:"op" enum:"add,remove"`
}

// MapRecomputed counts the subjects a map_sound_term call recomputed.
type MapRecomputed struct {
	Takes int `json:"takes"`
	Songs int `json:"songs"`
}

// MapResult is the outcome of map_sound_term.
type MapResult struct {
	Version         *int          `json:"version" nullable:"true" doc:"The new taxonomy version; null when every mapping was already effective"`
	MappingsApplied int           `json:"mappings_applied"`
	Unchanged       int           `json:"unchanged"`
	Recomputed      MapRecomputed `json:"recomputed"`
}
type mapSoundTermIn struct {
	Body struct {
		Mappings []MapEntry `json:"mappings" minItems:"1" maxItems:"50"`
		Note     *string    `json:"note,omitempty" minLength:"1" maxLength:"500"`
	}
}
type mapResultOut struct{ Body MapResult }

// ---- Board ----

// BoardColumn is one angle column (null = the "(none)" column).
type BoardColumn struct {
	Angle *string `json:"angle" nullable:"true"`
	Songs int     `json:"songs"`
}

// BoardCounts are Song counts by state.
type BoardCounts struct {
	Draft    int `json:"draft"`
	Rendered int `json:"rendered"`
	Judged   int `json:"judged"`
	Picked   int `json:"picked"`
	Queued   int `json:"queued"`
}

// SongChip is one Song on the Board.
type SongChip struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	State         string  `json:"state" enum:"draft,rendered,judged,picked"`
	Takes         int     `json:"takes"`
	PickSource    *string `json:"pick_source" nullable:"true"`
	PickTakeID    *string `json:"pick_take_id" nullable:"true"`
	Queued        bool    `json:"queued"`
	DraftID       *string `json:"draft_id" nullable:"true" doc:"Only when state is draft"`
	TempoMismatch bool    `json:"tempo_mismatch"`
}

// BoardCell is one non-empty (Muse, angle) cell.
type BoardCell struct {
	Angle *string    `json:"angle" nullable:"true"`
	Chips []SongChip `json:"chips" nullable:"false" doc:"Empty unless detail=chips"`
}

// BoardRow is one Muse row; Muse is null for the Unassigned row (Muse text).
type BoardRow struct {
	Muse                 *MuseRef    `json:"muse"`
	SongsTotal           int         `json:"songs_total"`
	Counts               BoardCounts `json:"counts"`
	Cells                []BoardCell `json:"cells" nullable:"false"`
	ChipsMore            bool        `json:"chips_more"`
	NextUnassignedCursor *string     `json:"next_unassigned_cursor" nullable:"true"`
}

// BoardTotals are over the whole filter, not the page.
type BoardTotals struct {
	Muses    int `json:"muses"`
	Songs    int `json:"songs"`
	Draft    int `json:"draft"`
	Rendered int `json:"rendered"`
	Judged   int `json:"judged"`
	Picked   int `json:"picked"`
	Queued   int `json:"queued"`
}

// Board is get_muse_board (C97).
type Board struct {
	GeneratedAt     string        `json:"generated_at"`
	TaxonomyVersion int           `json:"taxonomy_version"`
	Columns         []BoardColumn `json:"columns" nullable:"false"`
	Rows            []BoardRow    `json:"rows" nullable:"false"`
	Totals          BoardTotals   `json:"totals"`
	NextCursor      *string       `json:"next_cursor" nullable:"true"`
}
type boardIn struct {
	Detail          string   `query:"detail" enum:"chips,counts" default:"counts"`
	MuseID          string   `query:"muse_id"`
	SessionID       string   `query:"session_id"`
	Angle           string   `query:"angle"`
	State           []string `query:"state,explode" nullable:"false" enum:"draft,rendered,judged,picked"`
	Sound           []string `query:"sound,explode" nullable:"false" maxItems:"30"`
	Q               string   `query:"q" maxLength:"200"`
	IncludeHidden   bool     `query:"include_hidden"`
	Limit           int      `query:"limit" minimum:"1" maximum:"500" default:"200"`
	Cursor          string   `query:"cursor"`
	UnassignedAfter string   `query:"unassigned_after" doc:"Cursor inside the Unassigned row"`
}
type boardOut struct{ Body Board }
