package tape_test

import (
	"errors"
	"runtime"
	"sync/atomic"
	"time"
)

func errorsAs(err error, target any) bool { return errors.As(err, target) }
func errorsIs(err, target error) bool     { return errors.Is(err, target) }

// sampleHeap runs f and returns the largest HeapAlloc seen while it ran (sampled every 20 ms).
func sampleHeap(f func()) uint64 {
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	f()
	close(stop)
	<-done
	return peak.Load()
}
