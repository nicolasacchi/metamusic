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

// notImplemented is the body of every STUB(M3-Bn) handler: the contract (path, types, metadata, nil-dependency
// check) is frozen in M3-B2, the owning lane replaces the body. lane names the task, for example "M3-B4".
func notImplemented(lane string) error {
	return Err(501, "NOT_IMPLEMENTED", "this operation is not implemented yet", map[string]any{"stub": lane})
}
