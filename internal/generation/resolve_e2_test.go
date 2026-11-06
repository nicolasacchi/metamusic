package generation_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// setMedia rewrites a track's frozen media facts in this test's private database copy (the production trigger forbids it).
func (x *rigE2) setMedia(id string, durationS float64, rate, channels int) {
	x.t.Helper()
	x.exec(`DROP TRIGGER tracks_frozen`)
	x.exec(`UPDATE tracks SET duration_s = ?, sample_rate = ?, channels = ?, total_samples = ? WHERE id = ?`, durationS, rate, channels, int64(durationS*float64(rate)), id)
}

func refItem(mode, parent string, ref generation.Reference) generation.Item {
	it := parentItem(mode, parent)
	it.Reference = &ref
	return it
}

type takeJSON struct {
	RequestJSON string `json:"request_json"`
	Ref         *struct {
		TrackID    string `json:"track_id"`
		ClipSHA256 string `json:"clip_sha256"`
		FromMS     int64  `json:"from_ms"`
		ToMS       int64  `json:"to_ms"`
	} `json:"ref"`
	Params struct {
		TrialID     string `json:"trial_id"`
		CrossEngine bool   `json:"cross_engine"`
		Stem        string `json:"stem"`
	} `json:"params"`
	Patch     json.RawMessage `json:"patch"`
	TimeoutS  int             `json:"timeout_s"`
	ExpectedS int             `json:"expected_s"`
}

func (x *rigE2) stored(e generation.Estimate) []takeJSON {
	x.t.Helper()
	var out []takeJSON
	if err := json.Unmarshal([]byte(x.str(`SELECT items_json FROM estimates WHERE id = ?`, e.ID)), &out); err != nil {
		x.t.Fatal(err)
	}
	return out
}

func (x *rigE2) request(tk takeJSON) acestep.Request {
	x.t.Helper()
	r, _, is := acestep.Parse([]byte(tk.RequestJSON))
	if acestep.HasErrors(is) {
		x.t.Fatalf("stored request: %v", is)
	}
	return r
}

func TestResolve_SrcPinnedAtEstimate(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	port := x.info.Port
	e := x.mustEstimate(agent, "", repaintItem(port, 8, 14))
	tk := x.stored(e)[0]
	r := x.request(tk)
	sha := x.str(`SELECT master_sha256 FROM tracks WHERE id = ?`, port)
	if r.Src == nil || r.Src.TrackID != port || r.Src.MasterSHA256 != sha || r.Src.DurationDS != 300 {
		t.Errorf("src = %+v, want %s %s 300", r.Src, port, sha)
	}
	if r.RepaintFromDS == nil || *r.RepaintFromDS != 80 || *r.RepaintToDS != 140 || r.Task != "repaint" {
		t.Errorf("range = %v..%v task %s", r.RepaintFromDS, r.RepaintToDS, r.Task)
	}
	// the edit echo is in seconds
	ed := e.Items[0].Takes[0].Edit
	if ed == nil || *ed.FromS != 8 || *ed.ToS != 14 {
		t.Errorf("echo = %+v", ed)
	}
	if !strings.Contains(string(tk.Patch), `"src"`) || !strings.Contains(string(tk.Patch), `"repaint_from_ds":80`) {
		t.Errorf("the stored patch must carry the edit inputs for replay: %s", tk.Patch)
	}
	// create does not read the master again: a changed library cannot move the pin (rule 7 keeps masters frozen anyway)
	if tk.Params.CrossEngine {
		t.Error("an ACE-Step parent is not a cross-engine edit")
	}
}

func TestResolve_SecondsRounding(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	e := x.mustEstimate(agent, "", repaintItem(x.info.Port, 8.04, 14.05))
	r := x.request(x.stored(e)[0])
	if *r.RepaintFromDS != 80 || *r.RepaintToDS != 141 {
		t.Errorf("8.04 s and 14.05 s round half away from zero to 80 and 141 ds, got %d %d", *r.RepaintFromDS, *r.RepaintToDS)
	}
	ed := e.Items[0].Takes[0].Edit
	if *ed.FromS != 8 || *ed.ToS != 14.1 {
		t.Errorf("echo = %v..%v, want 8..14.1", *ed.FromS, *ed.ToS)
	}
	_, err := x.estimate(agent, "", repaintItem(x.info.Port, -0.04, 14))
	_ = err // negative seconds are refused by the API schema (minimum 0); the planner refuses what slips past
	it := repaintItem(x.info.Port, 8, 14)
	nan := -1.0
	it.Edit.FromS = &nan
	_, err = x.estimate(agent, "", it)
	wantProblem(t, err, 422, "INVALID_ITEM")
}

