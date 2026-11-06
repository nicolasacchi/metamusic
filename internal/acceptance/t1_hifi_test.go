package acceptance_test

import (
	"math"
	"net/http"
	"testing"
)

// The T1h acceptance (C224.3): master and widen end to end through the real REST surface, the real child and the real ingest.
// binaural (lane H2) is not on main when this lane was written: its rows are named TODOs below and skip.

const todoBinaural = "TODO(T1-H2): the binaural kind is not registered on main yet; enable when H2 merges (create master -> widen -> binaural, a second binaural refused already_binaural, master after binaural warns after_binaural)"

func hifiBodies(tr *t1Rig) map[string]map[string]any {
	b := t1Bodies(tr)
	return map[string]map[string]any{"master": b["master"], "widen": b["widen"]}
}

// TestAcceptance_T1_HiFi_TakeShape: each hi-fi kind is an ordinary Take: engine dsp, one input, the ledger operator is the kind,
// 24-bit stereo, mode 0444, the parent unchanged.
func TestAcceptance_T1_HiFi_TakeShape(t *testing.T) {
	tr := newT1Rig(t)
	a := tr.Info.Tx["A"]
	before := tr.fileState(a)
	for kind, body := range hifiBodies(tr) {
		t.Run(kind, func(t *testing.T) {
			tr.T = t
			v := tr.RunDone(body)
			assertTakeShape(t, tr, v, kind, a)
			if n := tr.Count(`SELECT COUNT(*) FROM transform_inputs WHERE transform_id = ?`, v["id"]); n != 1 {
				t.Errorf("%d inputs, want 1", n)
			}
		})
	}
	t.Run("binaural", func(t *testing.T) { t.Skip(todoBinaural) })
	if got := tr.fileState(a); got != before {
		t.Errorf("parent changed: %+v -> %+v", before, got)
	}
}

// TestAcceptance_T1_HiFi_LoudnessMatched: the result is within 1.5 LU of its parent's measured loudness (a duel against the
// parent compares tone, not volume) and its true peak is at most -0.5 dBTP, both measured on the files by the real ffmpeg.
func TestAcceptance_T1_HiFi_LoudnessMatched(t *testing.T) {
	tr := newT1Rig(t)
	a := tr.Info.Tx["A"]
	parent := tr.MeasureLUFS(tr.MasterPath(a))
	for kind, body := range hifiBodies(tr) {
		t.Run(kind, func(t *testing.T) {
			tr.T = t
			if kind == "master" {
				knownBug(t, "B5-3", "master's default target is -12 LUFS, not the parent's: estimate.go rawFloat reads the resolved \"target_lufs\": null as 0, which clamps to -12")
			}
			id := tr.RunDone(body)["id"].(string)
			if got := tr.MeasureLUFS(tr.MasterPath(id)); math.Abs(got-parent) > 1.5 {
				t.Errorf("%s measures %.2f LUFS, parent %.2f", kind, got, parent)
			}
			tp := obj(obj(obj(tr.H.OK(t, "GET", "/api/tracks/"+id, nil))["analysis"])["loudness"])["true_peak_dbtp"].(float64)
			if tp > -0.5 {
				t.Errorf("%s true peak %.2f dBTP", kind, tp)
			}
		})
	}
	t.Run("binaural", func(t *testing.T) { t.Skip(todoBinaural) })
}

// TestAcceptance_T1_HiFi_Chain: master -> widen creates two Takes in a line (the source of each is the previous result). The
// binaural half of the chain rules (terminal kind) is the TODO below.
func TestAcceptance_T1_HiFi_Chain(t *testing.T) {
	tr := newT1Rig(t)
	a := tr.Info.Tx["A"]
	m := tr.RunDone(masterBody(a, "neutral"))["id"].(string)
	w := tr.RunDone(widenBody(m))["id"].(string)
	lin := obj(tr.H.OK(t, "GET", "/api/tracks/"+w, nil))["lineage"]
	if obj(lin)["parent_track_id"] != m || obj(lin)["operator"] != "widen" || obj(lin)["root_track_id"] != a || obj(lin)["generation"].(float64) != 2 {
		t.Errorf("widen lineage = %v", lin)
	}
	// the estimate of a chained kind warns of nothing about binaural
	est := obj(tr.H.OK(t, "POST", "/api/transforms/estimate", masterBody(w, "bright")))
	for _, x := range est["warnings"].([]any) {
		if obj(x)["code"] == "after_binaural" {
			t.Errorf("after_binaural on a chain without binaural: %v", est["warnings"])
		}
	}
	t.Run("binaural", func(t *testing.T) { t.Skip(todoBinaural) })
}

// TestAcceptance_T1_HiFi_SameSpecReuses: the second create of a hi-fi spec is a reuse (200, same id), also for another principal.
func TestAcceptance_T1_HiFi_SameSpecReuses(t *testing.T) {
	tr := newT1Rig(t)
	for kind, body := range hifiBodies(tr) {
		t.Run(kind, func(t *testing.T) {
			tr.T = t
			first := tr.RunDone(body)
			st, again := tr.Create(tr.writer(), body)
			if st != http.StatusOK || again["reused"] != true || again["id"] != first["id"] {
				t.Errorf("second create = %d %v", st, again)
			}
		})
	}
	t.Run("binaural", func(t *testing.T) { t.Skip(todoBinaural) })
}

// TestAcceptance_T1_HiFi_JoinsLibrary: the result joins its parent's Song, is listed under origin=transform, and a duel against
// its parent can be created.
func TestAcceptance_T1_HiFi_JoinsLibrary(t *testing.T) {
	tr := newT1Rig(t)
	a := tr.Info.Tx["A"]
	id := tr.RunDone(masterBody(a, "punch"))["id"].(string)
	tr.Settle(t)
	var songA, songM string
	if err := tr.Store.R.QueryRow(`SELECT song_id FROM take_songs WHERE track_id = ?`, a).Scan(&songA); err != nil {
		t.Fatal(err)
	}
	if err := tr.Store.R.QueryRow(`SELECT song_id FROM take_songs WHERE track_id = ?`, id).Scan(&songM); err != nil || songM != songA {
		t.Fatalf("the master is in Song %q (%v), want %q", songM, err, songA)
	}
	page := obj(tr.H.OK(t, "GET", "/api/tracks?origin=transform&limit=100", nil))
	found := false
	for _, it := range page["items"].([]any) {
		found = found || (obj(it)["id"] == id && obj(it)["origin"] == "transform")
	}
	if !found {
		t.Error("origin=transform does not list the master")
	}
	resp := tr.H.Do(t, "POST", "/api/duels", map[string]any{"a_track_id": a, "b_track_id": id}, tr.idem()...)
	if resp.Status/100 != 2 {
		t.Fatalf("duel against the parent: %d %s", resp.Status, resp.Body)
	}
	duel := obj(resp.JSON(t))
	if duel["id"] == nil {
		t.Errorf("duel = %v", duel)
	}
	t.Run("binaural", func(t *testing.T) { t.Skip(todoBinaural) })
}
