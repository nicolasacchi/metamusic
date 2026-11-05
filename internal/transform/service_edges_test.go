package transform

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// TestResolve_Shepard: the loop source is loop_s, or loop_bars from a tempo (the ladder), or the default of 4 bars when a tempo
// resolves; with none it is loop_source_required, or ALIGNMENT_UNRESOLVED when bars were asked for.
func TestResolve_Shepard(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sh := func(label, extra string) Request {
		return req(KindShepard, fmt.Sprintf(`{"source_id":%q%s}`, r.tx(label), extra))
	}
	// a tempo resolves: loop_bars defaults to 4 and the alignment is recorded for the one input
	pl := r.mustBuild(sh("A", ""))
	if got := string(paramsMap(pl.Res.Params)["loop_bars"]); got != "4" || pl.Spec.Alignment == nil || len(pl.Spec.Alignment.Inputs) != 1 {
		t.Errorf("loop_bars %s alignment %+v", got, pl.Spec.Alignment)
	}
	if a := align(pl, 0); a.BPM != 92 || a.BPMSource != SrcDeclared || a.BeatsPerBar != 4 {
		t.Errorf("alignment %+v", a)
	}
	pl = r.mustBuild(sh("A", `,"loop_bars":2`))
	if got := string(paramsMap(pl.Res.Params)["loop_bars"]); got != "2" {
		t.Errorf("explicit loop_bars %s", got)
	}
	// loop_s needs no tempo at all
	r.clearDeclared("I")
	pl = r.mustBuild(sh("I", `,"loop_s":3`))
	if pl.Spec.Alignment != nil {
		t.Errorf("loop_s recorded an alignment %+v", pl.Spec.Alignment)
	}
	_, err := r.build(sh("I", ``))
	wantRule(t, err, "loop_s", RuleLoopSourceRequired)
	_, err = r.build(sh("I", `,"loop_bars":2`))
	pe := wantProblem(t, err, 422, ErrAlignmentUnresolved)
	if pe.Details["pass"] != "loop_s" {
		t.Errorf("details %v", pe.Details)
	}
	// start and loop must lie inside the take
	_, err = r.build(sh("A", `,"loop_s":3,"start_s":20`))
	wantRule(t, err, "start_s", RuleStartTooLate)
	_, err = r.build(sh("A", `,"loop_s":10,"start_s":15`))
	wantRule(t, err, "loop_s", RuleRange)
	// the output length is duration_s exactly
	if pl = r.mustBuild(sh("A", `,"loop_s":3,"duration_s":90`)); pl.Res.OutS != 90 {
		t.Errorf("out %v", pl.Res.OutS)
	}
}

// TestResolve_KeyFromABC: with a ScoreABC hook, a take with no key row takes its key from the score header (basis plan).
func TestResolve_KeyFromABC(t *testing.T) {
	t.Parallel()
	hook := func(_ context.Context, id string) (string, bool) {
		if id == "" {
			return "", false
		}
		return "X:1\nT:t\nM:4/4\nK:Dm\nCDEF|]\n", true
	}
	r := newRig(t, func(_ *config.Config, o *Options) { o.ScoreABC = hook })
	pl := r.mustBuild(r.morphAB(""))
	if a := align(pl, 0); a.KeyPC != 2 || !a.KeyMinor || a.KeySource != SrcPlan {
		t.Errorf("A key %+v", a)
	}
	if s := string(paramsMap(pl.Res.Params)["semitones_b"]); s != "0" {
		t.Errorf("same key must not shift: %s", s)
	}
	if slices.Contains(codes(pl.Warnings), WarnKeyUnknownAssumedNone) {
		t.Error("a known key warned")
	}
	// a hook that finds nothing leaves the key unknown
	r2 := newRig(t, func(_ *config.Config, o *Options) {
		o.ScoreABC = func(context.Context, string) (string, bool) { return "", false }
	})
	if pl = r2.mustBuild(r2.morphAB("")); !slices.Contains(codes(pl.Warnings), WarnKeyUnknownAssumedNone) {
		t.Error("no key found but no warning")
	}
}

