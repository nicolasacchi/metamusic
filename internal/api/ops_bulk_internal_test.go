package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
)

// TestEstimateFrom_PodBlock: a pod estimate answers {pod: {boot_s, deadline_s}}; every other estimate answers no pod key at all, so its
// body is the one it always was.
func TestEstimateFrom_PodBlock(t *testing.T) {
	t.Parallel()
	plain := estimateFrom(generation.Estimate{Engine: "yue2", Backend: "runpod-serverless"})
	b, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"pod"`) {
		t.Errorf("a serverless estimate carries a pod key: %s", b)
	}
	podE := estimateFrom(generation.Estimate{Engine: "yue2", Backend: "runpod-pod", Pod: &generation.PodBlock{BootS: 1200, DeadlineS: 18552}})
	if podE.Pod == nil || podE.Pod.BootS != 1200 || podE.Pod.DeadlineS != 18552 {
		t.Fatalf("pod block %+v", podE.Pod)
	}
	b, _ = json.Marshal(podE)
	if !strings.Contains(string(b), `"pod":{"boot_s":1200,"deadline_s":18552}`) {
		t.Errorf("body %s", b)
	}
}
