package api

import "github.com/danielgtaylor/huma/v2"

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

// The L2 lanes that replaced a stub; m2bErr names one in the details.task of a 501 from a service that answers ErrUnsupported.
const (
	taskV2 = "L2-V2" // Records operations (recordview)
	taskT2 = "L2-T2" // tape operations
	taskK  = "L2-K"  // Crates operations and the list-op predicates
	taskA  = "L2-A"  // curation settings
)
