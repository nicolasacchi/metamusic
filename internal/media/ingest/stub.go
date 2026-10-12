package ingest

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// Pipeline ingests masters into DataDir.
type Pipeline struct {
	data    *os.Root
	dataDir string
	tools   mediatool.Tools
	an      analysis.Analyzer
	now     func() time.Time
}

// New builds a Pipeline.
// STUB(M1-B7): returns a pipeline whose methods are unimplemented.
func New(data *os.Root, dataDir string, tools mediatool.Tools, an analysis.Analyzer, now func() time.Time) *Pipeline {
	return &Pipeline{data: data, dataDir: dataDir, tools: tools, an: an, now: now}
}

// Ingest freezes req.Source into the library.
// STUB(M1-B7)
func (p *Pipeline) Ingest(ctx context.Context, req Request) (Result, error) {
	return Result{}, errors.ErrUnsupported
}

// Discard removes exactly what r.Created lists after a failed DB insert.
// STUB(M1-B7)
func (p *Pipeline) Discard(r Result) error { return errors.ErrUnsupported }