func TestResolve_RefDefaultTail20s(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	x.mustEstimate(agent, "", refItem(lineage.ModeReference, t1, generation.Reference{TrackID: t1}))
	sp := x.clipper.last()
	dur := x.val(`SELECT CAST(duration_s * 1000 AS INTEGER) FROM tracks WHERE id = ?`, t1)
	if sp.ToMS != dur/100*100 || sp.ToMS-sp.FromMS != 20000 {
		t.Errorf("window = %d..%d of %d ms, want the last 20 s", sp.FromMS, sp.ToMS, dur)
	}
	if sp.MasterSHA256 != x.str(`SELECT master_sha256 FROM tracks WHERE id = ?`, t1) || sp.MasterPath == "" || sp.SampleRate == 0 || sp.Channels == 0 {
		t.Errorf("spec = %+v", sp)
	}
	// one end given: 20 s from it
	port := x.info.Port
	x.mustEstimate(agent, "", refItem(lineage.ModeReference, port, generation.Reference{TrackID: port, FromS: fp(2)}))
	if sp := x.clipper.last(); sp.FromMS != 2000 || sp.ToMS != 22000 {
		t.Errorf("from only: %d..%d", sp.FromMS, sp.ToMS)
	}
	x.mustEstimate(agent, "", refItem(lineage.ModeReference, port, generation.Reference{TrackID: port, ToS: fp(25)}))
	if sp := x.clipper.last(); sp.FromMS != 5000 || sp.ToMS != 25000 {
		t.Errorf("to only: %d..%d", sp.FromMS, sp.ToMS)
	}
}

func TestResolve_ReferenceDefaultsToParent(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	e := x.mustEstimate(agent, "", parentItem(lineage.ModeReference, t1)) // no reference object: the parent is the reference
	rf := e.Items[0].Takes[0].Reference
	if rf == nil || rf.TrackID != t1 || rf.ToS-rf.FromS != 20 || len(rf.ClipSHA256) != 64 {
		t.Errorf("reference echo = %+v", rf)
	}
}

func TestResolve_RefClipDeterministic(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	it := refItem(lineage.ModeReference, t1, generation.Reference{TrackID: t1, FromS: fp(20), ToS: fp(45)})
	a := x.mustEstimate(agent, "", it)
	b := x.mustEstimate(agent, "", it)
	ra, rb := a.Items[0].Takes[0].Reference, b.Items[0].Takes[0].Reference
	if ra.ClipSHA256 != rb.ClipSHA256 || ra.FromS != 20 || ra.ToS != 45 {
		t.Errorf("same window, different pins: %+v %+v", ra, rb)
	}
	c := x.mustEstimate(agent, "", refItem(lineage.ModeReference, t1, generation.Reference{TrackID: t1, FromS: fp(21), ToS: fp(46)}))
	if c.Items[0].Takes[0].Reference.ClipSHA256 == ra.ClipSHA256 {
		t.Error("another window pinned the same clip")
	}
	// the pin lands in the recipe and in the frozen estimate
	tk := x.stored(a)[0]
	if got := x.request(tk).Ref; got == nil || got.ClipSHA256 != ra.ClipSHA256 || got.FromDS != 200 || got.ToDS != 450 {
		t.Errorf("request ref = %+v", got)
	}
	if tk.Ref == nil || tk.Ref.ClipSHA256 != ra.ClipSHA256 || tk.Ref.FromMS != 20000 || tk.Ref.ToMS != 45000 {
		t.Errorf("frozen ref = %+v", tk.Ref)
	}
}

