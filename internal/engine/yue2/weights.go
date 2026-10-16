package yue2

// Weights pins the Hugging Face revisions of the YuE2 weights (M3 C73.1, D-M3-18): the revisions the
// runner sends in `expect.weights`, `runpod provision` writes into the endpoint environment, and the
// worker-contract job loads. One committed file is the only source (TestWeights_ProvisionMatches).
//
// The values are the real Hugging Face commit shas (the `sha` field of https://huggingface.co/api/models/<repo>),
// audited 2026-10-15 against the pinned yue2 code commit (worker/contract/YUE2_COMMIT = 1647252d), the same
// day as the 3B revision. Changing one is a deliberate, reviewed edit: a worker whose weights differ from
// `expect.weights` answers WEIGHTS_MISMATCH, which fails closed (no GPU work).
var Weights = map[string]string{
	WeightsRepo3B:  "8f24312f187763b9854adeec87e437e2b03bbff1",
	WeightsRepoVAE: "152733a19ad43aa67e367f9b5503ef8075bb5126",
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
	// Env3BDir is the Hugging Face hub-cache root holding the cached 3B model (RunPod mounts it at
	// /runpod-volume/huggingface-cache/hub); the worker resolves models--m-a-p--YuE2-3B/snapshots/<rev> under it.
	Env3BDir = "MM_YUE2_3B_DIR"
)

// WeightsEnv is the endpoint environment derived from Weights.
func WeightsEnv() map[string]string {
	return map[string]string{
		Env3BRevision:  Weights[WeightsRepo3B],
		EnvVAERevision: Weights[WeightsRepoVAE],
	}
}
