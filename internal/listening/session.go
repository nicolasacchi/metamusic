package listening

import "time"

// Session summarises the human's recent listening (C112.3): human and device listen events with
// client_at >= Since, grouped per track, newest last play first, at most the requested number of tracks.
// M4a-B2 freezes the types (prompts.Sources names them); M4a-A1 implements Service.Session.
type Session struct {
	Since  time.Time      `json:"since"`
	Until  time.Time      `json:"until"`
	Tracks []SessionTrack `json:"tracks" nullable:"false"`
	Totals SessionTotals  `json:"totals"`
}

// SessionTrack is one track of a Session.
type SessionTrack struct {
	TrackID      string    `json:"track_id"`
	Title        string    `json:"title"`
	Plays        int       `json:"plays"`
	Completed    int       `json:"completed"`
	Skipped      int       `json:"skipped"`
	SecondsHeard int       `json:"seconds_heard" doc:"sum of segment to_ms - from_ms, in seconds, rounded"`
	LastPlayedAt time.Time `json:"last_played_at"`
	Stars        *int      `json:"stars" nullable:"true" doc:"the human's rating, null when unrated"`
	OpenComments int       `json:"open_comments"`
}

// SessionTotals are the sums over the listed tracks.
type SessionTotals struct {
	Tracks       int `json:"tracks"`
	Plays        int `json:"plays"`
	SecondsHeard int `json:"seconds_heard"`
}
