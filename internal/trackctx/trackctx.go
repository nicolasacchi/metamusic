// Package trackctx builds the /context bundle (contract C35). M2a-B4a freezes
// the signatures and the response envelope; M2a-B8 implements them.
package trackctx

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// Limits (UTF-16 units of the REST body).
const (
	DefaultLimit = 24000
	IncludeLimit = 96000
)

// Bundle is the mm.context/1 document. Sub-objects are open JSON objects whose
// shape C35 documents; unknown values are null, never invented.
type Bundle struct {
	Format      string         `json:"format" example:"mm.context/1"`
	Track       map[string]any `json:"track"`
	Recipe      map[string]any `json:"recipe"`
	Provenance  map[string]any `json:"provenance"`
	Score       map[string]any `json:"score" nullable:"true"`
	Lineage     map[string]any `json:"lineage"`
	Judgments   map[string]any `json:"judgments"`
	Perception  map[string]any `json:"perception"`
	Listening   map[string]any `json:"listening"`
	Links       map[string]any `json:"_links"`
	NextActions []string       `json:"next_actions" nullable:"false"`
	Budget      Budget         `json:"_budget"`
}

// Budget reports the size of the bundle as the REST body carries it.
type Budget struct {
	Chars     int      `json:"chars"`
	Limit     int      `json:"limit"`
	Truncated []string `json:"truncated" nullable:"false"`
}

// Builder assembles bundles.
type Builder struct {
	lib       *library.Service
	j         *judgments.Service
	l         *listening.Service
	signer    *serve.Signer
	publicURL string
	now       func() time.Time
}

// New builds a Builder.
func New(lib *library.Service, j *judgments.Service, l *listening.Service, signer *serve.Signer, publicURL string, now func() time.Time) *Builder {
	if now == nil {
		now = time.Now
	}
	return &Builder{lib: lib, j: j, l: l, signer: signer, publicURL: publicURL, now: now}
}

// Build assembles the bundle of one track; include is a subset of score|lyrics|comments.
func (b *Builder) Build(ctx context.Context, p auth.Principal, trackID string, include []string) (Bundle, error) {
	// STUB(M2a-B8)
	return Bundle{}, errors.ErrUnsupported
}

// Fit trims b to limit characters and returns its measured size and the trimmed parts.
func Fit(b *Bundle, limit int, include []string) (chars int, truncated []string) {
	// STUB(M2a-B8)
	return 0, nil
}

// Measure is the UTF-16 length of b as Huma's JSON encoder writes it (trailing newline excluded).
func Measure(b *Bundle) int {
	// STUB(M2a-B8)
	return 0
}
