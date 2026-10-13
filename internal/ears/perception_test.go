package ears_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/ears"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

func TestPerception_Keys(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	p, err := r.svc.Perception(context.Background(), lt.TrackCitta)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	// exact M2a key order
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for dec.More() {
		k, _ := dec.Token()
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"tier", "lufs_i", "true_peak_db", "lra_lu", "bandwidth_hz", "sections", "timed", "flags", "blind_spots"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if p.Tier != "A" || !slices.Equal(p.Flags, []string{"clipping", "internal_silence", "band_limited"}) {
		t.Errorf("tier %q flags %v", p.Tier, p.Flags)
	}
	if p.Sections == nil || p.Flags == nil || p.BlindSpots == nil {
		t.Error("lists must be empty, not nil")
	}
	// a track with no Tier A at all
	p0, err := r.svc.Perception(context.Background(), lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	if p0.Tier != "0" || len(p0.Flags) != 0 || !slices.Contains(p0.BlindSpots, "silence, clipping and token loops (Tier A not run yet)") {
		t.Errorf("tier-0 perception = %+v", p0)
	}
}

func TestPerception_MatchesM2aContext(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	ctx := context.Background()
	b := trackctx.New(r.lib, nil, nil, serve.NewSigner([]byte("0123456789abcdef0123456789abcdef"), func() time.Time { return r.now }), "https://mm.example.com/", func() time.Time { return r.now })
	for _, id := range r.fx.Tracks {
		bundle, err := b.Build(ctx, human, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		p, err := r.svc.Perception(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var gm, wm map[string]json.RawMessage
		if err := json.Unmarshal(got, &gm); err != nil {
			t.Fatal(err)
		}
		wantRaw, err := json.Marshal(bundle.Perception)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(wantRaw, &wm); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"lufs_i", "true_peak_db", "lra_lu", "bandwidth_hz", "sections", "timed"} {
			if diff := cmp.Diff(string(wm[k]), string(gm[k])); diff != "" {
				t.Errorf("%s: %s differs from the M2a context (-m2a +ears):\n%s", id, k, diff)
			}
		}
	}
}

func TestBlindSpots_EveryPayload(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	ctx := context.Background()
	// Luna: timed plan sections, semantic.npy, complete Tier A
	secs, err := r.lib.Sections(ctx, lt.TrackLuna)
	if err != nil || !secs.Timed {
		t.Fatalf("fixture sections: %+v %v", secs, err)
	}
	lunaFacts := analysis.BlindSpotFacts{TierA: true, HasSemantic: true, Timed: true, SectionSource: secs.Source, Confidence: secs.Confidence}
	cittaFacts := analysis.BlindSpotFacts{TierA: true}
	twinFacts := analysis.BlindSpotFacts{} // no Tier A, untimed

	check := func(name string, got []string, f analysis.BlindSpotFacts) {
		t.Helper()
		if diff := cmp.Diff(analysis.BlindSpots(f), got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", name, diff)
		}
	}
	for _, c := range []struct {
		id string
		f  analysis.BlindSpotFacts
	}{{lt.TrackLuna, lunaFacts}, {lt.TrackCitta, cittaFacts}, {lt.TrackTwinA, twinFacts}} {
		d, err := r.svc.Dossier(ctx, c.id)
		if err != nil {
			t.Fatal(err)
		}
		check("dossier "+c.id, d.BlindSpots, c.f)
		p, err := r.svc.Perception(ctx, c.id)
		if err != nil {
			t.Fatal(err)
		}
		check("perception "+c.id, p.BlindSpots, c.f)
	}
	sf := lunaFacts
	sf.Spectrogram = true
	v, err := r.svc.Spectrogram(ctx, human, lt.TrackLuna, false)
	if err != nil {
		t.Fatal(err)
	}
	check("spectrogram", v.BlindSpots, sf)

	// a comparison carries what holds for both takes: Tier A only when both have it, semantic when either has it,
	// timed only when both are
	c, err := r.svc.Compare(ctx, lt.TrackLuna, lt.TrackCitta)
	if err != nil {
		t.Fatal(err)
	}
	check("comparison", c.BlindSpots, analysis.BlindSpotFacts{TierA: true, HasSemantic: true})
	c, err = r.svc.Compare(ctx, lt.TrackLuna, lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	check("comparison with a tier-0 take", c.BlindSpots, analysis.BlindSpotFacts{HasSemantic: true})
	_ = ears.Dossier{}
}
