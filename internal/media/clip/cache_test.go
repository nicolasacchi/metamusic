package clip_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// seedCache writes n 1000-byte files with strictly increasing mtimes (oldest first)
// and returns their keys.
func seedCache(t testing.TB, e *env, n int) []string {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var keys []string
	for i := range n {
		key := fmt.Sprintf("%02x", i+1) + strings.Repeat("0", 62)
		p := e.abs("derived/clips/" + key[:2] + "/" + key + ".flac")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 1000), 0o444); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, base.Add(time.Duration(i)*time.Hour), base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	return keys
}

func exists(e *env, key string) bool {
	_, err := os.Stat(e.abs("derived/clips/" + key[:2] + "/" + key + ".flac"))
	return err == nil
}

func TestClipCache_EvictsOldestUnpinned(t *testing.T) {
	t.Parallel()
	pin := func(keys ...string) func(context.Context) (map[string]struct{}, error) {
		return func(context.Context) (map[string]struct{}, error) {
			m := map[string]struct{}{}
			for _, k := range keys {
				m[k] = struct{}{}
			}
			return m, nil
		}
	}
	t.Run("oldest first, pinned survive", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		keys := seedCache(t, e, 5) // 5000 bytes, cap 3000 => goal 2700
		s := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil,
			clip.Options{CacheBytes: 3000, Pinned: pin(keys[0]), Render: cliptest.Render})
		if _, err := s.Ensure(context.Background(), e.fakeMaster(t, "ev1")); err != nil {
			t.Fatal(err)
		}
		for i, want := range []bool{true, false, false, false, true} {
			if got := exists(e, keys[i]); got != want {
				t.Errorf("file %d exists = %v, want %v", i, got, want)
			}
		}
	})
	t.Run("under the cap nothing goes", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		keys := seedCache(t, e, 3)
		s := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil,
			clip.Options{CacheBytes: 3000, Render: cliptest.Render})
		if _, err := s.Ensure(context.Background(), e.fakeMaster(t, "ev2")); err != nil {
			t.Fatal(err)
		}
		for i, k := range keys {
			if !exists(e, k) {
				t.Errorf("file %d evicted below the cap", i)
			}
		}
	})
	t.Run("a Pinned error evicts nothing", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		keys := seedCache(t, e, 5)
		s := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil, clip.Options{CacheBytes: 3000, Render: cliptest.Render,
			Pinned: func(context.Context) (map[string]struct{}, error) { return nil, errors.New("db down") }})
		if _, err := s.Ensure(context.Background(), e.fakeMaster(t, "ev3")); err != nil {
			t.Fatal(err)
		}
		for i, k := range keys {
			if !exists(e, k) {
				t.Errorf("file %d evicted despite the Pinned error", i)
			}
		}
	})
	t.Run("everything pinned still renders", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		keys := seedCache(t, e, 5)
		s := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil,
			clip.Options{CacheBytes: 3000, Pinned: pin(keys...), Render: cliptest.Render})
		o, err := s.Ensure(context.Background(), e.fakeMaster(t, "ev4"))
		if err != nil {
			t.Fatal(err)
		}
		if !exists(e, o.Key) {
			t.Error("the new clip is missing")
		}
		for i, k := range keys {
			if !exists(e, k) {
				t.Errorf("pinned file %d evicted", i)
			}
		}
	})
}
