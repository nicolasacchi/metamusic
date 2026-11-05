package providertest

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// E2 (C151.1, C162.2): the Backend methods the Set routes by, the ACE-Step scripted backend and a scripted
// ACE-Step output builder. The scripted backend serves the engine its name implies with fixed values.

// NewACEStep builds a scripted backend named provider.BackendFakeACEStep (engine acestep15).
func NewACEStep(s Script) *Backend { return New(provider.BackendFakeACEStep, s) }

// ColdStartS is a fixed 180 s (MM_COLD_START_S default).
func (b *Backend) ColdStartS() int { return 180 }

// Engine is the engine this backend renders: acestep15 for the two ACE-Step backend names, yue2 otherwise.
func (b *Backend) Engine() string {
	switch b.name {
	case provider.BackendFakeACEStep, provider.BackendRunPodACEStep:
		return provider.EngineACEStep
	}
	return provider.EngineYuE2
}

// MaxRuntime is a fixed 15 minutes (MM_JOB_MAX_RUNTIME default).
func (b *Backend) MaxRuntime() time.Duration { return 15 * time.Minute }

// ACEStepOutput is the scripted output of a successful ACE-Step run: the schema-valid mm.worker/1 object with
// truncated null, env.engine acestep15, the given weights and one digest per artifact name (a digest is the
// name's 64-digit hex placeholder with a size of 1). It validates against mm-worker-1.acestep15.output.schema.json.
func ACEStepOutput(takeID, attemptID string, audioSeconds float64, weights map[string]string, artifacts ...string) json.RawMessage {
	arts := map[string]provider.ArtifactDigest{}
	for i, n := range artifacts {
		if n == provider.ResultArtifact {
			continue // uploaded last, never listed
		}
		d := strconv.FormatInt(int64(i)+1, 16)
		sum := make([]byte, 64)
		for j := range sum {
			sum[j] = d[0]
		}
		arts[n] = provider.ArtifactDigest{SHA256: string(sum), Bytes: 1}
	}
	secs := json.Number(strconv.FormatFloat(audioSeconds, 'f', -1, 64))
	num := func(v string) *json.Number { n := json.Number(v); return &n }
	raw, _ := json.Marshal(provider.Output{
		Contract: provider.Contract, TakeID: takeID, AttemptID: attemptID, Status: "ok", Artifacts: arts, AudioSeconds: &secs,
		Timings: map[string]*json.Number{"load_s": nil, "generate_s": num("1.000"), "save_s": num("0.100"), "upload_s": num("0.100")},
		Env: provider.OutputEnv{Engine: provider.EngineACEStep, PkgVersion: "0.1.6", CodeCommit: "0000000000000000000000000000000000000fa1",
			Weights: weights, Decoder: "ACE-Step/Ace-Step1.5:vae", GPUName: "NVIDIA RTX A5000 (simulated)", CUDA: "12.8", Torch: "2.10.0",
			WorkerBuild: "0000000000000000000000000000000000000fa1", Python: "3.12.12"},
	})
	return raw
}
