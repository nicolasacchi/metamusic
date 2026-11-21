package pods

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
)

// rateWindow is one entry of a RateLimit structured-field list: `"minute";r=0;t=12`.
type rateWindow struct {
	remaining int           // r; -1 when absent
	reset     time.Duration // t; -1 when absent
}

// parseRateLimit reads `"minute";r=0;t=12, "hour";r=2800;t=1812`; malformed items are skipped.
func parseRateLimit(v string) []rateWindow {
	var out []rateWindow
	for _, item := range strings.Split(v, ",") {
		parts := strings.Split(strings.TrimSpace(item), ";")
		if strings.Trim(strings.TrimSpace(parts[0]), `"`) == "" {
			continue
		}
		w := rateWindow{remaining: -1, reset: -1}
		for _, p := range parts[1:] {
			k, val, ok := strings.Cut(strings.TrimSpace(p), "=")
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if !ok || err != nil || n < 0 {
				continue
			}
			switch k {
			case "r":
				w.remaining = n
			case "t":
				w.reset = time.Duration(n) * time.Second
			}
		}
		out = append(out, w)
	}
	return out
}

// limiter makes calls to the management host wait while any RateLimit window is exhausted.
type limiter struct {
	mu    sync.Mutex
	until time.Time
}

func (l *limiter) observe(now time.Time, header string) {
	var wait time.Duration
	for _, w := range parseRateLimit(header) {
		if w.remaining == 0 && w.reset > wait {
			wait = w.reset
		}
	}
	if wait <= 0 {
		return
	}
	l.mu.Lock()
	if t := now.Add(wait); t.After(l.until) {
		l.until = t
	}
	l.mu.Unlock()
}

func (l *limiter) wait(ctx context.Context, c clock.Clock) error {
	for {
		l.mu.Lock()
		d := l.until.Sub(c.Now())
		l.mu.Unlock()
		if d <= 0 {
			return nil
		}
		if err := sleep(ctx, c, d); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, c clock.Clock, d time.Duration) error {
	t := c.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