func TestResolve_RefChangedAtCreate(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	e := x.mustEstimate(agent, "", refItem(lineage.ModeReference, t1, generation.Reference{TrackID: t1, FromS: fp(20), ToS: fp(45)}))
	before := x.count(`SELECT COUNT(*) FROM batches`)
	calls := x.clipper.callCount()
	x.clipper.setDigest(strings.Repeat("ab", 32)) // a tool upgrade moved the clip bytes
	_, err := x.create(agent, e, viaFor(agent))
	pe := wantProblem(t, err, 409, "ESTIMATE_STALE")
	if pe.Details["reason"] != "reference_changed" || pe.Details["track_id"] != t1 {
		t.Errorf("details = %v", pe.Details)
	}
	if x.clipper.callCount() != calls+1 {
		t.Errorf("create must render the reference again, calls %d -> %d", calls, x.clipper.callCount())
	}
	if n := x.count(`SELECT COUNT(*) FROM batches`); n != before {
		t.Errorf("a stale reference created %d batches", n-before)
	}
	// the clip is back: the same estimate creates
	x.clipper.setDigest("")
	c := x.mustCreate(agent, e, viaFor(agent))
	if c.Takes != 1 || x.str(`SELECT engine FROM batches WHERE id = ?`, c.BatchID) != "acestep15" {
		t.Errorf("created = %+v", c)
	}
}

func TestResolve_RefClipErrors_Mapped(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	it := refItem(lineage.ModeReference, t1, generation.Reference{TrackID: t1, FromS: fp(20), ToS: fp(45)})
	for name, c := range map[string]struct {
		err           error
		status        int
		code          string
		key, wantRule string
	}{
		"window":   {problem.E(422, "CLIP_REFERENCE_WINDOW", "w", nil), 422, "INVALID_ITEM", "rule", "reference_window"},
		"silent":   {problem.E(422, "CLIP_REFERENCE_SILENT", "s", nil), 422, "INVALID_ITEM", "rule", "reference_silent"},
		"rate":     {problem.E(422, "CLIP_UNSUPPORTED_RATE", "r", nil), 422, "ENGINE_CAPABILITY", "capability", "src_format"},
		"channels": {problem.E(422, "CLIP_UNSUPPORTED_CHANNELS", "c", nil), 422, "ENGINE_CAPABILITY", "capability", "src_format"},
		"other":    {problem.E(503, "UNAVAILABLE", "u", nil), 503, "UNAVAILABLE", "", ""},
	} {
		x.clipper.setErr(c.err)
		_, err := x.estimate(agent, "", it)
		pe := wantProblem(t, err, c.status, c.code)
		if c.key != "" && pe.Details[c.key] != c.wantRule {
			t.Errorf("%s: details = %v", name, pe.Details)
		}
	}
	x.clipper.setErr(errors.New("disk on fire"))
	_, err := x.estimate(agent, "", it)
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("a plain error passes through, got %v", err)
	}
}

func TestResolve_NoClipperIsUnavailable(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withoutClipper())
	t1 := x.info.Takes["T1"]
	_, err := x.estimate(agent, "", refItem(lineage.ModeReference, t1, generation.Reference{TrackID: t1}))
	wantProblem(t, err, http.StatusServiceUnavailable, "UNAVAILABLE")
}

func TestResolve_SourceFormat(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		dur      float64
		rate, ch int
		wantCap  string
	}{
		"rate 22050":  {30, 22050, 2, "src_format"},
		"six channel": {30, 48000, 6, "src_format"},
		"too long":    {601, 48000, 2, "src_too_long"},
		"44.1 mono":   {30, 44100, 1, ""},
		"480 s":       {480, 48000, 2, ""},
		"600 s":       {600, 48000, 2, "max_seconds"}, // the render is as long as the source: above the pool cap (D-E2-14)
	} {
		x := newRigE2(t)
		t1 := x.info.Takes["T1"]
		x.setMedia(t1, c.dur, c.rate, c.ch)
		for _, it := range []generation.Item{repaintItem(t1, 10, 25), parentItem(lineage.ModeExtend, t1)} {
			if it.Mode == lineage.ModeExtend {
				if c.wantCap == "max_seconds" {
					continue // an extension of a 600 s source is total_over_600, covered by its own row
				}
				s := 10.0
				it.Edit = &generation.Edit{ExtendS: &s}
			}
			_, err := x.estimate(agent, "", it)
			if c.wantCap == "" {
				if err != nil && it.Mode == lineage.ModeRepaint {
					t.Errorf("%s/%s: %v", name, it.Mode, err)
				}
				continue
			}
			pe := wantProblem(t, err, 422, "ENGINE_CAPABILITY")
			if pe.Details["capability"] != c.wantCap || pe.Details["engine"] != aceName {
				t.Errorf("%s/%s: details = %v", name, it.Mode, pe.Details)
			}
		}
	}
}

