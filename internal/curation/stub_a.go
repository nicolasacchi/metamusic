package curation

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// STUB(L2-A): the frozen Set and Stats of C127.1 and C130.5. Lane A deletes this file and adds settings.go and stats.go;
// no signature changes. Both answer errors.ErrUnsupported, which the API maps to 501.

// Set changes the opt-ins; p.Kind must be KindHuman (403 HUMAN_ONLY before any read), an empty patch is 422 and unchanged
// values are a 200 without a write.
func (s *Service) Set(ctx context.Context, p auth.Principal, patch SettingsPatch) (Settings, error) {
	return Settings{}, errors.ErrUnsupported
}

// Stats counts Records, Crates, tapes, pending slots and the review queue for get_stats (C130.5).
func (s *Service) Stats(ctx context.Context) (Stats, error) { return Stats{}, errors.ErrUnsupported }
