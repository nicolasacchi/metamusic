package runpod_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

// TestPodMinCUDA_EqualsServerless pins the pod create body's gpu.minCudaVersion (C392.2) to the serverless endpoint's desired
// spec: a pod must run on the CUDA generation the image was verified on. The pods package cannot import this one, so the
// comparison lives here.
func TestPodMinCUDA_EqualsServerless(t *testing.T) {
	t.Parallel()
	for _, workers := range []int{0, 1, 2} {
		if got := desired(t, desiredCfg(), workers).GPU.MinCudaVersion; got != pods.MinCudaVersion {
			t.Errorf("workers %d: serverless minCudaVersion %q, pods %q", workers, got, pods.MinCudaVersion)
		}
	}
}
