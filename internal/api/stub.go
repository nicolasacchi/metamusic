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

// The T1 lane that replaces the transform stubs (details.task of a 501): B4 owns the operation handlers (C212).
const taskT1B4 = "T1-B4"

// The L3 lanes that replace a stub (details.task of a 501).
const (
	taskSR  = "L3-SR"  // suggest_sequence, the slot op sequence and propose_record.sequence
	taskNA1 = "L3-NA1" // list_next_actions
	taskTP2 = "L3-TP2" // propose_taxonomy, list_taxonomy_proposals, decide_taxonomy_proposal
	taskAT2 = "L3-AT2" // get_song_atlas
	taskAD  = "L3-AD"  // get_adherence
)

// The M5 lanes that replace a stub (details.task of a 501).
const (
	taskS1 = "M5-S1" // propose_patch, get_patch, list_patches; item mode song_pr
	taskC1 = "M5-C1" // list_callbacks, get_callback, submit_callback_verdict
	taskA1 = "M5-A1" // get_listen_heat
	taskV1 = "M5-V1" // create_voice_note

	// notImplementedCode is the code of every stub answer.
	notImplementedCode = "NOT_IMPLEMENTED"
)

// notImplemented is the 501 a frozen M5 operation answers until its lane lands; details.task names the lane.
func notImplemented(task string) error {
	return Err(501, notImplementedCode, "this operation is not implemented yet", map[string]any{"task": task})
}
