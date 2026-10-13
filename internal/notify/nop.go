package notify

import "context"

// Nop discards every message (MM_NTFY_URL unset).
type Nop struct{}

// Notify implements Notifier.
func (Nop) Notify(context.Context, Message) error { return nil }
