package app_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func mediaEnv(t *testing.T) (config.Config, *store.Store, *os.Root, *library.Service) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir, FFmpeg: filepath.Join(dir, "no-such-ffmpeg")}
	return cfg, st, root, library.New(st, root, time.Now)
}

func TestNewMedia_InjectedFakes(t *testing.T) {
	t.Parallel()
	cfg, st, root, lib := mediaEnv(t)
	m, err := app.NewMedia(context.Background(), cfg, st, root, lib, nil, nil, nil,
		&app.MediaOptions{Window: cliptest.NewAnalyzer(), Render: cliptest.Render, TierA: earstest.NewTierA()})
	if err != nil {
		t.Fatal(err)
	}
	if m.Clips == nil || m.Ears == nil || m.ClipsMissing != nil || m.TierAMissing != nil {
		t.Errorf("media = %+v", m)
	}
}

// A missing clip capability leaves Clips nil but keeps Ears (its read-only operations work from the database);
// a missing Tier A capability is carried by Ears.
func TestNewMedia_MissingCapabilities(t *testing.T) {
	t.Parallel()
	cfg, st, root, lib := mediaEnv(t)
	missing := errors.New("analysis: required tool capability missing: filter atrim")
	m, err := app.NewMedia(context.Background(), cfg, st, root, lib, nil, nil, nil,
		&app.MediaOptions{ClipsMissing: missing, Window: cliptest.NewAnalyzer(), TierA: earstest.NewTierA()})
	if err != nil {
		t.Fatal(err)
	}
	if m.Clips != nil || m.Ears == nil || !errors.Is(m.ClipsMissing, missing) {
		t.Errorf("clips missing: %+v", m)
	}
	m, err = app.NewMedia(context.Background(), cfg, st, root, lib, nil, nil, nil,
		&app.MediaOptions{TierAMissing: missing, Window: cliptest.NewAnalyzer()})
	if err != nil {
		t.Fatal(err)
	}
	if m.Clips == nil || m.Ears == nil || !errors.Is(m.TierAMissing, missing) {
		t.Errorf("tier A missing: %+v", m)
	}
}

// Probing a binary that does not exist is not an error: import and serve still start (D-M2b-10).
func TestNewMedia_ProbeFailureIsNotAnError(t *testing.T) {
	t.Parallel()
	cfg, st, root, lib := mediaEnv(t)
	m, err := app.NewMedia(context.Background(), cfg, st, root, lib, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewMedia: %v", err)
	}
	if m.Clips != nil || m.Ears == nil {
		t.Errorf("services = clips %v ears %v", m.Clips, m.Ears)
	}
	for name, e := range map[string]error{"clips": m.ClipsMissing, "tier A": m.TierAMissing} {
		if !errors.Is(e, analysis.ErrToolMissing) {
			t.Errorf("%s missing = %v, want ErrToolMissing", name, e)
		}
	}
}

// fakeFFmpeg answers the capability probes with the named filters and encoders.
func fakeFFmpeg(t *testing.T, filters, encoders []string) string {
	t.Helper()
	list := func(names []string) string {
		var b strings.Builder
		for _, n := range names {
			b.WriteString(" T.. " + n + "  A->A  synthetic\n")
		}
		return b.String()
	}
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in\n" +
		"-version) echo 'ffmpeg version n9.0.1 Copyright'; exit 0;;\n" +
		"-filters) cat <<'EOF'\n" + list(filters) + "EOF\nexit 0;;\n" +
		"-encoders) cat <<'EOF'\n" + list(encoders) + "EOF\nexit 0;;\nesac; done\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}
	return bin
}

// The capability split of C51.7: M1's requirements only ⇒ neither clips nor Tier A; everything ⇒ both.
// Serial: it writes and execs fake ffmpeg scripts (a parallel fork would make the exec fail with "text file busy").
func TestNewMedia_CapabilitySplit(t *testing.T) {
	cfg, st, root, lib := mediaEnv(t)
	m1 := []string{"ebur128"}
	all := []string{"ebur128", "atrim", "asetpts", "afade", "aresample", "volume", "ametadata", "asetnsamples", "astats", "silencedetect", "aformat", "showspectrumpic"}

	cfg.FFmpeg = fakeFFmpeg(t, m1, []string{"libmp3lame"})
	m, err := app.NewMedia(context.Background(), cfg, st, root, lib, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Clips != nil || m.ClipsMissing == nil || m.TierAMissing == nil || m.Ears == nil {
		t.Errorf("M1-only ffmpeg: %+v", m)
	}

	cfg.FFmpeg = fakeFFmpeg(t, all, []string{"libmp3lame", "flac", "png"})
	if m, err = app.NewMedia(context.Background(), cfg, st, root, lib, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if m.Clips == nil || m.ClipsMissing != nil || m.TierAMissing != nil {
		t.Errorf("complete ffmpeg: %+v", m)
	}
}

func TestApp_BuildWiresM2b(t *testing.T) {
	t.Parallel()
	var got duels.ClipRenderer
	called := false
	s, _ := build(t, app.Options{
		Media:     &app.MediaOptions{Window: cliptest.NewAnalyzer(), Render: cliptest.Render, TierA: earstest.NewTierA()},
		DuelClips: func(r duels.ClipRenderer) duels.ClipRenderer { got, called = r, true; return r },
	})
	if s.Duels == nil || s.Ears == nil || s.Clips == nil {
		t.Fatalf("services = %v %v %v", s.Duels, s.Ears, s.Clips)
	}
	if s.Deps.Duels != s.Duels || s.Deps.Ears != s.Ears || s.Deps.Clips != s.Clips ||
		s.Server.Duels != s.Duels || s.Server.Ears != s.Ears || s.Server.Clips != s.Clips {
		t.Error("Deps and Server options must carry the same M2b services")
	}
	if !called || got == nil {
		t.Errorf("DuelClips hook: called %v, renderer %v", called, got)
	}
}

// With clips unavailable, duels get no renderer at all: a nil interface, never a typed-nil *clip.Service.
func TestApp_BuildWithoutClipsPassesNilRenderer(t *testing.T) {
	t.Parallel()
	var got duels.ClipRenderer
	called := false
	s, _ := build(t, app.Options{
		Media:     &app.MediaOptions{ClipsMissing: errors.New("no clips"), TierA: earstest.NewTierA()},
		DuelClips: func(r duels.ClipRenderer) duels.ClipRenderer { got, called = r, true; return r },
	})
	if !called || got != nil {
		t.Errorf("hook called %v with renderer %#v, want a nil interface", called, got)
	}
	if s.Clips != nil || s.Deps.Clips != nil || s.Duels == nil || s.Ears == nil {
		t.Errorf("services = clips %v duels %v ears %v", s.Clips, s.Duels, s.Ears)
	}
}

// The media chain ends with the clips resolver: an unknown key is a 404 problem, not a 500.
func TestApp_MediaChainIncludesClips(t *testing.T) {
	t.Parallel()
	s, _ := build(t, app.Options{Media: &app.MediaOptions{Window: cliptest.NewAnalyzer(), Render: cliptest.Render, TierA: earstest.NewTierA()}})
	sha := strings.Repeat("ab", 32)
	for _, ext := range []string{"flac", "jpg"} {
		u := s.Deps.Signer.Sign(sha, ext, time.Hour, time.Minute).URL
		w := httptest.NewRecorder()
		s.Server.Media.ServeHTTP(w, httptest.NewRequest(http.MethodGet, u, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s, want 404", ext, w.Code, w.Body)
		}
	}
}
