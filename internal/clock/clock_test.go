package clock

import (
	"testing"
	"time"
)

func TestReal(t *testing.T) {
	t.Parallel()
	before := time.Now()
	if Real.Now().Before(before) {
		t.Fatal("Real.Now went backwards")
	}
	tm := Real.NewTimer(time.Millisecond)
	select {
	case <-tm.C():
	case <-time.After(5 * time.Second):
		t.Fatal("timer did not fire")
	}
	if tm.Stop() {
		t.Error("Stop on a fired timer reported true")
	}
	tm2 := Real.NewTimer(time.Hour)
	if !tm2.Stop() {
		t.Error("Stop on a pending timer reported false")
	}
}
