package jobs

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

func TestAttemptOverhead_PodReservesNothing(t *testing.T) {
	t.Parallel()
	if got := attemptOverhead(provider.BackendRunPodPod, 0, 1, 690000); got != 0 {
		t.Errorf("pod overhead %d, want 0", got)
	}
	if got := attemptOverhead(provider.BackendFakePod, 0, 1, 690000); got != 0 {
		t.Errorf("fake pod overhead %d, want 0", got)
	}
	if got, want := attemptOverhead(provider.BackendFake, 200, 1, 690000), money.ForSeconds(201, 690000); got != want {
		t.Errorf("serverless overhead %d, want %d", got, want)
	}
}
