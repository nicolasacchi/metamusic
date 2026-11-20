package app_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/config"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Hooks run in order with the finished Services, may add loops and Deps fields, and run before Server is built.
func TestBuild_HooksRunInOrderBeforeServer(t *testing.T) {
	t.Parallel()
	var order []string
	s, _ := build(t, app.Options{Hooks: []app.BuildHook{
		func(e *app.BuildEnv) error {
			order = append(order, "a")
			if e.Services.Deps.Library == nil || e.Services.Server.Media != nil {
				t.Error("hook must see Deps filled and Server not yet built")
			}
			e.AddLoop(app.Loop{Name: "one", Run: func(context.Context) {}})
			return nil
		},
		func(e *app.BuildEnv) error {
			order = append(order, "b")
			e.AddLoop(app.Loop{Name: "two", Run: func(context.Context) {}})
			return nil
		},
	}})
	if strings.Join(order, "") != "ab" {
		t.Errorf("hook order %v", order)
	}
	if len(s.Loops) != 2 || s.Loops[0].Name != "one" || s.Loops[1].Name != "two" {
		t.Errorf("Loops = %+v", s.Loops)
	}
	if s.Server.Deps != &s.Deps {
		t.Error("Server.Deps must point at Services.Deps")
	}
}

func TestBuild_HookErrorAbortsBuild(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir}
	boom := errors.New("boom")
	_, err = app.Build(context.Background(), cfg, config.Secrets{Key: key}, st, root,
		app.Options{Hooks: []app.BuildHook{func(*app.BuildEnv) error { return boom }}})
	if !errors.Is(err, boom) {
		t.Fatalf("Build error = %v, want the hook's error", err)
	}
}

// A resolver registered by a hook is chained after the built-in ones: the media handler asks it for an address nothing else knows.
func TestBuild_HookResolverReachesMediaHandler(t *testing.T) {
	t.Parallel()
	var got atomic.Value
	s, _ := build(t, app.Options{Hooks: []app.BuildHook{func(e *app.BuildEnv) error {
		e.AddResolver(resolverFunc(func(sha, ext string) { got.Store(sha + "." + ext) }))
		return nil
	}}})
	sha := strings.Repeat("ab", 32)
	link := s.Deps.Signer.Sign(sha, "flac", time.Hour, time.Minute)
	w := httptest.NewRecorder()
	s.Server.Media.ServeHTTP(w, httptest.NewRequest(http.MethodGet, link.URL, nil))
	if got.Load() != sha+".flac" || w.Code != http.StatusNotFound {
		t.Fatalf("resolver saw %v, status %d; want the hook resolver asked and a 404 from the chain", got.Load(), w.Code)
	}
}

type resolverFunc func(sha, ext string)

func (f resolverFunc) Resolve(_ context.Context, sha, ext string) (mediaserve.Object, error) {
	f(sha, ext)
	return mediaserve.Object{}, mediaserve.ErrNotFound
}

func TestServe_StartsAndDrainsLoops(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var started, stopped atomic.Int32
		loops := []app.Loop{
			{Name: "a", Run: func(ctx context.Context) { started.Add(1); <-ctx.Done(); stopped.Add(1) }},
			{Name: "b", Run: func(ctx context.Context) { started.Add(1); <-ctx.Done(); stopped.Add(1) }},
		}
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		app.StartLoops(ctx, loops, &wg)
		synctest.Wait()
		if started.Load() != 2 || stopped.Load() != 0 {
			t.Fatalf("started %d stopped %d", started.Load(), stopped.Load())
		}
		cancel()
		wg.Wait() // drained: both loops returned
		if stopped.Load() != 2 {
			t.Fatalf("stopped %d", stopped.Load())
		}
	})
}

func TestServe_LoopPanicRestarts(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int32
		loops := []app.Loop{{Name: "p", Run: func(ctx context.Context) {
			if runs.Add(1) == 1 {
				panic("token=SECRETVALUE failed")
			}
			<-ctx.Done()
		}}}
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		app.StartLoops(ctx, loops, &wg)
		synctest.Wait()
		if runs.Load() != 1 {
			t.Fatalf("runs %d after the panic, want 1 (restart waits a minute)", runs.Load())
		}
		time.Sleep(59 * time.Second)
		synctest.Wait()
		if runs.Load() != 1 {
			t.Fatalf("restarted early: %d", runs.Load())
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if runs.Load() != 2 {
			t.Fatalf("runs %d, want 2 after a minute", runs.Load())
		}
		cancel()
		wg.Wait()
	})
}
