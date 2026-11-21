package transform_test

// go-media tier of T1b: the real fluidsynth renders a score with the generated test soundfont (dsptest.WriteSF2), the real ffmpeg
// decodes it, the real `metamusic dsp-run` child finishes it and the real M1 pipeline ingests the Take. These skip when fluidsynth
// is absent unless MM_REQUIRE_FLUIDSYNTH=1 (CI sets it beside the apt line DESK-T3 adds), and under -short.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/score/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

func requireFluidsynthBin(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("media tier: skipped under -short")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	bin := testtools.Tools().Fluidsynth
	if _, err := exec.LookPath(bin); err != nil {
		if os.Getenv("MM_REQUIRE_FLUIDSYNTH") == "1" {
			t.Fatal("fluidsynth required (MM_REQUIRE_FLUIDSYNTH=1) but not found")
		}
		t.Skip("media tool \"fluidsynth\" not found")
	}
	return bin
}

// scoreMediaRig is a rig with every tool real.
func scoreMediaRig(t *testing.T) *rig {
	t.Helper()
	fsBin := requireFluidsynthBin(t)
	tools := testtools.Tools()
	bin := childBinary(t)
	sf := filepath.Join(t.TempDir(), "gm.sf2")
	if err := dsptest.WriteSF2(sf, dsptest.SF2Options{Presets: 128}); err != nil {
		t.Fatal(err)
	}
	fl, err := synth.NewFluidsynth(context.Background(), fsBin)
	if err != nil {
		t.Fatal(err)
	}
	return newRig(t, false, func(o *transform.Options) {
		an, err := ffmpeg.New(context.Background(), tools)
		if err != nil {
			t.Fatal(err)
		}
		o.Media = transform.ToolMedia{Tools: tools}
		o.Tools = tools
		o.Pipeline = ingest.New(o.Data, o.DataDir, tools, an, o.Now)
		o.Ears = &fakeEars{}
		o.Child = &transform.ExecChild{Self: bin}
		o.ToolStatus = func() transform.ToolStatus {
			return transform.ToolStatus{FFmpeg: true, FLAC: true, Fluidsynth: true, FluidsynthVersion: fl.Info().Version}
		}
		o.Score = &transform.ScoreSupport{Synth: fl, Info: fl.Info(), Fonts: synth.NewSet([]synth.Font{{Name: "gm", Path: sf}}, nil)}
	})
}

const mediaTune = "X:1\nT:Synthetic media\nM:4/4\nL:1/4\nQ:1/4=120\nK:C\n\"C\"CDEF|\"G\"GABc|\n"

// TestScoreRender_EndToEndTake: a score renders to a rootless Take with the three artifacts and no scores row, of the length the
// score and the tail promise.
func TestScoreRender_EndToEndTake(t *testing.T) {
	r := scoreMediaRig(t)
	id := r.createScore(mediaTune, `,"tail_s":1`)
	if !r.runOne() {
		t.Fatal("nothing claimed")
	}
	if row := r.row(id); row.State != "done" || len(row.PCM) != 64 {
		t.Fatalf("row %+v", row)
	}
	var dur float64
	var parent string
	if err := r.st.R.QueryRow(`SELECT duration_s, COALESCE(parent_track_id,'') FROM tracks WHERE id = ?`, id).Scan(&dur, &parent); err != nil {
		t.Fatal(err)
	}
	if parent != "" || dur < 4.9 || dur > 5.3 {
		t.Errorf("parent %q duration %.3f s, want a rootless take of about 5 s (4 s of score and a 1 s tail)", parent, dur)
	}
	for _, f := range []string{"score.abc", "score.mid", "transform.json"} {
		if !r.exists("takes/"+id+"/"+f) && !r.exists("takes/"+id+"/artifacts/"+f) {
			t.Errorf("artifact %s missing", f)
		}
	}
	if n := r.count(`SELECT count(*) FROM scores WHERE track_id = ?`, id); n != 0 {
		t.Errorf("%d scores rows", n)
	}
	rep, err := r.svc.Verify(context.Background(), id)
	if err != nil || !rep.Match || rep.Inconclusive {
		t.Errorf("verify: %+v %v", rep, err)
	}
}

// TestScoreRender_DeterministicPCM: the same spec in a second library renders the same PCM, byte for byte.
func TestScoreRender_DeterministicPCM(t *testing.T) {
	a, b := scoreMediaRig(t), scoreMediaRig(t)
	ida, idb := a.createScore(mediaTune, ""), b.createScore(mediaTune, "")
	a.runOne()
	b.runOne()
	ra, rb := a.row(ida), b.row(idb)
	if ra.State != "done" || rb.State != "done" || ra.PCM != rb.PCM {
		t.Errorf("pcm %s (%s) vs %s (%s)", ra.PCM, ra.State, rb.PCM, rb.State)
	}
}

// TestScoreRender_ChordsBlockAddsVoice: chords block adds the chord voice to the render (and so changes the audio); none keeps the
// render literal to the notes.
func TestScoreRender_ChordsBlockAddsVoice(t *testing.T) {
	r := scoreMediaRig(t)
	none := r.createScore(mediaTune, `,"tail_s":0.5`)
	block := r.createScore(mediaTune, `,"tail_s":0.5,"chords":"block"`)
	r.runOne()
	r.runOne()
	voices := func(id string) int {
		b, err := r.data.ReadFile("takes/" + id + "/transform.json")
		if err != nil {
			b, err = r.data.ReadFile("takes/" + id + "/artifacts/transform.json")
		}
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Voices []json.RawMessage `json:"voices"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		return len(doc.Voices)
	}
	if n, bl := voices(none), voices(block); n != 1 || bl != 2 {
		t.Errorf("voices: none %d, block %d, want 1 and 2", n, bl)
	}
	if r.row(none).PCM == r.row(block).PCM || r.row(none).PCM == "" {
		t.Error("the chord voice did not change the audio")
	}
}
