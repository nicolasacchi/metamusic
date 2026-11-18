package generation_test

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// Remix = extract + complete(source_stem): the complete item may name an extracted stem of its parent as the source audio.

func stemSHA(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (x *rigE2) addStem(track, name, content string) string {
	x.t.Helper()
	sha := stemSHA(content)
	x.exec(`INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?,?,?,?,?,?)`,
		track, name, sha, 1024, "takes/x/"+name, "2026-11-01T00:00:00Z")
	return sha
}

func completeStem(parent, stem string, tracks ...string) generation.Item {
	it := parentItem(lineage.ModeComplete, parent)
	it.Edit = &generation.Edit{Tracks: tracks, SourceStem: stem}
	return it
}

func TestExpandE2_CompleteSourceStem_PinsStemDigest(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	port := x.info.Port
	stem := x.str(`SELECT sha256 FROM artifacts WHERE track_id = ? AND name = 'stem-vocals.flac'`, port)
	master := x.str(`SELECT master_sha256 FROM tracks WHERE id = ?`, port)
	e := x.mustEstimate(agent, "", completeStem(port, "vocals", "drums", "bass"))
	tk := x.stored(e)[0]
	r := x.request(tk)
	if r.Task != "complete" || !slices.Equal(r.Tracks, []string{"bass", "drums"}) { // the request keeps tracks sorted
		t.Errorf("task %s tracks %v", r.Task, r.Tracks)
	}
	if r.Src == nil || r.Src.MasterSHA256 != stem || r.Src.MasterSHA256 == master || r.Src.TrackID != port || r.Src.DurationDS != 300 {
		t.Errorf("src = %+v, want stem digest %s of %s", r.Src, stem, port)
	}
	if !strings.Contains(string(tk.Patch), `"source_stem":"vocals"`) {
		t.Errorf("patch must record the stem: %s", tk.Patch)
	}
	if got := e.Items[0].Takes[0].Edit; got == nil || got.SourceStem != "vocals" {
		t.Errorf("echo = %+v", got)
	}
}

func TestExpandE2_CompleteSourceStem_Missing(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	_, err := x.estimate(agent, "", completeStem(x.info.Port, "drums", "bass"))
	pe := wantProblem(t, err, 422, "INVALID_ITEM")
	if pe.Details["rule"] != "source_stem_missing" || pe.Details["field"] != "edit.source_stem" {
		t.Errorf("details = %v", pe.Details)
	}
	if !strings.Contains(pe.Message, "stems") {
		t.Errorf("message should tell the caller to run a stems item: %q", pe.Message)
	}
}

func TestExpandE2_CompleteSourceStem_PicksNewestNumbered(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	port := x.info.Port
	x.addStem(port, "stem-vocals-2.flac", "second")
	x.addStem(port, "stem-vocals-10.flac", "tenth")
	e := x.mustEstimate(agent, "", completeStem(port, "vocals", "drums"))
	if got := x.request(x.stored(e)[0]).Src.MasterSHA256; got != stemSHA("tenth") {
		t.Errorf("src sha = %s, want the numerically newest (-10)", got)
	}
}

func TestExpandE2_SourceStem_NotAllowedOutsideComplete(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	port, t1 := x.info.Port, x.info.Takes["T1"]
	mk := func(mode, parent string) generation.Item {
		it := parentItem(mode, parent)
		it.Edit = &generation.Edit{SourceStem: "vocals"}
		switch mode {
		case lineage.ModeRepaint:
			it.Edit.FromS, it.Edit.ToS = fp(8), fp(14)
		case lineage.ModeExtend:
			it.Edit.ExtendS = fp(30)
		case lineage.ModeStems:
			it.Edit.Tracks = []string{"vocals"}
		}
		return it
	}
	for _, c := range []struct{ mode, parent string }{
		{lineage.ModeRepaint, port}, {lineage.ModeExtend, port}, {lineage.ModeStems, port}, {lineage.ModePort, t1},
	} {
		_, err := x.estimate(agent, "", mk(c.mode, c.parent))
		pe := wantProblem(t, err, 422, "INVALID_ITEM")
		if c.mode != lineage.ModePort && pe.Details["rule"] != "not_allowed_for_"+c.mode {
			t.Errorf("%s: details = %v", c.mode, pe.Details)
		}
	}
}

func TestExpandE2_SourceStem_UnknownName(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	_, err := x.estimate(agent, "", completeStem(x.info.Port, "kazoo", "drums"))
	pe := wantProblem(t, err, 422, "INVALID_ITEM")
	if pe.Details["rule"] != "unknown_track_name" || pe.Details["field"] != "edit.source_stem" {
		t.Errorf("details = %v", pe.Details)
	}
}

func TestExpandE2_CompleteStyleOverridesCaption(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	x.addStem(t1, "stem-vocals.flac", "t1 vocals")
	it := completeStem(t1, "vocals", "drums", "bass")
	it.Patch = &generation.Patch{Style: strp("lo-fi hip hop, mellow")}
	r := x.request(x.stored(x.mustEstimate(agent, aceName, it))[0])
	if r.Caption != "lo-fi hip hop, mellow" || r.Instrumental || r.Lyrics == "" {
		t.Errorf("caption %q instrumental %v lyrics %q", r.Caption, r.Instrumental, r.Lyrics)
	}
	if r.Src == nil || r.Src.MasterSHA256 != stemSHA("t1 vocals") || r.Src.TrackID != t1 {
		t.Errorf("src = %+v", r.Src)
	}
}

func TestExpandE2_CompleteSourceStem_HashesAndEstimateVsFullMix(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	port := x.info.Port
	full := parentItem(lineage.ModeComplete, port)
	full.Edit = &generation.Edit{Tracks: []string{"drums"}}
	ef := x.mustEstimate(agent, "", full)
	es := x.mustEstimate(agent, "", completeStem(port, "vocals", "drums"))
	tkf, tks := x.stored(ef)[0], x.stored(es)[0]
	rf, rs := x.request(tkf), x.request(tks)
	if rf.Src.MasterSHA256 == rs.Src.MasterSHA256 {
		t.Fatal("the stem must change the pinned src digest")
	}
	cf, rdf := lineage.Keys([]byte(tkf.RequestJSON))
	cs, rds := lineage.Keys([]byte(tks.RequestJSON))
	if cf == cs || rdf == rds {
		t.Errorf("content/render hash must differ from a full-mix complete: %s/%s %s/%s", cf, cs, rdf, rds)
	}
	tf, ts := ef.Items[0].Takes[0], es.Items[0].Takes[0]
	if tf.ExpectedAudioS != ts.ExpectedAudioS || tf.TaskClass != "base" || ts.TaskClass != "base" || ef.Cost != es.Cost {
		t.Errorf("estimate differs: %+v %+v cost %v %v", tf, ts, ef.Cost, es.Cost)
	}
}

func TestExpandE2_CompleteSourceStem_Warnings(t *testing.T) {
	t.Parallel()
	x := newRigE2(t)
	t1 := x.info.Takes["T1"]
	x.addStem(t1, "stem-vocals.flac", "t1 vocals")
	e := x.mustEstimate(agent, aceName, completeStem(t1, "vocals", "drums"))
	for _, w := range []string{generation.WarnStemSource, generation.WarnCrossEngineEdit, generation.WarnBaseClassCost} {
		if !hasWarning(e, w) {
			t.Errorf("missing %s, got %v", w, warningCodes(e))
		}
	}
	e = x.mustEstimate(agent, "", func() generation.Item {
		it := parentItem(lineage.ModeComplete, x.info.Port)
		it.Edit = &generation.Edit{Tracks: []string{"drums"}}
		return it
	}())
	if hasWarning(e, generation.WarnStemSource) {
		t.Error("a full-mix complete must not carry source_is_stem")
	}
}
