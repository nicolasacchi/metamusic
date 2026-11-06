package duels_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// m5Env is newEnv over the M5 fixture (plan/manual/measured/lyrics-only sections, reference and multi-part imports).
type m5Env struct {
	*env
	info lt.M5Info
}

func newM5Env(t *testing.T, coin bool) *m5Env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{st: st, now: lt.FixedNow, rend: &spyRenderer{Renderer: cliptest.NewRenderer()}}
	info := lt.FixtureM5(t, st, e.now)
	e.fx = duelstest.Fixture(t, st, "", info.Fixtures, e.now, nil)
	e.lib = library.New(st, nil, func() time.Time { return e.now })
	var nid atomic.Uint64
	e.opts = duels.Options{
		Store: st, Library: e.lib, Clips: e.rend, Signer: serve.NewSigner([]byte("0123456789abcdef0123456789abcdef"), func() time.Time { return e.now }),
		Now:   func() time.Time { return e.now },
		NewID: func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0x30000+nid.Add(1)) },
		Rand:  func() bool { return coin },
	}
	e.svc = duels.New(e.opts)
	return &m5Env{env: e, info: info}
}

func (e *m5Env) section(t *testing.T, p auth.Principal, a, b string, sel duels.SectionSelector) duels.Duel {
	t.Helper()
	blind := true
	d, err := e.svc.Create(context.Background(), p, duels.CreateInput{ATrackID: a, BTrackID: b, Blind: &blind, Section: &sel})
	if err != nil {
		t.Fatalf("create section duel: %v", err)
	}
	return d
}

func lbl(s string) duels.SectionSelector { return duels.SectionSelector{Label: &s} }
func kindOrd(k string, o int) duels.SectionSelector {
	return duels.SectionSelector{Kind: &k, Ordinal: &o}
}

type rowWin struct{ from, to, bfrom, bto int64 }

func (e *m5Env) row(t *testing.T, id string) (w rowWin, label, source string) {
	t.Helper()
	if err := e.st.R.QueryRow(`SELECT from_ms, to_ms, b_from_ms, b_to_ms, section_label, section_source FROM duels WHERE id = ?`, id).
		Scan(&w.from, &w.to, &w.bfrom, &w.bto, &label, &source); err != nil {
		t.Fatal(err)
	}
	return w, label, source
}

func TestCreateDuel_Section_Windows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		coin       bool
		sel        duels.SectionSelector
		want       rowWin // slot A first, as stored
		label, src string
	}{
		// G0 is plan-timed (chorus 1 = 30-50), G1 has manual rows (chorus 1 = 36-58): L = 22 s, 2 s pre-roll
		{"label", false, lbl("chorus 1"), rowWin{28000, 50000, 34000, 56000}, "chorus 1", "plan"},
		{"label is case-insensitive", false, lbl("Chorus 1"), rowWin{28000, 50000, 34000, 56000}, "Chorus 1", "plan"},
		{"kind and ordinal", false, kindOrd("chorus", 2), rowWin{78000, 105000, 84000, 111000}, "chorus 2", "plan"},
		{"swapped slots carry their own windows", true, lbl("chorus 1"), rowWin{34000, 56000, 28000, 50000}, "chorus 1", "plan"},
		{"short outro, shifted back from the track end", false, lbl("outro"), rowWin{103000, 113000, 110000, 120000}, "outro", "plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newM5Env(t, tc.coin)
			g0, g1 := e.info.Lineage[0], e.info.Lineage[1]
			d := e.section(t, human, g0, g1, tc.sel)
			got, label, src := e.row(t, d.ID)
			if got.to-got.from != got.bto-got.bfrom {
				t.Errorf("unequal windows: %+v", got)
			}
			if got != tc.want {
				t.Errorf("windows = %+v, want %+v", got, tc.want)
			}
			if !strings.EqualFold(label, tc.label) || src != tc.src {
				t.Errorf("section = %q/%q, want %q/%q", label, src, tc.label, tc.src)
			}
			if d.Section == nil || d.Section.Source != tc.src {
				t.Errorf("view section = %+v", d.Section)
			}
			// the clips are cut from the slot's own window
			specs := e.rend.specs()
			var wa, wb [2]int64
			for _, sp := range specs[len(specs)-2:] {
				if sp.MasterSHA256 == "" {
					t.Fatal("spec without master")
				}
			}
			wa = [2]int64{specs[len(specs)-2].W.StartMS, specs[len(specs)-2].W.EndMS}
			wb = [2]int64{specs[len(specs)-1].W.StartMS, specs[len(specs)-1].W.EndMS}
			if wa != [2]int64{tc.want.from, tc.want.to} || wb != [2]int64{tc.want.bfrom, tc.want.bto} {
				t.Errorf("clip windows = %v %v", wa, wb)
			}
		})
	}
}

