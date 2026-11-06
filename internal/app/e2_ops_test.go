package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/engineadopt"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	opReader    = auth.Principal{Kind: auth.KindAgent, ID: "tok-r", Name: "r", Scopes: []auth.Scope{auth.ScopeRead}, Via: "token"}
	opGenerator = auth.Principal{Kind: auth.KindAgent, ID: "tok-g", Name: "g", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}, Via: "token"}
)

// verifiedACE is the registry with the ACE-Step capabilities a full canary would have proved.
func verifiedACE() engine.Registry {
	f := acestep.EmbeddedFeatures()
	f.Tasks = []string{"text2music", "repaint", "extend"}
	f.Reference, f.ReferenceReproducible = true, true
	reg := registry.Default(registry.Options{ACEStep: true})
	reg[acestep.Name] = acestep.Engine{Verified: f}
	return reg
}

func TestEngineAdvisor_Table(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	info := lt.FixtureE2(t, st, lt.FixedNow)
	ad := engineadopt.New(engineadopt.Options{Store: st})
	ctx := context.Background()
	advise := func(a *engineAdvisor, p auth.Principal, id string) (got struct {
		Engine, Task string
		Cross, Block bool
		Actions      []string
	}) {
		adv, err := a.Advise(ctx, p, id, 5)
		if err != nil {
			t.Fatal(err)
		}
		got.Engine, got.Task, got.Cross, got.Block, got.Actions = adv.Engine, adv.Task, adv.CrossEngine, adv.BlockYue2Actions, adv.Actions
		return
	}
	plain := &engineAdvisor{st: st, engines: registry.Default(registry.Options{ACEStep: true}), adoption: ad}
	full := &engineAdvisor{st: st, engines: verifiedACE(), adoption: ad}

	// a yue2 take with a yue2 parent (or none) gets nothing: /context stays byte-identical
	if g := advise(plain, opGenerator, info.Golden[0]); g.Engine != "" || g.Actions != nil || g.Block {
		t.Errorf("yue2 take: %+v", g)
	}
	if adv, err := plain.Advise(ctx, opGenerator, "01900000-0000-7000-8000-00000000dead", 5); err != nil || adv.Engine != "" {
		t.Errorf("unknown track: %+v %v", adv, err)
	}
	// the port child is ACE-Step on a yue2 parent
	g := advise(plain, opGenerator, info.Port)
	if g.Engine != "acestep15" || g.Task != "text2music" || !g.Cross || !g.Block {
		t.Errorf("port take: %+v", g)
	}
	if len(g.Actions) != 0 {
		t.Errorf("a not adopted engine suggests spending: %v", g.Actions)
	}
	if g := advise(plain, opGenerator, info.Repaint); g.Task != "repaint" || g.Cross {
		t.Errorf("repaint take: %+v", g)
	}
	if _, err := ad.Adopt(ctx, "cli:test", "acestep15", "fixture"); err != nil {
		t.Fatal(err)
	}
	// adopted, turbo text2music only verified: only the lyric_edit line (it is in Caps.Tasks); no extend or repaint
	g = advise(plain, opGenerator, info.Port)
	if len(g.Actions) != 1 || !strings.Contains(g.Actions[0], "lyric_edit") {
		t.Errorf("default caps actions: %v", g.Actions)
	}
	// extend and repaint verified: the extend-or-repaint line leads
	g = advise(full, opGenerator, info.Port)
	if len(g.Actions) < 2 || !strings.Contains(g.Actions[0], "extend or repaint") {
		t.Errorf("verified caps actions: %v", g.Actions)
	}
	// without the generate scope nothing that spends is suggested
	if g := advise(full, opReader, info.Port); len(g.Actions) != 0 || g.Engine != "acestep15" {
		t.Errorf("reader: %+v", g)
	}
	// rejected again: back to nothing
	if _, err := ad.Reject(ctx, "cli:test", "acestep15", "regressed"); err != nil {
		t.Fatal(err)
	}
	if g := advise(full, opGenerator, info.Port); len(g.Actions) != 0 {
		t.Errorf("rejected engine actions: %v", g.Actions)
	}
}

// TestBuildE2Ops_WiresAdvisor: the real Services answer /context for an ACE-Step take with the engine key.
func TestBuildE2Ops_WiresAdvisor(t *testing.T) {
	s, st := buildSetEnv(t, true, func(e2Inputs) ([]e2Extra, error) { return nil, nil })
	info := lt.FixtureE2(t, st, lt.FixedNow)
	b, err := s.Deps.Context.Build(context.Background(), opGenerator, info.Port, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	var m struct {
		Provenance map[string]any `json:"provenance"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Provenance["engine"] != "acestep15" || m.Provenance["cross_engine"] != true {
		t.Errorf("provenance = %v", m.Provenance)
	}
	y, err := s.Deps.Context.Build(context.Background(), opGenerator, info.Golden[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(y)
	if strings.Contains(string(raw), `"cross_engine"`) || strings.Contains(string(raw), `"acestep15"`) {
		t.Error("a yue2 take's bundle mentions ACE-Step")
	}
}

// TestBuildE2Ops_WiresBound: arming the second backend over the per-batch ceiling is refused with CONCURRENCY_EXCEEDS_CAP.
func TestBuildE2Ops_WiresBound(t *testing.T) {
	s, st := buildSetEnv(t, true, func(e2Inputs) ([]e2Extra, error) {
		return []e2Extra{{Backend: aceTestBackend{providertest.New(provider.BackendFakeACEStep, providertest.Script{})}, KeysLoaded: true}}, nil
	})
	now := time.Now()
	for _, b := range []string{provider.BackendFake, provider.BackendFakeACEStep} {
		q, _ := json.Marshal(providertest.GoodQuote(b, now))
		if _, err := st.W.Exec(`UPDATE provider_state SET quote_json = ?, quote_at = ? WHERE backend = ?`, string(q), store.FormatTime(now), b); err != nil {
			t.Fatal(err)
		}
	}
	no, yes := false, true
	human := auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Scopes: []auth.Scope{auth.ScopeAdmin}, Via: "local"}
	if _, err := s.Provider.Update(context.Background(), human, jobs.UpdateIn{Armed: &no, Reason: "off", Backend: provider.BackendFakeACEStep}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`UPDATE spend_caps SET batch_micro_usd = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Provider.Update(context.Background(), human, jobs.UpdateIn{Armed: &yes, Reason: "on", Backend: provider.BackendFakeACEStep})
	var ce *jobs.ConcurrencyExceedsCapError
	if !errors.As(err, &ce) || ce.XMicro <= 1 || len(ce.Backends) != 2 {
		t.Fatalf("err = %v, want CONCURRENCY_EXCEEDS_CAP over both backends", err)
	}
	if a, _ := armedOf(t, st, provider.BackendFakeACEStep); a != 0 {
		t.Error("the refused arm armed the backend")
	}
	if a, _ := armedOf(t, st, provider.BackendFake); a != 1 {
		t.Error("the refused arm changed the default backend")
	}
}
