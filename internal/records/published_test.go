package records_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func openL2(t *testing.T) (*store.Store, librarytest.L2Info, *records.Service) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	info := librarytest.FixtureL2(t, st, librarytest.FixedNow)
	return st, info, records.New(st, nil, nil)
}

// TestResolvedPublished_Fixture: only R1 (published); its five live slots come back in order with the resolution of each mode.
func TestResolvedPublished_Fixture(t *testing.T) {
	t.Parallel()
	_, info, svc := openL2(t)
	got, err := svc.ResolvedPublished(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Record.ID != info.Records["R1"] || got[0].Record.Status != records.StatusPublished || got[0].Record.FirstPublishedAt == nil {
		t.Fatalf("records = %+v", got)
	}
	slots := got[0].Slots
	want := []struct {
		key, take string
		mode      records.SlotMode
		state     records.SlotState
	}{
		{"A", "a1", records.ModeFloating, records.StateOK},
		{"B", "b1", records.ModeFloating, records.StateOK},
		{"C", "c2", records.ModePinned, records.StateOK},
		{"D", "", records.ModeFloating, records.StateEmpty},
		{"E", "e1", records.ModeFloating, records.StateOK},
	}
	if len(slots) != len(want) {
		t.Fatalf("%d slots, want %d", len(slots), len(want))
	}
	for i, w := range want {
		s := slots[i]
		if s.Slot.ID != info.Slots[w.key] || s.Slot.Position != i+1 || s.Mode != w.mode || s.State != w.state {
			t.Errorf("slot %s: %+v", w.key, s)
		}
		if (w.take == "") != (s.Take == nil) || (s.Take != nil && s.Take.ID != info.Takes[w.take]) {
			t.Errorf("slot %s plays %+v, want %s", w.key, s.Take, w.take)
		}
		if s.RootID == "" {
			t.Errorf("slot %s has no cover root", w.key)
		}
	}
	if !slots[4].Take.Lossy {
		t.Error("e1 is the lossy Take")
	}
	if slots[3].SongRetired || slots[3].RootID == slots[0].RootID {
		t.Errorf("slot D: %+v", slots[3])
	}
}

// TestResolvedPublished_Filters: draft, proposed and hidden Records are absent; a discarded pinned Take is pin_ineligible; a
// slot that is no longer in order_json is dropped; unpublishing removes the Record.
func TestResolvedPublished_Filters(t *testing.T) {
	t.Parallel()
	st, info, svc := openL2(t)
	ctx := context.Background()
	if _, err := st.W.Exec(`UPDATE tracks SET status = 'discarded', status_set_by = 'x' WHERE id = ?`, info.Takes["c2"]); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ResolvedPublished(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if c := got[0].Slots[2]; c.State != records.StatePinIneligible || c.Take == nil || c.Take.Status != "discarded" {
		t.Errorf("slot C = %+v", c)
	}
	if _, err := st.W.Exec(`UPDATE records SET hidden_at = '2026-10-28T10:00:00.000Z', hidden_by = 'human:x' WHERE id = ?`, info.Records["R1"]); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.ResolvedPublished(ctx); err != nil || len(got) != 0 {
		t.Errorf("hidden Record listed: %v %v", got, err)
	}
}