func TestCreateDuel_Section_Refusals(t *testing.T) {
	t.Parallel()
	e := newM5Env(t, false)
	ctx := context.Background()
	g0, g1, g2 := e.info.Lineage[0], e.info.Lineage[1], e.info.Lineage[2]
	// real-world shapes: lyrics-only sections (an import / a lineage tip), a reference original, a multi-part lossy import
	untimed := map[string]string{"lyrics-only G2": g2, "lyrics-only T5": e.info.Takes["T5"], "lyrics-only T6": e.info.Takes["T6"],
		"reference original": e.info.Originals[0], "multi-part p1": e.info.Groups[0][0], "multi-part p2": e.info.Groups[0][1]}
	for name, id := range untimed {
		for _, swap := range []bool{false, true} {
			a, b := g0, id
			if swap {
				a, b = id, g0
			}
			_, err := e.svc.Create(ctx, human, duels.CreateInput{ATrackID: a, BTrackID: b, Section: &duels.SectionSelector{Label: sp("chorus 1")}})
			pe := asProblem(t, err, 422, "SECTION_UNTIMED")
			if pe.Details["track_id"] != id {
				t.Errorf("%s: details = %v, want track %s", name, pe.Details, id)
			}
		}
	}
	_, err := e.svc.Create(ctx, human, duels.CreateInput{ATrackID: g0, BTrackID: g1, Section: &duels.SectionSelector{Label: sp("bridge")}})
	asProblem(t, err, 422, "SECTION_NOT_FOUND")
	_, err = e.svc.Create(ctx, human, duels.CreateInput{ATrackID: g0, BTrackID: g1, Section: &duels.SectionSelector{Kind: sp("chorus"), Ordinal: sp(3)}})
	asProblem(t, err, 422, "SECTION_NOT_FOUND")
	// T2 has no "outro" while G0 does: the track lacking it is named
	_, err = e.svc.Create(ctx, human, duels.CreateInput{ATrackID: g0, BTrackID: e.info.Takes["T2"], Section: &duels.SectionSelector{Label: sp("outro")}})
	if pe := asProblem(t, err, 422, "SECTION_NOT_FOUND"); pe.Details["track_id"] != e.info.Takes["T2"] {
		t.Errorf("details = %v", pe.Details)
	}
	for name, in := range map[string]duels.CreateInput{
		"empty selector":     {Section: &duels.SectionSelector{}},
		"label and kind":     {Section: &duels.SectionSelector{Label: sp("chorus 1"), Kind: sp("chorus"), Ordinal: sp(1)}},
		"kind without ord":   {Section: &duels.SectionSelector{Kind: sp("chorus")}},
		"ordinal without it": {Section: &duels.SectionSelector{Ordinal: sp(1)}},
		"blank label":        {Section: &duels.SectionSelector{Label: sp("  ")}},
		"with from_s":        {Section: &duels.SectionSelector{Label: sp("chorus 1")}, FromS: sp(10.0)},
		"with to_s":          {Section: &duels.SectionSelector{Label: sp("chorus 1")}, ToS: sp(40.0)},
	} {
		in.ATrackID, in.BTrackID = g0, g1
		_, err := e.svc.Create(ctx, human, in)
		if pe := asProblem(t, err, 422, "VALIDATION_FAILED"); pe.Details["field"] == nil {
			t.Errorf("%s: details = %v", name, pe.Details)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM duels`); n != 4 {
		t.Errorf("a refused call left rows: %d", n)
	}
}

// A section duel whose track is shorter than the shortest section window is refused with the clip code, never clamped.
func TestCreateDuel_Section_ShortTrackRefused(t *testing.T) {
	t.Parallel()
	e := newM5Env(t, false)
	// manual rows outrank the plan: G0's chorus 1 now starts beyond the end of the 120 s track
	for i, r := range []struct {
		kind, label string
		from, to    float64
	}{{"verse", "verse 1", 0, 30}, {"chorus", "chorus 1", 130, 140}} {
		if _, err := e.st.W.Exec(`INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence) VALUES (?,?,?,?,?,?,?,?,0.9)`,
			e.info.Lineage[0], "manual", i, r.kind, r.label, "["+r.label+"]", r.from, r.to); err != nil {
			t.Fatal(err)
		}
	}
	_, err := e.svc.Create(context.Background(), human, duels.CreateInput{ATrackID: e.info.Lineage[0], BTrackID: e.info.Lineage[1], Section: &duels.SectionSelector{Label: sp("chorus 1")}})
	asProblem(t, err, 422, "CLIP_WINDOW_INVALID")
}

// Without a section the duel is the old one: one global window, no per-slot windows, no section.
func TestCreateDuel_NoSection_Unchanged(t *testing.T) {
	t.Parallel()
	e := newM5Env(t, false)
	d := e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true)
	if d.Section != nil || d.Slots.A.Window != nil || d.Slots.B.Window != nil || d.Window.FromS == nil || d.Window.ToS == nil {
		t.Errorf("window duel changed shape: %+v", d)
	}
	var n int
	if err := e.st.R.QueryRow(`SELECT count(*) FROM duels WHERE b_from_ms IS NOT NULL OR section_label IS NOT NULL OR section_source IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Errorf("section columns set on %d rows (%v)", n, err)
	}
	// the precompiled fixture duels still read the same, revealed or not
	for _, id := range []string{duelstest.DuelOpen, duelstest.DuelDecided, duelstest.DuelSighted} {
		g, err := e.svc.Get(context.Background(), human, id)
		if err != nil {
			t.Fatal(err)
		}
		if g.Section != nil || g.Window.FromS == nil || *g.Window.FromS != 16.3 || *g.Window.ToS != 61.3 || g.Slots.A.Window != nil {
			t.Errorf("%s: %+v", id, g)
		}
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), `"section"`) || strings.Contains(string(b), `"window":{"from_s":null`) {
		t.Errorf("window duel JSON carries section fields: %s", b)
	}
}

