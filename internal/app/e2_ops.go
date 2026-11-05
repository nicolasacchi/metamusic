package app

// buildE2Ops wires the collaborators of the engine operations (C160): lane E2-OP owns this file. It sets the trackctx
// EngineAdvisor (provenance.engine and the engine-aware next actions, C149.4) and anything else list_engines,
// get_provider_status and the `engines` CLI need from e. The freeze leaves it empty.
func buildE2Ops(in e2Inputs, e *e2Services) {}
