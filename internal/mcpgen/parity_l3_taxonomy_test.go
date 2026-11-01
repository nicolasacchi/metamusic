package mcpgen_test

// l3TaxonomyCases are the parity cases of propose_taxonomy and list_taxonomy_proposals (lane TP2 owns this file; the decision
// op has no MCP face, its REST tests are TestDecide_* in the api package). The proposal is not replayed through call_operation:
// a second identical call returns the open proposal with reused set.
var l3TaxonomyCases = []ParityCase{
	{Op: "propose_taxonomy", SkipGeneric: true, Masks: []string{"id", "proposed_by"}, Args: fixedArgs(map[string]any{
		"rationale": "Synthetic rationale", "mappings": []map[string]any{{"term": "synthterm", "facet": "mood", "value": "chill", "op": "add"}}})},
	{Op: "list_taxonomy_proposals", Masks: []string{"id", "proposed_by"}, Args: fixedArgs(map[string]any{"state": []string{"open"}})},
}
