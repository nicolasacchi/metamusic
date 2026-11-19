package generation

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// Brief is the reduced budget view of a principal that is not on the Owner side (C305, C310.5): whether its batches need the
// Owner's approval and whether generation is enabled. It reads no cap, no usage and no token or connector limit.
type Brief struct {
	ApprovalRequired bool
	Provider         provider.State
}

// Brief returns the Editor's budget view. Every Editor batch waits for the Owner (D-U1-11), so ApprovalRequired is true.
func (s *Service) Brief(ctx context.Context, p auth.Principal) (Brief, error) {
	if err := s.ready(); err != nil {
		return Brief{}, err
	}
	st, err := s.o.Gate.State(ctx)
	if err != nil {
		return Brief{}, err
	}
	return Brief{ApprovalRequired: !p.IsOwnerSide(), Provider: st}, nil
}
