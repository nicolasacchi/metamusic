package records_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
)

func sp(s string) *string { return &s }

func take(id, status string, lossy bool) *records.TakeInfo {
	return &records.TakeInfo{ID: id, Status: status, DurationS: 200, Lossy: lossy}
}

// TestResolveSlot_Table is C121.1: R01-R14, each Slot, pinned, pick -> Take, Mode, State, Warnings.
func TestResolveSlot_Table(t *testing.T) {
	t.Parallel()
	floating := records.Slot{ID: "sl", RecordID: "r", SongID: "s", Position: 1}
	pinnedTo := func(id string) records.Slot { s := floating; s.PinnedTakeID = sp(id); return s }
	retired := func(s records.Slot) records.Slot { s.SongRetired = true; return s }
	for _, c := range []struct {
		name     string
		slot     records.Slot
		pinned   *records.TakeInfo
		pick     records.PickInput
		pickTake *records.TakeInfo
		take     string // resolved take id, "" = none
		lossy    bool
		mode     records.SlotMode
		state    records.SlotState
		differs  bool
		warn     []string
	}{
		{"R01 floating, no pick", floating, nil, records.PickInput{Source: "none"}, nil, "", false, "floating", "empty", false, []string{"empty"}},
		{"R02 floating, newest pick", floating, nil, records.PickInput{TakeID: sp("A"), Source: "newest"}, take("A", "draft", false), "A", false, "floating", "ok", false, []string{"undecided_pick"}},
		{"R03 floating, pinned-source pick", floating, nil, records.PickInput{TakeID: sp("A"), Source: "pinned"}, take("A", "draft", false), "A", false, "floating", "ok", false, nil},
		{"R04 pinned P, pick A", pinnedTo("P"), take("P", "draft", false), records.PickInput{TakeID: sp("A"), Source: "ladder"}, take("A", "draft", false), "P", false, "pinned", "ok", true, nil},
		{"R05 pinned P discarded, pick A", pinnedTo("P"), take("P", "discarded", false), records.PickInput{TakeID: sp("A"), Source: "ladder"}, take("A", "draft", false), "P", false, "pinned", "pin_ineligible", true, []string{"pin_ineligible"}},
		{"R06 pinned P absent from the take table", pinnedTo("P"), nil, records.PickInput{TakeID: sp("A"), Source: "ladder"}, take("A", "draft", false), "P", false, "pinned", "pin_ineligible", true, []string{"pin_ineligible"}},
		{"R07 pinned equals the pick", pinnedTo("P"), take("P", "draft", false), records.PickInput{TakeID: sp("P"), Source: "pinned"}, take("P", "draft", false), "P", false, "pinned", "ok", false, nil},
		{"R08 pinned P discarded, no pick", pinnedTo("P"), take("P", "discarded", false), records.PickInput{Source: "none"}, nil, "P", false, "pinned", "pin_ineligible", true, []string{"pin_ineligible"}},
		{"R09 floating on a retired song", retired(floating), nil, records.PickInput{Source: "none"}, nil, "", false, "floating", "empty", false, []string{"empty", "retired_song"}},
		{"R10 lossy take carried", floating, nil, records.PickInput{TakeID: sp("L"), Source: "ladder"}, take("L", "draft", true), "L", true, "floating", "ok", false, nil},
		{"R11 pick on a to_relisten take", floating, nil, records.PickInput{TakeID: sp("A"), Source: "pinned"}, take("A", "to_relisten", false), "A", false, "floating", "ok", false, nil},
		{"R12 pinned P final", pinnedTo("P"), take("P", "final", false), records.PickInput{TakeID: sp("A"), Source: "stars"}, take("A", "draft", false), "P", false, "pinned", "ok", true, nil},
		{"R13 floating, stars pick", floating, nil, records.PickInput{TakeID: sp("A"), Source: "stars"}, take("A", "draft", false), "A", false, "floating", "ok", false, []string{"undecided_pick"}},
		{"R14 floating, ladder pick", floating, nil, records.PickInput{TakeID: sp("A"), Source: "ladder"}, take("A", "draft", false), "A", false, "floating", "ok", false, nil},
		{"pinned slot, undecided pick elsewhere", pinnedTo("P"), take("P", "draft", false), records.PickInput{TakeID: sp("A"), Source: "newest"}, take("A", "draft", false), "P", false, "pinned", "ok", true, nil},
		{"retired and pinned", retired(pinnedTo("P")), take("P", "draft", false), records.PickInput{Source: "none"}, nil, "P", false, "pinned", "ok", true, []string{"retired_song"}},
		{"pick names a take the table lacks", floating, nil, records.PickInput{TakeID: sp("A"), Source: "ladder"}, nil, "", false, "floating", "empty", false, []string{"empty"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := records.ResolveSlot(c.slot, c.pinned, c.pick, c.pickTake)
			id := ""
			if got.Take != nil {
				id = got.Take.ID
				if got.Take.Lossy != c.lossy {
					t.Errorf("lossy = %v", got.Take.Lossy)
				}
			}
			if id != c.take || got.Mode != c.mode || got.State != c.state || got.PinDiffers != c.differs {
				t.Errorf("take %q mode %s state %s differs %v; want %q %s %s %v", id, got.Mode, got.State, got.PinDiffers, c.take, c.mode, c.state, c.differs)
			}
			if !reflect.DeepEqual(got.Warnings, c.warn) {
				t.Errorf("warnings %v, want %v", got.Warnings, c.warn)
			}
			if got.Slot.ID != "sl" || !reflect.DeepEqual(got.Pick, c.pick) || got.SongRetired != c.slot.SongRetired {
				t.Errorf("slot, pick or retired flag not carried: %+v", got)
			}
		})
	}
}

