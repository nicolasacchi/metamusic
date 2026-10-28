package albums

import (
	"context"
	"errors"
)

// STUB(L2-A): the frozen method set of C126. Lane A deletes this file and adds membership.go and token.go (the record
// kind); lane A2 adds muse.go and naming.go. No signature changes. List and Get answer errors.ErrUnsupported; Token is
// stable (empty) so a Subsonic client that already asks for it sees "nothing changed".

// List returns the albums of the given kinds, all of them when none are given, ordered record (first_published_at, id),
// muse (ordinal), unfiled.
func (s *Service) List(ctx context.Context, kinds ...Kind) ([]Album, error) {
	return nil, errors.ErrUnsupported
}

// Get returns one album and its songs in order; ErrNotFound for an unknown or unlisted id.
func (s *Service) Get(ctx context.Context, id string) (Album, []Song, error) {
	return Album{}, nil, errors.ErrUnsupported
}

// Token is a cheap change token over probes and the two flags (C126.5); it never calls Ordinals or Labels.
func (s *Service) Token(ctx context.Context) (string, error) { return "", nil }
