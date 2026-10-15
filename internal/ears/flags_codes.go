package ears

import (
	"context"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/library"
)

// MaxFlagCodeIDs caps one FlagCodes batch.
const MaxFlagCodeIDs = 100

// ErrTooManyIDs is returned by FlagCodes for a batch over MaxFlagCodeIDs.
var ErrTooManyIDs = errors.New("ears: too many ids")

// FlagCodes returns the flag codes of each take (the codes of the dossier and of /context perception.flags), read-only:
// nothing is analysed. A take without analyses maps to an empty list; unknown ids are absent from the map (C90.6).
func (s *Service) FlagCodes(ctx context.Context, ids []string) (map[string][]string, error) {
	if len(ids) > MaxFlagCodeIDs {
		return nil, fmt.Errorf("%w: %d > %d", ErrTooManyIDs, len(ids), MaxFlagCodeIDs)
	}
	out := make(map[string][]string, len(ids))
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		st, err := s.load(ctx, id)
		switch {
		case errors.Is(err, library.ErrNotFound):
			continue
		case err != nil:
			return nil, err
		}
		out[id] = st.flagCodes()
	}
	return out, nil
}