// TestResolveSlot_Pure: the function neither mutates its inputs nor shares the warnings slice between calls.
func TestResolveSlot_Pure(t *testing.T) {
	t.Parallel()
	slot := records.Slot{ID: "sl", PinnedTakeID: sp("P")}
	pinned := take("P", "discarded", false)
	a := records.ResolveSlot(slot, pinned, records.PickInput{Source: "none"}, nil)
	a.Warnings[0] = "mutated"
	b := records.ResolveSlot(slot, pinned, records.PickInput{Source: "none"}, nil)
	if b.Warnings[0] != "pin_ineligible" || pinned.Status != "discarded" || *slot.PinnedTakeID != "P" {
		t.Errorf("state leaked between calls: %+v", b)
	}
}

func TestUndecided(t *testing.T) {
	t.Parallel()
	for src, want := range map[string]bool{"newest": true, "stars": true, "ladder": false, "pinned": false, "none": false, "": false} {
		if records.Undecided(src) != want {
			t.Errorf("Undecided(%q) = %v", src, !want)
		}
	}
}

func TestOptString_Decode(t *testing.T) {
	t.Parallel()
	type in struct {
		Side records.OptString `json:"side"`
	}
	for _, c := range []struct {
		body string
		want records.OptString
		bad  bool
	}{
		{`{}`, records.OptString{}, false},
		{`{"side":null}`, records.OptString{Set: true, Null: true}, false},
		{`{"side":"A"}`, records.OptString{Set: true, Value: "A"}, false},
		{`{"side":"D"}`, records.OptString{Set: true, Value: "D"}, false},
		{`{"side":"E"}`, records.OptString{}, true},
		{`{"side":"a"}`, records.OptString{}, true},
		{`{"side":""}`, records.OptString{}, true},
		{`{"side":1}`, records.OptString{}, true},
	} {
		var got in
		err := json.Unmarshal([]byte(c.body), &got)
		if c.bad {
			if err == nil {
				t.Errorf("%s: want an error", c.body)
			}
			continue
		}
		if err != nil || got.Side != c.want {
			t.Errorf("%s: %+v, %v; want %+v", c.body, got.Side, err, c.want)
		}
	}
}
