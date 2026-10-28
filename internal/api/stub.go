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

// The L2 lanes that replace a stub; notImplemented names one in the problem's details.task.
const (
	taskV2 = "L2-V2" // Records operations (recordview)
	taskT2 = "L2-T2" // tape operations
	taskK  = "L2-K"  // Crates operations and the list-op predicates
	taskA  = "L2-A"  // curation settings
)

// notImplemented is the answer of an L2 stub: 501 NOT_IMPLEMENTED naming the task that replaces it. The parity runner and
// the scripted-agent test skip a stub by name; lane INT-B's TestNoPendingL2Stubs turns every skip into a failure.
func notImplemented(task string) error {
	return Err(http.StatusNotImplemented, "NOT_IMPLEMENTED", "this operation is not implemented yet", map[string]any{"task": task})
}