// stripVolatile removes what legitimately differs between two duels of the same pair (ids, times, signed clip links).
func stripVolatile(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, c := range x {
			switch k {
			case "id", "created_at", "expires_at", "clip":
				continue
			}
			out[k] = stripVolatile(c)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = stripVolatile(c)
		}
		return out
	}
	return v
}

// TestDuel_SectionBlindness_Leak: before the reveal no response carries the hidden window or the slot identity; the
// unrevealed views of two duels whose slot coins differ are indistinguishable.
func TestDuel_SectionBlindness_Leak(t *testing.T) {
	t.Parallel()
	for _, p := range []auth.Principal{human, device, agent} {
		t.Run(string(p.Kind), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			views := map[bool][]any{}
			for _, coin := range []bool{false, true} {
				e := newM5Env(t, coin)
				g0, g1 := e.info.Lineage[0], e.info.Lineage[1]
				d := e.section(t, p, g0, g1, lbl("chorus 1"))
				get, err := e.svc.Get(ctx, p, d.ID)
				if err != nil {
					t.Fatal(err)
				}
				nx, err := e.svc.Next(ctx, p, []string{duelstest.DuelOpen})
				if err != nil || nx.Duel == nil {
					t.Fatalf("next = %+v, %v", nx, err)
				}
				pg, err := e.svc.List(ctx, p, duels.ListFilter{States: []string{"open"}})
				if err != nil {
					t.Fatal(err)
				}
				var row any
				for _, it := range pg.Items {
					if it.ID == d.ID {
						row = toJSON(t, it)
					}
				}
				if row == nil {
					t.Fatal("list row missing")
				}
				for what, v := range map[string]any{"create": toJSON(t, d), "get": toJSON(t, get), "next": toJSON(t, nx.Duel), "list": row} {
					m := v.(map[string]any)
					w := m["window"].(map[string]any)
					if w["from_s"] != nil || w["to_s"] != nil {
						t.Errorf("%s: unrevealed window bounds leak: %v", what, w)
					}
					if w["duration_s"] != 22.0 {
						t.Errorf("%s: duration_s = %v", what, w["duration_s"])
					}
					for _, s := range []string{"a", "b"} {
						slot := m["slots"].(map[string]any)[s].(map[string]any)
						if _, ok := slot["window"]; ok {
							t.Errorf("%s: slots.%s.window present while unrevealed", what, s)
						}
						if slot["track"] != nil || slot["gain_db"] != nil || slot["window_lufs"] != nil {
							t.Errorf("%s: slot %s identity leaks: %v", what, s, slot)
						}
					}
					raw, _ := json.Marshal(v)
					for _, secret := range []string{g0, g1, "Synthetic Lineage"} {
						if strings.Contains(string(raw), `"`+secret+`"`) || strings.Contains(string(raw), secret) {
							t.Errorf("%s leaks %q: %s", what, secret, raw)
						}
					}
				}
				views[coin] = []any{stripVolatile(toJSON(t, get)), stripVolatile(row)}
			}
			if !reflect.DeepEqual(views[false], views[true]) {
				t.Errorf("unrevealed views differ with the slot coin:\n%v\n%v", views[false], views[true])
			}
		})
	}
}

func TestDuel_SectionReveal(t *testing.T) {
	t.Parallel()
	e := newM5Env(t, true)
	g0, g1 := e.info.Lineage[0], e.info.Lineage[1]
	d := e.section(t, human, g0, g1, lbl("chorus 1"))
	res := e.judge(t, human, d.ID, "a")
	r := res.Duel
	if !r.Revealed || r.Window.FromS == nil || r.Window.ToS == nil || r.Window.DurationS != 22 {
		t.Fatalf("revealed window = %+v", r.Window)
	}
	// coin true: slot A holds G1 (manual chorus 1 at 36 s), slot B holds G0 (plan, 30 s)
	if r.Slots.A.Track.ID != g1 || r.Slots.A.Window == nil || r.Slots.A.Window.FromS != 34 || r.Slots.A.Window.ToS != 56 ||
		r.Slots.B.Window == nil || r.Slots.B.Window.FromS != 28 || r.Slots.B.Window.ToS != 50 {
		t.Errorf("slot windows = %+v / %+v", r.Slots.A.Window, r.Slots.B.Window)
	}
	if *r.Window.FromS != 34 || *r.Window.ToS != 56 {
		t.Errorf("duel window = %v..%v, want slot A's", *r.Window.FromS, *r.Window.ToS)
	}
}
