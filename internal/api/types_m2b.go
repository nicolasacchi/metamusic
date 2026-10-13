package api

import (
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/ears"
)

// Types of the M2b operations (contract C53). Domain shapes (Duel, LadderPage,
// Dossier, Comparison, ...) live in internal/duels and internal/ears with their
// nullable-object schemas; this file holds the request inputs, the envelopes
// and the shapes that exist only on the wire.

// ImageData is an image inside a response body (C52). Operations registered
// with Meta.MCPImage return it as MCP ImageContent. The type lives in ears
// (the only package that fills it) so the services never import api.
type ImageData = ears.ImageData

// ClipLink is a signed link to a rendered clip (get_clip).
type ClipLink struct {
	TrackID       string   `json:"track_id"`
	URL           string   `json:"url" doc:"Signed, relative URL; valid until expires_at"`
	Key           string   `json:"key" doc:"Content key of the clip: identical inputs give identical bytes"`
	ContentType   string   `json:"content_type" example:"audio/flac"`
	ExpiresAt     string   `json:"expires_at" doc:"RFC 3339 UTC"`
	TTLS          int64    `json:"ttl_s" doc:"Link lifetime granted to the caller, seconds"`
	Bytes         int64    `json:"bytes"`
	SampleRate    int      `json:"sample_rate" example:"48000"`
	BitsPerSample int      `json:"bits_per_sample" example:"24"`
	Channels      int      `json:"channels"`
	FromS         float64  `json:"from_s"`
	ToS           float64  `json:"to_s"`
	DurationS     float64  `json:"duration_s"`
	GainDB        float64  `json:"gain_db" doc:"Applied gain in dB; clips are only ever attenuated"`
	WindowLUFS    *float64 `json:"window_lufs" doc:"Integrated loudness of the window before gain; null when not measured"`
	WindowTPDBTP  *float64 `json:"window_tp_dbtp"`
	TargetLUFS    *float64 `json:"target_lufs"`
	Match         string   `json:"match" enum:"off,matched,limited,window_silent"`
}

type clipLinkOut struct{ Body ClipLink }

type getClipIn struct {
	ID        string            `path:"id" doc:"Track id"`
	FromS     optParam[float64] `query:"from_s" minimum:"0" doc:"Window start in seconds (rounded to 0.1 s); default: about 30% into the track"`
	ToS       optParam[float64] `query:"to_s" minimum:"0" doc:"Window end in seconds; the window must be 30-45 s long (or the whole track when it is shorter)"`
	MatchLUFS optParam[float64] `query:"match_lufs" minimum:"-40" maximum:"-6" doc:"Attenuate the clip toward this integrated loudness; quieter windows are never boosted"`
}

type createDuelIn struct {
	IdemHeader
	Body struct {
		ATrackID      string   `json:"a_track_id" minLength:"1"`
		BTrackID      string   `json:"b_track_id" minLength:"1"`
		Blind         *bool    `json:"blind,omitempty" doc:"Hide which slot holds which track until the human verdict (default true)"`
		FromS         *float64 `json:"from_s,omitempty" minimum:"0" doc:"Window start in seconds; default: about 30% into the shorter track"`
		ToS           *float64 `json:"to_s,omitempty" minimum:"0"`
		MatchLoudness *bool    `json:"match_loudness,omitempty" doc:"Attenuate the louder window to the quieter one (default true)"`
	}
}

type duelOut struct{ Body duels.Duel }

type nextDuelIn struct {
	Exclude []string `query:"exclude,explode" nullable:"false" maxItems:"50" doc:"Duel ids to skip"`
}
type nextDuelOut struct{ Body duels.NextResult }

type duelIDIn struct {
	ID string `path:"id" doc:"Duel id"`
}

type listDuelsIn struct {
	State       []string `query:"state,explode" nullable:"false" enum:"open,decided,expired" doc:"Repeatable state filter"`
	TrackID     string   `query:"track_id" doc:"Duels with this track in either slot (pair membership is not secret)"`
	RootTrackID string   `query:"root_track_id" doc:"Duels with a track of this lineage in either slot"`
	Mode        string   `query:"mode" enum:"any,blind,sighted" default:"any"`
	Limit       int      `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor      string   `query:"cursor" doc:"Opaque cursor from next_cursor"`
}
type duelPageOut struct{ Body duels.Page }

type submitDuelIn struct {
	ID   string `path:"id" doc:"Duel id"`
	Body struct {
		Verdict   string  `json:"verdict" enum:"a,b,tie,both_bad" doc:"a or b: that slot is better; tie; both_bad: neither is acceptable"`
		Note      *string `json:"note,omitempty" maxLength:"2000" doc:"Trimmed; empty means no note"`
		LatencyMS *int64  `json:"latency_ms,omitempty" minimum:"0" maximum:"86400000"`
		Switches  *int    `json:"switches,omitempty" minimum:"0" maximum:"100000"`
	}
}

// submitDuelOut answers 201 for a new verdict and 200 for a replay (Status).
type submitDuelOut struct {
	Status int
	Body   duels.SubmitResult
}

type getLadderIn struct {
	Scope          string `query:"scope" enum:"global,lineage" default:"global"`
	RootTrackID    string `query:"root_track_id" doc:"Required for scope=lineage"`
	IncludeSighted bool   `query:"include_sighted" doc:"Also count non-blind human verdicts (biased; default false)"`
	Limit          int    `query:"limit" minimum:"1" maximum:"500" default:"200"`
	Cursor         string `query:"cursor"`
}
type ladderOut struct{ Body duels.LadderPage }

type dossierOut struct{ Body ears.Dossier }

type spectrogramIn struct {
	ID     string `path:"id" doc:"Track id"`
	Inline bool   `query:"inline" default:"true" doc:"Embed the image as base64 (default); false returns only a signed link"`
}
type spectrogramOut struct{ Body ears.SpectrogramView }

type compareIn struct {
	A string `query:"a" required:"true" doc:"First track id"`
	B string `query:"b" required:"true" doc:"Second track id"`
}
type comparisonOut struct{ Body ears.Comparison }