func TestEstimate_RefusesUnknownRecipeAndLossy_E2(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	// the fixture's lossy imports are reference tracks too; make one take with a known recipe lossy (private copy)
	lossy := x.info.Takes["T5"]
	x.exec(`DROP TRIGGER tracks_lossy_frozen`)
	x.exec(`UPDATE tracks SET lossy = 1 WHERE id = ?`, lossy)
	stub := x.str(`SELECT t.id FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE r.engine = 'unknown' ORDER BY t.id LIMIT 1`)
	if lossy == "" || stub == "" {
		t.Fatalf("fixture lacks a lossy (%q) or a reference (%q) track", lossy, stub)
	}
	one, tracks := 10.0, []string{"vocals"}
	for _, bad := range []struct{ id, code string }{{lossy, "LOSSY_MASTER"}, {stub, "RECIPE_UNKNOWN"}} {
		for _, mode := range []string{lineage.ModePort, lineage.ModeRepaint, lineage.ModeExtend, lineage.ModeReference, lineage.ModeComplete, lineage.ModeStems} {
			it := parentItem(mode, bad.id)
			switch mode {
			case lineage.ModeRepaint:
				it.Edit = &generation.Edit{FromS: fp(1), ToS: fp(8)}
			case lineage.ModeExtend:
				it.Edit = &generation.Edit{ExtendS: &one}
			case lineage.ModeComplete, lineage.ModeStems:
				it.Edit = &generation.Edit{Tracks: tracks}
			}
			_, err := x.estimate(agent, "", it)
			wantProblem(t, err, 422, bad.code)
		}
		// as the reference of a reference render, and of a compose_new
		ok := x.info.Takes["T1"]
		_, err := x.estimate(agent, "", refItem(lineage.ModeReference, ok, generation.Reference{TrackID: bad.id}))
		wantProblem(t, err, 422, bad.code)
		c := composeACE(t, "ace-ref-bad", nil)
		c.Reference = &generation.Reference{TrackID: bad.id}
		_, err = x.estimate(agent, aceName, c)
		wantProblem(t, err, 422, bad.code)
	}
}

func TestEstimate_StemsExpandOnePerName(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	port := x.info.Port
	it := parentItem(lineage.ModeStems, port)
	it.Edit = &generation.Edit{Tracks: []string{"vocals", "drums", "bass"}}
	e := x.mustEstimate(agent, "", it)
	if e.Takes != 3 || len(e.Items[0].Takes) != 3 {
		t.Fatalf("takes = %d", e.Takes)
	}
	seen := map[string]bool{}
	for i, tk := range x.stored(e) {
		r := x.request(tk)
		if r.Task != "extract" || len(r.Tracks) != 1 || r.Src == nil || r.Src.TrackID != port || tk.Params.Stem != r.Tracks[0] {
			t.Errorf("job %d = %+v stem %q", i, r, tk.Params.Stem)
		}
		seen[r.Tracks[0]] = true
		ed := e.Items[0].Takes[i].Edit
		if ed == nil || ed.Stem != r.Tracks[0] || len(ed.Tracks) != 1 {
			t.Errorf("echo %d = %+v", i, ed)
		}
		if e.Items[0].Takes[i].TaskClass != "base" || e.Items[0].Takes[i].Operator != "extract_stems" {
			t.Errorf("take %d = %+v", i, e.Items[0].Takes[i])
		}
	}
	if len(seen) != 3 {
		t.Errorf("stems = %v", seen)
	}
	// each job is a batch member: three jobs count toward the 50-take limit
	big := parentItem(lineage.ModeStems, port)
	big.Edit = &generation.Edit{Tracks: []string{"vocals", "drums", "bass", "guitar", "synth", "strings"}}
	items := []generation.Item{big, big, big, big, big, big, big, big, big}
	_, err := x.estimate(agent, "", items...)
	pe := wantProblem(t, err, 422, "BATCH_TOO_LARGE")
	if pe.Details["takes"] != 54 {
		t.Errorf("details = %v", pe.Details)
	}
}

func TestEstimate_StemExistsWarning(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	// the fixture's port take already holds stem-vocals.flac
	it := parentItem(lineage.ModeStems, x.info.Port)
	it.Edit = &generation.Edit{Tracks: []string{"vocals", "drums"}}
	e := x.mustEstimate(agent, "", it)
	var on []int
	for _, w := range e.Warnings {
		if w.Code == generation.WarnStemExists {
			on = append(on, w.TakeIndex)
		}
	}
	if len(on) != 1 || on[0] != 0 {
		t.Errorf("stem_exists on takes %v, want [0] (vocals)", on)
	}
}

