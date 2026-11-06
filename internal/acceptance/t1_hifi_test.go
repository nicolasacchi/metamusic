package acceptance_test

import (
	"math"
	"net/http"
	"slices"
	"testing"
)

// The T1h acceptance (C224.3): master and widen end to end through the real REST surface, the real child and the real ingest.
// master, widen and binaural (lane H2, merged) all run the real child.

func binauralBody(src string) map[string]any {
	return map[string]any{"kind": "binaural", "binaural": map[string]any{"source_id": src, "room": "none"}}
}

func hifiBodies(tr *t1Rig) map[string]map[string]any {
	b := t1Bodies(tr)
	return map[string]map[string]any{"master": b["master"], "widen": b["widen"], "binaural": binauralBody(tr.Info.Tx["A"])}
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
}

// TestAcceptance_T1_HiFi_Chain: master -> widen -> binaural creates three Takes in a line (the source of each is the previous
// result); the third refuses a second binaural (already_binaural) and a master after binaural is allowed with the after_binaural
// warning.
func TestAcceptance_T1_HiFi_Chain(t *testing.T) {
	tr := newT1Rig(t)
	a := tr.Info.Tx["A"]
	m := tr.RunDone(masterBody(a, "neutral"))["id"].(string)
	w := tr.RunDone(widenBody(m))["id"].(string)
	// before binaural: a clean chain, no warning
	est := obj(tr.H.OK(t, "POST", "/api/transforms/estimate", binauralBody(w)))
	codes := func(e map[string]any) (out []string) {
		for _, x := range e["warnings"].([]any) {
			out = append(out, obj(x)["code"].(string))
		}
		return out
	}
	if slices.Contains(codes(est), "after_binaural") || !slices.Contains(codes(est), "binaural_headphones_only") {
		t.Errorf("binaural estimate warnings = %v", codes(est))
	}
	bi := tr.RunDone(binauralBody(w))["id"].(string)
	for child, want := range map[string][3]any{m: {a, "master", 1.0}, w: {m, "widen", 2.0}, bi: {w, "binaural", 3.0}} {
		lin := obj(obj(tr.H.OK(t, "GET", "/api/tracks/"+child, nil))["lineage"])
		if lin["parent_track_id"] != want[0] || lin["operator"] != want[1] || lin["root_track_id"] != a || lin["generation"].(float64) != want[2] {
			t.Errorf("lineage of %s = %v, want %v", want[1], lin, want)
		}
	}
	// a second binaural is refused, on the estimate and on create
	for _, path := range []string{"/api/transforms/estimate", "/api/transforms"} {
		resp := tr.H.Do(t, "POST", path, binauralBody(bi), tr.idem()...)
		e := obj(resp.JSON(t))
		if resp.Status != http.StatusUnprocessableEntity || e["code"] != "INVALID_TRANSFORM" || obj(e["details"])["rule"] != "already_binaural" {
			t.Errorf("%s of a second binaural = %d %v", path, resp.Status, e)
		}
	}
	// a master after binaural is allowed and warned about
	after := obj(tr.H.OK(t, "POST", "/api/transforms/estimate", masterBody(bi, "bright")))
	if !slices.Contains(codes(after), "after_binaural") {
		t.Errorf("master after binaural warnings = %v", codes(after))
	}
	if st, v := tr.Create(tr.H, masterBody(bi, "bright")); st != http.StatusAccepted {
		t.Errorf("create master after binaural = %d %v", st, v)
	}
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
}
