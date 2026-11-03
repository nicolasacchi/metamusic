package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrTransformMother marks an operation that needs a generation mother on a transform take (D-T1-12): a dsp recipe is not a
// YuE2 request, so "regenerate" must point at the nearest Take that is one.
var ErrTransformMother = errors.New("library: TRANSFORM_TAKE_NOT_MOTHER")

// TransformMotherError carries the offending take and the nearest ancestor that can be a mother (nil when the chain ends at
// a score render).
type TransformMotherError struct {
	TrackID           string
	NearestAncestorID *string
}

func (e *TransformMotherError) Error() string        { return "library: TRANSFORM_TAKE_NOT_MOTHER" }
func (e *TransformMotherError) Is(target error) bool { return target == ErrTransformMother }

// maxMotherHops bounds the ancestor walk (a lineage is a chain of generations, far shorter than this).
const maxMotherHops = 64

// RequireRenderableMother returns a *TransformMotherError for a transform take, next to RequireRecipeKnown and
// RequireLosslessMaster. ErrNotFound for an unknown id.
func (s *Service) RequireRenderableMother(ctx context.Context, trackID string) error {
	nearest, isTransform, err := s.nearestRenderable(ctx, trackID)
	if err != nil {
		return err
	}
	if isTransform {
		return &TransformMotherError{TrackID: trackID, NearestAncestorID: nearest}
	}
	return nil
}

// NearestRenderableAncestor returns the closest ancestor (following parent_track_id) that is not a transform take, nil when
// the take is itself renderable or the chain ends at a rootless transform (a score render).
func (s *Service) NearestRenderableAncestor(ctx context.Context, trackID string) (*string, error) {
	nearest, _, err := s.nearestRenderable(ctx, trackID)
	return nearest, err
}

func (s *Service) nearestRenderable(ctx context.Context, trackID string) (nearest *string, isTransform bool, err error) {
	cur := trackID
	for hop := 0; hop < maxMotherHops; hop++ {
		var tid, parent sql.NullString
		err := s.st.R.QueryRowContext(ctx, `SELECT transform_id, parent_track_id FROM tracks WHERE id = ?`, cur).Scan(&tid, &parent)
		if errors.Is(err, sql.ErrNoRows) {
			if hop == 0 {
				return nil, false, ErrNotFound
			}
			return nil, true, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("library: renderable mother: %w", err)
		}
		if !tid.Valid {
			if hop == 0 {
				return nil, false, nil
			}
			id := cur
			return &id, true, nil
		}
		if !parent.Valid {
			return nil, true, nil
		}
		cur = parent.String
	}
	return nil, true, nil
}