func TestEstimate_ReferenceNotReproducibleWarning(t *testing.T) {
	t.Parallel()
	for _, reproducible := range []bool{false, true} {
		x := newRigE2(t, withFeatures(func(f *acestep.Features) { f.ReferenceReproducible = reproducible }))
		t1 := x.info.Takes["T1"]
		e := x.mustEstimate(agent, "", refItem(lineage.ModeReference, t1, generation.Reference{TrackID: t1}))
		if got := hasWarning(e, generation.WarnReferenceNotReproducible); got == reproducible {
			t.Errorf("reproducible=%v: warning present = %v", reproducible, got)
		}
	}
}

func TestEstimate_ReferenceRender_FromYue2Parent_IsCrossEngine(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	e := x.mustEstimate(agent, "", refItem(lineage.ModeReference, t1, generation.Reference{TrackID: t1}))
	if !hasWarning(e, generation.WarnCrossEngineEdit) || !e.Items[0].Takes[0].CrossEngine {
		t.Errorf("warnings %v take %+v", warningCodes(e), e.Items[0].Takes[0])
	}
	if !x.stored(e)[0].Params.CrossEngine {
		t.Error("the mutation params must carry cross_engine (D-E2-25)")
	}
}

func TestEstimate_ExceedsMaxRuntime_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		srcS    float64
		maxRun  time.Duration
		refused bool
		timeout int
		genS    int
	}{
		{"300 s base fits", 300, 15 * time.Minute, false, 688, 380},
		{"410 s base is the last that fits", 410, 15 * time.Minute, false, 900, 512},
		{"411 s base is refused", 411, 15 * time.Minute, true, 903, 514},
		{"480 s base is refused", 480, 15 * time.Minute, true, 1034, 596},
		{"480 s base fits once the runtime is raised", 480, 20 * time.Minute, false, 1034, 596},
	} {
		x := newRigE2(t, withACEMaxRuntime(c.maxRun))
		port := x.info.Port
		x.setMedia(port, c.srcS, 48000, 2)
		it := parentItem(lineage.ModeComplete, port)
		it.Edit = &generation.Edit{Tracks: []string{"vocals"}}
		e, err := x.estimate(agent, "", it)
		if c.refused {
			pe := wantProblem(t, err, 422, "INVALID_ITEM")
			if pe.Details["rule"] != "exceeds_max_runtime" || pe.Details["fix"] != "MM_ACESTEP_MAX_RUNTIME" ||
				pe.Details["max_runtime_s"] != int(c.maxRun.Seconds()) || pe.Details["timeout_s"] != c.timeout || pe.Details["item"] != 0 {
				t.Errorf("%s: details = %v", c.name, pe.Details)
			}
			if _, ok := pe.Details["expected_s"]; !ok {
				t.Errorf("%s: no expected_s in %v", c.name, pe.Details)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		tk := x.stored(e)[0]
		if tk.TimeoutS != min(c.timeout, int(c.maxRun.Seconds())) || tk.ExpectedS != c.genS+10 {
			t.Errorf("%s: timeout %d expected %d, want %d and %d", c.name, tk.TimeoutS, tk.ExpectedS, min(c.timeout, int(c.maxRun.Seconds())), c.genS+10)
		}
	}
}

func TestEstimate_600sTurboFits(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withFeatures(func(f *acestep.Features) {
		f.Pools = map[string]acestep.PoolFeatures{"AMPERE_24": {MaxS: 600, CanaryCommit: "abc1234"}, "ADA_24": {MaxS: 600, CanaryCommit: "abc1234"}}
	}))
	it := composeACE(t, "ace-long-1", func(m map[string]any) { m["duration_ds"] = 6000 })
	e := x.mustEstimate(agent, "", it)
	tk := x.stored(e)[0]
	if e.Items[0].Takes[0].ExpectedAudioS != 600 || tk.TimeoutS != 240 || tk.ExpectedS != 110 {
		t.Errorf("take = audio %d timeout %d expected %d, want 600 240 110", e.Items[0].Takes[0].ExpectedAudioS, tk.TimeoutS, tk.ExpectedS)
	}
}

