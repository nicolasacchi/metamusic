package jobs

import (
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// TestIngestBridge_EveryDatabaseCallMayFail fails each database call of one IngestTake in turn: the take
// is either complete (one track, its files) or absent (no row, no file left behind by the failed call), and
// the retry from the still-present uploads succeeds.
func TestIngestBridge_EveryDatabaseCallMayFail(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy: one ingest per database call")
	}
	src := newBridgeRig(t, true)
	job := src.seed(batchSpec{recipe: src.newRecipe("pop, dream"), draft: ""}).Jobs[0]
	jr, ar, files, out := src.capture(job, src.flac(11))
	fz := src.freeze()
	attempt := func(t *testing.T, ctl *failCtl, k int64) (*bridgeRig, string, error) {
		r := fz.thaw(t, ctl)
		b := attachBridge(t, r, true)
		ctl.arm(k)
		id, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, append([]upload.Received(nil), files...), out)
		ctl.disarm()
		return b, id, err
	}
	count := &failCtl{}
	if _, _, err := attempt(t, count, 0); err != nil {
		t.Fatalf("the clean call: %v", err)
	}
	total := count.calls()
	if total < 5 {
		t.Fatalf("the call made %d database calls", total)
	}
	stride := int64(1)
	if raceEnabled {
		stride = 2
	}
	for k := int64(1); k <= total; k += stride {
		t.Run(fmt.Sprintf("call %d", k), func(t *testing.T) {
			t.Parallel()
			b, id, err := attempt(t, &failCtl{}, k)
			tracks := b.count(`SELECT COUNT(*) FROM tracks`)
			if err == nil {
				if tracks != 1 || id == "" || len(b.tracksOnDisk()) != 1 {
					t.Fatalf("success with %d tracks, %v on disk", tracks, b.tracksOnDisk())
				}
				return
			}
			if tracks != 0 || len(b.tracksOnDisk()) != 0 || b.mastersOnDisk() != 0 {
				t.Fatalf("failure left %d tracks, %v takes, %d masters: %v", tracks, b.tracksOnDisk(), b.mastersOnDisk(), err)
			}
			id, err = b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out)
			if err != nil || id == "" || b.count(`SELECT COUNT(*) FROM tracks`) != 1 {
				t.Fatalf("retry: %q %v", id, err)
			}
		})
	}
}

var _ = provider.Contract
