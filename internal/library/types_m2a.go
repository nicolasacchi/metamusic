package library

import "github.com/nicolasacchi/metamusic/internal/auth"

// Final JSON shapes of the M2a fields on M1 types (contract C38). The values
// are filled by M2a-B4b (judgments) and M2a-B5 (draft lint); until then they
// are zero values with non-nil slices. judgments, drafts and the API alias or
// embed these so the OpenAPI contract has one definition.

// RatingRow is one source's current rating of a track.
type RatingRow struct {
	Stars     *int              `json:"stars" minimum:"1" maximum:"5"`
	Verdict   *string           `json:"verdict" maxLength:"280"`
	By        auth.PrincipalRef `json:"by"`
	UpdatedAt string            `json:"updated_at" doc:"RFC 3339 UTC"`
}

// TrackRating holds both rating rows; the human row is canonical.
type TrackRating struct {
	TrackID string     `json:"track_id"`
	Human   *RatingRow `json:"human" doc:"The operator rating (canonical); null when none"`
	Critic  *RatingRow `json:"critic" doc:"The agent rating; null when none"`
}

// TagRef is one effective tag with the sources that carry it.
type TagRef struct {
	Name    string   `json:"name"`
	Sources []string `json:"sources" nullable:"false" enum:"human,agent,inherited"`
}

// TrackTags lists a track's effective tags.
type TrackTags struct {
	TrackID string   `json:"track_id"`
	Tags    []TagRef `json:"tags" nullable:"false"`
}

// PlaylistRef names a playlist that holds a track.
type PlaylistRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListeningStats summarises human listening (C36).
type ListeningStats struct {
	Plays          int      `json:"plays"`
	CompletionRate *float64 `json:"completion_rate" doc:"complete/start, at most 1; null without plays"`
	LastPlayedAt   *string  `json:"last_played_at"`
}

// TagFacet is one tag with its track count.
type TagFacet struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// StarFacet is one human star value with its track count.
type StarFacet struct {
	Stars int `json:"stars"`
	Count int `json:"count"`
}

// JudgmentStats are the judgment counters of get_stats.
type JudgmentStats struct {
	RatedHuman  int            `json:"rated_human"`
	RatedCritic int            `json:"rated_critic"`
	StarsHist   map[string]int `json:"stars_hist" doc:"Human stars 1..5 to track count"`
	Comments    int            `json:"comments"`
	OpenFix     int            `json:"open_fix"`
	Tags        int            `json:"tags"`
}

// BackupStats reports the last backup.
type BackupStats struct {
	LastCompletedAt *string `json:"last_completed_at"`
	LastState       *string `json:"last_state"`
}
