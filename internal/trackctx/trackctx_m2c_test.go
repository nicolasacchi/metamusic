package trackctx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// newEnvM2c is newEnv over lt.FixtureM2c (a reference FLAC and a lossy reference next to the M2a rows).
func newEnvM2c(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := func() time.Time { return lt.FixedNow }
	lt.FixtureM2c(t, st, lt.FixedNow)
	lib := library.New(st, nil, now)
	n := 0
	j := judgments.New(st, lib, now, func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xc000+n) })
	b := trackctx.New(lib, j, listening.New(st, now), serve.NewSigner([]byte("0123456789abcdef0123456789abcdef"), now), "https://mm.example.com/", now)
	b.UseM2b(ears.New(ears.Options{Store: st, Library: lib, Now: now}), nil)
	return &env{st: st, b: b, lib: lib, now: now}
}

func goldenBundle(t *testing.T, name string, raw []byte) {
	t.Helper()
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	golden := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(golden, pretty.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), pretty.String()); diff != "" {
		t.Errorf("golden %s (-want +got):\n%s", name, diff)
	}
}

func rawMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestContext_Reference(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	b, err := e.b.Build(context.Background(), agent, lt.TrackRefFlac, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	goldenBundle(t, "context_reference.golden.json", raw)
	if diff := cmp.Diff([]string{"id", "title", "status", "stars", "critic_stars", "critic_verdict", "tags", "duration_s", "recipe_known", "lossy", "recorded_at", "url"}, keys(t, section(t, raw, "track"))); diff != "" {
		t.Errorf("track keys:\n%s", diff)
	}
	if diff := cmp.Diff([]string{"engine", "known", "hash", "request", "note"}, keys(t, section(t, raw, "recipe"))); diff != "" {
		t.Errorf("recipe keys:\n%s", diff)
	}
	m := rawMap(t, raw)
	recipe := m["recipe"].(map[string]any)
	if recipe["engine"] != "unknown" || recipe["known"] != false || recipe["request"] != nil || recipe["note"] == "" {
		t.Errorf("recipe: %v", recipe)
	}
	if tr := m["track"].(map[string]any); tr["recipe_known"] != false || tr["lossy"] != false {
		t.Errorf("track flags: %v", tr)
	}
	if m["score"] != nil {
		t.Errorf("a reference track has no score: %v", m["score"])
	}
	if pv := m["provenance"].(map[string]any); pv["import_rule"] != "reference" {
		t.Errorf("provenance: %v", pv)
	}
	stream := m["_links"].(map[string]any)["stream"].(map[string]any)
	if stream["flac"] == nil || stream["mp3"] == nil {
		t.Errorf("reference FLAC keeps both links: %v", stream)
	}
}

func TestContext_Lossy(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	b, err := e.b.Build(context.Background(), agent, lt.TrackLossy, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	goldenBundle(t, "context_lossy.golden.json", raw)
	m := rawMap(t, raw)
	if tr := m["track"].(map[string]any); tr["recipe_known"] != false || tr["lossy"] != true {
		t.Errorf("track flags: %v", tr)
	}
	if rc := m["recipe"].(map[string]any); rc["request"] != nil || rc["known"] != false {
		t.Errorf("recipe: %v", rc)
	}
	if m["score"] != nil {
		t.Errorf("score: %v", m["score"])
	}
	stream := m["_links"].(map[string]any)["stream"].(map[string]any)
	if _, has := stream["flac"]; has || stream["mp3"] == nil {
		t.Errorf("stream links of a lossy track: %v", stream)
	}
	if diff := cmp.Diff([]string{"mp3", "expires_at"}, keys(t, section(t, section(t, raw, "_links"), "stream"))); diff != "" {
		t.Errorf("stream key order:\n%s", diff)
	}
	if b.Budget.Chars != trackctx.Measure(&b) {
		t.Errorf("budget %+v", b.Budget)
	}
}
