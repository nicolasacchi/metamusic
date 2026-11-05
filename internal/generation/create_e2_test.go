package generation_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

func TestAdmission_UnadoptedEngineRefuses(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withoutAdoption())
	before := x.count(`SELECT COUNT(*) FROM estimates`)
	for _, it := range []generation.Item{parentItem(lineage.ModePort, x.info.Takes["T1"]), newSeedItem(x.info.Port, 1), composeACE(t, "ace-na-1", nil)} {
		_, err := x.estimate(agent, "", it)
		pe := wantProblem(t, err, 409, "ENGINE_NOT_ADOPTED")
		if pe.Details["engine"] != aceName || pe.Details["hint"] == "" {
			t.Errorf("details = %v", pe.Details)
		}
	}
	if n := x.count(`SELECT COUNT(*) FROM estimates`); n != before {
		t.Errorf("a refused estimate wrote %d rows", n-before)
	}
	// yue2 is untouched by the gate
	if _, err := x.estimate(agent, "", newSeedItem(x.info.Takes["T1"], 1)); err != nil {
		t.Errorf("yue2: %v", err)
	}
}

func TestAdmission_RevokedBetweenEstimateAndCreate(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	e := x.mustEstimate(agent, "", parentItem(lineage.ModePort, x.info.Takes["T1"]))
	x.adoption.set(aceName, false)
	batches, ledger := x.count(`SELECT COUNT(*) FROM batches`), x.count(`SELECT COUNT(*) FROM ledger`)
	_, err := x.create(agent, e, viaFor(agent))
	wantProblem(t, err, 409, "ENGINE_NOT_ADOPTED")
	if x.count(`SELECT COUNT(*) FROM batches`) != batches || x.count(`SELECT COUNT(*) FROM ledger`) != ledger {
		t.Error("money moved for an engine that is no longer adopted")
	}
	// adopted again: the token was rolled back with the refusal and works
	x.adoption.set(aceName, true)
	x.mustCreate(agent, e, viaFor(agent))
}

func TestAdmission_NoGateFailsClosed(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withoutAdoption())
	x.svc.UseEngines(x.backends, x.gates, nil, x.clipper)
	_, err := x.estimate(agent, "", parentItem(lineage.ModePort, x.info.Takes["T1"]))
	wantProblem(t, err, 409, "ENGINE_NOT_ADOPTED")
}

