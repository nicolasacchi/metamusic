// Package duels holds blind A/B duels, their verdicts and the personal ladder
// built from the human ones (C48). This file is the contract: the service
// types and the wire shapes of C53; the behaviour lands in M2b-J2.
package duels

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// ClipRenderer is what duels need from the clip service; *clip.Service implements it.
type ClipRenderer interface {
	ToolVersion() string
	Measure(ctx context.Context, sp clip.Spec) (clip.Measure, error)
	Ensure(ctx context.Context, sp clip.Spec) (clip.Object, error)
}

// Options builds a Service.
type Options struct {
	Store   *store.Store
	Library *library.Service
	Clips   ClipRenderer // nil => create/next/get answer 503 UNAVAILABLE {component:"clips"}; list/submit/ladder still work
	Signer  *serve.Signer
	Now     func() time.Time
	NewID   func() string  // nil => uuid v7
	Rand    func() bool    // slot coin; nil => crypto/rand
	Expiry  time.Duration  // 0 => 30 days
	Ladder  ladder.Options // zero => defaults
}

// CreateInput is the create_duel request. Blind and MatchLoudness are pointers
// so an explicit false is honoured (nil => true; C53).
type CreateInput struct {
	ATrackID, BTrackID   string
	Blind, MatchLoudness *bool
	FromS, ToS           *float64
}

// VerdictInput is the submit_duel request.
type VerdictInput struct {
	Verdict   string // a | b | tie | both_bad
	Note      *string
	LatencyMS *int64
	Switches  *int
}

// ListFilter filters list_duels.
type ListFilter struct {
	States               []string // open | decided | expired
	TrackID, RootTrackID string
	Mode                 string // any (default) | blind | sighted
	Limit                int
	Cursor               string
}

// LadderQuery is the get_ladder request.
type LadderQuery struct {
	Scope          string // global (default) | lineage
	RootTrackID    string
	IncludeSighted bool
	Limit          int
	Cursor         string
}

// ErrNoSource is returned for principals that can never judge (system, anonymous).
var ErrNoSource = errors.New("duels: principal cannot judge")

// SourceFor maps a principal to its verdict source (C48.2): the operator and
// paired devices are "human", PAT agents (REST, MCP and mcp-stdio) are
// "critic"; system and anonymous principals never judge. No request field
// can select the source.
func SourceFor(p auth.Principal) (string, error) {
	switch {
	case p.ActsAsHuman():
		return "human", nil
	case p.Kind == auth.KindAgent:
		return "critic", nil
	}
	return "", ErrNoSource
}

// Wire shapes (C53). Every exported field has an explicit json tag; slices
// carry nullable:"false" and are never nil in a response.

// Window is the clip window of a duel.
type Window struct {
	FromS     float64 `json:"from_s"`
	ToS       float64 `json:"to_s"`
	DurationS float64 `json:"duration_s"`
}

// ClipRef is a signed clip link inside a slot.
type ClipRef struct {
	URL         string  `json:"url" doc:"Signed, relative URL; valid until expires_at"`
	ExpiresAt   string  `json:"expires_at" doc:"RFC 3339 UTC"`
	TTLS        int64   `json:"ttl_s"`
	ContentType string  `json:"content_type" example:"audio/flac"`
	Bytes       int64   `json:"bytes"`
	DurationS   float64 `json:"duration_s"`
}

// TrackBrief identifies the track in a revealed slot.
type TrackBrief struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Seed        int64   `json:"seed"`
	RequestID   string  `json:"request_id"`
	RootTrackID string  `json:"root_track_id"`
	DurationS   float64 `json:"duration_s"`
}

// Slot is one side of a duel. While a duel is unrevealed Track, GainDB and
// WindowLUFS are null (C48.3).
type Slot struct {
	Clip       ClipRef           `json:"clip"`
	Track      *TrackBriefOrNull `json:"track"`
	GainDB     *float64          `json:"gain_db"`
	WindowLUFS *float64          `json:"window_lufs"`
}

// Slots holds both sides.
type Slots struct {
	A Slot `json:"a"`
	B Slot `json:"b"`
}

// Verdict is one stored opinion.
type Verdict struct {
	ID            string  `json:"id"`
	Source        string  `json:"source" enum:"human,critic"`
	PrincipalKind string  `json:"principal_kind" enum:"human,device,agent"`
	Verdict       string  `json:"verdict" enum:"a,b,tie,both_bad"`
	WinnerTrackID *string `json:"winner_track_id" doc:"null while the duel is unrevealed, or for tie and both_bad"`
	Note          *string `json:"note"`
	LatencyMS     *int64  `json:"latency_ms"`
	Switches      *int    `json:"switches"`
	SawReveal     bool    `json:"saw_reveal" doc:"true when the server had revealed the duel to the principal before it judged"`
	CreatedAt     string  `json:"created_at" doc:"RFC 3339 UTC"`
}

