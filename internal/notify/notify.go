// Package notify publishes minimal push notifications (C37).
package notify

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Message is one notification.
type Message struct {
	Title, Body string
	Priority    int // 1..5
	Tags        []string
	Click       string
}

// Notifier publishes messages.
type Notifier interface {
	Notify(ctx context.Context, m Message) error
}

// NewNtfy returns an ntfy publisher for MM_NTFY_URL (<server base>/<topic>).
func NewNtfy(url, token string, client *http.Client) Notifier {
	// STUB(M2a-B9)
	return ntfyStub{}
}

type ntfyStub struct{}

func (ntfyStub) Notify(context.Context, Message) error { return errors.ErrUnsupported }

// Async wraps n so Notify returns at once; the publish runs with timeout and
// failures are logged through redact.
func Async(n Notifier, timeout time.Duration) Notifier { return async{n: n, timeout: timeout} }

type async struct {
	n       Notifier
	timeout time.Duration
}

func (a async) Notify(ctx context.Context, m Message) error {
	if a.n == nil {
		return nil
	}
	bg := context.WithoutCancel(ctx)
	go func() {
		c, cancel := context.WithTimeout(bg, a.timeout)
		defer cancel()
		if err := a.n.Notify(c, m); err != nil {
			slog.Warn("notification failed", "err", redact.Error(err))
		}
	}()
	return nil
}
