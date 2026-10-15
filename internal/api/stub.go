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
