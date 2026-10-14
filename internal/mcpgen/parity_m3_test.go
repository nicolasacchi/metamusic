package mcpgen_test

import (
	"strings"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// placeholderToken has the shape of a confirmation token; a create case reads the real token from the
// estimate case before it (prev) and falls back to this one.
var placeholderToken = "mmc_" + strings.Repeat("a", 52)

// tokenFrom reads the confirmation token of the previous case on the same rig, else the placeholder.
func tokenFrom(prev map[string]any) string {
	if s, ok := prev["confirmation_token"].(string); ok && s != "" {
		return s
	}
	return placeholderToken
}

// m3Cases covers every M3 operation that is a tool or generic (the five human-only ones are not on MCP).
// Until the owning lane fills a handler, all three faces must answer the same 501 problem (C86.1);
// the lane then removes Status and fills the arguments. Rigs run the fake backend with the runner idle,
// so a create stops at queued and no job ever runs during parity.
var m3Cases = []ParityCase{
	{Op: "estimate_generation", Masks: estimateMasks, Args: fixedArgs(map[string]any{"items": []map[string]any{{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 3}}})},
	{Op: "create_generation", Masks: batchMasks, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"confirmation_token": tokenFrom(prev), "rationale": "parity"}
	}},
	{Op: "estimate_variant", Masks: estimateMasks, Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "mode": "new_seed", "n_seeds": 3})},
	{Op: "create_variant", Masks: batchMasks, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"id": lt.TrackLuna, "confirmation_token": tokenFrom(prev)}
	}},
	// the batch the create_variant case just made belongs to the rig's own agent; the ids differ per rig
	{Op: "request_approval", Masks: batchMasks, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"id": idFrom(prev, "id")}
	}},
	{Op: "get_approval_status", Masks: batchMasks, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"id": idFrom(prev, "batch_id")}
	}},
	{Op: "list_batches", Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "get_batch", Args: fixedArgs(map[string]any{"id": lt.BatchDone})},
	{Op: "cancel_batch", Status: 403, SkipGeneric: true, Args: fixedArgs(map[string]any{"id": lt.BatchAwaiting, "reason": "parity"})},
	{Op: "list_jobs", Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "get_job", Args: fixedArgs(map[string]any{"id": lt.JobDone})},
	{Op: "wait_for_job", Args: fixedArgs(map[string]any{"id": lt.JobDone, "timeout_s": 0})},
	{Op: "cancel_job", Status: 403, Args: fixedArgs(map[string]any{"id": lt.JobAwaiting1, "reason": "parity"})},
	{Op: "get_budget", Masks: moneyMasks},
	{Op: "get_spend", Masks: moneyMasks, Args: fixedArgs(map[string]any{"month": "2026-10"})},
	{Op: "list_ledger", Masks: moneyMasks, Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "get_provider_status"},
	{Op: "get_lineage", Args: fixedArgs(map[string]any{"id": lt.TrackLuna})},
}

// Ids drawn from a rig's id sequence (which the audit rows of the calls advance), token ids and the
// references built from them legitimately differ between faces and rigs.
var (
	estimateMasks = []string{"estimate_id", "confirmation_token"}
	batchMasks    = []string{"id", "batch_id", "estimate_id", "created_by", "decided_by", "confirmation_token"}
	moneyMasks    = []string{"id", "batch_id", "job_id", "principal", "created_by", "updated_by", "tokens"}
)

// idFrom reads an id the previous case answered (a batch), else the fixture's awaiting batch.
func idFrom(prev map[string]any, key string) string {
	if s, ok := prev[key].(string); ok && s != "" {
		return s
	}
	return lt.BatchAwaiting
}

func init() {
	parityCases = append(parityCases, m3Cases...)
}
