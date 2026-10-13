package mcpgen_test

import (
	"strings"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// placeholderToken has the shape of a confirmation token; M3-B4 replaces the stubs, after which the
// create cases read the real token from the estimate case before them.
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
	{Op: "estimate_generation", Status: 501, Args: fixedArgs(map[string]any{"items": []map[string]any{{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 3}}})},
	{Op: "create_generation", Status: 501, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"confirmation_token": tokenFrom(prev), "rationale": "parity"}
	}},
	{Op: "estimate_variant", Status: 501, Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "mode": "new_seed", "n_seeds": 3})},
	{Op: "create_variant", Status: 501, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"id": lt.TrackLuna, "confirmation_token": tokenFrom(prev)}
	}},
	{Op: "request_approval", Status: 501, Args: fixedArgs(map[string]any{"id": lt.BatchAwaiting})},
	{Op: "get_approval_status", Status: 501, Args: fixedArgs(map[string]any{"id": lt.BatchAwaiting})},
	{Op: "list_batches", Status: 501, Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "get_batch", Status: 501, Args: fixedArgs(map[string]any{"id": lt.BatchDone})},
	{Op: "cancel_batch", Status: 501, SkipGeneric: true, Args: fixedArgs(map[string]any{"id": lt.BatchAwaiting, "reason": "parity"})},
	{Op: "list_jobs", Status: 501, Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "get_job", Status: 501, Args: fixedArgs(map[string]any{"id": lt.JobDone})},
	{Op: "wait_for_job", Status: 501, Args: fixedArgs(map[string]any{"id": lt.JobDone, "timeout_s": 0})},
	{Op: "cancel_job", Status: 501, Args: fixedArgs(map[string]any{"id": lt.JobAwaiting1, "reason": "parity"})},
	{Op: "get_budget", Status: 501},
	{Op: "get_spend", Status: 501, Args: fixedArgs(map[string]any{"month": "2026-10"})},
	{Op: "list_ledger", Status: 501, Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "get_provider_status", Status: 501},
	{Op: "get_lineage", Status: 501, Args: fixedArgs(map[string]any{"id": lt.TrackLuna})},
}

func init() {
	parityCases = append(parityCases, m3Cases...)
}