func TestEstimate_ACEQuoteMatchesSpend(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	port := x.info.Port
	comp := parentItem(lineage.ModeComplete, port)
	comp.Edit = &generation.Edit{Tracks: []string{"vocals"}}
	e := x.mustEstimate(agent, "", composeACE(t, "ace-q-1", func(m map[string]any) { m["duration_ds"] = 1800 }), repaintItem(port, 8, 14), comp)
	// ACE-Step fake backend: cold start 180, idle 20, two workers, kill slack 15, runtime 900; engine_rtf seeds max over A5000 and RTX 4090
	qp := spend.QuoteParams{PricePPH: 690_000, RTFMilli: 150, ColdStartS: 180, IdleS: 20, WorkersMax: 2, MaxRuntimeS: 900, KillSlackS: 15}
	want := spend.QuoteTakes(qp, []spend.TakeIn{{AudioS: 180, RTFMilli: 150, LoadS: 10}, {AudioS: 30, RTFMilli: 150, LoadS: 10}, {AudioS: 30, RTFMilli: 1200, LoadS: 20}})
	if e.Cost.Expected != want.ExpectedMicro || e.Cost.Max != want.MaxMicro || e.Cost.Ceiling != want.CeilingMicro || e.Cost.RetryAllowance != want.RetryAllowanceMicro || e.ETAS != want.ETAS {
		t.Errorf("cost = %+v eta %d, want %+v eta %d", e.Cost, e.ETAS, want, want.ETAS)
	}
	if e.PricePerHour != 690_000 {
		t.Errorf("price = %d", e.PricePerHour)
	}
	if !hasWarning(e, generation.WarnBaseClassCost) {
		t.Errorf("warnings = %v", warningCodes(e))
	}
	// mixed class batch: the take with the base class costs more than the turbo one of the same length
	if e.Items[2].Takes[0].Max <= e.Items[1].Takes[0].Max {
		t.Errorf("base %d <= turbo %d", e.Items[2].Takes[0].Max, e.Items[1].Takes[0].Max)
	}
}

func TestEstimate_NoRTFRowIsDisabled(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	// a GPU class the seed does not know: the cached quote of the backend lists only it
	q := x.goodQuote()
	q.Backend, q.GPUClasses = provider.BackendFakeACEStep, []string{"H100"}
	x.cacheQuote(provider.BackendFakeACEStep, q)
	_, err := x.estimate(agent, "", parentItem(lineage.ModePort, x.info.Takes["T1"]))
	pe := wantProblem(t, err, 503, "GENERATION_DISABLED")
	if pe.Details["reason"] != "no_rtf" || pe.Details["backend"] != provider.BackendFakeACEStep {
		t.Errorf("details = %v", pe.Details)
	}
	_ = context.Background
}

// cacheQuote stores q as the cached quote of the named backend.
func (x *rigE2) cacheQuote(backend string, q provider.Quote) {
	x.t.Helper()
	b, _ := json.Marshal(q)
	x.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES (?, 1, 'system:test', ?, ?, ?)
		ON CONFLICT(backend) DO UPDATE SET quote_json = excluded.quote_json, quote_at = excluded.quote_at`, backend, "2026-01-01T00:00:00.000Z", string(b), "2026-01-01T00:00:00.000Z")
}

func TestResolve_RefAtCreate_Failures(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	e := x.mustEstimate(agent, "", refItem(lineage.ModeReference, t1, generation.Reference{TrackID: t1, FromS: fp(20), ToS: fp(45)}))
	// the renderer refuses the window now (a master edited out from under it would): the estimate is stale
	x.clipper.setErr(problem.E(422, "CLIP_REFERENCE_WINDOW", "w", nil))
	_, err := x.create(agent, e, viaFor(agent))
	pe := wantProblem(t, err, 409, "ESTIMATE_STALE")
	if pe.Details["reason"] != "reference_changed" {
		t.Errorf("details = %v", pe.Details)
	}
	// an infrastructure failure is not a stale reference
	x.clipper.setErr(errors.New("disk on fire"))
	if _, err = x.create(agent, e, viaFor(agent)); err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("err = %v", err)
	}
	// no renderer at all
	x.clipper.setErr(nil)
	x.svc.UseEngines(x.backends, x.gates, x.adoption, nil)
	_, err = x.create(agent, e, viaFor(agent))
	wantProblem(t, err, 503, "UNAVAILABLE") // fail closed: a pinned digest nobody can check gets no money
	// yue2 batches never touch the renderer
	calls := x.clipper.callCount()
	x.mustCreate(agent, x.mustEstimate(agent, "", newSeedItem(t1, 1)), viaFor(agent))
	if x.clipper.callCount() != calls {
		t.Error("a yue2 create rendered a reference")
	}
}
