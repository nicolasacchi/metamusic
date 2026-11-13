package transform

import (
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
)

// TestCost_HifiStreamsItsFinish pins the memory model of master, widen and binaural after they moved to safety.FinishStream: they
// hold the input and bounded windows, never the output, so a long song fits the default cap. The production symptom was a 296 s Take
// refused by widen (1147 MiB) and binaural (1162 MiB) against 1024.
func TestCost_HifiStreamsItsFinish(t *testing.T) {
	t.Parallel()
	const rate, defaultCapMB = 48000, 1024
	for _, k := range []Kind{KindMaster, KindWiden, KindBinaural} {
		spec, ok := LookupKind(k)
		if !ok {
			t.Fatalf("%s is not registered", k)
		}
		ten := int64(600 * rate)
		out := ten
		if k == KindBinaural {
			out += 10 * rate // the longest room tail
		}
		u := UnitsIn{InFrames: ten, OutFrames: out, Rate: rate, Resolved: []byte(`{}`)}
		got := kindMemoryMB(spec, u)
		if got > defaultCapMB {
			t.Errorf("%s: a 10 min 48 kHz Take is estimated at %d MiB, over the %d MiB cap", k, got, defaultCapMB)
		}
		if floor := MemoryMB(ten, 0, 0); got < floor {
			t.Errorf("%s: estimate %d MiB is below the input alone (%d)", k, got, floor)
		}
		// the estimate does not grow with the output: doubling the output adds nothing
		u2 := u
		u2.OutFrames *= 2
		if g2 := kindMemoryMB(spec, u2); g2 != got {
			t.Errorf("%s: estimate grows with the output (%d -> %d MiB)", k, got, g2)
		}
	}
}

// TestCost_HifiDisk: the streamed hifi kinds keep the same scratch as glacier (raw spool, detector and limited signal: 40 bytes a
// frame) in the work dir.
func TestCost_HifiDisk(t *testing.T) {
	t.Parallel()
	out := int64(300 * 48000)
	want := DiskBytesNeeded(KindGlacier, out)
	for _, k := range []Kind{KindMaster, KindWiden, KindBinaural} {
		if got := DiskBytesNeeded(k, out); got != want {
			t.Errorf("%s disk %d, want the streamed-finish figure %d", k, got, want)
		}
	}
}

// TestEstimate_LongTakeFitsTheMemoryCap runs estimate for every kind on 360 s, 48 kHz stereo Takes at default params under the default
// 1024 MiB memory cap (the CPU and output limits are lifted: this is about memory). A 4-6 minute song is an ordinary input, and
// the kinds were refused for it (widen 1147 MiB and binaural 1162 MiB at 296 s, ensemble 1102 MiB at 272 s) because the estimate
// counted every buffer of an in-memory render. A kind that cannot stream its render is listed in tooLong with the reason; the test
// pins the list, so a kind that starts to fit must be taken off it and one that stops fitting fails.
func TestEstimate_LongTakeFitsTheMemoryCap(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *config.Config, _ *Options) {
		c.TransformMaxMemMB = 1024
		c.TransformMaxOutS = 7200
		c.TransformMaxCPUS = 1_000_000
	})
	r.unfrozen(func() { r.exec(`UPDATE tracks SET duration_s = 360 WHERE id IN (?, ?)`, r.tx("A"), r.tx("B")) })
	a, b := r.tx("A"), r.tx("B")
	reqs := map[Kind]Request{
		KindMaster:    req(KindMaster, fmt.Sprintf(`{"source_id":%q}`, a)),
		KindWiden:     req(KindWiden, fmt.Sprintf(`{"source_id":%q}`, a)),
		KindBinaural:  req(KindBinaural, fmt.Sprintf(`{"source_id":%q}`, a)),
		KindEnsemble:  req(KindEnsemble, fmt.Sprintf(`{"source_id":%q}`, a)),
		KindGlacier:   glacier(a),
		KindShepard:   req(KindShepard, fmt.Sprintf(`{"source_id":%q,"loop_s":2}`, a)),
		KindMorph:     r.morphAB(""),
		KindPolytempo: req(KindPolytempo, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q,"downbeat_a_s":0.4,"downbeat_b_s":0.2}`, a, b)),
	}
	for _, k := range RegisteredKinds() {
		q, ok := reqs[k]
		if !ok {
			continue // score_render and other kinds without a Take input
		}
		e, err := r.svc.Estimate(t.Context(), human, q)
		reason, known := tooLong[k]
		switch {
		case err != nil && !known:
			t.Errorf("%s: a 360 s Take is refused: %v", k, err)
		case err == nil && known:
			t.Errorf("%s: fits at %d MiB; take it off tooLong (%s)", k, e.MemoryMB, reason)
		case err != nil:
			pe := wantProblem(t, err, 422, ErrTransformTooLarge)
			if pe.Details["limit"] != "memory_mb" {
				t.Errorf("%s: refused by %v, not memory", k, pe.Details["limit"])
			}
		case e.MemoryMB > 1024:
			t.Errorf("%s: estimate %d MiB over the cap but accepted", k, e.MemoryMB)
		}
	}
}

// tooLong are the kinds whose render cannot be bounded by a window yet, with the reason.
var tooLong = map[Kind]string{}
