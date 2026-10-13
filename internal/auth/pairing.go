package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// PairingOptions configures the public pairing pages (C26). Bodies belong to M2a-B9.
type PairingOptions struct {
	Cfg      config.Config
	Devices  *DeviceStore
	Sessions SessionVerifier // local mode: a valid mm_session on /start goes straight to "/"
	Allow    func() (ok bool, retryAfter time.Duration)
	OnPaired func(ctx context.Context, d DeviceInfo)
	// FailDelay defaults to one second.
	FailDelay time.Duration
}

// PairingHandler serves /start, /pair, /pair.js and /auth/pair.
func PairingHandler(o PairingOptions) http.Handler {
	// STUB(M2a-B9)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "pairing is not implemented yet", nil)
	})
}
