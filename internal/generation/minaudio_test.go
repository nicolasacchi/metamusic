package generation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/songwriting"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

type fixedTargets struct{ t songwriting.Targets }

func (f fixedTargets) Get(context.Context) (songwriting.Targets, error) { return f.t, nil }

// minAudioOf reads items_json min_audio_s of every take of an estimate.
func (r *rig) minAudioOf(estimateID string) []int {
	r.t.Helper()
	var raw string
	if err := r.st.R.QueryRow(`SELECT items_json FROM estimates WHERE id = ?`, estimateID).Scan(&raw); err != nil {
		r.t.Fatal(err)
	}
	var docs []struct {
		MinAudioS int `json:"min_audio_s"`
	}
	if err := json.Unmarshal([]byte(raw), &docs); err != nil {
		r.t.Fatal(err)
	}
	out := make([]int, len(docs))
	for i, d := range docs {
		out[i] = d.MinAudioS
	}
	return out
}

// Without a songwriting source the control is off: nothing is forced, nothing recorded.
func TestEstimate_MinAudioOffWithoutSongwriting(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e := r.est(human, newSeedItem(lt.TrackLuna, 2))
	for i, m := range r.minAudioOf(e.ID) {
		if m != 0 || e.Items[0].Takes[i].MinAudioS != 0 {
			t.Errorf("take %d: min_audio_s = %d / %d, want 0", i, m, e.Items[0].Takes[i].MinAudioS)
		}
	}
}

// The floor min(max(min_duration_s, 0.7 x estimate), 330) is frozen into the estimate, shown in the plan, never
// quoted below, and priced: a longer forced render costs more than the same take without the control.
func TestEstimate_MinAudioFloorIsQuoted(t *testing.T) {
	t.Parallel()
	off := newRig(t)
	on := newRig(t, withSongwriting(fixedTargets{songwriting.Targets{MinDurationS: 300, TargetMinDurationS: 300, TargetMaxDurationS: 320}}))
	base := off.est(human, newSeedItem(lt.TrackLuna, 1)).Items[0].Takes[0]
	e := on.est(human, newSeedItem(lt.TrackLuna, 1))
	tk := e.Items[0].Takes[0]

	want := max(300, (base.ExpectedAudioS*7+9)/10)
	want = min(want, 330)
	if tk.MinAudioS != want {
		t.Fatalf("MinAudioS = %d, want %d (estimate without control %d s)", tk.MinAudioS, want, base.ExpectedAudioS)
	}
	if tk.ExpectedAudioS != max(base.ExpectedAudioS, want) {
		t.Errorf("ExpectedAudioS = %d, want max(%d, %d)", tk.ExpectedAudioS, base.ExpectedAudioS, want)
	}
	if tk.ExpectedAudioS < tk.MinAudioS {
		t.Errorf("quoted %d s, shorter than the forced minimum %d s", tk.ExpectedAudioS, tk.MinAudioS)
	}
	if tk.TimeoutS <= base.TimeoutS && tk.ExpectedAudioS > base.ExpectedAudioS {
		t.Errorf("timeout %d s not above the uncontrolled %d s although the quoted length grew", tk.TimeoutS, base.TimeoutS)
	}
	var warned bool
	for _, w := range on.items(e.ID)[0].Warnings {
		warned = warned || (w.Code == "min_audio_forced" && w.Severity == "warning" && strings.Contains(w.Message, fmt.Sprint(want)))
	}
	if !warned {
		t.Errorf("no min_audio_forced warning naming %d s: %+v", want, on.items(e.ID)[0].Warnings)
	}
	for _, w := range off.items(off.est(human, newSeedItem(lt.TrackLuna, 1)).ID)[0].Warnings {
		if w.Code == "min_audio_forced" {
			t.Errorf("warning without the control: %+v", w)
		}
	}
	if got := on.minAudioOf(e.ID); len(got) != 1 || got[0] != want {
		t.Errorf("items_json min_audio_s = %v, want [%d]", got, want)
	}
}

// Create freezes min_audio_s into jobs.mutation_json (the column the runner reads at submit).
func TestCreate_MinAudioInMutation(t *testing.T) {
	t.Parallel()
	r := newRig(t, withSongwriting(fixedTargets{songwriting.Defaults()}))
	e := r.est(human, newSeedItem(lt.TrackLuna, 1))
	c := r.mustCreate(human, e, spend.ViaHumanCreate)
	var mj string
	if err := r.st.R.QueryRow(`SELECT mutation_json FROM jobs WHERE id = ?`, c.JobIDs[0]).Scan(&mj); err != nil {
		t.Fatal(err)
	}
	var m struct {
		MinAudioS int `json:"min_audio_s"`
	}
	if err := json.Unmarshal([]byte(mj), &m); err != nil || m.MinAudioS < songwriting.DefaultMin || m.MinAudioS != e.Items[0].Takes[0].MinAudioS {
		t.Errorf("mutation_json = %s (%v), want min_audio_s = %d >= %d", mj, err, e.Items[0].Takes[0].MinAudioS, songwriting.DefaultMin)
	}
}