// TestTargetLUFS: an explicit target wins over the master parameter, which wins over the parent's measured loudness, which wins
// over -16; the result is clamped to [-30, -12] and rounded to 0.1 LU.
func TestTargetLUFS(t *testing.T) {
	t.Parallel()
	loud := -14.04
	in := []inputInfo{{LUFS: &loud}}
	for _, tc := range []struct {
		name string
		req  Request
		m    map[string]jsonRaw
		in   []inputInfo
		want float64
	}{
		{"parent", Request{}, nil, in, -14.0},
		{"default", Request{}, nil, nil, -16},
		{"explicit", Request{TargetLUFS: f64(-20.04)}, map[string]jsonRaw{"target_lufs": jsonRaw("-18")}, in, -20},
		{"master param", Request{}, map[string]jsonRaw{"target_lufs": jsonRaw("-18")}, in, -18},
		{"clamped loud", Request{}, nil, []inputInfo{{LUFS: f64(-3)}}, -12},
		{"clamped quiet", Request{}, nil, []inputInfo{{LUFS: f64(-50)}}, -30},
	} {
		if got := targetLUFS(tc.req, tc.m, tc.in); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	// the spec carries the parent's measured loudness (A: -14 in the fixture)
	r := newRig(t)
	if pl := r.mustBuild(glacier(r.tx("A"))); pl.Spec.TargetLUFS != -14 {
		t.Errorf("target %v", pl.Spec.TargetLUFS)
	}
	q := glacier(r.tx("A"))
	q.TargetLUFS = f64(-22)
	if pl := r.mustBuild(q); pl.Spec.TargetLUFS != -22 {
		t.Errorf("explicit target %v", pl.Spec.TargetLUFS)
	}
}

// TestCreate_SeedRules: kinds with a PCG default their seed from the first input, an explicit seed wins, the others carry 0.
func TestCreate_SeedRules(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	g := r.mustBuild(glacier(r.tx("A")))
	if g.Spec.Seed != DefaultSeed(r.tx("A")) {
		t.Errorf("glacier seed %d", g.Spec.Seed)
	}
	q := glacier(r.tx("A"))
	q.Seed = new(int64)
	*q.Seed = 42
	if s := r.mustBuild(q); s.Spec.Seed != 42 || s.Canon.SpecHash == g.Canon.SpecHash {
		t.Errorf("explicit seed %d", s.Spec.Seed)
	}
	m := r.morphAB("")
	m.Seed = q.Seed
	if s := r.mustBuild(m); s.Spec.Seed != 0 {
		t.Errorf("morph carries seed %d", s.Spec.Seed)
	}
	for _, bad := range []int64{-1, seedLimit} {
		q.Seed = &bad
		_, err := r.build(q)
		wantRule(t, err, "seed", RuleRange)
	}
	// two glaciers of different takes differ only by their input: same default seed rule
	if b := r.mustBuild(glacier(r.tx("B"))); b.Spec.Seed == g.Spec.Seed {
		t.Error("two takes share a default seed")
	}
}

// TestCreate_ConcurrentSameSpec: concurrent identical creates produce one transform and one wake.
func TestCreate_ConcurrentSameSpec(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := r.svc.Create(context.Background(), human, glacier(r.tx("A")), nil)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- res.View.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 || r.count(`SELECT COUNT(*) FROM transforms`) != 1 || r.wakes.Load() != 1 {
		t.Errorf("%d distinct ids, %d rows, %d wakes", len(seen), r.count(`SELECT COUNT(*) FROM transforms`), r.wakes.Load())
	}
}

// TestCreate_IdempotencyEncoderRequired: under an Idempotency-Key the create needs its encoder, and a failed create leaves
// nothing behind (the transaction rolls back with the marks).
func TestCreate_IdempotencyEncoderRequired(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := r.gated(human, "k-noenc")
	if _, err := r.svc.Create(ctx, human, glacier(r.tx("A")), nil); err == nil {
		t.Fatal("create without an encoder under a key succeeded")
	}
	if r.count(`SELECT COUNT(*) FROM transforms`) != 0 || r.count(`SELECT COUNT(*) FROM audit_log`) != 0 || r.wakes.Load() != 0 {
		t.Error("a failed create left rows behind")
	}
	// a stale lease: the key was reclaimed since the gate began it
	ctx = r.gated(human, "k-lost")
	r.exec(`UPDATE idempotency SET created_at = '2000-01-01T00:00:00Z' WHERE key = 'k-lost'`)
	_, err := r.svc.Create(ctx, human, glacier(r.tx("A")), encodeJSON)
	wantProblem(t, err, 409, "IDEMPOTENCY_IN_PROGRESS")
	if r.count(`SELECT COUNT(*) FROM transforms`) != 0 {
		t.Error("a lost lease created a transform")
	}
	// the happy path under a key replays through the gate; here the encoder's bytes are what is stored
	ctx = r.gated(human, "k-ok")
	if _, err := r.svc.Create(ctx, human, glacier(r.tx("A")), encodeJSON); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := r.st.R.QueryRow(`SELECT response FROM idempotency WHERE key = 'k-ok'`).Scan(&body); err != nil || body == "" {
		t.Errorf("stored response %q %v", body, err)
	}
}

// TestDisabled_Reads: a disabled service answers 503 on cancel, get and wait.
func TestDisabled_Reads(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	v := r.create(human, glacier(r.tx("A"))).View
	if _, err := r.cancel(r.gated(human, ""), human, v.ID); err != nil {
		t.Fatal(err)
	}
	r.svc.o.Config.TransformEnabled = false
	_, err := r.cancel(t.Context(), human, v.ID)
	wantProblem(t, err, 503, "UNAVAILABLE")
	_, err = r.svc.Get(t.Context(), human, v.ID)
	wantProblem(t, err, 503, "UNAVAILABLE")
	_, err = r.svc.Wait(t.Context(), human, v.ID, "", 0)
	wantProblem(t, err, 503, "UNAVAILABLE")
}

// TestService_Edges: small contracts of the helpers other lanes call.
func TestService_Edges(t *testing.T) {
	t.Parallel()
	// RuleError reads as "field: rule"
	if got := (&RuleError{Field: "bars", Rule: RuleRange}).Error(); got != "transform: invalid bars: range" {
		t.Errorf("RuleError = %q", got)
	}
	// a nil service is not enabled; New fills the clock and id defaults
	var nilSvc *Service
	if nilSvc.Enabled() {
		t.Error("a nil service is enabled")
	}
	s := New(Options{Config: config.Config{TransformEnabled: true}})
	if first := s.o.NewID(); !s.Enabled() || s.o.Now == nil || first == "" || first == s.o.NewID() {
		t.Error("New left defaults unset")
	}
	// error messages: a stored text wins, a code has a default, an INGEST_FAILED code has the ingest sentence
	for _, tc := range []struct{ code, text, want string }{
		{ErrTimeout, "custom", "custom"},
		{ErrTimeout, "", errorMessages[ErrTimeout]},
		{"INGEST_FAILED:DUPLICATE_AUDIO", "", errorMessages[ErrIngestFailedPrefix]},
		{"SOMETHING_NEW", "", "the transform did not finish"},
	} {
		if got := errorMessage(tc.code, tc.text); got != tc.want {
			t.Errorf("errorMessage(%q, %q) = %q", tc.code, tc.text, got)
		}
	}
	// asProblem passes problem errors through and leaves unknown errors alone
	pe := problem.E(404, "NOT_FOUND", "x", nil)
	if !errors.Is(asProblem(pe), pe) || asProblem(nil) != nil {
		t.Error("asProblem altered a problem error")
	}
	// the ladder helpers
	if framesMS(48000, 0) != 0 || ceilDiv(0, 5) != 0 || log2Ceil(1) != 0 || nextPow2(1) != 1 || nextPow2(5) != 8 {
		t.Error("cost helpers")
	}
	if c, err := Canonicalize(Spec{Kind: "nope", Params: jsonRaw(`{}`)}); err == nil {
		t.Errorf("unknown kind canonicalized: %+v", c)
	}
	if _, err := Canonicalize(Spec{Kind: KindGlacier}); err == nil {
		t.Error("a spec without parameters canonicalized")
	}
	if SpecID(KindGlacier, "short") != "" {
		t.Error("SpecID of a short hash")
	}
	if principalName(human) != "op" || principalName(testPrincipalNoName()) != "x" {
		t.Error("principalName")
	}
}

// TestEstimate_FixtureShapes: every Take of the fixture world (the L1, L2 and L3 Takes with and without Picks, A..OCT of T1, the
// reference FLAC, the lossy reference and the multi-part lossy trio) is either an ordinary input of a glacier or refused with the
// stable code of its shape; draft-only Songs have no Take to name; and creating a transform never touches Songs or Picks.
func TestEstimate_FixtureShapes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rows, err := r.st.R.Query(`SELECT t.id, r.engine, t.lossy, (SELECT COUNT(*) FROM song_picks p WHERE p.take_id = t.id) FROM tracks t JOIN recipes r ON r.id = t.recipe_id ORDER BY t.id`)
	if err != nil {
		t.Fatal(err)
	}
	type shape struct {
		id     string
		engine string
		lossy  int
		picked int
	}
	var all []shape
	for rows.Next() {
		var s shape
		if err := rows.Scan(&s.id, &s.engine, &s.lossy, &s.picked); err != nil {
			t.Fatal(err)
		}
		all = append(all, s)
	}
	_ = rows.Close()
	var ok, refRef, picked, unpicked int
	for _, s := range all {
		_, err := r.svc.Estimate(t.Context(), human, glacier(s.id))
		switch {
		case s.engine == "unknown":
			wantProblem(t, err, 422, "RECIPE_UNKNOWN")
			refRef++
		case s.lossy == 1:
			wantProblem(t, err, 422, "LOSSY_MASTER")
		default:
			if err != nil {
				t.Errorf("%s (engine %s) refused: %v", s.id, s.engine, err)
			}
			ok++
			if s.picked > 0 {
				picked++
			} else {
				unpicked++
			}
		}
	}
	if ok < 15 || refRef < 5 {
		t.Errorf("only %d ordinary and %d reference takes in the fixture world", ok, refRef)
	}
	if unpicked == 0 {
		t.Error("the fixture has no Take without a Pick")
	}
	t.Logf("%d ordinary takes (%d picked, %d not), %d references", ok, picked, unpicked, refRef)
	// draft-only Songs have no Take: every draft id is just an unknown take id
	for _, d := range r.fx.Drafts3 {
		_, err := r.svc.Estimate(t.Context(), human, glacier(d))
		wantProblem(t, err, 404, "NOT_FOUND")
	}
	// a transform of a picked Take and of an unpicked one leaves Songs, Picks and the pick ledger alone
	before := map[string]int{}
	for _, tb := range []string{"songs", "song_picks", "pick_events"} {
		before[tb] = r.count(`SELECT COUNT(*) FROM ` + tb)
	}
	for _, s := range all {
		if s.engine != "unknown" && s.lossy == 0 && (s.picked > 0 || s.id == r.tx("A")) {
			r.create(human, glacier(s.id))
			if s.picked > 0 {
				break
			}
		}
	}
	r.create(human, glacier(r.tx("A")))
	for tb, n := range before {
		if got := r.count(`SELECT COUNT(*) FROM ` + tb); got != n {
			t.Errorf("%s: %d -> %d", tb, n, got)
		}
	}
}

// TestChanged_WakesWait: the runner's Changed hook is wired to the hub, so a row change releases a waiter, and a caller's own
// Changed hook still runs.
func TestChanged_WakesWait(t *testing.T) {
	t.Parallel()
	var seen string
	r := newRig(t, func(_ *config.Config, o *Options) { o.Changed = func(id string) { seen = id } })
	v := r.create(human, glacier(r.tx("A"))).View
	ch, cancel := r.svc.hub.subscribe(v.ID)
	defer cancel()
	r.svc.o.Changed(v.ID)
	select {
	case <-ch:
	default:
		t.Error("Changed did not notify the hub")
	}
	if seen != v.ID {
		t.Error("the caller's Changed hook was dropped")
	}
}
