package pods_test

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

func spec(id string) provider.TakeSpec { return provider.TakeSpec{AttemptID: id, TimeoutS: 100} }

func TestQueue_OrderFitAndDrop(t *testing.T) {
	t.Parallel()
	q := pods.NewQueue()
	if _, res := q.Take("b1", nil); res != pods.TakeEmpty {
		t.Fatal(res)
	}
	q.Add("b1", spec("a1"))
	q.Add("b1", spec("a2"))
	q.Add("b2", spec("a3"))
	if q.Len("b1") != 2 || q.Len("b2") != 1 || q.Len("b3") != 0 {
		t.Fatal("len")
	}
	if _, res := q.Take("b1", func(provider.TakeSpec) bool { return false }); res != pods.TakeUnfit || q.Len("b1") != 2 {
		t.Fatal("an unfit take left the queue")
	}
	if s, res := q.Take("b1", nil); res != pods.TakeOK || s.AttemptID != "a1" {
		t.Fatalf("%v %v", s.AttemptID, res)
	}
	q.Requeue("b1", spec("a1"))
	if s, _ := q.Take("b1", func(provider.TakeSpec) bool { return true }); s.AttemptID != "a1" {
		t.Fatal("requeue is not at the front")
	}
	if !q.Drop("a2") || q.Drop("a2") || q.Drop("nope") || q.Len("b1") != 0 {
		t.Fatal("drop")
	}
	q.Add("b1", spec("a4"))
	q.Add("b1", spec("a5"))
	if !q.Drop("a4") || q.Len("b1") != 1 { // from the middle of a longer list
		t.Fatal("drop head")
	}
	ids := q.Purge()
	if len(ids) != 2 || q.Len("b1") != 0 || q.Len("b2") != 0 {
		t.Fatalf("purge %v", ids)
	}
}

func TestQueue_ChangedAndCancel(t *testing.T) {
	t.Parallel()
	q := pods.NewQueue()
	// in this order: a Drop signals only when the take is queued, so a random map order made this test fail one run in a few
	for _, step := range []struct {
		name string
		act  func()
	}{
		{"add", func() { q.Add("b", spec("a")) }}, {"requeue", func() { q.Requeue("b", spec("a")) }},
		{"drop", func() { q.Drop("a") }}, {"purge", func() { q.Purge() }}, {"cancel", func() { q.RequestCancel("a") }},
	} {
		name, act := step.name, step.act
		ch := q.Changed()
		select {
		case <-ch:
			t.Fatalf("%s: closed before the change", name)
		default:
		}
		act()
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("%s: not signalled", name)
		}
	}
	if !q.CancelRequested("a") || q.CancelRequested("z") {
		t.Fatal("cancel set")
	}
}
