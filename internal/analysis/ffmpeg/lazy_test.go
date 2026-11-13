package ffmpeg

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
)

func TestLazy_NotReadyAnswersToolMissing(t *testing.T) {
	t.Parallel()
	l := NewLazy()
	if l.Ready() {
		t.Fatal("a new Lazy must not be ready")
	}
	for name, err := range map[string]error{"clips": l.ClipsErr(), "tier_a": l.TierAErr()} {
		if !errors.Is(err, analysis.ErrToolMissing) {
			t.Fatalf("%s: want ErrToolMissing, got %v", name, err)
		}
	}
	if _, err := l.Loudness(context.Background(), "/x"); !errors.Is(err, analysis.ErrToolMissing) {
		t.Fatalf("Loudness before ready: %v", err)
	}
	if _, err := l.CountFrames(context.Background(), "/x", 44100, 2); !errors.Is(err, analysis.ErrToolMissing) {
		t.Fatalf("CountFrames before ready: %v", err)
	}
	select {
	case <-l.ReadyCh():
		t.Fatal("ReadyCh closed before ready")
	default:
	}
}

func TestLazy_SetEnables(t *testing.T) {
	t.Parallel()
	l := NewLazy()
	var woke int
	l.OnReady(func() { woke++ })
	l.Set(&FFmpeg{version: "7.1"})
	l.Set(&FFmpeg{version: "7.2"}) // a second Set is ignored: the first probe wins
	if !l.Ready() || l.ToolVersion() != "ffmpeg 7.1" || woke != 1 {
		t.Fatalf("ready=%v version=%q woke=%d", l.Ready(), l.ToolVersion(), woke)
	}
	select {
	case <-l.ReadyCh():
	default:
		t.Fatal("ReadyCh must be closed once ready")
	}
	// the stub has empty listings, so the capability checks report the first missing item, not the pending error
	if err := l.ClipsErr(); err == nil {
		t.Fatal("empty listings must lack clip filters")
	}
}

func TestRetry_BacksOffUntilSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLazy()
		var mu sync.Mutex
		var at []time.Duration
		start := time.Now()
		probe := func(context.Context) (*FFmpeg, error) {
			mu.Lock()
			defer mu.Unlock()
			at = append(at, time.Since(start))
			if len(at) < 10 {
				return nil, errors.New("timeout")
			}
			return &FFmpeg{version: "7"}, nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); l.Retry(ctx, probe, RetryOptions{}) }()
		synctest.Wait()
		<-done
		if !l.Ready() {
			t.Fatal("Retry must end ready")
		}
		// gaps 5s, 10s, 20s, 40s, 80s, 160s, then capped at 300s
		want := []time.Duration{5, 15, 35, 75, 155, 315, 615, 915, 1215, 1515}
		for i := range want {
			want[i] *= time.Second
		}
		if len(at) != len(want) {
			t.Fatalf("attempts %v", at)
		}
		for i := range want {
			if at[i] != want[i] {
				t.Fatalf("attempt %d at %v, want %v (all %v)", i, at[i], want[i], at)
			}
		}
	})
}

func TestRetry_StopsOnContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLazy()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			l.Retry(ctx, func(context.Context) (*FFmpeg, error) { return nil, errors.New("down") }, RetryOptions{})
		}()
		time.Sleep(time.Minute)
		cancel()
		<-done
		if l.Ready() {
			t.Fatal("must not be ready")
		}
	})
}
