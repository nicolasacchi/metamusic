package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
)

// stub answers 501 NOT_IMPLEMENTED. Every M2a handler starts as a stub marked
// STUB(M2a-Bn) at its registration, so the contract (types, metadata, schemas)
// is frozen in Stage 1 and the owning task replaces only the handler body.
func stub[I, O any](task string) func(context.Context, *I) (*O, error) {
	return func(context.Context, *I) (*O, error) { return nil, notImplemented(task) }
}

// withBody sets the request body cap of an operation (C38).
func withBody(op huma.Operation, n int64) huma.Operation {
	op.MaxBodyBytes = n
	return op
}

// created marks an operation that answers 201.
func created(op huma.Operation) huma.Operation {
	op.DefaultStatus = 201
	return op
}
