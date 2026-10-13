package notify

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recorder struct {
	got chan Message
	err error
}

func (r recorder) Notify(_ context.Context, m Message) error { r.got <- m; return r.err }

func TestNop(t *testing.T) {
	if err := (Nop{}).Notify(context.Background(), Message{Title: "t"}); err != nil {
		t.Fatal(err)
	}
}

func TestAsync_DeliversWithoutBlocking(t *testing.T) {
	rec := recorder{got: make(chan Message, 1)}
	n := Async(rec, time.Second)
	if err := n.Notify(context.Background(), Message{Title: "backup failed", Priority: 4}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-rec.got:
		if m.Title != "backup failed" || m.Priority != 4 {
			t.Fatalf("message = %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("message not delivered")
	}
}

func TestAsync_ErrorsAreSwallowed(t *testing.T) {
	rec := recorder{got: make(chan Message, 1), err: errors.New("dial tcp: refused")}
	if err := Async(rec, time.Second).Notify(context.Background(), Message{}); err != nil {
		t.Fatalf("Async must not return delivery errors: %v", err)
	}
	<-rec.got
}

func TestAsync_NilNotifier(t *testing.T) {
	if err := Async(nil, time.Second).Notify(context.Background(), Message{}); err != nil {
		t.Fatal(err)
	}
}
