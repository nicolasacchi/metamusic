package nextactions

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// Rules is the registry (extension move 3), pinned by testdata/rules_v1_{a,b,c}.golden.json once NA1, NA2 and NA3 fill
// it; it is empty until then. NA1 deletes this file.
var Rules []Rule

// List answers list_next_actions (C139.1).
func (s *Service) List(ctx context.Context, viewer auth.Principal, f Filter) (Page, error) {
	return Page{}, errors.ErrUnsupported
}

// ForTake answers the /context assembly: rendered strings of the rules about this take's Song, Muse or Records (C139.4).
func (s *Service) ForTake(ctx context.Context, viewer auth.Principal, trackID string, limit int) ([]Action, error) {
	return nil, errors.ErrUnsupported
}
