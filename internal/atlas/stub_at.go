package atlas

import (
	"context"
	"errors"
)

// Get answers get_song_atlas: one read-only transaction, never writes (lane AT2).
func (s *Service) Get(ctx context.Context, q Query) (Atlas, error) {
	return Atlas{}, errors.ErrUnsupported
}
