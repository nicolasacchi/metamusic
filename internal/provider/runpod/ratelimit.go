package runpod

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
)

// window is one entry of a RateLimit structured-field list: `"minute";r=0;t=12`.
type window struct {
	Name      string
	Remaining int           // r; -1 when absent
	Reset     time.Duration // t; -1 when absent
}

// parseRateLimit reads `"minute";r=0;t=12, "hour";r=2800;t=1812`. Malformed items are skipped.
func parseRateLimit(v string) []window {
	var out []window
	for _, item := range strings.Split(v, ",") {
		parts := strings.Split(strings.TrimSpace(item), ";")
		name := strings.Trim(strings.TrimSpace(parts[0]), `"`)
		if name == "" {
			continue
		}
		w := window{Name: name, Remaining: -1, Reset: -1}
		for _, p := range parts[1:] {
			k, val, ok := strings.Cut(strings.TrimSpace(p), "=")
			if !ok {
				continue
			}
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil || n < 0 {
				continue
			}
			switch k {
			case "r":
				w.Remaining = n
			case "t":
				w.Reset = time.Duration(n) * time.Second
			}
		}
		out = append(out, w)
	}
	return out
}

// limiter makes requests to one host wait while any RateLimit window is exhausted.
type limiter struct {
	mu    sync.Mutex
	until time.Time
}

// observe records the exhausted windows of a response.
func (l *limiter) observe(now time.Time, h interface{ Get(string) string }) {
	var wait time.Duration
	for _, w := range parseRateLimit(h.Get("RateLimit")) {
		if w.Remaining == 0 && w.Reset > wait {
			wait = w.Reset
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

// wait blocks until the host may be called again or ctx ends.
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
