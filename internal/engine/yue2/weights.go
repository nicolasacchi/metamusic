package yue2

// Weights pins the Hugging Face revisions of the YuE2 weights (M3 C73.1, D-M3-18): the revisions the
// runner sends in `expect.weights`, `runpod provision` writes into the endpoint environment, and the
// worker-contract job loads. One committed file is the only source (TestWeights_ProvisionMatches).
//
// The values below are PLACEHOLDERS (40 hex digits, clearly synthetic): the operator replaces them with
// the audited revisions in M3-X2, before the endpoint is provisioned. Until then a real worker would
// answer WEIGHTS_MISMATCH, which fails closed (no GPU work).
var Weights = map[string]string{
	WeightsRepo3B:  "0000000000000000000000000000000000000001",
	WeightsRepoVAE: "0000000000000000000000000000000000000002",
}

// Hugging Face repositories of the weights; WeightsRepoVAE is also the decoder of the env fingerprint.
const (
	WeightsRepo3B  = "m-a-p/YuE2-3B"
	WeightsRepoVAE = "m-a-p/YuE2-Vae"
)

// Environment variable names `runpod provision` sets on the endpoint (C83.6).
const (
	Env3BRevision  = "MM_YUE2_3B_REVISION"
	EnvVAERevision = "MM_YUE2_VAE_REVISION"
)

// WeightsEnv is the endpoint environment derived from Weights.
func WeightsEnv() map[string]string {
	return map[string]string{
		Env3BRevision:  Weights[WeightsRepo3B],
		EnvVAERevision: Weights[WeightsRepoVAE],
	}
}
