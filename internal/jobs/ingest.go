package jobs

import (
	"context"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// JobRow is the claimed job as the runner and the ingest bridge see it.
type JobRow struct {
	ID, BatchID, RecipeID, Operator, Title, State string
	SourceTrackID, DraftID                        *string
	MutationJSON                                  string
	TakeIndex, TimeoutS, ExpectedAudioS           int
}

// AttemptRow is one provider attempt of a job.
type AttemptRow struct {
	ID, JobID, State string
	N                int
	WindowEndsAt     time.Time
}

// Ingester turns a verified worker result into a library take (C75.5).
type Ingester interface {
	IngestTake(ctx context.Context, job JobRow, att AttemptRow, files []upload.Received, out provider.Output) (trackID string, err error)
}

// BridgeOptions builds the real ingest bridge.
type BridgeOptions struct {
	Data    *os.Root
	Library *library.Service
	Ears    *ears.Service
	Lineage *lineage.Service
	Store   *store.Store
	Clock   clock.Clock
	NewID   func() string
}

// IngestBridge is the production Ingester.
type IngestBridge struct{ o BridgeOptions }

// NewIngestBridge builds the bridge.
func NewIngestBridge(o BridgeOptions) *IngestBridge { return &IngestBridge{o: o} }

// IngestTake implements Ingester. STUB(M3-B6).
func (b *IngestBridge) IngestTake(ctx context.Context, job JobRow, att AttemptRow, files []upload.Received, out provider.Output) (string, error) {
	return "", ErrNotImplemented
}
