package api

import (
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/morning"
)

// Morning Crate wire types (M6N C383). The operations that serve them belong to lane loop-morning-reveal (C4); the lane that built the
// service owns the shapes and the conversions, so the handlers only call them.

// MorningSettings is the crate's behaviour: when it is built and how much of what goes in. It holds no money.
type MorningSettings struct {
	Enabled          bool         `json:"enabled"`
	At               string       `json:"at" pattern:"^([01][0-9]|2[0-3]):[0-5][0-9]$" doc:"Local time of day (MM_TZ) the crate is built, HH:MM"`
	Minutes          int          `json:"minutes" minimum:"5" maximum:"120" doc:"Length of the whole crate"`
	SideMinutes      int          `json:"side_minutes" minimum:"5" maximum:"15" doc:"Longest side"`
	MaxTakes         int          `json:"max_takes" minimum:"1" maximum:"30"`
	Anchors          int          `json:"anchors" minimum:"0" maximum:"2" doc:"Calibration takes the Owner rated five stars or marked final, mixed in blind"`
	IncludeTruncated bool         `json:"include_truncated"`
	KeepDays         int          `json:"keep_days" minimum:"1" maximum:"30" doc:"How long the side files are kept"`
	WaitMinutes      int          `json:"wait_minutes" minimum:"0" maximum:"120" doc:"How long the 07:00 build waits for tonight's batch to finish"`
	UpdatedBy        PrincipalRef `json:"updated_by"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// MorningSettingsPatch is the body of update_morning_crate_settings; absent fields keep their value.
type MorningSettingsPatch struct {
	Enabled          *bool   `json:"enabled,omitempty"`
	At               *string `json:"at,omitempty" pattern:"^([01][0-9]|2[0-3]):[0-5][0-9]$"`
	Minutes          *int    `json:"minutes,omitempty" minimum:"5" maximum:"120"`
	SideMinutes      *int    `json:"side_minutes,omitempty" minimum:"5" maximum:"15"`
	MaxTakes         *int    `json:"max_takes,omitempty" minimum:"1" maximum:"30"`
	Anchors          *int    `json:"anchors,omitempty" minimum:"0" maximum:"2"`
	IncludeTruncated *bool   `json:"include_truncated,omitempty"`
	KeepDays         *int    `json:"keep_days,omitempty" minimum:"1" maximum:"30"`
	WaitMinutes      *int    `json:"wait_minutes,omitempty" minimum:"0" maximum:"120"`
}

// Patch converts the body for the service.
func (p MorningSettingsPatch) Patch() morning.SettingsPatch {
	return morning.SettingsPatch{Enabled: p.Enabled, At: p.At, Minutes: p.Minutes, SideMinutes: p.SideMinutes, MaxTakes: p.MaxTakes, Anchors: p.Anchors,
		IncludeTruncated: p.IncludeTruncated, KeepDays: p.KeepDays, WaitMinutes: p.WaitMinutes}
}

// MorningSettingsOut renders the settings for a viewer.
func MorningSettingsOut(viewer auth.Principal, s morning.Settings) MorningSettings {
	return MorningSettings{Enabled: s.Enabled, At: s.At, Minutes: s.Minutes, SideMinutes: s.SideMinutes, MaxTakes: s.MaxTakes, Anchors: s.Anchors,
		IncludeTruncated: s.IncludeTruncated, KeepDays: s.KeepDays, WaitMinutes: s.WaitMinutes, UpdatedBy: mandateRef(viewer, s.UpdatedBy), UpdatedAt: s.UpdatedAt}
}

// BuildMorningCrateBody is the body of build_morning_crate.
type BuildMorningCrateBody struct {
	IncludeTrackIDs []string `json:"include_track_ids,omitempty" maxItems:"30" doc:"Takes to put in whatever their rating or age"`
	Minutes         int      `json:"minutes,omitempty" minimum:"5" maximum:"120" doc:"Length of this crate; the setting when absent"`
}

// In converts the body for the service.
func (b BuildMorningCrateBody) In() morning.BuildIn {
	return morning.BuildIn{IncludeTrackIDs: b.IncludeTrackIDs, Minutes: b.Minutes}
}

// MorningTrack names a take. It is null on every chapter until the crate is revealed.
type MorningTrack struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	RootTrackID string `json:"root_track_id"`
}

// MorningTrackOrNull is a MorningTrack that may be null (the repo's OrNull convention).
type MorningTrackOrNull MorningTrack

type morningTrackShape MorningTrack

// Schema implements huma.SchemaProvider.
func (MorningTrackOrNull) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[morningTrackShape](r)
}

// MorningSide is one file of a crate.
type MorningSide struct {
	Side       int        `json:"side" doc:"0-based"`
	Label      string     `json:"label" doc:"Side A, Side B ..."`
	DurationMS int64      `json:"duration_ms"`
	Bytes      int64      `json:"bytes"`
	Chapters   int        `json:"chapters"`
	URL        *string    `json:"url" nullable:"true" doc:"Signed link, null once the file is swept"`
	ExpiresAt  *time.Time `json:"expires_at" nullable:"true"`
	SHA256     string     `json:"sha256"`
	Removed    bool       `json:"removed"`
}

// MorningChapter is one take of a crate. Until the reveal the take, anchor flag, gain and loudness are null: nothing identifies it.
type MorningChapter struct {
	N             int                 `json:"n" doc:"1-based, also the number in the slate tone"`
	Side          int                 `json:"side"`
	StartMS       int64               `json:"start_ms" doc:"Start of the slate, relative to the side"`
	AudioStartMS  int64               `json:"audio_start_ms"`
	EndMS         int64               `json:"end_ms"`
	Stars         *int                `json:"stars" nullable:"true" doc:"The latest blind rating"`
	Verdict       string              `json:"verdict" enum:"kept,skipped,marked,heard,unheard"`
	Markers       int                 `json:"markers"`
	StrongMarkers int                 `json:"strong_markers"`
	SkippedAtMS   *int64              `json:"skipped_at_ms" nullable:"true"`
	Track         *MorningTrackOrNull `json:"track"`
	Anchor        *bool               `json:"anchor" nullable:"true"`
	GainDB        *float64            `json:"gain_db" nullable:"true"`
	Limited       *bool               `json:"limited" nullable:"true"`
	LUFSAfter     *float64            `json:"lufs_after" nullable:"true"`
}

// MorningCrate is one crate with its sides and chapters.
type MorningCrate struct {
	ID               string           `json:"id"`
	State            string           `json:"state" enum:"building,ready,empty,failed"`
	Trigger          string           `json:"trigger" enum:"scheduled,manual"`
	CreatedAt        time.Time        `json:"created_at"`
	BuiltAt          *time.Time       `json:"built_at" nullable:"true"`
	Revealed         bool             `json:"revealed"`
	RevealedAt       *time.Time       `json:"revealed_at" nullable:"true"`
	AdjacencyRelaxed bool             `json:"adjacency_relaxed" doc:"Two takes of one lineage sit side by side because the order could not avoid it"`
	Takes            int              `json:"takes"`
	Anchors          int              `json:"anchors"`
	DurationMS       int64            `json:"duration_ms"`
	ErrorCode        *string          `json:"error_code" nullable:"true"`
	Sides            []MorningSide    `json:"sides" nullable:"false"`
	Chapters         []MorningChapter `json:"chapters" nullable:"false"`
	PlaylistID       *string          `json:"playlist_id" nullable:"true" doc:"The shared playlist made at the reveal"`
}

// MorningCrateSummary is a row of list_morning_crates.
type MorningCrateSummary struct {
	ID         string     `json:"id"`
	State      string     `json:"state" enum:"building,ready,empty,failed"`
	Trigger    string     `json:"trigger" enum:"scheduled,manual"`
	CreatedAt  time.Time  `json:"created_at"`
	BuiltAt    *time.Time `json:"built_at" nullable:"true"`
	Revealed   bool       `json:"revealed"`
	RevealedAt *time.Time `json:"revealed_at" nullable:"true"`
	Takes      int        `json:"takes"`
	Anchors    int        `json:"anchors"`
	Sides      int        `json:"sides"`
	DurationMS int64      `json:"duration_ms"`
	ErrorCode  *string    `json:"error_code" nullable:"true"`
}

// MorningCratePage is one page of list_morning_crates, newest first.
type MorningCratePage struct {
	Items      []MorningCrateSummary `json:"items" nullable:"false"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

// MorningCrateOut converts a crate. The service has already hidden what the viewer may not see.
func MorningCrateOut(c morning.Crate) MorningCrate {
	out := MorningCrate{ID: c.ID, State: c.State, Trigger: c.Trigger, CreatedAt: c.CreatedAt, BuiltAt: c.BuiltAt, Revealed: c.Revealed, RevealedAt: c.RevealedAt,
		AdjacencyRelaxed: c.AdjacencyRelaxed, Takes: c.Takes, Anchors: c.Anchors, DurationMS: c.DurationMS, ErrorCode: c.ErrorCode, PlaylistID: c.PlaylistID,
		Sides: make([]MorningSide, 0, len(c.Sides)), Chapters: make([]MorningChapter, 0, len(c.Chapters))}
	for _, s := range c.Sides {
		out.Sides = append(out.Sides, MorningSide{Side: s.Side, Label: s.Label, DurationMS: s.DurationMS, Bytes: s.Bytes, Chapters: s.Chapters, URL: s.URL,
			ExpiresAt: s.ExpiresAt, SHA256: s.SHA256, Removed: s.Removed})
	}
	for _, ch := range c.Chapters {
		w := MorningChapter{N: ch.N, Side: ch.Side, StartMS: ch.StartMS, AudioStartMS: ch.AudioStartMS, EndMS: ch.EndMS, Stars: ch.Stars, Verdict: ch.Verdict,
			Markers: ch.Markers, StrongMarkers: ch.StrongMarkers, SkippedAtMS: ch.SkippedAtMS, Anchor: ch.Anchor, GainDB: ch.GainDB, Limited: ch.Limited,
			LUFSAfter: ch.LUFSAfter}
		if ch.Track != nil {
			t := MorningTrackOrNull(MorningTrack{ID: ch.Track.ID, Title: ch.Track.Title, RootTrackID: ch.Track.RootTrackID})
			w.Track = &t
		}
		out.Chapters = append(out.Chapters, w)
	}
	return out
}

// MorningCratePageOut converts a page of summaries.
func MorningCratePageOut(p morning.Page) MorningCratePage {
	out := MorningCratePage{Items: make([]MorningCrateSummary, 0, len(p.Items)), NextCursor: p.NextCursor}
	for _, s := range p.Items {
		out.Items = append(out.Items, MorningCrateSummary(s))
	}
	return out
}
