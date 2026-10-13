package clip

import (
	"context"
	"errors"
	"os"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// STUB(M2b-M1): the methods below answer errors.ErrUnsupported (501 NOT_IMPLEMENTED at the API) until M2b-M1.

// New builds the service.
func New(data *os.Root, dataDir string, tools mediatool.Tools, an WindowAnalyzer, lib *library.Service,
	signer *serve.Signer, o Options) *Service {
	return &Service{data: data, dataDir: dataDir, tools: tools, an: an, lib: lib, signer: signer, opts: o}
}

// ToolVersion is the analyzer's version token ("ffmpeg <token>"); it is part of every clip key.
func (s *Service) ToolVersion() string { return s.an.ToolVersion() }

// Measure measures the loudness of the window of sp.
func (s *Service) Measure(ctx context.Context, sp Spec) (Measure, error) {
	return Measure{}, errors.ErrUnsupported
}

// Ensure renders the clip of sp into the cache if it is absent.
func (s *Service) Ensure(ctx context.Context, sp Spec) (Object, error) {
	return Object{}, errors.ErrUnsupported
}

// Resolve serves rendered clips ("flac" keys under derived/clips/); unknown keys are not found.
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) {
	return serve.Object{}, serve.ErrNotFound
}

// Link answers get_clip: a signed URL of the (rendered) clip of trackID.
func (s *Service) Link(ctx context.Context, p auth.Principal, trackID string, q Query) (Link, error) {
	return Link{}, errors.ErrUnsupported
}
