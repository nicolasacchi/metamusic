package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

var scoreOwner = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleOwner, ID: "op", Name: "op", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}

func scoreRequest() transform.Request {
	return transform.Request{Kind: transform.KindScoreRender, Members: map[transform.Kind]json.RawMessage{
		transform.KindScoreRender: json.RawMessage(`{"score":{"abc":"X:1\nT:Synthetic\nM:4/4\nL:1/4\nQ:1/4=120\nK:C\nCDEF|"}}`)}}
}

// t1bRig is t1Rig with a configurable config (soundfonts, fluidsynth path).
func t1bRig(t *testing.T, mod func(*config.Config)) *transform.Service {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	data, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	cfg := config.Config{TransformEnabled: true, FFmpeg: "ffmpeg", FLAC: "flac", Metaflac: "metaflac", DataDir: dir, TransformQueueMax: 4,
		TransformMaxCPUS: 600, TransformMaxOutS: 900, TransformMaxMemMB: 1024, TransformAgentDailyCPUS: 1800, TransformHumanDailyCPUS: 14400,
		FluidsynthPath: filepath.Join(dir, "no-such-fluidsynth")}
	if mod != nil {
		mod(&cfg)
	}
	return buildT1(t1Inputs{cfg: cfg, st: st, lib: library.New(st, data, time.Now), data: data, now: time.Now, newID: func() string { return "id" }})
}

// TestWireScore_GateFollowsTheHost: with no soundfont and no fluidsynth (the image today) score_render is unavailable at estimate
// and create, never a failure after a queue slot is spent; a soundfont alone is not enough, and with both the kind is listed.
func TestWireScore_GateFollowsTheHost(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sf := filepath.Join(dir, "gm.sf2")
	if err := os.WriteFile(sf, []byte("synthetic soundfont"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "fluidsynth")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'FluidSynth runtime version 9.9.9'\n"), 0o755); err != nil { //nolint:gosec // a test script
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		mod       func(*config.Config)
		component string // "" = available
	}{
		"nothing":   {nil, "soundfont"},
		"font only": {func(c *config.Config) { c.Soundfonts = []config.Soundfont{{Name: "gm", Path: sf}} }, "fluidsynth"},
		"tool only": {func(c *config.Config) { c.FluidsynthPath = fake }, "soundfont"},
		"unreadable font": {func(c *config.Config) {
			c.FluidsynthPath = fake
			c.Soundfonts = []config.Soundfont{{Name: "gm", Path: "/nonexistent/gm.sf2"}}
		}, "soundfont"},
		"font and fluidsynth": {func(c *config.Config) {
			c.FluidsynthPath = fake
			c.Soundfonts = []config.Soundfont{{Name: "gm", Path: sf}}
		}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			svc := t1bRig(t, tc.mod)
			_, err := svc.Estimate(context.Background(), scoreOwner, scoreRequest())
			caps, cerr := svc.Capabilities(context.Background(), scoreOwner)
			if cerr != nil {
				t.Fatal(cerr)
			}
			listed := false
			for _, k := range caps.Kinds {
				listed = listed || k.Kind == transform.KindScoreRender
			}
			if tc.component != "" {
				if err == nil || !strings.Contains(err.Error(), tc.component) {
					t.Errorf("estimate: %v, want 503 %s", err, tc.component)
				}
				return
			}
			if err != nil {
				t.Fatalf("estimate: %v", err)
			}
			if !listed || !caps.Tools.Fluidsynth || caps.Tools.FluidsynthVersion != "FluidSynth runtime version 9.9.9" ||
				len(caps.Soundfonts) != 1 || len(caps.Soundfonts[0].SHA12) != 12 {
				t.Errorf("capabilities: listed %v tools %+v fonts %+v", listed, caps.Tools, caps.Soundfonts)
			}
		})
	}
}
