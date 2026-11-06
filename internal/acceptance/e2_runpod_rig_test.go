package acceptance_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// lateFetcher lets the simulated worker be built before the rig's media route exists.
type lateFetcher struct {
	mu sync.Mutex
	f  fake.InputFetcher
}

func (l *lateFetcher) set(f fake.InputFetcher) { l.mu.Lock(); l.f = f; l.mu.Unlock() }

func (l *lateFetcher) Get(ctx context.Context, url string) (int, []byte, error) {
	l.mu.Lock()
	f := l.f
	l.mu.Unlock()
	if f == nil {
		return 404, nil, nil
	}
	return f.Get(ctx, url)
}

const aceCanaryEndpoint = "ep-ace-canary-1111"

// aceRunPodRig is a generation rig over the serverless backend of BOTH engines against one runpodtest server: the yue2 endpoint
// (ep-canary-0000) and the ACE-Step endpoint (aceCanaryEndpoint, the shared jobs key). The simulated worker uploads through the
// rig's own handlers and fetches inputs through its media route; the runner and reconciler are idle and driven with pump.
func aceRunPodRig(t *testing.T, tpl archivetest.Template, o servertest.Options) *provRig {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	testtools.Require(t, "ffmpeg")
	late, lf := &lateUploader{}, &lateFetcher{}
	clk := servertest.NewClock(librarytest.FixedNow)
	keys := runpodtest.KeySet{Jobs: "rp-jobs-" + strings.Repeat("a", 8), Read: "rp-read-" + strings.Repeat("b", 8), Write: "rp-write-" + strings.Repeat("c", 8)}
	sim := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: late, Fetcher: lf, Tools: testtools.Tools(), AudioSeconds: 3, AudioScale: 4})
	rp := runpodtest.New(t, runpodtest.Options{Keys: keys, Runner: runpodtest.FakeWorker(sim), Clock: clk, PollsToRun: 2})
	o.Archive, o.Clock, o.M2b.Media = &tpl, clk, true
	o.ACEStep = true
	o.AdoptEngines = append(o.AdoptEngines, acestep.Name)
	o.Backend, o.RunPod = "runpod", &servertest.RunPodOptions{Server: rp, Keys: keys, ACEStepEndpointID: aceCanaryEndpoint}
	r := servertest.New(t, o)
	hu := fake.HandlerUploader{UploadHandler: r.Services.Uploads.Handler(), StartHandler: r.Services.Uploads.StartHandler(), FetchHandler: r.Services.Server.Media}
	late.set(hu)
	lf.set(hu)
	// the first reconciler pass reads each backend's quote and endpoint; generation is refused while a quote is stale
	if _, err := r.Runner.Step(context.Background()); err != nil {
		t.Fatalf("runner step: %v", err)
	}
	if err := r.Reconciler.Pass(context.Background()); err != nil {
		t.Fatalf("reconciler pass: %v", err)
	}
	return &provRig{Rig: r, kind: "runpod", rp: rp, keys: keys}
}
