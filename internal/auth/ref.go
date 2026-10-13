package auth

// PrincipalRef is how a principal appears in API responses (D-M2a-19). Build
// one with api.RefFor, which applies the masking for non-human viewers.
type PrincipalRef struct {
	Kind string `json:"kind" doc:"human, device, agent or system"`
	ID   string `json:"id" doc:"Principal key (kind:id); \"human\" for the operator as seen by an agent"`
	Name string `json:"name" doc:"Display name; \"human\" or \"device\" for agents"`
}