// Duel is a caller's view of a duel (C48.3).
type Duel struct {
	ID              string         `json:"id"`
	State           string         `json:"state" enum:"open,decided,expired"`
	Blind           bool           `json:"blind"`
	Revealed        bool           `json:"revealed" doc:"false while a blind duel hides which slot holds which track"`
	CreatedAt       string         `json:"created_at" doc:"RFC 3339 UTC"`
	ExpiresAt       string         `json:"expires_at" doc:"RFC 3339 UTC"`
	CreatedByKind   string         `json:"created_by_kind" enum:"human,device,agent"`
	Window          Window         `json:"window"`
	LoudnessMatched bool           `json:"loudness_matched"`
	Slots           Slots          `json:"slots"`
	MyVerdict       *VerdictOrNull `json:"my_verdict"`
	HumanVerdict    *VerdictOrNull `json:"human_verdict"`
	CriticVerdicts  []Verdict      `json:"critic_verdicts" nullable:"false"`
}

// SlotSummary is a slot in a list row.
type SlotSummary struct {
	Track *TrackBriefOrNull `json:"track"`
}

// SlotsSummary holds both list-row sides.
type SlotsSummary struct {
	A SlotSummary `json:"a"`
	B SlotSummary `json:"b"`
}

// HumanVerdictBrief is the human verdict in a list row (only when revealed).
type HumanVerdictBrief struct {
	Verdict       string  `json:"verdict" enum:"a,b,tie,both_bad"`
	WinnerTrackID *string `json:"winner_track_id"`
}

// DuelSummary is one list_duels row.
type DuelSummary struct {
	ID              string                   `json:"id"`
	State           string                   `json:"state" enum:"open,decided,expired"`
	Blind           bool                     `json:"blind"`
	Revealed        bool                     `json:"revealed"`
	CreatedAt       string                   `json:"created_at"`
	ExpiresAt       string                   `json:"expires_at"`
	Window          Window                   `json:"window"`
	LoudnessMatched bool                     `json:"loudness_matched"`
	Slots           SlotsSummary             `json:"slots"`
	MyVerdict       *string                  `json:"my_verdict"`
	HumanVerdict    *HumanVerdictBriefOrNull `json:"human_verdict"`
	CriticVerdicts  int                      `json:"critic_verdicts"`
}

// Page is a list_duels page.
type Page struct {
	Items      []DuelSummary `json:"items" nullable:"false"`
	Total      int           `json:"total"`
	NextCursor *string       `json:"next_cursor"`
}

// NextResult is the next_duel answer: the oldest duel waiting for the caller's source, or null.
type NextResult struct {
	Duel      *DuelOrNull `json:"duel"`
	OpenTotal int         `json:"open_total" doc:"How many duels are waiting for you"`
}

// SubmitResult is the submit_duel answer.
type SubmitResult struct {
	Duel     Duel    `json:"duel"`
	Verdict  Verdict `json:"verdict"`
	Replayed bool    `json:"replayed" doc:"true when the verdict already existed and this call changed nothing"`
}

// LadderModel describes the fitted model.
type LadderModel struct {
	Kind       string  `json:"kind" example:"btd"`
	PriorSD    float64 `json:"prior_sd"`
	NuPrior    string  `json:"nu_prior" example:"gamma(2,2)"`
	Anchor     string  `json:"anchor" example:"reject"`
	AnchorU    float64 `json:"anchor_u"`
	Nu         float64 `json:"nu"`
	Iterations int     `json:"iterations"`
	Converged  bool    `json:"converged"`
}

// LadderCounts tallies the verdicts behind a ladder.
type LadderCounts struct {
	VerdictsHumanCounted    int `json:"verdicts_human_counted"`
	VerdictsCriticExcluded  int `json:"verdicts_critic_excluded"`
	VerdictsSightedExcluded int `json:"verdicts_sighted_excluded"`
	BothBad                 int `json:"both_bad"`
	Ties                    int `json:"ties"`
	Tracks                  int `json:"tracks"`
}

// LadderDiagnostics exposes slot bias.
type LadderDiagnostics struct {
	SlotAWinRate *float64 `json:"slot_a_win_rate" doc:"Share of slot A among counted a/b verdicts; null when there are none"`
	NAB          int      `json:"n_ab"`
}

// LadderItem is one track on the ladder.
type LadderItem struct {
	Rank        int        `json:"rank"`
	TrackID     string     `json:"track_id"`
	Title       string     `json:"title"`
	RootTrackID string     `json:"root_track_id"`
	Seed        int64      `json:"seed"`
	U           float64    `json:"u" doc:"Log-strength anchored at the virtual reject item (0)"`
	SD          float64    `json:"sd"`
	CI95        [2]float64 `json:"ci95"`
	NDuels      int        `json:"n_duels"`
	Wins        int        `json:"wins"`
	Losses      int        `json:"losses"`
	Ties        int        `json:"ties"`
	BothBad     int        `json:"both_bad"`
	Provisional bool       `json:"provisional" doc:"fewer than 3 duels or sd above 1.0"`
}

// LadderPage is the get_ladder answer.
type LadderPage struct {
	Scope          string            `json:"scope" enum:"global,lineage"`
	RootTrackID    *string           `json:"root_track_id"`
	IncludeSighted bool              `json:"include_sighted"`
	GeneratedAt    string            `json:"generated_at" doc:"RFC 3339 UTC"`
	Model          LadderModel       `json:"model"`
	Counts         LadderCounts      `json:"counts"`
	Diagnostics    LadderDiagnostics `json:"diagnostics"`
	Items          []LadderItem      `json:"items" nullable:"false"`
	Total          int               `json:"total"`
	NextCursor     *string           `json:"next_cursor"`
}
