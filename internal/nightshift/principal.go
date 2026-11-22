package nightshift

import "github.com/nicolasacchi/metamusic/internal/auth"

// HolderKey is the mandate holder key and the night principal's Key() (C371.1, D-M6N-3).
const HolderKey = "agent:nightshift"

// Principal is the night shift's identity: an agent (estimates and batches allow only human|agent principals) that owns no
// token and no row in api_tokens, with the read and generate scopes only. It never approves anything: without a covering
// mandate its night is skipped.
func Principal() auth.Principal {
	return auth.Principal{
		Kind:   auth.KindAgent,
		ID:     "nightshift",
		Name:   "nightshift",
		Via:    "nightshift",
		Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate},
	}
}
