package api

import "time"

// Input and output shapes of the Morning Crate operations served by ops_morning_m6n.go (M6N C383). The crate, settings and page
// types are types_m6n_morning.go's.

// bodyMorningEvents is the body limit of submit_morning_crate_events (C365.6).
const bodyMorningEvents = 256 << 10

type morningSettingsOut struct{ Body MorningSettings }

type updateMorningSettingsIn struct{ Body MorningSettingsPatch }

type listMorningCratesIn struct {
	State  string `query:"state" enum:"building,ready,empty,failed" doc:"Only crates in this state"`
	Limit  int    `query:"limit" minimum:"1" maximum:"100" default:"20"`
	Cursor string `query:"cursor"`
}

type morningCratePageOut struct{ Body MorningCratePage }

type getMorningCrateIn struct {
	ID string `path:"id" doc:"Morning crate id"`
}

type morningCrateOut struct{ Body MorningCrate }

type buildMorningCrateIn struct {
	IdemHeader
	Body BuildMorningCrateBody
}

// MorningEvent is one raw headset or rating event of a crate. Nothing in it names a take: it addresses the crate's own chapters.
type MorningEvent struct {
	ID        string    `json:"id" pattern:"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$" doc:"Client-made UUID (v7); a replay of the same id is counted as a duplicate"`
	Side      int       `json:"side" minimum:"0" doc:"0-based side the position is in"`
	Chapter   int       `json:"chapter" minimum:"1" doc:"Chapter number (the number of the slate tone)"`
	Kind      string    `json:"kind" enum:"marker,strong_marker,skip,complete,play,rate"`
	Stars     *int      `json:"stars,omitempty" nullable:"true" minimum:"1" maximum:"5" doc:"rate only: 1..5, or null to clear the blind rating"`
	SideMS    int64     `json:"side_ms" minimum:"0" doc:"Position in the side, milliseconds"`
	LatencyMS int64     `json:"latency_ms,omitempty" minimum:"0" maximum:"5000" doc:"Headset latency, subtracted from a marker's position"`
	ClientAt  time.Time `json:"client_at"`
}

type submitMorningEventsIn struct {
	ID   string `path:"id" doc:"Morning crate id"`
	Body struct {
		Events []MorningEvent `json:"events" minItems:"1" maxItems:"200"`
	}
}

// MorningEventRejection names an event that was not stored.
type MorningEventRejection struct {
	ID   string `json:"id"`
	Code string `json:"code" enum:"UNKNOWN_CHAPTER,OUT_OF_RANGE,INVALID_EVENT"`
}

// MorningEventsResult is the answer of submit_morning_crate_events.
type MorningEventsResult struct {
	Accepted   int                     `json:"accepted"`
	Duplicates int                     `json:"duplicates"`
	Rejected   []MorningEventRejection `json:"rejected" nullable:"false"`
	Revealed   bool                    `json:"revealed" doc:"The crate is revealed now: by this call (every chapter had blind stars) or earlier"`
}

type morningEventsOut struct{ Body MorningEventsResult }

type revealMorningCrateIn struct {
	ID string `path:"id" doc:"Morning crate id"`
}