func TestTrialID_Plumbing(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	e, err := x.svc.Estimate(ctxBG(), agent, generation.EstimateIn{Engine: aceName, TrialID: "trial-1", Via: viaFor(agent), Items: []generation.Item{parentItem(lineage.ModePort, t1)}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(x.adoption.trials, "trial-1") {
		t.Errorf("the gate saw trials %v", x.adoption.trials)
	}
	if tk := x.stored(e)[0]; tk.Params.TrialID != "trial-1" {
		t.Errorf("params.trial_id = %q", tk.Params.TrialID)
	}
	c := x.mustCreate(agent, e, viaFor(agent))
	x.hook.mu.Lock()
	calls := slices.Clone(x.hook.calls)
	x.hook.mu.Unlock()
	if len(calls) != 1 || calls[0] != [2]string{"trial-1", c.BatchID} {
		t.Errorf("hook calls = %v", calls)
	}
	var mut struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(x.str(`SELECT mutation_json FROM jobs WHERE batch_id = ?`, c.BatchID)), &mut); err != nil {
		t.Fatal(err)
	}
	if mut.Params["trial_id"] != "trial-1" || mut.Params["cross_engine"] != true {
		t.Errorf("mutation params = %v", mut.Params)
	}
	// without a trial the hook stays quiet
	e2 := x.mustEstimate(agent, "", parentItem(lineage.ModePort, t1))
	x.mustCreate(agent, e2, viaFor(agent))
	if len(x.hook.calls) != 1 {
		t.Errorf("hook calls = %v", x.hook.calls)
	}
	// a trial names a candidate engine
	_, err = x.svc.Estimate(ctxBG(), agent, generation.EstimateIn{TrialID: "trial-1", Via: viaFor(agent), Items: []generation.Item{newSeedItem(t1, 1)}})
	pe := wantProblem(t, err, 422, "VALIDATION_FAILED")
	if pe.Details["field"] != "options.trial_id" {
		t.Errorf("details = %v", pe.Details)
	}
}

func TestBackend_Routing(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	port := parentItem(lineage.ModePort, t1)
	for name, c := range map[string]struct {
		engine, backend string
		items           []generation.Item
		wantErr         bool
	}{
		"ace default":     {aceName, "", []generation.Item{port}, false},
		"ace named":       {aceName, provider.BackendFakeACEStep, []generation.Item{port}, false},
		"ace on yue2 one": {aceName, provider.BackendFake, []generation.Item{port}, true},
		"ace unknown":     {aceName, "nope", []generation.Item{port}, true},
		"yue2 default":    {"", "", []generation.Item{newSeedItem(t1, 1)}, false},
		"yue2 named":      {"", provider.BackendFake, []generation.Item{newSeedItem(t1, 1)}, false},
		"yue2 on ace one": {"", provider.BackendFakeACEStep, []generation.Item{newSeedItem(t1, 1)}, true},
	} {
		e, err := x.svc.Estimate(ctxBG(), agent, generation.EstimateIn{Engine: c.engine, Backend: c.backend, Items: c.items, Via: viaFor(agent)})
		if c.wantErr {
			pe := wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
			if pe.Details["backend"] != c.backend {
				t.Errorf("%s: details = %v", name, pe.Details)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		want := provider.BackendFake
		if c.engine == aceName {
			want = provider.BackendFakeACEStep
		}
		if e.Backend != want {
			t.Errorf("%s: backend = %q, want %q", name, e.Backend, want)
		}
	}
}

func TestBackend_MissingSetIsUnavailable(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withoutGates())
	_, err := x.estimate(agent, "", parentItem(lineage.ModePort, x.info.Takes["T1"]))
	pe := wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
	if pe.Details["engine"] != aceName {
		t.Errorf("details = %v", pe.Details)
	}
	bare := newRigE2(t)
	bare.svc.UseEngines(nil, nil, bare.adoption, bare.clipper)
	_, err = bare.estimate(agent, "", parentItem(lineage.ModePort, bare.info.Takes["T1"]))
	wantProblem(t, err, 422, "BACKEND_UNAVAILABLE")
	// yue2 does not need the set
	if _, err := bare.estimate(agent, "", newSeedItem(bare.info.Takes["T1"], 1)); err != nil {
		t.Errorf("yue2: %v", err)
	}
}

func TestBackend_DisabledGateNamesTheBackend(t *testing.T) {
	t.Parallel()
	x := newRigE2(t, withACEGate(provider.ModeRunPod, false))
	_, err := x.estimate(agent, "", parentItem(lineage.ModePort, x.info.Takes["T1"]))
	pe := wantProblem(t, err, 503, "GENERATION_DISABLED")
	if pe.Details["reason"] != provider.ReasonKeysAbsent || pe.Details["backend"] != provider.BackendFakeACEStep {
		t.Errorf("details = %v", pe.Details)
	}
	// the yue2 gate is a different one and still enabled
	if _, err := x.estimate(agent, "", newSeedItem(x.info.Takes["T1"], 1)); err != nil {
		t.Errorf("yue2: %v", err)
	}
}

func TestCreate_BackendDisarmed_PerBackend(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	ace := x.mustEstimate(agent, "", parentItem(lineage.ModePort, t1))
	yue := x.mustEstimate(agent, "", newSeedItem(t1, 1))
	x.exec(`UPDATE provider_state SET armed = 0 WHERE backend = 'fake-acestep'`)
	batches := x.count(`SELECT COUNT(*) FROM batches`)
	_, err := x.create(agent, ace, viaFor(agent))
	pe := wantProblem(t, err, 503, "PROVIDER_DISARMED")
	if pe.Details["backend"] != provider.BackendFakeACEStep {
		t.Errorf("details = %v", pe.Details)
	}
	if x.count(`SELECT COUNT(*) FROM batches`) != batches {
		t.Error("a disarmed backend created a batch")
	}
	// the other backend is unaffected
	x.mustCreate(agent, yue, viaFor(agent))
	// a tripped ACE backend names itself as well
	x.exec(`UPDATE provider_state SET armed = 1 WHERE backend = 'fake-acestep'`)
	x.exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES ('trip-1', 'fake-acestep', ?, 'crash_loop', '{}', '[]')`, "2026-10-06T11:00:00.000Z")
	_, err = x.create(agent, ace, viaFor(agent))
	pe = wantProblem(t, err, 503, "PROVIDER_TRIPPED")
	if pe.Details["backend"] != provider.BackendFakeACEStep {
		t.Errorf("details = %v", pe.Details)
	}
}

func TestCreate_BackendLeftTheSet(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	e := x.mustEstimate(agent, "", parentItem(lineage.ModePort, x.info.Takes["T1"]))
	x.svc.UseEngines(x.backends, nil, x.adoption, x.clipper) // the ACE-Step gate is gone
	_, err := x.create(agent, e, viaFor(agent))
	pe := wantProblem(t, err, 409, "ESTIMATE_STALE")
	if pe.Details["reason"] != "backend_changed" {
		t.Errorf("details = %v", pe.Details)
	}
}

func TestCreate_E2_RowsAndSpend(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	port := x.info.Port
	it := parentItem(lineage.ModeStems, port)
	it.Edit = &generation.Edit{Tracks: []string{"vocals", "drums"}}
	e := x.mustEstimate(human, "", it, repaintItem(port, 8, 14))
	c := x.mustCreate(human, e, viaFor(human))
	if c.Takes != 3 || len(c.JobIDs) != 3 {
		t.Fatalf("created = %+v", c)
	}
	if got := x.str(`SELECT engine || '/' || backend FROM batches WHERE id = ?`, c.BatchID); got != "acestep15/fake-acestep" {
		t.Errorf("batch = %s", got)
	}
	rows, err := x.st.R.Query(`SELECT j.engine, j.backend, j.operator, j.kind, j.expected_audio_s, r.engine, json_extract(j.mutation_json, '$.params.stem'), j.reserved_micro_usd
		FROM jobs j JOIN recipes r ON r.id = j.recipe_id WHERE j.batch_id = ? ORDER BY j.take_index`, c.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ops, stems []string
	var reserved int64
	for rows.Next() {
		var en, be, op, kind, ren string
		var audio int
		var stem *string
		var res int64
		if err := rows.Scan(&en, &be, &op, &kind, &audio, &ren, &stem, &res); err != nil {
			t.Fatal(err)
		}
		if en != aceName || be != "fake-acestep" || ren != aceName || kind != "generate" || audio != 30 {
			t.Errorf("job = %s %s %s %s %d", en, be, op, kind, audio)
		}
		ops = append(ops, op)
		if stem != nil {
			stems = append(stems, *stem)
		}
		reserved += res
	}
	if !slices.Equal(ops, []string{"extract_stems", "extract_stems", "repaint"}) || !slices.Equal(stems, []string{"vocals", "drums"}) {
		t.Errorf("operators %v stems %v", ops, stems)
	}
	// the reserve is the estimate's: integer micro-USD, the take reserves sum to what the batch holds
	if reserved <= 0 || x.val(`SELECT SUM(micro_usd) FROM ledger WHERE batch_id = ? AND kind = 'reserve'`, c.BatchID) != reserved {
		t.Errorf("reserved %d vs ledger", reserved)
	}
	if x.val(`SELECT ceiling_micro_usd FROM batches WHERE id = ?`, c.BatchID) != int64(e.Cost.Ceiling) {
		t.Error("the batch ceiling is not the estimate's")
	}
}

func TestCreate_E2_ApprovalRulesUnchanged(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	// an agent above its auto-approve limits needs a human, exactly as for yue2
	items := make([]generation.Item, 0, 8)
	for range 8 {
		items = append(items, parentItem(lineage.ModePort, x.info.Takes["T1"]))
	}
	e := x.mustEstimate(agent, "", items...)
	if !e.RequiresApproval {
		t.Skip("limits are generous in this fixture")
	}
	c := x.mustCreate(agent, e, viaFor(agent))
	if c.State != "awaiting_confirmation" {
		t.Errorf("state = %s", c.State)
	}
}
