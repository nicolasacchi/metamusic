package tape

import (
	"context"
	"errors"
)

// STUB(L2-T1): the production Pipeline (C128.4). Lane T1 deletes this file and adds pipeline.go, gainstage.go, chapters.go,
// tags.go and the preflight files; NewPipeline keeps its signature.

type stubPipeline struct{}

// NewPipeline builds the production Pipeline: ffmpeg decode, the Go gain stage, the reference flac encoder, the Go
// metadata rewrite and the verification.
func NewPipeline(o PipelineOptions) Pipeline { return stubPipeline{} }

func (stubPipeline) Render(ctx context.Context, j Job, progress func(Progress)) (Result, error) {
	return Result{}, errors.ErrUnsupported
}
