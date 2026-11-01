package mcpgen_test

// l3TaxonomyCases are the parity cases of propose_taxonomy and list_taxonomy_proposals (lane TP2 owns this file; the decision
// op has no MCP face). Until TP2 lands the stubs answer 501 on every face; TP2 replaces the cases and drops Status.
var l3TaxonomyCases = []ParityCase{
	{Op: "propose_taxonomy", SkipGeneric: true, Status: 501, Args: fixedArgs(map[string]any{
		"rationale": "Synthetic rationale", "mappings": []map[string]any{{"term": "synthterm", "facet": "mood", "value": "chill", "op": "add"}}})},
	{Op: "list_taxonomy_proposals", Args: fixedArgs(map[string]any{"state": []string{"open"}}), Status: 501},
}
