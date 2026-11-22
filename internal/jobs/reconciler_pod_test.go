package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods/podtest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestPodBackend_M3ReconcilerStepsNoOp: the M3 reconciler over the pod backend refreshes the quote and sweeps, but its worker-based trips
// never fire (an empty sweep opens no trip), it creates and deletes nothing at RunPod, and it bills no pod it does not own (C396.4).
func TestPodBackend_M3ReconcilerStepsNoOp(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	dir, key := t.TempDir(), "synthetic-"+strings.Repeat("k", 24)
	if err := os.WriteFile(filepath.Join(dir, "runpod-pod.env"), []byte("RUNPOD_"+"POD_KEY="+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := podtest.New(t, podtest.Options{Key: key})
	srv.SetPrice("NVIDIA GeForce RTX 4090", "0.34")
	be := pods.NewBackend(pods.BackendOptions{
		API:   pods.NewClient(pods.Options{BaseURL: srv.URL, KeyDir: dir, UserAgent: pods.UserAgentFor("test")}),
		Store: r.st, Clock: r.clk, Image: "ghcr.io/example/mm-worker@sha256:" + strings.Repeat("a", 64), GPUTypes: []string{"NVIDIA GeForce RTX 4090"},
		MaxRuntime: 6 * time.Hour, KillSlackS: 15, KeyPresent: func() bool { return true }, BillingReadable: func(context.Context) bool { return true },
	})
	r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at) VALUES('runpod-pod', 1, 'system:default', ?)`, store.FormatTime(t0))
	rec := r.recon(func(o *ReconcilerOptions) {
		o.Backend, o.Scoped, o.ColdStartS, o.MaxRuntime = be, true, 0, 6*time.Hour
		o.GateCfg = provider.GateConfig{Mode: provider.ModeRunPod, Backend: provider.BackendRunPodPod, KeysLoaded: true, MaxRuntime: 6 * time.Hour}
	})
	for range 4 {
		r.advance(time.Minute)
		r.mustPass(rec)
	}
	if got := r.count(`SELECT COUNT(*) FROM provider_trips WHERE backend = 'runpod-pod'`); got != 0 {
		t.Errorf("a pod backend with no pods opened %d trips (%s)", got, r.str(`SELECT group_concat(reason) FROM provider_trips`))
	}
	for _, op := range []string{podtest.OpCreate, podtest.OpDelete, podtest.OpCancelQueued} {
		if n := srv.Calls(op); n != 0 {
			t.Errorf("the reconciler called %s %d times", op, n)
		}
	}
	if srv.Calls(podtest.OpBilling) != 0 {
		t.Error("with no pod of ours there is nothing to bill")
	}
	if got := r.count(`SELECT COUNT(*) FROM provider_state WHERE backend = 'runpod-pod' AND quote_json IS NOT NULL`); got != 1 {
		t.Fatalf("the quote was not cached (%d)", got)
	}
	st, err := provider.NewGate(r.st.R, provider.GateConfig{Mode: provider.ModeRunPod, Backend: provider.BackendRunPodPod, KeysLoaded: true, MaxRuntime: 6 * time.Hour}, r.clk.Now).State(context.Background())
	if err != nil || !st.Enabled || st.Tripped {
		t.Errorf("the pod gate after the passes: %+v, %v", st, err)
	}
}
