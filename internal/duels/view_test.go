package duels_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
)

func toJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// walk visits every key and leaf of a decoded JSON value.
func walk(v any, path string, key func(path, k string), leaf func(path string, v any)) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			key(path+"."+k, k)
			walk(c, path+"."+k, key, leaf)
		}
	case []any:
		for i, c := range x {
			walk(c, fmt.Sprintf("%s[%d]", path, i), key, leaf)
		}
	default:
		leaf(path, v)
	}
}

var forbiddenKeys = []string{"swapped", "seed", "request_id", "title", "root_track_id", "target_lufs"}

func TestDuel_BlindRedaction(t *testing.T) {
	t.Parallel()
	secrets := []string{duelstest.TrackAlpha, duelstest.TrackBeta, duelstest.TitleAlpha, duelstest.TitleBeta,
		duelstest.RequestAlpha, duelstest.RequestBeta}
	check := func(t *testing.T, v any, what string) {
		t.Helper()
		m := v.(map[string]any)
		slots := m["slots"].(map[string]any)
		for _, s := range []string{"a", "b"} {
			slot := slots[s].(map[string]any)
			for _, f := range []string{"track", "gain_db", "window_lufs"} {
				if slot[f] != nil {
					t.Errorf("%s: slots.%s.%s = %v, want null", what, s, f, slot[f])
				}
			}
		}
		walk(v, "", func(path, k string) {
			for _, f := range forbiddenKeys {
				if k == f {
					t.Errorf("%s: key %s at %s", what, k, path)
				}
			}
			if k == "winner_track_id" {
				t.Errorf("%s: winner_track_id present at %s while unrevealed", what, path)
			}
		}, func(path string, leaf any) {
			switch x := leaf.(type) {
			case string:
				for _, s := range secrets {
					if x == s {
						t.Errorf("%s: %s leaks %q", what, path, s)
					}
				}
			case float64:
				if x == float64(duelstest.SeedAlpha) || x == float64(duelstest.SeedBeta) || x == 7.37 || x == -7.37 {
					t.Errorf("%s: %s = %v looks like a seed or gain", what, path, x)
				}
			}
		})
	}
	for _, p := range []auth.Principal{human, device, agent} {
		t.Run(string(p.Kind), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			ctx := context.Background()
			d, err := e.svc.Get(ctx, p, duelstest.DuelOpen)
			if err != nil {
				t.Fatal(err)
			}
			check(t, toJSON(t, d), "get")
			nx, err := e.svc.Next(ctx, p, nil)
			if err != nil || nx.Duel == nil {
				t.Fatalf("next = %+v, %v", nx, err)
			}
			check(t, toJSON(t, nx.Duel), "next")
			// a fresh blind duel as returned to its creator
			c := e.create(t, p, duelstest.TrackAlpha, duelstest.TrackBeta, true)
			check(t, toJSON(t, c), "create")
			// list rows keep the slots anonymous
			pg, err := e.svc.List(ctx, p, duels.ListFilter{Mode: "blind"})
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range pg.Items {
				if it.Revealed {
					continue
				}
				if it.Slots.A.Track != nil || it.Slots.B.Track != nil || it.HumanVerdict != nil {
					t.Errorf("list row %s leaks: %+v", it.ID, it)
				}
			}
			// after the caller's verdict every value is present and correct
			res := e.judge(t, p, duelstest.DuelOpen, "a")
			r := res.Duel
			if !r.Revealed || r.Slots.A.Track == nil || r.Slots.A.Track.ID != duelstest.TrackAlpha || r.Slots.A.Track.Title != duelstest.TitleAlpha ||
				r.Slots.A.Track.Seed != duelstest.SeedAlpha || r.Slots.B.Track.RequestID != duelstest.RequestBeta ||
				r.Slots.B.Track.RootTrackID != duelstest.TrackAlpha {
				t.Errorf("revealed view = %+v", r)
			}
			if g := r.Slots.A.GainDB; g == nil || *g != -7.37 {
				t.Errorf("gain a = %v", g)
			}
			if g := r.Slots.B.GainDB; g == nil || *g != 0 {
				t.Errorf("gain b = %v", g)
			}
			if w := r.Slots.A.WindowLUFS; w == nil || *w != duelstest.LUFSA {
				t.Errorf("window lufs = %v", w)
			}
			if r.MyVerdict == nil || r.MyVerdict.WinnerTrackID == nil || *r.MyVerdict.WinnerTrackID != duelstest.TrackAlpha {
				t.Errorf("my verdict = %+v", r.MyVerdict)
			}
		})
	}
	t.Run("sighted is present from creation", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		d := e.create(t, agent, duelstest.TrackAlpha, duelstest.TrackBeta, false)
		if !d.Revealed || d.Slots.A.Track == nil || d.Slots.A.Track.ID != duelstest.TrackAlpha || d.Slots.B.Track.ID != duelstest.TrackBeta ||
			d.Slots.A.GainDB == nil || d.Slots.B.WindowLUFS == nil {
			t.Errorf("view = %+v", d)
		}
	})
	t.Run("critic sees own verdict after judging", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		res := e.judge(t, agent, duelstest.DuelOpen, "tie")
		if !res.Duel.Revealed || res.Duel.Slots.A.Track == nil || res.Verdict.WinnerTrackID != nil {
			t.Errorf("result = %+v", res)
		}
		// the revealed critic still sees no human verdict that does not exist
		if res.Duel.HumanVerdict != nil {
			t.Errorf("human verdict = %+v", res.Duel.HumanVerdict)
		}
	})
}

