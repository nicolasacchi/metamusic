package clocktest

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func TestClocktest_TimersInOrder(t *testing.T) {
	t.Parallel()
	m := NewManual(t0)
	a := m.NewTimer(30 * time.Second)
	b := m.NewTimer(10 * time.Second)
	c := m.NewTimer(20 * time.Second)
	stopped := m.NewTimer(15 * time.Second)
	if !stopped.Stop() || stopped.Stop() {
		t.Fatal("Stop should report true once")
	}
	if m.Pending() != 3 {
		t.Fatalf("pending = %d, want 3", m.Pending())
	}
	m.Advance(25 * time.Second)
	if got := <-b.C(); !got.Equal(t0.Add(10 * time.Second)) {
		t.Errorf("b fired at %v", got)
	}
	if got := <-c.C(); !got.Equal(t0.Add(20 * time.Second)) {
		t.Errorf("c fired at %v", got)
	}
	select {
	case <-a.C():
		t.Error("a fired early")
	case <-stopped.C():
		t.Error("stopped timer fired")
	default:
	}
	if !m.Now().Equal(t0.Add(25 * time.Second)) {
		t.Errorf("now = %v", m.Now())
	}
	m.Set(t0.Add(time.Hour))
	if got := <-a.C(); !got.Equal(t0.Add(30 * time.Second)) {
		t.Errorf("a fired at %v", got)
	}
	if m.Pending() != 0 {
		t.Errorf("pending = %d", m.Pending())
	}
}

func TestManual_ZeroDurationFiresAtOnce(t *testing.T) {
	t.Parallel()
	m := NewManual(t0)
	tm := m.NewTimer(0)
	select {
	case got := <-tm.C():
		if !got.Equal(t0) {
			t.Errorf("fired at %v", got)
		}
	default:
		t.Fatal("zero-duration timer did not fire")
	}
}

func TestManual_SetBackwards(t *testing.T) {
	t.Parallel()
	m := NewManual(t0)
	m.Set(t0.Add(-time.Hour))
	if !m.Now().Equal(t0.Add(-time.Hour)) {
		t.Errorf("now = %v", m.Now())
	}
}
