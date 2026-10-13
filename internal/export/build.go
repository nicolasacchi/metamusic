package export

import (
	"context"
	"errors"
)

// Build assembles the songs.jsonl of a selection (C33): EXPORT_EMPTY (an api problem) when no row matches.
func Build(ctx context.Context, src Source, sel Selection) (Result, error) {
	// STUB(M2a-B6)
	return Result{}, errors.ErrUnsupported
}
