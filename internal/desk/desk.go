// Package desk is the Composer's Desk store and service (M5b C357, C358): immutable candidate scores (plans) from a take, from
// an edit of another plan and (a later lane) from a plan job, one verdict per rater, and the files that belong to a plan. A plan
// is never a Take and never enters the library views. The package reads the library tables and writes only the desk_* tables and
// the plans/ tree of the data directory. It never imports api, generation, jobs, library or songpr (C350.2); it uses the transform service for auditions (C358) and nothing imports it back.
package desk

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Limits of C357 and D-DESK-18.
const (
	// MaxScoreBytes is the largest score a plan holds (the table CHECK on desk_plans.score_bytes).
	MaxScoreBytes = 64 << 10
	// MaxFileBytes is the largest file of a plan (the CHECK on desk_plan_files.bytes).
	MaxFileBytes = 2 << 20
	// MaxNote is the longest verdict note.
	MaxNote = 2000
	// MaxAudioS is the longest score a plan may describe (the CHECK on desk_plans.expected_audio_s).
	MaxAudioS = 600

	scoreName = "score.abc"
	plansRoot = "plans"
)

// Options wires the service. Store and Data are required; the other fields default.
type Options struct {
	Store  *store.Store
	Data   *os.Root
	Signer *serve.Signer // signs the playback links of auditions; nil leaves their url empty
	// Transform is the T1b pipeline an audition runs through (nil: auditions answer 503 UNAVAILABLE {component: transform}).
	Transform Renderer
	// DataDir is the absolute path of Data (the tools take paths); FFmpeg encodes the audition MP3.
	DataDir string
	FFmpeg  string
	// Encode replaces the ffmpeg MP3 encode (tests).
	Encode func(ctx context.Context, pcmPath, mp3Path string) error
	// Zone is the zone of the local day of the daily limit (default UTC); AuditionsPerDay overrides AuditionsPerDay.
	Zone            *time.Location
	AuditionsPerDay int
	Now             func() time.Time
	NewID           func() string
	Log             *slog.Logger
}

// Service is the Desk.
type Service struct{ o Options }

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = func() string { return uuid.NewV7().String() }
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Service{o: o}
}

func (s *Service) now() string { return store.FormatTime(s.o.Now()) }

// Error codes of C350.3 (the ones this lane raises).
const (
	CodeSourceUnsupported = "PLAN_SOURCE_UNSUPPORTED"
	CodeEditInvalid       = "PLAN_EDIT_INVALID"
	CodeNoChange          = "PLAN_NO_CHANGE"
	CodeScoreInvalid      = "SCORE_INVALID"
)

func notFound(what string) error {
	return problem.E(http.StatusNotFound, "NOT_FOUND", what+" not found", nil)
}

func invalid(field, msg string) error {
	return problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

func unavailable() error {
	return problem.E(http.StatusServiceUnavailable, "UNAVAILABLE", "the Desk is not available", map[string]any{"component": "desk"})
}

func (s *Service) wired() error {
	if s == nil || s.o.Store == nil || s.o.Data == nil {
		return unavailable()
	}
	return nil
}

// queryer is what both *sql.DB and *sql.Tx offer.
type queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}
