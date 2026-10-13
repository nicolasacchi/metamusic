package mcpgen_test

// tokenCases are the M2a-B9 cases. Token ids and prefixes are random per rig.
// revoke_own_token runs last: it ends the principal of every face, so nothing
// may follow it.
var tokenCases = []ParityCase{
	{Op: "list_tokens", Masks: []string{"id", "prefix"}},
	{Op: "revoke_own_token", Masks: []string{"id", "prefix"}, Last: true},
}

func init() {
	parityCases = append(parityCases, tokenCases...)
}
