package api

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

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

// notImplemented is the answer of an M4a stub: 501 NOT_IMPLEMENTED naming the task that replaces it. Both
// the parity runner and the scripted-agent test recognise it and skip by name; B3's TestNoPendingM4aStubs turns
// the skip into a failure.
func notImplemented(task string) error {
	return Err(http.StatusNotImplemented, "NOT_IMPLEMENTED", "this operation is not implemented yet", map[string]any{"task": task})
}
