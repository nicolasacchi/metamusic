package transform

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// TestTitle_Table: "<primary title> — <label>" with an em dash, cut to 200 runes on a rune boundary, an explicit title wins, and
// every kind's label (C202.4).
func TestTitle_Table(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", 150)
	exp := "My own title"
	for _, tc := range []struct {
		name            string
		explicit        *string
		primary, label  string
		want            string
		wantRunesAtMost int
		wantPrefix      string
	}{
		{name: "separator", primary: "Song", label: "glacier ×20", want: "Song — glacier ×20"},
		{name: "explicit wins", explicit: &exp, primary: "Song", label: "glacier ×20", want: "My own title"},
		{name: "explicit blank falls back", explicit: ptrStr("   "), primary: "Song", label: "x", want: "Song — x"},
		{name: "no parent", label: "Score render", want: "Score render"},
		{name: "nothing", want: "Transform"},
		{name: "rune boundary", primary: long, label: strings.Repeat("→", 100), wantRunesAtMost: 200, wantPrefix: long + " — "},
		{name: "four-byte runes", primary: strings.Repeat("🎵", 190), label: strings.Repeat("🎶", 30), wantRunesAtMost: 200, wantPrefix: strings.Repeat("🎵", 190)},
		{name: "explicit is cut too", explicit: ptrStr(strings.Repeat("ü", 300)), primary: "x", label: "y", wantRunesAtMost: 200},
	} {
		got := DefaultTitle(tc.explicit, tc.primary, tc.label)
		if tc.want != "" && got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
		if tc.wantRunesAtMost > 0 {
			if n := utf8.RuneCountInString(got); n != tc.wantRunesAtMost {
				t.Errorf("%s: %d runes, want exactly %d", tc.name, n, tc.wantRunesAtMost)
			}
			if !utf8.ValidString(got) {
				t.Errorf("%s: cut inside a rune", tc.name)
			}
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("%s: prefix lost", tc.name)
			}
		}
	}
	// the label of each kind, from resolved parameters
	for _, tc := range []struct {
		kind   Kind
		params string
		second string
		want   string
	}{
		{KindMorph, `{}`, "B Title", "→ B Title morph"},
		{KindShepard, `{"direction":"up"}`, "", "shepard ↑"},
		{KindShepard, `{"direction":"down"}`, "", "shepard ↓"},
		{KindGlacier, `{"factor":20}`, "", "glacier ×20"},
		{KindGlacier, `{"factor":22.5}`, "", "glacier ×22.5"},
		{KindPolytempo, `{"bpm_a":90,"bpm_b":120}`, "", "polytempo 90/120"},
		{KindEnsemble, `{"space":"cistern"}`, "", "ensemble cistern"},
	} {
		ks, _ := LookupKind(tc.kind)
		if got := ks.Title(TitleIn{Primary: "A", Secondary: tc.second, Resolved: []byte(tc.params)}); got != tc.want {
			t.Errorf("%s label %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// TestGet_List: every principal sees every transform; filters by state, kind and input; newest first; cursor paging.
func TestGet_List(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var ids []string
	for i, who := range []struct {
		p    string
		kind Kind
		src  string
		body string
	}{
		{"agent", KindGlacier, "A", `{"source_id":%q,"factor":11}`},
		{"human", KindGlacier, "B", `{"source_id":%q}`},
		{"agent", KindEnsemble, "A", `{"source_id":%q}`},
		{"human", KindShepard, "A", `{"source_id":%q,"loop_s":2}`},
	} {
		p := human
		if who.p == "agent" {
			p = agentA
		}
		r.advance(time.Second)
		res := r.create(p, req(who.kind, fmt.Sprintf(who.body, r.tx(who.src))))
		ids = append(ids, res.View.ID)
		if i == 1 {
			r.finish(res.View.ID, StateDone, 5)
		}
	}
	ctx := t.Context()
	// an agent sees a human's transform, and get_transform is the same view
	v, err := r.svc.Get(ctx, agentB, ids[1])
	if err != nil || v.PrincipalKind != "human" || v.State != StateDone || v.UsedMS == nil || *v.UsedMS != 5 || len(v.Inputs) != 1 || v.Inputs[0].Title != "Synthetic Take XB" {
		t.Fatalf("get %+v %v", v, err)
	}
	pg, err := r.svc.List(ctx, agentB, ListFilter{})
	if err != nil || len(pg.Items) != 4 || pg.NextCursor != "" {
		t.Fatalf("list %+v %v", pg, err)
	}
	var got []string
	for _, it := range pg.Items {
		got = append(got, it.ID)
	}
	if !slices.Equal(got, []string{ids[3], ids[2], ids[1], ids[0]}) {
		t.Errorf("order %v, want newest first %v", got, ids)
	}
	for name, tc := range map[string]struct {
		f    ListFilter
		want []string
	}{
		"state done":  {ListFilter{State: StateDone}, []string{ids[1]}},
		"state queue": {ListFilter{State: StateQueued}, []string{ids[3], ids[2], ids[0]}},
		"kind":        {ListFilter{Kind: KindGlacier}, []string{ids[1], ids[0]}},
		"input A":     {ListFilter{TrackID: r.tx("A")}, []string{ids[3], ids[2], ids[0]}},
		"input B":     {ListFilter{TrackID: r.tx("B")}, []string{ids[1]}},
		"none":        {ListFilter{TrackID: "x"}, nil},
	} {
		pg, err := r.svc.List(ctx, human, tc.f)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, it := range pg.Items {
			ids = append(ids, it.ID)
		}
		if !slices.Equal(ids, tc.want) || pg.Items == nil {
			t.Errorf("%s: %v, want %v", name, ids, tc.want)
		}
	}
	// paging: two at a time, no overlap, the cursor ends
	var all []string
	cursor := ""
	for pages := 0; pages < 5; pages++ {
		pg, err := r.svc.List(ctx, human, ListFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range pg.Items {
			all = append(all, it.ID)
		}
		if cursor = pg.NextCursor; cursor == "" {
			break
		}
	}
	if !slices.Equal(all, got) {
		t.Errorf("paged %v, want %v", all, got)
	}
	_, err = r.svc.List(ctx, human, ListFilter{Cursor: "!!"})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	if _, err = r.svc.Get(ctx, human, "nope"); err == nil {
		t.Error("get of an unknown id succeeded")
	}
	wantProblem(t, err, 404, "NOT_FOUND")
	// disabled
	r.svc.o.Config.TransformEnabled = false
	_, err = r.svc.List(ctx, human, ListFilter{})
	wantProblem(t, err, 503, "UNAVAILABLE")
}

// TestWait_Changed: a state change releases a waiter at once; the default since_state is the state at call time.
func TestWait_Changed(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	v := r.create(human, glacier(r.tx("A"))).View
	var wg sync.WaitGroup
	var w WaitResult
	var werr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		w, werr = r.svc.Wait(context.Background(), agentA, v.ID, "", 20*time.Second)
	}()
	time.Sleep(80 * time.Millisecond)
	start := time.Now()
	r.exec(`UPDATE transforms SET state='running', attempts=1, owner='t', lease_expires_at='2026-10-07T12:00:00.000Z', started_at='2026-10-07T12:00:00.000Z' WHERE id=?`, v.ID)
	r.svc.Broadcast(v.ID)
	wg.Wait()
	if werr != nil || !w.Changed || w.TimedOut || w.View.State != StateRunning {
		t.Fatalf("wait %+v %v", w, werr)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("the broadcast did not wake the waiter")
	}
	// an explicit since_state that already differs returns immediately
	w, err := r.svc.Wait(t.Context(), human, v.ID, StateQueued, 20*time.Second)
	if err != nil || !w.Changed || w.View.State != StateRunning {
		t.Errorf("immediate %+v %v", w, err)
	}
}

// TestWait_Timeout: no change within the timeout returns timed_out with changed false; 0 is a poll; above 55 s is clamped.
func TestWait_Timeout(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	v := r.create(human, glacier(r.tx("A"))).View
	start := time.Now()
	w, err := r.svc.Wait(t.Context(), human, v.ID, "", 150*time.Millisecond)
	if err != nil || w.Changed || !w.TimedOut || w.View.State != StateQueued || time.Since(start) < 140*time.Millisecond {
		t.Fatalf("wait %+v %v after %v", w, err, time.Since(start))
	}
	w, err = r.svc.Wait(t.Context(), human, v.ID, "", 0)
	if err != nil || w.Changed || !w.TimedOut {
		t.Errorf("poll %+v %v", w, err)
	}
	if MaxWait != 55*time.Second || DefaultWait != 25*time.Second {
		t.Error("wait limits drifted")
	}
	// a cancelled context ends the wait with its error
	ctx, cancel := context.WithCancel(t.Context())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err = r.svc.Wait(ctx, human, v.ID, "", 10*time.Second); err == nil {
		t.Error("a cancelled wait returned nil")
	}
	_, err = r.svc.Wait(t.Context(), human, "nope", "", time.Second)
	wantProblem(t, err, 404, "NOT_FOUND")
}

// TestWait_Terminal: a terminal transform returns at once, changed only if its state differs from since_state.
func TestWait_Terminal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	v := r.create(human, glacier(r.tx("A"))).View
	r.finish(v.ID, StateFailed, 40)
	start := time.Now()
	w, err := r.svc.Wait(t.Context(), human, v.ID, StateFailed, 30*time.Second)
	if err != nil || w.Changed || w.TimedOut || w.View.State != StateFailed || w.View.Error == nil || w.View.Error.Code != ErrTransformFailed || time.Since(start) > 2*time.Second {
		t.Fatalf("wait %+v %v", w, err)
	}
	if w, _ = r.svc.Wait(t.Context(), human, v.ID, StateRunning, 30*time.Second); !w.Changed {
		t.Errorf("failed after running is a change: %+v", w)
	}
	if w.View.UsedMS == nil || *w.View.UsedMS != 40 || w.View.Error.Message == "" {
		t.Errorf("view %+v", w.View)
	}
}

// TestWait_RenderWarnings: a done view carries the render-time warnings the artifact reader returns; a running one does not.
func TestWait_RenderWarnings(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(_ *configT, o *Options) {
		o.RenderWarnings = func(_ context.Context, id string) ([]Warning, error) {
			return []Warning{{Code: WarnDownbeatAmbiguous, Field: "downbeat_a_s", Message: "m"}}, nil
		}
	})
	v := r.create(human, glacier(r.tx("A"))).View
	if got := r.view(v.ID); len(got.Warnings) != 0 {
		t.Errorf("queued view has warnings %v", got.Warnings)
	}
	r.finish(v.ID, StateDone, 1)
	if got := r.view(v.ID); len(got.Warnings) != 1 || got.Warnings[0].Code != WarnDownbeatAmbiguous {
		t.Errorf("done view warnings %v", got.Warnings)
	}
}
