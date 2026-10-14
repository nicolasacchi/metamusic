package jobs_test

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/jobs"
)

func TestHub_NotifyReachesSubscribers(t *testing.T) {
	t.Parallel()
	h := jobs.NewHub()
	a, cancelA := h.Subscribe()
	b, cancelB := h.Subscribe()
	defer cancelB()
	h.Notify()
	h.Notify() // a pending nudge is not queued twice
	for name, ch := range map[string]<-chan struct{}{"a": a, "b": b} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("subscriber %s got no nudge", name)
		}
		select {
		case <-ch:
			t.Errorf("subscriber %s got a second nudge", name)
		default:
		}
	}
	cancelA()
	h.Notify() // a cancelled subscriber is gone and Notify never blocks
	select {
	case <-a:
		t.Error("cancelled subscriber was nudged")
	default:
	}
}