func TestReveal_Rules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	type c struct {
		name                                       string
		duel                                       string
		p                                          auth.Principal
		revealed, hasHuman, hasMine, hasCriticList bool
		mineSource                                 string
	}
	tests := []c{
		// DuelOpen: blind, no verdicts
		{"open human", duelstest.DuelOpen, human, false, false, false, false, ""},
		{"open device", duelstest.DuelOpen, device, false, false, false, false, ""},
		{"open critic", duelstest.DuelOpen, agent, false, false, false, false, ""},
		// DuelDecided: blind, human verdict a, critic verdict b (agent:fixture-agent)
		{"decided human", duelstest.DuelDecided, human, true, true, true, true, "human"},
		{"decided device", duelstest.DuelDecided, device, true, true, true, true, "human"},
		{"decided critic who judged", duelstest.DuelDecided, agent, true, true, true, true, "critic"},
		{"decided critic who did not", duelstest.DuelDecided, agent2, true, true, false, false, ""},
		// DuelSighted: non-blind, device tie
		{"sighted human", duelstest.DuelSighted, human, true, true, true, false, "human"},
		{"sighted critic unjudged", duelstest.DuelSighted, agent2, true, true, false, false, ""},
		// DuelExpired: blind, no verdicts
		{"expired human", duelstest.DuelExpired, human, false, false, false, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			d, err := e.svc.Get(ctx, tc.p, tc.duel)
			if err != nil {
				t.Fatal(err)
			}
			if d.Revealed != tc.revealed || (d.Slots.A.Track != nil) != tc.revealed {
				t.Errorf("revealed = %v track = %v, want %v", d.Revealed, d.Slots.A.Track, tc.revealed)
			}
			if (d.HumanVerdict != nil) != tc.hasHuman || (d.MyVerdict != nil) != tc.hasMine {
				t.Errorf("human %v mine %v, want %v %v", d.HumanVerdict, d.MyVerdict, tc.hasHuman, tc.hasMine)
			}
			if d.MyVerdict != nil && d.MyVerdict.Source != tc.mineSource {
				t.Errorf("my source = %s", d.MyVerdict.Source)
			}
			if (len(d.CriticVerdicts) > 0) != tc.hasCriticList {
				t.Errorf("critic verdicts = %d, want list=%v", len(d.CriticVerdicts), tc.hasCriticList)
			}
		})
	}
	t.Run("states", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		for id, want := range map[string]string{duelstest.DuelOpen: "open", duelstest.DuelDecided: "decided", duelstest.DuelSighted: "decided", duelstest.DuelExpired: "expired"} {
			d, err := e.svc.Get(ctx, human, id)
			if err != nil || d.State != want {
				t.Errorf("%s: state %q, %v; want %s", id, d.State, err, want)
			}
		}
	})
	t.Run("human sees the human verdict of the device, whoever cast it", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		d, _ := e.svc.Get(ctx, human, duelstest.DuelSighted)
		if d.MyVerdict == nil || d.MyVerdict.PrincipalKind != "device" || d.MyVerdict.Verdict != "tie" {
			t.Errorf("my verdict = %+v", d.MyVerdict)
		}
	})
	t.Run("unknown and clipless", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		_, err := e.svc.Get(ctx, human, "nope")
		asProblem(t, err, 404, "NOT_FOUND")
		nc := duels.New(duels.Options{Store: e.st, Library: e.lib, Now: e.opts.Now, Signer: e.opts.Signer})
		for name, f := range map[string]func() error{
			"get":  func() error { _, err := nc.Get(ctx, human, duelstest.DuelOpen); return err },
			"next": func() error { _, err := nc.Next(ctx, human, nil); return err },
			"create": func() error {
				_, err := nc.Create(ctx, human, duels.CreateInput{ATrackID: "a", BTrackID: "b"})
				return err
			},
		} {
			pe := asProblem(t, f(), 503, "UNAVAILABLE")
			if pe.Details["component"] != "clips" {
				t.Errorf("%s: details = %v", name, pe.Details)
			}
		}
		// list, submit and ladder still work without a clip service
		if _, err := nc.List(ctx, human, duels.ListFilter{}); err != nil {
			t.Errorf("list: %v", err)
		}
		r, err := nc.Submit(ctx, human, duelstest.DuelOpen, duels.VerdictInput{Verdict: "a"})
		if err != nil || r.Duel.Slots.A.Track == nil {
			t.Errorf("submit: %+v, %v", r, err)
		}
		if _, err := nc.Ladder(ctx, duels.LadderQuery{}); err != nil {
			t.Errorf("ladder: %v", err)
		}
	})
	t.Run("clip links", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		for p, ttl := range map[string]int64{"human": 12 * 3600, "agent": 15 * 60} {
			pr := map[string]auth.Principal{"human": human, "agent": agent}[p]
			d, err := e.svc.Get(ctx, pr, duelstest.DuelOpen)
			if err != nil {
				t.Fatal(err)
			}
			c := d.Slots.A.Clip
			if c.TTLS != ttl || c.ContentType != "audio/flac" || !strings.HasPrefix(c.URL, "/media/") || !strings.Contains(c.URL, ".flac?exp=") || c.Bytes <= 0 {
				t.Errorf("%s clip = %+v", p, c)
			}
		}
	})
}
