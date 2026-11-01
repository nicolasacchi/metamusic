package app

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
)

// buildL3NextActions constructs the rule engine and hands it to /context through the nil-safe reader (lane NA1 owns this
// file).
func buildL3NextActions(in l3Inputs, s *l3Services) {
	s.NextActions = nextactions.New(in.st, in.muses, in.songview, in.sounds, in.now)
	in.ctxb.UseNextActions(nextActionReader{s.NextActions})
}

// nextActionReader adapts *nextactions.Service to trackctx.NextActionReader: the rendered text of each action.
type nextActionReader struct{ s *nextactions.Service }

func (r nextActionReader) ForTake(ctx context.Context, p auth.Principal, trackID string, limit int) ([]string, error) {
	as, err := r.s.ForTake(ctx, p, trackID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Text
	}
	return out, nil
}
