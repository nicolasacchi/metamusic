package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/startup"
)

// deriveStep is one startup derivation step. name and code are the stable identifiers /healthz reports; the error itself only
// reaches the (redacted) log.
type deriveStep struct {
	name, code string
	run        func(ctx context.Context) error
}

const (
	// startupRetryBase and startupRetryMax bound the backoff between attempts of a failed derivation step.
	startupRetryBase = 5 * time.Second
	startupRetryMax  = 5 * time.Minute
)

// startupSteps lists the derivation steps in order: the Sound taxonomy seed (the taxonomy exists before any Sound is
// computed), then the first Settle. Add L3 derivation steps here, never as a bare `return err` in serve.
func startupSteps(svc *app.Services) []deriveStep {
	return []deriveStep{
		{name: "sound_taxonomy_seed", code: "TAXONOMY_SEED_FAILED", run: func(ctx context.Context) error {
			if err := svc.Sounds.EnsureSeed(ctx); err != nil && !errors.Is(err, errors.ErrUnsupported) {
				return fmt.Errorf("sound taxonomy seed: %w", err)
			}
			return nil
		}},
		{name: "library_settle", code: "SETTLE_FAILED", run: func(ctx context.Context) error { return settleOnce(ctx, svc) }},
	}
}

// settleOnce runs the first Settle, waiting while another process holds the lease (a restarting server whose predecessor is
// still draining; that is not a failure).
func settleOnce(ctx context.Context, svc *app.Services) error {
	for {
		rep, err := svc.Provenance.Settle(ctx)
		switch {
		case err == nil:
			if rep.KeysProcessed > 0 {
				slog.Info("library model settled", "keys", rep.KeysProcessed, "songs_created", rep.SongsCreated, "muses_created", rep.MusesCreated, "sessions_created", rep.SessionsCreated)
			}
			return nil
		case errors.Is(err, provenance.ErrSettleBusy):
			slog.Info("waiting for the settle lease")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		default:
			return fmt.Errorf("startup settle: %w", err)
		}
	}
}

// runDerive runs steps in order. The first failing step is recorded in h (with its code) and the steps behind it are skipped,
// since they depend on it; every step that succeeds clears its own issue. It reports whether all steps succeeded. A step ended
// by ctx is neither a failure nor recorded.
func runDerive(ctx context.Context, steps []deriveStep, h *startup.Health) bool {
	for _, st := range steps {
		err := st.run(ctx)
		if err == nil {
			h.Clear(st.name)
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		h.Fail(st.name, st.code)
		slog.Error("startup step failed; serving degraded and retrying", "step", st.name, "code", st.code, "err", redact.Error(err))
		return false
	}
	return true
}

// retryDerive repeats runDerive with exponential backoff (base, doubling, capped at startupRetryMax) until it succeeds or ctx
// ends. base <= 0 means startupRetryBase.
func retryDerive(ctx context.Context, steps []deriveStep, h *startup.Health, base time.Duration) {
	if base <= 0 {
		base = startupRetryBase
	}
	wait := base
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if runDerive(ctx, steps, h) {
			slog.Info("startup derivation recovered")
			return
		}
		wait = min(wait*2, max(startupRetryMax, base))
	}
}
